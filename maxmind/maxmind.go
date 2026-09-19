// Package maxmind downloads GeoLite2 databases from MaxMind.
package maxmind

import (
	"archive/tar"
	"compress/gzip"
	"context"
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
	"time"
)

// The GeoLite2 editions this package downloads.
const (
	EditionCity = "GeoLite2-City"
	EditionASN  = "GeoLite2-ASN"
)

// Overridden in tests.
var downloadURL = "https://download.maxmind.com/app/geoip_download"

const (
	// Bounds the extracted database. GeoLite2-City is around 70 MB.
	maxDatabaseSize = 1 << 29

	defaultTimeout = 10 * time.Minute

	// Delay until the next attempt when a database is missing or a refresh
	// failed, so the server does not stay without data for a whole Interval.
	retryInterval = 15 * time.Minute
)

type Updater struct {
	LicenseKey string
	// Databases maps a GeoLite2 edition ID to the file it is written to.
	Databases map[string]string
	Interval  time.Duration
	Timeout   time.Duration
}

// Enabled reports whether this updater has everything it needs to download.
func (u *Updater) Enabled() bool {
	return u.LicenseKey != "" && u.Interval > 0 && len(u.Databases) > 0
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

// nextRefresh returns how long to wait for the next download. It counts from
// the age of the databases rather than from now, so a restart does not push
// the refresh a full Interval into the future.
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

	params := url.Values{
		"edition_id":  {edition},
		"license_key": {u.LicenseKey},
		"suffix":      {"tar.gz"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL+"?"+params.Encode(), nil)
	if err != nil {
		return false, err
	}

	// The modification time of the file on disk is the last time this edition
	// was confirmed current, which is what the condition compares against.
	if fi, err := os.Stat(path); err == nil {
		req.Header.Set("If-Modified-Since", fi.ModTime().UTC().Format(http.TimeFormat))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, withoutURL(err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return false, touch(path)
	case http.StatusOK:
		return true, extract(resp.Body, edition+".mmdb", path)
	default:
		return false, fmt.Errorf("unexpected status: %s", resp.Status)
	}
}

// touch records that the file is current as of now, so the next refresh is due
// a full interval later.
func touch(path string) error {
	now := time.Now()
	return os.Chtimes(path, now, now)
}

// withoutURL strips the request URL from an error, because it carries the
// license key.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

func extract(r io.Reader, name, path string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s not found in archive", name)
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != name {
			continue
		}
		if header.Size > maxDatabaseSize {
			return fmt.Errorf("%s is %d bytes, larger than the %d byte limit", name, header.Size, maxDatabaseSize)
		}
		return writeFile(path, io.LimitReader(tr, maxDatabaseSize))
	}
}

// writeFile writes to a temporary file next to path and renames it, so a
// reader never opens a half written database.
func writeFile(path string, r io.Reader) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	f, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()

	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
