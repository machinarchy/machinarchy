package auth

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
)

type Handler struct {
	authService  domain.AuthService
	secureCookie bool
}

func NewHandler(as domain.AuthService, secureCookie bool) *Handler {
	return &Handler{authService: as, secureCookie: secureCookie}
}

type SignUpRequest struct {
	Username string `json:"username" binding:"required" example:"operator"`
	Email    string `json:"email" example:"operator@example.com"`
	Password string `json:"password" binding:"required" example:"use-a-secret-with-12+-bytes"`
}

type LoginRequest struct {
	Email    string `json:"email,omitempty" example:"operator@example.com"`
	Username string `json:"username,omitempty" example:"operator"`
	Password string `json:"password" binding:"required" example:"account-password"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type MessageResponse struct {
	Message string `json:"message"`
}

func setAuthCookie(c echo.Context, token string, secure bool) {
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
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/auth/signup [post]
func (h *Handler) SignUp(c echo.Context) error {
	var req SignUpRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "Invalid request payload"})
	}

	if req.Username == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "Username and password are required"})
	}

	if len([]byte(req.Password)) < 12 || len([]byte(req.Password)) > 72 {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "Password must be between 12 and 72 bytes"})
	}

	ctx := c.Request().Context()
	res, err := h.authService.SignUp(ctx, domain.AuthSignUpRequest{
		Username: req.Username,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		if errors.Is(err, domain.ErrUserAlreadyExists) {
			return c.JSON(http.StatusConflict, ErrorResponse{Error: "Username or email is already registered"})
		}
		if errors.Is(err, domain.ErrInvalidInput) {
			return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "Invalid account details"})
		}
		return c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "Unable to create account"})
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
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/auth/login [post]
func (h *Handler) Login(c echo.Context) error {
	var req LoginRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "Invalid request body"})
	}

	if req.Password == "" || (req.Email == "" && req.Username == "") {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "Missing validation credentials"})
	}

	ctx := c.Request().Context()
	res, err := h.authService.Login(ctx, domain.AuthLoginRequest{
		Username: req.Username,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			return c.JSON(http.StatusUnauthorized, ErrorResponse{Error: "Invalid credentials"})
		}
		return c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "Unable to authenticate"})
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
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/me [get]
func (h *Handler) GetMe(c echo.Context) error {
	claims, ok := c.Get("user").(*domain.JWTCustomClaims)
	if !ok || claims == nil {
		return c.JSON(http.StatusUnauthorized, ErrorResponse{Error: "Unauthorized session configuration"})
	}

	ctx := c.Request().Context()
	userResp, err := h.authService.VerifySession(ctx, claims.Username)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			return c.JSON(http.StatusUnauthorized, ErrorResponse{Error: "Session user is no longer active"})
		}
		return c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "Unable to verify session"})
	}

	return c.JSON(http.StatusOK, userResp)
}

// Logout clears the session cookie.
// @Summary Log out
// @Tags Authentication
// @Produce json
// @Success 200 {object} MessageResponse
// @Router /api/auth/logout [post]
func (h *Handler) Logout(c echo.Context) error {
	clearAuthCookie(c, h.secureCookie)
	return c.JSON(http.StatusOK, MessageResponse{Message: "Operation completed successfully"})
}
