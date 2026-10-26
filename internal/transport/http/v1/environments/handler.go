package environments

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
)

type Handler struct {
	repo domain.EnvironmentStore
}

func NewHandler(r domain.EnvironmentStore) *Handler {
	return &Handler{repo: r}
}

// EnvSavePayload defines the JSON request schema for environment mutations.
type EnvSavePayload struct {
	Name        string `json:"name" binding:"required" example:"production"`
	Description string `json:"description" example:"Primary cluster management environment"`
}

// Create godoc
// @Summary Create an environment
// @Tags Environments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body EnvSavePayload true "Environment details"
// @Success 201 {object} domain.Environment
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/environments [post]
func (h *Handler) Create(c echo.Context) error {
	var body EnvSavePayload
	if err := c.Bind(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid environment metadata parameters"})
	}

	env := &domain.Environment{Name: strings.TrimSpace(body.Name), Description: strings.TrimSpace(body.Description)}
	if err := h.repo.CreateEnvironment(c.Request().Context(), env); err != nil {
		if errors.Is(err, domain.ErrEnvironmentAlreadyExists) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "Environment name is already registered"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Unable to create environment"})
	}
	return c.JSON(http.StatusCreated, env)
}

// List godoc
// @Summary List environments
// @Tags Environments
// @Produce json
// @Security BearerAuth
// @Success 200 {array} domain.Environment
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/environments [get]
func (h *Handler) List(c echo.Context) error {
	list, err := h.repo.ListEnvironments(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Unable to retrieve environments"})
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
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/environments/{id} [put]
func (h *Handler) Update(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid URL request ID format"})
	}

	var body EnvSavePayload
	if err := c.Bind(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid mutation updates layout"})
	}

	env := &domain.Environment{ID: id, Name: strings.TrimSpace(body.Name), Description: strings.TrimSpace(body.Description)}
	if err := h.repo.UpdateEnvironment(c.Request().Context(), env); err != nil {
		if errors.Is(err, domain.ErrEnvironmentNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "Environment not found"})
		}
		if errors.Is(err, domain.ErrEnvironmentAlreadyExists) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "Environment name is already registered"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Unable to update environment"})
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
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/environments/{id} [delete]
func (h *Handler) Delete(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid URL delete parameters ID target"})
	}

	if err := h.repo.DeleteEnvironment(c.Request().Context(), id); err != nil {
		if errors.Is(err, domain.ErrEnvironmentNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "Environment not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Unable to delete environment"})
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "Environment structure removed successfully"})
}
