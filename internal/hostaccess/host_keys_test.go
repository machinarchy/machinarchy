package hostaccess_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/ssh"

	"github.com/machinarchy/machinarchy/internal/controlmanager"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/hostaccess"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/store"
	"github.com/machinarchy/machinarchy/internal/testutil/sshtest"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

// trustEnv runs the real Host controller loop over SQLite stores, the Secret
// Store and SSH test servers, with a chosen deployment default policy.
type trustEnv struct {
	t       *testing.T
	ctx     context.Context
	stores  *store.Stores
	app     *service.ManagedResourceService
	secrets *secrets.Service
	owner   domain.Principal
	ports   []uint16
	policy  resource.HostKeyPolicy
	metrics *prometheus.Registry
}

func newTrustEnv(t *testing.T, deploymentDefault resource.HostKeyPolicy, servers ...*sshtest.Server) *trustEnv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stores, err := store.Open(ctx, store.Config{Engine: store.EngineSQLite, SQLitePath: filepath.Join(dir, "k.db"), EncryptionSecret: "host-key-test-key-material-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	authorizer, err := secrets.NewProfileAuthorizer(e2eBindings{stores}, secrets.WorkloadControlManager)
	if err != nil {
		t.Fatal(err)
	}
	secretService, err := secrets.New(stores.Management, &secrets.StaticKeyProvider{KeyID: "k", Key: []byte("0123456789abcdef0123456789abcdef")}, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	app, err := service.NewManagedResourceService(stores.ResourceHosts, stores.ResourceProfiles, stores.Operations,
		func() domain.ExecutionPolicy { return domain.ExecutionPolicy{} }, secretService)
	if err != nil {
		t.Fatal(err)
	}
	e := &trustEnv{t: t, ctx: ctx, stores: stores, app: app, secrets: secretService,
		owner: domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner}, policy: deploymentDefault,
		metrics: prometheus.NewRegistry()}
	for _, server := range servers {
		e.ports = append(e.ports, uint16(server.Port))
	}
	connector, err := hostaccess.NewSSHConnector(secretService, netpolicy.Policy{AllowLoopback: true, AllowedPorts: e.ports}, 4, 5*time.Second, secrets.WorkloadControlManager)
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := hostaccess.NewHostReconciler(stores.ResourceProfiles, connector, hostaccess.WithDefaultHostKeyPolicy(deploymentDefault))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := controlmanager.NewEngine(controlmanager.Dependencies{Hosts: stores.ResourceHosts, Profiles: stores.ResourceProfiles, Outbox: stores.Outbox}, controlmanager.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	controller, err := controlmanager.NewHostControllerWithResync(stores.ResourceHosts, reconciler, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	controller.RegisterMetrics(e.metrics)
	if err := engine.Register(controller); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return e
}

// hostKeyEvents reads machinarchy_controlmanager_host_key_events_total.
func (e *trustEnv) hostKeyEvents(event string) float64 {
	e.t.Helper()
	families, err := e.metrics.Gather()
	if err != nil {
		e.t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "machinarchy_controlmanager_host_key_events_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "event" && label.GetValue() == event {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func (e *trustEnv) auditEvents(action string) []domain.AuditEvent {
	e.t.Helper()
	events, err := e.stores.Audit.ListAuditEvents(e.ctx, 1, domain.AuditFilter{Action: action, Limit: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	return events
}

// profile creates an Access Profile with a private key (authorized on the
// given servers) or a password.
func (e *trustEnv) profile(name string, auth resource.AuthenticationMethod, credential string, mutate func(*resource.AccessProfileSpec)) resource.ID {
	e.t.Helper()
	secret, err := e.secrets.Create(e.ctx, e.owner, secrets.Input{Name: name, Purpose: secrets.PurposeSSHCredential, Value: credential})
	if err != nil {
		e.t.Fatal(err)
	}
	spec := resource.AccessProfileSpec{Connector: resource.ConnectorSSH, Username: "deploy", Authentication: auth,
		Credential: resource.SecretReference{ID: secret.ID, Version: secret.CurrentVersion}, Enabled: true}
	if mutate != nil {
		mutate(&spec)
	}
	profile, err := e.app.CreateAccessProfile(e.ctx, e.owner, name, "", spec)
	if err != nil {
		e.t.Fatal(err)
	}
	return profile.ID
}

func (e *trustEnv) host(name string, profile resource.ID, server *sshtest.Server, trust ...string) resource.ID {
	e.t.Helper()
	host, err := e.app.CreateHost(e.ctx, e.owner, name, nil, resource.HostSpec{AccessProfileID: profile,
		AccessTarget: resource.HostAccessTarget{Endpoint: server.Addr(), Trust: trust}})
	if err != nil {
		e.t.Fatal(err)
	}
	return host.Metadata.ID
}

// waitReady waits for the Host's Ready condition to have reason want.
func (e *trustEnv) waitReady(id resource.ID, want string) *resource.Host {
	e.t.Helper()
	var current *resource.Host
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		host, err := e.stores.ResourceHosts.GetHost(e.ctx, 1, id)
		if err == nil {
			current = host
			for _, c := range host.Status.Conditions {
				if c.Type == "Ready" && c.Reason == want && host.Status.ObservedGeneration == host.Metadata.Generation {
					return host
				}
			}
		}
		time.Sleep(20 * time.Millisecond) // poll interval of a bounded wait
	}
	e.t.Fatalf("Host never reached Ready reason %s: %+v", want, current.Status)
	return nil
}

func sorted(values []string) string {
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	return strings.Join(copied, ",")
}

func clientKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	return sshtest.NewClientKey(t, "")
}

// TOFU: the keys seen on first contact are pinned before anything
// authenticates, then the Host verifies against them.
func TestTOFUPinsEveryHostKeyThenVerifies(t *testing.T) {
	private, public := clientKey(t)
	server := sshtest.Start(t, sshtest.Options{AuthorizedKeys: []ssh.PublicKey{public}, ExtraECDSAHostKey: true})
	e := newTrustEnv(t, resource.HostKeyPolicyApprove, server)
	profile := e.profile("key-profile", resource.AuthenticationPrivateKey, private,
		func(s *resource.AccessProfileSpec) { s.HostKeyPolicy = resource.HostKeyPolicyTOFU })
	id := e.host("web-1", profile, server)

	host := e.waitReady(id, "SSHVerified")
	if got, want := sorted(host.Spec.AccessTarget.Trust), sorted(server.Fingerprints); got != want {
		t.Fatalf("pinned %s, want every presented key %s", got, want)
	}
	events, err := e.stores.Audit.ListAuditEvents(e.ctx, 1, domain.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var trusted []domain.AuditEvent
	for _, event := range events {
		if event.Action == domain.AuditResourceHostKeyTrusted {
			trusted = append(trusted, event)
		}
	}
	if len(trusted) != 1 || trusted[0].Details["source"] != "tofu" || !strings.Contains(trusted[0].Details["fingerprints"], server.Fingerprint) {
		t.Fatalf("host key trust audit events = %+v", trusted)
	}
	if got := e.hostKeyEvents("tofu_pinned"); got != 1 {
		t.Fatalf("host_key_events_total{event=tofu_pinned} = %v, want 1", got)
	}
}

// Approve: keys are observed without sending anything; an owner approves
// them in one call, and only then does anything authenticate.
func TestApproveWaitsForTheOwner(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: "pw"})
	// The deployment default is tofu, but a password profile never trusts
	// on first use: it falls back to approval.
	e := newTrustEnv(t, resource.HostKeyPolicyTOFU, server)
	profile := e.profile("password-profile", resource.AuthenticationPassword, "pw", nil)
	id := e.host("db-1", profile, server)

	host := e.waitReady(id, "HostKeyPendingApproval")
	if len(host.Status.ObservedHostKeys) != 1 || host.Status.ObservedHostKeys[0].Fingerprint != server.Fingerprint ||
		host.Status.ObservedHostKeys[0].FirstSeenAt.IsZero() || len(host.Spec.AccessTarget.Trust) != 0 {
		t.Fatalf("pending host = %+v", host)
	}
	if attempts := server.AuthAttempts(); attempts != 0 {
		t.Fatalf("the password was offered %d times before the key was approved", attempts)
	}

	reviewed := []string{host.Status.ObservedHostKeys[0].Fingerprint}
	operator := domain.Principal{UserID: 2, WorkspaceID: 1, Role: domain.RoleOperator}
	if _, err := e.app.TrustHostKeys(e.ctx, operator, id, host.Metadata.Generation, reviewed, false); !errors.Is(err, service.ErrAccessProfileOwnerRequired) {
		t.Fatalf("operator approval = %v", err)
	}
	if _, err := e.app.TrustHostKeys(e.ctx, e.owner, id, host.Metadata.Generation, nil, false); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("approval without naming the reviewed keys = %v, want ErrInvalidInput", err)
	}
	if _, err := e.app.TrustHostKeys(e.ctx, e.owner, id, host.Metadata.Generation+1, reviewed, false); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("approval against another spec generation = %v, want ErrConflict", err)
	}
	// The controller keeps writing status meanwhile; approval must not race
	// it. Wait until at least one more observation has landed.
	deadline := time.Now().Add(10 * time.Second)
	for {
		current, err := e.stores.ResourceHosts.GetHost(e.ctx, 1, id)
		if err == nil && current.Metadata.Revision > host.Metadata.Revision {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the controller did not observe the Host again")
		}
		time.Sleep(20 * time.Millisecond) // poll interval of a bounded wait
	}
	if _, err := e.app.TrustHostKeys(e.ctx, e.owner, id, host.Metadata.Generation, reviewed, false); err != nil {
		t.Fatalf("owner approval of the reviewed keys: %v", err)
	}
	e.waitReady(id, "SSHVerified")
	if server.AuthAttempts() == 0 {
		t.Fatal("the Host was marked verified without authenticating")
	}
}

// A changed key is reported, nothing is sent to the host, and trust stays as
// it was until an owner replaces it explicitly.
func TestChangedHostKeyIsNeverAccepted(t *testing.T) {
	impostor := sshtest.Start(t, sshtest.Options{Password: "pw"})
	e := newTrustEnv(t, resource.HostKeyPolicyTOFU, impostor)
	private, _ := clientKey(t)
	profile := e.profile("key-profile", resource.AuthenticationPrivateKey, private,
		func(s *resource.AccessProfileSpec) { s.HostKeyPolicy = resource.HostKeyPolicyTOFU })
	const original = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	id := e.host("web-2", profile, impostor, original)

	host := e.waitReady(id, "HostKeyChanged")
	if impostor.AuthAttempts() != 0 {
		t.Fatalf("credentials were offered to a host with a changed key (%d attempts)", impostor.AuthAttempts())
	}
	if len(host.Spec.AccessTarget.Trust) != 1 || host.Spec.AccessTarget.Trust[0] != original {
		t.Fatalf("trust was changed to %v; TOFU must never replace a pinned key", host.Spec.AccessTarget.Trust)
	}
	message := ""
	for _, c := range host.Status.Conditions {
		if c.Type == "Ready" {
			message = c.Message
		}
	}
	if !strings.Contains(message, impostor.Fingerprint) || !strings.Contains(message, original) {
		t.Fatalf("condition message %q must name the presented and trusted keys", message)
	}
	// The controller retries every second; the change is audited and
	// counted once, not on every retry.
	revision := host.Metadata.Revision
	deadline := time.Now().Add(10 * time.Second)
	for {
		current, err := e.stores.ResourceHosts.GetHost(e.ctx, 1, id)
		if err != nil {
			t.Fatal(err)
		}
		if current.Metadata.Revision >= revision+2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the controller did not retry the changed Host")
		}
		time.Sleep(50 * time.Millisecond) // poll interval of a deadline-bounded wait
	}
	changed := e.auditEvents(domain.AuditResourceHostKeyChanged)
	if len(changed) != 1 || changed[0].Details["presented"] != impostor.Fingerprint || changed[0].Details["trusted"] != original {
		t.Fatalf("host key change audit events = %+v, want one naming both keys", changed)
	}
	if got := e.hostKeyEvents("changed"); got != 1 {
		t.Fatalf("host_key_events_total{event=changed} = %v, want 1", got)
	}
	presented := []string{impostor.Fingerprint}
	if _, err := e.app.TrustHostKeys(e.ctx, e.owner, id, host.Metadata.Generation, presented, false); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("approval without replace = %v, want ErrConflict", err)
	}
	if _, err := e.app.TrustHostKeys(e.ctx, e.owner, id, host.Metadata.Generation, presented, true); err != nil {
		t.Fatalf("explicit replace: %v", err)
	}
}

// pinned never scans: without a trusted key nothing connects at all.
func TestPinnedPolicyNeverScans(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: "pw"})
	e := newTrustEnv(t, resource.HostKeyPolicyPinned, server)
	profile := e.profile("pinned-profile", resource.AuthenticationPassword, "pw", nil)
	id := e.host("legacy-1", profile, server)
	e.waitReady(id, "HostKeyNotTrusted")
	if server.Accepted() != 0 {
		t.Fatalf("a pinned profile without trust connected %d times", server.Accepted())
	}
}

// ca: a host certificate from the profile's CA is enough, no fingerprint.
func TestCertificateAuthorityPolicy(t *testing.T) {
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := ssh.NewSignerFromKey(caKey)
	if err != nil {
		t.Fatal(err)
	}
	certified := sshtest.Start(t, sshtest.Options{Password: "pw", HostCertificateAuthority: ca})
	e := newTrustEnv(t, resource.HostKeyPolicyApprove, certified)
	profile := e.profile("ca-profile", resource.AuthenticationPassword, "pw", func(s *resource.AccessProfileSpec) {
		s.HostKeyPolicy = resource.HostKeyPolicyCA
		s.HostCertificateAuthorities = []string{string(ssh.MarshalAuthorizedKey(ca.PublicKey()))}
	})
	id := e.host("ca-1", profile, certified)
	host := e.waitReady(id, "SSHVerified")
	if len(host.Spec.AccessTarget.Trust) != 0 {
		t.Fatalf("ca policy pinned fingerprints: %v", host.Spec.AccessTarget.Trust)
	}
}

func TestProfilePolicyValidation(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: "pw"})
	e := newTrustEnv(t, resource.HostKeyPolicyApprove, server)
	secret, err := e.secrets.Create(e.ctx, e.owner, secrets.Input{Name: "s", Purpose: secrets.PurposeSSHCredential, Value: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	base := resource.AccessProfileSpec{Connector: resource.ConnectorSSH, Username: "deploy", Authentication: resource.AuthenticationPassword,
		Credential: resource.SecretReference{ID: secret.ID, Version: secret.CurrentVersion}, Enabled: true}
	for name, mutate := range map[string]func(*resource.AccessProfileSpec){
		"tofu with a password": func(s *resource.AccessProfileSpec) { s.HostKeyPolicy = resource.HostKeyPolicyTOFU },
		"ca without keys":      func(s *resource.AccessProfileSpec) { s.HostKeyPolicy = resource.HostKeyPolicyCA },
		"ca with garbage": func(s *resource.AccessProfileSpec) {
			s.HostKeyPolicy, s.HostCertificateAuthorities = resource.HostKeyPolicyCA, []string{"nope"}
		},
		"unknown policy": func(s *resource.AccessProfileSpec) { s.HostKeyPolicy = "trust-everything" },
	} {
		spec := base
		mutate(&spec)
		if _, err := e.app.CreateAccessProfile(e.ctx, e.owner, "p-"+strings.ReplaceAll(name, " ", "-"), "", spec); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
