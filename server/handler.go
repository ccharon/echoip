package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// appHandler returns its error for ServeHTTP to render, so every handler
// answers in one style.
type appHandler func(http.ResponseWriter, *http.Request) *AppError

func (fn appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e := fn(w, r)
	if e == nil {
		return
	}

	logRefused(r, e)

	message := e.Message
	if e.IsJSON() {
		data := struct {
			Code  int    `json:"status"`
			Error string `json:"error"`
		}{e.Code, e.Message}
		if b, err := json.MarshalIndent(data, "", "  "); err == nil {
			message = string(b)
		} else {
			log.Printf("Rendering error as JSON failed: %v", err)
		}
	}

	if e.ContentType != "" {
		w.Header().Set("Content-Type", e.ContentType)
	}
	w.WriteHeader(e.Code)
	writeRaw(w, message)
}

// logRefused records a refused or failed request, the only trace a probe
// leaves. The peer address is used because a caller cannot choose it, and the
// values are quoted because a newline in them would forge a second line.
func logRefused(r *http.Request, e *AppError) {
	log.Printf("%s %s %q -> %d: %q", r.RemoteAddr, r.Method, clip(r.URL.RequestURI()), e.Code, clip(e.Error()))
}

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
	return func(w http.ResponseWriter, r *http.Request) *AppError {
		f.ServeHTTP(w, r)
		return nil
	}
}

// requestError reports a request that could not be read, as JSON so that CLI
// clients get a parsable answer. The message is bounded, because an error may
// quote what the caller sent.
func requestError(err error) *AppError {
	return badRequest(err).WithMessage(clip(err.Error())).AsJSON()
}

// writeRaw writes without a trailing newline. A failed write means the client
// is gone, which is only worth a log line.
func writeRaw(w http.ResponseWriter, s string) {
	if _, err := io.WriteString(w, s); err != nil {
		log.Printf("Writing response failed: %v", err)
	}
}

func writeLine(w http.ResponseWriter, s string) {
	writeRaw(w, s+"\n")
}

func writeJSON(w http.ResponseWriter, v any) *AppError {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return internalServerError(err).AsJSON()
	}
	w.Header().Set("Content-Type", jsonMediaType)
	writeRaw(w, string(b))
	return nil
}

// cliField answers with a single field of the response, one line of text.
func (s *Server) cliField(field func(Response) string) appHandler {
	return func(w http.ResponseWriter, r *http.Request) *AppError {
		response, err := s.newResponse(r)
		if err != nil {
			return requestError(err)
		}
		writeLine(w, field(response))
		return nil
	}
}

// ipHandler answers with the address alone. It skips the geo lookups that the
// other CLI handlers need.
func (s *Server) ipHandler(w http.ResponseWriter, r *http.Request) *AppError {
	ip, err := s.ipFromRequest(r)
	if err != nil {
		return requestError(err)
	}
	writeLine(w, ip.String())
	return nil
}

func (s *Server) jsonHandler(w http.ResponseWriter, r *http.Request) *AppError {
	response, err := s.newResponse(r)
	if err != nil {
		return requestError(err)
	}
	return writeJSON(w, response)
}

func (s *Server) healthHandler(w http.ResponseWriter, _ *http.Request) *AppError {
	w.Header().Set("Content-Type", jsonMediaType)
	writeRaw(w, `{"status":"OK"}`)
	return nil
}

func (s *Server) cacheHandler(w http.ResponseWriter, _ *http.Request) *AppError {
	stats := s.cache.Stats()
	return writeJSON(w, struct {
		Size      int    `json:"size"`
		Capacity  int    `json:"capacity"`
		Evictions uint64 `json:"evictions"`
	}{stats.Size, stats.Capacity, stats.Evictions})
}

func (s *Server) cacheResizeHandler(w http.ResponseWriter, r *http.Request) *AppError {
	// The body holds one number, so reading further would only let a caller
	// decide how much is held in memory.
	var capacity int
	if _, err := fmt.Fscan(io.LimitReader(r.Body, maxResizeBody), &capacity); err != nil {
		return requestError(err)
	}

	if err := s.cache.Resize(capacity); err != nil {
		return requestError(err)
	}

	return writeJSON(w, struct {
		Message string `json:"message"`
	}{fmt.Sprintf("Changed cache capacity to %d.", capacity)})
}

// browserHandler renders the HTML page.
func (s *Server) browserHandler(w http.ResponseWriter, r *http.Request) *AppError {
	response, err := s.newResponse(r)
	if err != nil {
		return badRequest(err).WithMessage(err.Error())
	}

	jsonData, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return internalServerError(err)
	}

	data := pageData{
		Response:     response,
		Host:         r.Host,
		GeoBuilt:     s.geoBuilt(),
		BoxLatTop:    response.Latitude + boxMargin,
		BoxLatBottom: response.Latitude - boxMargin,
		BoxLonLeft:   response.Longitude - boxMargin,
		BoxLonRight:  response.Longitude + boxMargin,
		JSON:         string(jsonData),
	}

	// Rendered into a buffer first, because a failure halfway through would
	// otherwise leave a truncated page that no status code can take back.
	var page bytes.Buffer
	if err := pageTemplate.ExecuteTemplate(&page, indexTemplate, &data); err != nil {
		return internalServerError(err)
	}

	// Set explicitly rather than left to content sniffing, which the nosniff
	// header tells the browser to ignore.
	w.Header().Set("Content-Type", htmlContentType)
	if _, err := page.WriteTo(w); err != nil {
		log.Printf("Writing response failed: %v", err)
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
	JSON         string
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

func notFoundHandler(_ http.ResponseWriter, r *http.Request) *AppError {
	err := notFound(nil).WithMessage("404 page not found")
	if acceptsMediaType(r, jsonMediaType) {
		err = err.AsJSON()
	}
	return err
}
