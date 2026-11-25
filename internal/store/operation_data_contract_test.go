package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/store"
	"github.com/machinarchy/machinarchy/pkg/crypto"
)

// AUDIT S-07 (Phase 16): Operation specs, output and live progress carry a
// key ID and are bound to their row; values written before Phase 16 stay
// readable; a root-key rotation followed by rewrap leaves nothing on the old
// key, after which the old secret can be removed.
func testOperationDataKeys(t *testing.T, f *fixture) {
	ctx := context.Background()
	workspaceID := f.newWorkspace(t, "data-keys")
	now := time.Now().UTC()
	profile, profileRevision, err := resource.NewAccessProfile(workspaceID, "data-profile", "", resource.AccessProfileSpec{Connector: resource.ConnectorSSH,
		Username: "deploy", Authentication: resource.AuthenticationPassword, Credential: resource.SecretReference{ID: "data-secret", Version: 1}, Enabled: true}, 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.stores.ResourceProfiles.CreateAccessProfile(ctx, profile, profileRevision); err != nil {
		t.Fatal(err)
	}
	newOperation := func(name string, hosts int, detach bool) (*resource.Operation, []resource.ID) {
		var targets []resource.OperationTargetSpec
		var ids []resource.ID
		for i := range hosts {
			host, err := resource.NewHost(workspaceID, 0, fmt.Sprintf("%s-%d", name, i), nil, resource.HostSpec{AccessProfileID: profile.ID}, now)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.stores.ResourceHosts.CreateHost(ctx, host); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, host.Metadata.ID)
			targets = append(targets, resource.OperationTargetSpec{HostID: host.Metadata.ID, HostGeneration: host.Metadata.Generation,
				DisplayName: host.Metadata.Name, AccessProfileID: profile.ID, AccessProfileRevision: profileRevision.Revision})
		}
		op, err := resource.NewRunOperation(workspaceID, resource.ActorRef{UserID: 7}, resource.OperationSpec{Targets: targets,
			Run: &resource.RunSpec{Argv: []string{"echo", name}, TimeoutSeconds: 600, Concurrency: hosts, Detach: detach}}, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.stores.Operations.CreateOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		expected := op.Revision
		if err := op.Start(now); err != nil {
			t.Fatal(err)
		}
		if err := f.stores.Operations.UpdateOperation(ctx, op, expected); err != nil {
			t.Fatal(err)
		}
		return op, ids
	}

	// One finished target with output, one running detached target with progress.
	first, hosts := newOperation("first", 2, true)
	exit := 0
	if err := f.stores.Operations.UpdateOperationTarget(ctx, workspaceID, first.ID, hosts[0], resource.TargetRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.stores.Operations.UpdateOperationTarget(ctx, workspaceID, first.ID, hosts[0], resource.TargetSucceeded,
		&resource.TargetResult{ExitCode: &exit, Stdout: "secret-output"}); err != nil {
		t.Fatal(err)
	}
	detached := f.stores.Operations.(resource.DetachedTargetRepository)
	if err := detached.RecordTargetLaunch(ctx, workspaceID, first.ID, hosts[1], resource.LaunchHandle{Mode: "setsid", PID: 1,
		Endpoint: "10.0.0.1:22", LaunchedAt: now, Deadline: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var live resource.LiveOutput
	live.Stdout.Apply(0, 13, []byte("secret-stream"))
	if err := detached.UpdateTargetLive(ctx, workspaceID, first.ID, hosts[1], live); err != nil {
		t.Fatal(err)
	}

	raw := f.raw(t)
	column := func(query string, args ...any) []string {
		rows, err := raw.Query(f.pick(query, strings.ReplaceAll(strings.ReplaceAll(query, "= ?", "= $1"), "?", "$1")), args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var values []string
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			values = append(values, v)
		}
		return values
	}
	stored := func() []string {
		return append(column(`SELECT spec_encrypted FROM operations WHERE workspace_id = ?`, workspaceID),
			column(`SELECT t.output FROM operation_targets t JOIN operations o ON o.id = t.operation_id WHERE o.workspace_id = ? AND t.output <> ''`, workspaceID)...)
	}
	storedLive := func() []string {
		return column(`SELECT t.live FROM operation_targets t JOIN operations o ON o.id = t.operation_id WHERE o.workspace_id = ? AND t.live <> ''`, workspaceID)
	}
	all := append(stored(), storedLive()...)
	if len(all) != 3 {
		t.Fatalf("expected spec, output and progress, got %d values", len(all))
	}
	for _, value := range all {
		if !strings.HasPrefix(value, `{"v":2,"k":"local:bootstrap-v1",`) || strings.Contains(value, "secret-") {
			t.Fatalf("stored value lacks a key ID or leaks plaintext: %.80s", value)
		}
	}

	// Bound to its row: a spec copied to another Operation does not open.
	second, _ := newOperation("second", 1, false)
	specs := column(`SELECT spec_encrypted FROM operations WHERE id = ?`, string(first.ID))
	original := column(`SELECT spec_encrypted FROM operations WHERE id = ?`, string(second.ID))
	exec := func(sqliteSQL, postgresSQL string, args ...any) {
		if _, err := raw.Exec(f.pick(sqliteSQL, postgresSQL), args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE operations SET spec_encrypted = ? WHERE id = ?`, `UPDATE operations SET spec_encrypted = $1 WHERE id = $2`, specs[0], string(second.ID))
	if _, err := f.stores.Operations.GetOperation(ctx, workspaceID, second.ID); err == nil {
		t.Fatal("a spec moved to another Operation opened")
	}
	exec(`UPDATE operations SET spec_encrypted = ? WHERE id = ?`, `UPDATE operations SET spec_encrypted = $1 WHERE id = $2`, original[0], string(second.ID))

	// A value written before Phase 16 (no key ID, no AAD) still reads.
	legacyOp, legacyHosts := newOperation("legacy", 1, false)
	legacyKey, err := crypto.DeriveKey(testEncryptionSecret, "machinarchy-operation-spec-v1")
	if err != nil {
		t.Fatal(err)
	}
	specJSON, _ := json.Marshal(legacyOp.Spec)
	legacySpec, _ := crypto.Encrypt(string(specJSON), legacyKey)
	legacyOutput, _ := crypto.Encrypt("legacy-output", legacyKey)
	exec(`UPDATE operations SET spec_encrypted = ? WHERE id = ?`, `UPDATE operations SET spec_encrypted = $1 WHERE id = $2`, legacySpec, string(legacyOp.ID))
	exec(`UPDATE operation_targets SET output = ?, state = 'Succeeded' WHERE operation_id = ? AND host_id = ?`,
		`UPDATE operation_targets SET output = $1, state = 'Succeeded' WHERE operation_id = $2 AND host_id = $3`,
		legacyOutput, string(legacyOp.ID), string(legacyHosts[0]))
	read := func(stores *store.Stores, id resource.ID) *resource.Operation {
		t.Helper()
		op, err := stores.Operations.GetOperation(ctx, workspaceID, id)
		if err != nil {
			t.Fatalf("read Operation %s: %v", id, err)
		}
		return op
	}
	if got := read(f.stores, legacyOp.ID); got.Status.Targets[0].Result == nil || got.Status.Targets[0].Result.Output != "legacy-output" || got.Spec.Run.Argv[1] != "legacy" {
		t.Fatalf("legacy Operation = %+v", got.Status.Targets[0])
	}

	// Rotate the root key: the old secret moves to the keyring.
	const newSecret = "rotated-test-secret-with-more-than-thirty-two-bytes"
	reopen := func(keys *secrets.LocalProvider, previous []string) *store.Stores {
		cfg := f.cfg
		cfg.EncryptionSecret, cfg.Keys, cfg.PreviousSecrets = newSecret, keys, previous
		stores, err := store.Open(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stores.Close() })
		return stores
	}
	rotatedKeys, err := secrets.NewLocalProvider("v2", newSecret, map[string]string{"bootstrap-v1": testEncryptionSecret})
	if err != nil {
		t.Fatal(err)
	}
	rotated := reopen(rotatedKeys, []string{testEncryptionSecret})
	if got := read(rotated, first.ID); got.Status.Targets[0].Result.Stdout != "secret-output" {
		t.Fatal("old data unreadable during the rotation")
	}
	rewrapper := rotated.Operations.(interface {
		RewrapOperationData(context.Context, string, int) (int, int64, error)
	})
	total, rounds := 0, 0
	for {
		rewrapped, remaining, err := rewrapper.RewrapOperationData(ctx, "bootstrap-v1", 1)
		if err != nil {
			t.Fatal(err)
		}
		total += rewrapped
		rounds++
		if remaining == 0 {
			break
		}
		if rounds > 20 {
			t.Fatalf("rewrap does not converge: %d remaining", remaining)
		}
	}
	// 3 Operations' specs, 2 outputs (one legacy) and one progress value.
	if total != 6 {
		t.Fatalf("rewrapped %d values, want 6", total)
	}
	for _, value := range append(stored(), storedLive()...) {
		if !strings.HasPrefix(value, `{"v":2,"k":"local:v2",`) {
			t.Fatalf("value not on the new key after rewrap: %.60s", value)
		}
	}

	// The old secret is gone: everything still reads.
	onlyNew, err := secrets.NewLocalProvider("v2", newSecret, nil)
	if err != nil {
		t.Fatal(err)
	}
	final := reopen(onlyNew, nil)
	if got := read(final, first.ID); got.Status.Targets[0].Result.Stdout != "secret-output" {
		t.Fatal("output unreadable after the old key was removed")
	}
	if got := read(final, legacyOp.ID); got.Status.Targets[0].Result.Output != "legacy-output" {
		t.Fatal("migrated legacy output unreadable after the old key was removed")
	}
	if _, err := final.Operations.(resource.DetachedTargetRepository).ListDetachedTargets(ctx, 10); err != nil {
		t.Fatalf("live progress unreadable after the old key was removed: %v", err)
	}
}
