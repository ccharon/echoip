// Package maxmind downloads GeoLite2 databases from MaxMind.
package maxmind

import (
	"archive/tar"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The GeoLite2 editions this package downloads.
const (
	EditionCity = "GeoLite2-City"
	EditionASN  = "GeoLite2-ASN"
)

const defaultEndpoint = "https://download.maxmind.com/geoip/databases"

// Credentials and the signed URL a redirect leads to have no business on a
// plain connection, so a hop that leaves the scheme of the first request is
// refused.
var client = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if req.URL.Scheme != via[0].URL.Scheme {
			return fmt.Errorf("refusing redirect from %s to %s", via[0].URL.Scheme, req.URL.Scheme)
		}
		return nil
	},
}

const (
	// Bounds the extracted database. GeoLite2-City is around 70 MB.
	maxDatabaseSize = 1 << 29

	// Bounds the bytes read from the archive. It holds the database, which
	// gzip barely grows when it cannot shrink it, and a few small text files.
	maxArchiveSize = maxDatabaseSize + 1<<20

	defaultTimeout = 10 * time.Minute

	// Bounds the checksum body, which holds one hash and a file name.
	maxChecksumSize = 1 << 10

	// What the default client allows, restated because CheckRedirect replaces
	// that limit.
	maxRedirects = 10

	// Delay until the next attempt when a database is missing or a refresh
	// failed, so the server does not stay without data for a whole Interval.
	retryInterval = 30 * time.Minute

	// Retries before a failing check waits a whole Interval, to spare the quota.
	maxRetries = 2
)

// Updater keeps GeoLite2 databases on disk current.
type Updater struct {
	// AccountID and LicenseKey authenticate the download.
	AccountID  string
	LicenseKey string
	// Databases maps a GeoLite2 edition ID to the file it is written to.
	Databases map[string]string
	Interval  time.Duration
	// Timeout bounds one download. Zero means defaultTimeout.
	Timeout time.Duration

	// endpoint replaces defaultEndpoint in tests.
	endpoint string
	// archiveLimit replaces maxArchiveSize in tests.
	archiveLimit int64

	// next is the time of the next check in Unix nanoseconds, zero while idle.
	next atomic.Int64

	// failed records whether the last Update failed, so Run can tell a caller
	// that has just tried from one that has not.
	failed atomic.Bool

	// unusable holds the editions whose file is on disk but does not open.
	mu       sync.Mutex
	unusable map[string]bool
}

// Scheduled reports whether there are databases to check periodically.
func (u *Updater) Scheduled() bool {
	return u.Interval > 0 && len(u.Databases) > 0
}

// Enabled reports whether this updater has everything it needs to download.
func (u *Updater) Enabled() bool {
	return u.Scheduled() && u.AccountID != "" && u.LicenseKey != ""
}

// Stale reports whether any database is missing, unusable or older than
// Interval.
func (u *Updater) Stale() bool {
	return u.oldest() >= u.Interval
}

// Invalidate marks the file of an edition as unusable until it has been
// downloaded again. Its modification time says nothing about its content after
// a crash, so the next download leaves out the condition, which MaxMind would
// answer with 304 and leave the file broken for good.
func (u *Updater) Invalidate(edition string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.unusable == nil {
		u.unusable = make(map[string]bool)
	}
	u.unusable[edition] = true
}

func (u *Updater) isUnusable(edition string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.unusable[edition]
}

func (u *Updater) repaired(edition string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.unusable, edition)
}

// oldest returns the age of the database that was written longest ago. A
// missing or unusable database counts as infinitely old.
func (u *Updater) oldest() time.Duration {
	oldest := time.Duration(0)
	for edition, path := range u.Databases {
		fi, err := os.Stat(path)
		if err != nil || u.isUnusable(edition) {
			return time.Duration(math.MaxInt64)
		}
		if age := time.Since(fi.ModTime()); age > oldest {
			oldest = age
		}
	}
	return oldest
}

// nextRefresh counts from the age of the databases, so a restart does not
// delay it. The retryDelay floor guards against a lost modification time.
func (u *Updater) nextRefresh() time.Duration {
	if remaining := u.Interval - u.oldest(); remaining > 0 {
		return remaining
	}
	return u.retryDelay()
}

// Update fetches every configured database and reports whether any of them
// changed. A database that MaxMind has not rebuilt since the last check is
// left alone.
func (u *Updater) Update(ctx context.Context) (bool, error) {
	changed, err := u.update(ctx)
	u.failed.Store(err != nil)
	return changed, err
}

func (u *Updater) update(ctx context.Context) (bool, error) {
	changed := false
	for _, edition := range slices.Sorted(maps.Keys(u.Databases)) {
		updated, err := u.download(ctx, edition, u.Databases[edition])
		if err != nil {
			return changed, fmt.Errorf("downloading %s: %w", edition, err)
		}
		changed = changed || updated
	}
	return changed, nil
}

// Run checks the databases every Interval until ctx is done and calls onUpdate
// after new data. Stale databases are checked right away, unless the last
// Update failed: then that attempt counts as the first failed check, so a
// caller that has just tried does not try again at once.
func (u *Updater) Run(ctx context.Context, onUpdate func() error) {
	if !u.Scheduled() {
		return
	}

	failures := 0
	delay := u.nextRefresh()
	switch {
	case u.failed.Load():
		failures = 1
		delay = u.wait(failures)
	case u.Stale():
		delay = 0
	}
	timer := time.NewTimer(u.schedule(delay))
	defer timer.Stop()
	defer u.next.Store(0)

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		if u.refresh(ctx, onUpdate) {
			failures = 0
		} else {
			failures = afterFailure(failures)
		}
		timer.Reset(u.schedule(u.wait(failures)))
	}
}

// schedule publishes the time of the next check for NextCheck.
func (u *Updater) schedule(delay time.Duration) time.Duration {
	u.next.Store(time.Now().Add(delay).UnixNano())
	return delay
}

// NextCheck returns when Run checks next, the zero time while it is idle.
func (u *Updater) NextCheck() time.Time {
	next := u.next.Load()
	if next == 0 {
		return time.Time{}
	}
	return time.Unix(0, next)
}

// afterFailure counts a failed check and starts over after the whole Interval.
func afterFailure(failures int) int {
	if failures > maxRetries {
		return 1
	}
	return failures + 1
}

// wait returns the delay after the given number of failed checks in a row.
func (u *Updater) wait(failures int) time.Duration {
	switch {
	case failures == 0:
		return u.nextRefresh()
	case failures <= maxRetries:
		return u.retryDelay()
	default:
		return u.Interval
	}
}

// refresh runs one check and reports whether it succeeded.
func (u *Updater) refresh(ctx context.Context, onUpdate func() error) bool {
	changed, updateErr := u.Update(ctx)
	if updateErr != nil {
		slog.Error("GeoIP update failed", "error", updateErr)
	}

	// An edition that was replaced before a later one failed is loaded all the
	// same, so it does not wait on disk until a check succeeds as a whole.
	if changed {
		if err := onUpdate(); err != nil {
			slog.Error("reloading GeoIP databases failed", "error", err)
			return false
		}
		slog.Info("GeoIP databases updated")
	}

	return updateErr == nil
}

// retryDelay never exceeds Interval, so a short Interval keeps its pace.
func (u *Updater) retryDelay() time.Duration {
	return min(u.Interval, retryInterval)
}

// download replaces the file at path and reports whether it changed. The
// request is conditional, so MaxMind answers 304 with an empty body while the
// edition has not been rebuilt.
func (u *Updater) download(ctx context.Context, edition, path string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(u.Timeout, defaultTimeout))
	defer cancel()

	req, err := u.request(ctx, edition, "tar.gz")
	if err != nil {
		return false, withoutURL(err)
	}

	// The modification time of the file on disk is the last time this edition
	// was confirmed current, which is what the condition compares against. An
	// unusable file is fetched again whatever its age.
	if fi, err := os.Stat(path); err == nil && !u.isUnusable(edition) {
		req.Header.Set("If-Modified-Since", fi.ModTime().UTC().Format(http.TimeFormat))
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, withoutURL(err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return false, touch(path)
	case http.StatusOK:
	default:
		return false, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	// The database is put next to its destination and only moved into place
	// once the archive it came from matches the checksum MaxMind publishes for
	// it.
	body := &boundedReader{r: resp.Body, limit: cmp.Or(u.archiveLimit, maxArchiveSize)}
	sum := sha256.New()
	tmp, err := extract(io.TeeReader(body, sum), edition+".mmdb", path)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp) }()

	// The checksum covers the whole archive, and extract stops at the entry it
	// wants.
	if _, err := io.Copy(sum, body); err != nil {
		return false, withoutURL(err)
	}

	if err := u.verify(ctx, edition, sum.Sum(nil)); err != nil {
		return false, err
	}

	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	u.repaired(edition)

	// Without this the rename may not survive a crash. The new file is in
	// place either way.
	return true, syncDir(filepath.Dir(path))
}

// boundedReader fails once more than limit bytes have been read. Cutting the
// archive short instead would surface as a puzzling checksum mismatch.
type boundedReader struct {
	r     io.Reader
	limit int64
	read  int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	// One byte beyond the limit tells a larger body from one that ends there.
	if left := b.limit + 1 - b.read; int64(len(p)) > left {
		p = p[:left]
	}
	n, err := b.r.Read(p)
	b.read += int64(n)
	if b.read > b.limit {
		return n, fmt.Errorf("archive is larger than the %d byte limit", b.limit)
	}
	return n, err
}

// request builds an authenticated GET for one file of an edition. The
// credentials go in a header, so they stay out of the URL a log or an error
// might carry.
func (u *Updater) request(ctx context.Context, edition, suffix string) (*http.Request, error) {
	target := cmp.Or(u.endpoint, defaultEndpoint) + "/" + url.PathEscape(edition) + "/download?suffix=" + url.QueryEscape(suffix)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(u.AccountID, u.LicenseKey)

	return req, nil
}

// verify compares the archive against the checksum MaxMind publishes beside
// it.
func (u *Updater) verify(ctx context.Context, edition string, sum []byte) error {
	req, err := u.request(ctx, edition, "tar.gz.sha256")
	if err != nil {
		return withoutURL(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return withoutURL(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status for checksum: %s", resp.Status)
	}

	// The body reads "<hex>  <file name>".
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxChecksumSize))
	if err != nil {
		return withoutURL(err)
	}
	want, _, _ := strings.Cut(strings.TrimSpace(string(body)), " ")

	if got := hex.EncodeToString(sum); !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch: archive is %s, MaxMind published %s", got, want)
	}

	return nil
}

// touch records that the file is current as of now, so the next refresh is due
// a full interval later.
func touch(path string) error {
	now := time.Now()
	return os.Chtimes(path, now, now)
}

// withoutURL strips the request URL from an error. The URL is of no use to
// the reader and a redirect may have replaced it with a signed one.
func withoutURL(err error) error {
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		return urlErr.Err
	}
	return err
}

// extract writes the named database from the archive to a temporary file next
// to path and returns that file. The caller moves it into place or removes it.
func extract(r io.Reader, name, path string) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("%s not found in archive", name)
		}
		if err != nil {
			return "", err
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != name {
			continue
		}
		if header.Size > maxDatabaseSize {
			return "", fmt.Errorf("%s is %d bytes, larger than the %d byte limit", name, header.Size, maxDatabaseSize)
		}
		return writeTemp(path, io.LimitReader(tr, maxDatabaseSize))
	}
}

// writeTemp writes to a temporary file next to path, so a reader never opens a
// half written or unverified database.
func writeTemp(path string, r io.Reader) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	removeTemps(path)

	f, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()

	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	// The rename that follows must not put a file in place whose content is
	// still in the page cache, or a crash leaves a fresh but empty database.
	if err := fsync(f); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}

	return tmp, nil
}

// fsync is replaced in tests to see what is synced.
var fsync = (*os.File).Sync

// syncDir makes a rename in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return fsync(d)
}

// removeTemps removes what writeTemp left beside path when the process died
// during a download. It only touches names os.CreateTemp gives for that path,
// the base name, a dot and a random number.
func removeTemps(path string) {
	dir, prefix := filepath.Dir(path), filepath.Base(path)+"."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		random, ok := strings.CutPrefix(e.Name(), prefix)
		if ok && e.Type().IsRegular() && isDigits(random) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}
