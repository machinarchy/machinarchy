package configuration

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/runtimeconfig"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

type Handler struct{ service *runtimeconfig.Service }

func NewHandler(s *runtimeconfig.Service) *Handler { return &Handler{service: s} }
func (h *Handler) Current(c echo.Context) error {
	r, err := h.service.Current(c.Request().Context())
	if err != nil {
		return configError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"revision": r, "classifications": runtimeconfig.Classifications()})
}
func (h *Handler) List(c echo.Context) error {
	limit := 100
	if raw := c.QueryParam("limit"); raw != "" {
		v, e := strconv.Atoi(raw)
		if e != nil {
			return c.JSON(400, map[string]string{"error": "invalid limit"})
		}
		limit = v
	}
	var before uint64
	if raw := c.QueryParam("before"); raw != "" {
		v, e := strconv.ParseUint(raw, 10, 64)
		if e != nil {
			return c.JSON(400, map[string]string{"error": "invalid before revision"})
		}
		before = v
	}
	rows, err := h.service.List(c.Request().Context(), limit, before)
	if err != nil {
		return configError(c, err)
	}
	return c.JSON(200, rows)
}
func (h *Handler) Get(c echo.Context) error {
	v, err := strconv.ParseUint(c.Param("revision"), 10, 64)
	if err != nil {
		return c.JSON(400, map[string]string{"error": "invalid revision"})
	}
	r, err := h.service.Get(c.Request().Context(), v)
	if err != nil {
		return configError(c, err)
	}
	return c.JSON(200, r)
}

type publishRequest struct {
	ExpectedRevision uint64               `json:"expectedRevision"`
	Config           runtimeconfig.Config `json:"config"`
	ChangeNote       string               `json:"changeNote,omitempty"`
}

func (h *Handler) Validate(c echo.Context) error {
	var body struct {
		Config runtimeconfig.Config `json:"config"`
	}
	if err := decode(c, &body); err != nil {
		return c.JSON(400, map[string]string{"error": "invalid request body"})
	}
	if err := body.Config.Validate(); err != nil {
		return c.JSON(422, map[string]string{"error": err.Error()})
	}
	return c.JSON(200, map[string]any{"valid": true, "classifications": runtimeconfig.Classifications()})
}
func (h *Handler) Publish(c echo.Context) error {
	var req publishRequest
	if err := decode(c, &req); err != nil {
		return c.JSON(400, map[string]string{"error": "invalid request body"})
	}
	p, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(401, map[string]string{"error": "unauthorized"})
	}
	r, err := h.service.Publish(c.Request().Context(), p, req.ExpectedRevision, req.Config, req.ChangeNote)
	if errors.Is(err, runtimeconfig.ErrApplyPending) {
		return c.JSON(http.StatusAccepted, map[string]any{"revision": r, "applicationPending": true})
	}
	if err != nil {
		return configError(c, err)
	}
	return c.JSON(http.StatusCreated, r)
}
func (h *Handler) Rollback(c echo.Context) error {
	var req struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
		TargetRevision   uint64 `json:"targetRevision"`
		ChangeNote       string `json:"changeNote,omitempty"`
	}
	if err := decode(c, &req); err != nil {
		return c.JSON(400, map[string]string{"error": "invalid request body"})
	}
	p, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(401, map[string]string{"error": "unauthorized"})
	}
	r, err := h.service.Rollback(c.Request().Context(), p, req.ExpectedRevision, req.TargetRevision, req.ChangeNote)
	if errors.Is(err, runtimeconfig.ErrApplyPending) {
		return c.JSON(http.StatusAccepted, map[string]any{"revision": r, "applicationPending": true})
	}
	if err != nil {
		return configError(c, err)
	}
	return c.JSON(http.StatusCreated, r)
}
func (h *Handler) Status(c echo.Context) error {
	rows, err := h.service.Status(c.Request().Context())
	if err != nil {
		return configError(c, err)
	}
	return c.JSON(200, rows)
}
func decode(c echo.Context, dst any) error {
	dec := json.NewDecoder(io.LimitReader(c.Request().Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}
func configError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, runtimeconfig.ErrConflict):
		return c.JSON(409, map[string]string{"error": "configuration revision conflict"})
	case errors.Is(err, runtimeconfig.ErrInvalid), errors.Is(err, runtimeconfig.ErrRestartRequired), errors.Is(err, domain.ErrInvalidInput):
		return c.JSON(422, map[string]string{"error": err.Error()})
	case errors.Is(err, domain.ErrPermissionDenied):
		return c.JSON(403, map[string]string{"error": "permission denied"})
	case errors.Is(err, runtimeconfig.ErrNotFound):
		return c.JSON(404, map[string]string{"error": "configuration revision not found"})
	default:
		return c.JSON(500, map[string]string{"error": "configuration operation failed"})
	}
}
