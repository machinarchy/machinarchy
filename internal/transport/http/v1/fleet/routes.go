package fleet

import "github.com/labstack/echo/v4"

// RegisterRoutes registers the fleet API endpoints on the provided Echo group.
func RegisterRoutes(g *echo.Group, h *Handler) {
	g.POST("/targets/preview", h.Preview)
}
