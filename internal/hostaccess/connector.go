// Package hostaccess adapts connector-neutral managed Host resources to the
// existing Go-native SSH client.
package hostaccess

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

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
	raw, runErr := sshclient.ExecuteRemoteCommandDetailed(callCtx, params, shellQuote(argv))
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

func (c *SSHConnector) params(ctx context.Context, host *resource.Host, revision *resource.AccessProfileRevision, operationID string) (sshclient.DialParams, func(), error) {
	if c == nil || host == nil || revision == nil || ctx == nil || !revision.Spec.Enabled ||
		host.Spec.Presence != resource.PresenceManaged || revision.WorkspaceID != host.Metadata.WorkspaceID ||
		host.Spec.AccessProfileID != revision.ProfileID || revision.Spec.Connector != resource.ConnectorSSH {
		return sshclient.DialParams{}, func() {}, ErrUnavailable
	}
	endpoint := strings.TrimSpace(host.Spec.AccessTarget.Endpoint)
	address, portText, err := net.SplitHostPort(endpoint)
	if err != nil || strings.TrimSpace(address) == "" || len(host.Spec.AccessTarget.Trust) != 1 {
		return sshclient.DialParams{}, func() {}, ErrUnavailable
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return sshclient.DialParams{}, func() {}, ErrUnavailable
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
	method := domain.AuthTypePassword
	if revision.Spec.Authentication == resource.AuthenticationPrivateKey {
		method = domain.AuthTypePrivateKey
	} else if revision.Spec.Authentication != resource.AuthenticationPassword {
		wipe(credential)
		return sshclient.DialParams{}, func() {}, ErrUnavailable
	}
	params := sshclient.DialParams{IP: address, Port: port, Username: revision.Spec.Username,
		AuthMethod: method, SecretData: string(credential), ExpectedFingerprint: host.Spec.AccessTarget.Trust[0], Dialer: policy.DialContext}
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

func shellQuote(argv []string) string {
	quoted := make([]string, len(argv))
	for i, value := range argv {
		quoted[i] = "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}
func wipe(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
