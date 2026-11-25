package secret

import (
	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// RegisterRoutes mounts /secrets on the authenticated v1 group. The Secret
// Store needs a human workspace owner.
func RegisterRoutes(v1 *echo.Group, h *Handler) {
	g := v1.Group("/secrets", middleware.RequireOwner(), middleware.RequireHuman())
	g.GET("", h.List)
	g.POST("", h.Create)
	g.POST("/rewrap", h.Rewrap)
	g.GET("/:id", h.Describe)
	g.GET("/:id/versions", h.Versions)
	g.POST("/:id/rotate", h.Rotate)
	g.DELETE("/:id", h.Delete)
}
