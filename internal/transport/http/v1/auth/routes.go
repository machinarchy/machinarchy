package auth

import "github.com/labstack/echo/v4"

func RegisterRoutes(g *echo.Group, h *Handler) {
	g.POST("/signup", h.SignUp)
	g.POST("/login", h.Login)
	g.POST("/logout", h.Logout)
}