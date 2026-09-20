package http

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
		addr, err := server.ipFromRequest(r)
		if err != nil {
			t.Fatal(err)
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
			addr, err := server.ipFromRequest(r)
			if err == nil {
				t.Fatalf("expected an error, got %s", addr)
			}
			if !strings.Contains(err.Error(), "not a public IP") {
				t.Errorf("unexpected error: %v", err)
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

		addr, err := (&Server{}).ipFromRequest(r)
		if err != nil {
			t.Fatalf("peer %s: %v", peer, err)
		}
		if want := netip.MustParseAddrPort(u.Host).Addr(); addr != want {
			t.Errorf("peer %s: expected %s, got %s", peer, want, addr)
		}
	}
}

func TestCLIMatcher(t *testing.T) {
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
		if got := cliMatcher(r); got != tt.out {
			t.Errorf("Expected %t, got %t for %q", tt.out, got, tt.in)
		}
	}
}

func TestAcceptsMediaType(t *testing.T) {
	tests := []struct {
		header string
		out    bool
	}{
		{"application/json", true},
		{"application/json, text/plain, */*", true},
		{"application/json; charset=utf-8", true},
		{"text/html, application/json;q=0.9", true},
		{"application/json;q=0", false},
		{"*/*", false},
		{"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", false},
		{"", false},
		{"application/jsonp", false},
		{"not a media type", false},
	}

	for _, tt := range tests {
		r := &http.Request{Header: http.Header{"Accept": []string{tt.header}}}
		if got := acceptsMediaType(r, "application/json"); got != tt.out {
			t.Errorf("Expected %t, got %t for %q", tt.out, got, tt.header)
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
		// No URL, so nothing reaches the ?ip= branch and the header decides.
		r := &http.Request{
			RemoteAddr: tt.peer,
			Header:     http.Header{"X-Real-Ip": []string{"1.3.3.7"}},
		}

		addr, err := server.ipFromRequest(r)
		if err != nil {
			t.Fatal(err)
		}
		if want := netip.MustParseAddr(tt.out); addr != want {
			t.Errorf("peer %s with %v: expected %s, got %s", tt.peer, tt.trusted, want, addr)
		}
	}
}
