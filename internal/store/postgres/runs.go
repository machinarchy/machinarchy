package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/pkg/crypto"
)

var _ domain.RunStore = (*Store)(nil)

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}

func nullableTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t.UTC(), Valid: true}
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
	var status, argvEncrypted, rule string
	var created, heartbeat time.Time
	var started, finished sql.NullTime
	dest := []any{&run.ID, &run.WorkspaceID, &status, &run.CreatedByUserID, &run.CreatedByUsername,
		&run.CreatedByTokenID, &created, &started, &finished, &run.Program, &run.ArgCount,
		&argvEncrypted, &run.ArgvSHA256, &run.TimeoutSeconds, &run.Concurrency, &run.TargetsHash, &rule,
		&run.CancelRequested, &heartbeat}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	run.Status = domain.RunStatus(status)
	run.CreatedAt, run.HeartbeatAt = created.UTC(), heartbeat.UTC()
	run.StartedAt, run.FinishedAt = nullTimePtr(started), nullTimePtr(finished)
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create run transaction: %w", err)
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO runs (workspace_id, status, created_by_user_id,
		created_by_username, created_by_token_id, created_at, program, arg_count, argv_encrypted, argv_sha256,
		timeout_seconds, concurrency, targets_hash, rule, heartbeat_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $6) RETURNING id`,
		run.WorkspaceID, string(domain.RunQueued), run.CreatedByUserID, run.CreatedByUsername, run.CreatedByTokenID,
		now, run.Program, run.ArgCount, encrypted, run.ArgvSHA256, run.TimeoutSeconds,
		run.Concurrency, run.TargetsHash, string(ruleJSON)).Scan(&id)
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	for _, host := range run.Hosts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_hosts (run_id, host_id, host_name, address, port, status)
			VALUES ($1, $2, $3, $4, $5, $6)`, id, host.HostID, host.Name, host.Address, host.Port,
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

func (s *Store) GetRun(ctx context.Context, workspaceID int, id int64) (*domain.Run, error) {
	run, err := s.scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs r
		WHERE r.id = $1 AND r.workspace_id = $2`, id, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrRunNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read run: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT host_id, host_name, address, port, status, exit_code, output,
		error, started_at, finished_at FROM run_hosts WHERE run_id = $1 ORDER BY host_name, host_id`, id)
	if err != nil {
		return nil, fmt.Errorf("read run hosts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var host domain.RunHost
		var status string
		var exit sql.NullInt64
		var started, finished sql.NullTime
		if err := rows.Scan(&host.HostID, &host.Name, &host.Address, &host.Port, &status, &exit, &host.Output,
			&host.Error, &started, &finished); err != nil {
			return nil, fmt.Errorf("scan run host: %w", err)
		}
		host.Status = domain.RunHostStatus(status)
		if exit.Valid {
			code := int(exit.Int64)
			host.ExitCode = &code
		}
		host.StartedAt, host.FinishedAt = nullTimePtr(started), nullTimePtr(finished)
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

func (s *Store) ListRuns(ctx context.Context, workspaceID int, filter domain.RunFilter) ([]domain.Run, error) {
	filter, err := domain.NormalizeRunFilter(filter)
	if err != nil {
		return nil, err
	}
	count := func(status domain.RunHostStatus) string {
		return `COALESCE(SUM(CASE WHEN h.status = '` + string(status) + `' THEN 1 ELSE 0 END), 0)`
	}
	query := `SELECT r.id, r.workspace_id, r.status, r.created_by_user_id, r.created_by_username,
		r.created_by_token_id, r.created_at, r.started_at, r.finished_at, r.program, r.arg_count,
		'' AS argv_encrypted, r.argv_sha256, r.timeout_seconds, r.concurrency, r.targets_hash, r.rule,
		r.cancel_requested, r.heartbeat_at, ` +
		count(domain.RunHostPending) + `, ` + count(domain.RunHostRunning) + `, ` + count(domain.RunHostSucceeded) + `, ` +
		count(domain.RunHostFailed) + `, ` + count(domain.RunHostTimedOut) + `, ` + count(domain.RunHostCancelled) + `, ` +
		count(domain.RunHostInterrupted) + `
		FROM runs r LEFT JOIN run_hosts h ON h.run_id = r.id WHERE r.workspace_id = $1`
	args := []any{workspaceID}
	if filter.Status != "" {
		args = append(args, filter.Status)
		query += fmt.Sprintf(` AND r.status = $%d`, len(args))
	}
	if filter.BeforeID > 0 {
		args = append(args, filter.BeforeID)
		query += fmt.Sprintf(` AND r.id < $%d`, len(args))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(` GROUP BY r.id ORDER BY r.id DESC LIMIT $%d`, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()
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

func (s *Store) StartRun(ctx context.Context, id int64, owner string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET status = $1, started_at = $2, owner_id = $3, heartbeat_at = $2
		WHERE id = $4 AND status = $5`, string(domain.RunRunning), now.UTC(), owner, id, string(domain.RunQueued))
	if err != nil {
		return fmt.Errorf("start run: %w", err)
	}
	return affected(result, domain.ErrRunFinished)
}

func (s *Store) Heartbeat(ctx context.Context, id int64, now time.Time) (bool, error) {
	var cancel bool
	err := s.db.QueryRowContext(ctx, `UPDATE runs SET heartbeat_at = $1 WHERE id = $2 AND status IN ($3, $4)
		RETURNING cancel_requested`, now.UTC(), id, string(domain.RunQueued), string(domain.RunRunning)).Scan(&cancel)
	if errors.Is(err, sql.ErrNoRows) {
		return false, domain.ErrRunFinished
	}
	if err != nil {
		return false, fmt.Errorf("renew run heartbeat: %w", err)
	}
	return cancel, nil
}

func (s *Store) UpdateRunHost(ctx context.Context, runID int64, hostID string, update domain.RunHostUpdate) error {
	var exit sql.NullInt64
	if update.ExitCode != nil {
		exit = sql.NullInt64{Int64: int64(*update.ExitCode), Valid: true}
	}
	// A host that already has a final result is never overwritten.
	_, err := s.db.ExecContext(ctx, `UPDATE run_hosts SET status = $1, exit_code = $2, output = $3, error = $4,
		started_at = COALESCE($5, started_at), finished_at = COALESCE($6, finished_at)
		WHERE run_id = $7 AND host_id = $8 AND status IN ($9, $10)`,
		string(update.Status), exit, update.Output, update.Error, nullableTime(update.StartedAt),
		nullableTime(update.FinishedAt), runID, hostID, string(domain.RunHostPending), string(domain.RunHostRunning))
	if err != nil {
		return fmt.Errorf("update run host: %w", err)
	}
	return nil
}

func (s *Store) RequestCancel(ctx context.Context, workspaceID int, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cancel transaction: %w", err)
	}
	defer tx.Rollback()
	var status string
	var already bool
	err = tx.QueryRowContext(ctx, `SELECT status, cancel_requested FROM runs WHERE id = $1 AND workspace_id = $2 FOR UPDATE`,
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
	if already {
		return nil // already requested; nothing new to record
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET cancel_requested = TRUE WHERE id = $1`, id); err != nil {
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
func runHostCounts(ctx context.Context, tx *sql.Tx, runID int64) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT status, COUNT(*) FROM run_hosts WHERE run_id = $1 GROUP BY status`, runID)
	if err != nil {
		return nil, fmt.Errorf("count run hosts: %w", err)
	}
	defer rows.Close()
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

func (s *Store) FinishRun(ctx context.Context, id int64, status domain.RunStatus, now time.Time) error {
	if !status.Final() {
		return fmt.Errorf("%w: %q is not a final run status", domain.ErrInvalidInput, status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finish run transaction: %w", err)
	}
	defer tx.Rollback()
	var workspaceID int
	var current string
	err = tx.QueryRowContext(ctx, `SELECT workspace_id, status FROM runs WHERE id = $1 FOR UPDATE`, id).Scan(&workspaceID, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrRunNotFound
	}
	if err != nil {
		return fmt.Errorf("read run before finish: %w", err)
	}
	if domain.RunStatus(current).Final() {
		return nil
	}
	details, err := runHostCounts(ctx, tx, id)
	if err != nil {
		return err
	}
	details["status"] = string(status)
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = $1, finished_at = $2, heartbeat_at = $2 WHERE id = $3`,
		string(status), now.UTC(), id); err != nil {
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

func (s *Store) InterruptStaleRuns(ctx context.Context, cutoff, now time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin recovery transaction: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, workspace_id FROM runs
		WHERE status IN ($1, $2) AND heartbeat_at < $3 ORDER BY id FOR UPDATE`,
		string(domain.RunQueued), string(domain.RunRunning), cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("find stale runs: %w", err)
	}
	type stale struct {
		id        int64
		workspace int
	}
	var stales []stale
	for rows.Next() {
		var item stale
		if err := rows.Scan(&item.id, &item.workspace); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan stale run: %w", err)
		}
		stales = append(stales, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, fmt.Errorf("iterate stale runs: %w", err)
	}
	recovery := domain.WithAuditActor(ctx, domain.AuditActor{Source: "recovery"})
	for _, item := range stales {
		if _, err := tx.ExecContext(ctx, `UPDATE run_hosts SET status = $1, error = $2, finished_at = COALESCE(finished_at, $3)
			WHERE run_id = $4 AND status IN ($5, $6)`, string(domain.RunHostInterrupted),
			"the run was interrupted before this host finished; the command may or may not have run",
			now.UTC(), item.id, string(domain.RunHostPending), string(domain.RunHostRunning)); err != nil {
			return 0, fmt.Errorf("interrupt run hosts: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = $1, finished_at = $2 WHERE id = $3`,
			string(domain.RunInterrupted), now.UTC(), item.id); err != nil {
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
