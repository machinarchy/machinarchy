package tokens

import (
	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// RegisterRoutes mounts /tokens on the authenticated v1 group. Token
// management needs a human session.
func RegisterRoutes(v1 *echo.Group, h *Handler) {
	g := v1.Group("/tokens", middleware.RequirePermission(domain.PermissionTokenManage), middleware.RequireHuman())
	g.POST("", h.Create)
	g.GET("", h.List)
	g.DELETE("/:id", h.Revoke)
}
