package http

import (
	"fmt"
	"mime"
	"net/http"
	"net/netip"
	"strings"

	"github.com/ccharon/echoip/useragent"
)

// cliProducts are the user agents that get the plain text response on /.
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

// acceptsMediaType reports whether the Accept header asks for mediaType.
// Clients list several types and add parameters, so each entry is parsed
// instead of comparing the header as a whole. A wildcard does not count as a
// match, which leaves the CLI response as the answer for clients that take
// anything.
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

// ipFromRequest returns the address to report for this request. Headers are
// read in the configured order and only the first entry of X-Forwarded-For is
// trusted, because a client may append to that header. customIP allows the
// address to be overridden with the ip query parameter.
//
// The result is unmapped, so an IPv4 address has one representation whether it
// arrived on its own or inside IPv6.
func ipFromRequest(headers []string, r *http.Request, customIP bool) (netip.Addr, error) {
	remoteIP := ""
	if customIP && r.URL != nil {
		if v := r.URL.Query().Get("ip"); v != "" {
			remoteIP = v
		}
	}
	if remoteIP == "" {
		for _, header := range headers {
			value := r.Header.Get(header)
			if http.CanonicalHeaderKey(header) == "X-Forwarded-For" {
				value = firstForwardedFor(value)
			}
			if value != "" {
				remoteIP = value
				break
			}
		}
	}
	if remoteIP == "" {
		addrPort, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil {
			return netip.Addr{}, err
		}
		return addrPort.Addr().Unmap(), nil
	}

	addr, err := netip.ParseAddr(remoteIP)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("could not parse IP: %s", remoteIP)
	}
	return addr.Unmap(), nil
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
