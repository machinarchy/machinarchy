package hostaccess_test

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/hostaccess"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/testutil/sshtest"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

// useChecker gives the service the same verifier the API process builds.
func (e *trustEnv) useChecker() {
	e.t.Helper()
	connector, err := hostaccess.NewSSHConnector(e.secrets, netpolicy.Policy{AllowLoopback: true, AllowedPorts: e.ports}, 4, 5*time.Second, secrets.WorkloadControlManager)
	if err != nil {
		e.t.Fatal(err)
	}
	reconciler, err := hostaccess.NewHostReconciler(e.stores.ResourceProfiles, connector, hostaccess.WithDefaultHostKeyPolicy(resource.HostKeyPolicyApprove))
	if err != nil {
		e.t.Fatal(err)
	}
	e.app.UseHostChecker(reconciler)
}

// A check against the trusted key verifies sign-in, sends no command and
// leaves the Host's spec alone.
func TestCheckHostVerifiesAgainstTheTrustedKey(t *testing.T) {
	private, public := clientKey(t)
	server := sshtest.Start(t, sshtest.Options{AuthorizedKeys: []ssh.PublicKey{public}})
	e := newTrustEnv(t, resource.HostKeyPolicyApprove, server)
	profile := e.profile("key-profile", resource.AuthenticationPrivateKey, private,
		func(s *resource.AccessProfileSpec) { s.HostKeyPolicy = resource.HostKeyPolicyPinned })
	id := e.host("web-1", profile, server, server.Fingerprint)
	e.useChecker()
	before, err := e.stores.ResourceHosts.GetHost(e.ctx, 1, id)
	if err != nil {
		t.Fatal(err)
	}

	got, err := e.app.CheckHost(e.ctx, e.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Ready || !got.Reachable || got.Reason != "SSHVerified" || got.HostID != id || got.CheckedAt.IsZero() {
		t.Fatalf("check = %+v", got)
	}
	if server.Execs() != 0 {
		t.Fatalf("a check ran %d commands", server.Execs())
	}
	after, err := e.stores.ResourceHosts.GetHost(e.ctx, 1, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Metadata.Generation != before.Metadata.Generation || strings.Join(after.Spec.AccessTarget.Trust, ",") != server.Fingerprint {
		t.Fatalf("check changed the spec: %+v", after.Spec)
	}
}

// A host presenting a key other than the trusted one fails closed: no
// credential is offered, nothing is trusted, and the answer names the reason.
func TestCheckHostFailsClosedOnAChangedKey(t *testing.T) {
	private, public := clientKey(t)
	server := sshtest.Start(t, sshtest.Options{AuthorizedKeys: []ssh.PublicKey{public}})
	e := newTrustEnv(t, resource.HostKeyPolicyApprove, server)
	profile := e.profile("key-profile", resource.AuthenticationPrivateKey, private,
		func(s *resource.AccessProfileSpec) { s.HostKeyPolicy = resource.HostKeyPolicyPinned })
	stale := "SHA256:" + strings.Repeat("A", 43)
	id := e.host("web-1", profile, server, stale)
	e.useChecker()

	got, err := e.app.CheckHost(e.ctx, e.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready || got.Reason != "HostKeyChanged" {
		t.Fatalf("check = %+v, want not ready with HostKeyChanged", got)
	}
	if strings.Contains(got.Message, "PRIVATE KEY") || server.AuthAttempts() != 0 || server.Execs() != 0 {
		t.Fatalf("a changed key reached authentication: attempts=%d execs=%d message=%q", server.AuthAttempts(), server.Execs(), got.Message)
	}
	host, err := e.stores.ResourceHosts.GetHost(e.ctx, 1, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(host.Spec.AccessTarget.Trust) != 1 || host.Spec.AccessTarget.Trust[0] != stale {
		t.Fatalf("check replaced the trusted key: %v", host.Spec.AccessTarget.Trust)
	}
}

// A host with no trusted key and an approval policy is reported as waiting
// for an owner, and the check does not approve it.
func TestCheckHostDoesNotApprove(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: "pw"})
	e := newTrustEnv(t, resource.HostKeyPolicyApprove, server)
	profile := e.profile("password-profile", resource.AuthenticationPassword, "pw", nil)
	id := e.host("db-1", profile, server)
	e.useChecker()

	got, err := e.app.CheckHost(e.ctx, e.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready || got.Reason != "HostKeyPendingApproval" || !strings.Contains(got.Message, "hosts trust db-1") {
		t.Fatalf("check = %+v", got)
	}
	if server.AuthAttempts() != 0 {
		t.Fatalf("the password was offered %d times before approval", server.AuthAttempts())
	}
	host, err := e.stores.ResourceHosts.GetHost(e.ctx, 1, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(host.Spec.AccessTarget.Trust) != 0 {
		t.Fatalf("check trusted %v", host.Spec.AccessTarget.Trust)
	}
}

// The address policy applies to checks like every other dial.
func TestCheckHostHonoursTheAddressPolicy(t *testing.T) {
	private, public := clientKey(t)
	server := sshtest.Start(t, sshtest.Options{AuthorizedKeys: []ssh.PublicKey{public}})
	e := newTrustEnv(t, resource.HostKeyPolicyApprove, server)
	profile := e.profile("key-profile", resource.AuthenticationPrivateKey, private,
		func(s *resource.AccessProfileSpec) { s.HostKeyPolicy = resource.HostKeyPolicyPinned })
	id := e.host("web-1", profile, server, server.Fingerprint)
	connector, err := hostaccess.NewSSHConnector(e.secrets, netpolicy.Policy{AllowedPorts: e.ports}, 4, 5*time.Second, secrets.WorkloadControlManager) // loopback denied
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := hostaccess.NewHostReconciler(e.stores.ResourceProfiles, connector)
	if err != nil {
		t.Fatal(err)
	}
	e.app.UseHostChecker(reconciler)

	got, err := e.app.CheckHost(e.ctx, e.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready || got.Reason != "TargetNotAllowed" || server.Accepted() != 0 {
		t.Fatalf("check = %+v accepted=%d", got, server.Accepted())
	}
}
