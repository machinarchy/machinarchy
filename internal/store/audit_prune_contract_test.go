package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/store"
)

// oldEnough is a clock far enough ahead that every seeded event is past the
// minimum prune age.
func oldEnough() time.Time { return time.Now().Add(domain.MinAuditPruneAge + time.Hour) }

func pruneRequest(events []domain.AuditEvent, upTo int) domain.AuditPruneRequest {
	last := events[upTo]
	return domain.AuditPruneRequest{BeforeID: last.ID + 1, ArchiveHeadHash: last.Hash, ArchiveCount: upTo + 1}
}

func mustVerify(t *testing.T, f *fixture) *domain.AuditVerification {
	t.Helper()
	result, err := f.stores.Audit.VerifyAuditChain(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func testAuditPrune(t *testing.T, f *fixture) {
	ctx := domain.WithAuditActor(context.Background(), domain.AuditActor{UserID: 1, Username: "ops", Source: "api"})
	events := seedAuditEvents(t, f, 8)
	if len(events) < 8 {
		t.Fatalf("seeded %d events, need at least 8", len(events))
	}
	pruned := pruneRequest(events, 3) // the four oldest

	// Refusals change nothing: too young, a partial or wrong archive, no events.
	if _, err := f.stores.Audit.PruneAudit(ctx, 1, pruned, time.Now()); !errors.Is(err, domain.ErrInvalidInput) || !strings.Contains(err.Error(), "younger") {
		t.Errorf("young events error = %v, want ErrInvalidInput about age", err)
	}
	wrongCount := pruned
	wrongCount.ArchiveCount--
	wrongHash := pruned
	wrongHash.ArchiveHeadHash = events[2].Hash
	for name, req := range map[string]domain.AuditPruneRequest{"partial archive": wrongCount, "wrong head": wrongHash} {
		if _, err := f.stores.Audit.PruneAudit(ctx, 1, req, oldEnough()); !errors.Is(err, domain.ErrAuditArchiveMismatch) {
			t.Errorf("%s error = %v, want ErrAuditArchiveMismatch", name, err)
		}
	}
	if _, err := f.stores.Audit.PruneAudit(ctx, 1, domain.AuditPruneRequest{BeforeID: events[0].ID, ArchiveCount: 1, ArchiveHeadHash: events[0].Hash}, oldEnough()); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("empty range error = %v, want ErrInvalidInput", err)
	}
	if _, err := f.stores.Audit.PruneAudit(ctx, 0, pruned, oldEnough()); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("no workspace error = %v, want ErrInvalidInput", err)
	}
	if after := auditEvents(t, f, 1); len(after) != len(events) {
		t.Fatalf("a refused prune changed the trail: %d events, want %d", len(after), len(events))
	}
	if got := mustVerify(t, f); !got.Valid || got.Checkpoint != nil {
		t.Fatalf("verification before any prune = %+v", got)
	}

	// A prune of another workspace's events needs its own proof and touches nothing here.
	if _, err := f.stores.Audit.PruneAudit(ctx, 99, pruned, oldEnough()); err == nil {
		t.Error("pruning an unrelated workspace with this archive succeeded")
	}

	checkpoint, err := f.stores.Audit.PruneAudit(ctx, 1, pruned, oldEnough())
	if err != nil {
		t.Fatalf("PruneAudit() error = %v", err)
	}
	if checkpoint.LastEventID != events[3].ID || checkpoint.LastHash != events[3].Hash || checkpoint.EventsRemoved != 4 || checkpoint.ActorUserID != 1 {
		t.Errorf("checkpoint = %+v, want event %d hash %s, 4 removed", checkpoint, events[3].ID, events[3].Hash)
	}
	remaining := auditEvents(t, f, 1)
	if len(remaining) != len(events)-4+1 || remaining[0].ID != events[4].ID {
		t.Fatalf("retained %d events starting at %d; want %d starting at %d", len(remaining), remaining[0].ID, len(events)-3, events[4].ID)
	}
	record := remaining[len(remaining)-1]
	if record.Action != domain.AuditPruned || record.Details["removed"] != "4" || record.Details["last_hash"] != events[3].Hash || record.ActorUsername != "ops" {
		t.Errorf("audit.pruned event = %+v", record)
	}
	if got := mustVerify(t, f); !got.Valid || got.Checkpoint == nil || got.Checkpoint.LastHash != events[3].Hash || got.Events != len(remaining) {
		t.Errorf("verification after prune = %+v", got)
	}

	// Everything stays append-only for rows that were not pruned, and the
	// checkpoint itself cannot be changed or removed.
	ctxBG := context.Background()
	remove := f.pick(`DELETE FROM audit_events WHERE id = ?`, `DELETE FROM audit_events WHERE id = $1`)
	if _, err := f.stores.DB.ExecContext(ctxBG, remove, events[4].ID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("DELETE of a retained event = %v, want an append-only refusal", err)
	}
	for _, statement := range []string{`UPDATE audit_checkpoints SET last_hash = 'x'`, `DELETE FROM audit_checkpoints`} {
		if _, err := f.stores.DB.ExecContext(ctxBG, statement); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s error = %v, want an append-only refusal", statement, err)
		}
	}

	// The next prune starts from the checkpoint, and removing everything leaves
	// audit.pruned linked to the new checkpoint.
	for i := 20; i < 23; i++ {
		if err := f.stores.Access.CreateAPIToken(ctx, seedToken(1, i)); err != nil {
			t.Fatal(err)
		}
	}
	all := auditEvents(t, f, 1)
	second := pruneRequest(all, len(all)-1)
	if _, err := f.stores.Audit.PruneAudit(ctx, 1, pruneRequest(events, 3), oldEnough()); err == nil {
		t.Error("a prune with the previous archive succeeded a second time")
	}
	cp2, err := f.stores.Audit.PruneAudit(ctx, 1, second, oldEnough())
	if err != nil {
		t.Fatalf("second PruneAudit() error = %v", err)
	}
	if cp2.EventsRemoved != len(all) {
		t.Errorf("second prune removed %d, want %d", cp2.EventsRemoved, len(all))
	}
	left := auditEvents(t, f, 1)
	if len(left) != 1 || left[0].Action != domain.AuditPruned || left[0].PrevHash != all[len(all)-1].Hash {
		t.Fatalf("after pruning everything: %+v", left)
	}
	if got := mustVerify(t, f); !got.Valid || got.Events != 1 || got.Checkpoint == nil || got.Checkpoint.ID != cp2.ID {
		t.Errorf("verification after second prune = %+v", got)
	}
	// New events keep chaining.
	if err := f.stores.Access.CreateAPIToken(ctx, seedToken(1, 40)); err != nil {
		t.Fatal(err)
	}
	if got := mustVerify(t, f); !got.Valid || got.Events != 2 {
		t.Errorf("verification after a new event = %+v", got)
	}
}

func testAuditPruneIsScopedToItsWorkspace(t *testing.T, f *fixture) {
	ctx := context.Background()
	other := f.newWorkspace(t, "other")
	insert := f.pick(`INSERT INTO audit_events (workspace_id, occurred_at, action, target_type, prev_hash, hash) VALUES (?, ?, 'x', 'x', 'p', 'h')`,
		`INSERT INTO audit_events (workspace_id, occurred_at, action, target_type, prev_hash, hash) VALUES ($1, $2, 'x', 'x', 'p', 'h')`)
	if _, err := f.stores.DB.ExecContext(ctx, insert, other, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	events := seedAuditEvents(t, f, 6)
	if _, err := f.stores.Audit.PruneAudit(ctx, 1, pruneRequest(events, 3), oldEnough()); err != nil {
		t.Fatal(err)
	}
	// The other workspace's older row is below the checkpoint's ID but not its workspace.
	var foreign int64
	if err := f.stores.DB.QueryRowContext(ctx, `SELECT MIN(id) FROM audit_events WHERE workspace_id <> 1`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	remove := f.pick(`DELETE FROM audit_events WHERE id = ?`, `DELETE FROM audit_events WHERE id = $1`)
	if _, err := f.stores.DB.ExecContext(ctx, remove, foreign); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("DELETE of another workspace's event = %v, want an append-only refusal", err)
	}
}

func testAuditPruneRefusesABrokenChain(t *testing.T, f *fixture) {
	ctx := context.Background()
	events := seedAuditEvents(t, f, 6)
	disableAuditTriggers(t, f)
	update := f.pick(`UPDATE audit_events SET details = '{"name":"evil"}' WHERE id = ?`, `UPDATE audit_events SET details = '{"name":"evil"}' WHERE id = $1`)
	if _, err := f.stores.DB.ExecContext(ctx, update, events[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.stores.Audit.PruneAudit(ctx, 1, pruneRequest(events, 3), oldEnough()); !errors.Is(err, domain.ErrInvalidInput) || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("prune of a modified range = %v, want ErrInvalidInput about a broken chain", err)
	}
	var count int
	if err := f.stores.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events`).Scan(&count); err != nil || count != len(events) {
		t.Errorf("events after the refusal = %d, %v; want %d", count, err, len(events))
	}
	var checkpoints int
	if err := f.stores.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_checkpoints`).Scan(&checkpoints); err != nil || checkpoints != 0 {
		t.Errorf("checkpoints after the refusal = %d, %v; want none", checkpoints, err)
	}
}

func testAuditDetectsATamperedCheckpoint(t *testing.T, f *fixture) {
	ctx := context.Background()
	events := seedAuditEvents(t, f, 6)
	if _, err := f.stores.Audit.PruneAudit(ctx, 1, pruneRequest(events, 3), oldEnough()); err != nil {
		t.Fatal(err)
	}
	statements := []string{`DROP TRIGGER audit_checkpoints_no_update`, `DROP TRIGGER audit_checkpoints_no_delete`}
	if f.engine == store.EnginePostgres {
		statements = []string{
			`ALTER TABLE audit_checkpoints DISABLE TRIGGER audit_checkpoints_no_update`,
			`ALTER TABLE audit_checkpoints DISABLE TRIGGER audit_checkpoints_no_delete`,
		}
	}
	for _, statement := range statements {
		if _, err := f.stores.DB.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if _, err := f.stores.DB.ExecContext(ctx, `UPDATE audit_checkpoints SET last_hash = '`+strings.Repeat("0", 64)+`'`); err != nil {
		t.Fatal(err)
	}
	if got := mustVerify(t, f); got.Valid || got.FirstInvalidID != events[4].ID || !strings.Contains(got.Reason, "link") {
		t.Errorf("verification with an altered checkpoint = %+v, want a broken link at event %d", got, events[4].ID)
	}
	if _, err := f.stores.DB.ExecContext(ctx, `DELETE FROM audit_checkpoints`); err != nil {
		t.Fatal(err)
	}
	if got := mustVerify(t, f); got.Valid || got.FirstInvalidID != events[4].ID {
		t.Errorf("verification with the checkpoint removed = %+v, want a broken link at event %d", got, events[4].ID)
	}
}

func testAuditPruneRacesAppends(t *testing.T, f *fixture) {
	ctx := domain.WithAuditActor(context.Background(), domain.AuditActor{UserID: 1, Username: "ops", Source: "api"})
	events := seedAuditEvents(t, f, 6)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- f.stores.Access.CreateAPIToken(ctx, seedToken(1, 100+i))
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := f.stores.Audit.PruneAudit(ctx, 1, pruneRequest(events, 3), oldEnough())
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent operation error = %v", err)
		}
	}
	got := mustVerify(t, f)
	if !got.Valid || got.Checkpoint == nil {
		t.Errorf("chain after a prune racing appends = %+v", got)
	}
}
