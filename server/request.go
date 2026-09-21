package server

import (
	"fmt"
	"mime"
	"net/http"
	"net/netip"
	"strings"

	"github.com/ccharon/echoip/iputil"
	"github.com/ccharon/echoip/useragent"
)

// cliProducts get the plain text response on /.
var cliProducts = map[string]bool{
	"curl":           true,
	"HTTPie":         true,
	"httpie-go":      true,
	"Wget":           true,
	"fetch libfetch": true,
	"Go":             true,
	"Go-http-client": true,
	"ddclient":       true,
	"Mikrotik":       true,
	"xh":             true,
}

func cliMatcher(r *http.Request) bool {
	return cliProducts[useragent.Parse(r.UserAgent()).Product]
}

// acceptsMediaType reports whether the Accept header asks for mediaType. A
// wildcard does not match, which leaves the text response for clients that
// take anything.
func acceptsMediaType(r *http.Request, mediaType string) bool {
	header := r.Header.Get("Accept")
	if header == "" {
		return false
	}

	for entry := range strings.SplitSeq(header, ",") {
		parsed, params, err := mime.ParseMediaType(entry)
		if err != nil || parsed != mediaType {
			continue
		}
		// q=0 states that the client does not want this type at all.
		if params["q"] == "0" {
			return false
		}
		return true
	}

	return false
}

// ipFromRequest returns the address to report, unmapped so that an IPv4
// address has one representation. Only the first entry of X-Forwarded-For is
// trusted, because a client may append to that header.
func (s *Server) ipFromRequest(r *http.Request) (netip.Addr, error) {
	peer, peerErr := peerAddr(r)

	if r.URL != nil {
		if v := r.URL.Query().Get("ip"); v != "" {
			return suppliedAddr(v)
		}
	}

	if s.trusts(peer) {
		for _, header := range s.cfg.IPHeaders {
			value := r.Header.Get(header)
			if http.CanonicalHeaderKey(header) == "X-Forwarded-For" {
				value = firstForwardedFor(value)
			}
			if value != "" {
				return suppliedAddr(value)
			}
		}
	}

	return peer, peerErr
}

// trusts reports whether the headers of this peer may be believed. Without
// TrustedProxies every peer is believed, which is what a service behind a
// proxy that always sets the header needs.
func (s *Server) trusts(peer netip.Addr) bool {
	if len(s.cfg.TrustedProxies) == 0 {
		return true
	}
	for _, prefix := range s.cfg.TrustedProxies {
		if prefix.Contains(peer) {
			return true
		}
	}
	return false
}

func peerAddr(r *http.Request) (netip.Addr, error) {
	addrPort, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, err
	}
	return addrPort.Addr().Unmap(), nil
}

// suppliedAddr reads an address the caller chose, from ?ip= or from a trusted
// header. Only a public address is accepted, so a caller cannot aim a lookup at
// the network the service runs in. The peer address is exempt, see ipFromRequest.
func suppliedAddr(v string) (netip.Addr, error) {
	addr, err := parseAddr(v)
	if err != nil {
		return netip.Addr{}, err
	}
	if !iputil.Public(addr) {
		return netip.Addr{}, fmt.Errorf("not a public IP: %s", addr)
	}
	return addr, nil
}

// parseAddr reads an address with or without a port, because a proxy may put
// the port into the header it sets. The bare form is tried first, so an
// unbracketed IPv6 address does not lose its last group to a supposed port.
// The rejected value is named, bounded, because the caller chose what to send.
func parseAddr(s string) (netip.Addr, error) {
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.Unmap(), nil
	}
	if addrPort, err := netip.ParseAddrPort(s); err == nil {
		return addrPort.Addr().Unmap(), nil
	}
	return netip.Addr{}, fmt.Errorf("could not parse IP: %s", clip(s))
}

func firstForwardedFor(v string) string {
	if sep := strings.Index(v, ","); sep != -1 {
		v = v[:sep]
	}
	return strings.TrimSpace(v)
}

func userAgentFromRequest(r *http.Request) *useragent.UserAgent {
	raw := r.UserAgent()
	if raw == "" {
		return nil
	}
	parsed := useragent.Parse(raw)
	return &parsed
}
