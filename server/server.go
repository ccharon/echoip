package server

import (
	"context"
	"embed"
	"html/template"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"time"

	"github.com/ccharon/echoip/iputil/geo"
)

//go:embed html
var templateFS embed.FS

// pageTemplate renders the browser page. Parsing at init means a broken
// template stops the binary rather than quietly dropping the page.
var pageTemplate = template.Must(template.ParseFS(templateFS, "html/*"))

// csp covers the page as it is rendered, which never changes at runtime.
var csp = contentSecurityPolicy()

const (
	jsonMediaType = "application/json"
	textMediaType = "text/plain"

	htmlContentType = "text/html; charset=utf-8"

	// The template rendered for browsers. The other files beside it are
	// included from it.
	indexTemplate = "index.html"
)

// Timeouts that keep a stalled or idle client from holding a connection.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 120 * time.Second

	// Time given to requests that are still running when a shutdown starts.
	shutdownTimeout = 10 * time.Second
)

// maxHeaderBytes caps the request line and the headers. The default is a
// megabyte per connection, and nothing this service answers needs more than a
// browser sends.
const maxHeaderBytes = 8 << 10

// Config holds the options that stay fixed while the server runs.
type Config struct {
	// IPHeaders are trusted for the remote address, in the order given.
	IPHeaders []string
	// TrustedProxies limits IPHeaders to requests from these networks. Empty
	// trusts every peer.
	TrustedProxies []netip.Prefix
	// LookupAddr resolves the hostname of an address. Nil leaves the hostname
	// out of the response.
	LookupAddr func(netip.Addr) (string, error)
	// Profile registers the cache and pprof handlers below /debug.
	Profile bool
}

type Server struct {
	cfg   Config
	cache *Cache
	geo   geo.Reader
}

func New(cfg Config, geoReader geo.Reader, cache *Cache) *Server {
	return &Server{cfg: cfg, geo: geoReader, cache: cache}
}

// hasCity and hasASN are checked per request, because the databases may be
// downloaded after the server starts.
func (s *Server) hasCity(*http.Request) bool { return s.geo.HasCity() }
func (s *Server) hasASN(*http.Request) bool  { return s.geo.HasASN() }

func (s *Server) Handler() http.Handler {
	r := NewRouter()

	r.Route("GET", "/health", s.healthHandler)

	r.Route("GET", "/", s.jsonHandler).Accept(jsonMediaType)
	r.Route("GET", "/json", s.jsonHandler)

	r.Route("GET", "/", s.ipHandler).MatcherFunc(cliMatcher)
	r.Route("GET", "/", s.ipHandler).Accept(textMediaType)
	r.Route("GET", "/ip", s.ipHandler)

	r.Route("GET", "/country", s.cliField(func(r Response) string { return r.Country })).MatcherFunc(s.hasCity)
	r.Route("GET", "/country-iso", s.cliField(func(r Response) string { return r.CountryISO })).MatcherFunc(s.hasCity)
	r.Route("GET", "/city", s.cliField(func(r Response) string { return r.City })).MatcherFunc(s.hasCity)
	r.Route("GET", "/coordinates", s.cliField(Response.Coordinates)).MatcherFunc(s.hasCity)
	r.Route("GET", "/asn", s.cliField(func(r Response) string { return r.ASN })).MatcherFunc(s.hasASN)
	r.Route("GET", "/asn-org", s.cliField(func(r Response) string { return r.ASNOrg })).MatcherFunc(s.hasASN)

	r.Route("GET", "/", s.browserHandler)

	if s.cfg.Profile {
		r.Route("POST", "/debug/cache/resize", s.cacheResizeHandler)
		r.Route("GET", "/debug/cache/", s.cacheHandler)
		r.Route("GET", "/debug/pprof/cmdline", wrapHandlerFunc(pprof.Cmdline))
		r.Route("GET", "/debug/pprof/profile", wrapHandlerFunc(pprof.Profile))
		r.Route("GET", "/debug/pprof/symbol", wrapHandlerFunc(pprof.Symbol))
		r.Route("GET", "/debug/pprof/trace", wrapHandlerFunc(pprof.Trace))
		r.RoutePrefix("GET", "/debug/pprof/", wrapHandlerFunc(pprof.Index))
	}

	return withSecurityHeaders(csp, r.Handler())
}

func withSecurityHeaders(csp string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		for name, value := range securityHeaders {
			header.Set(name, value)
		}
		header.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

// ListenAndServe serves until ctx is done, then gives requests that are still
// running up to shutdownTimeout to finish.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		MaxHeaderBytes:    maxHeaderBytes,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
