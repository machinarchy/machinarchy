package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/utils"
	"github.com/machinarchy/machinarchy/pkg/crypto"
)

var _ domain.RunStore = (*Store)(nil)

// runTimeLayout is fixed-width UTC text, so run timestamps compare correctly as strings.
const runTimeLayout = "2006-01-02T15:04:05.000000000Z"

func formatRunTime(t time.Time) string { return t.UTC().Format(runTimeLayout) }

func parseRunTime(value string) time.Time {
	parsed, err := time.Parse(runTimeLayout, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func nullRunTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed := parseRunTime(value.String)
	return &parsed
}

func nullableTime(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: formatRunTime(*t), Valid: true}
}

func shortHash(hash string) string {
	if len(hash) > 16 {
		return hash[:16]
	}
	return hash
}

const runColumns = `r.id, r.workspace_id, r.status, r.created_by_user_id, r.created_by_username,
	r.created_by_token_id, r.created_at, r.started_at, r.finished_at, r.program, r.arg_count,
	r.argv_encrypted, r.argv_sha256, r.timeout_seconds, r.concurrency, r.targets_hash, r.rule,
	r.cancel_requested, r.heartbeat_at`

func (s *Store) scanRun(row rowScanner, extra ...any) (*domain.Run, error) {
	var run domain.Run
	var status, createdAt, heartbeat, argvEncrypted, rule string
	var started, finished sql.NullString
	var cancel int
	dest := []any{&run.ID, &run.WorkspaceID, &status, &run.CreatedByUserID, &run.CreatedByUsername,
		&run.CreatedByTokenID, &createdAt, &started, &finished, &run.Program, &run.ArgCount,
		&argvEncrypted, &run.ArgvSHA256, &run.TimeoutSeconds, &run.Concurrency, &run.TargetsHash, &rule,
		&cancel, &heartbeat}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	run.Status = domain.RunStatus(status)
	run.CreatedAt, run.HeartbeatAt = parseRunTime(createdAt), parseRunTime(heartbeat)
	run.StartedAt, run.FinishedAt = nullRunTime(started), nullRunTime(finished)
	run.CancelRequested = cancel != 0
	if err := json.Unmarshal([]byte(rule), &run.Rule); err != nil {
		return nil, fmt.Errorf("decode rule of run %d: %w", run.ID, err)
	}
	if argvEncrypted != "" {
		plain, err := crypto.Decrypt(argvEncrypted, s.encryptionKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt arguments of run %d: %w", run.ID, err)
		}
		if err := json.Unmarshal([]byte(plain), &run.Argv); err != nil {
			return nil, fmt.Errorf("decode arguments of run %d: %w", run.ID, err)
		}
	}
	return &run, nil
}

// CreateRun persists a new run in the SQLite database.
// It returns an error if the run cannot be created.
func (s *Store) CreateRun(ctx context.Context, run *domain.Run) error {
	if run == nil || run.WorkspaceID <= 0 || run.Program == "" || len(run.Argv) == 0 || len(run.Hosts) == 0 {
		return fmt.Errorf("%w: invalid run record", domain.ErrInvalidInput)
	}
	argvJSON, err := json.Marshal(run.Argv)
	if err != nil {
		return fmt.Errorf("marshal run arguments: %w", err)
	}
	encrypted, err := crypto.Encrypt(string(argvJSON), s.encryptionKey)
	if err != nil {
		return fmt.Errorf("encrypt run arguments: %w", err)
	}
	ruleJSON, err := json.Marshal(run.Rule)
	if err != nil {
		return fmt.Errorf("marshal run rule: %w", err)
	}
	now := time.Now().UTC()

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create run transaction: %w", err)
	}
	defer utils.RollbackTx(tx, s.logger, "create run")

	result, err := tx.ExecContext(ctx, `INSERT INTO runs (workspace_id, status, created_by_user_id,
		created_by_username, created_by_token_id, created_at, program, arg_count, argv_encrypted, argv_sha256,
		timeout_seconds, concurrency, targets_hash, rule, heartbeat_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.WorkspaceID, string(domain.RunQueued), run.CreatedByUserID, run.CreatedByUsername, run.CreatedByTokenID,
		formatRunTime(now), run.Program, run.ArgCount, encrypted, run.ArgvSHA256, run.TimeoutSeconds,
		run.Concurrency, run.TargetsHash, string(ruleJSON), formatRunTime(now))
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("read run ID: %w", err)
	}
	for _, host := range run.Hosts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_hosts (run_id, host_id, host_name, address, port, status)
			VALUES (?, ?, ?, ?, ?, ?)`, id, host.HostID, host.Name, host.Address, host.Port,
			string(domain.RunHostPending)); err != nil {
			return fmt.Errorf("insert run host %s: %w", host.HostID, err)
		}
	}
	// The arguments themselves never enter the audit trail, only their digest.
	if err := appendAudit(ctx, tx, domain.AuditEvent{
		WorkspaceID: run.WorkspaceID, Action: domain.AuditRunCreated,
		TargetType: domain.AuditTargetRun, TargetID: strconv.FormatInt(id, 10),
		Details: map[string]string{
			"program": run.Program, "arg_count": strconv.Itoa(run.ArgCount), "argv_sha256": shortHash(run.ArgvSHA256),
			"hosts": strconv.Itoa(len(run.Hosts)), "targets_hash": shortHash(run.TargetsHash),
			"timeout_seconds": strconv.Itoa(run.TimeoutSeconds), "concurrency": strconv.Itoa(run.Concurrency),
		},
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create run transaction: %w", err)
	}
	run.ID, run.Status, run.CreatedAt, run.HeartbeatAt = id, domain.RunQueued, now, now
	return nil
}

// GetRun retrieves a run by its ID within the specified workspace.
// It returns an error if the run cannot be found or the query fails.
func (s *Store) GetRun(ctx context.Context, workspaceID int, id int64) (*domain.Run, error) {
	run, err := s.scanRun(s.DB.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs r
		WHERE r.id = ? AND r.workspace_id = ?`, id, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrRunNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read run: %w", err)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT host_id, host_name, address, port, status, exit_code, output,
		error, started_at, finished_at FROM run_hosts WHERE run_id = ? ORDER BY host_name, host_id`, id)
	if err != nil {
		return nil, fmt.Errorf("read run hosts: %w", err)
	}
	defer utils.CloseRows(rows, s.logger, "query run hosts")

	for rows.Next() {
		var host domain.RunHost
		var status string
		var exit sql.NullInt64
		var started, finished sql.NullString
		if err := rows.Scan(&host.HostID, &host.Name, &host.Address, &host.Port, &status, &exit, &host.Output,
			&host.Error, &started, &finished); err != nil {
			return nil, fmt.Errorf("scan run host: %w", err)
		}
		host.Status = domain.RunHostStatus(status)
		if exit.Valid {
			code := int(exit.Int64)
			host.ExitCode = &code
		}
		host.StartedAt, host.FinishedAt = nullRunTime(started), nullRunTime(finished)
		run.Hosts = append(run.Hosts, host)
		addToCounts(&run.Counts, host.Status)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run hosts: %w", err)
	}
	return run, nil
}

func addToCounts(counts *domain.RunCounts, status domain.RunHostStatus) {
	switch status {
	case domain.RunHostPending:
		counts.Pending++
	case domain.RunHostRunning:
		counts.Running++
	case domain.RunHostSucceeded:
		counts.Succeeded++
	case domain.RunHostFailed:
		counts.Failed++
	case domain.RunHostTimedOut:
		counts.TimedOut++
	case domain.RunHostCancelled:
		counts.Cancelled++
	case domain.RunHostInterrupted:
		counts.Interrupted++
	}
}

// ListRuns retrieves runs for the specified workspace that match the given filter.
// It returns an error if the query fails.
func (s *Store) ListRuns(ctx context.Context, workspaceID int, filter domain.RunFilter) ([]domain.Run, error) {
	filter, err := domain.NormalizeRunFilter(filter)
	if err != nil {
		return nil, err
	}
	count := func(status domain.RunHostStatus) string {
		return `COALESCE(SUM(CASE WHEN h.status = '` + string(status) + `' THEN 1 ELSE 0 END), 0)`
	}
	// #nosec G202 -- count() generates fixed application-controlled SQL expressions.
	query := `SELECT r.id, r.workspace_id, r.status, r.created_by_user_id, r.created_by_username,
		r.created_by_token_id, r.created_at, r.started_at, r.finished_at, r.program, r.arg_count,
		'' AS argv_encrypted, r.argv_sha256, r.timeout_seconds, r.concurrency, r.targets_hash, r.rule,
		r.cancel_requested, r.heartbeat_at, ` +
		count(domain.RunHostPending) + `, ` + count(domain.RunHostRunning) + `, ` + count(domain.RunHostSucceeded) + `, ` +
		count(domain.RunHostFailed) + `, ` + count(domain.RunHostTimedOut) + `, ` + count(domain.RunHostCancelled) + `, ` +
		count(domain.RunHostInterrupted) + `
		FROM runs r LEFT JOIN run_hosts h ON h.run_id = r.id WHERE r.workspace_id = ?`
	args := []any{workspaceID}
	if filter.Status != "" {
		query += ` AND r.status = ?`
		args = append(args, filter.Status)
	}
	if filter.BeforeID > 0 {
		query += ` AND r.id < ?`
		args = append(args, filter.BeforeID)
	}
	query += ` GROUP BY r.id ORDER BY r.id DESC LIMIT ?`
	args = append(args, filter.Limit)

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer utils.CloseRows(rows, s.logger, "query list runs")

	runs := make([]domain.Run, 0, filter.Limit)
	for rows.Next() {
		var c domain.RunCounts
		run, err := s.scanRun(rows, &c.Pending, &c.Running, &c.Succeeded, &c.Failed, &c.TimedOut, &c.Cancelled, &c.Interrupted)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		run.Counts = c
		runs = append(runs, *run)
	}
	return runs, rows.Err()
}

// StartRun marks a run as started, assigning the specified owner and start time.
// It returns an error if the run cannot be started.
func (s *Store) StartRun(ctx context.Context, id int64, owner string, now time.Time) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE runs SET status = ?, started_at = ?, owner_id = ?, heartbeat_at = ?
		WHERE id = ? AND status = ?`, string(domain.RunRunning), formatRunTime(now), owner, formatRunTime(now),
		id, string(domain.RunQueued))
	if err != nil {
		return fmt.Errorf("start run: %w", err)
	}
	return requireRowsAffected(result, domain.ErrRunFinished)
}

// Heartbeat updates the last heartbeat timestamp for a run.
// It returns false if the run was not found or could not be updated, and an error if the operation fails.
func (s *Store) Heartbeat(ctx context.Context, id int64, now time.Time) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin heartbeat transaction: %w", err)
	}
	defer utils.RollbackTx(tx, s.logger, "heartbeat")

	result, err := tx.ExecContext(ctx, `UPDATE runs SET heartbeat_at = ? WHERE id = ? AND status IN (?, ?)`,
		formatRunTime(now), id, string(domain.RunQueued), string(domain.RunRunning))
	if err != nil {
		return false, fmt.Errorf("renew run heartbeat: %w", err)
	}
	if err := requireRowsAffected(result, domain.ErrRunFinished); err != nil {
		return false, err
	}
	var cancel int
	if err := tx.QueryRowContext(ctx, `SELECT cancel_requested FROM runs WHERE id = ?`, id).Scan(&cancel); err != nil {
		return false, fmt.Errorf("read cancel flag: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit heartbeat: %w", err)
	}
	return cancel != 0, nil
}

// UpdateRunHost updates the host information associated with a run.
// It returns an error if the update fails.
func (s *Store) UpdateRunHost(ctx context.Context, runID int64, hostID string, update domain.RunHostUpdate) error {
	var exit sql.NullInt64
	if update.ExitCode != nil {
		exit = sql.NullInt64{Int64: int64(*update.ExitCode), Valid: true}
	}
	// A host that already has a final result is never overwritten.
	_, err := s.DB.ExecContext(ctx, `UPDATE run_hosts SET status = ?, exit_code = ?, output = ?, error = ?,
		started_at = COALESCE(?, started_at), finished_at = COALESCE(?, finished_at)
		WHERE run_id = ? AND host_id = ? AND status IN (?, ?)`,
		string(update.Status), exit, update.Output, update.Error, nullableTime(update.StartedAt),
		nullableTime(update.FinishedAt), runID, hostID, string(domain.RunHostPending), string(domain.RunHostRunning))
	if err != nil {
		return fmt.Errorf("update run host: %w", err)
	}
	return nil
}

// RequestCancel requests cancellation of a run within the specified workspace.
// It returns an error if the cancellation request fails.
func (s *Store) RequestCancel(ctx context.Context, workspaceID int, id int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cancel transaction: %w", err)
	}
	defer utils.RollbackTx(tx, s.logger, "request cancel")

	var status string
	var already int
	err = tx.QueryRowContext(ctx, `SELECT status, cancel_requested FROM runs WHERE id = ? AND workspace_id = ?`,
		id, workspaceID).Scan(&status, &already)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrRunNotFound
	}
	if err != nil {
		return fmt.Errorf("read run before cancel: %w", err)
	}
	if domain.RunStatus(status).Final() {
		return domain.ErrRunFinished
	}
	if already != 0 {
		return nil // already requested; nothing new to record
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET cancel_requested = 1 WHERE id = ?`, id); err != nil {
		return fmt.Errorf("request run cancellation: %w", err)
	}
	if err := appendAudit(ctx, tx, domain.AuditEvent{
		WorkspaceID: workspaceID, Action: domain.AuditRunCancelRequested,
		TargetType: domain.AuditTargetRun, TargetID: strconv.FormatInt(id, 10),
		Details: map[string]string{"status": status},
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cancel transaction: %w", err)
	}
	return nil
}

// runHostCounts returns per-status host counts of a run as audit details.
func (s *Store) runHostCounts(ctx context.Context, tx *sql.Tx, runID int64) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT status, COUNT(*) FROM run_hosts WHERE run_id = ? GROUP BY status`, runID)
	if err != nil {
		return nil, fmt.Errorf("count run hosts: %w", err)
	}
	defer utils.CloseRows(rows, s.logger, "query count run hosts")

	details := map[string]string{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan run host count: %w", err)
		}
		details["hosts_"+status] = strconv.Itoa(count)
	}
	return details, rows.Err()
}

// FinishRun marks a run as finished with the specified status and completion time.
// It returns an error if the run cannot be updated.
func (s *Store) FinishRun(ctx context.Context, id int64, status domain.RunStatus, now time.Time) error {
	if !status.Final() {
		return fmt.Errorf("%w: %q is not a final run status", domain.ErrInvalidInput, status)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finish run transaction: %w", err)
	}
	defer utils.RollbackTx(tx, s.logger, "finish run")

	var workspaceID int
	var current string
	err = tx.QueryRowContext(ctx, `SELECT workspace_id, status FROM runs WHERE id = ?`, id).Scan(&workspaceID, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrRunNotFound
	}
	if err != nil {
		return fmt.Errorf("read run before finish: %w", err)
	}
	if domain.RunStatus(current).Final() {
		return nil
	}
	details, err := s.runHostCounts(ctx, tx, id)
	if err != nil {
		return err
	}
	details["status"] = string(status)
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, finished_at = ?, heartbeat_at = ? WHERE id = ?`,
		string(status), formatRunTime(now), formatRunTime(now), id); err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	if err := appendAudit(ctx, tx, domain.AuditEvent{
		WorkspaceID: workspaceID, Action: domain.AuditRunFinished,
		TargetType: domain.AuditTargetRun, TargetID: strconv.FormatInt(id, 10), Details: details,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit finish run transaction: %w", err)
	}
	return nil
}

// InterruptStaleRuns marks runs with heartbeats older than the specified cutoff as interrupted.
// It returns the number of affected runs and an error if the operation fails.
func (s *Store) InterruptStaleRuns(ctx context.Context, cutoff, now time.Time) (int, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin recovery transaction: %w", err)
	}
	defer utils.RollbackTx(tx, s.logger, "recovery")

	rows, err := tx.QueryContext(ctx, `SELECT id, workspace_id FROM runs
		WHERE status IN (?, ?) AND heartbeat_at < ? ORDER BY id`,
		string(domain.RunQueued), string(domain.RunRunning), formatRunTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("find stale runs: %w", err)
	}
	defer utils.CloseRows(rows, s.logger, "query stale runs")

	type stale struct {
		id        int64
		workspace int
	}
	var stales []stale
	for rows.Next() {
		var item stale
		if err := rows.Scan(&item.id, &item.workspace); err != nil {
			return 0, fmt.Errorf("scan stale run: %w", err)
		}
		stales = append(stales, item)
	}
	err = rows.Err()
	if err != nil {
		return 0, fmt.Errorf("iterate stale runs: %w", err)
	}
	recovery := domain.WithAuditActor(ctx, domain.AuditActor{Source: "recovery"})
	for _, item := range stales {
		if _, err := tx.ExecContext(ctx, `UPDATE run_hosts SET status = ?, error = ?, finished_at = COALESCE(finished_at, ?)
			WHERE run_id = ? AND status IN (?, ?)`, string(domain.RunHostInterrupted),
			"the run was interrupted before this host finished; the command may or may not have run",
			formatRunTime(now), item.id, string(domain.RunHostPending), string(domain.RunHostRunning)); err != nil {
			return 0, fmt.Errorf("interrupt run hosts: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, finished_at = ? WHERE id = ?`,
			string(domain.RunInterrupted), formatRunTime(now), item.id); err != nil {
			return 0, fmt.Errorf("interrupt run: %w", err)
		}
		if err := appendAudit(recovery, tx, domain.AuditEvent{
			WorkspaceID: item.workspace, Action: domain.AuditRunInterrupted,
			TargetType: domain.AuditTargetRun, TargetID: strconv.FormatInt(item.id, 10),
		}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit recovery transaction: %w", err)
	}
	return len(stales), nil
}
