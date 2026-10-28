package sshclient

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

func loopbackListener(t *testing.T) (port int, accepted *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted = new(atomic.Int32)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port, accepted
}

func testParams(port int) DialParams {
	return DialParams{
		IP: "127.0.0.1", Port: port, Username: "deploy",
		AuthMethod: domain.AuthTypePassword, SecretData: "pw", ExpectedFingerprint: "SHA256:test",
	}
}

func TestTestConnectionRefusesLoopbackByDefault(t *testing.T) {
	port, accepted := loopbackListener(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := TestConnection(ctx, testParams(port))
	if !errors.Is(err, domain.ErrTargetNotAllowed) {
		t.Fatalf("TestConnection() error = %v, want ErrTargetNotAllowed", err)
	}
	time.Sleep(50 * time.Millisecond)
	if accepted.Load() != 0 {
		t.Fatalf("listener saw %d connections; the default policy must stop the dial", accepted.Load())
	}
}

func TestTestConnectionUsesInjectedDialer(t *testing.T) {
	port, accepted := loopbackListener(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	params := testParams(port)
	params.Dialer = netpolicy.Policy{AllowLoopback: true}.DialContext
	err := TestConnection(ctx, params)
	if err == nil || errors.Is(err, domain.ErrTargetNotAllowed) {
		t.Fatalf("error = %v; the dial should be permitted and then fail during the SSH handshake", err)
	}
	if accepted.Load() != 1 {
		t.Fatalf("listener saw %d connections, want 1", accepted.Load())
	}
}
