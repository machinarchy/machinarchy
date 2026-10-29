package host

import "github.com/labstack/echo/v4"

func RegisterRoutes(read, write *echo.Group, h *Handler) {
	write.POST("", h.Create)
	write.POST("/bulk", h.BulkCreate)
	read.GET("", h.List)
	write.PUT("/:id", h.Update)
	write.PATCH("/:id/labels", h.PatchLabels)
	write.DELETE("/:id", h.Delete)
	read.GET("/:id/jobs", h.ListJobsForHost)
}
