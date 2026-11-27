package domain

import (
	"context"
	"errors"
	"time"
)

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
	AcceptInvitation(ctx context.Context, tokenHash, username, passwordHash string, now time.Time) (*User, error)
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
	// Accept redeems a token; it needs no principal.
	Accept(ctx context.Context, token, username, password string) (*User, error)
}
