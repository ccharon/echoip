package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// appHandler returns its error for ServeHTTP to render, so every handler
// answers in one style.
type appHandler func(http.ResponseWriter, *http.Request) *appError

func (fn appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if e := fn(w, r); e != nil {
		e.write(w, r)
	}
}

// logRefused records a refused or failed request, the only trace a probe
// leaves. Behind a proxy the peer is the proxy, so the address it reported
// goes in client.
func (s *Server) logRefused(next appHandler) appHandler {
	return func(w http.ResponseWriter, r *http.Request) *appError {
		e := next(w, r)
		if e == nil {
			return nil
		}
		attrs := []any{"peer", r.RemoteAddr}
		if peer, err := peerAddr(r); err == nil {
			if forwarded := s.forwarded(r, peer); forwarded != "" {
				attrs = append(attrs, "client", clip(forwarded))
			}
		}
		attrs = append(attrs,
			"method", r.Method,
			"uri", clip(r.URL.RequestURI()),
			"status", e.problem.status,
			"problem", e.problem.slug,
			"error", clip(e.Error()))
		if e.problem.status >= http.StatusInternalServerError {
			slog.Error("request failed", attrs...)
		} else {
			slog.Info("request refused", attrs...)
		}
		return e
	}
}

// recoverPanic answers a panic with a 500, so the client gets an answer and
// logRefused a line. A handler that has already written cannot take it back,
// and the connection is dropped instead.
func recoverPanic(next appHandler) appHandler {
	return func(w http.ResponseWriter, r *http.Request) (e *appError) {
		tw := &trackingWriter{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			slog.Error("handler panicked", "panic", v, "stack", string(debug.Stack()))
			if tw.written {
				panic(http.ErrAbortHandler)
			}
			e = internal(fmt.Errorf("panic: %v", v))
		}()
		return next(tw, r)
	}
}

// trackingWriter records whether the response has started.
type trackingWriter struct {
	http.ResponseWriter
	written bool
}

func (w *trackingWriter) WriteHeader(code int) {
	w.written = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *trackingWriter) Write(b []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection.
func (w *trackingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

const (
	// maxValueLen bounds a value a caller controls, so one request cannot fill
	// the log with a single line or have its input echoed back at length.
	maxValueLen = 128

	// maxResizeBody is wider than the longest int64 and narrower than anything
	// worth reading into memory.
	maxResizeBody = 32
)

func clip(s string) string {
	if len(s) <= maxValueLen {
		return s
	}
	return s[:maxValueLen] + "..."
}

func wrapHandlerFunc(f http.HandlerFunc) appHandler {
	return func(w http.ResponseWriter, r *http.Request) *appError {
		f.ServeHTTP(w, r)
		return nil
	}
}

// writeRaw writes without a trailing newline. A failed write means the client
// is gone, which is only worth a log line.
func writeRaw(w http.ResponseWriter, s string) {
	if _, err := io.WriteString(w, s); err != nil {
		slog.Info("writing response failed", "error", err)
	}
}

// writeLine answers with one line of plain text.
func writeLine(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", textContentType)
	writeRaw(w, s+"\n")
}

func writeJSON(w http.ResponseWriter, v any) *appError {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return internal(err)
	}
	w.Header().Set("Content-Type", jsonMediaType)
	writeRaw(w, string(b))
	return nil
}

// retryAfter is the delay after which the updater tries a missing database
// again, in seconds.
const retryAfter = "900"

// fieldEndpoint is a text endpoint that answers one field of the response.
// The page offers the same ones as buttons.
type fieldEndpoint struct {
	path  string
	field func(Response) string
}

var cityEndpoints = []fieldEndpoint{
	{"country", func(r Response) string { return r.Country }},
	{"country-iso", func(r Response) string { return r.CountryISO }},
	{"city", func(r Response) string { return r.City }},
	{"coordinates", Response.Coordinates},
}

var asnEndpoints = []fieldEndpoint{
	{"asn", func(r Response) string { return r.ASN }},
	{"asn-org", func(r Response) string { return r.ASNOrg }},
}

func (s *Server) cityField(field func(Response) string) appHandler {
	return s.cliField("city", func() bool { return s.geo.HasCity() }, field)
}

func (s *Server) asnField(field func(Response) string) appHandler {
	return s.cliField("ASN", func() bool { return s.geo.HasASN() }, field)
}

func notLoaded(database string) string {
	return "The " + database + " database is not loaded yet."
}

// cliField answers with a single field of the response, one line of text. The
// database it comes from is checked per request, because it may be downloaded
// after the server starts.
func (s *Server) cliField(database string, loaded func() bool, field func(Response) string) appHandler {
	return func(w http.ResponseWriter, r *http.Request) *appError {
		if !loaded() {
			w.Header().Set("Retry-After", retryAfter)
			return newError(databaseUnavailable, notLoaded(database))
		}
		response, e := s.newResponse(r)
		if e != nil {
			return e
		}
		writeLine(w, field(response))
		return nil
	}
}

// rootHandler answers / in the format negotiateFormat picks, which is the one
// its failures come in as well.
func (s *Server) rootHandler(w http.ResponseWriter, r *http.Request) *appError {
	switch negotiateFormat(r) {
	case jsonFormat:
		return s.jsonHandler(w, r)
	case textFormat:
		return s.ipHandler(w, r)
	}
	return s.browserHandler(w, r)
}

// ipHandler answers with the address alone. It skips the geo lookups that the
// other CLI handlers need.
func (s *Server) ipHandler(w http.ResponseWriter, r *http.Request) *appError {
	ip, e := s.ipFromRequest(r)
	if e != nil {
		return e
	}
	writeLine(w, ip.String())
	return nil
}

func (s *Server) jsonHandler(w http.ResponseWriter, r *http.Request) *appError {
	response, e := s.newResponse(r)
	if e != nil {
		return e
	}
	return writeJSON(w, response)
}

func (s *Server) healthHandler(w http.ResponseWriter, _ *http.Request) *appError {
	w.Header().Set("Content-Type", jsonMediaType)
	writeRaw(w, `{"status":"OK"}`)
	return nil
}

func (s *Server) cacheHandler(w http.ResponseWriter, _ *http.Request) *appError {
	stats := s.cache.stats()
	return writeJSON(w, struct {
		Size      int    `json:"size"`
		Capacity  int    `json:"capacity"`
		Evictions uint64 `json:"evictions"`
	}{stats.Size, stats.Capacity, stats.Evictions})
}

func (s *Server) cacheResizeHandler(w http.ResponseWriter, r *http.Request) *appError {
	capacity, err := readCapacity(r.Body)
	if err == nil {
		err = s.cache.resize(capacity)
	}
	if err != nil {
		return &appError{problem: invalidCapacity, detail: "The body has to be a whole number of at least 0.", err: err}
	}

	return writeJSON(w, struct {
		Message string `json:"message"`
	}{fmt.Sprintf("Changed cache capacity to %d.", capacity)})
}

// readCapacity reads a body that holds one number and nothing else. It reads
// one byte past maxResizeBody, so a longer body is refused rather than cut.
func readCapacity(body io.Reader) (int, error) {
	b, err := io.ReadAll(io.LimitReader(body, maxResizeBody+1))
	if err != nil {
		return 0, err
	}
	if len(b) > maxResizeBody {
		return 0, fmt.Errorf("body longer than %d bytes", maxResizeBody)
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// browserHandler renders the HTML page.
func (s *Server) browserHandler(w http.ResponseWriter, r *http.Request) *appError {
	response, e := s.newResponse(r)
	if e != nil {
		return e
	}

	jsonData, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return internal(err)
	}

	data := pageData{
		Response: response,
		Host:     r.Host,
		GeoBuilt: s.geoBuilt(),
		Chips:    s.chips(response),
		JSON:     string(jsonData),
	}
	if lat, lon := response.Latitude, response.Longitude; lat != nil && lon != nil {
		data.BoxLatTop = *lat + boxMargin
		data.BoxLatBottom = *lat - boxMargin
		data.BoxLonLeft = *lon - boxMargin
		data.BoxLonRight = *lon + boxMargin
	}

	// Rendered into a buffer first, because a failure halfway through would
	// otherwise leave a truncated page that no status code can take back.
	var page bytes.Buffer
	if err := pageTemplate.ExecuteTemplate(&page, indexTemplate, &data); err != nil {
		return internal(err)
	}

	// Set explicitly rather than left to content sniffing, which the nosniff
	// header tells the browser to ignore.
	w.Header().Set("Content-Type", htmlContentType)
	if _, err := page.WriteTo(w); err != nil {
		slog.Info("writing response failed", "error", err)
	}

	return nil
}

// boxMargin spans the map a little wider than the location itself, so the
// marker has some context around it.
const boxMargin = 0.05

// pageData is what the browser template renders.
type pageData struct {
	Response
	Host         string
	GeoBuilt     string
	BoxLatTop    float64
	BoxLatBottom float64
	BoxLonLeft   float64
	BoxLonRight  float64
	Chips        []chip
	JSON         string
}

// chip is a button on the page that previews a field endpoint. Unavailable
// is the reason the endpoint answers 503 at the moment.
type chip struct {
	Path        string
	Value       string
	Unavailable string
}

// chips offers the field endpoints the server registered, so no button leads
// to a 404.
func (s *Server) chips(response Response) []chip {
	var chips []chip
	add := func(endpoints []fieldEndpoint, unavailable string) {
		for _, ep := range endpoints {
			chips = append(chips, chip{Path: ep.path, Value: ep.field(response), Unavailable: unavailable})
		}
	}
	if s.cfg.City {
		add(cityEndpoints, unlessLoaded(s.geo.HasCity(), "city"))
	}
	if s.cfg.ASN {
		add(asnEndpoints, unlessLoaded(s.geo.HasASN(), "ASN"))
	}
	return chips
}

func unlessLoaded(loaded bool, database string) string {
	if loaded {
		return ""
	}
	return notLoaded(database)
}

// geoBuilt names when MaxMind built the databases that are loaded, for the
// attribution in the page footer. It is read per request, because a database
// may be replaced while the server runs.
func (s *Server) geoBuilt() string {
	var parts []string
	if built := s.geo.CityBuilt(); !built.IsZero() {
		parts = append(parts, "City database built "+buildDate(built))
	}
	if built := s.geo.ASNBuilt(); !built.IsZero() {
		parts = append(parts, "ASN database built "+buildDate(built))
	}
	return strings.Join(parts, ", ")
}

// buildDate states the build in UTC, the zone MaxMind stamps it in.
func buildDate(built time.Time) string {
	return built.UTC().Format(time.DateOnly)
}
