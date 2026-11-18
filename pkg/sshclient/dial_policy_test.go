package sshclient

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
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
		AuthMethod: AuthPassword, SecretData: "pw", ExpectedFingerprint: "SHA256:test",
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
	if got := sentinelAccepted(t, strconv.Itoa(port), accepted); got != 1 {
		t.Fatalf("listener saw %d connections besides the sentinel; the default policy must stop the dial", got-1)
	}
}

func TestTestConnectionUsesInjectedDialer(t *testing.T) {
	port, accepted := loopbackListener(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	params := testParams(port)
	params.Dialer = loopbackDialer
	err := TestConnection(ctx, params)
	if err == nil || errors.Is(err, domain.ErrTargetNotAllowed) {
		t.Fatalf("error = %v; the dial should be permitted and then fail during the SSH handshake", err)
	}
	if accepted.Load() != 1 {
		t.Fatalf("listener saw %d connections, want 1", accepted.Load())
	}
}

// sentinelAccepted proves that no earlier connection reached the listener:
// it connects once itself and waits for that connection to be accepted.
// Accepts happen in order, so a count above 1 means something else
// connected first.
func sentinelAccepted(t *testing.T, port string, accepted *atomic.Int32) int32 {
	t.Helper()
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for accepted.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond) // poll interval of a bounded wait
	}
	return accepted.Load()
}
