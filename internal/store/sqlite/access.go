package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var _ domain.AccessStore = (*SQLiteStore)(nil)

func (s *SQLiteStore) GetWorkspaceRole(ctx context.Context, userID, workspaceID int) (domain.Role, error) {
	var role string
	err := s.DB.QueryRowContext(ctx, `SELECT role FROM workspace_memberships
		WHERE user_id = ? AND workspace_id = ?`, userID, workspaceID).Scan(&role)
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

func (s *SQLiteStore) ResolveAPIToken(ctx context.Context, tokenHash string) (*domain.Principal, error) {
	if len(tokenHash) != 64 {
		return nil, domain.ErrAPITokenNotFound
	}
	var principal domain.Principal
	var tokenRole, memberRole string
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err := s.DB.QueryRowContext(ctx, `SELECT t.id, t.user_id, t.workspace_id, t.role,
		u.username, m.role
		FROM api_tokens AS t
		JOIN users AS u ON u.id = t.user_id
		JOIN workspace_memberships AS m ON m.user_id = t.user_id AND m.workspace_id = t.workspace_id
		WHERE t.token_hash = ? AND t.revoked_at IS NULL AND t.expires_at > ?`, tokenHash, now).
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

func (s *SQLiteStore) ListAPITokens(ctx context.Context, userID, workspaceID int) ([]domain.APIToken, error) {
	if userID <= 0 || workspaceID <= 0 {
		return nil, fmt.Errorf("%w: user and workspace IDs must be positive", domain.ErrInvalidInput)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, user_id, workspace_id, name, role,
		created_at, expires_at, revoked_at FROM api_tokens
		WHERE user_id = ? AND workspace_id = ? ORDER BY id DESC`, userID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list API tokens: %w", err)
	}
	defer rows.Close()

	tokens := make([]domain.APIToken, 0)
	for rows.Next() {
		var token domain.APIToken
		var createdAt, expiresAt string
		var revokedAt sql.NullString
		var role string
		if err := rows.Scan(&token.ID, &token.UserID, &token.WorkspaceID, &token.Name,
			&role, &createdAt, &expiresAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("scan API token: %w", err)
		}
		token.Role = domain.Role(role)
		if token.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, fmt.Errorf("parse API token creation time: %w", err)
		}
		parsedExpiry, err := time.Parse(time.RFC3339Nano, expiresAt)
		if err != nil {
			return nil, fmt.Errorf("parse API token expiry: %w", err)
		}
		token.ExpiresAt = &parsedExpiry
		if revokedAt.Valid && strings.TrimSpace(revokedAt.String) != "" {
			parsedRevoked, err := time.Parse(time.RFC3339Nano, revokedAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse API token revocation time: %w", err)
			}
			token.RevokedAt = &parsedRevoked
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate API token rows: %w", err)
	}
	return tokens, nil
}

func (s *SQLiteStore) CreateAPIToken(ctx context.Context, token *domain.APIToken) error {
	if token == nil || token.UserID <= 0 || token.WorkspaceID <= 0 ||
		(token.Role != domain.RoleOperator && token.Role != domain.RoleReader) || len(token.TokenHash) != 64 {
		return fmt.Errorf("%w: invalid API token record", domain.ErrInvalidInput)
	}
	if token.CreatedAt.IsZero() || token.ExpiresAt == nil || token.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: API token timestamps are required", domain.ErrInvalidInput)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create API token transaction: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO api_tokens
		(user_id, workspace_id, name, role, token_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, token.UserID, token.WorkspaceID, token.Name,
		string(token.Role), token.TokenHash, token.CreatedAt.UTC().Format(time.RFC3339Nano),
		token.ExpiresAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		var sqliteErr *sqliteDriver.Error
		if errors.As(err, &sqliteErr) && (sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
			return fmt.Errorf("%w: %v", domain.ErrAPITokenAlreadyExists, err)
		}
		return fmt.Errorf("insert API token: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("read API token ID: %w", err)
	}
	// Only the token's name, role, and expiry are recorded; the secret and its
	// hash never enter the audit trail.
	if err := appendAudit(ctx, tx, domain.AuditEvent{
		WorkspaceID: token.WorkspaceID, Action: domain.AuditAPITokenCreated,
		TargetType: domain.AuditTargetAPIToken, TargetID: fmt.Sprint(id),
		Details: map[string]string{
			"name": token.Name, "role": string(token.Role),
			"expires_at": token.ExpiresAt.UTC().Format(time.RFC3339),
		},
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create API token transaction: %w", err)
	}
	token.ID = int(id)
	return nil
}

func (s *SQLiteStore) RevokeAPIToken(ctx context.Context, id, userID, workspaceID int) error {
	if id <= 0 || userID <= 0 || workspaceID <= 0 {
		return fmt.Errorf("%w: token, user, and workspace IDs must be positive", domain.ErrInvalidInput)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin revoke API token transaction: %w", err)
	}
	defer tx.Rollback()
	var name string
	err = tx.QueryRowContext(ctx, `SELECT name FROM api_tokens
		WHERE id = ? AND user_id = ? AND workspace_id = ? AND revoked_at IS NULL`,
		id, userID, workspaceID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrAPITokenNotFound
	}
	if err != nil {
		return fmt.Errorf("read API token before revoke: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ?
		WHERE id = ? AND user_id = ? AND workspace_id = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano), id, userID, workspaceID)
	if err != nil {
		return fmt.Errorf("revoke API token: %w", err)
	}
	if err := requireRowsAffected(result, domain.ErrAPITokenNotFound); err != nil {
		return err
	}
	if err := appendAudit(ctx, tx, domain.AuditEvent{
		WorkspaceID: workspaceID, Action: domain.AuditAPITokenRevoked,
		TargetType: domain.AuditTargetAPIToken, TargetID: fmt.Sprint(id),
		Details: map[string]string{"name": name},
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit revoke API token transaction: %w", err)
	}
	return nil
}
