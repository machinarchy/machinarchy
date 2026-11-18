// Package httperr writes the single JSON error format of the HTTP API:
//
//	{"error": "human-readable message", "code": "machine_readable_code"}
//
// "error" is kept for existing clients; "code" is stable and meant for
// programs. Handlers return errors through Write or FromError instead of
// building ad hoc maps.
package httperr

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

// Codes returned in Body.Code.
const (
	CodeInvalidRequest   = "invalid_request"
	CodeUnauthorized     = "unauthorized"
	CodePermissionDenied = "permission_denied"
	CodeNotFound         = "not_found"
	CodeConflict         = "conflict"
	CodeTargetsChanged   = "targets_changed"
	CodeTooManyRequests  = "too_many_requests"
	CodeUnavailable      = "unavailable"
	CodeInternal         = "internal"
)

// Body is the error response document.
type Body struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// Write sends an error response.
func Write(c echo.Context, status int, code, message string) error {
	return c.JSON(status, Body{Error: message, Code: code})
}

// CodeFor returns the default code for an HTTP status.
func CodeFor(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusRequestEntityTooLarge, http.StatusMethodNotAllowed:
		return CodeInvalidRequest
	case http.StatusUnauthorized:
		return CodeUnauthorized
	case http.StatusForbidden:
		return CodePermissionDenied
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusConflict:
		return CodeConflict
	case http.StatusTooManyRequests:
		return CodeTooManyRequests
	case http.StatusServiceUnavailable:
		return CodeUnavailable
	}
	if status >= 500 {
		return CodeInternal
	}
	return CodeInvalidRequest
}

// Status writes message with the default code for status.
func Status(c echo.Context, status int, message string) error {
	return Write(c, status, CodeFor(status), message)
}

// Handler is the Echo HTTPErrorHandler: errors that escape handlers, and the
// framework's own (unknown route, method not allowed, body too large,
// panics recovered by middleware), use the same JSON shape.
func Handler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	status, message := http.StatusInternalServerError, "request failed"
	var httpError *echo.HTTPError
	if errors.As(err, &httpError) {
		status = httpError.Code
		if text, ok := httpError.Message.(string); ok && status < 500 {
			message = text
		} else if status < 500 {
			message = http.StatusText(status)
		}
	}
	var writeErr error
	if c.Request().Method == http.MethodHead {
		writeErr = c.NoContent(status)
	} else {
		writeErr = Status(c, status, message)
	}
	if writeErr != nil {
		c.Logger().Error(writeErr)
	}
}

// BadRequest reports a malformed request.
func BadRequest(c echo.Context, message string) error {
	if message == "" {
		message = "invalid request"
	}
	return Write(c, http.StatusBadRequest, CodeInvalidRequest, message)
}

// Unauthorized reports a missing or invalid credential.
func Unauthorized(c echo.Context) error {
	return Write(c, http.StatusUnauthorized, CodeUnauthorized, "unauthorized")
}

// Mapping is one domain error and the response it produces.
type Mapping struct {
	Target  error
	Status  int
	Code    string
	Message string // empty: use the error text, which must then be safe to show
}

// common maps errors shared by every API surface. Their messages are fixed
// so internal details never leak.
var common = []Mapping{
	{domain.ErrPermissionDenied, http.StatusForbidden, CodePermissionDenied, "permission denied"},
	{resource.ErrTargetsChanged, http.StatusConflict, CodeTargetsChanged, resource.ErrTargetsChanged.Error()},
	{resource.ErrConflict, http.StatusConflict, CodeConflict, "resource revision conflict"},
	{resource.ErrAlreadyExists, http.StatusConflict, CodeConflict, resource.ErrAlreadyExists.Error()},
	{resource.ErrHostNotFound, http.StatusNotFound, CodeNotFound, "resource not found"},
	{resource.ErrOperationNotFound, http.StatusNotFound, CodeNotFound, "resource not found"},
	{resource.ErrAccessProfileNotFound, http.StatusNotFound, CodeNotFound, "resource not found"},
	{domain.ErrInvalidInput, http.StatusBadRequest, CodeInvalidRequest, ""},
	{resource.ErrInvalidResource, http.StatusBadRequest, CodeInvalidRequest, "invalid resource request"},
}

// FromError maps err with the given mappings first, then the common ones.
// Unknown errors become a generic 500 without detail.
func FromError(c echo.Context, err error, mappings ...Mapping) error {
	for _, list := range [][]Mapping{mappings, common} {
		for _, m := range list {
			if errors.Is(err, m.Target) {
				message := m.Message
				if message == "" {
					message = err.Error()
				}
				return Write(c, m.Status, m.Code, message)
			}
		}
	}
	return Write(c, http.StatusInternalServerError, CodeInternal, "request failed")
}
