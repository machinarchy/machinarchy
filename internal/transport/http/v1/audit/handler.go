package audit

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

type Handler struct {
	service domain.AuditService
}

func NewHandler(service domain.AuditService) *Handler {
	return &Handler{service: service}
}

// List returns audit events, newest first.
// @Summary List audit events
// @Description Requires a human workspace owner session; API tokens are refused. Events are append-only and never contain credentials. Page backwards by passing the smallest id received as before_id.
// @Tags Audit
// @Produce json
// @Security BearerAuth
// @Param action query string false "Only events with this action, for example member.role_changed"
// @Param before_id query int false "Only events with a smaller id (paging cursor)"
// @Param limit query int false "Page size (default 100, maximum 500)"
// @Success 200 {array} domain.AuditEvent
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/audit/events [get]
func (h *Handler) List(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	filter := domain.AuditFilter{Action: c.QueryParam("action")}
	if raw := c.QueryParam("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "limit must be a number"})
		}
		filter.Limit = limit
	}
	if raw := c.QueryParam("before_id"); raw != "" {
		before, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "before_id must be a number"})
		}
		filter.BeforeID = before
	}
	events, err := h.service.List(c.Request().Context(), principal, filter)
	if err != nil {
		return auditError(c, err)
	}
	return c.JSON(http.StatusOK, events)
}

// Verify walks the workspace's audit hash chain.
// @Summary Verify the audit trail
// @Description Requires a human workspace owner session. Recomputes every event's hash and link. A valid result proves no earlier event was edited, removed, or reordered; it cannot detect events removed from the end, so record headHash somewhere outside the database to detect that.
// @Tags Audit
// @Produce json
// @Security BearerAuth
// @Success 200 {object} domain.AuditVerification
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/audit/verify [get]
func (h *Handler) Verify(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	result, err := h.service.Verify(c.Request().Context(), principal)
	if err != nil {
		return auditError(c, err)
	}
	return c.JSON(http.StatusOK, result)
}

func auditError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, domain.ErrPermissionDenied):
		return c.JSON(http.StatusForbidden, map[string]string{"error": "permission denied"})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "unable to read the audit trail"})
	}
}
