package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/machinarchy/machinarchy/internal/domain"
)

const maxEmailLength = 320

type invitationService struct {
	store     domain.InvitationStore
	users     domain.UserStore
	mailer    domain.Mailer // nil when mail is not configured
	serverURL string
	now       func() time.Time
}

var _ domain.InvitationService = (*invitationService)(nil)

// NewInvitationService creates the invitation service. mailer may be nil: the
// owner-facing calls then fail with ErrMailNotConfigured for Invite only.
func NewInvitationService(store domain.InvitationStore, users domain.UserStore, mailer domain.Mailer, serverURL string) (domain.InvitationService, error) {
	if store == nil || users == nil {
		return nil, errors.New("invitation service requires an invitation store and a user store")
	}
	return &invitationService{store: store, users: users, mailer: mailer, serverURL: serverURL, now: time.Now}, nil
}

// HashInvitationToken is the stored form of a token: lowercase hex SHA-256.
func HashInvitationToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newInvitationToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate invitation token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (s *invitationService) Invite(ctx context.Context, principal domain.Principal, request domain.InvitationRequest) (*domain.Invitation, error) {
	if ctx == nil {
		return nil, errors.New("invitation context is required")
	}
	if err := authorizeMemberAdmin(principal); err != nil {
		return nil, err
	}
	if s.mailer == nil {
		return nil, domain.ErrMailNotConfigured
	}
	email, err := normalizeInviteEmail(request.Email)
	if err != nil {
		return nil, err
	}
	if request.Role != domain.RoleOperator && request.Role != domain.RoleReader {
		return nil, fmt.Errorf("%w: an invitation role must be operator or reader", domain.ErrInvalidInput)
	}
	expires := s.now().UTC().Add(domain.InvitationTTL)

	// The answer is the same for an address that already has an account, so the
	// call does not reveal which addresses are registered. Nothing is sent.
	if existing, err := s.users.GetUserByEmail(ctx, email); err == nil && existing != nil {
		slog.Info("audit", "event", "member.invite_skipped", "actor_user_id", principal.UserID,
			"workspace_id", principal.WorkspaceID, "reason", "account_exists")
		return &domain.Invitation{Email: email, Role: request.Role, CreatedAt: s.now().UTC(), ExpiresAt: expires}, nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, domain.ErrUserNotFound) {
		return nil, fmt.Errorf("look up invited email: %w", err)
	}

	token, err := newInvitationToken()
	if err != nil {
		return nil, err
	}
	invitation, err := s.store.CreateInvitation(ctx, domain.NewInvitation{
		WorkspaceID: principal.WorkspaceID, Email: email, Role: request.Role,
		InvitedBy: principal.UserID, TokenHash: HashInvitationToken(token), ExpiresAt: expires,
	})
	if err != nil {
		return nil, err
	}
	if err := s.mailer.SendInvitation(ctx, email, domain.InvitationMail{
		ServerURL: s.serverURL, Token: token, Role: request.Role, InvitedBy: principal.Username, ExpiresAt: invitation.ExpiresAt,
	}); err != nil {
		// An invitation nobody received must not stay valid.
		if revokeErr := s.store.RevokeInvitation(context.WithoutCancel(ctx), principal.WorkspaceID, invitation.ID); revokeErr != nil {
			slog.Error("revoke undelivered invitation", "invitation_id", invitation.ID, "error", revokeErr)
		}
		slog.Error("send invitation email", "invitation_id", invitation.ID, "error", err)
		if errors.Is(err, domain.ErrInvalidInput) {
			return nil, err
		}
		return nil, domain.ErrMailFailed
	}
	invitation.ID = 0 // same shape as the "account exists" answer; owners find ids with List
	return invitation, nil
}

func normalizeInviteEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > maxEmailLength || strings.ContainsAny(value, "\r\n<>\"") {
		return "", fmt.Errorf("%w: a plain email address is required", domain.ErrInvalidInput)
	}
	addr, err := mail.ParseAddress(value)
	if err != nil || addr.Name != "" || addr.Address != value {
		return "", fmt.Errorf("%w: a plain email address is required", domain.ErrInvalidInput)
	}
	return value, nil
}

func (s *invitationService) List(ctx context.Context, principal domain.Principal) ([]domain.Invitation, error) {
	if ctx == nil {
		return nil, errors.New("invitation context is required")
	}
	if err := authorizeMemberAdmin(principal); err != nil {
		return nil, err
	}
	return s.store.ListInvitations(ctx, principal.WorkspaceID)
}

func (s *invitationService) Revoke(ctx context.Context, principal domain.Principal, id int) error {
	if ctx == nil {
		return errors.New("invitation context is required")
	}
	if err := authorizeMemberAdmin(principal); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("%w: invitation ID must be positive", domain.ErrInvalidInput)
	}
	return s.store.RevokeInvitation(ctx, principal.WorkspaceID, id)
}

func (s *invitationService) Accept(ctx context.Context, token, username, password string) (*domain.User, error) {
	if ctx == nil {
		return nil, errors.New("invitation context is required")
	}
	username = strings.ToLower(strings.TrimSpace(username))
	if token == "" || len(token) > 256 {
		return nil, domain.ErrInvitationInvalid
	}
	if !validUsername(username) || !validPassword(password) {
		return nil, fmt.Errorf("%w: username is required (no spaces or @) and password must be between 12 and 72 bytes", domain.ErrInvalidInput)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	return s.store.AcceptInvitation(ctx, HashInvitationToken(token), username, string(hash), s.now())
}

func (s *invitationService) AddMember(ctx context.Context, principal domain.Principal, username, email string, role domain.Role) (*domain.MemberLink, error) {
	if ctx == nil {
		return nil, errors.New("add member context is required")
	}
	if err := authorizeMemberAdmin(principal); err != nil {
		return nil, err
	}
	username = strings.ToLower(strings.TrimSpace(username))
	if !validUsername(username) {
		return nil, fmt.Errorf("%w: username is required (up to 64 characters, no spaces or @)", domain.ErrInvalidInput)
	}
	if role != domain.RoleOperator && role != domain.RoleReader {
		return nil, fmt.Errorf("%w: an added member's role must be operator or reader", domain.ErrInvalidInput)
	}
	if strings.TrimSpace(email) != "" {
		var err error
		if email, err = normalizeInviteEmail(email); err != nil {
			return nil, err
		}
	} else {
		email = ""
	}
	token, err := newInvitationToken()
	if err != nil {
		return nil, err
	}
	expires := s.now().UTC().Add(domain.ActivationTTL)
	user, err := s.store.AddMember(ctx, domain.NewMember{
		WorkspaceID: principal.WorkspaceID, Username: username, Email: email, Role: role,
		CreatedBy: principal.UserID, TokenHash: HashInvitationToken(token), ExpiresAt: expires,
	})
	if err != nil {
		return nil, err
	}
	return &domain.MemberLink{UserID: user.ID, Username: user.Username, Role: user.Role, Token: token, ExpiresAt: expires}, nil
}

func (s *invitationService) ReissueLink(ctx context.Context, principal domain.Principal, userID int) (*domain.MemberLink, error) {
	if ctx == nil {
		return nil, errors.New("reissue link context is required")
	}
	if err := authorizeMemberAdmin(principal); err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, fmt.Errorf("%w: user ID must be positive", domain.ErrInvalidInput)
	}
	token, err := newInvitationToken()
	if err != nil {
		return nil, err
	}
	expires := s.now().UTC().Add(domain.ActivationTTL)
	if err := s.store.ReissueMemberLink(ctx, principal.WorkspaceID, userID, principal.UserID, HashInvitationToken(token), expires); err != nil {
		return nil, err
	}
	return &domain.MemberLink{UserID: userID, Token: token, ExpiresAt: expires}, nil
}

func validUsername(username string) bool {
	return username != "" && len(username) <= 64 && !strings.ContainsAny(username, " \t\r\n@")
}
