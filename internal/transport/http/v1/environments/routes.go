package environments

import (
	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// RegisterRoutes mounts /environments on the authenticated v1 group: reading
// needs inventory:read, changing it inventory:write.
func RegisterRoutes(v1 *echo.Group, h *Handler) {
	read := v1.Group("/environments", middleware.RequirePermission(domain.PermissionInventoryRead))
	write := v1.Group("/environments", middleware.RequirePermission(domain.PermissionInventoryWrite))
	read.GET("", h.List)
	write.POST("", h.Create)
	write.PUT("/:id", h.Update)
	write.DELETE("/:id", h.Delete)
}
