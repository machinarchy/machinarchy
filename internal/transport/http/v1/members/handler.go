// Package members provides HTTP handlers and routes for workspace member management.
package members

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// Handler handles HTTP requests for workspace member management operations.
type Handler struct {
	service domain.MemberService
}

// SetRoleRequest is the body of a role change.
type SetRoleRequest struct {
	Role string `json:"role" enums:"owner,operator,reader" example:"operator"`
}

// NewHandler creates a member HTTP handler using the provided member service.
func NewHandler(service domain.MemberService) *Handler {
	return &Handler{service: service}
}

// List returns the members of the caller's workspace.
// @Summary List workspace members
// @Description Requires a human workspace owner session. API tokens are refused.
// @Tags Members
// @Produce json
// @Security BearerAuth
// @Success 200 {array} domain.WorkspaceMember
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /api/v1/members [get]
func (h *Handler) List(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Status(c, http.StatusUnauthorized, "unauthorized")
	}
	members, err := h.service.List(c.Request().Context(), principal)
	if err != nil {
		return memberError(c, err)
	}
	return c.JSON(http.StatusOK, members)
}

// SetRole changes a member's role.
// @Summary Change a member's role
// @Description Requires a human workspace owner session. Takes effect on the member's next request, including for API tokens, whose effective role never exceeds the membership role. A workspace must always keep at least one owner. The change is recorded in the structured server log.
// @Tags Members
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "User ID"
// @Param request body SetRoleRequest true "New role"
// @Success 200 {object} domain.WorkspaceMember
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /api/v1/members/{id}/role [put]
func (h *Handler) SetRole(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Status(c, http.StatusUnauthorized, "unauthorized")
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return httperr.Status(c, http.StatusBadRequest, "invalid user ID")
	}
	var request SetRoleRequest
	if err := c.Bind(&request); err != nil {
		return httperr.Status(c, http.StatusBadRequest, "invalid request body")
	}
	member, err := h.service.SetRole(c.Request().Context(), principal, id,
		domain.Role(strings.ToLower(strings.TrimSpace(request.Role))))
	if err != nil {
		return memberError(c, err)
	}
	return c.JSON(http.StatusOK, member)
}

func memberError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		return httperr.Status(c, http.StatusBadRequest, "role must be owner, operator, or reader")
	case errors.Is(err, domain.ErrPermissionDenied):
		return httperr.Status(c, http.StatusForbidden, "permission denied")
	case errors.Is(err, domain.ErrUserNotFound):
		return httperr.Status(c, http.StatusNotFound, "member not found")
	case errors.Is(err, domain.ErrLastOwner):
		return httperr.Status(c, http.StatusConflict, "a workspace must keep at least one owner")
	default:
		return httperr.Status(c, http.StatusInternalServerError, "unable to process member request")
	}
}
