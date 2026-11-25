package hostaccess

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/testutil/sshtest"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

type testResolver struct {
	request secrets.ResolutionRequest
	err     error
}

func (r *testResolver) Resolve(_ context.Context, request secrets.ResolutionRequest) ([]byte, error) {
	r.request = request
	if r.err != nil {
		return nil, r.err
	}
	return []byte("ssh-test-password"), nil
}

func TestSSHConnectorVerifiesPinnedHostAndExecutesBoundedArgv(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: "ssh-test-password", ExecStreams: func(_ context.Context, command string, stdout, stderr io.Writer) int {
		_, _ = stdout.Write([]byte("stdout:" + command))
		_, _ = stderr.Write([]byte("stderr:" + command))
		return 0
	}})
	resolver := &testResolver{}
	policy := netpolicy.Policy{AllowLoopback: true, AllowedPorts: []uint16{uint16(server.Port)}}
	connector, err := NewSSHConnector(resolver, policy, 1, 2*time.Second, secrets.WorkloadOperationWorker)
	if err != nil {
		t.Fatal(err)
	}
	host, revision := testHostAndRevision(t, server.Addr(), server.Fingerprint)

	result, err := connector.Execute(context.Background(), host, revision, "op-ignored-by-test-resolver", []string{"printf", "hello world", "'"})
	if err != nil {
		t.Fatalf("Execute(): %v", err)
	}
	if !strings.Contains(result.Stdout, "'hello world'") || !strings.Contains(result.Stdout, "'\\''") || !strings.Contains(result.Stderr, "stderr:") {
		t.Fatalf("remote argv was not safely quoted: %q", result.Stdout)
	}
	if resolver.request.WorkloadID != secrets.WorkloadOperationWorker || resolver.request.OperationID != "op-ignored-by-test-resolver" {
		t.Fatalf("secret resolution was not operation-bound: %+v", resolver.request)
	}
}

func TestSSHConnectorRejectsIncorrectHostKeyBeforeAuthentication(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: "ssh-test-password"})
	resolver := &testResolver{}
	connector, err := NewSSHConnector(resolver, netpolicy.Policy{AllowLoopback: true, AllowedPorts: []uint16{uint16(server.Port)}}, 1, time.Second, secrets.WorkloadControlManager)
	if err != nil {
		t.Fatal(err)
	}
	host, revision := testHostAndRevision(t, server.Addr(), "SHA256:incorrect")
	err = connector.Verify(context.Background(), host, revision)
	if err == nil || !strings.Contains(err.Error(), "host key verification") {
		t.Fatalf("Verify() error = %v", err)
	}
	if server.AuthAttempts() != 0 {
		t.Fatalf("authentication attempted before host key validation: %d", server.AuthAttempts())
	}
}

func TestSSHConnectorHonorsCancellationWhileWaitingForCapacity(t *testing.T) {
	resolver := &testResolver{err: errors.New("secret unavailable")}
	connector, err := NewSSHConnector(resolver, netpolicy.Default(), 1, time.Second, secrets.WorkloadControlManager)
	if err != nil {
		t.Fatal(err)
	}
	connector.slots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = connector.acquire(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire() error=%v", err)
	}
}

func testHostAndRevision(t *testing.T, endpoint, fingerprint string) (*resource.Host, *resource.AccessProfileRevision) {
	t.Helper()
	profileID, err := resource.NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	host, err := resource.NewHost(1, 0, "managed", nil, resource.HostSpec{AccessProfileID: profileID,
		AccessTarget: resource.HostAccessTarget{Endpoint: endpoint, Trust: []string{fingerprint}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	revision := &resource.AccessProfileRevision{ProfileID: profileID, WorkspaceID: 1, Revision: 1,
		Spec: resource.AccessProfileSpec{Connector: resource.ConnectorSSH, Username: "deploy", Authentication: resource.AuthenticationPassword,
			Credential: resource.SecretReference{ID: "secret-id", Version: 2}, Enabled: true}, CreatedAt: time.Now(), CreatedBy: 1}
	return host, revision
}
