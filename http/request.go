package http

import (
	"fmt"
	"net"
	"net/http"
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

// ipFromRequest returns the address to report for this request. Headers are
// read in the configured order and only the first entry of X-Forwarded-For is
// trusted, because a client may append to that header. customIP allows the
// address to be overridden with the ip query parameter.
func ipFromRequest(headers []string, r *http.Request, customIP bool) (net.IP, error) {
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
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return nil, err
		}
		remoteIP = host
	}
	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return nil, fmt.Errorf("could not parse IP: %s", remoteIP)
	}
	return ip, nil
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
