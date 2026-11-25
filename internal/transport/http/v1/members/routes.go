package members

import (
	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// RegisterRoutes mounts /members on the authenticated v1 group. Member
// administration is owner-only and API tokens are refused.
func RegisterRoutes(v1 *echo.Group, h *Handler) {
	g := v1.Group("/members", middleware.RequirePermission(domain.PermissionMembersManage), middleware.RequireHuman())
	g.GET("", h.List)
	g.PUT("/:id/role", h.SetRole)
}
