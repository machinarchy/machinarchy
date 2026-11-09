package tokens

import "github.com/labstack/echo/v4"

// RegisterRoutes registers the API token endpoints on the provided Echo group.
func RegisterRoutes(g *echo.Group, h *Handler) {
	g.POST("", h.Create)
	g.GET("", h.List)
	g.DELETE("/:id", h.Revoke)
}
