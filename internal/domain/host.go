package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Security Architecture Custom Domain Errors
var (
	ErrInvalidInput          = errors.New("host validation constraint failed")
	ErrAuthenticationFailed  = errors.New("ssh authentication rejected: invalid credentials")
	ErrConnectionTimeout     = errors.New("network timeout reached while dialing target server")
	ErrConnectionRefused     = errors.New("target machine actively refused the network connection")
	ErrSessionCreationFailed = errors.New("failed to initialize interactive payload session channel")
	ErrServiceBusy           = errors.New("service is at capacity")

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

// HostStore defines the persistence contract required by Machinarchy.
type HostStore interface {
	CreateHost(ctx context.Context, host *Host) error
	GetHostByID(ctx context.Context, id int) (*Host, error)
	ListAllHosts(ctx context.Context) ([]*Host, error)
	UpdateHostStatus(ctx context.Context, id string, status HostStatus) error
	UpdateHostStatusWithGuard(ctx context.Context, id int, expectedCurrent, newStatus HostStatus) error
	DeleteHost(ctx context.Context, id string) error
	UpdateHost(ctx context.Context, host *Host) error
}
