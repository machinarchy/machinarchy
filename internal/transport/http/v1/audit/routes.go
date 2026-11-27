package audit

import (
	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// RegisterRoutes mounts /audit on the authenticated v1 group. The audit trail
// is owner-only and API tokens are refused.
func RegisterRoutes(v1 *echo.Group, h *Handler) {
	g := v1.Group("/audit", middleware.RequirePermission(domain.PermissionAuditRead), middleware.RequireHuman())
	g.GET("/events", h.List)
	g.GET("/verify", h.Verify)
	g.POST("/prune", h.Prune)
}
