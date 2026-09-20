// Package maxmind downloads GeoLite2 databases from MaxMind.
package maxmind

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The GeoLite2 editions this package downloads.
const (
	EditionCity = "GeoLite2-City"
	EditionASN  = "GeoLite2-ASN"
)

// Overridden in tests.
var downloadURL = "https://download.maxmind.com/geoip/databases"

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

	defaultTimeout = 10 * time.Minute

	// Bounds the checksum body, which holds one hash and a file name.
	maxChecksumSize = 1 << 10

	// What the default client allows, restated because CheckRedirect replaces
	// that limit.
	maxRedirects = 10

	// Delay until the next attempt when a database is missing or a refresh
	// failed, so the server does not stay without data for a whole Interval.
	retryInterval = 15 * time.Minute
)

type Updater struct {
	// AccountID and LicenseKey authenticate the download.
	AccountID  string
	LicenseKey string
	// Databases maps a GeoLite2 edition ID to the file it is written to.
	Databases map[string]string
	Interval  time.Duration
	Timeout   time.Duration
}

// Enabled reports whether this updater has everything it needs to download.
func (u *Updater) Enabled() bool {
	return u.AccountID != "" && u.LicenseKey != "" && u.Interval > 0 && len(u.Databases) > 0
}

// Stale reports whether any database is missing or older than Interval.
func (u *Updater) Stale() bool {
	return u.oldest() >= u.Interval
}

// oldest returns the age of the database that was written longest ago. A
// missing database counts as infinitely old.
func (u *Updater) oldest() time.Duration {
	oldest := time.Duration(0)
	for _, path := range u.Databases {
		fi, err := os.Stat(path)
		if err != nil {
			return time.Duration(math.MaxInt64)
		}
		if age := time.Since(fi.ModTime()); age > oldest {
			oldest = age
		}
	}
	return oldest
}

// nextRefresh returns how long to wait, counted from the age of the databases
// so that a restart does not delay it.
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

// Run checks the databases every Interval until ctx is done, and calls
// onUpdate whenever a check brought new data. A failed check is retried after
// retryInterval.
func (u *Updater) Run(ctx context.Context, onUpdate func() error) {
	if u.Interval <= 0 {
		return
	}

	timer := time.NewTimer(u.nextRefresh())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		var next time.Duration
		changed, err := u.Update(ctx)
		switch {
		case err != nil:
			log.Printf("GeoIP update failed: %v", err)
			next = u.retryDelay()
		case !changed:
			next = u.nextRefresh()
		default:
			if err := onUpdate(); err != nil {
				log.Printf("Reloading GeoIP databases failed: %v", err)
				next = u.retryDelay()
			} else {
				log.Print("GeoIP databases updated")
				next = u.nextRefresh()
			}
		}
		timer.Reset(next)
	}
}

// retryDelay never exceeds Interval, so a short Interval keeps its pace.
func (u *Updater) retryDelay() time.Duration {
	return min(u.Interval, retryInterval)
}

// download replaces the file at path and reports whether it changed. The
// request is conditional, so MaxMind answers 304 with an empty body while the
// edition has not been rebuilt.
func (u *Updater) download(ctx context.Context, edition, path string) (bool, error) {
	timeout := u.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := u.request(ctx, edition, "tar.gz")
	if err != nil {
		return false, withoutURL(err)
	}

	// The modification time of the file on disk is the last time this edition
	// was confirmed current, which is what the condition compares against.
	if fi, err := os.Stat(path); err == nil {
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
	sum := sha256.New()
	tmp, err := extract(io.TeeReader(resp.Body, sum), edition+".mmdb", path)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp) }()

	// The checksum covers the whole archive, and extract stops at the entry it
	// wants.
	if _, err := io.Copy(sum, resp.Body); err != nil {
		return false, withoutURL(err)
	}

	if err := u.verify(ctx, edition, sum.Sum(nil)); err != nil {
		return false, err
	}

	return true, os.Rename(tmp, path)
}

// request builds an authenticated GET for one file of an edition. The
// credentials go in a header, so they stay out of the URL a log or an error
// might carry.
func (u *Updater) request(ctx context.Context, edition, suffix string) (*http.Request, error) {
	target := downloadURL + "/" + url.PathEscape(edition) + "/download?suffix=" + url.QueryEscape(suffix)

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
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
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
