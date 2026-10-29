package fleet

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

type Handler struct {
	targets domain.TargetService
}

func NewHandler(targets domain.TargetService) *Handler {
	return &Handler{targets: targets}
}

// PreviewRequest describes which hosts to preview. At least one of selector,
// include, or all is required; an empty request is an error, never "all hosts".
type PreviewRequest struct {
	// Selector is a label selector, for example environment=production,role in (web,db),!canary.
	Selector string `json:"selector,omitempty" example:"environment=production,role=web"`
	// Include adds hosts by ID or name regardless of the selector.
	Include []string `json:"include,omitempty"`
	// Exclude removes hosts by ID or name, always winning over selector and include.
	Exclude []string `json:"exclude,omitempty"`
	// All explicitly targets every host; it cannot be combined with selector or include.
	All bool `json:"all,omitempty"`
}

// Preview shows which hosts a target rule matches without touching any host.
// @Summary Preview fleet targets
// @Description Resolves a selector with explicit include and exclude lists against the workspace inventory and lists the matching hosts (at most 500; total is always exact), the hosts removed by the exclude list, and include/exclude entries that matched no host. Nothing is contacted or changed, and no credentials are returned.
// @Tags Fleet
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body PreviewRequest true "Target rule"
// @Success 200 {object} domain.TargetPreview
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/fleet/targets/preview [post]
func (h *Handler) Preview(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	var request PreviewRequest
	if err := c.Bind(&request); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	preview, err := h.targets.Preview(c.Request().Context(), principal, domain.TargetRule{
		Selector: request.Selector, Include: request.Include, Exclude: request.Exclude, All: request.All,
	})
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, domain.ErrPermissionDenied):
		return c.JSON(http.StatusForbidden, map[string]string{"error": "permission denied"})
	case err != nil:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "unable to resolve targets"})
	}
	return c.JSON(http.StatusOK, preview)
}
