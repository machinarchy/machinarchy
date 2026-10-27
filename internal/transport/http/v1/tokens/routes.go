package tokens

import "github.com/labstack/echo/v4"

func RegisterRoutes(g *echo.Group, h *Handler) {
	g.POST("", h.Create)
	g.GET("", h.List)
	g.DELETE("/:id", h.Revoke)
}
