package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultWorkspaceID ensures single-tenant operations work automatically
	// while keeping multi-tenant database structures intact for future phases.
	DefaultWorkspaceID = 1
)

// Security Architecture Custom Domain Errors
var (
	ErrInvalidInput          = errors.New("host validation constraint failed")
	ErrAuthenticationFailed  = errors.New("ssh authentication rejected: invalid credentials")
	ErrConnectionTimeout     = errors.New("network timeout reached while dialing target server")
	ErrConnectionRefused     = errors.New("target machine actively refused the network connection")
	ErrSessionCreationFailed = errors.New("failed to initialize interactive payload session channel")
	ErrServiceBusy           = errors.New("service is at capacity")
	ErrTargetNotAllowed      = errors.New("target address is not permitted by the network policy")

	ErrHostNotFound       = errors.New("requested host resource could not be found")
	ErrConcurrentConflict = errors.New("concurrent state modification conflict")
)

// ErrHostKeyUnknown carries calculated hashes back up to handlers without global blind bypasses
type ErrHostKeyUnknown struct {
	Host        string
	Algorithm   string
	Fingerprint string
}

func (e *ErrHostKeyUnknown) Error() string {
	return fmt.Sprintf("untrusted host key matching %s presented from %s [Fingerprint: %s]", e.Algorithm, e.Host, e.Fingerprint)
}

type HostStatus string

const (
	HostStatusPending      HostStatus = "PENDING"
	HostStatusActive       HostStatus = "ACTIVE"
	HostStatusSubjugated   HostStatus = "SUBJUGATED"
	HostStatusDisconnected HostStatus = "DISCONNECTED"
)

// connection method
type ConnectionMethod string

const (
	MethodSSH       ConnectionMethod = "SSH"
	MethodAgentPull ConnectionMethod = "AGENT_PULL"
	MethodAgentPush ConnectionMethod = "AGENT_PUSH"
)

type Host struct {
	ID            string            `json:"id"`
	WorkspaceID   int               `json:"-"`
	EnvironmentID int               `json:"environmentId"`
	Name          string            `json:"name"`
	Hostname      string            `json:"hostname"`
	IPAddress     string            `json:"ip_address"`
	Connection    ConnectionMethod  `json:"connection"`
	Labels        map[string]string `json:"labels"`
	SSH           *SSHAccess        `json:"ssh,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

type SSHAccess struct {
	Username string `json:"username"`
	Port     int    `json:"port"`
	// AuthType contains credentials and must never be serialized in API responses.
	AuthType         SSHAuthType `json:"-"`
	Status           HostStatus  `json:"status"`
	LastCheckAt      *time.Time  `json:"last_check_at,omitempty"`
	LastCheckMS      *int64      `json:"last_check_duration_ms,omitempty"`
	LastCheckMessage string      `json:"last_check_message,omitempty"`
}

type SSHAuthMethod string

const (
	AuthTypePassword   SSHAuthMethod = "PASSWORD"
	AuthTypePrivateKey SSHAuthMethod = "PRIVATE_KEY"
)

func ParseAuthType(value string) (SSHAuthMethod, error) {
	switch SSHAuthMethod(strings.ToUpper(strings.TrimSpace(value))) {
	case AuthTypePassword:
		return AuthTypePassword, nil
	case AuthTypePrivateKey:
		return AuthTypePrivateKey, nil
	default:
		return "", fmt.Errorf("%w: unsupported SSH authentication method %q", ErrInvalidInput, value)
	}
}

type SSHAuthType struct {
	Method              SSHAuthMethod `json:"-"`
	Password            string        `json:"-"`
	PrivateKey          string        `json:"-"`
	Passphrase          string        `json:"-"`
	ExpectedFingerprint string        `json:"-"`
}

func (h *Host) SSHValidate() error {
	if h == nil {
		return fmt.Errorf("%w: host is required", ErrInvalidInput)
	}

	// Guard against uninitialized SSH configurations
	if h.SSH == nil {
		h.SSH = &SSHAccess{}
	}

	// Clean trailing spaces from network identifiers
	h.Name = strings.TrimSpace(h.Name)
	h.IPAddress = strings.TrimSpace(h.IPAddress)
	h.Hostname = strings.TrimSpace(h.Hostname)
	h.SSH.Username = strings.TrimSpace(h.SSH.Username)

	// Validate mandatory operational requirements
	if h.Name == "" || h.IPAddress == "" || h.SSH.Username == "" {
		return fmt.Errorf("%w: host name, network target, and SSH username are required", ErrInvalidInput)
	}

	// Dynamic network port validation layer
	if h.SSH.Port == 0 {
		h.SSH.Port = 22 // Default structural fallback context
	} else if h.SSH.Port < 1 || h.SSH.Port > 65535 {
		return fmt.Errorf("%w: network configuration port %d resides outside the valid TCP legal range (1-65535)", ErrInvalidInput, h.SSH.Port)
	}

	// Auto-initialize fallback state if unset
	if h.Connection == "" {
		h.Connection = MethodSSH
	}
	if h.Connection != MethodSSH {
		return fmt.Errorf("%w: connection method %q is not supported by this service", ErrInvalidInput, h.Connection)
	}
	if h.SSH.Status == "" {
		h.SSH.Status = HostStatusPending
	}
	if h.SSH.AuthType.Method == "" {
		if h.SSH.AuthType.PrivateKey != "" {
			h.SSH.AuthType.Method = AuthTypePrivateKey
		} else if h.SSH.AuthType.Password != "" {
			h.SSH.AuthType.Method = AuthTypePassword
		}
	}
	switch h.SSH.AuthType.Method {
	case AuthTypePassword:
		if h.SSH.AuthType.Password == "" {
			return fmt.Errorf("%w: SSH password is required", ErrInvalidInput)
		}
	case AuthTypePrivateKey:
		if h.SSH.AuthType.PrivateKey == "" {
			return fmt.Errorf("%w: SSH private key is required", ErrInvalidInput)
		}
	default:
		return fmt.Errorf("%w: unsupported SSH authentication method %q", ErrInvalidInput, h.SSH.AuthType.Method)
	}

	return nil
}

// HostResultStatus is the outcome of one record in a bulk host operation.
type HostResultStatus string

const (
	// StatusSuccess means the host passed SSH verification and was persisted.
	StatusSuccess HostResultStatus = "success"
	// StatusValid means a validate-only run accepted the record; nothing was
	// contacted or stored.
	StatusValid HostResultStatus = "valid"
	// StatusFailed means the record was rejected or could not be verified.
	StatusFailed HostResultStatus = "failed"
	// StatusDuplicate means the name or address:port already exists in the
	// inventory or earlier in the same batch.
	StatusDuplicate HostResultStatus = "duplicate"
)

// HostOperationResult reports one record of a bulk operation. Index is the
// zero-based position of the record in the request.
type HostOperationResult struct {
	Index    int              `json:"index"`
	Name     string           `json:"name"`
	Hostname string           `json:"hostname"`
	Address  string           `json:"address"`
	Status   HostResultStatus `json:"status"`
	ID       string           `json:"id,omitempty"`
	Error    string           `json:"error,omitempty"`
}

// BulkHostEntry is one candidate record. PreError carries a failure found
// while decoding or pre-validating the record so it is still reported at its
// position instead of failing the whole request.
type BulkHostEntry struct {
	Host     *Host
	PreError error
}

// BulkImportResponse aggregates per-record results. Results is always in
// request order and has one element per submitted record.
type BulkImportResponse struct {
	ValidateOnly   bool                  `json:"validate_only"`
	TotalProcessed int                   `json:"total_processed"`
	SuccessCount   int                   `json:"success_count"`
	ValidCount     int                   `json:"valid_count"`
	FailureCount   int                   `json:"failure_count"`
	DuplicateCount int                   `json:"duplicate_count"`
	Results        []HostOperationResult `json:"results"`
}

// NewBulkImportResponse derives the counters from results so they can never
// disagree with the per-record outcomes.
func NewBulkImportResponse(validateOnly bool, results []HostOperationResult) *BulkImportResponse {
	response := &BulkImportResponse{
		ValidateOnly:   validateOnly,
		TotalProcessed: len(results),
		Results:        results,
	}
	for _, result := range results {
		switch result.Status {
		case StatusSuccess:
			response.SuccessCount++
		case StatusValid:
			response.ValidCount++
		case StatusDuplicate:
			response.DuplicateCount++
		default:
			response.FailureCount++
		}
	}
	return response
}

// HostStore defines the persistence contract required by Machinarchy.
type HostStore interface {
	CreateHost(ctx context.Context, host *Host) error
	GetHostByID(ctx context.Context, id int) (*Host, error)
	GetHostByIDInWorkspace(ctx context.Context, workspaceID, id int) (*Host, error)
	ListAllHosts(ctx context.Context, workspaceID int) ([]*Host, error)
	UpdateHostStatus(ctx context.Context, id string, status HostStatus) error
	UpdateHostStatusWithGuard(ctx context.Context, id int, expectedCurrent, newStatus HostStatus) error
	DeleteHost(ctx context.Context, workspaceID int, id string) error
	UpdateHost(ctx context.Context, host *Host) error
}
