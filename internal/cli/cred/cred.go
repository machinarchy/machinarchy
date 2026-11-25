// Package cred stores the credentials the CLI uses to talk to servers
// (docs/cli/spec/05-architecture.md section 4).
//
// Each saved server can hold two credentials: a login session (from a
// password) and an API token (a device token created by "stay signed in", or a
// token the person supplied). They are kept apart because they can do
// different things: only a session can act as the owner.
//
// A Credential never prints its secret: formatting it with %v, %+v, %#v or %s
// shows "***", so a log line or an error that includes one cannot leak it.
// Credentials are stored in a file readable by the owner only; a file that
// other users can read is refused, not tolerated.
package cred

import (
	"fmt"
	"time"
)

// Kind says where a credential came from and what it can do.
type Kind string

// Credential kinds.
const (
	// KindSession is a login session from an email and password. It can do
	// owner actions while it lasts.
	KindSession Kind = "session"
	// KindDeviceToken is an operator API token created by "stay signed in".
	KindDeviceToken Kind = "device-token"
	// KindToken is an API token the person supplied (login --with-token).
	KindToken Kind = "token"
)

// Slot is one of the two places a server's credentials are kept.
type Slot string

// Slots.
const (
	SlotSession Slot = "session"
	SlotToken   Slot = "token"
)

// SlotOf returns the slot a kind is stored in.
func SlotOf(kind Kind) Slot {
	if kind == KindSession {
		return SlotSession
	}

	return SlotToken
}

// Credential is one saved secret and what is known about it.
type Credential struct {
	Kind   Kind   `yaml:"kind"`
	Secret string `yaml:"secret"`
	// User is the account name, for display.
	User string `yaml:"user,omitempty"`
	// Name is the token's name, when it has one.
	Name string `yaml:"name,omitempty"`
	// Role is the role, when the server reported it.
	Role string `yaml:"role,omitempty"`
	// ExpiresAt is when the credential stops working, when it is known.
	ExpiresAt *time.Time `yaml:"expires-at,omitempty"`
	// TokenID is the server's ID of an API token, so it can be revoked.
	TokenID int `yaml:"token-id,omitempty"`
}

// redacted is what a Credential prints.
const redacted = "***"

// String returns "***", never the secret.
func (c Credential) String() string { return redacted }

// GoString returns "***", never the secret, for %#v.
func (c Credential) GoString() string { return redacted }

// Format makes every fmt verb print "***".
func (c Credential) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, redacted) }

// Expired reports whether the credential is known to have expired at now.
func (c Credential) Expired(now time.Time) bool {
	return c.ExpiresAt != nil && !c.ExpiresAt.After(now)
}

// Entry holds the credentials saved for one server.
type Entry struct {
	Session *Credential `yaml:"session,omitempty"`
	Token   *Credential `yaml:"token,omitempty"`
}

// Empty reports whether the entry holds no credential.
func (e Entry) Empty() bool { return e.Session == nil && e.Token == nil }

// Store keeps credentials per server name.
type Store interface {
	// Get returns the credentials saved for server; an empty Entry when there
	// are none.
	Get(server string) (Entry, error)
	// Put saves a credential in the slot of its kind and keeps the other.
	Put(server string, c Credential) error
	// Delete removes the given slots, or both when none is given.
	Delete(server string, slots ...Slot) error
	// Where says where credentials are kept, for `config show`.
	Where() string
}
