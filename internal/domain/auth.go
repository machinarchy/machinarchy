// Package domain defines the core domain models and interfaces for the application.
package domain

import "context"

// AuthSignUpRequest represents the request payload for user sign-up.
type AuthSignUpRequest struct {
	Username string
	Email    string
	Password string
}

// AuthLoginRequest represents the request payload for user login.
type AuthLoginRequest struct {
	Username string
	Email    string
	Password string
}

// AuthTokenResponse represents the response payload for authentication tokens.
type AuthTokenResponse struct {
	Token string       `json:"token"`
	User  UserResponse `json:"user"`
}

// AuthService defines the interface for authentication-related operations.
type AuthService interface {
	SignUp(ctx context.Context, req AuthSignUpRequest) (*AuthTokenResponse, error)
	Login(ctx context.Context, req AuthLoginRequest) (*AuthTokenResponse, error)
	VerifySession(ctx context.Context, username string) (*UserResponse, error)
}
