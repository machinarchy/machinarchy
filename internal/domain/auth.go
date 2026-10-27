// internal/domain/auth.go
package domain

import "context"

type AuthSignUpRequest struct {
	Username string
	Email    string
	Password string
}

type AuthLoginRequest struct {
	Username string
	Email    string
	Password string
}

type AuthTokenResponse struct {
	Token string       `json:"token"`
	User  UserResponse `json:"user"`
}

type AuthService interface {
	SignUp(ctx context.Context, req AuthSignUpRequest) (*AuthTokenResponse, error)
	Login(ctx context.Context, req AuthLoginRequest) (*AuthTokenResponse, error)
	VerifySession(ctx context.Context, username string) (*UserResponse, error)
}
