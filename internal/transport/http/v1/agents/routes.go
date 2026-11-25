package agents

import (
	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// RegisterRoutes mounts /agents on the authenticated v1 group: reading needs
// inventory:read, everything that changes agents or keys the workspace owner.
// Agent work runs as capability Operations (/api/v1/managed/operations), not
// here (ADR-057).
func RegisterRoutes(v1 *echo.Group, h *Handler) {
	read := v1.Group("/agents", middleware.RequirePermission(domain.PermissionInventoryRead))
	owner := v1.Group("/agents", middleware.RequireOwner(), middleware.RequireHuman())
	read.GET("", h.List)
	read.GET("/controller-key", h.ControllerKey)
	owner.POST("/:id/revoke", h.Revoke)
	owner.POST("/enrollment-tokens", h.CreateToken)
	owner.GET("/enrollment-tokens", h.ListTokens)
	owner.DELETE("/enrollment-tokens/:id", h.RevokeToken)
	owner.GET("/signing-keys", h.ListSigningKeys)
	owner.POST("/signing-keys/next", h.StageSigningKey)
	owner.POST("/signing-keys/next/activate", h.ActivateSigningKey)
	owner.DELETE("/signing-keys/next", h.DiscardSigningKey)
}
