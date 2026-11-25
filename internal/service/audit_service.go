package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/machinarchy/machinarchy/internal/domain"
)

type auditService struct {
	store domain.AuditStore
}

var _ domain.AuditService = (*auditService)(nil)

// NewAuditService creates the owner-only audit trail reader.
func NewAuditService(store domain.AuditStore) (domain.AuditService, error) {
	if store == nil {
		return nil, errors.New("audit service requires an audit store")
	}
	return &auditService{store: store}, nil
}

// authorizeAuditRead allows only human sessions whose role carries audit:read
// (owners). The route layer enforces the same rule; repeating it keeps the
// service safe if it is reused from another entry point.
func authorizeAuditRead(principal domain.Principal) error {
	if principal.UserID <= 0 || principal.WorkspaceID <= 0 ||
		principal.APITokenID != 0 || !principal.Role.Allows(domain.PermissionAuditRead) {
		return domain.ErrPermissionDenied
	}
	return nil
}

func (s *auditService) List(ctx context.Context, principal domain.Principal, filter domain.AuditFilter) ([]domain.AuditEvent, error) {
	if ctx == nil {
		return nil, errors.New("audit list context is required")
	}
	if err := authorizeAuditRead(principal); err != nil {
		return nil, err
	}
	filter, err := domain.NormalizeAuditFilter(filter)
	if err != nil {
		return nil, err
	}
	return s.store.ListAuditEvents(ctx, principal.WorkspaceID, filter)
}

func (s *auditService) Verify(ctx context.Context, principal domain.Principal) (*domain.AuditVerification, error) {
	if ctx == nil {
		return nil, errors.New("audit verify context is required")
	}
	if err := authorizeAuditRead(principal); err != nil {
		return nil, err
	}
	result, err := s.store.VerifyAuditChain(ctx, principal.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("verify audit chain: %w", err)
	}
	return result, nil
}
