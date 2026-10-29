package service

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/testutil/sshtest"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

const integrationPassword = "correct horse battery"

// realSSHService builds a host service that uses the real SSH tester (not a
// fake) and permits the loopback address the test servers listen on.
func realSSHService(t *testing.T, store *bulkFakeStore, policy netpolicy.Policy) HostService {
	t.Helper()
	capacity, err := NewSSHCapacity(8)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewHostServiceWithCapacity(store, nil, capacity, WithTargetPolicy(policy))
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func hostFor(server *sshtest.Server, name, password, fingerprint string) *domain.Host {
	return &domain.Host{
		Name: name, IPAddress: server.Host,
		SSH: &domain.SSHAccess{
			Port: server.Port, Username: "deploy",
			AuthType: domain.SSHAuthType{Method: domain.AuthTypePassword, Password: password, ExpectedFingerprint: fingerprint},
		},
	}
}

var allowLoopback = netpolicy.Policy{AllowLoopback: true}

func TestRegisterHostAgainstARealSSHServer(t *testing.T) {
	ctx := context.Background()

	t.Run("verified host is stored as active", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Password: integrationPassword})
		store := &bulkFakeStore{}
		svc := realSSHService(t, store, allowLoopback)

		host := hostFor(server, "web-01", integrationPassword, server.Fingerprint)
		if err := svc.RegisterHost(ctx, host); err != nil {
			t.Fatalf("RegisterHost() error = %v", err)
		}
		if len(store.created) != 1 || store.created[0].SSH.Status != domain.HostStatusActive || host.ID == "" {
			t.Fatalf("stored hosts = %+v; want one active host with an ID", store.created)
		}
		if server.AuthAttempts() == 0 {
			t.Error("the server should have seen a login")
		}
	})

	t.Run("wrong fingerprint returns the real key and stores nothing", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Password: integrationPassword})
		store := &bulkFakeStore{}
		svc := realSSHService(t, store, allowLoopback)

		err := svc.RegisterHost(ctx, hostFor(server, "web-02", integrationPassword, "SHA256:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"))
		var unknown *domain.ErrHostKeyUnknown
		if !errors.As(err, &unknown) || unknown.Fingerprint != server.Fingerprint {
			t.Fatalf("error = %v, want ErrHostKeyUnknown carrying the server's fingerprint %s", err, server.Fingerprint)
		}
		if len(store.created) != 0 || server.AuthAttempts() != 0 {
			t.Errorf("stored=%d authAttempts=%d; nothing may be stored or sent to an unverified host", len(store.created), server.AuthAttempts())
		}
	})

	t.Run("wrong password stores nothing", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Password: integrationPassword})
		store := &bulkFakeStore{}
		svc := realSSHService(t, store, allowLoopback)

		err := svc.RegisterHost(ctx, hostFor(server, "web-03", "wrong password", server.Fingerprint))
		if !errors.Is(err, domain.ErrAuthenticationFailed) || len(store.created) != 0 {
			t.Fatalf("error = %v, stored = %d; want ErrAuthenticationFailed and nothing stored", err, len(store.created))
		}
	})

	t.Run("the default policy never reaches a loopback server", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Password: integrationPassword})
		store := &bulkFakeStore{}
		svc := realSSHService(t, store, netpolicy.Default())

		err := svc.RegisterHost(ctx, hostFor(server, "web-04", integrationPassword, server.Fingerprint))
		if !errors.Is(err, domain.ErrTargetNotAllowed) {
			t.Fatalf("error = %v, want ErrTargetNotAllowed", err)
		}
		if server.Accepted() != 0 || len(store.created) != 0 {
			t.Errorf("accepted=%d stored=%d; a forbidden target must not even be dialed", server.Accepted(), len(store.created))
		}
	})

	t.Run("a hung host cannot stall registration", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Mode: sshtest.ModeSilent})
		svc := realSSHService(t, &bulkFakeStore{}, allowLoopback)

		callCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := svc.RegisterHost(callCtx, hostFor(server, "web-05", integrationPassword, server.Fingerprint))
		if err == nil || time.Since(start) > 5*time.Second {
			t.Fatalf("error = %v after %s; a hung host must fail promptly", err, time.Since(start))
		}
	})
}

func TestBulkRegisterEndToEndAgainstRealServers(t *testing.T) {
	good := sshtest.Start(t, sshtest.Options{Password: integrationPassword})
	wrongKey := sshtest.Start(t, sshtest.Options{Password: integrationPassword})
	wrongPassword := sshtest.Start(t, sshtest.Options{Password: integrationPassword})

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	refusedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	refused := &domain.Host{
		Name: "gone", IPAddress: "127.0.0.1",
		SSH: &domain.SSHAccess{
			Port: refusedPort, Username: "deploy",
			AuthType: domain.SSHAuthType{Method: domain.AuthTypePassword, Password: integrationPassword, ExpectedFingerprint: "SHA256:x"},
		},
	}

	store := &bulkFakeStore{}
	svc := realSSHService(t, store, allowLoopback)
	res, err := svc.BulkRegisterHosts(context.Background(), 1, []domain.BulkHostEntry{
		{Host: hostFor(good, "ok", integrationPassword, good.Fingerprint)},
		{Host: hostFor(wrongKey, "wrong-key", integrationPassword, "SHA256:DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD")},
		{Host: hostFor(wrongPassword, "wrong-password", "nope", wrongPassword.Fingerprint)},
		{Host: refused},
	}, BulkRegisterOptions{})
	if err != nil {
		t.Fatal(err)
	}

	want := []domain.HostResultStatus{domain.StatusSuccess, domain.StatusFailed, domain.StatusFailed, domain.StatusFailed}
	for i, status := range want {
		if res.Results[i].Status != status {
			t.Errorf("result[%d] = %+v, want status %s", i, res.Results[i], status)
		}
	}
	if res.SuccessCount != 1 || res.FailureCount != 3 || len(store.created) != 1 {
		t.Fatalf("counters = %+v, stored = %d; one success, three isolated failures", res, len(store.created))
	}
	if !strings.Contains(res.Results[1].Error, wrongKey.Fingerprint) {
		t.Errorf("wrong-key result %q should show the key the host presented so it can be reviewed", res.Results[1].Error)
	}
	if res.Results[2].Error != "SSH authentication failed" {
		t.Errorf("wrong-password result = %q", res.Results[2].Error)
	}
	if !strings.Contains(res.Results[3].Error, "refused") {
		t.Errorf("refused result = %q", res.Results[3].Error)
	}
	if wrongKey.AuthAttempts() != 0 {
		t.Errorf("credentials reached the host with the wrong key (%d attempts)", wrongKey.AuthAttempts())
	}
}
