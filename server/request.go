package server

import (
	"fmt"
	"mime"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
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

func isCLI(r *http.Request) bool {
	return cliProducts[useragent.Parse(r.UserAgent()).Product]
}

// format is what a route answers in, successes and failures alike.
type format int

const (
	negotiated format = iota
	textFormat
	jsonFormat
	htmlFormat
)

// negotiateFormat picks the answer on / and for a path no route serves. An
// explicit choice of JSON or text wins, then a CLI client gets text and
// everything else the page.
func negotiateFormat(r *http.Request) format {
	switch preferredMediaType(r) {
	case jsonMediaType:
		return jsonFormat
	case textMediaType:
		return textFormat
	}
	if isCLI(r) {
		return textFormat
	}
	return htmlFormat
}

// negotiable are the media types the service answers with, in the order that
// breaks a tie between equal weights.
var negotiable = []string{jsonMediaType, textMediaType, htmlMediaType}

// preferredMediaType returns the type from negotiable that the Accept header
// weights highest, or nothing when it names none of them with a weight above
// zero. A wildcard does not count, so a client that takes anything falls
// through to the User-Agent.
func preferredMediaType(r *http.Request) string {
	weights := make(map[string]float64)
	for entry := range strings.SplitSeq(r.Header.Get("Accept"), ",") {
		parsed, params, err := mime.ParseMediaType(entry)
		if err != nil || !slices.Contains(negotiable, parsed) {
			continue
		}
		if _, seen := weights[parsed]; seen {
			continue
		}
		if w, ok := weight(params); ok {
			weights[parsed] = w
		}
	}

	best, bestWeight := "", 0.0
	for _, mediaType := range negotiable {
		if w := weights[mediaType]; w > bestWeight {
			best, bestWeight = mediaType, w
		}
	}
	return best
}

// weight reads the q parameter of an Accept entry, 1 when it is absent. A
// malformed weight is not ok, and the entry is ignored like a malformed type.
func weight(params map[string]string) (float64, bool) {
	q, ok := params["q"]
	if !ok {
		return 1, true
	}
	if !qvalue.MatchString(q) {
		return 0, false
	}
	w, err := strconv.ParseFloat(q, 64)
	return w, err == nil
}

// qvalue is the weight grammar of RFC 9110 section 12.4.2: 0 to 1 with at
// most three decimals.
var qvalue = regexp.MustCompile(`^(?:0(?:\.[0-9]{0,3})?|1(?:\.0{0,3})?)$`)

// ipFromRequest returns the address to report, unmapped so that an IPv4
// address has one representation. Only the first entry of X-Forwarded-For is
// trusted, because a client may append to that header.
func (s *Server) ipFromRequest(r *http.Request) (netip.Addr, *appError) {
	peer, peerErr := peerAddr(r)

	if v := r.URL.Query().Get("ip"); v != "" {
		return suppliedAddr(v)
	}

	if v := s.forwarded(r, peer); v != "" {
		return suppliedAddr(v)
	}

	if peerErr != nil {
		return netip.Addr{}, internal(peerErr)
	}
	return peer, nil
}

// forwarded returns the client address a trusted peer reported, as it was
// sent, or nothing when the peer is not trusted or set none of IPHeaders.
func (s *Server) forwarded(r *http.Request, peer netip.Addr) string {
	if !s.trusts(peer) {
		return ""
	}
	for _, header := range s.cfg.IPHeaders {
		value := r.Header.Get(header)
		if http.CanonicalHeaderKey(header) == "X-Forwarded-For" {
			value = firstForwardedFor(value)
		}
		if value != "" {
			return value
		}
	}
	return ""
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
// The rejected value is quoted before it is bounded, because quoting lengthens
// it. The zone is left out, since the caller chose it and it cannot be public.
func suppliedAddr(v string) (netip.Addr, *appError) {
	addr, ok := parseAddr(v)
	if !ok {
		return netip.Addr{}, newError(invalidAddress, clip(strconv.Quote(v))+" is not an IP address.")
	}
	if !iputil.Public(addr) {
		return netip.Addr{}, newError(notPublicAddress, fmt.Sprintf("%s is not a public address and is not looked up.", addr.WithZone("")))
	}
	return addr, nil
}

// parseAddr reads an address with or without a port, because a proxy may put
// the port into the header it sets. The bare form is tried first, so an
// unbracketed IPv6 address does not lose its last group to a supposed port.
func parseAddr(s string) (netip.Addr, bool) {
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.Unmap(), true
	}
	if addrPort, err := netip.ParseAddrPort(s); err == nil {
		return addrPort.Addr().Unmap(), true
	}
	return netip.Addr{}, false
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
