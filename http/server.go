package http

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"path/filepath"
	"time"

	"github.com/ccharon/echoip/iputil/geo"
)

const (
	jsonMediaType = "application/json"
	textMediaType = "text/plain"
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

// Config holds the options that stay fixed while the server runs.
type Config struct {
	// TemplateDir holds the templates for the browser page. An empty path
	// serves the CLI response on / instead.
	TemplateDir string
	// IPHeaders are trusted for the remote address, in the order given.
	IPHeaders []string
	// LookupAddr resolves the hostname of an address. Nil leaves the hostname
	// out of the response.
	LookupAddr func(netip.Addr) (string, error)
	// LookupPort reports whether a port is reachable. Nil disables /port.
	LookupPort func(netip.Addr, uint16) error
	// Profile registers the cache and pprof handlers below /debug.
	Profile bool
}

type Server struct {
	cfg      Config
	cache    *Cache
	geo      geo.Reader
	template *template.Template
}

// New builds the server. Templates are parsed once, and a template that fails
// to parse disables the browser page rather than the whole server.
func New(cfg Config, geoReader geo.Reader, cache *Cache) *Server {
	s := &Server{cfg: cfg, geo: geoReader, cache: cache}

	if cfg.TemplateDir != "" {
		t, err := template.ParseGlob(filepath.Join(cfg.TemplateDir, "*"))
		if err != nil {
			log.Printf("Browser page is disabled: %v", err)
		} else {
			s.template = t
		}
	}

	return s
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

	if s.template != nil {
		r.Route("GET", "/", s.browserHandler)
	}

	if s.cfg.LookupPort != nil {
		r.RoutePrefix("GET", "/port/", s.portHandler)
	}

	if s.cfg.Profile {
		r.Route("POST", "/debug/cache/resize", s.cacheResizeHandler)
		r.Route("GET", "/debug/cache/", s.cacheHandler)
		r.Route("GET", "/debug/pprof/cmdline", wrapHandlerFunc(pprof.Cmdline))
		r.Route("GET", "/debug/pprof/profile", wrapHandlerFunc(pprof.Profile))
		r.Route("GET", "/debug/pprof/symbol", wrapHandlerFunc(pprof.Symbol))
		r.Route("GET", "/debug/pprof/trace", wrapHandlerFunc(pprof.Trace))
		r.RoutePrefix("GET", "/debug/pprof/", wrapHandlerFunc(pprof.Index))
	}

	return r.Handler()
}

// ListenAndServe serves until ctx is done, then gives requests that are still
// running up to shutdownTimeout to finish.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
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
