// Package sessionkey signs and verifies login-session JWTs with a keyring
// (AUDIT S-08). Tokens carry a key ID ("kid") in their header. New tokens use
// the current key; verification accepts the current key and any previous one,
// so rotating the signing secret does not log everyone out at once.
package sessionkey

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/machinarchy/machinarchy/internal/domain"
)

// minSecretBytes matches MACHINARCHY_OPERATOR_TOKEN's minimum length.
const minSecretBytes = 32

// Keyring holds the signing key and the keys still accepted.
type Keyring struct {
	currentID string
	keys      map[string][]byte
}

// KeyID derives a key's public identifier from its secret. It does not reveal
// the secret.
func KeyID(secret string) string {
	sum := sha256.Sum256([]byte("machinarchy/session-key-id/" + secret))
	return hex.EncodeToString(sum[:8])
}

// New builds a keyring that signs with current and also verifies previous.
func New(current string, previous []string) (*Keyring, error) {
	k := &Keyring{keys: map[string][]byte{}}
	add := func(secret string) error {
		if len(secret) < minSecretBytes || strings.ContainsAny(secret, " \t\r\n") {
			return fmt.Errorf("session signing keys must be at least %d bytes without whitespace", minSecretBytes)
		}
		k.keys[KeyID(secret)] = []byte(secret)
		return nil
	}
	if err := add(current); err != nil {
		return nil, err
	}
	k.currentID = KeyID(current)
	for _, secret := range previous {
		if secret = strings.TrimSpace(secret); secret == "" {
			continue
		}
		if err := add(secret); err != nil {
			return nil, fmt.Errorf("previous session key: %w", err)
		}
	}
	return k, nil
}

// CurrentKeyID is the kid of new tokens.
func (k *Keyring) CurrentKeyID() string { return k.currentID }

// Sign returns an HS256 token for claims, with the current kid.
func (k *Keyring) Sign(claims jwt.Claims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["kid"] = k.currentID
	signed, err := token.SignedString(k.keys[k.currentID])
	if err != nil {
		return "", fmt.Errorf("sign session token: %w", err)
	}
	return signed, nil
}

// ErrUnknownKey reports a token without a kid, or signed with a key the
// keyring no longer holds.
var ErrUnknownKey = errors.New("session token signed with an unknown key")

// Keyfunc resolves a token's verification key by its kid. Pass it to a jwt
// parser restricted to HS256.
func (k *Keyring) Keyfunc(token *jwt.Token) (any, error) {
	if token.Method != jwt.SigningMethodHS256 {
		return nil, errors.New("unexpected session signing method")
	}
	kid, _ := token.Header["kid"].(string)
	key, ok := k.keys[kid]
	if !ok {
		return nil, ErrUnknownKey
	}
	return key, nil
}

// ClockSkew is the leeway allowed on token times.
const ClockSkew = time.Minute

// Parse verifies a session token's signature (by kid), issuer and times, and
// that it names a user, a workspace and a session. maxLifetime bounds both
// the token's validity window and its age. It does not check that the
// session is still active: that is the session store's per-request check.
func (k *Keyring) Parse(token string, maxLifetime time.Duration, now time.Time) (*domain.JWTCustomClaims, error) {
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("machinarchy"),
		jwt.WithLeeway(ClockSkew),
		jwt.WithIssuedAt(),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	claims := new(domain.JWTCustomClaims)
	parsed, err := parser.ParseWithClaims(token, claims, k.Keyfunc)
	if err != nil || parsed == nil || !parsed.Valid || claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return nil, ErrInvalidToken
	}
	issuedAt := claims.IssuedAt.Time
	if issuedAt.After(now.Add(ClockSkew)) || now.Sub(issuedAt) > maxLifetime || claims.ExpiresAt.Sub(issuedAt) > maxLifetime {
		return nil, ErrInvalidToken
	}
	if claims.UserID <= 0 || claims.WorkspaceID <= 0 || strings.TrimSpace(claims.Username) == "" || !domain.ValidSessionID(claims.SessionID) {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// ErrInvalidToken reports a token that is not a valid session token.
var ErrInvalidToken = errors.New("invalid session token")
