package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

// problemBase leads each problem type to its section in the README, which is
// what RFC 9457 expects a type URI to resolve to.
const problemBase = "https://github.com/ccharon/echoip#"

// problem is a kind of failure. Title and status are the same for every
// occurrence, the detail of an appError describes the one at hand.
type problem struct {
	slug   string
	title  string
	status int
}

var (
	invalidAddress      = problem{"invalid-address", "Not an IP address", http.StatusBadRequest}
	notPublicAddress    = problem{"not-public-address", "Address is not public", http.StatusBadRequest}
	invalidCapacity     = problem{"invalid-capacity", "Not a cache capacity", http.StatusBadRequest}
	notFound            = problem{"not-found", "Not found", http.StatusNotFound}
	methodNotAllowed    = problem{"method-not-allowed", "Method not allowed", http.StatusMethodNotAllowed}
	internalError       = problem{"internal", "Internal error", http.StatusInternalServerError}
	databaseUnavailable = problem{"database-unavailable", "Database not loaded yet", http.StatusServiceUnavailable}
)

// appError is a failed request. Detail goes to the client, err to the log
// only.
type appError struct {
	problem problem
	detail  string
	err     error
	// format is the one the route answers in. Zero negotiates it.
	format format
}

func newError(p problem, detail string) *appError {
	return &appError{problem: p, detail: detail}
}

// internal hides err from the client, which learns nothing it could act on.
func internal(err error) *appError {
	return &appError{problem: internalError, detail: "The request could not be completed.", err: err}
}

func (e *appError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return e.detail
}

func (e *appError) Unwrap() error { return e.err }

// write answers with e in the format of the route, the one a success would
// have come in.
func (e *appError) write(w http.ResponseWriter, r *http.Request) {
	f := e.format
	if f == negotiated {
		f = negotiateFormat(r)
	}

	var body []byte
	var contentType string
	switch f {
	case jsonFormat:
		body, contentType = e.json(), problemMediaType
	case htmlFormat:
		body, contentType = e.html(), htmlContentType
	}
	if body == nil {
		body, contentType = e.text(), textContentType
	}

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(e.problem.status)
	writeRaw(w, body)
}

// json renders problem details as in RFC 9457.
func (e *appError) json() []byte {
	b, err := json.MarshalIndent(struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}{problemBase + e.problem.slug, e.problem.title, e.problem.status, e.detail}, "", "  ")
	if err != nil {
		slog.Error("rendering error as JSON failed", "error", err)
		return nil
	}
	return b
}

// text is one line, so a script that reads the answer without checking the
// status cannot take the error for a value.
func (e *appError) text() []byte {
	return fmt.Appendf(nil, "error: %s\n", e.detail)
}

// html renders the error page. It shares the style of the main page, and with
// it the hash the content security policy allows.
func (e *appError) html() []byte {
	var page bytes.Buffer
	data := errorData{Status: e.problem.status, Title: e.problem.title, Detail: e.detail}
	if err := pageTemplate.ExecuteTemplate(&page, errorTemplate, &data); err != nil {
		slog.Error("rendering error page failed", "error", err)
		return nil
	}
	return page.Bytes()
}

// errorData is what the error page renders.
type errorData struct {
	Status int
	Title  string
	Detail string
}
