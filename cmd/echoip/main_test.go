package main

import (
	"errors"
	"flag"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ccharon/echoip/iputil/geo"
	"github.com/ccharon/echoip/maxmind"
)

func TestParseFlagsDefaults(t *testing.T) {
	opts, err := parseFlags(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	if opts.listen != ":8080" {
		t.Errorf("listen = %q, want %q", opts.listen, ":8080")
	}
	if opts.updateHours != 24 {
		t.Errorf("updateHours = %d, want 24", opts.updateHours)
	}
	if got := opts.updateInterval(); got != 24*time.Hour {
		t.Errorf("updateInterval() = %s, want %s", got, 24*time.Hour)
	}
	if opts.cacheSize != 0 || opts.reverseLookup || opts.profile || opts.showVersion {
		t.Errorf("expected the remaining options to be off: %+v", opts)
	}
	if opts.cityFile != "" || opts.asnFile != "" {
		t.Error("expected no database paths")
	}
}

func TestParseFlags(t *testing.T) {
	opts, err := parseFlags([]string{
		"-c", "city.mmdb", "-a", "asn.mmdb", "-l", "127.0.0.1:9000",
		"-u", "6", "-C", "500", "-r", "-P",
		"-H", "X-Real-IP", "-H", "X-Forwarded-For",
		"-T", "10.0.0.0/8", "-T", "192.168.1.1",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"X-Real-IP", "X-Forwarded-For"}; !slices.Equal(opts.headers, want) {
		t.Errorf("headers = %v, want %v", opts.headers, want)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("192.168.1.1/32"),
	}
	if !slices.Equal(opts.trustedProxies, want) {
		t.Errorf("trustedProxies = %v, want %v", opts.trustedProxies, want)
	}
	if opts.updateInterval() != 6*time.Hour || opts.cacheSize != 500 {
		t.Errorf("unexpected options: %+v", opts)
	}
	if !opts.reverseLookup || !opts.profile {
		t.Errorf("expected the switches to be on: %+v", opts)
	}
}

func TestParseFlagsErrors(t *testing.T) {
	tests := [][]string{
		{"-T", "not-an-address"},
		{"-T", "10.0.0.0/64"},
		{"-u", "forever"},
		{"-u", "24h"},
		{"-u", "-1"},
		{"-nosuchflag"},
		{"leftover-argument"},
	}

	for _, args := range tests {
		if _, err := parseFlags(args, io.Discard); err == nil {
			t.Errorf("expected an error for %v", args)
		}
	}
}

func TestParseFlagsHelp(t *testing.T) {
	_, err := parseFlags([]string{"-h"}, io.Discard)
	if !errors.Is(err, flag.ErrHelp) {
		t.Errorf("expected flag.ErrHelp, got %v", err)
	}
}

func TestParsePrefix(t *testing.T) {
	tests := []struct {
		in  string
		out string
	}{
		{"10.0.0.0/8", "10.0.0.0/8"},
		// A host address covers itself.
		{"192.168.1.1", "192.168.1.1/32"},
		{"::1", "::1/128"},
		{"2001:db8::/32", "2001:db8::/32"},
		{"192.168.1.128/25", "192.168.1.128/25"},
		{"0.0.0.0/0", "0.0.0.0/0"},
		// A single address is unmapped, so it matches the address the server
		// reads from a request.
		{"::ffff:10.0.0.5", "10.0.0.5/32"},
		// PrefixFrom drops the zone, which is what makes this match.
		{"fe80::1%eth0", "fe80::1/128"},
	}

	for _, tt := range tests {
		got, err := parsePrefix(tt.in)
		if err != nil {
			t.Fatalf("%s: %v", tt.in, err)
		}
		if got.String() != tt.out {
			t.Errorf("parsePrefix(%s) = %s, want %s", tt.in, got, tt.out)
		}
	}

	// Host bits, a dotted netmask and a mapped prefix are errors.
	errors := []string{
		"", "localhost", "10.0.0.0/33", "300.1.1.1",
		"10.0.0.0/255.0.0.0",
		"10.1.2.3/8",
		"192.168.1.1/24",
		"::ffff:192.168.0.0/120",
	}
	for _, in := range errors {
		if got, err := parsePrefix(in); err == nil {
			t.Errorf("expected an error for %q, got %s", in, got)
		}
	}
}

func TestEditions(t *testing.T) {
	tests := []struct {
		city, asn string
		want      map[string]string
	}{
		{"", "", map[string]string{}},
		{"city.mmdb", "", map[string]string{maxmind.EditionCity: "city.mmdb"}},
		{"", "asn.mmdb", map[string]string{maxmind.EditionASN: "asn.mmdb"}},
		{"c", "a", map[string]string{maxmind.EditionCity: "c", maxmind.EditionASN: "a"}},
	}

	for _, tt := range tests {
		got := editions(tt.city, tt.asn)
		if len(got) != len(tt.want) {
			t.Fatalf("editions(%q, %q) = %v, want %v", tt.city, tt.asn, got, tt.want)
		}
		for edition, path := range tt.want {
			if got[edition] != path {
				t.Errorf("editions(%q, %q)[%s] = %q, want %q", tt.city, tt.asn, edition, got[edition], path)
			}
		}
	}
}

func TestServerConfig(t *testing.T) {
	cfg := serverConfig(&options{
		headers:        []string{"X-Real-IP"},
		trustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		reverseLookup:  true,
		profile:        true,
		cityFile:       "data/GeoLite2-City.mmdb",
	})

	if cfg.LookupAddr == nil {
		t.Error("expected the reverse lookup to be set")
	}
	if !cfg.Profile {
		t.Error("expected profiling to be on")
	}
	if len(cfg.TrustedProxies) != 1 {
		t.Errorf("TrustedProxies = %v", cfg.TrustedProxies)
	}
	if !cfg.City || cfg.ASN {
		t.Errorf("City = %t, ASN = %t, want only the configured city database", cfg.City, cfg.ASN)
	}

	if cfg = serverConfig(&options{}); cfg.LookupAddr != nil {
		t.Error("expected no reverse lookup")
	}
}

func TestPrefixList(t *testing.T) {
	got := prefixList([]netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("2001:db8::/32"),
	})
	if want := "10.0.0.0/8, 2001:db8::/32"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := prefixList(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestMissingCredential(t *testing.T) {
	databases := map[string]string{maxmind.EditionASN: "asn.mmdb"}

	tests := []struct {
		name string
		u    *maxmind.Updater
		want string
	}{
		{"complete", &maxmind.Updater{AccountID: "1", LicenseKey: "k", Interval: time.Hour, Databases: databases}, ""},
		{"no account", &maxmind.Updater{LicenseKey: "k", Interval: time.Hour, Databases: databases}, accountIDEnv},
		{"no key", &maxmind.Updater{AccountID: "1", Interval: time.Hour, Databases: databases}, licenseKeyEnv},
		{"account is reported first", &maxmind.Updater{Interval: time.Hour, Databases: databases}, accountIDEnv},
		// Without an interval nothing is downloaded, so nothing is needed.
		{"checking disabled", &maxmind.Updater{Databases: databases}, ""},
		{"no databases", &maxmind.Updater{Interval: time.Hour}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := missingCredential(tt.u); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

type fakeDatabase struct {
	city, asn bool
	reloadErr error
	reloads   int
}

func (f *fakeDatabase) CityLoaded() bool { return f.city }
func (f *fakeDatabase) ASNLoaded() bool  { return f.asn }
func (f *fakeDatabase) Reload() error {
	f.reloads++
	return f.reloadErr
}

// Only a configured database that did not open holds up the start.
func TestUnloaded(t *testing.T) {
	both := editions("city.mmdb", "asn.mmdb")

	tests := []struct {
		name      string
		db        *fakeDatabase
		databases map[string]string
		want      []string
	}{
		{"all loaded", &fakeDatabase{city: true, asn: true}, both, nil},
		{"city missing", &fakeDatabase{asn: true}, both, []string{maxmind.EditionCity}},
		{"both missing", &fakeDatabase{}, both, []string{maxmind.EditionASN, maxmind.EditionCity}},
		{"not configured", &fakeDatabase{}, editions("", "asn.mmdb"), []string{maxmind.EditionASN}},
		{"nothing configured", &fakeDatabase{}, editions("", ""), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unloaded(tt.db, tt.databases); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// A file that is there but holds no database counts as unloaded, so it is
// fetched again rather than kept by its modification time.
func TestUnloadedBrokenFile(t *testing.T) {
	city := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	if err := os.WriteFile(city, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := geo.Open(city, "")
	if err == nil {
		t.Fatal("expected the empty file to fail to open")
	}
	if got, want := unloaded(db, editions(city, "")), []string{maxmind.EditionCity}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

type fakeCache struct{ clears int }

func (f *fakeCache) Clear() { f.clears++ }

// Reload replaces the databases it can open even when another fails, so the
// cache has to go in both cases.
func TestReloaderClearsCache(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"success", nil},
		{"partial", errors.New("asn missing")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeDatabase{reloadErr: tt.err}
			cache := &fakeCache{}

			if err := reloader(db, cache)(); !errors.Is(err, tt.err) {
				t.Errorf("got error %v, want %v", err, tt.err)
			}
			if db.reloads != 1 || cache.clears != 1 {
				t.Errorf("got %d reloads and %d clears, want one each", db.reloads, cache.clears)
			}
		})
	}
}
