// Package auth provides HTTP handlers and routes for authentication operations,
// including registration, login, and session management.
package auth

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
)

// Handler handles HTTP requests for authentication operations.
type Handler struct {
	authService    domain.AuthService
	secureCookie   bool
	signupEnabled  atomic.Bool
	signupProvider func() bool
}

// NewHandler creates an authentication HTTP handler using the provided
// authentication service and cookie and signup settings.
func NewHandler(as domain.AuthService, secureCookie, signupEnabled bool) *Handler {
	h := &Handler{authService: as, secureCookie: secureCookie}
	h.signupEnabled.Store(signupEnabled)
	return h
}

func (h *Handler) SetSignupEnabledProvider(provider func() bool) { h.signupProvider = provider }

// SignUpRequest represents the payload required to register a new user.
type SignUpRequest struct {
	Username string `json:"username" binding:"required" example:"operator"`
	Email    string `json:"email" example:"operator@example.com"`
	Password string `json:"password" binding:"required" example:"use-a-secret-with-12+-bytes"`
}

// LoginRequest represents the credentials submitted during authentication.
type LoginRequest struct {
	Email    string `json:"email,omitempty" example:"operator@example.com"`
	Username string `json:"username,omitempty" example:"operator"`
	Password string `json:"password" binding:"required" example:"account-password"`
}

// ErrorResponse is the API error document (see package httperr).
type ErrorResponse = httperr.Body

// MessageResponse represents a simple API response containing a message.
type MessageResponse struct {
	Message string `json:"message"`
}

func setAuthCookie(c echo.Context, token string, secure bool) {
	// #nosec G124 -- Secure is controlled by the application's HTTPS configuration.
	cookie := &http.Cookie{
		Name:     "auth_token",
		Value:    token,
		Expires:  time.Now().Add(24 * time.Hour),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	}

	c.SetCookie(cookie)
}

func clearAuthCookie(c echo.Context, secure bool) {
	// #nosec G124 -- Secure matches the application's HTTPS configuration.
	cookie := &http.Cookie{
		Name:     "auth_token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Now().Add(-1 * time.Hour),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}

	c.SetCookie(cookie)
}

// SignUp creates a user account and sets the session cookie.
// @Summary Create an account
// @Description Creates an account, returns a JWT, and sets the auth_token cookie.
// @Tags Authentication
// @Accept json
// @Produce json
// @Param request body SignUpRequest true "Account details"
// @Success 201 {object} domain.AuthTokenResponse
// @Failure 400 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Failure 500 {object} httperr.Body
// @Router /api/auth/signup [post]
func (h *Handler) SignUp(c echo.Context) error {
	enabled := h.signupEnabled.Load()
	if h.signupProvider != nil {
		enabled = h.signupProvider()
	}
	if !enabled {
		return httperr.Status(c, http.StatusForbidden, "Account registration is disabled")
	}

	var req SignUpRequest
	if err := c.Bind(&req); err != nil {
		return httperr.Status(c, http.StatusBadRequest, "Invalid request payload")
	}

	if req.Username == "" || req.Password == "" {
		return httperr.Status(c, http.StatusBadRequest, "Username and password are required")
	}

	if len([]byte(req.Password)) < 12 || len([]byte(req.Password)) > 72 {
		return httperr.Status(c, http.StatusBadRequest, "Password must be between 12 and 72 bytes")
	}

	ctx := c.Request().Context()
	res, err := h.authService.SignUp(ctx, domain.AuthSignUpRequest{
		Client:   clientInfo(c),
		Username: req.Username,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		if errors.Is(err, domain.ErrUserAlreadyExists) {
			return httperr.Status(c, http.StatusConflict, "Username or email is already registered")
		}
		if errors.Is(err, domain.ErrInvalidInput) {
			return httperr.Status(c, http.StatusBadRequest, "Invalid account details")
		}
		return httperr.Status(c, http.StatusInternalServerError, "Unable to create account")
	}

	setAuthCookie(c, res.Token, h.secureCookie)
	return c.JSON(http.StatusCreated, res)
}

// Login authenticates a user by email or username.
// @Summary Log in
// @Description Authenticates an account, returns a JWT, and sets the auth_token cookie.
// @Tags Authentication
// @Accept json
// @Produce json
// @Param request body LoginRequest true "Credentials"
// @Success 200 {object} domain.AuthTokenResponse
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 500 {object} httperr.Body
// @Router /api/auth/login [post]
func (h *Handler) Login(c echo.Context) error {
	var req LoginRequest
	if err := c.Bind(&req); err != nil {
		return httperr.Status(c, http.StatusBadRequest, "Invalid request body")
	}

	if req.Password == "" || (req.Email == "" && req.Username == "") {
		return httperr.Status(c, http.StatusBadRequest, "Missing validation credentials")
	}

	ctx := c.Request().Context()
	res, err := h.authService.Login(ctx, domain.AuthLoginRequest{
		Client:   clientInfo(c),
		Username: req.Username,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			return httperr.Status(c, http.StatusUnauthorized, "Invalid credentials")
		}
		return httperr.Status(c, http.StatusInternalServerError, "Unable to authenticate")
	}

	setAuthCookie(c, res.Token, h.secureCookie)
	return c.JSON(http.StatusOK, res)
}

// GetMe returns the currently authenticated user's profile.
// @Summary Get current user
// @Tags Authentication
// @Produce json
// @Security BearerAuth
// @Success 200 {object} domain.UserResponse
// @Failure 401 {object} httperr.Body
// @Failure 500 {object} httperr.Body
// @Router /api/v1/me [get]
func (h *Handler) GetMe(c echo.Context) error {
	username, ok := c.Get("username").(string)
	if !ok || username == "" {
		return httperr.Status(c, http.StatusUnauthorized, "Unauthorized session configuration")
	}

	ctx := c.Request().Context()
	userResp, err := h.authService.VerifySession(ctx, username)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			return httperr.Status(c, http.StatusUnauthorized, "Session user is no longer active")
		}
		return httperr.Status(c, http.StatusInternalServerError, "Unable to verify session")
	}

	return c.JSON(http.StatusOK, userResp)
}

// Logout ends the current session: the session is revoked on the server, so a
// copy of the token stops working too, and the cookie is cleared.
// @Summary Log out
// @Description Revokes the session of the token sent (cookie or bearer) and clears the cookie. Succeeds even without a valid session.
// @Tags Authentication
// @Produce json
// @Success 200 {object} MessageResponse
// @Failure 500 {object} httperr.Body
// @Router /api/auth/logout [post]
func (h *Handler) Logout(c echo.Context) error {
	clearAuthCookie(c, h.secureCookie)
	if err := h.authService.Logout(c.Request().Context(), sessionToken(c)); err != nil {
		return httperr.Status(c, http.StatusInternalServerError, "Unable to end the session")
	}
	return c.JSON(http.StatusOK, MessageResponse{Message: "Operation completed successfully"})
}

// sessionToken returns the login-session token of the request (bearer first,
// then cookie); API tokens are ignored.
func sessionToken(c echo.Context) string {
	if value, ok := strings.CutPrefix(c.Request().Header.Get(echo.HeaderAuthorization), "Bearer "); ok {
		if value = strings.TrimSpace(value); value != "" && !strings.HasPrefix(value, "mch_pat_") {
			return value
		}
	}
	if cookie, err := c.Cookie("auth_token"); err == nil {
		return cookie.Value
	}
	return ""
}

// clientInfo is recorded with a new session.
func clientInfo(c echo.Context) domain.ClientInfo {
	return domain.ClientInfo{IP: c.RealIP(), UserAgent: c.Request().UserAgent()}
}

// SessionsResponse lists the caller's active sessions.
type SessionsResponse struct {
	Items []domain.Session `json:"items"`
}

// RevokedResponse reports how many sessions ended.
type RevokedResponse struct {
	Revoked int `json:"revoked"`
}

// PasswordChangeRequest changes the caller's password.
type PasswordChangeRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// ListSessions returns the caller's active login sessions.
// @Summary List my sessions
// @Description The caller's active login sessions, newest first; "current" marks the one used for this request. Needs a login session (API tokens are refused).
// @Tags Authentication
// @Produce json
// @Security BearerAuth
// @Success 200 {object} SessionsResponse
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/sessions [get]
func (h *Handler) ListSessions(c echo.Context) error {
	sessions, err := h.authService.ListSessions(c.Request().Context(), principalOf(c))
	if err != nil {
		return sessionError(c, err)
	}
	return c.JSON(http.StatusOK, SessionsResponse{Items: sessions})
}

// RevokeSession ends one of the caller's sessions.
// @Summary Revoke one of my sessions
// @Tags Authentication
// @Produce json
// @Security BearerAuth
// @Param id path string true "Session ID"
// @Success 200 {object} MessageResponse
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Router /api/v1/sessions/{id} [delete]
func (h *Handler) RevokeSession(c echo.Context) error {
	principal := principalOf(c)
	id := c.Param("id")
	if err := h.authService.RevokeSession(c.Request().Context(), principal, id); err != nil {
		return sessionError(c, err)
	}
	if id == principal.SessionID {
		clearAuthCookie(c, h.secureCookie)
	}
	return c.JSON(http.StatusOK, MessageResponse{Message: "Session revoked"})
}

// RevokeAllSessions ends every session of the caller, including this one.
// @Summary Log out everywhere
// @Description Revokes every login session of the caller, including the current one. API tokens are not affected.
// @Tags Authentication
// @Produce json
// @Security BearerAuth
// @Success 200 {object} RevokedResponse
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/sessions [delete]
func (h *Handler) RevokeAllSessions(c echo.Context) error {
	revoked, err := h.authService.RevokeAllSessions(c.Request().Context(), principalOf(c))
	if err != nil {
		return sessionError(c, err)
	}
	clearAuthCookie(c, h.secureCookie)
	return c.JSON(http.StatusOK, RevokedResponse{Revoked: revoked})
}

// ChangePassword replaces the caller's password and ends all their sessions.
// @Summary Change my password
// @Description Checks the current password, stores the new one (12–72 bytes) and revokes every login session of the caller, including this one: log in again. API tokens are not affected.
// @Tags Authentication
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body PasswordChangeRequest true "Current and new password"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/me/password [post]
func (h *Handler) ChangePassword(c echo.Context) error {
	var req PasswordChangeRequest
	if err := c.Bind(&req); err != nil || req.CurrentPassword == "" || req.NewPassword == "" {
		return httperr.Status(c, http.StatusBadRequest, "currentPassword and newPassword are required")
	}
	err := h.authService.ChangePassword(c.Request().Context(), principalOf(c), req.CurrentPassword, req.NewPassword)
	if errors.Is(err, domain.ErrInvalidCredentials) {
		return httperr.Status(c, http.StatusForbidden, "the current password is incorrect")
	}
	if err != nil {
		return sessionError(c, err)
	}
	clearAuthCookie(c, h.secureCookie)
	return c.JSON(http.StatusOK, MessageResponse{Message: "Password changed; every session was ended, log in again"})
}

func principalOf(c echo.Context) domain.Principal {
	principal, _ := c.Get("principal").(domain.Principal)
	return principal
}

func sessionError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, domain.ErrPermissionDenied):
		return httperr.Status(c, http.StatusForbidden, "a login session is required; API tokens cannot manage sessions or passwords")
	case errors.Is(err, domain.ErrSessionNotFound):
		return httperr.Status(c, http.StatusNotFound, "session not found")
	case errors.Is(err, domain.ErrInvalidInput):
		return httperr.Status(c, http.StatusBadRequest, err.Error())
	default:
		return httperr.Status(c, http.StatusInternalServerError, "unable to manage sessions")
	}
}

// RegisterSessionRoutes adds the authenticated self-service routes.
func RegisterSessionRoutes(g *echo.Group, h *Handler) {
	g.GET("/sessions", h.ListSessions)
	g.DELETE("/sessions", h.RevokeAllSessions)
	g.DELETE("/sessions/:id", h.RevokeSession)
	g.POST("/me/password", h.ChangePassword)
}
