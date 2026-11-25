package auth

import (
	"github.com/labstack/echo/v4"
)

// RegisterRoutes mounts the public /auth routes on the API group (no
// authentication middleware).
func RegisterRoutes(api *echo.Group, h *Handler) {
	g := api.Group("/auth")
	g.POST("/signup", h.SignUp)
	g.POST("/login", h.Login)
	g.POST("/logout", h.Logout)
}

// RegisterSessionRoutes mounts the authenticated self-service routes (the
// caller's profile, sessions and password) on the v1 group.
func RegisterSessionRoutes(v1 *echo.Group, h *Handler) {
	v1.GET("/me", h.GetMe)
	v1.GET("/sessions", h.ListSessions)
	v1.DELETE("/sessions", h.RevokeAllSessions)
	v1.DELETE("/sessions/:id", h.RevokeSession)
	v1.POST("/me/password", h.ChangePassword)
}
