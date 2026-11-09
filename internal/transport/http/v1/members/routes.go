package members

import "github.com/labstack/echo/v4"

// RegisterRoutes registers the workspace member API endpoints on the provided Echo group.
func RegisterRoutes(g *echo.Group, h *Handler) {
	g.GET("", h.List)
	g.PUT("/:id/role", h.SetRole)
}
