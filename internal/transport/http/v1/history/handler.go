package history

import (
	"errors"
	"net/http"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"

	"github.com/labstack/echo/v4"
)

// Handler contains dependencies injected at runtime
type Handler struct {
	metricsService domain.MetricsService
	store          domain.HostStore
}

// NewHandler acts as the constructor
func NewHandler(ms domain.MetricsService, hs domain.HostStore) *Handler {
	return &Handler{
		metricsService: ms,
		store:          hs,
	}
}

// GetHistory returns recent CPU and memory metrics for the requested range.
// @Summary Get historical metrics
// @Tags Metrics
// @Produce json
// @Security BearerAuth
// @Param range query string false "Supported time ranges: 1h, 5h, or 24h" default(1h)
// @Success 200 {array} domain.DataPoint
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/history [get]
func (h *Handler) GetHistory(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	// 1. Extract inputs from delivery layer
	timeRange := c.QueryParam("range")
	if timeRange == "" {
		timeRange = "1h" // Explicitly assign a smart default fallback
	}

	// 2. Delegate data gathering down to the service interface
	ctx := c.Request().Context()
	data, err := h.metricsService.GetHistory(ctx, principal.WorkspaceID, timeRange)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidInput) {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "unsupported metrics range"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "unable to retrieve metrics history"})
	}

	// 3. Return a clean delivery format
	return c.JSON(http.StatusOK, data)
}
