package job

import "github.com/labstack/echo/v4"

// RegisterRoutes registers global job endpoints under /api/v1/jobs
func RegisterRoutes(g *echo.Group, h *Handler) {
	g.GET("", h.List)
	g.GET("/:id", h.GetByID)
}
