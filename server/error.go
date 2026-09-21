package server

import "net/http"

// AppError carries what a handler needs to render a failed request: the cause
// for the log, the message for the client, and how to format it.
type AppError struct {
	Err         error
	Message     string
	Code        int
	ContentType string
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Message
}

func (e *AppError) Unwrap() error { return e.Err }

func internalServerError(err error) *AppError {
	return &AppError{
		Err:     err,
		Message: "Internal server error",
		Code:    http.StatusInternalServerError,
	}
}

func notFound(err error) *AppError {
	return &AppError{Err: err, Code: http.StatusNotFound}
}

func badRequest(err error) *AppError {
	return &AppError{Err: err, Code: http.StatusBadRequest}
}

func (e *AppError) AsJSON() *AppError {
	e.ContentType = jsonMediaType
	return e
}

func (e *AppError) WithMessage(message string) *AppError {
	e.Message = message
	return e
}

func (e *AppError) IsJSON() bool {
	return e.ContentType == jsonMediaType
}
