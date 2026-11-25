package configuration

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/runtimeconfig"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

type Handler struct{ service *runtimeconfig.Service }

func NewHandler(s *runtimeconfig.Service) *Handler { return &Handler{service: s} }

// @Summary Get the active runtime configuration
// @Description Workspace owner, human session only.
// @Tags Runtime configuration
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/runtime-config [get]
func (h *Handler) Current(c echo.Context) error {
	r, err := h.service.Current(c.Request().Context())
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"revision": r, "classifications": runtimeconfig.Classifications()})
}

// @Summary List configuration revisions
// @Description Workspace owner, human session only.
// @Tags Runtime configuration
// @Produce json
// @Security BearerAuth
// @Param before query int false "Return revisions older than this"
// @Param limit query int false "Page size"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/runtime-config/revisions [get]
func (h *Handler) List(c echo.Context) error {
	limit := 100
	if raw := c.QueryParam("limit"); raw != "" {
		v, e := strconv.Atoi(raw)
		if e != nil {
			return httperr.Status(c, 400, "invalid limit")
		}
		limit = v
	}
	var before uint64
	if raw := c.QueryParam("before"); raw != "" {
		v, e := strconv.ParseUint(raw, 10, 64)
		if e != nil {
			return httperr.Status(c, 400, "invalid before revision")
		}
		before = v
	}
	rows, err := h.service.List(c.Request().Context(), limit, before)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(200, rows)
}

// @Summary Get one configuration revision
// @Description Workspace owner, human session only.
// @Tags Runtime configuration
// @Produce json
// @Security BearerAuth
// @Param revision path int true "Revision"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Router /api/v1/runtime-config/revisions/{revision} [get]
func (h *Handler) Get(c echo.Context) error {
	v, err := strconv.ParseUint(c.Param("revision"), 10, 64)
	if err != nil {
		return httperr.Status(c, 400, "invalid revision")
	}
	r, err := h.service.Get(c.Request().Context(), v)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(200, r)
}

type publishRequest struct {
	ExpectedRevision uint64               `json:"expectedRevision"`
	Config           runtimeconfig.Config `json:"config"`
	ChangeNote       string               `json:"changeNote,omitempty"`
}

// @Summary Validate a configuration document
// @Description Workspace owner, human session only.
// @Tags Runtime configuration
// @Produce json
// @Security BearerAuth
// @Param request body object true "{\"config\": {...}}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 422 {object} httperr.Body
// @Router /api/v1/runtime-config/validate [post]
func (h *Handler) Validate(c echo.Context) error {
	var body struct {
		Config runtimeconfig.Config `json:"config"`
	}
	if err := decode(c, &body); err != nil {
		return httperr.Status(c, 400, "invalid request body")
	}
	if err := body.Config.Validate(); err != nil {
		return httperr.Status(c, 422, err.Error())
	}
	return c.JSON(200, map[string]any{"valid": true, "classifications": runtimeconfig.Classifications()})
}

// @Summary Publish a new configuration revision
// @Description Workspace owner, human session only. Hot-reloadable settings apply at once. A change to a restart-required setting (worker counts, resync interval, ...) is stored too and applies when each process next starts; the response lists those settings in `restartRequired`, and `GET /runtime-config/status` shows `restartPending` for the processes that started without them.
// @Tags Runtime configuration
// @Produce json
// @Security BearerAuth
// @Param request body object true "{\"expectedRevision\", \"config\", \"changeNote\"}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Failure 422 {object} httperr.Body
// @Router /api/v1/runtime-config [put]
func (h *Handler) Publish(c echo.Context) error {
	var req publishRequest
	if err := decode(c, &req); err != nil {
		return httperr.Status(c, 400, "invalid request body")
	}
	p, ok := middleware.Principal(c)
	if !ok {
		return httperr.Status(c, 401, "unauthorized")
	}
	r, err := h.service.Publish(c.Request().Context(), p, req.ExpectedRevision, req.Config, req.ChangeNote)
	if errors.Is(err, runtimeconfig.ErrApplyPending) {
		return c.JSON(http.StatusAccepted, map[string]any{"revision": r, "applicationPending": true})
	}
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusCreated, r)
}

// @Summary Copy an earlier revision into a new one
// @Description Workspace owner, human session only. Like publish, it lists changed restart-required settings in `restartRequired`.
// @Tags Runtime configuration
// @Produce json
// @Security BearerAuth
// @Param request body object true "{\"expectedRevision\", \"targetRevision\", \"changeNote\"}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/runtime-config/rollback [post]
func (h *Handler) Rollback(c echo.Context) error {
	var req struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
		TargetRevision   uint64 `json:"targetRevision"`
		ChangeNote       string `json:"changeNote,omitempty"`
	}
	if err := decode(c, &req); err != nil {
		return httperr.Status(c, 400, "invalid request body")
	}
	p, ok := middleware.Principal(c)
	if !ok {
		return httperr.Status(c, 401, "unauthorized")
	}
	r, err := h.service.Rollback(c.Request().Context(), p, req.ExpectedRevision, req.TargetRevision, req.ChangeNote)
	if errors.Is(err, runtimeconfig.ErrApplyPending) {
		return c.JSON(http.StatusAccepted, map[string]any{"revision": r, "applicationPending": true})
	}
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusCreated, r)
}

// @Summary Show applied revisions per process
// @Description Workspace owner, human session only.
// @Tags Runtime configuration
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/runtime-config/status [get]
func (h *Handler) Status(c echo.Context) error {
	rows, err := h.service.Status(c.Request().Context())
	if err != nil {
		return respondError(c, err)
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

var errorMappings = []httperr.Mapping{
	httperr.Map(runtimeconfig.ErrConflict, http.StatusConflict, "configuration revision conflict"),
	httperr.Map(runtimeconfig.ErrInvalid, http.StatusUnprocessableEntity, ""),
	httperr.Map(domain.ErrInvalidInput, http.StatusUnprocessableEntity, ""),
	httperr.Map(runtimeconfig.ErrNotFound, http.StatusNotFound, "configuration revision not found"),
}

// respondError writes err as an API error and logs unexpected ones.
func respondError(c echo.Context, err error) error {
	return httperr.FromError(c, err, errorMappings...)
}
