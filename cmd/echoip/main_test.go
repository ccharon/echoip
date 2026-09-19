package main

import (
	"errors"
	"flag"
	"io"
	"log"
	"net/netip"
	"os"
	"slices"
	"testing"
	"time"

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
	if opts.updateInterval != 24*time.Hour {
		t.Errorf("updateInterval = %s, want %s", opts.updateInterval, 24*time.Hour)
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
		"-u", "6h", "-C", "500", "-r", "-P",
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
	if opts.updateInterval != 6*time.Hour || opts.cacheSize != 500 {
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
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	cfg := serverConfig(&options{
		headers:        []string{"X-Real-IP"},
		trustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		reverseLookup:  true,
		profile:        true,
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
