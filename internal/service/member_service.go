package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/machinarchy/machinarchy/internal/domain"
)

type memberService struct {
	users domain.UserStore
}

var _ domain.MemberService = (*memberService)(nil)

// NewMemberService creates the owner-only member administration service.
func NewMemberService(users domain.UserStore) (domain.MemberService, error) {
	if users == nil {
		return nil, errors.New("member service requires a user store")
	}
	return &memberService{users: users}, nil
}

// authorizeMemberAdmin allows only human sessions whose role carries members:manage. The
// route layer enforces the same rule; repeating it here keeps the service safe
// if it is ever reused from another entry point.
func authorizeMemberAdmin(principal domain.Principal) error {
	if principal.UserID <= 0 || principal.WorkspaceID <= 0 ||
		principal.APITokenID != 0 || !principal.Role.Allows(domain.PermissionMembersManage) {
		return domain.ErrPermissionDenied
	}
	return nil
}

func (s *memberService) List(ctx context.Context, principal domain.Principal) ([]domain.WorkspaceMember, error) {
	if ctx == nil {
		return nil, errors.New("member list context is required")
	}
	if err := authorizeMemberAdmin(principal); err != nil {
		return nil, err
	}
	return s.users.ListWorkspaceMembers(ctx, principal.WorkspaceID)
}

func (s *memberService) SetRole(ctx context.Context, principal domain.Principal, userID int, role domain.Role) (*domain.WorkspaceMember, error) {
	if ctx == nil {
		return nil, errors.New("member role context is required")
	}
	if err := authorizeMemberAdmin(principal); err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, fmt.Errorf("%w: user ID must be positive", domain.ErrInvalidInput)
	}
	if !domain.ValidRole(role) {
		return nil, fmt.Errorf("%w: role must be owner, operator, or reader", domain.ErrInvalidInput)
	}

	members, err := s.users.ListWorkspaceMembers(ctx, principal.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var target *domain.WorkspaceMember
	for i := range members {
		if members[i].UserID == userID {
			target = &members[i]
			break
		}
	}
	if target == nil {
		return nil, domain.ErrUserNotFound
	}
	if target.Role == role {
		return target, nil // already in the requested state; nothing to change or record
	}

	previous := target.Role
	// The store enforces "keep at least one owner" atomically with the update.
	if err := s.users.UpdateWorkspaceMemberRole(ctx, principal.WorkspaceID, userID, role); err != nil {
		return nil, err
	}
	target.Role = role
	slog.Info("audit",
		"event", "member.role_changed",
		"actor_user_id", principal.UserID,
		"workspace_id", principal.WorkspaceID,
		"target_user_id", userID,
		"previous_role", string(previous),
		"new_role", string(role),
	)
	return target, nil
}
