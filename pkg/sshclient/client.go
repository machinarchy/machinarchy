package sshclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

type DialParams struct {
	IP                  string
	Port                int
	Username            string
	AuthMethod          domain.SSHAuthMethod
	SecretData          string
	Passphrase          string
	ExpectedFingerprint string
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
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("%w: command is required", domain.ErrInvalidInput)
	}

	client, cleanup, err := dial(ctx, params)
	if err != nil {
		return "", err
	}
	defer cleanup()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("create SSH session: %w", err)
	}
	defer session.Close()

	output := &limitedBuffer{limit: maxCommandOutputBytes}
	session.Stdout = output
	session.Stderr = output
	err = session.Run(command)
	return output.String(), err
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
	if deadline, ok := ctx.Deadline(); ok {
		_ = netConn.SetDeadline(deadline)
	} else {
		_ = netConn.SetDeadline(time.Now().Add(defaultHandshakeTimeout))
	}

	clientConfig := &ssh.ClientConfig{
		User:            params.Username,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback(params.ExpectedFingerprint),
		Timeout:         defaultHandshakeTimeout,
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
	case domain.AuthTypePassword:
		if params.SecretData == "" {
			return nil, fmt.Errorf("%w: SSH password is required", domain.ErrInvalidInput)
		}
		return []ssh.AuthMethod{ssh.Password(params.SecretData)}, nil
	case domain.AuthTypePrivateKey:
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
			return nil, fmt.Errorf("%w: parse SSH private key: %v", domain.ErrInvalidInput, err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported SSH authentication method %q", domain.ErrInvalidInput, params.AuthMethod)
	}
}

func hostKeyCallback(expectedFingerprint string) ssh.HostKeyCallback {
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint := ssh.FingerprintSHA256(key)
		if expectedFingerprint == "" || fingerprint != expectedFingerprint {
			return &domain.ErrHostKeyUnknown{
				Host:        hostname,
				Algorithm:   key.Type(),
				Fingerprint: fingerprint,
			}
		}
		return nil
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
