package configuration

import (
	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// RegisterRoutes mounts /runtime-config on the authenticated v1 group. Runtime
// configuration needs a human workspace owner.
func RegisterRoutes(v1 *echo.Group, h *Handler) {
	g := v1.Group("/runtime-config", middleware.RequireOwner(), middleware.RequireHuman())
	g.GET("", h.Current)
	g.GET("/revisions", h.List)
	g.GET("/revisions/:revision", h.Get)
	g.POST("/validate", h.Validate)
	g.PUT("", h.Publish)
	g.POST("/rollback", h.Rollback)
	g.GET("/status", h.Status)
}
