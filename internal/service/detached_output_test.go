package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/secrets"
)

func TestDetachedSubmissionAndOutputAccess(t *testing.T) {
	ctx := context.Background()
	f := newManagedFixture(t)
	s := f.service
	f.execution.Store(&domain.ExecutionPolicy{Enabled: true, AllowAny: true})
	secret, err := f.secrets.Create(ctx, f.owner, secrets.Input{Name: "deploy-key", Purpose: secrets.PurposeSSHCredential, Value: "key material"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.CreateAccessProfile(ctx, f.owner, "web", "", resource.AccessProfileSpec{Connector: resource.ConnectorSSH,
		Authentication: resource.AuthenticationPrivateKey, Username: "deploy", Enabled: true,
		Credential: resource.SecretReference{ID: secret.ID, Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.CreateHost(ctx, f.owner, "web-1", nil, resource.HostSpec{Presence: resource.PresenceManaged, AccessProfileID: profile.ID,
		AccessTarget: resource.HostAccessTarget{Endpoint: "192.0.2.10:22", Trust: []string{"SHA256:test"}}})
	if err != nil {
		t.Fatal(err)
	}
	long := resource.RunSpec{Argv: []string{"sleep", "7200"}, TimeoutSeconds: 7200, Concurrency: 1}
	if _, err := s.SubmitOperation(ctx, f.operator, []resource.ID{host.Metadata.ID}, long); err == nil {
		t.Fatal("an attached run over 15 minutes was accepted")
	}
	long.Detach = true
	op, err := s.SubmitOperation(ctx, f.operator, []resource.ID{host.Metadata.ID}, long)
	if err != nil {
		t.Fatalf("a detached 2 h run = %v", err)
	}

	output, err := s.TargetOutput(ctx, f.operator, op.ID, host.Metadata.ID, resource.StreamStdout, 0)
	if err != nil || output.State != resource.TargetPending || output.Text != "" {
		t.Fatalf("operator output = %+v, %v", output, err)
	}
	// Output can carry secrets: readers may see the Operation, not its output.
	if _, err := s.TargetOutput(ctx, f.reader, op.ID, host.Metadata.ID, resource.StreamStdout, 0); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("reader output = %v", err)
	}
	if _, err := s.TargetOutput(ctx, f.operator, op.ID, host.Metadata.ID, "stdin", 0); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("bad stream = %v", err)
	}
	if _, err := s.TargetOutput(ctx, f.operator, op.ID, host.Metadata.ID, resource.StreamStdout, -1); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("negative offset = %v", err)
	}
	other, _ := resource.NewResourceID()
	if _, err := s.TargetOutput(ctx, f.operator, op.ID, other, resource.StreamStderr, 0); !errors.Is(err, resource.ErrOperationNotFound) {
		t.Fatalf("unknown target = %v", err)
	}
}

// The Operation JSON never carries the live capture, even for callers who
// may read output: it is served by the output endpoint only.
func TestOperationJSONOmitsLiveOutput(t *testing.T) {
	var target resource.OperationTarget
	target.State = resource.TargetRunning
	target.Live = &resource.LiveOutput{}
	target.Live.Stdout.Apply(0, 12, []byte("secret-token"))
	target.Progress = target.Live.Summary()
	data, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-token") || strings.Contains(string(data), "c2VjcmV0") || !strings.Contains(string(data), `"stdoutBytes":12`) {
		t.Fatalf("target JSON = %s", data)
	}
	if redacted := redactOperation(&resource.Operation{Status: resource.OperationStatus{Targets: []resource.OperationTarget{target}}}); redacted.Status.Targets[0].Live != nil {
		t.Fatal("redaction kept the live capture")
	}
}
