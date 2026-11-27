package api

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// MembersClient manages workspace members. Every call needs an owner signed in
// with a password; the server refuses API tokens.
type MembersClient interface {
	ListMembers(ctx context.Context) ([]Member, error)
	SetMemberRole(ctx context.Context, id int, role string) (*Member, error)
	RemoveMember(ctx context.Context, id int) error
	// InviteMember emails an invitation. The answer is the same when the
	// address already has an account.
	InviteMember(ctx context.Context, email, role string) (*InviteResult, error)
	ListInvitations(ctx context.Context) ([]Invitation, error)
	RevokeInvitation(ctx context.Context, id int) error
	// AddMember creates a member who cannot sign in yet and returns a one-time
	// activation token, shown once.
	AddMember(ctx context.Context, username, email, role string) (*MemberLink, error)
	// ReissueMemberLink replaces the token of a member who has not activated.
	ReissueMemberLink(ctx context.Context, id int) (*MemberLink, error)
}

var _ MembersClient = (*HTTPClient)(nil)

// Member is a person's access to the workspace (never a credential).
type Member struct {
	ID        int       `json:"id" yaml:"id"`
	Username  string    `json:"username" yaml:"username"`
	Email     string    `json:"email,omitempty" yaml:"email,omitempty"`
	Role      string    `json:"role" yaml:"role"`
	CreatedAt time.Time `json:"createdAt" yaml:"createdAt"`
}

func (c *HTTPClient) ListMembers(ctx context.Context) ([]Member, error) {
	var members []Member
	err := c.get(ctx, "/api/v1/members", &members)

	return members, err
}

func (c *HTTPClient) SetMemberRole(ctx context.Context, id int, role string) (*Member, error) {
	var member Member
	if err := c.request(ctx, http.MethodPut, "/api/v1/members/"+strconv.Itoa(id)+"/role", map[string]string{"role": role}, &member); err != nil {
		return nil, err
	}

	return &member, nil
}

func (c *HTTPClient) RemoveMember(ctx context.Context, id int) error {
	return c.request(ctx, http.MethodDelete, "/api/v1/members/"+strconv.Itoa(id), nil, nil)
}

// Invitation is a pending invitation (never its token).
type Invitation struct {
	ID        int       `json:"id" yaml:"id"`
	Email     string    `json:"email" yaml:"email"`
	Role      string    `json:"role" yaml:"role"`
	InvitedBy string    `json:"invitedBy,omitempty" yaml:"invitedBy,omitempty"`
	CreatedAt time.Time `json:"createdAt" yaml:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt" yaml:"expiresAt"`
}

// InviteResult confirms an invitation.
type InviteResult struct {
	Email     string    `json:"email" yaml:"email"`
	Role      string    `json:"role" yaml:"role"`
	ExpiresAt time.Time `json:"expiresAt" yaml:"expiresAt"`
}

func (c *HTTPClient) InviteMember(ctx context.Context, email, role string) (*InviteResult, error) {
	var result InviteResult
	if err := c.request(ctx, http.MethodPost, "/api/v1/members/invitations", map[string]string{"email": email, "role": role}, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

func (c *HTTPClient) ListInvitations(ctx context.Context) ([]Invitation, error) {
	var invitations []Invitation
	err := c.get(ctx, "/api/v1/members/invitations", &invitations)

	return invitations, err
}

func (c *HTTPClient) RevokeInvitation(ctx context.Context, id int) error {
	return c.request(ctx, http.MethodDelete, "/api/v1/members/invitations/"+strconv.Itoa(id), nil, nil)
}

// MemberLink is a one-time activation token for a member added by an owner. The
// token is a secret and is shown only once.
type MemberLink struct {
	ID        int       `json:"id" yaml:"id"`
	Username  string    `json:"username,omitempty" yaml:"username,omitempty"`
	Role      string    `json:"role,omitempty" yaml:"role,omitempty"`
	Token     string    `json:"token" yaml:"token"`
	ExpiresAt time.Time `json:"expiresAt" yaml:"expiresAt"`
}

func (c *HTTPClient) AddMember(ctx context.Context, username, email, role string) (*MemberLink, error) {
	body := map[string]string{"username": username, "role": role}
	if email != "" {
		body["email"] = email
	}
	var link MemberLink
	if err := c.request(ctx, http.MethodPost, "/api/v1/members", body, &link); err != nil {
		return nil, err
	}

	return &link, nil
}

func (c *HTTPClient) ReissueMemberLink(ctx context.Context, id int) (*MemberLink, error) {
	var link MemberLink
	if err := c.request(ctx, http.MethodPost, "/api/v1/members/"+strconv.Itoa(id)+"/link", nil, &link); err != nil {
		return nil, err
	}

	return &link, nil
}
