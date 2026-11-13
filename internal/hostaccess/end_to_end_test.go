package hostaccess_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/controlmanager"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/hostaccess"
	"github.com/machinarchy/machinarchy/internal/operationworker"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/store"
	"github.com/machinarchy/machinarchy/internal/testutil/sshtest"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

type e2eBindings struct{ stores *store.Stores }

func (b e2eBindings) GetAccessProfile(c context.Context, w int, id resource.ID) (*resource.AccessProfile, error) {
	return b.stores.ResourceProfiles.GetAccessProfile(c, w, id)
}
func (b e2eBindings) GetAccessProfileRevision(c context.Context, w int, id resource.ID, n uint64) (*resource.AccessProfileRevision, error) {
	return b.stores.ResourceProfiles.GetAccessProfileRevision(c, w, id, n)
}
func (b e2eBindings) GetHost(c context.Context, w int, id resource.ID) (*resource.Host, error) {
	return b.stores.ResourceHosts.GetHost(c, w, id)
}
func (b e2eBindings) GetOperation(c context.Context, w int, id resource.ID) (*resource.Operation, error) {
	return b.stores.Operations.GetOperation(c, w, id)
}

func TestManagedHostAndOperationEndToEnd(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: "e2e-secret", Exec: func(ctx context.Context, command string, out io.Writer) int {
		if strings.Contains(command, "'hold'") {
			<-ctx.Done()
			return 1
		}
		if strings.Contains(command, "'slow'") {
			timer := time.NewTimer(800 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return 1
			case <-timer.C:
			}
		}
		_, _ = io.WriteString(out, "ran:"+command)
		return 0
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dbPath := filepath.Join(t.TempDir(), "phase-h.db")
	if err := os.Chmod(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	stores, err := store.Open(ctx, store.Config{Engine: store.EngineSQLite, SQLitePath: dbPath, EncryptionSecret: "integration-key-material-that-is-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	defer stores.Close()
	const workspace = 1
	key := []byte("0123456789abcdef0123456789abcdef")
	cmAuth, err := secrets.NewProfileAuthorizer(e2eBindings{stores}, secrets.WorkloadControlManager)
	if err != nil {
		t.Fatal(err)
	}
	opAuth, err := secrets.NewProfileAuthorizer(e2eBindings{stores}, secrets.WorkloadOperationWorker)
	if err != nil {
		t.Fatal(err)
	}
	cmSecrets, err := secrets.New(stores.Management, &secrets.StaticKeyProvider{KeyID: "test-key", Key: key}, cmAuth)
	if err != nil {
		t.Fatal(err)
	}
	opSecrets, err := secrets.New(stores.Management, &secrets.StaticKeyProvider{KeyID: "test-key", Key: key}, opAuth)
	if err != nil {
		t.Fatal(err)
	}
	owner := domain.Principal{UserID: 1, WorkspaceID: workspace, Role: domain.RoleOwner}
	secret, err := cmSecrets.Create(ctx, owner, secrets.Input{Name: "managed-host", Purpose: secrets.PurposeSSHCredential, Value: "e2e-secret"})
	if err != nil {
		t.Fatal(err)
	}
	policy := domain.ExecutionPolicy{Enabled: true, AllowAny: true}
	application, err := service.NewManagedResourceService(stores.ResourceHosts, stores.ResourceProfiles, stores.Operations,
		func() domain.ExecutionPolicy { return policy }, cmSecrets)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := application.CreateAccessProfile(ctx, owner, "e2e-profile", "", resource.AccessProfileSpec{Connector: resource.ConnectorSSH,
		Username: "deploy", Authentication: resource.AuthenticationPassword,
		Credential: resource.SecretReference{ID: secret.ID, Version: secret.CurrentVersion}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	host, err := application.CreateHost(ctx, owner, "e2e-host", nil, resource.HostSpec{AccessProfileID: profile.ID,
		AccessTarget: resource.HostAccessTarget{Endpoint: server.Addr(), Trust: []string{server.Fingerprint}}})
	if err != nil {
		t.Fatal(err)
	}
	connector, err := hostaccess.NewSSHConnector(cmSecrets, netpolicy.Policy{AllowLoopback: true, AllowedPorts: []uint16{uint16(server.Port)}}, 2, 5*time.Second, secrets.WorkloadControlManager)
	if err != nil {
		t.Fatal(err)
	}
	converger, err := hostaccess.NewHostReconciler(stores.ResourceProfiles, connector)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := controlmanager.NewEngine(controlmanager.Dependencies{Hosts: stores.ResourceHosts, Profiles: stores.ResourceProfiles, Outbox: stores.Outbox}, controlmanager.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	hostController, err := controlmanager.NewHostController(stores.ResourceHosts, converger)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.Register(hostController); err != nil {
		t.Fatal(err)
	}
	engineCtx, stopEngine := context.WithCancel(ctx)
	engineDone := make(chan error, 1)
	go func() { engineDone <- engine.Start(engineCtx) }()
	waitFor(t, func() bool {
		current, e := stores.ResourceHosts.GetHost(ctx, workspace, host.Metadata.ID)
		return e == nil && condition(current, "Ready") == resource.ConditionTrue
	})

	op, err := application.SubmitOperation(ctx, owner, []resource.ID{host.Metadata.ID}, resource.RunSpec{Argv: []string{"printf", "phase-h"}, TimeoutSeconds: 20, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	opConnector, err := hostaccess.NewSSHConnector(opSecrets, netpolicy.Policy{AllowLoopback: true, AllowedPorts: []uint16{uint16(server.Port)}}, 2, 10*time.Second, secrets.WorkloadOperationWorker)
	if err != nil {
		t.Fatal(err)
	}
	queue, ok := stores.Operations.(resource.OperationQueueRepository)
	if !ok {
		t.Fatal("operation store does not expose durable queue scans")
	}
	worker, err := operationworker.New(operationworker.Dependencies{Operations: stores.Operations, Queue: queue, Hosts: stores.ResourceHosts, Profiles: stores.ResourceProfiles,
		Outbox: stores.Outbox, Connector: opConnector, ExecutionPolicy: func() domain.ExecutionPolicy { return policy }}, 1)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Start(workerCtx) }()
	waitFor(t, func() bool {
		current, e := stores.Operations.GetOperation(ctx, workspace, op.ID)
		return e == nil && current.IsTerminal()
	})
	completed, err := stores.Operations.GetOperation(ctx, workspace, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status.State != resource.OperationSucceeded || completed.Status.Targets[0].Result == nil || !strings.Contains(completed.Status.Targets[0].Result.Stdout, "phase-h") {
		t.Fatalf("Operation result was not persisted: %+v", completed.Status)
	}
	slow, err := application.SubmitOperation(ctx, owner, []resource.ID{host.Metadata.ID}, resource.RunSpec{Argv: []string{"slow"}, TimeoutSeconds: 20, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		current, e := stores.Operations.GetOperation(ctx, workspace, slow.ID)
		return e == nil && current.Status.State == resource.OperationSucceeded
	})
	cancelled, err := application.SubmitOperation(ctx, owner, []resource.ID{host.Metadata.ID}, resource.RunSpec{Argv: []string{"hold"}, TimeoutSeconds: 20, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		current, e := stores.Operations.GetOperation(ctx, workspace, cancelled.ID)
		return e == nil && current.Status.State == resource.OperationRunning && len(current.Status.Targets) == 1 && current.Status.Targets[0].State == resource.TargetRunning
	})
	if _, err = application.CancelOperation(ctx, owner, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	waitForOperation(ctx, t, stores, workspace, cancelled.ID, resource.OperationCancelled)
	stopWorker()
	if err = <-workerDone; err != nil {
		t.Fatal(err)
	}

	// Simulate a process crash after the remote execution began. Startup must
	// mark the result uncertain instead of replaying a potentially non-idempotent command.
	interrupted, err := application.SubmitOperation(ctx, owner, []resource.ID{host.Metadata.ID}, resource.RunSpec{
		Argv: []string{"should-not-replay"}, TimeoutSeconds: 20, Concurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = interrupted.Start(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = stores.Operations.UpdateOperation(ctx, interrupted, interrupted.Revision-1); err != nil {
		t.Fatal(err)
	}
	recovery, ok := stores.Operations.(resource.OperationRecoveryRepository)
	if !ok {
		t.Fatal("operation store does not expose running-operation recovery")
	}
	recoveryWorker, err := operationworker.New(operationworker.Dependencies{Operations: stores.Operations, Queue: queue, Hosts: stores.ResourceHosts, Profiles: stores.ResourceProfiles,
		Outbox: stores.Outbox, Connector: opConnector, ExecutionPolicy: func() domain.ExecutionPolicy { return policy }, Recovery: recovery}, 1)
	if err != nil {
		t.Fatal(err)
	}
	recoveryCtx, stopRecovery := context.WithCancel(ctx)
	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- recoveryWorker.Start(recoveryCtx) }()
	waitFor(t, func() bool {
		current, e := stores.Operations.GetOperation(ctx, workspace, interrupted.ID)
		return e == nil && current.Status.State == resource.OperationInterrupted
	})
	stopRecovery()
	if err = <-recoveryDone; err != nil {
		t.Fatal(err)
	}
	latestHost, err := stores.ResourceHosts.GetHost(ctx, workspace, host.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = stores.ResourceHosts.UpdateHostSpec(ctx, workspace, host.Metadata.ID, latestHost.Metadata.Revision, latestHost.Metadata.Generation, resource.HostSpec{Presence: resource.PresenceAbsent,
		AccessProfileID: profile.ID, AccessTarget: host.Spec.AccessTarget}, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		_, e := stores.ResourceHosts.GetHost(ctx, workspace, host.Metadata.ID)
		return errors.Is(e, resource.ErrHostNotFound)
	})
	stopEngine()
	if err = <-engineDone; err != nil {
		t.Fatal(err)
	}
	if server.Execs() != 3 {
		t.Fatalf("remote exec count=%d, want 3", server.Execs())
	}
}

func condition(host *resource.Host, name string) resource.ConditionStatus {
	for _, item := range host.Status.Conditions {
		if item.Type == name {
			return item.Status
		}
	}
	return resource.ConditionUnknown
}
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not reached before timeout")
}

func waitForOperation(ctx context.Context, t *testing.T, stores *store.Stores, workspace int, id resource.ID, expected resource.OperationState) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		current, err := stores.Operations.GetOperation(ctx, workspace, id)
		if err == nil && current.Status.State == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, err := stores.Operations.GetOperation(ctx, workspace, id)
	if err != nil {
		t.Fatalf("load Operation after waiting for %s: %v", expected, err)
	}
	t.Fatalf("Operation state = %s, want %s; status=%+v", current.Status.State, expected, current.Status)
}
