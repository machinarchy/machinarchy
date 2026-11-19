package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/store/sqlx"
)

var _ domain.LoginThrottleStore = (*Store)(nil)

// LoginBlockedUntil implements domain.LoginThrottleStore.
func (s *Store) LoginBlockedUntil(ctx context.Context, account, network string, now time.Time) (time.Time, error) {
	var until sql.NullInt64
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT MAX(blocked_until_ns) FROM login_failures
		WHERE (scope = 'account' AND subject = ?) OR (scope = 'network' AND subject = ?)`), account, network).Scan(&until)
	if err != nil {
		return time.Time{}, fmt.Errorf("read login throttle: %w", err)
	}
	if !until.Valid || until.Int64 <= now.UnixNano() {
		return time.Time{}, nil
	}
	return time.Unix(0, until.Int64).UTC(), nil
}

// RecordLoginFailure implements domain.LoginThrottleStore. Each count is one
// atomic upsert, so concurrent failures are never lost.
func (s *Store) RecordLoginFailure(ctx context.Context, failure domain.LoginFailure, policy domain.LoginThrottlePolicy, now time.Time) (int, error) {
	if failure.Account == "" {
		return 0, fmt.Errorf("%w: a login failure needs an account", domain.ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin login failure: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "record login failure")
	windowStart := now.Add(-policy.Window).UnixNano()

	accountFailures, _, err := s.countFailure(ctx, tx, "account", failure.Account, now, windowStart)
	if err != nil {
		return 0, err
	}
	if delay := policy.AccountDelay(accountFailures); delay > 0 {
		if err := s.block(ctx, tx, "account", failure.Account, now.Add(delay)); err != nil {
			return 0, err
		}
		// Audit the start of backoff once per window, for real accounts.
		if accountFailures == policy.FreeFailures+1 && failure.WorkspaceID > 0 {
			if err := s.appendAudit(ctx, tx, domain.AuditEvent{WorkspaceID: failure.WorkspaceID, Action: domain.AuditLoginThrottled,
				TargetType: domain.AuditTargetUser, TargetID: fmt.Sprint(failure.UserID), Outcome: domain.AuditOutcomeDenied,
				Details: map[string]string{"failures": fmt.Sprint(accountFailures), "network": failure.Network,
					"retry_after": delay.String()}}); err != nil {
				return 0, err
			}
		}
	}
	if failure.Network != "" {
		networkFailures, started, err := s.countFailure(ctx, tx, "network", failure.Network, now, windowStart)
		if err != nil {
			return 0, err
		}
		if networkFailures >= policy.IPMaxFailures {
			if err := s.block(ctx, tx, "network", failure.Network, time.Unix(0, started).Add(policy.Window)); err != nil {
				return 0, err
			}
		}
	}
	// Forget long-finished entries.
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM login_failures WHERE window_started_at_ns < ? AND blocked_until_ns < ?`),
		now.Add(-2*policy.Window).UnixNano(), now.UnixNano()); err != nil {
		return 0, fmt.Errorf("prune login failures: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit login failure: %w", err)
	}
	return accountFailures, nil
}

// countFailure adds one failure to scope/subject, starting a new window when
// the last one ended, and returns the count and the window's start.
func (s *Store) countFailure(ctx context.Context, tx *sql.Tx, scope, subject string, now time.Time, windowStart int64) (int, int64, error) {
	var failures int
	var started int64
	err := tx.QueryRowContext(ctx, s.bind(`INSERT INTO login_failures (scope, subject, failures, window_started_at_ns, blocked_until_ns)
		VALUES (?, ?, 1, ?, 0)
		ON CONFLICT (scope, subject) DO UPDATE SET
			failures = CASE WHEN login_failures.window_started_at_ns < ? THEN 1 ELSE login_failures.failures + 1 END,
			window_started_at_ns = CASE WHEN login_failures.window_started_at_ns < ? THEN excluded.window_started_at_ns ELSE login_failures.window_started_at_ns END
		RETURNING failures, window_started_at_ns`), scope, truncate(subject, 256), now.UnixNano(), windowStart, windowStart).Scan(&failures, &started)
	if err != nil {
		return 0, 0, fmt.Errorf("count login failure: %w", err)
	}
	return failures, started, nil
}

func (s *Store) block(ctx context.Context, tx *sql.Tx, scope, subject string, until time.Time) error {
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE login_failures SET blocked_until_ns = ? WHERE scope = ? AND subject = ?`),
		until.UnixNano(), scope, truncate(subject, 256)); err != nil {
		return fmt.Errorf("block login: %w", err)
	}
	return nil
}

// ResetLoginFailures implements domain.LoginThrottleStore.
func (s *Store) ResetLoginFailures(ctx context.Context, account string) error {
	if _, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM login_failures WHERE scope = 'account' AND subject = ?`), account); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reset login failures: %w", err)
	}
	return nil
}
