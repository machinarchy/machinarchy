package domain

import (
	"context"
	"errors"
	"time"
)

var (
	ErrMembershipNotFound    = errors.New("workspace membership not found")
	ErrPermissionDenied      = errors.New("permission denied")
	ErrAPITokenNotFound      = errors.New("API token not found")
	ErrAPITokenAlreadyExists = errors.New("API token name already exists")
	ErrLastOwner             = errors.New("a workspace must keep at least one owner")
)

type Role string

const (
	RoleOwner    Role = "owner"
	RoleOperator Role = "operator"
	RoleReader   Role = "reader"
)

type Permission string

const (
	PermissionInventoryRead  Permission = "inventory:read"
	PermissionInventoryWrite Permission = "inventory:write"
	PermissionOperationsRead Permission = "operations:read"
	PermissionTokenManage    Permission = "tokens:manage"
	PermissionMembersManage  Permission = "members:manage"
)

// Principal is the authenticated identity attached to an API request. API
// tokens carry their own role but remain bound to the issuing user's workspace.
type Principal struct {
	UserID      int    `json:"-"`
	WorkspaceID int    `json:"-"`
	Role        Role   `json:"-"`
	APITokenID  int    `json:"-"`
	Username    string `json:"-"`
}

func (r Role) Allows(permission Permission) bool {
	switch r {
	case RoleOwner:
		return true
	case RoleOperator:
		return permission == PermissionInventoryRead ||
			permission == PermissionInventoryWrite ||
			permission == PermissionOperationsRead
	case RoleReader:
		return permission == PermissionInventoryRead || permission == PermissionOperationsRead
	default:
		return false
	}
}

func ValidRole(role Role) bool {
	return role == RoleOwner || role == RoleOperator || role == RoleReader
}

// EffectiveRole returns the less privileged of a credential role and current
// membership role so a token cannot outlive a membership downgrade.
func EffectiveRole(credential, membership Role) Role {
	if !ValidRole(credential) || !ValidRole(membership) {
		return ""
	}
	if credential == RoleReader || membership == RoleReader {
		return RoleReader
	}
	if credential == RoleOperator || membership == RoleOperator {
		return RoleOperator
	}
	return RoleOwner
}

type AccessStore interface {
	GetWorkspaceRole(ctx context.Context, userID, workspaceID int) (Role, error)
	ResolveAPIToken(ctx context.Context, tokenHash string) (*Principal, error)
	ListAPITokens(ctx context.Context, userID, workspaceID int) ([]APIToken, error)
	CreateAPIToken(ctx context.Context, token *APIToken) error
	RevokeAPIToken(ctx context.Context, id, userID, workspaceID int) error
}

type APIToken struct {
	ID          int        `json:"id"`
	UserID      int        `json:"-"`
	WorkspaceID int        `json:"-"`
	Name        string     `json:"name"`
	Role        Role       `json:"role"`
	TokenHash   string     `json:"-"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
}

type IssuedAPIToken struct {
	APIToken
	Token string `json:"token"`
}

type APITokenService interface {
	Create(ctx context.Context, principal Principal, name string, role Role, expiresInHours int) (*IssuedAPIToken, error)
	List(ctx context.Context, principal Principal) ([]APIToken, error)
	Revoke(ctx context.Context, principal Principal, id int) error
}

// WorkspaceMember is a user's membership in a workspace.
type WorkspaceMember struct {
	UserID    int       `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email,omitempty"`
	Role      Role      `json:"role" enums:"owner,operator,reader"`
	CreatedAt time.Time `json:"createdAt"`
}

// MemberService lets workspace owners inspect and change member roles. Only
// human owner sessions may use it; API-token principals are always refused.
type MemberService interface {
	List(ctx context.Context, principal Principal) ([]WorkspaceMember, error)
	// SetRole changes a member's role. It returns ErrUserNotFound when the user is
	// not a member and ErrLastOwner when the change would leave no owner.
	SetRole(ctx context.Context, principal Principal, userID int, role Role) (*WorkspaceMember, error)
}
