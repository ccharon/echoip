// Package server answers HTTP requests with the address of the caller and what
// the GeoIP databases know about it.
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
	htmlMediaType = "text/html"

	problemMediaType = "application/problem+json"

	textContentType = "text/plain; charset=utf-8"
	htmlContentType = "text/html; charset=utf-8"

	// The templates rendered for browsers. The other files beside them are
	// included from them.
	indexTemplate = "index.html"
	errorTemplate = "error.html"
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
// browser sends. net/http reads up to 4 KiB beyond this for its buffer, so a
// request is refused at 12 KiB.
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
	LookupAddr func(context.Context, netip.Addr) (string, error)
	// Profile registers the cache and pprof handlers below /debug.
	Profile bool
	// City and ASN register the endpoints that need that database. They
	// answer 503 until it is loaded.
	City bool
	ASN  bool
}

// GeoReader looks up GeoIP records. The Has methods report which lookups the
// databases currently loaded can answer, the Built methods when MaxMind built
// them.
type GeoReader interface {
	City(netip.Addr) (geo.City, error)
	ASN(netip.Addr) (geo.ASN, error)
	HasCity() bool
	HasASN() bool
	CityBuilt() time.Time
	ASNBuilt() time.Time
}

// Server serves the address and GeoIP data of the caller.
type Server struct {
	cfg   Config
	cache *Cache
	geo   GeoReader
}

// New returns a server that looks up addresses in geoReader and keeps the
// answers in cache.
func New(cfg Config, geoReader GeoReader, cache *Cache) *Server {
	return &Server{cfg: cfg, geo: geoReader, cache: cache}
}

// Handler returns the routes with the security and cache headers every
// response carries.
func (s *Server) Handler() http.Handler {
	r := newRouter()

	r.Route(http.MethodGet, "/", s.rootHandler)
	r.Route(http.MethodGet, "/health", s.healthHandler).Format(jsonFormat)
	r.Route(http.MethodGet, "/json", s.jsonHandler).Format(jsonFormat)
	r.Route(http.MethodGet, "/ip", s.ipHandler).Format(textFormat)

	if s.cfg.City {
		for _, ep := range cityEndpoints {
			r.Route(http.MethodGet, "/"+ep.path, s.cityField(ep.field)).Format(textFormat)
		}
	}
	if s.cfg.ASN {
		for _, ep := range asnEndpoints {
			r.Route(http.MethodGet, "/"+ep.path, s.asnField(ep.field)).Format(textFormat)
		}
	}

	if s.cfg.Profile {
		r.Route(http.MethodPost, "/debug/cache/resize", s.cacheResizeHandler).Format(jsonFormat)
		r.Route(http.MethodGet, "/debug/cache/", s.cacheHandler).Format(jsonFormat)
		r.Route(http.MethodGet, "/debug/pprof/cmdline", wrapHandlerFunc(pprof.Cmdline))
		r.Route(http.MethodGet, "/debug/pprof/profile", wrapHandlerFunc(pprof.Profile))
		r.Route(http.MethodGet, "/debug/pprof/symbol", wrapHandlerFunc(pprof.Symbol))
		r.Route(http.MethodGet, "/debug/pprof/trace", wrapHandlerFunc(pprof.Trace))
		r.RoutePrefix(http.MethodGet, "/debug/pprof/", wrapHandlerFunc(pprof.Index))
	}

	return withHeaders(csp, s.logRefused(recoverPanic(r.Handler())))
}

func withHeaders(csp string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		for name, value := range securityHeaders {
			header.Set(name, value)
		}
		header.Set("Content-Security-Policy", csp)
		// The answer on / depends on both, a 404 on Accept.
		header.Set("Vary", "Accept, User-Agent")
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
