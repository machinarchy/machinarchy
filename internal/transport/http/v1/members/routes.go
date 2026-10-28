package members

import "github.com/labstack/echo/v4"

func RegisterRoutes(g *echo.Group, h *Handler) {
	g.GET("", h.List)
	g.PUT("/:id/role", h.SetRole)
}
