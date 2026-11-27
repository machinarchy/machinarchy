package domain

import (
	"context"
	"errors"
	"time"
)

// LockedPasswordHash is the password hash of an account added by an owner that
// has not been activated yet. It is not a bcrypt hash, so no password matches it.
const LockedPasswordHash = "!locked"

// ActivationTTL is how long an owner-added user's one-time token stays valid.
const ActivationTTL = 24 * time.Hour

// Invitation limits.
const (
	// InvitationTTL is how long an emailed invitation stays valid.
	InvitationTTL = 7 * 24 * time.Hour
	// MaxPendingInvitations bounds the pending invitations of one workspace.
	MaxPendingInvitations = 100
)

var (
	// ErrInvitationInvalid is returned for a token that is unknown, expired,
	// revoked or already used. The cases are not told apart on purpose.
	ErrInvitationInvalid = errors.New("invitation is invalid or expired")
	// ErrInvitationNotFound is returned when revoking an invitation that does not exist.
	ErrInvitationNotFound = errors.New("invitation not found")
	// ErrInvitationLimit is returned when the workspace has too many pending invitations.
	ErrInvitationLimit = errors.New("too many pending invitations")
	// ErrMemberActive is returned when an activation link is requested for an
	// account that already has a password.
	ErrMemberActive = errors.New("the account is already active")
	// ErrMailNotConfigured is returned when email invitations are requested but
	// the server has no mail settings.
	ErrMailNotConfigured = errors.New("email delivery is not configured")
	// ErrMailFailed is returned when the invitation email could not be sent.
	ErrMailFailed = errors.New("invitation email could not be sent")
)

// Invitation is a pending invitation. It never carries the token.
type Invitation struct {
	ID        int       `json:"id"`
	Email     string    `json:"email"`
	Role      Role      `json:"role" enums:"operator,reader"`
	InvitedBy string    `json:"invitedBy,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// NewInvitation is what the store needs to record an invitation.
type NewInvitation struct {
	WorkspaceID int
	Email       string
	Role        Role
	InvitedBy   int
	TokenHash   string
	ExpiresAt   time.Time
}

// InvitationStore persists pending invitations. Only the SHA-256 hash of a
// token is stored.
type InvitationStore interface {
	// CreateInvitation records an invitation, replacing a pending one for the
	// same email, and writes a member.invited audit event in the same
	// transaction. It returns ErrInvitationLimit when the workspace is full.
	CreateInvitation(ctx context.Context, invitation NewInvitation) (*Invitation, error)
	// ListInvitations returns the unexpired invitations, oldest first.
	ListInvitations(ctx context.Context, workspaceID int) ([]Invitation, error)
	// RevokeInvitation deletes an invitation and audits it. It returns
	// ErrInvitationNotFound when there is none in that workspace.
	RevokeInvitation(ctx context.Context, workspaceID, id int) error
	// AcceptInvitation consumes the invitation with the token hash and creates
	// the account and membership with the invited role in one transaction. It
	// returns ErrInvitationInvalid for an unknown, expired or used token and
	// ErrUserAlreadyExists when the username or email is taken (the invitation
	// then stays valid).
	// AddMember creates a locked account, its membership and an activation
	// token hash in one transaction (member.added audit event). It returns
	// ErrUserAlreadyExists when the username or email is taken.
	AddMember(ctx context.Context, member NewMember) (*User, error)
	// ReissueMemberLink replaces the activation token of a member who is still
	// locked. It returns ErrUserNotFound for a non-member and ErrMemberActive
	// when the account already has a password.
	ReissueMemberLink(ctx context.Context, workspaceID, userID, createdBy int, tokenHash string, expiresAt time.Time) error
	// AcceptInvitation consumes an invitation or activation token. For an
	// invitation it creates the account; for an activation token it sets the
	// first password of the added member, and username must be that member's.
	AcceptInvitation(ctx context.Context, tokenHash, username, passwordHash string, now time.Time) (*User, error)
}

// NewMember is an account an owner adds without email.
type NewMember struct {
	WorkspaceID int
	Username    string
	Email       string // optional
	Role        Role
	CreatedBy   int
	TokenHash   string
	ExpiresAt   time.Time
}

// MemberLink is a one-time activation token, shown once. Token is a secret.
type MemberLink struct {
	UserID    int
	Username  string
	Role      Role
	Token     string
	ExpiresAt time.Time
}

// Mailer sends the invitation email.
type Mailer interface {
	SendInvitation(ctx context.Context, to string, invite InvitationMail) error
}

// InvitationMail is the content of an invitation email. Token is a secret.
type InvitationMail struct {
	ServerURL string
	Token     string
	Role      Role
	InvitedBy string
	ExpiresAt time.Time
}

// InvitationRequest is an owner's invitation.
type InvitationRequest struct {
	Email string
	Role  Role
}

// InvitationService issues, lists, revokes and accepts invitations.
type InvitationService interface {
	// Invite emails an invitation. It answers the same whether or not the
	// address already has an account. Owner invitations are refused with ErrInvalidInput.
	Invite(ctx context.Context, principal Principal, request InvitationRequest) (*Invitation, error)
	List(ctx context.Context, principal Principal) ([]Invitation, error)
	Revoke(ctx context.Context, principal Principal, id int) error
	// AddMember creates an account that cannot sign in until its one-time token
	// is used to choose a password. The owner passes the username and token on.
	AddMember(ctx context.Context, principal Principal, username, email string, role Role) (*MemberLink, error)
	// ReissueLink issues a new token for a member who has not activated yet; the
	// old one stops working. It refuses an account that already has a password.
	ReissueLink(ctx context.Context, principal Principal, userID int) (*MemberLink, error)
	// Accept redeems a token; it needs no principal.
	Accept(ctx context.Context, token, username, password string) (*User, error)
}
