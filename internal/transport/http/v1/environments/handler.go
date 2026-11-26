// Package environments provides HTTP handlers and routes for environment management.
package environments

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// Handler handles HTTP requests for environment management operations.
type Handler struct {
	repo domain.EnvironmentStore
}

var errorMappings = []httperr.Mapping{
	httperr.Map(domain.ErrEnvironmentNotFound, http.StatusNotFound, "environment not found"),
	{Target: domain.ErrEnvironmentUnprotectOwnerOnly, Status: http.StatusForbidden, Code: httperr.CodePermissionDenied, Message: domain.ErrEnvironmentUnprotectOwnerOnly.Error()},
	{Target: domain.ErrEnvironmentAlreadyExists, Status: http.StatusConflict, Code: httperr.CodeAlreadyExists, Message: "an environment with this name already exists"},
}

// respondError writes err as an API error and logs unexpected ones.
func respondError(c echo.Context, err error) error {
	return httperr.FromError(c, err, errorMappings...)
}

// NewHandler creates an environment HTTP handler using the provided environment store.
func NewHandler(r domain.EnvironmentStore) *Handler {
	return &Handler{repo: r}
}

// EnvSavePayload defines the JSON request schema for environment mutations.
type EnvSavePayload struct {
	Name        string `json:"name" binding:"required" example:"production"`
	Description string `json:"description" example:"Primary cluster management environment"`
	// Protected is optional: absent keeps the current value (false on create).
	// Only the workspace owner may set it to false on a protected environment.
	Protected *bool `json:"protected,omitempty"`
}

// Create godoc
// @Summary Create an environment
// @Tags Environments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body EnvSavePayload true "Environment details"
// @Success 201 {object} domain.Environment
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Failure 500 {object} httperr.Body
// @Router /api/v1/environments [post]
func (h *Handler) Create(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	var body EnvSavePayload
	if err := c.Bind(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		return httperr.Status(c, http.StatusBadRequest, "invalid environment request")
	}

	env := &domain.Environment{WorkspaceID: principal.WorkspaceID, Name: strings.TrimSpace(body.Name), Description: strings.TrimSpace(body.Description), Protected: body.Protected != nil && *body.Protected}
	if err := h.repo.CreateEnvironment(c.Request().Context(), env); err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusCreated, env)
}

// List godoc
// @Summary List environments
// @Tags Environments
// @Produce json
// @Security BearerAuth
// @Success 200 {array} domain.Environment
// @Failure 401 {object} httperr.Body
// @Failure 500 {object} httperr.Body
// @Router /api/v1/environments [get]
func (h *Handler) List(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	list, err := h.repo.ListEnvironments(c.Request().Context(), principal.WorkspaceID)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, list)
}

// Update godoc
// @Summary Update an environment
// @Tags Environments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Environment ID"
// @Param request body EnvSavePayload true "Updated environment details"
// @Success 200 {object} domain.Environment
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Failure 500 {object} httperr.Body
// @Router /api/v1/environments/{id} [put]
func (h *Handler) Update(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		return httperr.Status(c, http.StatusBadRequest, "invalid environment ID")
	}

	var body EnvSavePayload
	if err := c.Bind(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		return httperr.Status(c, http.StatusBadRequest, "invalid environment request")
	}

	current, err := h.repo.GetEnvironmentByID(c.Request().Context(), principal.WorkspaceID, id)
	if err != nil {
		return respondError(c, err)
	}
	protected := current.Protected
	if body.Protected != nil {
		protected = *body.Protected
	}
	if current.Protected && !protected && principal.Role != domain.RoleOwner {
		return respondError(c, domain.ErrEnvironmentUnprotectOwnerOnly)
	}
	env := &domain.Environment{ID: id, WorkspaceID: principal.WorkspaceID, Name: strings.TrimSpace(body.Name), Description: strings.TrimSpace(body.Description), Protected: protected}
	if err := h.repo.UpdateEnvironment(c.Request().Context(), env); err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, env)
}

// Delete godoc
// @Summary Delete an environment
// @Tags Environments
// @Produce json
// @Security BearerAuth
// @Param id path int true "Environment ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 500 {object} httperr.Body
// @Router /api/v1/environments/{id} [delete]
func (h *Handler) Delete(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		return httperr.Status(c, http.StatusBadRequest, "invalid environment ID")
	}

	if err := h.repo.DeleteEnvironment(c.Request().Context(), principal.WorkspaceID, id); err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "Environment structure removed successfully"})
}
