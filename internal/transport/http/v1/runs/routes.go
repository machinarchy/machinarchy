package runs

import "github.com/labstack/echo/v4"

// RegisterRoutes mounts the read routes on read and the routes that start or
// stop commands on execute, which carries the runs:execute guard.
func RegisterRoutes(read, execute *echo.Group, h *Handler) {
	read.GET("", h.List)
	read.GET("/:id", h.Get)
	execute.POST("", h.Submit)
	execute.POST("/:id/cancel", h.Cancel)
}
