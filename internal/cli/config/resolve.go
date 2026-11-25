package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/cred"
)

// Need says what a command asks of the credential it runs with.
type Need int

// Needs.
const (
	// NeedAny: any valid credential will do.
	NeedAny Need = iota
	// NeedOwner: the command acts as the workspace owner, which only a login
	// session can (an API token can never be an owner).
	NeedOwner
)

// Target is the server and credential one command runs with.
type Target struct {
	// ServerName is the saved server's name; empty for an address given for one
	// command.
	ServerName string
	// Server is the API base URL.
	Server string
	// Token is the credential's secret, sent as a bearer token. It is never
	// part of an error message.
	Token string
	// Kind says what the credential is. A token from the environment is
	// KindToken with FromEnv set.
	Kind cred.Kind
	// FromEnv: the credential came from MACHINARCHY_TOKEN and is not saved.
	FromEnv bool
	// CredentialName is the token's name, when it has one.
	CredentialName string
	// Role is the credential's role, when the server reported it.
	Role string
	// Environment is the environment name or ID commands are scoped to.
	Environment    string
	RequestTimeout time.Duration
	// CAFile is a PEM file of extra certificate authorities for this server.
	CAFile string
}

// selectServer works out which server a command talks to: the one named or
// addressed by --server or MACHINARCHY_SERVER, else the active one.
func (s *Store) selectServer() (name string, server Server, adhoc bool, err error) {
	selector := strings.TrimSpace(s.v.GetString("server"))
	if selector == "" {
		if s.doc.Active == "" {
			return "", Server{}, false, clierr.Detailed(clierr.ExitAuth,
				fmt.Errorf("you are not signed in to any server"), "", "machinarchy login YOUR-SERVER-URL")
		}
		active, ok := s.doc.Servers[s.doc.Active]
		if !ok {
			return "", Server{}, false, unknownServer(s.doc.Active, s.ServerNames())
		}

		return s.doc.Active, active, false, nil
	}

	if saved, ok := s.doc.Servers[selector]; ok {
		return selector, saved, false, nil
	}
	if strings.Contains(selector, "://") || strings.ContainsAny(selector, ".:") || strings.EqualFold(selector, "localhost") {
		address, err := NormalizeURL(selector)
		if err != nil {
			return "", Server{}, false, err
		}

		return "", Server{URL: address}, true, nil
	}

	return "", Server{}, false, unknownServer(selector, s.ServerNames())
}

// ResolveServerName returns the name of the server a command would use, or "".
func (s *Store) ResolveServerName() string {
	if s == nil || s.v == nil {
		return ""
	}

	name, _, _, err := s.selectServer()
	if err != nil {
		return ""
	}

	return name
}

// Resolve decides the server and the credential for one command.
//
// The server is the one --server (or MACHINARCHY_SERVER) names, else the
// active one. A saved credential is only ever sent to the server it was saved
// for: an address that is not a saved server needs MACHINARCHY_TOKEN.
//
// The credential is, in order: MACHINARCHY_TOKEN, the saved API token, the
// saved login session. A command that needs an owner uses the saved session
// and nothing else, and says so when it only has a token. A credential that is
// known to have expired is not used.
func (s *Store) Resolve(creds cred.Store, need Need, now time.Time) (Target, error) {
	if s == nil || s.v == nil {
		return Target{}, fmt.Errorf("CLI config is not loaded")
	}

	name, server, adhoc, err := s.selectServer()
	if err != nil {
		return Target{}, err
	}
	if err := ValidateServer(server.URL); err != nil {
		return Target{}, err
	}
	if err := CheckTransport(server.URL, server.InsecureHTTP); err != nil {
		return Target{}, err
	}

	envToken := strings.TrimSpace(s.v.GetString("token"))
	if adhoc && envToken == "" {
		return Target{}, clierr.Detailed(clierr.ExitAuth,
			fmt.Errorf("you have no credential for %s", server.URL),
			"saved credentials are only sent to the server they were saved for",
			"machinarchy login "+server.URL+"\nMACHINARCHY_TOKEN=... machinarchy ... --server "+server.URL)
	}

	var entry cred.Entry
	if !adhoc && creds != nil {
		if entry, err = creds.Get(name); err != nil {
			return Target{}, err
		}
	}

	picked, err := pick(need, envToken, entry, now, serverLabel(name, server.URL), s.loginCommand(name, server.URL, adhoc))
	if err != nil {
		return Target{}, err
	}
	if err := ValidateToken(picked.Token); err != nil {
		return Target{}, err
	}

	timeout, err := s.RequestTimeout()
	if err != nil {
		return Target{}, err
	}

	environment := strings.TrimSpace(s.v.GetString("env"))
	if environment == "" {
		environment = server.Env
	}

	picked.ServerName = name
	picked.Server = strings.TrimRight(server.URL, "/")
	picked.Environment = environment
	picked.RequestTimeout = timeout
	picked.CAFile = s.caPath(server.CAFile)

	return picked, nil
}

// Environment returns the active environment scope, including --env: the flag
// or variable, else the selected server's saved default.
func (s *Store) Environment() string {
	if s == nil || s.v == nil {
		return ""
	}
	if environment := strings.TrimSpace(s.v.GetString("env")); environment != "" {
		return environment
	}
	if _, server, _, err := s.selectServer(); err == nil {
		return server.Env
	}

	return ""
}

// CAPath returns the absolute path of a saved CA file.
func (s *Store) caPath(file string) string {
	if file == "" || filepath.IsAbs(file) || s.path == "" {
		return file
	}

	return filepath.Join(filepath.Dir(s.path), file)
}

// CAPath returns the absolute path of a CA file saved for a server.
func (s *Store) CAPath(file string) string { return s.caPath(file) }

// loginCommand is the command that signs in to the server again.
func (s *Store) loginCommand(name, url string, adhoc bool) string {
	switch {
	case adhoc:
		return "machinarchy login " + url
	case name == s.doc.Active && strings.TrimSpace(s.v.GetString("server")) == "":
		return "machinarchy login"
	default:
		return "machinarchy login " + name
	}
}

func serverLabel(name, url string) string {
	if name == "" {
		return url
	}

	return name
}

// pick chooses the credential. It is pure, so the rules can be tested without
// a file or a server.
func pick(need Need, envToken string, entry cred.Entry, now time.Time, server, login string) (Target, error) {
	session := usable(entry.Session, now)
	token := usable(entry.Token, now)

	if need == NeedOwner {
		if session != nil {
			return target(*session, false), nil
		}

		return Target{}, ownerNeeded(envToken, entry, now, server, login)
	}

	switch {
	case envToken != "":
		return Target{Token: envToken, Kind: cred.KindToken, FromEnv: true}, nil
	case token != nil:
		return target(*token, false), nil
	case session != nil:
		return target(*session, false), nil
	}

	return Target{}, noUsableCredential(entry, now, server, login)
}

// usable returns c unless it is missing or known to have expired.
func usable(c *cred.Credential, now time.Time) *cred.Credential {
	if c == nil || c.Expired(now) {
		return nil
	}

	return c
}

func target(c cred.Credential, fromEnv bool) Target {
	return Target{Token: c.Secret, Kind: c.Kind, FromEnv: fromEnv, CredentialName: c.Name, Role: c.Role}
}

// ownerNeeded is the refusal for a command that acts as the owner when there
// is no usable login session.
func ownerNeeded(envToken string, entry cred.Entry, now time.Time, server, login string) error {
	switch {
	case entry.Session != nil && entry.Session.Expired(now):
		return clierr.Detailed(clierr.ExitAuth,
			fmt.Errorf("your session on %s expired at %s", server, entry.Session.ExpiresAt.Local().Format("15:04")),
			"this command acts as the owner, and only a login session can", login)
	case envToken != "":
		return clierr.Detailed(clierr.ExitAuth,
			fmt.Errorf("this needs an owner signed in with a password; you are using an API token (from MACHINARCHY_TOKEN)"),
			"API tokens can never act as the owner", login)
	case entry.Token != nil:
		return clierr.Detailed(clierr.ExitAuth,
			fmt.Errorf("this needs an owner signed in with a password; you are using an API token%s", describeToken(*entry.Token)),
			"API tokens can never act as the owner", login)
	}

	return notSignedIn(server, login)
}

// noUsableCredential explains why a command that needs any credential has
// none.
func noUsableCredential(entry cred.Entry, now time.Time, server, login string) error {
	switch {
	case entry.Token != nil && entry.Token.Expired(now):
		return clierr.Detailed(clierr.ExitAuth,
			fmt.Errorf("the saved API token for %s expired at %s", server, entry.Token.ExpiresAt.Local().Format("2006-01-02 15:04")),
			"", login)
	case entry.Session != nil && entry.Session.Expired(now):
		return clierr.Detailed(clierr.ExitAuth,
			fmt.Errorf("your session on %s expired at %s", server, entry.Session.ExpiresAt.Local().Format("2006-01-02 15:04")),
			"", login)
	}

	return notSignedIn(server, login)
}

func notSignedIn(server, login string) error {
	return clierr.Detailed(clierr.ExitAuth, fmt.Errorf("you are not signed in to %s", server), "", login)
}

// describeToken renders " (name, role)" for the facts that are known.
func describeToken(c cred.Credential) string {
	var parts []string
	if c.Name != "" {
		parts = append(parts, c.Name)
	}
	if c.Role != "" {
		parts = append(parts, c.Role)
	}
	if len(parts) == 0 {
		return ""
	}

	return " (" + strings.Join(parts, ", ") + ")"
}
