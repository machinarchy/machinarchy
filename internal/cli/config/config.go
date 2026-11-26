// Package config holds the CLI's non-secret settings: the servers a person has
// signed in to, which one is active, and saved defaults for a few global
// options. Secrets are not here: credentials live in package cred, in a file of
// their own.
//
// The file is YAML. Global options resolve with one precedence, in
// settings.go: flag, MACHINARCHY_<NAME> environment variable, saved
// "defaults:", flag default. Which server and credential a command uses is
// decided in resolve.go.
//
// Servers replace the cluster, user and context trio of earlier versions. A
// config file in that older format is not read: it is named in an error that
// says how to move it aside and sign in again, so nothing is overwritten and no
// token is silently carried over.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"go.yaml.in/yaml/v3"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/fsutil"
)

const (
	fileVersion   = 2
	maxConfigSize = 1 << 20

	// MaxTokenBytes is the largest accepted API token.
	MaxTokenBytes = 64 << 10

	// DefaultRequestTimeout bounds one API request when no timeout is set.
	DefaultRequestTimeout = 30 * time.Second
)

// serverNamePattern is what a saved server may be called.
var serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// File is the on-disk CLI configuration.
type File struct {
	Version int `yaml:"version"`
	// Active is the name of the server commands use unless --server says
	// otherwise.
	Active  string            `yaml:"active,omitempty"`
	Servers map[string]Server `yaml:"servers,omitempty"`
	// Defaults are saved values for a few global options (see settings.go):
	// they apply when neither a flag nor an environment variable is given.
	Defaults map[string]string `yaml:"defaults,omitempty"`

	// The keys below belong to the format this CLI replaced. They are read only
	// to recognise such a file and refuse it with a helpful message.
	APIVersion string         `yaml:"apiVersion,omitempty"`
	Contexts   map[string]any `yaml:"contexts,omitempty"`
	Clusters   map[string]any `yaml:"clusters,omitempty"`
	Users      map[string]any `yaml:"users,omitempty"`
}

// Server is one server the person has signed in to.
type Server struct {
	// URL is the API base URL.
	URL string `yaml:"url"`
	// InsecureHTTP allows sending a credential over plain http:// to a host
	// that is not loopback. It is chosen per server, at login, and never
	// inherited by another URL.
	InsecureHTTP bool `yaml:"insecure-http,omitempty"`
	// CAFile is a PEM file of certificate authorities trusted for this server
	// only (a private CA, or a self-signed certificate).
	CAFile string `yaml:"ca-file,omitempty"`
	// Env is the default environment name or ID for commands on this server.
	Env string `yaml:"env,omitempty"`
	// ProtectedEnvs are environments guarded locally: the CLI asks for the
	// env's name before it changes them. It is enforced by this CLI only and
	// is never a server property (docs/cli/spec/04-security.md section 6.3).
	ProtectedEnvs []string `yaml:"protected-envs,omitempty"`
	// AgentNATSURL is the broker address new agents of this server connect to
	// (nats:// or tls://, no credentials in it). The server does not report it.
	AgentNATSURL string `yaml:"agent-nats-url,omitempty"`
	// AgentBootstrapCreds is the local path of the bootstrap credential file
	// copied to new agents. Only the path is stored, never the credential.
	AgentBootstrapCreds string `yaml:"agent-bootstrap-creds,omitempty"`
}

// Store holds the typed config document and the flag and environment
// overrides. A command changes and saves the document only through the
// methods below.
type Store struct {
	path string
	v    *settings
	doc  File
}

// ResolvePath applies precedence: --config, MACHINARCHY_CONFIG, then the
// platform's user config directory.
func ResolvePath(explicit string) (string, error) {
	if path := strings.TrimSpace(explicit); path != "" {
		return path, nil
	}
	if path := strings.TrimSpace(os.Getenv("MACHINARCHY_CONFIG")); path != "" {
		return path, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", clierr.Detailed(clierr.ExitUsage,
			fmt.Errorf("resolve user config directory: %w", err), "", "Set MACHINARCHY_CONFIG or pass --config.")
	}

	return filepath.Join(configDir, ".machinarchy", "config.yaml"), nil
}

// CredentialsPath returns where the credentials that belong to the config file
// at configPath are kept: next to it, with ".credentials" before the
// extension (config.yaml -> config.credentials.yaml).
func CredentialsPath(configPath string) string {
	return strings.TrimSuffix(configPath, filepath.Ext(configPath)) + ".credentials" + filepath.Ext(configPath)
}

// Load reads the config file, then binds environment variables and command
// flags. A missing file is not an error; a damaged one, or one in an older
// format, is.
func Load(path string, flags *pflag.FlagSet) (*Store, error) {
	resolvedPath, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}

	doc, err := readDocument(resolvedPath)
	if err != nil {
		return nil, err
	}

	return &Store{path: resolvedPath, v: newSettings(flags, doc.Defaults), doc: doc}, nil
}

// LoadSettings binds only flags and environment variables and reads no file.
// Commands that must work with a missing or damaged config file (help,
// version, completion) use it so global flags still apply.
func LoadSettings(flags *pflag.FlagSet) (*Store, error) {
	return &Store{v: newSettings(flags, nil), doc: newDocument()}, nil
}

// Path returns the config file path, or "" when no file was selected.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}

	return s.path
}

func newDocument() File {
	return File{Version: fileVersion, Servers: make(map[string]Server)}
}

func readDocument(path string) (File, error) {
	doc := newDocument()

	file, err := os.Open(path) // #nosec G304 -- path is the operator-selected CLI config file.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return doc, nil
		}

		return File{}, unreadable(path, err)
	}
	defer func() { _ = file.Close() }() // read-only: a close error cannot lose data

	info, err := file.Stat()
	if err != nil {
		return File{}, unreadable(path, err)
	}
	if info.IsDir() {
		return File{}, unreadable(path, errors.New("it is a directory"))
	}

	data, err := io.ReadAll(io.LimitReader(file, maxConfigSize+1))
	if err != nil {
		return File{}, unreadable(path, err)
	}
	if len(data) > maxConfigSize {
		return File{}, unreadable(path, errors.New("it exceeds the maximum supported size"))
	}

	if len(bytes.TrimSpace(data)) == 0 {
		return doc, nil
	}
	var parsed File
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return File{}, unreadable(path, fmt.Errorf("it is not valid YAML: %w", err))
	}

	if parsed.APIVersion != "" || parsed.Contexts != nil || parsed.Clusters != nil || parsed.Users != nil {
		return File{}, clierr.Detailed(clierr.ExitUsage,
			fmt.Errorf("the CLI config %s is in an older format that this version no longer reads", path),
			"it describes contexts, clusters and users, which servers replace; its tokens were not copied",
			fmt.Sprintf("mv %q %q\nmachinarchy login YOUR-SERVER-URL", path, path+".old"))
	}
	if parsed.Version > fileVersion {
		return File{}, clierr.Detailed(clierr.ExitUsage,
			fmt.Errorf("the CLI config %s has version %d; this CLI reads version %d", path, parsed.Version, fileVersion),
			"it was written by a newer machinarchy", "Update the CLI, or move the file aside and log in again.")
	}
	if err := validateDefaults(parsed.Defaults); err != nil {
		return File{}, err
	}
	for name, server := range parsed.Servers {
		if !serverNamePattern.MatchString(name) {
			return File{}, clierr.Newf(clierr.ExitUsage, "Rename it in "+path+".", "%q is not a valid server name in the CLI config", name)
		}
		if err := ValidateServer(server.URL); err != nil {
			return File{}, clierr.Detailed(clierr.ExitUsage, fmt.Errorf("server %q in the CLI config has an invalid URL", name), "", "machinarchy login "+"YOUR-SERVER-URL")
		}
	}

	parsed.Version = fileVersion
	if parsed.Servers == nil {
		parsed.Servers = make(map[string]Server)
	}

	return parsed, nil
}

func unreadable(path string, err error) error {
	return clierr.Detailed(clierr.ExitUsage,
		fmt.Errorf("read CLI config %q: %w", path, err),
		"", "Fix or remove the file, or point --config (or MACHINARCHY_CONFIG) at another file.")
}

// ServerNames returns the saved server names in stable order.
func (s *Store) ServerNames() []string {
	names := make([]string, 0, len(s.doc.Servers))
	for name := range s.doc.Servers {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

// Server returns a saved server by name.
func (s *Store) Server(name string) (Server, bool) {
	server, ok := s.doc.Servers[name]

	return server, ok
}

// ActiveName returns the saved active server's name, ignoring any --server
// override.
func (s *Store) ActiveName() string {
	if s == nil {
		return ""
	}

	return s.doc.Active
}

// SaveServer adds or replaces a saved server and makes it the active one.
func (s *Store) SaveServer(name string, server Server) error {
	if !serverNamePattern.MatchString(name) {
		return clierr.Newf(clierr.ExitUsage, "Use letters, digits, '.', '_' or '-' (up to 63 characters), for example --name lab.",
			"%q is not a valid server name", name)
	}
	if err := ValidateServer(server.URL); err != nil {
		return err
	}
	server.URL = strings.TrimRight(strings.TrimSpace(server.URL), "/")

	s.doc.Servers[name] = server
	s.doc.Active = name

	return s.save()
}

// Activate makes a saved server the active one.
func (s *Store) Activate(name string) error {
	if _, ok := s.doc.Servers[name]; !ok {
		return unknownServer(name, s.ServerNames())
	}
	s.doc.Active = name

	return s.save()
}

// RemoveServer forgets a saved server. When it was the active one, no server
// is active afterwards.
func (s *Store) RemoveServer(name string) error {
	if _, ok := s.doc.Servers[name]; !ok {
		return nil
	}
	delete(s.doc.Servers, name)
	if s.doc.Active == name {
		s.doc.Active = ""
	}

	return s.save()
}

func unknownServer(name string, known []string) error {
	next := "machinarchy login YOUR-SERVER-URL"
	why := "no server has been saved yet"
	if len(known) > 0 {
		next = "machinarchy switch\nmachinarchy login YOUR-SERVER-URL"
		why = "saved servers: " + strings.Join(known, ", ")
	}

	return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("no saved server is called %q", name), why, next)
}

// OutputFormat returns the selected resource output format, defaulting to
// "table". Use OutputExplicit to learn whether the user chose one.
func (s *Store) OutputFormat() string {
	if format := s.OutputExplicit(); format != "" {
		return format
	}

	return "table"
}

// OutputExplicit returns the output format chosen by flag, environment or saved
// default, or an empty string when the command's own default should apply.
func (s *Store) OutputExplicit() string {
	if s == nil || s.v == nil {
		return ""
	}

	return strings.ToLower(strings.TrimSpace(s.v.GetString("output")))
}

// Verbose reports whether debug logging is enabled.
func (s *Store) Verbose() bool {
	return s != nil && s.v != nil && s.v.GetBool("verbose")
}

// Quiet reports whether status output should be suppressed.
func (s *Store) Quiet() bool {
	return s != nil && s.v != nil && s.v.GetBool("quiet")
}

// NoHeaders reports whether table headers should be omitted.
func (s *Store) NoHeaders() bool {
	return s != nil && s.v != nil && s.v.GetBool("no-headers")
}

// NoInput reports whether interactive prompts are disabled.
func (s *Store) NoInput() bool {
	return s != nil && s.v != nil && s.v.GetBool("no-input")
}

// RequestTimeout returns the per-request HTTP timeout.
func (s *Store) RequestTimeout() (time.Duration, error) {
	if s == nil || s.v == nil {
		return DefaultRequestTimeout, nil
	}

	raw := strings.TrimSpace(s.v.GetString("request-timeout"))
	if raw == "" {
		return DefaultRequestTimeout, nil
	}

	timeout, err := time.ParseDuration(raw)
	if err != nil || timeout <= 0 {
		return 0, clierr.Usagef("invalid request timeout %q; use a positive duration such as 10s or 2m", raw)
	}

	return timeout, nil
}

// ValidateServer accepts absolute HTTP or HTTPS API base URLs.
func ValidateServer(server string) error {
	parsed, err := url.Parse(strings.TrimSpace(server))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return clierr.Usagef("API server must be an absolute http:// or https:// URL without credentials, query, or fragment")
	}

	return nil
}

// NormalizeURL turns what a person typed into an API base URL: https:// is
// assumed when there is no scheme, and a trailing slash is dropped.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", clierr.Usagef("the server address is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if err := ValidateServer(raw); err != nil {
		return "", err
	}

	return strings.TrimRight(raw, "/"), nil
}

// DefaultServerName derives a name for a server from its URL, for example
// "machinarchy-example-com" or "localhost-8080".
func DefaultServerName(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "server"
	}

	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(parsed.Host) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false

			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}

	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "server"
	}
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}

	return name
}

// CheckTransport refuses to send a credential over plain HTTP to a host that
// is not loopback, where anyone on the path can read it. insecureAllowed is the
// server's explicit --insecure-http choice.
func CheckTransport(server string, insecureAllowed bool) error {
	parsed, err := url.Parse(strings.TrimSpace(server))
	if err != nil || parsed.Scheme != "http" || insecureAllowed || IsLoopbackHost(parsed.Hostname()) {
		return nil
	}

	return clierr.Detailed(clierr.ExitUsage,
		fmt.Errorf("refusing to send your credentials over plain http:// to %s", parsed.Host),
		"anyone on the network path can read them",
		"Use an https:// address. On a network you trust, sign in again with --insecure-http:\n"+
			"  machinarchy login "+server+" --insecure-http")
}

// IsLoopbackHost reports whether host is localhost, a *.localhost name or a
// loopback IP literal.
func IsLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

// ValidateToken rejects empty, oversized, or whitespace/control-character
// tokens. A token that passes is safe to place in an Authorization header.
func ValidateToken(token string) error {
	if token == "" {
		return clierr.Usagef("API token is required")
	}
	invalid := len(token) > MaxTokenBytes
	for _, r := range token {
		if r <= ' ' || r == 0x7f {
			invalid = true

			break
		}
	}
	if invalid {
		return clierr.Usagef("API token is invalid; tokens must not contain whitespace or exceed 64 KiB")
	}

	return nil
}

func (s *Store) save() error {
	if s == nil {
		return errors.New("CLI config is not loaded")
	}
	if s.path == "" {
		return errors.New("CLI config file path is not set")
	}

	s.doc.Version = fileVersion
	data, err := yaml.Marshal(s.doc)
	if err != nil {
		return fmt.Errorf("encode CLI config: %w", err)
	}
	if len(data) > maxConfigSize {
		return errors.New("CLI config exceeds the maximum supported size")
	}

	// The file holds no secret, but it names every server, so it is private.
	return fsutil.WriteAtomic(s.path, data, 0o700, 0o600)
}

// savedServerName returns the saved server a command works on, for settings
// that are stored per server. A server given only as an address has no entry
// to store them in.
func (s *Store) savedServerName() (string, error) {
	name, _, adhoc, err := s.selectServer()
	if err != nil {
		return "", err
	}
	if adhoc || name == "" {
		return "", clierr.Detailed(clierr.ExitUsage, errors.New("this server is not saved, so a setting cannot be stored for it"),
			"it was given as an address with --server", "machinarchy login YOUR-SERVER-URL")
	}

	return name, nil
}

// SetEnv saves the default environment of the selected server; "" clears it.
func (s *Store) SetEnv(env string) error {
	name, err := s.savedServerName()
	if err != nil {
		return err
	}
	server := s.doc.Servers[name]
	server.Env = env
	s.doc.Servers[name] = server

	return s.save()
}

// AgentDefaults returns the saved broker URL and bootstrap credential path of
// the selected server ("" when not set).
func (s *Store) AgentDefaults() (natsURL, credsFile string) {
	if s == nil || s.v == nil {
		return "", ""
	}
	_, server, _, err := s.selectServer()
	if err != nil {
		return "", ""
	}

	return server.AgentNATSURL, server.AgentBootstrapCreds
}

// SetAgentDefaults saves the broker URL and credential path for the selected
// server; "" clears one.
func (s *Store) SetAgentDefaults(natsURL, credsFile string) error {
	name, err := s.savedServerName()
	if err != nil {
		return err
	}
	server := s.doc.Servers[name]
	server.AgentNATSURL, server.AgentBootstrapCreds = natsURL, credsFile
	s.doc.Servers[name] = server

	return s.save()
}

// LocalProtected reports whether env carries the local guard on the selected
// server.
func (s *Store) LocalProtected(env string) bool {
	if s == nil || s.v == nil {
		return false
	}
	_, server, _, err := s.selectServer()
	if err != nil {
		return false
	}
	for _, name := range server.ProtectedEnvs {
		if strings.EqualFold(name, env) {
			return true
		}
	}

	return false
}

// SetLocalProtected adds or removes the local guard on env for the selected
// server.
func (s *Store) SetLocalProtected(env string, protected bool) error {
	name, err := s.savedServerName()
	if err != nil {
		return err
	}
	server := s.doc.Servers[name]
	kept := make([]string, 0, len(server.ProtectedEnvs)+1)
	for _, existing := range server.ProtectedEnvs {
		if !strings.EqualFold(existing, env) {
			kept = append(kept, existing)
		}
	}
	if protected {
		kept = append(kept, env)
	}
	sort.Strings(kept)
	server.ProtectedEnvs = kept
	s.doc.Servers[name] = server

	return s.save()
}
