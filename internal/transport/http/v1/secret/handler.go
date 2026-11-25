package secret

import (
	"context"
	"net/http"
	"strconv"

	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// OperationDataRewrapper moves Operation data (specs, output, progress) off
// an old key (AUDIT S-07); the resources store implements it.
type OperationDataRewrapper interface {
	RewrapOperationData(ctx context.Context, oldKeyID string, batchSize int) (rewrapped int, remaining int64, err error)
}

type Handler struct {
	service    *secrets.Service
	operations OperationDataRewrapper
}

// NewHandler creates the handler; operations may be nil (secrets only).
func NewHandler(s *secrets.Service, operations OperationDataRewrapper) *Handler {
	return &Handler{service: s, operations: operations}
}

// RewrapResponse reports one rewrap batch. Repeat until Done.
type RewrapResponse struct {
	// KeyID is the key secrets now use.
	KeyID string `json:"keyId"`
	// Rewrapped and Remaining count secret versions.
	Rewrapped int   `json:"rewrapped"`
	Remaining int64 `json:"remaining"`
	// OperationData covers Operation specs, output and progress, including
	// values written before key IDs existed (resealed by any rewrap).
	OperationData *RewrapCounts `json:"operationData,omitempty"`
	// Done is true when nothing remains under the old key; it can then be
	// removed from the keyring.
	Done bool `json:"done"`
}

// RewrapCounts is one batch's progress.
type RewrapCounts struct {
	Rewrapped int   `json:"rewrapped"`
	Remaining int64 `json:"remaining"`
}

// @Summary List secret metadata (never values)
// @Description Workspace owner, human session only.
// @Tags Secrets
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/secrets [get]
func (h *Handler) List(c echo.Context) error {
	p, _ := middleware.Principal(c)
	rows, err := h.service.List(c.Request().Context(), p, 100)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(200, rows)
}

// @Summary Create a secret from a write-only value
// @Description Workspace owner, human session only.
// @Tags Secrets
// @Produce json
// @Security BearerAuth
// @Param request body object true "{\"name\", \"purpose\", \"value\", \"description\"}"
// @Success 201 {object} map[string]interface{}
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/secrets [post]
func (h *Handler) Create(c echo.Context) error {
	var in secrets.Input
	if err := c.Bind(&in); err != nil {
		return httperr.Status(c, 400, "invalid request body")
	}
	p, _ := middleware.Principal(c)
	m, err := h.service.Create(c.Request().Context(), p, in)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusCreated, m)
}

// @Summary Get secret metadata
// @Description Workspace owner, human session only.
// @Tags Secrets
// @Produce json
// @Security BearerAuth
// @Param id path string true "Secret ID"
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Router /api/v1/secrets/{id} [get]
func (h *Handler) Describe(c echo.Context) error {
	p, _ := middleware.Principal(c)
	m, err := h.service.Describe(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(200, m)
}

// @Summary List secret version metadata
// @Description Workspace owner, human session only.
// @Tags Secrets
// @Produce json
// @Security BearerAuth
// @Param id path string true "Secret ID"
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Router /api/v1/secrets/{id}/versions [get]
func (h *Handler) Versions(c echo.Context) error {
	p, _ := middleware.Principal(c)
	rows, err := h.service.Versions(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(200, rows)
}

// @Summary Add a new secret version
// @Description Workspace owner, human session only.
// @Tags Secrets
// @Produce json
// @Security BearerAuth
// @Param id path string true "Secret ID"
// @Param request body object true "{\"expectedVersion\", \"value\"}"
// @Success 201 {object} map[string]interface{}
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/secrets/{id}/rotate [post]
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
	m, err := h.service.Rotate(c.Request().Context(), p, c.Param("id"), v, secrets.Input{Value: in.Value})
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(201, m)
}

// @Summary Retire a secret
// @Description Workspace owner, human session only.
// @Tags Secrets
// @Produce json
// @Security BearerAuth
// @Param id path string true "Secret ID"
// @Success 204
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Router /api/v1/secrets/{id} [delete]
func (h *Handler) Delete(c echo.Context) error {
	p, _ := middleware.Principal(c)
	if err := h.service.Delete(c.Request().Context(), p, c.Param("id")); err != nil {
		return respondError(c, err)
	}
	return c.NoContent(204)
}

// @Summary Move secrets and Operation data off an old encryption key
// @Description Moves secrets and Operation data off oldKeyId to the current key, batchSize at a time per kind; repeat until done. Workspace owner, human session only.
// @Tags Secrets
// @Produce json
// @Security BearerAuth
// @Param request body object true "{\"oldKeyId\", \"batchSize\"}"
// @Success 200 {object} RewrapResponse
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 503 {object} httperr.Body
// @Router /api/v1/secrets/rewrap [post]
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
		return respondError(c, err)
	}
	response := RewrapResponse{KeyID: result.KeyID, Rewrapped: result.Rewrapped, Remaining: result.Remaining, Done: result.Remaining == 0}
	if h.operations != nil {
		rewrapped, remaining, err := h.operations.RewrapOperationData(c.Request().Context(), in.OldKeyID, in.BatchSize)
		if err != nil {
			return httperr.Status(c, http.StatusServiceUnavailable, "operation data could not be rewrapped; keep the old key configured and retry")
		}
		response.OperationData = &RewrapCounts{Rewrapped: rewrapped, Remaining: remaining}
		response.Done = response.Done && remaining == 0
	}
	return c.JSON(http.StatusOK, response)
}

var errorMappings = []httperr.Mapping{
	httperr.Map(secrets.ErrUnauthorized, http.StatusForbidden, "permission denied"),
	httperr.Map(secrets.ErrNotFound, http.StatusNotFound, "secret not found"),
	httperr.Map(secrets.ErrConflict, http.StatusConflict, "secret version conflict"),
	httperr.Map(secrets.ErrInvalid, http.StatusBadRequest, "invalid secret request"),
	httperr.Map(secrets.ErrKeyUnavailable, http.StatusServiceUnavailable, "configured secret key is unavailable"),
}

// respondError writes err as an API error and logs unexpected ones.
func respondError(c echo.Context, err error) error {
	return httperr.FromError(c, err, errorMappings...)
}
