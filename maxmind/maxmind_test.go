package maxmind

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
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

func TestExtract(t *testing.T) {
	data := archive(t, map[string]string{
		"GeoLite2-ASN_20240101/COPYRIGHT.txt":     "copyright",
		"GeoLite2-ASN_20240101/GeoLite2-ASN.mmdb": "database",
	})

	path := filepath.Join(t.TempDir(), "sub", "GeoLite2-ASN.mmdb")
	if err := extract(bytes.NewReader(data), "GeoLite2-ASN.mmdb", path); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "database"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	data := archive(t, map[string]string{"x/GeoLite2-ASN.mmdb": "database"})

	if err := extract(bytes.NewReader(data), "GeoLite2-ASN.mmdb", filepath.Join(dir, "GeoLite2-ASN.mmdb")); err != nil {
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

	err := extract(bytes.NewReader(data), "GeoLite2-ASN.mmdb", filepath.Join(t.TempDir(), "db.mmdb"))
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
		edition := r.URL.Query().Get("edition_id")
		gotQuery = append(gotQuery, edition+"|"+r.URL.Query().Get("license_key")+"|"+r.URL.Query().Get("suffix"))
		_, _ = w.Write(archive(t, map[string]string{
			edition + "_20240101/" + edition + ".mmdb": edition + " data",
		}))
	}))
	defer srv.Close()

	defer swapDownloadURL(srv.URL)()

	dir := t.TempDir()
	u := &Updater{
		LicenseKey: "secret",
		Interval:   time.Hour,
		Databases: map[string]string{
			"GeoLite2-City": filepath.Join(dir, "GeoLite2-City.mmdb"),
			"GeoLite2-ASN":  filepath.Join(dir, "GeoLite2-ASN.mmdb"),
		},
	}

	if err := u.Update(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Editions are requested in a stable order.
	want := []string{"GeoLite2-ASN|secret|tar.gz", "GeoLite2-City|secret|tar.gz"}
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
		LicenseKey: "secret",
		Interval:   time.Hour,
		Databases:  map[string]string{"GeoLite2-ASN": path},
	}

	err := u.Update(context.Background())
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
		LicenseKey: "secret",
		Interval:   time.Hour,
		Databases:  map[string]string{"GeoLite2-ASN": filepath.Join(t.TempDir(), "db.mmdb")},
	}

	err := u.Update(context.Background())
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
		{"complete", Updater{LicenseKey: "k", Interval: time.Hour, Databases: databases}, true},
		{"no key", Updater{Interval: time.Hour, Databases: databases}, false},
		{"no interval", Updater{LicenseKey: "k", Databases: databases}, false},
		{"no databases", Updater{LicenseKey: "k", Interval: time.Hour}, false},
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
		if attempts.Add(1) == 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		edition := r.URL.Query().Get("edition_id")
		_, _ = w.Write(archive(t, map[string]string{
			edition + "_20240101/" + edition + ".mmdb": edition + " data",
		}))
	}))
	defer srv.Close()

	defer swapDownloadURL(srv.URL)()

	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	u := &Updater{
		LicenseKey: "secret",
		Interval:   10 * time.Millisecond,
		Databases:  map[string]string{"GeoLite2-ASN": filepath.Join(t.TempDir(), "GeoLite2-ASN.mmdb")},
	}

	ctx, cancel := context.WithCancel(context.Background())
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
	err := extract(&buf, "GeoLite2-ASN.mmdb", path)
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
