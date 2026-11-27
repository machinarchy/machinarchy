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
