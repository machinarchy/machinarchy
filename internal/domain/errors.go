package domain

import (
	"errors"
	"fmt"
)

// DefaultWorkspaceID is the single shared workspace (ADR-0003). The schema
// keeps workspace_id columns so multi-tenancy stays possible later.
const DefaultWorkspaceID = 1

// Errors shared across services and adapters.
var (
	// ErrInvalidInput marks a request the caller must fix. Messages wrapping
	// it are safe to show to users.
	ErrInvalidInput         = errors.New("invalid input")
	ErrAuthenticationFailed = errors.New("ssh authentication rejected: invalid credentials")
	ErrConnectionTimeout    = errors.New("network timeout reached while dialing target server")
	ErrConnectionRefused    = errors.New("target machine actively refused the network connection")
	ErrServiceBusy          = errors.New("service is at capacity")
	ErrTargetNotAllowed     = errors.New("target address is not permitted by the network policy")
)

// ErrHostKeyUnknown reports a host key that is not in the trusted list,
// with the fingerprint an operator can verify and pin.
type ErrHostKeyUnknown struct {
	Host        string
	Algorithm   string
	Fingerprint string
}

func (e *ErrHostKeyUnknown) Error() string {
	return fmt.Sprintf("untrusted host key matching %s presented from %s [Fingerprint: %s]", e.Algorithm, e.Host, e.Fingerprint)
}
