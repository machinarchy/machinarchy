package history

import "github.com/labstack/echo/v4"

// RegisterRoutes registers the history API endpoints on the provided Echo group.
func RegisterRoutes(g *echo.Group, h *Handler) {
	g.GET("", h.GetHistory)
}
