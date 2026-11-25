package agentgateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// ErrEnrollmentRejected reports a registration whose token is unknown,
// already used, expired, revoked, or bound to another hostname.
var ErrEnrollmentRejected = errors.New("enrollment token is not valid for this registration")

// Enrollment says how a registration is authorised (AUDIT M-4).
type Enrollment struct {
	// Shared is true when the development-only shared token matched.
	Shared bool
	// TokenHash identifies a per-host token: HashEnrollmentToken(secret).
	TokenHash string
	// Hostname is the registering agent's hostname, checked against tokens
	// bound to a hostname.
	Hostname string
}

// EnrollmentToken is an owner-issued, single-use enrolment token. Only the
// hash of the secret is stored.
type EnrollmentToken struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname,omitempty"`
	// HostID is the managed host the enrolling agent is bound to (ADR-057).
	HostID      string     `json:"hostId,omitempty"`
	CreatedBy   int        `json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	UsedAt      *time.Time `json:"usedAt,omitempty"`
	UsedByAgent string     `json:"usedByAgent,omitempty"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
	Hash        string     `json:"-"`
}

// Usable reports whether the token can still enrol a host at now.
func (t EnrollmentToken) Usable(now time.Time) bool {
	return t.UsedAt == nil && t.RevokedAt == nil && now.Before(t.ExpiresAt)
}

// Errors returned by TokenStore implementations.
var ErrEnrollmentTokenNotFound = errors.New("enrollment token not found")

// TokenStore persists enrolment tokens. Tokens are consumed by
// Store.RegisterAgent, in the same transaction as the registration.
type TokenStore interface {
	CreateEnrollmentToken(ctx context.Context, token EnrollmentToken) error
	ListEnrollmentTokens(ctx context.Context) ([]EnrollmentToken, error)
	// RevokeEnrollmentToken revokes an unused token; a used or unknown one
	// gives ErrEnrollmentTokenNotFound.
	RevokeEnrollmentToken(ctx context.Context, id string, actorID int, now time.Time) error
}

// Enrolment token lifetimes.
const (
	DefaultEnrollmentTokenTTL = time.Hour
	MinEnrollmentTokenTTL     = time.Minute
	MaxEnrollmentTokenTTL     = 24 * time.Hour
	enrollmentTokenPrefix     = "mae_"
)

// NewEnrollmentToken creates a token record and its secret. The secret is
// shown once; only its hash is stored.
func NewEnrollmentToken(hostname string, ttl time.Duration, createdBy int, now time.Time) (string, EnrollmentToken, error) {
	if ttl == 0 {
		ttl = DefaultEnrollmentTokenTTL
	}
	if ttl < MinEnrollmentTokenTTL || ttl > MaxEnrollmentTokenTTL {
		return "", EnrollmentToken{}, fmt.Errorf("token lifetime must be between %s and %s", MinEnrollmentTokenTTL, MaxEnrollmentTokenTTL)
	}
	hostname = NormalizeHostname(hostname)
	if hostname != "" && !validHostname(hostname) {
		return "", EnrollmentToken{}, errors.New("hostname must be a DNS name of letters, digits, hyphens and dots (at most 253 characters)")
	}
	if createdBy <= 0 {
		return "", EnrollmentToken{}, errors.New("an enrolment token needs the issuing user")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", EnrollmentToken{}, err
	}
	secret := enrollmentTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	if len(secret) < agentv1.MinEnrollmentToken || len(secret) > agentv1.MaxEnrollmentToken {
		return "", EnrollmentToken{}, errors.New("generated token does not fit the protocol bounds")
	}
	id, err := randomID("enr-")
	if err != nil {
		return "", EnrollmentToken{}, err
	}
	now = now.UTC()
	return secret, EnrollmentToken{ID: id, Hostname: hostname, CreatedBy: createdBy, CreatedAt: now,
		ExpiresAt: now.Add(ttl), Hash: HashEnrollmentToken(secret)}, nil
}

// HashEnrollmentToken is the stored lookup key of a token secret. The secret
// carries 256 random bits, so a plain SHA-256 is enough.
func HashEnrollmentToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// NormalizeHostname lower-cases a hostname and drops a trailing dot, so a
// token bound to "Web-01.example." matches an agent reporting "web-01.example".
func NormalizeHostname(hostname string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
}

func validHostname(hostname string) bool {
	if len(hostname) > 253 {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

// Matches reports whether a token bound to t.Hostname accepts hostname.
func (t EnrollmentToken) Matches(hostname string) bool {
	return t.Hostname == "" || t.Hostname == NormalizeHostname(hostname)
}
