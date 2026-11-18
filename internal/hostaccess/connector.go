// Package hostaccess adapts connector-neutral managed Host resources to the
// existing Go-native SSH client.
package hostaccess

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
	"github.com/machinarchy/machinarchy/pkg/sshclient"
)

var (
	ErrUnavailable = errors.New("host access configuration unavailable")
	ErrCapacity    = errors.New("host connector capacity unavailable")
	ErrConnection  = errors.New("ssh connection failed")
	ErrHostKey     = errors.New("ssh host key verification failed")
	ErrAuth        = errors.New("ssh authentication failed")
	ErrTimeout     = errors.New("ssh operation timed out")
	ErrRefused     = errors.New("ssh connection refused")
)

// HostConnector contains only the capabilities needed by reconciliation and
// imperative Operations. Implementations can later use an Agent transport.
type HostConnector interface {
	Verify(context.Context, *resource.Host, *resource.AccessProfileRevision) error
	Execute(context.Context, *resource.Host, *resource.AccessProfileRevision, string, []string) (Result, error)
}

type Result struct {
	Stdout    string
	Stderr    string
	Truncated bool
	ExitCode  int
}

type CredentialResolver interface {
	Resolve(context.Context, secrets.ResolutionRequest) ([]byte, error)
}

type SSHConnector struct {
	resolver CredentialResolver
	policy   func() netpolicy.Policy
	slots    chan struct{}
	timeout  time.Duration
	workload secrets.WorkloadID
}

// NewSSHConnector builds a connector with a fixed network policy. Production
// uses NewSSHConnectorWithPolicyProvider so policy follows runtime
// configuration; this form serves tests and fixed deployments.
func NewSSHConnector(resolver CredentialResolver, policy netpolicy.Policy, maxConcurrent int, timeout time.Duration, workload secrets.WorkloadID) (*SSHConnector, error) {
	if resolver == nil || maxConcurrent < 1 || maxConcurrent > 1024 || timeout <= 0 ||
		(workload != secrets.WorkloadControlManager && workload != secrets.WorkloadOperationWorker) {
		return nil, errors.New("SSH connector requires a resolver, bounded capacity, timeout, and workload identity")
	}
	return NewSSHConnectorWithPolicyProvider(resolver, func() netpolicy.Policy { return policy }, maxConcurrent, timeout, workload)
}

func NewSSHConnectorWithPolicyProvider(resolver CredentialResolver, policy func() netpolicy.Policy, maxConcurrent int, timeout time.Duration, workload secrets.WorkloadID) (*SSHConnector, error) {
	if resolver == nil || policy == nil || maxConcurrent < 1 || maxConcurrent > 1024 || timeout <= 0 ||
		(workload != secrets.WorkloadControlManager && workload != secrets.WorkloadOperationWorker) {
		return nil, errors.New("SSH connector requires a resolver, policy provider, bounded capacity, timeout, and workload identity")
	}
	return &SSHConnector{resolver: resolver, policy: policy, slots: make(chan struct{}, maxConcurrent), timeout: timeout, workload: workload}, nil
}

func (c *SSHConnector) Verify(ctx context.Context, host *resource.Host, profile *resource.AccessProfileRevision) error {
	params, cleanup, err := c.params(ctx, host, profile, "")
	if err != nil {
		return err
	}
	defer cleanup()
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.acquire(callCtx); err != nil {
		return err
	}
	defer c.release()
	if err := sshclient.TestConnection(callCtx, params); err != nil {
		return classify(err)
	}
	return nil
}

func (c *SSHConnector) Execute(ctx context.Context, host *resource.Host, profile *resource.AccessProfileRevision, operationID string, argv []string) (Result, error) {
	if err := domain.ValidateArgv(argv); err != nil {
		return Result{}, err
	}
	params, cleanup, err := c.params(ctx, host, profile, operationID)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.acquire(callCtx); err != nil {
		return Result{}, err
	}
	defer c.release()
	raw, runErr := sshclient.ExecuteRemoteCommandDetailed(callCtx, params, domain.QuoteArgv(argv))
	result := Result{Stdout: raw.Stdout, Stderr: raw.Stderr, Truncated: raw.Truncated}
	if runErr != nil {
		if code, ok := sshclient.ExitStatus(runErr); ok {
			result.ExitCode = code
			return result, nil
		}
		return result, classify(runErr)
	}
	return result, nil
}

// RunRunner runs a command built by the runner package and returns its
// standard output. A non-zero exit is not an error here: the runner reports
// refusals in its output, and the runner parsers turn them into errors.
// Only connection-level failures are returned, classified like Execute's.
func (c *SSHConnector) RunRunner(ctx context.Context, host *resource.Host, profile *resource.AccessProfileRevision, operationID, command string) (string, error) {
	params, cleanup, err := c.params(ctx, host, profile, operationID)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if err := c.acquire(ctx); err != nil {
		return "", err
	}
	defer c.release()
	raw, runErr := sshclient.ExecuteRemoteCommandDetailed(ctx, params, command)
	if runErr != nil {
		if _, ok := sshclient.ExitStatus(runErr); ok {
			return raw.Stdout, nil
		}
		return "", classify(runErr)
	}
	if raw.Truncated {
		return "", fmt.Errorf("%w: runner reply exceeded the output cap", ErrConnection)
	}
	return raw.Stdout, nil
}

func (c *SSHConnector) params(ctx context.Context, host *resource.Host, revision *resource.AccessProfileRevision, operationID string) (sshclient.DialParams, func(), error) {
	if c == nil || host == nil || revision == nil || ctx == nil || !revision.Spec.Enabled ||
		host.Spec.Presence != resource.PresenceManaged || revision.WorkspaceID != host.Metadata.WorkspaceID ||
		host.Spec.AccessProfileID != revision.ProfileID || revision.Spec.Connector != resource.ConnectorSSH {
		return sshclient.DialParams{}, func() {}, ErrUnavailable
	}
	address, port, err := endpointOf(host)
	if err != nil {
		return sshclient.DialParams{}, func() {}, err
	}
	authorities, err := sshclient.ParseHostCertificateAuthorities(revision.Spec.HostCertificateAuthorities)
	if err != nil {
		return sshclient.DialParams{}, func() {}, ErrUnavailable
	}
	if len(host.Spec.AccessTarget.Trust) == 0 && len(authorities) == 0 {
		// Nothing to verify the host against: never authenticate blindly.
		return sshclient.DialParams{}, func() {}, ErrHostKey
	}
	policy := c.policy()
	if err := policy.CheckHost(address); err != nil {
		return sshclient.DialParams{}, func() {}, domain.ErrTargetNotAllowed
	}
	request := secrets.ResolutionRequest{WorkloadID: c.workload, Ref: secrets.Ref{ID: revision.Spec.Credential.ID, Version: revision.Spec.Credential.Version},
		Purpose: secrets.PurposeSSHCredential, WorkspaceID: host.Metadata.WorkspaceID, AccessProfileID: string(revision.ProfileID),
		AccessProfileRevision: revision.Revision, HostID: string(host.Metadata.ID), OperationID: operationID}
	credential, err := c.resolver.Resolve(ctx, request)
	if err != nil {
		return sshclient.DialParams{}, func() {}, ErrUnavailable
	}
	method := sshclient.AuthPassword
	if revision.Spec.Authentication == resource.AuthenticationPrivateKey {
		method = sshclient.AuthPrivateKey
	} else if revision.Spec.Authentication != resource.AuthenticationPassword {
		wipe(credential)
		return sshclient.DialParams{}, func() {}, ErrUnavailable
	}
	params := sshclient.DialParams{IP: address, Port: port, Username: revision.Spec.Username,
		AuthMethod: method, SecretData: string(credential), TrustedFingerprints: host.Spec.AccessTarget.Trust,
		HostCertAuthorities: authorities, Dialer: policy.DialContext}
	return params, func() { wipe(credential); params.SecretData = "" }, nil
}

func (c *SSHConnector) acquire(ctx context.Context) error {
	select {
	case c.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ErrCapacity
		}
		return ctx.Err()
	}
}
func (c *SSHConnector) release() { <-c.slots }

func classify(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	var unknownHostKey *domain.ErrHostKeyUnknown
	if errors.As(err, &unknownHostKey) {
		return ErrHostKey
	}
	if errors.Is(err, domain.ErrAuthenticationFailed) {
		return ErrAuth
	}
	if errors.Is(err, domain.ErrTargetNotAllowed) {
		return domain.ErrTargetNotAllowed
	}
	if errors.Is(err, domain.ErrConnectionRefused) {
		return ErrRefused
	}
	return ErrConnection
}

func wipe(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func endpointOf(host *resource.Host) (string, int, error) {
	address, portText, err := net.SplitHostPort(strings.TrimSpace(host.Spec.AccessTarget.Endpoint))
	if err != nil || strings.TrimSpace(address) == "" {
		return "", 0, ErrUnavailable
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, ErrUnavailable
	}
	return address, port, nil
}

// HostKeyScanner reads a host's SSH host keys without authenticating.
type HostKeyScanner interface {
	ScanHostKeys(context.Context, *resource.Host) ([]resource.ObservedHostKey, error)
}

// ScanHostKeys returns the host's keys as seen now. It resolves no
// credential and stops before authentication; the egress policy and the
// connector's concurrency limit apply.
func (c *SSHConnector) ScanHostKeys(ctx context.Context, host *resource.Host) ([]resource.ObservedHostKey, error) {
	if c == nil || ctx == nil || host == nil {
		return nil, ErrUnavailable
	}
	address, port, err := endpointOf(host)
	if err != nil {
		return nil, err
	}
	policy := c.policy()
	if err := policy.CheckHost(address); err != nil {
		return nil, domain.ErrTargetNotAllowed
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.acquire(callCtx); err != nil {
		return nil, err
	}
	defer c.release()
	keys, err := sshclient.ScanHostKeys(callCtx, sshclient.DialParams{IP: address, Port: port, Dialer: policy.DialContext})
	if err != nil {
		return nil, classify(err)
	}
	now := time.Now().UTC()
	observed := make([]resource.ObservedHostKey, 0, len(keys))
	for _, key := range keys {
		observed = append(observed, resource.ObservedHostKey{Algorithm: key.Type(), Fingerprint: ssh.FingerprintSHA256(key), FirstSeenAt: now, LastSeenAt: now})
	}
	return observed, nil
}
