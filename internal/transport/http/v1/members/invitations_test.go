package members_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/members"
)

type fakeInvitations struct {
	err       error
	invited   domain.InvitationRequest
	principal domain.Principal
	revoked   int
	acceptArg [3]string
	calls     int
	added     [3]string
	linkID    int
}

func (f *fakeInvitations) Invite(_ context.Context, p domain.Principal, r domain.InvitationRequest) (*domain.Invitation, error) {
	f.calls++
	f.principal, f.invited = p, r
	if f.err != nil {
		return nil, f.err
	}
	return &domain.Invitation{ID: 0, Email: r.Email, Role: r.Role, ExpiresAt: time.Date(2026, 12, 4, 0, 0, 0, 0, time.UTC)}, nil
}
func (f *fakeInvitations) AddMember(_ context.Context, p domain.Principal, user, email string, role domain.Role) (*domain.MemberLink, error) {
	f.calls++
	f.principal, f.added = p, [3]string{user, email, string(role)}
	if f.err != nil {
		return nil, f.err
	}
	return &domain.MemberLink{UserID: 12, Username: user, Role: role, Token: "ONE-TIME-TOKEN", ExpiresAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}, nil
}
func (f *fakeInvitations) ReissueLink(_ context.Context, _ domain.Principal, id int) (*domain.MemberLink, error) {
	f.calls++
	f.linkID = id
	if f.err != nil {
		return nil, f.err
	}
	return &domain.MemberLink{UserID: id, Token: "NEW-TOKEN", ExpiresAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}, nil
}
func (f *fakeInvitations) List(_ context.Context, p domain.Principal) ([]domain.Invitation, error) {
	f.calls++
	f.principal = p
	return []domain.Invitation{{ID: 3, Email: "a@example.test", Role: domain.RoleReader}}, f.err
}
func (f *fakeInvitations) Revoke(_ context.Context, _ domain.Principal, id int) error {
	f.calls++
	f.revoked = id
	return f.err
}
func (f *fakeInvitations) Accept(_ context.Context, token, user, pass string) (*domain.User, error) {
	f.calls++
	f.acceptArg = [3]string{token, user, pass}
	if f.err != nil {
		return nil, f.err
	}
	return &domain.User{ID: 9, Username: user, Email: "a@example.test", Role: domain.RoleOperator, PasswordHash: "SECRET-HASH"}, nil
}

func invRouter(svc domain.InvitationService, principal domain.Principal) *echo.Echo {
	e := echo.New()
	inject := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("principal", principal)
			return next(c)
		}
	}
	// The public accept route lives on the API group, outside the principal stub.
	members.RegisterInvitationRoutes(e.Group("/api"), e.Group("", inject), members.NewInvitationHandler(svc))
	return e
}

func TestInvitationOwnerRoutesAreGuarded(t *testing.T) {
	for name, principal := range map[string]domain.Principal{
		"operator":  {UserID: 2, WorkspaceID: 7, Role: domain.RoleOperator},
		"reader":    {UserID: 3, WorkspaceID: 7, Role: domain.RoleReader},
		"api token": {UserID: 1, WorkspaceID: 7, Role: domain.RoleOwner, APITokenID: 5},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &fakeInvitations{}
			e := invRouter(svc, principal)
			for _, r := range [][3]string{
				{http.MethodPost, "/members/invitations", `{"email":"a@example.test","role":"reader"}`},
				{http.MethodGet, "/members/invitations", ""},
				{http.MethodDelete, "/members/invitations/1", ""},
				{http.MethodPost, "/members", `{"username":"a","role":"reader"}`},
				{http.MethodPost, "/members/3/link", ""},
			} {
				if rec := do(e, r[0], r[1], r[2]); rec.Code != http.StatusForbidden {
					t.Errorf("%s %s = %d, want 403", r[0], r[1], rec.Code)
				}
			}
			if svc.calls != 0 {
				t.Error("a rejected caller must never reach the service")
			}
		})
	}
}

func TestInviteListRevokeSuccess(t *testing.T) {
	svc := &fakeInvitations{}
	e := invRouter(svc, owner)

	rec := do(e, http.MethodPost, "/members/invitations", `{"email":"New@Example.test","role":" Operator "}`)
	if rec.Code != http.StatusAccepted || svc.invited.Role != domain.RoleOperator || svc.principal.WorkspaceID != 7 {
		t.Fatalf("invite = %d %s, got %+v", rec.Code, rec.Body.String(), svc.invited)
	}
	if strings.Contains(rec.Body.String(), `"id"`) || strings.Contains(strings.ToLower(rec.Body.String()), "token") {
		t.Errorf("the invite answer must carry neither an id nor a token: %s", rec.Body.String())
	}
	if rec := do(e, http.MethodGet, "/members/invitations", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":3`) {
		t.Errorf("list = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(e, http.MethodDelete, "/members/invitations/3", ""); rec.Code != http.StatusNoContent || svc.revoked != 3 {
		t.Errorf("revoke = %d, id %d", rec.Code, svc.revoked)
	}
	if rec := do(e, http.MethodDelete, "/members/invitations/abc", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d", rec.Code)
	}
	if rec := do(e, http.MethodPost, "/members/invitations", `{`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json = %d", rec.Code)
	}
}

func TestInvitationErrorMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
	}{
		"invalid input": {domain.ErrInvalidInput, http.StatusBadRequest},
		"no mail":       {domain.ErrMailNotConfigured, http.StatusNotImplemented},
		"mail failed":   {domain.ErrMailFailed, http.StatusBadGateway},
		"limit":         {domain.ErrInvitationLimit, http.StatusConflict},
		"denied":        {domain.ErrPermissionDenied, http.StatusForbidden},
		"unexpected":    {errors.New("boom: secret detail"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(invRouter(&fakeInvitations{err: tc.err}, owner), http.MethodPost, "/members/invitations", `{"email":"a@example.test","role":"reader"}`)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "secret detail") {
				t.Errorf("internal detail leaked: %s", rec.Body.String())
			}
		})
	}
	if rec := do(invRouter(&fakeInvitations{err: domain.ErrInvitationNotFound}, owner), http.MethodDelete, "/members/invitations/1", ""); rec.Code != http.StatusNotFound {
		t.Errorf("revoke unknown = %d, want 404", rec.Code)
	}
}

func TestAcceptIsPublicAndReturnsNoCredential(t *testing.T) {
	svc := &fakeInvitations{}
	// A reader principal is injected for the owner routes only; accept must not need any.
	e := invRouter(svc, domain.Principal{})
	rec := do(e, http.MethodPost, "/api/auth/invitations/accept", `{"token":"tok","username":"newbie","password":"correct horse battery"}`)
	if rec.Code != http.StatusCreated || svc.acceptArg != [3]string{"tok", "newbie", "correct horse battery"} {
		t.Fatalf("accept = %d %s, args %v", rec.Code, rec.Body.String(), svc.acceptArg)
	}
	body := rec.Body.String()
	if strings.Contains(body, "SECRET-HASH") || strings.Contains(strings.ToLower(body), "password") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("accept answer must hold no credential and be uncacheable: %s %v", body, rec.Header())
	}

	for name, tc := range map[string]struct {
		err    error
		status int
	}{
		"bad token":  {domain.ErrInvitationInvalid, http.StatusNotFound},
		"name taken": {domain.ErrUserAlreadyExists, http.StatusConflict},
		"bad input":  {domain.ErrInvalidInput, http.StatusBadRequest},
	} {
		got := do(invRouter(&fakeInvitations{err: tc.err}, domain.Principal{}), http.MethodPost, "/api/auth/invitations/accept", `{"token":"t","username":"u","password":"p"}`)
		if got.Code != tc.status {
			t.Errorf("%s: status = %d, want %d", name, got.Code, tc.status)
		}
	}
}

func TestAddMemberAndLink(t *testing.T) {
	svc := &fakeInvitations{}
	e := invRouter(svc, owner)

	rec := do(e, http.MethodPost, "/members", `{"username":"Alice","email":"a@example.test","role":" Reader "}`)
	if rec.Code != http.StatusCreated || svc.added != [3]string{"Alice", "a@example.test", "reader"} || svc.principal.WorkspaceID != 7 {
		t.Fatalf("add = %d %s, got %v", rec.Code, rec.Body.String(), svc.added)
	}
	if !strings.Contains(rec.Body.String(), `"token":"ONE-TIME-TOKEN"`) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("the token must be returned once and not cached: %s %v", rec.Body.String(), rec.Header())
	}
	rec = do(e, http.MethodPost, "/members/5/link", "")
	if rec.Code != http.StatusOK || svc.linkID != 5 || !strings.Contains(rec.Body.String(), "NEW-TOKEN") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("link = %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/members/abc/link", "/members/0/link"} {
		if rec := do(e, http.MethodPost, path, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", path, rec.Code)
		}
	}
	if rec := do(e, http.MethodPost, "/members", `{`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json = %d", rec.Code)
	}
	for name, tc := range map[string]struct {
		err    error
		status int
	}{
		"taken":      {domain.ErrUserAlreadyExists, http.StatusConflict},
		"active":     {domain.ErrMemberActive, http.StatusConflict},
		"not member": {domain.ErrUserNotFound, http.StatusNotFound},
		"invalid":    {domain.ErrInvalidInput, http.StatusBadRequest},
		"unexpected": {errors.New("boom: secret detail"), http.StatusInternalServerError},
	} {
		for _, path := range []string{"/members", "/members/5/link"} {
			got := do(invRouter(&fakeInvitations{err: tc.err}, owner), http.MethodPost, path, `{"username":"a","role":"reader"}`)
			if got.Code != tc.status || strings.Contains(got.Body.String(), "secret detail") || strings.Contains(got.Body.String(), "token") {
				t.Errorf("%s %s: %d %s, want %d without detail", name, path, got.Code, got.Body.String(), tc.status)
			}
		}
	}
}
