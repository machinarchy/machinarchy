package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

var _ domain.AccessStore = (*Store)(nil)

func (s *Store) GetWorkspaceRole(ctx context.Context, userID, workspaceID int) (domain.Role, error) {
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT role FROM workspace_memberships
		WHERE user_id = $1 AND workspace_id = $2`, userID, workspaceID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrMembershipNotFound
	}
	if err != nil {
		return "", fmt.Errorf("query workspace membership: %w", err)
	}
	if !domain.ValidRole(domain.Role(role)) {
		return "", fmt.Errorf("invalid role %q in workspace membership", role)
	}
	return domain.Role(role), nil
}

func (s *Store) ResolveAPIToken(ctx context.Context, tokenHash string) (*domain.Principal, error) {
	if len(tokenHash) != 64 {
		return nil, domain.ErrAPITokenNotFound
	}
	var principal domain.Principal
	var tokenRole, memberRole string
	err := s.db.QueryRowContext(ctx, `SELECT t.id, t.user_id, t.workspace_id, t.role,
		u.username, m.role
		FROM api_tokens AS t
		JOIN users AS u ON u.id = t.user_id
		JOIN workspace_memberships AS m ON m.user_id = t.user_id AND m.workspace_id = t.workspace_id
		WHERE t.token_hash = $1 AND t.revoked_at IS NULL AND t.expires_at > CURRENT_TIMESTAMP`, tokenHash).
		Scan(&principal.APITokenID, &principal.UserID, &principal.WorkspaceID,
			&tokenRole, &principal.Username, &memberRole)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrAPITokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve API token: %w", err)
	}
	principal.Role = domain.EffectiveRole(domain.Role(tokenRole), domain.Role(memberRole))
	if !domain.ValidRole(principal.Role) {
		return nil, domain.ErrAPITokenNotFound
	}
	return &principal, nil
}

func (s *Store) ListAPITokens(ctx context.Context, userID, workspaceID int) ([]domain.APIToken, error) {
	if userID <= 0 || workspaceID <= 0 {
		return nil, fmt.Errorf("%w: user and workspace IDs must be positive", domain.ErrInvalidInput)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, workspace_id, name, role,
		created_at, expires_at, revoked_at FROM api_tokens
		WHERE user_id = $1 AND workspace_id = $2 ORDER BY id DESC`, userID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list API tokens: %w", err)
	}
	defer rows.Close()

	tokens := make([]domain.APIToken, 0)
	for rows.Next() {
		var token domain.APIToken
		var role string
		var expiresAt, revokedAt sql.NullTime
		if err := rows.Scan(&token.ID, &token.UserID, &token.WorkspaceID, &token.Name,
			&role, &token.CreatedAt, &expiresAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("scan API token: %w", err)
		}
		token.Role = domain.Role(role)
		if expiresAt.Valid {
			expires := expiresAt.Time
			token.ExpiresAt = &expires
		}
		if revokedAt.Valid {
			revoked := revokedAt.Time
			token.RevokedAt = &revoked
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate API token rows: %w", err)
	}
	return tokens, nil
}

func (s *Store) CreateAPIToken(ctx context.Context, token *domain.APIToken) error {
	if token == nil || token.UserID <= 0 || token.WorkspaceID <= 0 ||
		(token.Role != domain.RoleOperator && token.Role != domain.RoleReader) || len(token.TokenHash) != 64 {
		return fmt.Errorf("%w: invalid API token record", domain.ErrInvalidInput)
	}
	if token.CreatedAt.IsZero() || token.ExpiresAt == nil || token.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: API token timestamps are required", domain.ErrInvalidInput)
	}
	err := s.db.QueryRowContext(ctx, `INSERT INTO api_tokens
		(user_id, workspace_id, name, role, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		token.UserID, token.WorkspaceID, token.Name, string(token.Role), token.TokenHash,
		token.CreatedAt, token.ExpiresAt).Scan(&token.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %v", domain.ErrAPITokenAlreadyExists, err)
		}
		return fmt.Errorf("insert API token: %w", err)
	}
	return nil
}

func (s *Store) RevokeAPIToken(ctx context.Context, id, userID, workspaceID int) error {
	if id <= 0 || userID <= 0 || workspaceID <= 0 {
		return fmt.Errorf("%w: token, user, and workspace IDs must be positive", domain.ErrInvalidInput)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = $1
		WHERE id = $2 AND user_id = $3 AND workspace_id = $4 AND revoked_at IS NULL`,
		time.Now().UTC(), id, userID, workspaceID)
	if err != nil {
		return fmt.Errorf("revoke API token: %w", err)
	}
	return affected(result, domain.ErrAPITokenNotFound)
}
