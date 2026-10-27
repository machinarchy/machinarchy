package tokens

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

type Handler struct {
	service domain.APITokenService
}

type CreateRequest struct {
	Name           string `json:"name" example:"ci-deploy"`
	Role           string `json:"role" enums:"operator,reader" example:"reader"`
	ExpiresInHours int    `json:"expiresInHours,omitempty" example:"720"`
}

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
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /api/v1/tokens [post]
func (h *Handler) Create(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	var request CreateRequest
	if err := c.Bind(&request); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	token, err := h.service.Create(c.Request().Context(), principal,
		request.Name, domain.Role(strings.ToLower(strings.TrimSpace(request.Role))), request.ExpiresInHours)
	if err != nil {
		return tokenError(c, err)
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
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /api/v1/tokens [get]
func (h *Handler) List(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	tokens, err := h.service.List(c.Request().Context(), principal)
	if err != nil {
		return tokenError(c, err)
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
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/v1/tokens/{id} [delete]
func (h *Handler) Revoke(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid token ID"})
	}
	if err := h.service.Revoke(c.Request().Context(), principal, id); err != nil {
		return tokenError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "API token revoked"})
}

func tokenError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid API token request"})
	case errors.Is(err, domain.ErrPermissionDenied):
		return c.JSON(http.StatusForbidden, map[string]string{"error": "permission denied"})
	case errors.Is(err, domain.ErrAPITokenAlreadyExists):
		return c.JSON(http.StatusConflict, map[string]string{"error": "an API token with that name already exists"})
	case errors.Is(err, domain.ErrAPITokenNotFound):
		return c.JSON(http.StatusNotFound, map[string]string{"error": "API token not found"})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "unable to process API token request"})
	}
}
