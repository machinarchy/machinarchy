package secret

import "github.com/labstack/echo/v4"

func RegisterRoutes(g *echo.Group, h *Handler) {
	g.GET("", h.List)
	g.POST("", h.Create)
	g.POST("/rewrap", h.Rewrap)
	g.GET("/:id", h.Describe)
	g.GET("/:id/versions", h.Versions)
	g.POST("/:id/rotate", h.Rotate)
	g.DELETE("/:id", h.Delete)
}
