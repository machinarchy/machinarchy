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
