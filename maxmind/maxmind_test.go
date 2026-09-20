package maxmind

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// archive builds a tar.gz laid out like a GeoLite2 download, where the
// database sits in a directory named after the edition and its build date.
func archive(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, content := range entries {
		header := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// editionArchive builds the archive the download endpoint would serve for an
// edition. It is deterministic, so its checksum is stable across requests.
func editionArchive(t *testing.T, edition string) []byte {
	t.Helper()
	return archive(t, map[string]string{
		edition + "_20240101/" + edition + ".mmdb": edition + " data",
	})
}

// requestedEdition reads the edition from the path, which is where the
// download endpoint carries it.
func requestedEdition(r *http.Request) string {
	return strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/download")
}

// isChecksum reports whether the request asks for the checksum rather than the
// archive.
func isChecksum(r *http.Request) bool {
	return r.URL.Query().Get("suffix") == "tar.gz.sha256"
}

func writeChecksum(w http.ResponseWriter, edition string, data []byte) {
	_, _ = fmt.Fprintf(w, "%x  %s_20240101.tar.gz\n", sha256.Sum256(data), edition)
}

// serveMaxmind answers like the download endpoint, with the archive and the
// checksum that belongs to it.
func serveMaxmind(t *testing.T) http.HandlerFunc {
	t.Helper()

	archives := map[string][]byte{
		EditionASN:  editionArchive(t, EditionASN),
		EditionCity: editionArchive(t, EditionCity),
	}

	return func(w http.ResponseWriter, r *http.Request) {
		edition := requestedEdition(r)
		data, ok := archives[edition]
		if !ok {
			http.Error(w, "unknown edition", http.StatusNotFound)
			return
		}
		if isChecksum(r) {
			writeChecksum(w, edition, data)
			return
		}
		_, _ = w.Write(data)
	}
}

func TestExtract(t *testing.T) {
	data := archive(t, map[string]string{
		"GeoLite2-ASN_20240101/COPYRIGHT.txt":     "copyright",
		"GeoLite2-ASN_20240101/GeoLite2-ASN.mmdb": "database",
	})

	path := filepath.Join(t.TempDir(), "sub", "GeoLite2-ASN.mmdb")
	tmp, err := extract(bytes.NewReader(data), "GeoLite2-ASN.mmdb", path)
	if err != nil {
		t.Fatal(err)
	}

	// extract stops short of putting the file in place.
	if _, err := os.Stat(path); err == nil {
		t.Error("expected the database to stay in its temporary file")
	}

	got, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if want := "database"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUpdateLeavesNoTempFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(serveMaxmind(t)))
	defer srv.Close()

	defer swapDownloadURL(srv.URL)()

	dir := t.TempDir()
	u := &Updater{
		AccountID:  "12345",
		LicenseKey: "secret",
		Interval:   time.Hour,
		Databases:  map[string]string{EditionASN: filepath.Join(dir, "GeoLite2-ASN.mmdb")},
	}

	if _, err := u.Update(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "GeoLite2-ASN.mmdb" {
		t.Errorf("unexpected directory contents: %v", entries)
	}
}

func TestExtractMissingDatabase(t *testing.T) {
	data := archive(t, map[string]string{"x/COPYRIGHT.txt": "copyright"})

	_, err := extract(bytes.NewReader(data), "GeoLite2-ASN.mmdb", filepath.Join(t.TempDir(), "db.mmdb"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found in archive") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUpdate(t *testing.T) {
	var gotQuery []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		edition := requestedEdition(r)
		data := editionArchive(t, edition)
		if isChecksum(r) {
			writeChecksum(w, edition, data)
			return
		}
		id, key, _ := r.BasicAuth()
		gotQuery = append(gotQuery, edition+"|"+id+":"+key+"|"+r.URL.Query().Get("suffix"))
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	defer swapDownloadURL(srv.URL)()

	dir := t.TempDir()
	u := &Updater{
		AccountID:  "12345",
		LicenseKey: "secret",
		Interval:   time.Hour,
		Databases: map[string]string{
			"GeoLite2-City": filepath.Join(dir, "GeoLite2-City.mmdb"),
			"GeoLite2-ASN":  filepath.Join(dir, "GeoLite2-ASN.mmdb"),
		},
	}

	changed, err := u.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected the databases to be reported as changed")
	}

	// Editions are requested in a stable order.
	want := []string{"GeoLite2-ASN|12345:secret|tar.gz", "GeoLite2-City|12345:secret|tar.gz"}
	if len(gotQuery) != len(want) {
		t.Fatalf("got %v, want %v", gotQuery, want)
	}
	for i := range want {
		if gotQuery[i] != want[i] {
			t.Errorf("request %d: got %q, want %q", i, gotQuery[i], want[i])
		}
	}

	for edition, path := range u.Databases {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := edition + " data"; string(got) != want {
			t.Errorf("%s: got %q, want %q", edition, got, want)
		}
	}
}

func TestUpdateKeepsExistingDatabaseOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	defer swapDownloadURL(srv.URL)()

	path := filepath.Join(t.TempDir(), "GeoLite2-ASN.mmdb")
	if err := os.WriteFile(path, []byte("old database"), 0o644); err != nil {
		t.Fatal(err)
	}

	u := &Updater{
		AccountID:  "12345",
		LicenseKey: "secret",
		Interval:   time.Hour,
		Databases:  map[string]string{"GeoLite2-ASN": path},
	}

	_, err := u.Update(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error leaks the license key: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "old database"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUpdateErrorHidesLicenseKey(t *testing.T) {
	// A closed server makes the transport fail, which is the case where the
	// request URL reaches the error message.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	defer swapDownloadURL(url)()

	u := &Updater{
		AccountID:  "12345",
		LicenseKey: "secret",
		Interval:   time.Hour,
		Databases:  map[string]string{"GeoLite2-ASN": filepath.Join(t.TempDir(), "db.mmdb")},
	}

	_, err := u.Update(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error leaks the license key: %v", err)
	}
}

func TestStale(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "fresh.mmdb")
	if err := os.WriteFile(fresh, []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}

	old := filepath.Join(dir, "old.mmdb")
	if err := os.WriteFile(old, []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	var tests = []struct {
		name      string
		databases map[string]string
		want      bool
	}{
		{"fresh", map[string]string{"a": fresh}, false},
		{"missing", map[string]string{"a": filepath.Join(dir, "absent.mmdb")}, true},
		{"expired", map[string]string{"a": old}, true},
		{"one expired", map[string]string{"a": fresh, "b": old}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := &Updater{Interval: 24 * time.Hour, Databases: tt.databases}
			if got := u.Stale(); got != tt.want {
				t.Errorf("got %t, want %t", got, tt.want)
			}
		})
	}
}

func TestEnabled(t *testing.T) {
	databases := map[string]string{"GeoLite2-ASN": "db.mmdb"}

	var tests = []struct {
		name string
		u    Updater
		want bool
	}{
		{"complete", Updater{AccountID: "1", LicenseKey: "k", Interval: time.Hour, Databases: databases}, true},
		{"no account", Updater{LicenseKey: "k", Interval: time.Hour, Databases: databases}, false},
		{"no key", Updater{AccountID: "1", Interval: time.Hour, Databases: databases}, false},
		{"no interval", Updater{AccountID: "1", LicenseKey: "k", Databases: databases}, false},
		{"no databases", Updater{AccountID: "1", LicenseKey: "k", Interval: time.Hour}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.u.Enabled(); got != tt.want {
				t.Errorf("got %t, want %t", got, tt.want)
			}
		})
	}
}

func swapDownloadURL(url string) func() {
	previous := downloadURL
	downloadURL = url
	return func() { downloadURL = previous }
}

func TestRetryDelay(t *testing.T) {
	tests := []struct {
		interval time.Duration
		want     time.Duration
	}{
		{336 * time.Hour, retryInterval},
		{time.Minute, time.Minute},
	}

	for _, tt := range tests {
		u := Updater{Interval: tt.interval}
		if got := u.retryDelay(); got != tt.want {
			t.Errorf("Expected %s for interval %s, got %s", tt.want, tt.interval, got)
		}
	}
}

func TestRunRetriesAfterFailedDownload(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		edition := requestedEdition(r)
		data := editionArchive(t, edition)
		if isChecksum(r) {
			writeChecksum(w, edition, data)
			return
		}
		if attempts.Add(1) == 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	defer swapDownloadURL(srv.URL)()

	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	u := &Updater{
		AccountID:  "12345",
		LicenseKey: "secret",
		Interval:   10 * time.Millisecond,
		Databases:  map[string]string{"GeoLite2-ASN": filepath.Join(t.TempDir(), "GeoLite2-ASN.mmdb")},
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	updated := make(chan struct{}, 1)
	go u.Run(ctx, func() error {
		select {
		case updated <- struct{}{}:
		default:
		}
		return nil
	})

	select {
	case <-updated:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a successful refresh")
	}

	if got := attempts.Load(); got < 2 {
		t.Errorf("expected a retry after the failed download, got %d attempts", got)
	}
}

func TestExtractRejectsOversizedDatabase(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// A header that claims more than the limit, without writing that much.
	if err := tw.WriteHeader(&tar.Header{
		Name:     "GeoLite2-ASN_20240101/GeoLite2-ASN.mmdb",
		Typeflag: tar.TypeReg,
		Size:     maxDatabaseSize + 1,
		Mode:     0o644,
	}); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()

	path := filepath.Join(t.TempDir(), "GeoLite2-ASN.mmdb")
	_, err := extract(&buf, "GeoLite2-ASN.mmdb", path)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("expected the error to name the limit, got %q", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("expected no database to be written")
	}
}

func TestNextRefreshCountsFromDatabaseAge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "GeoLite2-ASN.mmdb")
	u := &Updater{
		AccountID:  "12345",
		LicenseKey: "secret",
		Interval:   14 * 24 * time.Hour,
		Databases:  map[string]string{EditionASN: path},
	}

	// A missing database is due right away, bounded by the retry delay.
	if got := u.nextRefresh(); got != u.retryDelay() {
		t.Errorf("missing database: got %s, want %s", got, u.retryDelay())
	}

	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		age  time.Duration
		want time.Duration
	}{
		{0, 14 * 24 * time.Hour},
		{13 * 24 * time.Hour, 24 * time.Hour},
		{14 * 24 * time.Hour, retryInterval},
		{30 * 24 * time.Hour, retryInterval},
	}

	for _, tt := range tests {
		written := time.Now().Add(-tt.age)
		if err := os.Chtimes(path, written, written); err != nil {
			t.Fatal(err)
		}
		got := u.nextRefresh()
		// The age is measured against time.Now, so allow for the test running.
		if diff := got - tt.want; diff > time.Minute || diff < -time.Minute {
			t.Errorf("age %s: got %s, want %s", tt.age, got, tt.want)
		}
	}
}

func TestRunStopsWithoutInterval(t *testing.T) {
	u := &Updater{AccountID: "12345", LicenseKey: "secret", Databases: map[string]string{EditionASN: "unused"}}

	done := make(chan struct{})
	go func() {
		u.Run(context.Background(), func() error { return nil })
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return for an interval of zero")
	}
}

func TestRunRefreshesRepeatedly(t *testing.T) {
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		edition := requestedEdition(r)
		data := editionArchive(t, edition)
		if isChecksum(r) {
			writeChecksum(w, edition, data)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	defer swapDownloadURL(srv.URL)()

	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	u := &Updater{
		AccountID:  "12345",
		LicenseKey: "secret",
		Interval:   20 * time.Millisecond,
		Databases:  map[string]string{EditionASN: filepath.Join(t.TempDir(), "GeoLite2-ASN.mmdb")},
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reloads := make(chan struct{}, 8)
	go u.Run(ctx, func() error {
		select {
		case reloads <- struct{}{}:
		default:
		}
		return nil
	})

	// Three refreshes prove the timer is armed again after each one.
	for i := range 3 {
		select {
		case <-reloads:
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for refresh %d", i+1)
		}
	}

	if got := downloads.Load(); got < 3 {
		t.Errorf("expected at least 3 downloads, got %d", got)
	}
}

func TestUpdateSendsConditionalRequest(t *testing.T) {
	var gotCondition string
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		edition := requestedEdition(r)
		data := editionArchive(t, edition)
		if isChecksum(r) {
			writeChecksum(w, edition, data)
			return
		}
		if served.Add(1) > 1 {
			gotCondition = r.Header.Get("If-Modified-Since")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	defer swapDownloadURL(srv.URL)()

	path := filepath.Join(t.TempDir(), "GeoLite2-ASN.mmdb")
	u := &Updater{
		AccountID:  "12345",
		LicenseKey: "secret",
		Interval:   time.Hour,
		Databases:  map[string]string{EditionASN: path},
	}

	// The first request has nothing to compare against.
	changed, err := u.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected the first update to report a change")
	}

	written, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	changed, err = u.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("expected an unchanged database to report no change")
	}
	if want := written.ModTime().UTC().Format(http.TimeFormat); gotCondition != want {
		t.Errorf("If-Modified-Since: got %q, want %q", gotCondition, want)
	}

	// A 304 still marks the file as checked, so the next refresh is a full
	// interval away.
	checked, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !checked.ModTime().After(written.ModTime()) {
		t.Error("expected the modification time to move forward after a 304")
	}
	if got := u.nextRefresh(); got < 59*time.Minute {
		t.Errorf("expected a refresh about an interval away, got %s", got)
	}
}

func TestRunSkipsReloadWhenUnchanged(t *testing.T) {
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		edition := requestedEdition(r)
		data := editionArchive(t, edition)
		if isChecksum(r) {
			writeChecksum(w, edition, data)
			return
		}
		if served.Add(1) > 1 {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	defer swapDownloadURL(srv.URL)()

	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	u := &Updater{
		AccountID:  "12345",
		LicenseKey: "secret",
		Interval:   20 * time.Millisecond,
		Databases:  map[string]string{EditionASN: filepath.Join(t.TempDir(), "GeoLite2-ASN.mmdb")},
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var reloads atomic.Int32
	go u.Run(ctx, func() error {
		reloads.Add(1)
		return nil
	})

	// Wait for several checks, of which only the first brings data.
	deadline := time.After(10 * time.Second)
	for served.Load() < 4 {
		select {
		case <-deadline:
			t.Fatalf("timed out after %d requests", served.Load())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()

	if got := reloads.Load(); got != 1 {
		t.Errorf("expected exactly one reload, got %d", got)
	}
}

func TestUpdateRejectsWrongChecksum(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		edition := requestedEdition(r)
		if isChecksum(r) {
			// A checksum for a different archive than the one served.
			writeChecksum(w, edition, []byte("something else"))
			return
		}
		_, _ = w.Write(editionArchive(t, edition))
	}))
	defer srv.Close()

	defer swapDownloadURL(srv.URL)()

	dir := t.TempDir()
	path := filepath.Join(dir, "GeoLite2-ASN.mmdb")
	u := &Updater{
		AccountID:  "12345",
		LicenseKey: "secret",
		Interval:   time.Hour,
		Databases:  map[string]string{EditionASN: path},
	}

	changed, err := u.Update(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if changed {
		t.Error("expected no change to be reported")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("unexpected error: %v", err)
	}

	// Nothing of the rejected archive is left behind.
	if _, err := os.Stat(path); err == nil {
		t.Error("expected no database to be written")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected an empty directory, got %v", entries)
	}
}

func TestUpdateKeepsDatabaseOnWrongChecksum(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		edition := requestedEdition(r)
		if isChecksum(r) {
			writeChecksum(w, edition, []byte("something else"))
			return
		}
		_, _ = w.Write(editionArchive(t, edition))
	}))
	defer srv.Close()

	defer swapDownloadURL(srv.URL)()

	path := filepath.Join(t.TempDir(), "GeoLite2-ASN.mmdb")
	if err := os.WriteFile(path, []byte("old database"), 0o644); err != nil {
		t.Fatal(err)
	}

	u := &Updater{
		AccountID:  "12345",
		LicenseKey: "secret",
		Interval:   time.Hour,
		Databases:  map[string]string{EditionASN: path},
	}

	if _, err := u.Update(context.Background()); err == nil {
		t.Fatal("expected error")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "old database"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestChecksumErrorHidesLicenseKey(t *testing.T) {
	// A checksum request that cannot be made at all, so the error carries the
	// request URL unless it is stripped.
	defer swapDownloadURL("http://127.0.0.1:1")()

	u := &Updater{AccountID: "12345",
		LicenseKey: "super-secret", Interval: time.Hour}

	err := u.verify(context.Background(), EditionASN, []byte{1, 2, 3})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Errorf("error leaks the license key: %v", err)
	}
}

// The license key is a query parameter, so a redirect that changes the scheme
// would hand it to whoever answers.
func TestUpdateRefusesSchemeChange(t *testing.T) {
	// A working https target, so the refusal is about the scheme and not about
	// the certificate.
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	defer swapDownloadURL(redirect.URL)()

	u := &Updater{
		AccountID:  "12345",
		LicenseKey: "super-secret",
		Interval:   time.Hour,
		Databases:  map[string]string{EditionASN: filepath.Join(t.TempDir(), "db.mmdb")},
	}
	_, err := u.Update(context.Background())
	if err == nil {
		t.Fatal("expected the redirect to be refused")
	}
	if !strings.Contains(err.Error(), "refusing redirect") {
		t.Errorf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Errorf("error leaks the license key: %v", err)
	}
}

// A redirect that keeps the scheme is followed, so the check does not break an
// ordinary move of the endpoint.
func TestUpdateFollowsSameSchemeRedirect(t *testing.T) {
	archive := archive(t, map[string]string{"GeoLite2-ASN_20240101/GeoLite2-ASN.mmdb": "payload"})
	sum := sha256.Sum256(archive)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("suffix") == "tar.gz.sha256" {
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  GeoLite2-ASN.tar.gz"))
			return
		}
		_, _ = w.Write(archive)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"?"+r.URL.RawQuery, http.StatusFound)
	}))
	defer redirect.Close()
	defer swapDownloadURL(redirect.URL)()

	path := filepath.Join(t.TempDir(), "db.mmdb")
	u := &Updater{
		AccountID:  "12345",
		LicenseKey: "secret",
		Interval:   time.Hour,
		Databases:  map[string]string{EditionASN: path},
	}
	updated, err := u.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Error("expected the database to be written")
	}
}

// The credentials belong in a header, so no URL a log or an error carries can
// hold them.
func TestRequestUsesBasicAuth(t *testing.T) {
	u := &Updater{AccountID: "12345", LicenseKey: "secret"}

	req, err := u.request(context.Background(), EditionASN, "tar.gz")
	if err != nil {
		t.Fatal(err)
	}

	id, key, ok := req.BasicAuth()
	if !ok {
		t.Fatal("expected basic auth to be set")
	}
	if id != "12345" || key != "secret" {
		t.Errorf("got %q:%q, want 12345:secret", id, key)
	}
	if got := req.URL.String(); !strings.HasSuffix(got, "/GeoLite2-ASN/download?suffix=tar.gz") {
		t.Errorf("unexpected URL: %s", got)
	}
	if strings.Contains(req.URL.String(), "secret") || strings.Contains(req.URL.String(), "12345") {
		t.Errorf("credentials leak into the URL: %s", req.URL)
	}
}
