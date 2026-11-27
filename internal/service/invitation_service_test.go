package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

type invFakeStore struct {
	added                  []domain.NewMember
	reissued               []string
	reissueErr, addErr     error
	created                []domain.NewInvitation
	revoked                []int
	acceptErr              error
	acceptHash, acceptUser string
}

func (f *invFakeStore) CreateInvitation(_ context.Context, n domain.NewInvitation) (*domain.Invitation, error) {
	f.created = append(f.created, n)
	return &domain.Invitation{ID: len(f.created), Email: n.Email, Role: n.Role, ExpiresAt: n.ExpiresAt}, nil
}
func (f *invFakeStore) AddMember(_ context.Context, m domain.NewMember) (*domain.User, error) {
	f.added = append(f.added, m)
	if f.addErr != nil {
		return nil, f.addErr
	}
	return &domain.User{ID: 11, Username: m.Username, Role: m.Role}, nil
}
func (f *invFakeStore) ReissueMemberLink(_ context.Context, _, _, _ int, hash string, _ time.Time) error {
	f.reissued = append(f.reissued, hash)
	return f.reissueErr
}
func (f *invFakeStore) ListInvitations(context.Context, int) ([]domain.Invitation, error) {
	return nil, nil
}
func (f *invFakeStore) RevokeInvitation(_ context.Context, _, id int) error {
	f.revoked = append(f.revoked, id)
	return nil
}
func (f *invFakeStore) AcceptInvitation(_ context.Context, hash, username, _ string, _ time.Time) (*domain.User, error) {
	f.acceptHash, f.acceptUser = hash, username
	if f.acceptErr != nil {
		return nil, f.acceptErr
	}
	return &domain.User{ID: 9, Username: username, Role: domain.RoleReader}, nil
}

type invFakeUsers struct {
	domain.UserStore
	existing string
}

func (f *invFakeUsers) GetUserByEmail(_ context.Context, email string) (*domain.User, error) {
	if email == f.existing {
		return &domain.User{ID: 5, Email: email}, nil
	}
	return nil, sql.ErrNoRows
}

type invFakeMailer struct {
	sent []domain.InvitationMail
	to   []string
	err  error
}

func (f *invFakeMailer) SendInvitation(_ context.Context, to string, m domain.InvitationMail) error {
	f.to, f.sent = append(f.to, to), append(f.sent, m)
	return f.err
}

var invOwner = domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner, Username: "alice"}

func newInvSvc(t *testing.T, store *invFakeStore, users *invFakeUsers, mailer domain.Mailer) domain.InvitationService {
	t.Helper()
	svc, err := NewInvitationService(store, users, mailer, "https://m.example.test")
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestInviteSendsTokenAndStoresOnlyItsHash(t *testing.T) {
	store, mailer := &invFakeStore{}, &invFakeMailer{}
	svc := newInvSvc(t, store, &invFakeUsers{}, mailer)

	got, err := svc.Invite(context.Background(), invOwner, domain.InvitationRequest{Email: " New@Example.test ", Role: domain.RoleOperator})
	if err != nil || got.Email != "new@example.test" || got.ID != 0 {
		t.Fatalf("invite = %+v, %v", got, err)
	}
	if len(mailer.sent) != 1 || len(store.created) != 1 || mailer.to[0] != "new@example.test" {
		t.Fatalf("sent=%v created=%v", mailer.to, store.created)
	}
	token := mailer.sent[0].Token
	if len(token) < 43 || store.created[0].TokenHash == token || store.created[0].TokenHash != HashInvitationToken(token) {
		t.Fatalf("token %q must be long and stored only as its hash %q", token, store.created[0].TokenHash)
	}
	if ttl := time.Until(store.created[0].ExpiresAt); ttl < 6*24*time.Hour || ttl > 7*24*time.Hour {
		t.Errorf("expiry in %v, want about 7 days", ttl)
	}
	// Tokens differ per invitation.
	_, _ = svc.Invite(context.Background(), invOwner, domain.InvitationRequest{Email: "b@example.test", Role: domain.RoleReader})
	if mailer.sent[1].Token == token {
		t.Error("two invitations got the same token")
	}
}

func TestInviteRefusals(t *testing.T) {
	cases := []struct {
		name      string
		principal domain.Principal
		request   domain.InvitationRequest
		noMailer  bool
		want      error
	}{
		{"owner role", invOwner, domain.InvitationRequest{Email: "a@example.test", Role: domain.RoleOwner}, false, domain.ErrInvalidInput},
		{"unknown role", invOwner, domain.InvitationRequest{Email: "a@example.test", Role: "root"}, false, domain.ErrInvalidInput},
		{"empty email", invOwner, domain.InvitationRequest{Email: " ", Role: domain.RoleReader}, false, domain.ErrInvalidInput},
		{"display name form", invOwner, domain.InvitationRequest{Email: "Bob <b@example.test>", Role: domain.RoleReader}, false, domain.ErrInvalidInput},
		{"header injection", invOwner, domain.InvitationRequest{Email: "a@example.test\r\nBcc: x@example.test", Role: domain.RoleReader}, false, domain.ErrInvalidInput},
		{"operator principal", domain.Principal{UserID: 2, WorkspaceID: 1, Role: domain.RoleOperator}, domain.InvitationRequest{Email: "a@example.test", Role: domain.RoleReader}, false, domain.ErrPermissionDenied},
		{"api token principal", domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner, APITokenID: 3}, domain.InvitationRequest{Email: "a@example.test", Role: domain.RoleReader}, false, domain.ErrPermissionDenied},
		{"no mail configured", invOwner, domain.InvitationRequest{Email: "a@example.test", Role: domain.RoleReader}, true, domain.ErrMailNotConfigured},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, mailer := &invFakeStore{}, &invFakeMailer{}
			var m domain.Mailer = mailer
			if c.noMailer {
				m = nil
			}
			svc := newInvSvc(t, store, &invFakeUsers{}, m)
			if _, err := svc.Invite(context.Background(), c.principal, c.request); !errors.Is(err, c.want) {
				t.Fatalf("error = %v, want %v", err, c.want)
			}
			if len(store.created) != 0 || len(mailer.sent) != 0 {
				t.Errorf("a refused invitation stored %d and sent %d", len(store.created), len(mailer.sent))
			}
		})
	}
}

func TestInviteExistingAccountLooksTheSameAndSendsNothing(t *testing.T) {
	store, mailer := &invFakeStore{}, &invFakeMailer{}
	svc := newInvSvc(t, store, &invFakeUsers{existing: "taken@example.test"}, mailer)
	got, err := svc.Invite(context.Background(), invOwner, domain.InvitationRequest{Email: "Taken@example.test", Role: domain.RoleReader})
	if err != nil || got.Email != "taken@example.test" || got.Role != domain.RoleReader || got.ExpiresAt.IsZero() {
		t.Fatalf("invite = %+v, %v", got, err)
	}
	if len(store.created) != 0 || len(mailer.sent) != 0 {
		t.Fatalf("an existing account got an invitation: created=%d sent=%d", len(store.created), len(mailer.sent))
	}
}

func TestInviteRevokesWhenDeliveryFails(t *testing.T) {
	store := &invFakeStore{}
	svc := newInvSvc(t, store, &invFakeUsers{}, &invFakeMailer{err: errors.New("smtp: dial tcp secret-host:25: refused")})
	_, err := svc.Invite(context.Background(), invOwner, domain.InvitationRequest{Email: "a@example.test", Role: domain.RoleReader})
	if !errors.Is(err, domain.ErrMailFailed) || strings.Contains(err.Error(), "secret-host") {
		t.Fatalf("error = %v, want a generic ErrMailFailed", err)
	}
	if len(store.revoked) != 1 {
		t.Fatalf("an undelivered invitation must be revoked, revoked = %v", store.revoked)
	}
}

func TestAcceptValidatesAndHashesTheToken(t *testing.T) {
	store := &invFakeStore{}
	svc := newInvSvc(t, store, &invFakeUsers{}, nil)
	user, err := svc.Accept(context.Background(), "the-token", " Newbie ", "correct horse battery")
	if err != nil || user.Username != "newbie" || store.acceptHash != HashInvitationToken("the-token") || store.acceptHash == "the-token" {
		t.Fatalf("accept = %+v, %v, hash %q", user, err, store.acceptHash)
	}
	for name, args := range map[string][3]string{
		"short password":  {"t", "newbie", "short"},
		"long password":   {"t", "newbie", strings.Repeat("x", 73)},
		"empty username":  {"t", " ", "correct horse battery"},
		"username spaces": {"t", "new bie", "correct horse battery"},
		"username at":     {"t", "a@b", "correct horse battery"},
	} {
		if _, err := svc.Accept(context.Background(), args[0], args[1], args[2]); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
	for name, token := range map[string]string{"empty": "", "huge": strings.Repeat("x", 300)} {
		if _, err := svc.Accept(context.Background(), token, "newbie", "correct horse battery"); !errors.Is(err, domain.ErrInvitationInvalid) {
			t.Errorf("%s token: error = %v, want ErrInvitationInvalid", name, err)
		}
	}
	store.acceptErr = domain.ErrInvitationInvalid
	if _, err := svc.Accept(context.Background(), "x", "newbie", "correct horse battery"); !errors.Is(err, domain.ErrInvitationInvalid) {
		t.Errorf("store error not passed on: %v", err)
	}
}

func TestListAndRevokeNeedAHumanOwner(t *testing.T) {
	svc := newInvSvc(t, &invFakeStore{}, &invFakeUsers{}, nil)
	token := domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner, APITokenID: 2}
	if _, err := svc.List(context.Background(), token); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("list with API token = %v", err)
	}
	if err := svc.Revoke(context.Background(), domain.Principal{UserID: 2, WorkspaceID: 1, Role: domain.RoleReader}, 1); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("revoke as reader = %v", err)
	}
	if err := svc.Revoke(context.Background(), invOwner, 0); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("revoke id 0 = %v", err)
	}
}

func TestAddMemberIssuesAHashedOneTimeToken(t *testing.T) {
	store := &invFakeStore{}
	svc := newInvSvc(t, store, &invFakeUsers{}, nil) // adding needs no mail
	link, err := svc.AddMember(context.Background(), invOwner, " Alice ", " Alice@Example.test ", domain.RoleReader)
	if err != nil || link.Username != "alice" || link.UserID != 11 || len(link.Token) < 43 {
		t.Fatalf("add = %+v, %v", link, err)
	}
	m := store.added[0]
	if m.Username != "alice" || m.Email != "alice@example.test" || m.TokenHash != HashInvitationToken(link.Token) || m.TokenHash == link.Token {
		t.Fatalf("stored member = %+v: only the token's hash may be stored", m)
	}
	if ttl := time.Until(m.ExpiresAt); ttl < 23*time.Hour || ttl > 24*time.Hour {
		t.Errorf("activation expires in %v, want about 24h", ttl)
	}
	if _, err := svc.AddMember(context.Background(), invOwner, "bob", "", domain.RoleOperator); err != nil || store.added[1].Email != "" {
		t.Errorf("an email is optional: %v, %+v", err, store.added[1])
	}
	again, _ := svc.AddMember(context.Background(), invOwner, "carol", "", domain.RoleReader)
	if again.Token == link.Token {
		t.Error("two members got the same token")
	}
}

func TestAddMemberRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		principal   domain.Principal
		user, email string
		role        domain.Role
		want        error
	}{
		"owner role":      {invOwner, "a", "", domain.RoleOwner, domain.ErrInvalidInput},
		"bad role":        {invOwner, "a", "", "root", domain.ErrInvalidInput},
		"empty name":      {invOwner, " ", "", domain.RoleReader, domain.ErrInvalidInput},
		"name with space": {invOwner, "a b", "", domain.RoleReader, domain.ErrInvalidInput},
		"name with at":    {invOwner, "a@b", "", domain.RoleReader, domain.ErrInvalidInput},
		"long name":       {invOwner, strings.Repeat("x", 65), "", domain.RoleReader, domain.ErrInvalidInput},
		"bad email":       {invOwner, "a", "Bob <b@example.test>", domain.RoleReader, domain.ErrInvalidInput},
		"operator":        {domain.Principal{UserID: 2, WorkspaceID: 1, Role: domain.RoleOperator}, "a", "", domain.RoleReader, domain.ErrPermissionDenied},
		"api token":       {domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner, APITokenID: 4}, "a", "", domain.RoleReader, domain.ErrPermissionDenied},
	} {
		store := &invFakeStore{}
		svc := newInvSvc(t, store, &invFakeUsers{}, nil)
		if _, err := svc.AddMember(context.Background(), c.principal, c.user, c.email, c.role); !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", name, err, c.want)
		}
		if len(store.added) != 0 {
			t.Errorf("%s: a refused add reached the store", name)
		}
	}
	store := &invFakeStore{addErr: domain.ErrUserAlreadyExists}
	if _, err := newInvSvc(t, store, &invFakeUsers{}, nil).AddMember(context.Background(), invOwner, "a", "", domain.RoleReader); !errors.Is(err, domain.ErrUserAlreadyExists) {
		t.Errorf("a taken username must pass through: %v", err)
	}
}

func TestReissueLink(t *testing.T) {
	store := &invFakeStore{}
	svc := newInvSvc(t, store, &invFakeUsers{}, nil)
	link, err := svc.ReissueLink(context.Background(), invOwner, 11)
	if err != nil || link.UserID != 11 || store.reissued[0] != HashInvitationToken(link.Token) {
		t.Fatalf("reissue = %+v, %v, stored %v", link, err, store.reissued)
	}
	store.reissueErr = domain.ErrMemberActive
	if _, err := svc.ReissueLink(context.Background(), invOwner, 11); !errors.Is(err, domain.ErrMemberActive) {
		t.Errorf("an active account must be refused: %v", err)
	}
	if _, err := svc.ReissueLink(context.Background(), invOwner, 0); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("id 0: %v", err)
	}
	if _, err := svc.ReissueLink(context.Background(), domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner, APITokenID: 2}, 11); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("an API token must be refused: %v", err)
	}
}
