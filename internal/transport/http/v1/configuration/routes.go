package configuration

import "github.com/labstack/echo/v4"

func RegisterRoutes(g *echo.Group, h *Handler) {
	g.GET("", h.Current)
	g.GET("/revisions", h.List)
	g.GET("/revisions/:revision", h.Get)
	g.POST("/validate", h.Validate)
	g.PUT("", h.Publish)
	g.POST("/rollback", h.Rollback)
	g.GET("/status", h.Status)
}
