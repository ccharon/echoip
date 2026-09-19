package http

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

// appHandler reports the error instead of writing it, so every handler answers
// in one style. ServeHTTP renders it.
type appHandler func(http.ResponseWriter, *http.Request) *AppError

func (fn appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e := fn(w, r)
	if e == nil {
		return
	}

	if e.Code/100 == 5 {
		log.Println(e.Err)
	}

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

func wrapHandlerFunc(f http.HandlerFunc) appHandler {
	return func(w http.ResponseWriter, r *http.Request) *AppError {
		f.ServeHTTP(w, r)
		return nil
	}
}

// requestError reports a request that could not be read, as JSON so that CLI
// clients get a parsable answer.
func requestError(err error) *AppError {
	return badRequest(err).WithMessage(err.Error()).AsJSON()
}

// writeRaw writes without a trailing newline. A failed write means the client
// is gone, which is only worth a log line.
func writeRaw(w http.ResponseWriter, s string) {
	if _, err := fmt.Fprint(w, s); err != nil {
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
	ip, err := ipFromRequest(s.cfg.IPHeaders, r, true)
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

func (s *Server) portHandler(w http.ResponseWriter, r *http.Request) *AppError {
	response, err := s.newPortResponse(r)
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
	var capacity int
	if _, err := fmt.Fscan(r.Body, &capacity); err != nil {
		return requestError(err)
	}

	if err := s.cache.Resize(capacity); err != nil {
		return requestError(err)
	}

	return writeJSON(w, struct {
		Message string `json:"message"`
	}{fmt.Sprintf("Changed cache capacity to %d.", capacity)})
}

// browserHandler renders the HTML page. It is only registered when a template
// was loaded.
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
		BoxLatTop:    response.Latitude + boxMargin,
		BoxLatBottom: response.Latitude - boxMargin,
		BoxLonLeft:   response.Longitude - boxMargin,
		BoxLonRight:  response.Longitude + boxMargin,
		JSON:         string(jsonData),
	}

	if err := s.template.ExecuteTemplate(w, indexTemplate, &data); err != nil {
		return internalServerError(err)
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
	BoxLatTop    float64
	BoxLatBottom float64
	BoxLonLeft   float64
	BoxLonRight  float64
	JSON         string
}

func notFoundHandler(_ http.ResponseWriter, r *http.Request) *AppError {
	err := notFound(nil).WithMessage("404 page not found")
	if acceptsMediaType(r, jsonMediaType) {
		err = err.AsJSON()
	}
	return err
}
