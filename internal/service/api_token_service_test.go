package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

type memAccess struct {
	domain.AccessStore
	created []*domain.APIToken
	listed  [2]int
	revoked [3]int
}

func (s *memAccess) CreateAPIToken(_ context.Context, token *domain.APIToken) error {
	token.ID = len(s.created) + 1
	s.created = append(s.created, token)
	return nil
}
func (s *memAccess) ListAPITokens(_ context.Context, userID, workspaceID int) ([]domain.APIToken, error) {
	s.listed = [2]int{userID, workspaceID}
	return nil, nil
}
func (s *memAccess) RevokeAPIToken(_ context.Context, id, userID, workspaceID int) error {
	s.revoked = [3]int{id, userID, workspaceID}
	return nil
}

func TestAPITokenService(t *testing.T) {
	store := &memAccess{}
	tokens, err := NewAPITokenService(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewAPITokenService(nil); err == nil {
		t.Fatal("a token service without a store was created")
	}
	fixed := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	tokens.(*apiTokenService).now = func() time.Time { return fixed }
	ctx := context.Background()
	owner := domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner}

	issued, err := tokens.Create(ctx, owner, "  ci-deploy ", domain.RoleOperator, 0)
	if err != nil {
		t.Fatal(err)
	}
	stored := store.created[0]
	switch {
	case !strings.HasPrefix(issued.Token, apiTokenPrefix) || len(issued.Token) < len(apiTokenPrefix)+40:
		t.Fatalf("token %q", issued.Token)
	case stored.TokenHash != HashAPIToken(issued.Token) || strings.Contains(stored.TokenHash, issued.Token):
		t.Fatal("the store must receive the SHA-256 of the secret, never the secret")
	case stored.Name != "ci-deploy" || stored.UserID != 1 || stored.WorkspaceID != 1 || stored.Role != domain.RoleOperator:
		t.Fatalf("stored = %+v", stored)
	case !stored.ExpiresAt.Equal(fixed.Add(30 * 24 * time.Hour)):
		t.Fatalf("default lifetime: expires %v", stored.ExpiresAt)
	}
	if again, _ := tokens.Create(ctx, owner, "other", domain.RoleReader, 2); again.Token == issued.Token ||
		!store.created[1].ExpiresAt.Equal(fixed.Add(2*time.Hour)) {
		t.Fatal("tokens must be unique and honour an explicit lifetime")
	}

	for name, tc := range map[string]struct {
		principal domain.Principal
		tokenName string
		role      domain.Role
		hours     int
		want      error
	}{
		"operator cannot issue": {domain.Principal{UserID: 2, WorkspaceID: 1, Role: domain.RoleOperator}, "x", domain.RoleReader, 0, domain.ErrPermissionDenied},
		"reader cannot issue":   {domain.Principal{UserID: 3, WorkspaceID: 1, Role: domain.RoleReader}, "x", domain.RoleReader, 0, domain.ErrPermissionDenied},
		"no workspace":          {domain.Principal{UserID: 1, Role: domain.RoleOwner}, "x", domain.RoleReader, 0, domain.ErrPermissionDenied},
		"owner role refused":    {owner, "x", domain.RoleOwner, 0, domain.ErrInvalidInput},
		"unknown role":          {owner, "x", "admin", 0, domain.ErrInvalidInput},
		"blank name":            {owner, "  ", domain.RoleReader, 0, domain.ErrInvalidInput},
		"long name":             {owner, strings.Repeat("n", 101), domain.RoleReader, 0, domain.ErrInvalidInput},
		"negative lifetime":     {owner, "x", domain.RoleReader, -1, domain.ErrInvalidInput},
		"over a year":           {owner, "x", domain.RoleReader, 365*24 + 1, domain.ErrInvalidInput},
	} {
		if _, err := tokens.Create(ctx, tc.principal, tc.tokenName, tc.role, tc.hours); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}

	if _, err := tokens.List(ctx, owner); err != nil || store.listed != [2]int{1, 1} {
		t.Fatalf("List scoped to %v, %v", store.listed, err)
	}
	if err := tokens.Revoke(ctx, owner, 7); err != nil || store.revoked != [3]int{7, 1, 1} {
		t.Fatalf("Revoke scoped to %v, %v (it must pass the caller's user and workspace)", store.revoked, err)
	}
	operator := domain.Principal{UserID: 2, WorkspaceID: 1, Role: domain.RoleOperator}
	if _, err := tokens.List(ctx, operator); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("operator List = %v", err)
	}
	if err := tokens.Revoke(ctx, operator, 7); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("operator Revoke = %v", err)
	}
	if err := tokens.Revoke(ctx, owner, 0); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Revoke(0) = %v", err)
	}
	//nolint:staticcheck // a nil context must be refused, not dereferenced
	if _, err := tokens.Create(nil, owner, "x", domain.RoleReader, 0); err == nil {
		t.Fatal("a nil context was accepted")
	}
}
