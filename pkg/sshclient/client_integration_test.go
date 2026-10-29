package sshclient

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/testutil/sshtest"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
	"golang.org/x/crypto/ssh"
)

const serverPassword = "correct horse battery"

// loopbackDialer permits the loopback address the test server listens on; the
// production default refuses it (see dial_policy_test.go).
var loopbackDialer = netpolicy.Policy{AllowLoopback: true}.DialContext

func passwordParams(s *sshtest.Server) DialParams {
	return DialParams{
		IP: s.Host, Port: s.Port, Username: "deploy",
		AuthMethod: domain.AuthTypePassword, SecretData: serverPassword,
		ExpectedFingerprint: s.Fingerprint, Dialer: loopbackDialer,
	}
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func timeout(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestSSHConnectionSucceedsWithPinnedHostKey(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: serverPassword})
	if err := TestConnection(timeout(t, 5*time.Second), passwordParams(server)); err != nil {
		t.Fatalf("TestConnection() error = %v", err)
	}
	eventually(t, "the server to see the client disconnect", func() bool { return server.OpenConnections() == 0 })
}

func TestSSHHostKeyIsVerifiedBeforeAnyCredentialIsSent(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: serverPassword})
	for name, pinned := range map[string]string{
		"mismatched fingerprint": "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"no pinned fingerprint":  "",
	} {
		t.Run(name, func(t *testing.T) {
			params := passwordParams(server)
			params.ExpectedFingerprint = pinned
			err := TestConnection(timeout(t, 5*time.Second), params)

			var unknown *domain.ErrHostKeyUnknown
			if !errors.As(err, &unknown) {
				t.Fatalf("error = %v, want *domain.ErrHostKeyUnknown", err)
			}
			if unknown.Fingerprint != server.Fingerprint || unknown.Algorithm != server.HostKeyType {
				t.Errorf("reported key = %s %s, want the server's %s %s so an operator can review and pin it",
					unknown.Algorithm, unknown.Fingerprint, server.HostKeyType, server.Fingerprint)
			}
			eventually(t, "the connection to close", func() bool { return server.OpenConnections() == 0 })
			if server.AuthAttempts() != 0 {
				t.Errorf("the server saw %d credential attempts; credentials must never reach an unverified host", server.AuthAttempts())
			}
		})
	}
}

func TestSSHAuthenticationFailures(t *testing.T) {
	t.Run("wrong password", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Password: serverPassword})
		params := passwordParams(server)
		params.SecretData = "not the password"
		if err := TestConnection(timeout(t, 5*time.Second), params); !errors.Is(err, domain.ErrAuthenticationFailed) {
			t.Fatalf("error = %v, want ErrAuthenticationFailed", err)
		}
		if server.AuthAttempts() == 0 {
			t.Error("the server should have seen the rejected attempt")
		}
	})
	t.Run("wrong username", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Password: serverPassword})
		params := passwordParams(server)
		params.Username = "intruder"
		if err := TestConnection(timeout(t, 5*time.Second), params); !errors.Is(err, domain.ErrAuthenticationFailed) {
			t.Fatalf("error = %v, want ErrAuthenticationFailed", err)
		}
	})
	t.Run("server does not offer password authentication", func(t *testing.T) {
		_, authorized := sshtest.NewClientKey(t, "")
		server := sshtest.Start(t, sshtest.Options{AuthorizedKeys: []ssh.PublicKey{authorized}})
		if err := TestConnection(timeout(t, 5*time.Second), passwordParams(server)); !errors.Is(err, domain.ErrAuthenticationFailed) {
			t.Fatalf("error = %v, want ErrAuthenticationFailed", err)
		}
	})
}

func TestSSHPrivateKeyAuthentication(t *testing.T) {
	keyParams := func(s *sshtest.Server, privatePEM, passphrase string) DialParams {
		return DialParams{
			IP: s.Host, Port: s.Port, Username: "deploy",
			AuthMethod: domain.AuthTypePrivateKey, SecretData: privatePEM, Passphrase: passphrase,
			ExpectedFingerprint: s.Fingerprint, Dialer: loopbackDialer,
		}
	}

	t.Run("unencrypted key", func(t *testing.T) {
		privatePEM, authorized := sshtest.NewClientKey(t, "")
		server := sshtest.Start(t, sshtest.Options{AuthorizedKeys: []ssh.PublicKey{authorized}})
		if err := TestConnection(timeout(t, 5*time.Second), keyParams(server, privatePEM, "")); err != nil {
			t.Fatalf("TestConnection() error = %v", err)
		}
	})
	t.Run("encrypted key with passphrase", func(t *testing.T) {
		privatePEM, authorized := sshtest.NewClientKey(t, "s3cret passphrase")
		server := sshtest.Start(t, sshtest.Options{AuthorizedKeys: []ssh.PublicKey{authorized}})
		if err := TestConnection(timeout(t, 5*time.Second), keyParams(server, privatePEM, "s3cret passphrase")); err != nil {
			t.Fatalf("TestConnection() error = %v", err)
		}
	})
	t.Run("wrong passphrase is rejected locally without contacting the host", func(t *testing.T) {
		privatePEM, authorized := sshtest.NewClientKey(t, "s3cret passphrase")
		server := sshtest.Start(t, sshtest.Options{AuthorizedKeys: []ssh.PublicKey{authorized}})
		err := TestConnection(timeout(t, 5*time.Second), keyParams(server, privatePEM, "wrong"))
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("error = %v, want ErrInvalidInput", err)
		}
		if server.Accepted() != 0 {
			t.Errorf("the server accepted %d connections; an unusable key must fail before dialing", server.Accepted())
		}
	})
	t.Run("key the server does not trust", func(t *testing.T) {
		_, authorized := sshtest.NewClientKey(t, "")
		otherPEM, _ := sshtest.NewClientKey(t, "")
		server := sshtest.Start(t, sshtest.Options{AuthorizedKeys: []ssh.PublicKey{authorized}})
		if err := TestConnection(timeout(t, 5*time.Second), keyParams(server, otherPEM, "")); !errors.Is(err, domain.ErrAuthenticationFailed) {
			t.Fatalf("error = %v, want ErrAuthenticationFailed", err)
		}
	})
}

func TestSSHUnreachableAndMisbehavingHosts(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Skipf("cannot listen on loopback: %v", err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close() // nothing listens here any more

		params := DialParams{
			IP: "127.0.0.1", Port: port, Username: "deploy", AuthMethod: domain.AuthTypePassword,
			SecretData: serverPassword, ExpectedFingerprint: "SHA256:x", Dialer: loopbackDialer,
		}
		if err := TestConnection(timeout(t, 5*time.Second), params); !errors.Is(err, domain.ErrConnectionRefused) {
			t.Fatalf("error = %v, want ErrConnectionRefused", err)
		}
	})

	t.Run("host that accepts TCP but never answers honors the deadline", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Mode: sshtest.ModeSilent})
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()

		start := time.Now()
		err := TestConnection(ctx, passwordParams(server))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("took %s; a hung host must not stall the caller past its deadline", elapsed)
		}
		eventually(t, "the client to drop the hung connection", func() bool { return server.OpenConnections() == 0 })
	})

	t.Run("cancellation interrupts a handshake in progress", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Mode: sshtest.ModeSilent})
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
		start := time.Now()
		err := TestConnection(ctx, passwordParams(server))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("took %s after cancellation", elapsed)
		}
	})

	t.Run("host that does not speak SSH", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Mode: sshtest.ModeGarbage})
		err := TestConnection(timeout(t, 5*time.Second), passwordParams(server))
		if err == nil {
			t.Fatal("TestConnection() succeeded against a non-SSH service")
		}
		var unknown *domain.ErrHostKeyUnknown
		if errors.As(err, &unknown) || errors.Is(err, domain.ErrAuthenticationFailed) {
			t.Fatalf("a non-SSH service must not look like a key or credential problem: %v", err)
		}
		if !strings.Contains(err.Error(), "negotiate SSH connection") {
			t.Errorf("error = %v, want a negotiation failure", err)
		}
	})
}

func TestSSHConnectionsAreReleased(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: serverPassword})
	for i := 0; i < 20; i++ {
		if err := TestConnection(timeout(t, 5*time.Second), passwordParams(server)); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	eventually(t, "every connection to be released", func() bool { return server.OpenConnections() == 0 })
	if server.Accepted() != 20 {
		t.Errorf("server accepted %d connections, want 20", server.Accepted())
	}
}

func TestSSHConcurrentConnections(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: serverPassword})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- TestConnection(timeout(t, 5*time.Second), passwordParams(server))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent TestConnection() error = %v", err)
		}
	}
	eventually(t, "every connection to be released", func() bool { return server.OpenConnections() == 0 })
}

func TestExecuteRemoteCommand(t *testing.T) {
	echo := func(ctx context.Context, command string, out io.Writer) int {
		switch command {
		case "fail":
			_, _ = io.WriteString(out, "boom")
			return 3
		case "flood":
			chunk := []byte(strings.Repeat("x", 64<<10))
			for written := 0; written < 2<<20; written += len(chunk) {
				if _, err := out.Write(chunk); err != nil {
					return 1
				}
			}
			return 0
		case "block":
			select {
			case <-time.After(10 * time.Second):
			case <-ctx.Done(): // the client went away
			}
			return 0
		}
		_, _ = io.WriteString(out, "ran: "+command)
		return 0
	}

	t.Run("returns output", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Password: serverPassword, Exec: echo})
		out, err := ExecuteRemoteCommand(timeout(t, 5*time.Second), passwordParams(server), "uptime")
		if err != nil || out != "ran: uptime" {
			t.Fatalf("ExecuteRemoteCommand() = %q, %v", out, err)
		}
	})

	t.Run("non-zero exit status is an error that keeps the output", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Password: serverPassword, Exec: echo})
		out, err := ExecuteRemoteCommand(timeout(t, 5*time.Second), passwordParams(server), "fail")
		var exit *ssh.ExitError
		if !errors.As(err, &exit) || exit.ExitStatus() != 3 {
			t.Fatalf("error = %v, want an *ssh.ExitError with status 3", err)
		}
		if out != "boom" {
			t.Errorf("output = %q, want it preserved on failure", out)
		}
	})

	t.Run("output is capped", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Password: serverPassword, Exec: echo})
		out, err := ExecuteRemoteCommand(timeout(t, 10*time.Second), passwordParams(server), "flood")
		if err != nil {
			t.Fatalf("ExecuteRemoteCommand() error = %v", err)
		}
		const marker = "\n[output truncated]"
		if !strings.HasSuffix(out, marker) || len(out) > maxCommandOutputBytes+len(marker) {
			t.Fatalf("output length %d: want at most %d bytes plus the truncation marker", len(out), maxCommandOutputBytes)
		}
	})

	t.Run("nothing runs on a host with an unexpected key", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Password: serverPassword, Exec: echo})
		params := passwordParams(server)
		params.ExpectedFingerprint = "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
		_, err := ExecuteRemoteCommand(timeout(t, 5*time.Second), params, "uptime")
		var unknown *domain.ErrHostKeyUnknown
		if !errors.As(err, &unknown) {
			t.Fatalf("error = %v, want *domain.ErrHostKeyUnknown", err)
		}
		if server.Execs() != 0 || server.AuthAttempts() != 0 {
			t.Errorf("execs=%d authAttempts=%d; neither credentials nor commands may reach an unverified host", server.Execs(), server.AuthAttempts())
		}
	})

	t.Run("blank command is rejected before dialing", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Password: serverPassword, Exec: echo})
		if _, err := ExecuteRemoteCommand(timeout(t, 5*time.Second), passwordParams(server), "   "); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("error = %v, want ErrInvalidInput", err)
		}
		if server.Accepted() != 0 {
			t.Errorf("the server accepted %d connections for a blank command", server.Accepted())
		}
	})

	t.Run("a deadline stops a command that never finishes", func(t *testing.T) {
		server := sshtest.Start(t, sshtest.Options{Password: serverPassword, Exec: echo})
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := ExecuteRemoteCommand(ctx, passwordParams(server), "block")
		if err == nil {
			t.Fatal("ExecuteRemoteCommand() succeeded although the deadline passed mid-command")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("took %s; the deadline must stop a running command", elapsed)
		}
		eventually(t, "the connection to be released", func() bool { return server.OpenConnections() == 0 })
	})
}
