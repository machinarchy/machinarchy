package service

import (
	"context"
	"errors"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
)

type auditFakeStore struct {
	events       []domain.AuditEvent
	err          error
	listCalls    int
	gotWorkspace int
	gotFilter    domain.AuditFilter
	verifyCalls  int
}

func (f *auditFakeStore) ListAuditEvents(_ context.Context, workspaceID int, filter domain.AuditFilter) ([]domain.AuditEvent, error) {
	f.listCalls++
	f.gotWorkspace, f.gotFilter = workspaceID, filter
	return f.events, f.err
}

func (f *auditFakeStore) VerifyAuditChain(_ context.Context, workspaceID int) (*domain.AuditVerification, error) {
	f.verifyCalls++
	f.gotWorkspace = workspaceID
	if f.err != nil {
		return nil, f.err
	}
	return &domain.AuditVerification{Valid: true, Events: len(f.events), HeadHash: domain.AuditGenesisHash}, nil
}

func TestAuditServiceRequiresAHumanOwner(t *testing.T) {
	denied := map[string]domain.Principal{
		"operator":     {UserID: 3, WorkspaceID: 7, Role: domain.RoleOperator},
		"reader":       {UserID: 2, WorkspaceID: 7, Role: domain.RoleReader},
		"api token":    {UserID: 1, WorkspaceID: 7, Role: domain.RoleOwner, APITokenID: 9},
		"no user":      {WorkspaceID: 7, Role: domain.RoleOwner},
		"no workspace": {UserID: 1, Role: domain.RoleOwner},
		"no role":      {UserID: 1, WorkspaceID: 7},
	}
	for name, principal := range denied {
		t.Run(name, func(t *testing.T) {
			store := &auditFakeStore{}
			svc, err := NewAuditService(store)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.List(context.Background(), principal, domain.AuditFilter{}); !errors.Is(err, domain.ErrPermissionDenied) {
				t.Errorf("List() error = %v, want ErrPermissionDenied", err)
			}
			if _, err := svc.Verify(context.Background(), principal); !errors.Is(err, domain.ErrPermissionDenied) {
				t.Errorf("Verify() error = %v, want ErrPermissionDenied", err)
			}
			if store.listCalls != 0 || store.verifyCalls != 0 {
				t.Error("a denied caller must never reach the store")
			}
		})
	}
}

func TestAuditServiceListAndVerifyUseTheCallersWorkspace(t *testing.T) {
	store := &auditFakeStore{events: []domain.AuditEvent{{ID: 2}, {ID: 1}}}
	svc, _ := NewAuditService(store)
	owner := domain.Principal{UserID: 1, WorkspaceID: 7, Role: domain.RoleOwner}

	events, err := svc.List(context.Background(), owner, domain.AuditFilter{Limit: domain.MaxAuditPageSize * 10, Action: " host.created "})
	if err != nil || len(events) != 2 {
		t.Fatalf("List() = %v, %v", events, err)
	}
	if store.gotWorkspace != 7 || store.gotFilter.Limit != domain.MaxAuditPageSize || store.gotFilter.Action != "host.created" {
		t.Errorf("store saw workspace %d filter %+v; the workspace must come from the principal and the filter must be normalized", store.gotWorkspace, store.gotFilter)
	}

	result, err := svc.Verify(context.Background(), owner)
	if err != nil || !result.Valid || result.Events != 2 || store.gotWorkspace != 7 {
		t.Fatalf("Verify() = %+v, %v", result, err)
	}
}

func TestAuditServiceRejectsBadFiltersAndPropagatesStoreErrors(t *testing.T) {
	store := &auditFakeStore{}
	svc, _ := NewAuditService(store)
	owner := domain.Principal{UserID: 1, WorkspaceID: 7, Role: domain.RoleOwner}

	if _, err := svc.List(context.Background(), owner, domain.AuditFilter{Limit: -1}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("negative limit error = %v, want ErrInvalidInput", err)
	}
	if store.listCalls != 0 {
		t.Error("an invalid filter must not reach the store")
	}
	store.err = errors.New("database is down")
	if _, err := svc.List(context.Background(), owner, domain.AuditFilter{}); err == nil {
		t.Error("store errors must be returned")
	}
	if _, err := svc.Verify(context.Background(), owner); err == nil {
		t.Error("store errors must be returned from Verify")
	}
	if _, err := NewAuditService(nil); err == nil {
		t.Error("a nil store must be rejected")
	}
}
