package server

import (
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestIPFromRequest(t *testing.T) {
	tests := []struct {
		remoteAddr     string
		headerKey      string
		headerValue    string
		trustedHeaders []string
		out            string
	}{
		{"127.0.0.1:9999", "", "", nil, "127.0.0.1"},                                                                // No header given
		{"127.0.0.1:9999", "X-Real-IP", "1.3.3.7", nil, "127.0.0.1"},                                                // Trusted header is empty
		{"127.0.0.1:9999", "X-Real-IP", "1.3.3.7", []string{"X-Foo-Bar"}, "127.0.0.1"},                              // Trusted header does not match
		{"127.0.0.1:9999", "X-Real-IP", "1.3.3.7", []string{"X-Real-IP", "X-Forwarded-For"}, "1.3.3.7"},             // Trusted header matches
		{"127.0.0.1:9999", "X-Forwarded-For", "1.3.3.7", []string{"X-Real-IP", "X-Forwarded-For"}, "1.3.3.7"},       // Second trusted header matches
		{"127.0.0.1:9999", "X-Forwarded-For", "1.3.3.7,4.2.4.2", []string{"X-Forwarded-For"}, "1.3.3.7"},            // X-Forwarded-For with multiple entries (commas separator)
		{"127.0.0.1:9999", "X-Forwarded-For", "1.3.3.7, 4.2.4.2", []string{"X-Forwarded-For"}, "1.3.3.7"},           // X-Forwarded-For with multiple entries (space+comma separator)
		{"127.0.0.1:9999", "X-Forwarded-For", "", []string{"X-Forwarded-For"}, "127.0.0.1"},                         // Empty header
		{"127.0.0.1:9999?ip=1.2.3.4", "", "", nil, "1.2.3.4"},                                                       // passed in "ip" parameter
		{"127.0.0.1:9999?ip=::ffff:1.2.3.4", "", "", nil, "1.2.3.4"},                                                // IPv4 inside IPv6 is unmapped
		{"127.0.0.1:9999?ip=1.2.3.4", "X-Forwarded-For", "1.3.3.7,4.2.4.2", []string{"X-Forwarded-For"}, "1.2.3.4"}, // ip parameter wins over X-Forwarded-For with multiple entries
		{"127.0.0.1:9999?ip=1.2.3.4:53", "", "", nil, "1.2.3.4"},                                                    // port is dropped from the ip parameter
		{"127.0.0.1:9999", "X-Real-IP", "1.2.3.4:53", []string{"X-Real-IP"}, "1.2.3.4"},                             // port is dropped from a trusted header
		{"127.0.0.1:9999", "X-Real-IP", "[2606:4700:4700::1111]:53", []string{"X-Real-IP"}, "2606:4700:4700::1111"}, // bracketed IPv6 with a port
		{"127.0.0.1:9999", "X-Real-IP", "2606:4700:4700::1111", []string{"X-Real-IP"}, "2606:4700:4700::1111"},      // a bare IPv6 address keeps its last group
	}
	for _, tt := range tests {
		u, err := url.Parse("http://" + tt.remoteAddr)
		if err != nil {
			t.Fatal(err)
		}
		r := &http.Request{
			RemoteAddr: u.Host,
			Header:     http.Header{},
			URL:        u,
		}
		r.Header.Add(tt.headerKey, tt.headerValue)
		server := &Server{cfg: Config{IPHeaders: tt.trustedHeaders}}
		addr, e := server.ipFromRequest(r)
		if e != nil {
			t.Fatal(e)
		}
		if want := netip.MustParseAddr(tt.out); addr != want {
			t.Errorf("Expected %s, got %s", want, addr)
		}
	}
}

func TestIPFromRequestRefusesSuppliedPrivate(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		headerValue string
	}{
		{"ip parameter", "?ip=10.0.0.5", ""},
		{"ip parameter, mapped", "?ip=::ffff:192.168.1.1", ""},
		{"ip parameter, loopback", "?ip=127.0.0.1", ""},
		{"trusted header", "", "192.168.1.5"},
		{"trusted header, first entry", "", "172.16.0.1, 1.3.3.7"},
		{"ip parameter with a port", "?ip=10.0.0.5:53", ""},
		{"trusted header with a port", "", "192.168.1.5:53"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse("http://203.0.113.9:9999" + tt.query)
			if err != nil {
				t.Fatal(err)
			}
			r := &http.Request{RemoteAddr: u.Host, Header: http.Header{}, URL: u}
			if tt.headerValue != "" {
				r.Header.Set("X-Forwarded-For", tt.headerValue)
			}

			server := &Server{cfg: Config{IPHeaders: []string{"X-Forwarded-For"}}}
			addr, e := server.ipFromRequest(r)
			if e == nil {
				t.Fatalf("expected an error, got %s", addr)
			}
			if e.problem != notPublicAddress {
				t.Errorf("expected %s, got %s: %v", notPublicAddress.slug, e.problem.slug, e)
			}
		})
	}
}

// A peer on a local network is answered for its own address, which is what
// keeps the service usable behind a LAN and on localhost.
func TestIPFromRequestAllowsPrivatePeer(t *testing.T) {
	for _, peer := range []string{"127.0.0.1:9999", "10.1.2.3:9999", "[fd00::1]:9999"} {
		u, err := url.Parse("http://" + peer)
		if err != nil {
			t.Fatal(err)
		}
		r := &http.Request{RemoteAddr: u.Host, Header: http.Header{}, URL: u}

		addr, e := (&Server{}).ipFromRequest(r)
		if e != nil {
			t.Fatalf("peer %s: %v", peer, e)
		}
		if want := netip.MustParseAddrPort(u.Host).Addr(); addr != want {
			t.Errorf("peer %s: expected %s, got %s", peer, want, addr)
		}
	}
}

func TestIsCLI(t *testing.T) {
	browserUserAgent := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_8_4) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/30.0.1599.28 " +
		"Safari/537.36"
	tests := []struct {
		in  string
		out bool
	}{
		{"curl/7.26.0", true},
		{"Wget/1.13.4 (linux-gnu)", true},
		{"Wget", true},
		{"fetch libfetch/2.0", true},
		{"HTTPie/0.9.3", true},
		{"httpie-go/0.6.0", true},
		{"Go 1.1 package http", true},
		{"Go-http-client/1.1", true},
		{"Go-http-client/2.0", true},
		{"ddclient/3.8.3", true},
		{"Mikrotik/6.x Fetch", true},
		{browserUserAgent, false},
	}
	for _, tt := range tests {
		r := &http.Request{Header: http.Header{"User-Agent": []string{tt.in}}}
		if got := isCLI(r); got != tt.out {
			t.Errorf("Expected %t, got %t for %q", tt.out, got, tt.in)
		}
	}
}

func TestPreferredMediaType(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{"application/json", jsonMediaType},
		{"application/json, text/plain, */*", jsonMediaType},
		{"application/json; charset=utf-8", jsonMediaType},
		{"text/plain", textMediaType},
		{"text/plain, application/json", jsonMediaType},
		{"text/html, application/json;q=0.9", htmlMediaType},
		{"text/html;q=0.5, application/json", jsonMediaType},
		{"text/plain;q=0.9, application/json;q=0.8", textMediaType},
		{"application/json;q=0", ""},
		{"application/json;q=0.0", ""},
		{"application/json;q=0.000", ""},
		{"application/json;q=0.", ""},
		{"application/json;q=0.001", jsonMediaType},
		{"application/json;q=1", jsonMediaType},
		{"application/json;q=1.000", jsonMediaType},
		{"application/json;q=1.001", ""},
		{"application/json;q=0.0000", ""},
		{"application/json;q=-0", ""},
		{"application/json;q=-1", ""},
		{"application/json;q=NaN", ""},
		{"application/json;q=1e-3", ""},
		{"application/json;q=0x1p-2", ""},
		{"application/json;q=high, application/json", jsonMediaType},
		{"application/json;q=0, text/plain;q=0.1", textMediaType},
		{"*/*", ""},
		{"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", htmlMediaType},
		{"", ""},
		{"application/jsonp", ""},
		{"not a media type", ""},
	}

	for _, tt := range tests {
		r := &http.Request{Header: http.Header{"Accept": []string{tt.header}}}
		if got := preferredMediaType(r); got != tt.want {
			t.Errorf("preferredMediaType(%q) = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestTrustedProxies(t *testing.T) {
	tests := []struct {
		peer    string
		trusted []string
		out     string
	}{
		// Without a list every peer is believed.
		{"203.0.113.9:1234", nil, "1.3.3.7"},
		{"10.0.0.2:1234", []string{"10.0.0.0/8"}, "1.3.3.7"},
		{"10.0.0.2:1234", []string{"10.0.0.2/32"}, "1.3.3.7"},
		{"127.0.0.1:1234", []string{"127.0.0.1/32", "10.0.0.0/8"}, "1.3.3.7"},
		// A peer outside the list keeps its own address.
		{"203.0.113.9:1234", []string{"10.0.0.0/8"}, "203.0.113.9"},
		{"10.1.0.2:1234", []string{"10.0.0.2/32"}, "10.1.0.2"},
		{"[2001:db8::1]:1234", []string{"2001:db8::/32"}, "1.3.3.7"},
		{"[2001:db8::1]:1234", []string{"10.0.0.0/8"}, "2001:db8::1"},
	}

	for _, tt := range tests {
		var trusted []netip.Prefix
		for _, p := range tt.trusted {
			trusted = append(trusted, netip.MustParsePrefix(p))
		}

		server := &Server{cfg: Config{IPHeaders: []string{"X-Real-IP"}, TrustedProxies: trusted}}
		r := &http.Request{
			RemoteAddr: tt.peer,
			Header:     http.Header{"X-Real-Ip": []string{"1.3.3.7"}},
			URL:        &url.URL{Path: "/"},
		}

		addr, e := server.ipFromRequest(r)
		if e != nil {
			t.Fatal(e)
		}
		if want := netip.MustParseAddr(tt.out); addr != want {
			t.Errorf("peer %s with %v: expected %s, got %s", tt.peer, tt.trusted, want, addr)
		}
	}
}

// A value that is no address is told apart from an address that is not public,
// and the value is quoted so a newline in it stays visible.
func TestIPFromRequestRefusesInvalid(t *testing.T) {
	for _, v := range []string{"foo", "1.2.3", "10.0.0.1%0aforged"} {
		u, err := url.Parse("http://203.0.113.9:9999?ip=" + v)
		if err != nil {
			t.Fatal(err)
		}
		r := &http.Request{RemoteAddr: u.Host, Header: http.Header{}, URL: u}

		_, e := (&Server{}).ipFromRequest(r)
		if e == nil {
			t.Fatalf("%s: expected an error", v)
		}
		if e.problem != invalidAddress {
			t.Errorf("%s: expected %s, got %s", v, invalidAddress.slug, e.problem.slug)
		}
		if strings.Contains(e.detail, "\n") {
			t.Errorf("%s: detail carries a raw newline: %q", v, e.detail)
		}
	}
}

// Both details stay one bounded line, whatever the caller put into the value.
func TestSuppliedAddrDetailIsBounded(t *testing.T) {
	tests := []struct {
		v       string
		problem problem
	}{
		{"::%\n8.8.8.8\n", notPublicAddress},
		{"fe80::1%" + strings.Repeat("a", 3000), notPublicAddress},
		{strings.Repeat("\xff", 200), invalidAddress},
	}

	for _, tt := range tests {
		_, e := suppliedAddr(tt.v)
		if e == nil || e.problem != tt.problem {
			t.Fatalf("%q: expected %s, got %v", tt.v, tt.problem.slug, e)
		}
		if strings.Contains(e.detail, "\n") {
			t.Errorf("%q: detail spans lines: %q", tt.v, e.detail)
		}
		if len(e.detail) > maxValueLen+64 {
			t.Errorf("%q: detail is %d bytes", tt.v, len(e.detail))
		}
	}

	if _, e := suppliedAddr("fe80::1%eth0"); e.detail != "fe80::1 is not a public address and is not looked up." {
		t.Errorf("expected the address without its zone, got %q", e.detail)
	}
}
