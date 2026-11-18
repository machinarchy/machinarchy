package secret

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

type Handler struct{ service *secrets.Service }

func NewHandler(s *secrets.Service) *Handler { return &Handler{service: s} }
func (h *Handler) List(c echo.Context) error {
	p, _ := middleware.Principal(c)
	rows, err := h.service.List(c.Request().Context(), p, 100)
	if err != nil {
		return secretError(c, err)
	}
	return c.JSON(200, rows)
}
func (h *Handler) Create(c echo.Context) error {
	var in secrets.Input
	if err := c.Bind(&in); err != nil {
		return httperr.Status(c, 400, "invalid request body")
	}
	p, _ := middleware.Principal(c)
	m, err := h.service.Create(c.Request().Context(), p, in)
	if err != nil {
		return secretError(c, err)
	}
	return c.JSON(http.StatusCreated, m)
}
func (h *Handler) Describe(c echo.Context) error {
	p, _ := middleware.Principal(c)
	m, err := h.service.Describe(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return secretError(c, err)
	}
	return c.JSON(200, m)
}
func (h *Handler) Versions(c echo.Context) error {
	p, _ := middleware.Principal(c)
	rows, err := h.service.Versions(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return secretError(c, err)
	}
	return c.JSON(200, rows)
}
func (h *Handler) Rotate(c echo.Context) error {
	var in struct {
		ExpectedVersion uint64 `json:"expectedVersion"`
		Value           string `json:"value"`
	}
	if err := c.Bind(&in); err != nil {
		return httperr.Status(c, 400, "invalid request body")
	}
	v, err := strconv.ParseUint(strconv.FormatUint(in.ExpectedVersion, 10), 10, 64)
	if err != nil || v == 0 {
		return httperr.Status(c, 400, "expectedVersion is required")
	}
	p, _ := middleware.Principal(c)
	m, err := h.service.Rotate(c.Request().Context(), p, c.Param("id"), v, secrets.Input{Name: "rotation", Value: in.Value})
	if err != nil {
		return secretError(c, err)
	}
	return c.JSON(201, m)
}
func (h *Handler) Delete(c echo.Context) error {
	p, _ := middleware.Principal(c)
	if err := h.service.Delete(c.Request().Context(), p, c.Param("id")); err != nil {
		return secretError(c, err)
	}
	return c.NoContent(204)
}
func (h *Handler) Rewrap(c echo.Context) error {
	var in struct {
		OldKeyID  string `json:"oldKeyId"`
		BatchSize int    `json:"batchSize"`
	}
	if err := c.Bind(&in); err != nil {
		return httperr.Status(c, 400, "invalid request body")
	}
	p, _ := middleware.Principal(c)
	result, err := h.service.RewrapKey(c.Request().Context(), p, in.OldKeyID, in.BatchSize)
	if err != nil {
		return secretError(c, err)
	}
	return c.JSON(http.StatusOK, result)
}
func secretError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, secrets.ErrUnauthorized):
		return httperr.Status(c, 403, "permission denied")
	case errors.Is(err, secrets.ErrNotFound):
		return httperr.Status(c, 404, "secret not found")
	case errors.Is(err, secrets.ErrConflict):
		return httperr.Status(c, 409, "secret version conflict")
	case errors.Is(err, secrets.ErrInvalid):
		return httperr.Status(c, 400, "invalid secret request")
	case errors.Is(err, secrets.ErrKeyUnavailable):
		return httperr.Status(c, 503, "configured secret key is unavailable")
	default:
		return httperr.Status(c, 500, "secret operation failed")
	}
}
