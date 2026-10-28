// internal/domain/user.go
package domain

import (
	"context"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// MaxWorkspaceMembers bounds a member listing.
const MaxWorkspaceMembers = 1000

var ErrUserAlreadyExists = errors.New("username or email is already registered")

var ErrUserNotFound = errors.New("user not found")

var ErrInvalidCredentials = errors.New("invalid credentials")

type User struct {
	ID                int       `json:"id"`
	WorkspaceID       int       `json:"-"`
	Role              Role      `json:"-"`
	Username          string    `json:"username"`
	Email             string    `json:"email"`
	PasswordHash      string    `json:"-"`
	ResetToken        string    `json:"-"`
	ResetTokenExpires time.Time `json:"-"`
	CreatedAt         time.Time `json:"-"`
}

type JWTCustomClaims struct {
	UserID      int    `json:"id"`
	WorkspaceID int    `json:"workspace_id"`
	Username    string `json:"username"`
	jwt.RegisteredClaims
}

type UserResponse struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
}

type UserStore interface {
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserByIdentifier(ctx context.Context, identifier string) (*User, error)
	CreateUser(ctx context.Context, username, email, passwordHash string) (*User, error)
	SaveUserResetToken(ctx context.Context, username, token string, expiresAt time.Time) error
	UpdateUserPassword(ctx context.Context, userID int, newPasswordHash string) error
	// UpdateWorkspaceMemberRole changes an existing membership's role. It returns
	// ErrUserNotFound when the user is not a member of the workspace and
	// ErrLastOwner, without changing anything, when it would remove the
	// workspace's only owner. The check is atomic with the update.
	UpdateWorkspaceMemberRole(ctx context.Context, workspaceID, userID int, role Role) error
	// ListWorkspaceMembers returns up to MaxWorkspaceMembers members ordered by user ID.
	ListWorkspaceMembers(ctx context.Context, workspaceID int) ([]WorkspaceMember, error)
	// CountWorkspaceOwners returns how many members hold the owner role.
	CountWorkspaceOwners(ctx context.Context, workspaceID int) (int, error)
}
