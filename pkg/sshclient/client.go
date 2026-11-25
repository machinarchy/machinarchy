// Package sshclient provides SSH client functionality for connecting to and
// communicating with remote hosts.
package sshclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

const (
	defaultHandshakeTimeout = 10 * time.Second
	maxCommandOutputBytes   = 1 << 20
)

// handshakeTimeout bounds the SSH handshake of one connection: a host that
// accepts TCP and never speaks must not hold its slot for the length of the
// call (a command may be allowed 15 minutes). A variable so tests can shorten it.
var handshakeTimeout = defaultHandshakeTimeout

// AuthMethod selects how the client authenticates.
type AuthMethod string

// Supported authentication methods.
const (
	AuthPassword   AuthMethod = "PASSWORD"
	AuthPrivateKey AuthMethod = "PRIVATE_KEY"
)

// DialParams contains the parameters required to establish an SSH connection.
type DialParams struct {
	IP         string
	Port       int
	Username   string
	AuthMethod AuthMethod
	SecretData string
	Passphrase string
	// ExpectedFingerprint and TrustedFingerprints are the accepted SHA-256
	// host key fingerprints (one per key algorithm the host offers).
	ExpectedFingerprint string
	TrustedFingerprints []string
	// HostCertAuthorities, when set, also accept a host certificate signed by
	// one of these keys for the dialed host name.
	HostCertAuthorities []ssh.PublicKey
	// Dialer opens the TCP connection. When nil, a netpolicy.Default() dialer is
	// used, so targets are always resolved and checked under a policy and the
	// validated address is the one dialed. Callers with a configured policy pass
	// its DialContext.
	Dialer func(ctx context.Context, network, address string) (net.Conn, error)
}

// TestConnection verifies SSH authentication and the pinned host key.
func TestConnection(ctx context.Context, params DialParams) error {
	client, cleanup, err := dial(ctx, params)
	if err != nil {
		return err
	}
	defer cleanup()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("create SSH session: %w", err)
	}
	return session.Close()
}

// ExecuteRemoteCommand executes a command after validating the pinned SSH host
// key. Output is capped to prevent an untrusted remote command from consuming
// unbounded memory in the API process.
func ExecuteRemoteCommand(ctx context.Context, params DialParams, command string) (string, error) {
	result, err := ExecuteRemoteCommandDetailed(ctx, params, command)
	return result.Stdout + result.Stderr, err
}

// ExecutionResult preserves stdout and stderr independently. Output is capped
// per stream; Truncated reports whether either stream exceeded the cap.
type ExecutionResult struct {
	Stdout    string
	Stderr    string
	Truncated bool
}

// ExecuteRemoteCommandDetailed executes a command with strict pinned host-key
// validation, bounded output, and cancellation that closes an active session.
func ExecuteRemoteCommandDetailed(ctx context.Context, params DialParams, command string) (ExecutionResult, error) {
	if strings.TrimSpace(command) == "" {
		return ExecutionResult{}, fmt.Errorf("%w: command is required", domain.ErrInvalidInput)
	}

	client, cleanup, err := dial(ctx, params)
	if err != nil {
		return ExecutionResult{}, err
	}
	defer cleanup()
	stopCancellation := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopCancellation()

	session, err := client.NewSession()
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("create SSH session: %w", err)
	}
	defer func() {
		// After Run the server has normally closed the channel already, and
		// Close reports io.EOF: that is not a failure worth a warning (it
		// would be logged on every detached poll).
		if err := session.Close(); err != nil && !errors.Is(err, io.EOF) {
			slog.Warn("failed to close SSH session", "error", err)
		}
	}()

	stdout := &limitedBuffer{limit: maxCommandOutputBytes / 2}
	stderr := &limitedBuffer{limit: maxCommandOutputBytes / 2}
	session.Stdout = stdout
	session.Stderr = stderr
	err = session.Run(command)
	result := ExecutionResult{Stdout: stdout.String(), Stderr: stderr.String(), Truncated: stdout.Truncated() || stderr.Truncated()}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	return result, err
}

// ExitStatus reports the exit status of a remote command that ran and exited
// unsuccessfully. It returns false for every other kind of error, including
// failures to connect, so callers can tell "the command failed" from "the
// command may not have run".
func ExitStatus(err error) (int, bool) {
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		return exit.ExitStatus(), true
	}
	return 0, false
}

func dial(ctx context.Context, params DialParams) (*ssh.Client, func(), error) {
	if ctx == nil {
		return nil, nil, fmt.Errorf("%w: context is required", domain.ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(params.IP) == "" || params.Port < 1 || params.Port > 65535 || strings.TrimSpace(params.Username) == "" {
		return nil, nil, fmt.Errorf("%w: SSH host, port, and username are required", domain.ErrInvalidInput)
	}

	auth, err := authMethods(params)
	if err != nil {
		return nil, nil, err
	}
	addr := net.JoinHostPort(strings.TrimSpace(params.IP), strconv.Itoa(params.Port))
	dialContext := params.Dialer
	if dialContext == nil {
		dialContext = netpolicy.Default().DialContext
	}
	netConn, err := dialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, classifyNetworkError(ctx, err)
	}

	stopCancellation := context.AfterFunc(ctx, func() { _ = netConn.Close() })
	cleanupConn := func() {
		stopCancellation()
		_ = netConn.Close()
	}
	deadline := time.Now().Add(handshakeTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}

	if err := netConn.SetDeadline(deadline); err != nil {
		cleanupConn()
		return nil, nil, fmt.Errorf("set SSH handshake deadline: %w", err)
	}

	clientConfig := &ssh.ClientConfig{
		User:            params.Username,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback(params),
		Timeout:         handshakeTimeout,
	}
	sshConn, channels, requests, err := ssh.NewClientConn(netConn, addr, clientConfig)
	if err != nil {
		cleanupConn()
		var unknownKey *domain.ErrHostKeyUnknown
		if errors.As(err, &unknownKey) {
			return nil, nil, unknownKey
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, nil, domain.ErrAuthenticationFailed
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		// The connection deadline is the context deadline, so a read timeout can
		// surface a moment before the context reports it; report it as the
		// deadline it is rather than as an opaque negotiation failure.
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
				return nil, nil, context.DeadlineExceeded
			}
			return nil, nil, domain.ErrConnectionTimeout
		}
		return nil, nil, fmt.Errorf("negotiate SSH connection to %s: %w", addr, err)
	}
	// The handshake deadline must not constrain the lifetime of the session.
	if err := netConn.SetDeadline(time.Time{}); err != nil {
		_ = sshConn.Close()
		cleanupConn()
		return nil, nil, fmt.Errorf("clear SSH handshake deadline: %w", err)
	}

	client := ssh.NewClient(sshConn, channels, requests)
	cleanup := func() {
		_ = client.Close()
		cleanupConn()
	}
	return client, cleanup, nil
}

func authMethods(params DialParams) ([]ssh.AuthMethod, error) {
	switch params.AuthMethod {
	case AuthPassword:
		if params.SecretData == "" {
			return nil, fmt.Errorf("%w: SSH password is required", domain.ErrInvalidInput)
		}
		return []ssh.AuthMethod{ssh.Password(params.SecretData)}, nil
	case AuthPrivateKey:
		if params.SecretData == "" {
			return nil, fmt.Errorf("%w: SSH private key is required", domain.ErrInvalidInput)
		}
		var signer ssh.Signer
		var err error
		if params.Passphrase == "" {
			signer, err = ssh.ParsePrivateKey([]byte(params.SecretData))
		} else {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(params.SecretData), []byte(params.Passphrase))
		}
		if err != nil {
			return nil, fmt.Errorf("%w: parse SSH private key: %w", domain.ErrInvalidInput, err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported SSH authentication method %q", domain.ErrInvalidInput, params.AuthMethod)
	}
}

func classifyNetworkError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, domain.ErrTargetNotAllowed) {
		return err
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return domain.ErrConnectionRefused
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return domain.ErrConnectionTimeout
	}
	return fmt.Errorf("dial SSH target: %w", err)
}

type limitedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buffer.Write(p[:remaining])
			b.truncated = true
		} else {
			_, _ = b.buffer.Write(p)
		}
	} else if len(p) != 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := b.buffer.String()
	if b.truncated {
		result += "\n[output truncated]"
	}
	return result
}

func (b *limitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}
