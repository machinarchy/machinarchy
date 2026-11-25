// Package tokens provides HTTP handlers and routes for API token management.
package tokens

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// Handler handles HTTP requests for API token management operations.
type Handler struct {
	service domain.APITokenService
}

// CreateRequest represents the payload required to create an API token.
type CreateRequest struct {
	Name           string `json:"name" example:"ci-deploy"`
	Role           string `json:"role" enums:"operator,reader" example:"reader"`
	ExpiresInHours int    `json:"expiresInHours,omitempty" example:"720"`
}

// NewHandler creates an API token HTTP handler using the provided token service.
func NewHandler(service domain.APITokenService) *Handler {
	return &Handler{service: service}
}

// Create issues a workspace-scoped API token. The plaintext token is returned
// once and is not stored by the server.
// @Summary Create an API token
// @Description Requires a human workspace owner session. Tokens are limited to operator or reader and their effective access cannot exceed the owner's current workspace membership role. Plaintext is returned only in this response.
// @Tags API tokens
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body CreateRequest true "Token name, role, and lifetime"
// @Success 201 {object} domain.IssuedAPIToken
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/tokens [post]
func (h *Handler) Create(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	var request CreateRequest
	if err := c.Bind(&request); err != nil {
		return httperr.Status(c, http.StatusBadRequest, "invalid request body")
	}
	token, err := h.service.Create(c.Request().Context(), principal,
		request.Name, domain.Role(strings.ToLower(strings.TrimSpace(request.Role))), request.ExpiresInHours)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusCreated, token)
}

// List returns token metadata for the authenticated user's workspace.
// @Summary List API tokens
// @Description Requires a human workspace owner session. Returns metadata for tokens created by the current user; plaintext secrets are never returned.
// @Tags API tokens
// @Produce json
// @Security BearerAuth
// @Success 200 {array} domain.APIToken
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/tokens [get]
func (h *Handler) List(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	tokens, err := h.service.List(c.Request().Context(), principal)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, tokens)
}

// Revoke disables an API token immediately.
// @Summary Revoke an API token
// @Description Requires a human workspace owner session. The revoked token is rejected on its next request.
// @Tags API tokens
// @Produce json
// @Security BearerAuth
// @Param id path int true "API token ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Router /api/v1/tokens/{id} [delete]
func (h *Handler) Revoke(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return httperr.Status(c, http.StatusBadRequest, "invalid token ID")
	}
	if err := h.service.Revoke(c.Request().Context(), principal, id); err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "API token revoked"})
}

var errorMappings = []httperr.Mapping{
	httperr.Map(domain.ErrInvalidInput, http.StatusBadRequest, "invalid API token request"),
	{Target: domain.ErrAPITokenAlreadyExists, Status: http.StatusConflict, Code: httperr.CodeAlreadyExists, Message: "an API token with that name already exists"},
	httperr.Map(domain.ErrAPITokenNotFound, http.StatusNotFound, "API token not found"),
}

// respondError writes err as an API error and logs unexpected ones.
func respondError(c echo.Context, err error) error {
	return httperr.FromError(c, err, errorMappings...)
}
