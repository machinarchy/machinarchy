package auth

import "github.com/labstack/echo/v4"

// RegisterRoutes registers the authentication API endpoints on the provided Echo group.
func RegisterRoutes(g *echo.Group, h *Handler) {
	g.POST("/signup", h.SignUp)
	g.POST("/login", h.Login)
	g.POST("/logout", h.Logout)
}
