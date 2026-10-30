package audit

import "github.com/labstack/echo/v4"

func RegisterRoutes(g *echo.Group, h *Handler) {
	g.GET("/events", h.List)
	g.GET("/verify", h.Verify)
}
