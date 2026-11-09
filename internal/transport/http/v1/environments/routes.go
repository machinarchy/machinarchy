package environments

import "github.com/labstack/echo/v4"

// RegisterRoutes registers the environment API endpoints on the provided
// read and write Echo groups.
func RegisterRoutes(read, write *echo.Group, h *Handler) {
	write.POST("", h.Create)
	read.GET("", h.List)
	write.PUT("/:id", h.Update)
	write.DELETE("/:id", h.Delete)
}
