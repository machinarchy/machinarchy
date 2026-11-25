// Package config handles the CLI's kubeconfig-style contexts independently of
// the server's runtime configuration.
//
// The on-disk document is parsed directly as YAML so that context, cluster,
// and user names are preserved exactly (Viper would case-fold map keys and
// split them on dots). Viper is used only to resolve flag and environment
// overrides with the documented precedence: flag, environment, config file,
// default.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"go.yaml.in/yaml/v3"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

const (
	defaultAPIVersion = "machinarchy.io/v1"
	maxConfigSize     = 1 << 20

	// MaxTokenBytes is the largest accepted API token.
	MaxTokenBytes = 64 << 10

	// DefaultRequestTimeout bounds one API request when no timeout is set.
	DefaultRequestTimeout = 30 * time.Second

	envPrefix = "MACHINARCHY_CLI"

	// flagYes is deliberately never bound to the environment or the config
	// file: a confirmation bypass must be an explicit per-invocation choice.
	flagYes = "yes"

	setupHint = "Log in to create a context:\n" +
		"  machinarchy login --server http://localhost:8080 --context local --username admin\n" +
		"or, with an API token, set one up by hand:\n" +
		"  machinarchy config set-cluster local --server http://localhost:8080\n" +
		"  printf '%s' \"$MACHINARCHY_USER_TOKEN\" | machinarchy config set-credentials local --token-stdin\n" +
		"  machinarchy config set-context local --cluster local --user local\n" +
		"  machinarchy config use-context local"

	fileHint = "Fix or remove the file, or point --config (or MACHINARCHY_CONFIG) at another file."
)

// File is the on-disk, kubeconfig-style CLI configuration.
type File struct {
	APIVersion     string             `yaml:"apiVersion"`
	CurrentContext string             `yaml:"current-context"`
	Contexts       map[string]Context `yaml:"contexts"`
	Clusters       map[string]Cluster `yaml:"clusters"`
	Users          map[string]User    `yaml:"users"`
}

// Context selects a cluster, credential, and optional Machinarchy environment.
type Context struct {
	Cluster     string `yaml:"cluster"`
	User        string `yaml:"user"`
	Environment string `yaml:"environment,omitempty"`
}

// Cluster stores the API server base URL.
type Cluster struct {
	Server string `yaml:"server"`
	// InsecureHTTP allows sending the token over plain http:// to a host that
	// is not loopback (AUDIT S-18). Set only with config set-cluster --insecure-http.
	InsecureHTTP bool `yaml:"insecure-http,omitempty"`
}

// User stores an API bearer token.
type User struct {
	Token string `yaml:"token"`
}

// Target is the resolved endpoint and credential for a single CLI operation.
type Target struct {
	ContextName    string
	Server         string
	Token          string
	Environment    string
	RequestTimeout time.Duration
}

// Store holds the typed config document and the flag/environment overrides.
// A command updates and saves the document only through the methods below.
type Store struct {
	path     string
	v        *settings
	doc      File
	warnings []string
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
		return "", clierr.Wrap(clierr.ExitUsage,
			fmt.Errorf("resolve user config directory: %w", err),
			"Set MACHINARCHY_CONFIG or pass --config.")
	}
	return filepath.Join(configDir, ".machinarchy", "config.yaml"), nil
}

// Load reads the YAML config, then binds environment variables and command
// flags. A missing file is not an error; a damaged one is.
func Load(path string, flags *pflag.FlagSet) (*Store, error) {
	resolvedPath, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}

	doc, warnings, err := readDocument(resolvedPath)
	if err != nil {
		return nil, err
	}

	return &Store{path: resolvedPath, v: newSettings(flags), doc: doc, warnings: warnings}, nil
}

// LoadSettings binds only flags and environment variables and reads no file.
// Commands that must work with a missing or damaged config file (help,
// version, completion) use it so global flags still apply.
func LoadSettings(flags *pflag.FlagSet) (*Store, error) {
	return &Store{v: newSettings(flags), doc: newDocument()}, nil
}

// settings resolves a global setting with the precedence flag (when given on
// the command line) > MACHINARCHY_CLI_<NAME> environment variable > flag
// default. It replaces viper, which the CLI used only for this lookup (AUDIT
// P-02); the config file is read separately by readDocument.
type settings struct {
	flags *pflag.FlagSet
}

func newSettings(flags *pflag.FlagSet) *settings { return &settings{flags: flags} }

// envName maps a flag name to its variable, for example request-timeout to
// MACHINARCHY_CLI_REQUEST_TIMEOUT.
func envName(key string) string {
	return envPrefix + "_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(key))
}

func (s *settings) GetString(key string) string {
	var flag *pflag.Flag
	if s != nil && s.flags != nil {
		flag = s.flags.Lookup(key)
	}
	if flag != nil && flag.Changed {
		return flag.Value.String()
	}
	// --yes is deliberately not settable through the environment.
	if key != flagYes {
		if value, ok := os.LookupEnv(envName(key)); ok {
			return value
		}
	}
	if flag != nil {
		return flag.DefValue
	}
	return ""
}

func (s *settings) GetBool(key string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(s.GetString(key)))
	return err == nil && value
}

func newDocument() File {
	return File{
		APIVersion: defaultAPIVersion,
		Contexts:   make(map[string]Context),
		Clusters:   make(map[string]Cluster),
		Users:      make(map[string]User),
	}
}

func readDocument(path string) (File, []string, error) {
	doc := newDocument()

	file, err := os.Open(path) // #nosec G304 -- path is the operator-selected CLI config file.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return doc, nil, nil
		}
		return File{}, nil, clierr.Wrap(clierr.ExitUsage,
			fmt.Errorf("read CLI config %q: %w", path, err), fileHint)
	}
	defer func() { _ = file.Close() }() // read-only: a close error cannot lose data

	info, err := file.Stat()
	if err != nil {
		return File{}, nil, clierr.Wrap(clierr.ExitUsage,
			fmt.Errorf("read CLI config %q: %w", path, err), fileHint)
	}
	if info.IsDir() {
		return File{}, nil, clierr.Wrap(clierr.ExitUsage,
			fmt.Errorf("CLI config %q is a directory", path), fileHint)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxConfigSize+1))
	if err != nil {
		return File{}, nil, clierr.Wrap(clierr.ExitUsage,
			fmt.Errorf("read CLI config %q: %w", path, err), fileHint)
	}
	if len(data) > maxConfigSize {
		return File{}, nil, clierr.Wrap(clierr.ExitUsage,
			fmt.Errorf("CLI config %q exceeds the maximum supported size", path), fileHint)
	}

	if len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return File{}, nil, clierr.Wrap(clierr.ExitUsage,
				fmt.Errorf("parse CLI config %q: %w", path, err), fileHint)
		}
	}

	if doc.APIVersion == "" {
		doc.APIVersion = defaultAPIVersion
	}
	if doc.Contexts == nil {
		doc.Contexts = make(map[string]Context)
	}
	if doc.Clusters == nil {
		doc.Clusters = make(map[string]Cluster)
	}
	if doc.Users == nil {
		doc.Users = make(map[string]User)
	}

	return doc, permissionWarnings(path, info, doc), nil
}

// permissionWarnings reports a config file that stores tokens but is readable
// by other users. It never fails the command, so existing setups keep working.
func permissionWarnings(path string, info os.FileInfo, doc File) []string {
	if runtime.GOOS == "windows" || info.Mode().Perm()&0o077 == 0 {
		return nil
	}

	for _, user := range doc.Users {
		if user.Token != "" {
			return []string{fmt.Sprintf(
				"CLI config %s is readable by other users (mode %04o) and contains API tokens; restrict it with: chmod 600 %s",
				path, info.Mode().Perm(), path)}
		}
	}

	return nil
}

// Warnings returns non-fatal problems found while loading the config file.
func (s *Store) Warnings() []string {
	if s == nil {
		return nil
	}

	return s.warnings
}

// Resolve combines the selected context with explicit flags and environment
// overrides. The returned token is never included in error messages.
func (s *Store) Resolve() (Target, error) {
	if s == nil || s.v == nil {
		return Target{}, errors.New("CLI config is not loaded")
	}

	contextName := s.ContextName()
	if contextName == "" {
		return Target{}, clierr.Wrap(clierr.ExitUsage,
			errors.New("no active context is configured"), setupHint)
	}
	selected, ok := s.doc.Contexts[contextName]
	if !ok {
		return Target{}, clierr.Wrap(clierr.ExitUsage,
			fmt.Errorf("context %q does not exist", contextName),
			"List configured contexts with 'machinarchy config get-contexts'.\n\n"+setupHint)
	}

	cluster, clusterOK := s.doc.Clusters[selected.Cluster]
	if !clusterOK {
		return Target{}, clierr.Usagef("context %q references unknown cluster %q", contextName, selected.Cluster)
	}
	user, userOK := s.doc.Users[selected.User]
	if !userOK {
		return Target{}, clierr.Usagef("context %q references unknown user %q", contextName, selected.User)
	}

	server := strings.TrimSpace(s.v.GetString("server"))
	insecureAllowed := false
	if server == "" {
		server = cluster.Server
		insecureAllowed = cluster.InsecureHTTP
	}
	if err := ValidateServer(server); err != nil {
		return Target{}, err
	}
	if err := checkTransport(server, insecureAllowed); err != nil {
		return Target{}, err
	}

	token := strings.TrimSpace(s.v.GetString("token"))
	if token == "" {
		token = user.Token
	}
	if token == "" {
		return Target{}, clierr.Wrap(clierr.ExitUsage,
			errors.New("no API token configured; set credentials or MACHINARCHY_CLI_TOKEN"),
			"Store a token with: printf '%s' \"$TOKEN\" | machinarchy config set-credentials NAME --token-stdin")
	}
	if err := ValidateToken(token); err != nil {
		return Target{}, err
	}

	timeout, err := s.RequestTimeout()
	if err != nil {
		return Target{}, err
	}

	environment := strings.TrimSpace(s.v.GetString("env"))
	if environment == "" {
		environment = selected.Environment
	}

	return Target{
		ContextName:    contextName,
		Server:         strings.TrimRight(server, "/"),
		Token:          token,
		Environment:    environment,
		RequestTimeout: timeout,
	}, nil
}

// LoginTarget is where `machinarchy login` signs in and which user entry
// receives the session token.
type LoginTarget struct {
	ContextName    string
	Server         string
	User           string
	RequestTimeout time.Duration
	// Create: the context does not exist yet; login creates a cluster, user
	// and context named ContextName for Server.
	Create bool
}

// ResolveLogin resolves the active context's server and user without
// requiring a stored token, for `machinarchy login`.
func (s *Store) ResolveLogin() (LoginTarget, error) {
	if s == nil || s.v == nil {
		return LoginTarget{}, errors.New("CLI config is not loaded")
	}
	contextName := s.ContextName()
	override := strings.TrimSpace(s.v.GetString("server"))
	selected, ok := s.doc.Contexts[contextName]
	if !ok {
		// A first login with --server creates the context.
		if override == "" {
			return LoginTarget{}, clierr.Wrap(clierr.ExitUsage, fmt.Errorf("context %q does not exist", contextName),
				"For a first login, name the server and the context to create: machinarchy login --server URL --context NAME --username USER")
		}
		if contextName == "" {
			contextName = "default"
		}
		if err := ValidateServer(override); err != nil {
			return LoginTarget{}, err
		}
		if err := checkTransport(override, false); err != nil {
			return LoginTarget{}, err
		}
		timeout, err := s.RequestTimeout()
		if err != nil {
			return LoginTarget{}, err
		}
		return LoginTarget{ContextName: contextName, Server: strings.TrimRight(override, "/"), User: contextName,
			RequestTimeout: timeout, Create: true}, nil
	}
	cluster, ok := s.doc.Clusters[selected.Cluster]
	if !ok {
		return LoginTarget{}, clierr.Usagef("context %q references unknown cluster %q", contextName, selected.Cluster)
	}
	server := strings.TrimSpace(s.v.GetString("server"))
	insecureAllowed := false
	if server == "" {
		server, insecureAllowed = cluster.Server, cluster.InsecureHTTP
	}
	if err := ValidateServer(server); err != nil {
		return LoginTarget{}, err
	}
	// A password is sent here: the same transport rule as for tokens.
	if err := checkTransport(server, insecureAllowed); err != nil {
		return LoginTarget{}, err
	}
	timeout, err := s.RequestTimeout()
	if err != nil {
		return LoginTarget{}, err
	}
	return LoginTarget{ContextName: contextName, Server: strings.TrimRight(server, "/"), User: selected.User, RequestTimeout: timeout}, nil
}

// ContextName returns the active context name, including any --context override.
func (s *Store) ContextName() string {
	if s == nil || s.v == nil {
		return ""
	}
	if name := strings.TrimSpace(s.v.GetString("context")); name != "" {
		return name
	}
	return s.doc.CurrentContext
}

// Environment returns the active Machinarchy environment filter, including --env.
func (s *Store) Environment() string {
	if s == nil || s.v == nil {
		return ""
	}
	if environment := strings.TrimSpace(s.v.GetString("env")); environment != "" {
		return environment
	}
	if selected, ok := s.doc.Contexts[s.ContextName()]; ok {
		return selected.Environment
	}
	return ""
}

// OutputFormat returns the selected resource output format, defaulting to
// "table". Use OutputExplicit to learn whether the user chose one.
func (s *Store) OutputFormat() string {
	if format := s.OutputExplicit(); format != "" {
		return format
	}
	return "table"
}

// OutputExplicit returns the output format chosen by flag or environment, or
// an empty string when the command's own default should apply.
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

// ContextNames returns configured context names in stable sorted order.
func (s *Store) ContextNames() []string {
	names := make([]string, 0, len(s.doc.Contexts))
	for name := range s.doc.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetContext returns a named context if it exists.
func (s *Store) GetContext(name string) (Context, bool) {
	selected, ok := s.doc.Contexts[name]
	return selected, ok
}

// CurrentContext returns the persisted current context, ignoring one-command overrides.
func (s *Store) CurrentContext() string {
	if s == nil {
		return ""
	}
	return s.doc.CurrentContext
}

// Cluster returns a named API cluster if it exists.
func (s *Store) Cluster(name string) (Cluster, bool) {
	cluster, ok := s.doc.Clusters[name]
	return cluster, ok
}

// User returns named credentials if they exist.
func (s *Store) User(name string) (User, bool) {
	user, ok := s.doc.Users[name]
	return user, ok
}

// SetCluster adds or updates a cluster and saves the config securely.
func (s *Store) SetCluster(name, server string, insecureHTTP bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return clierr.Usagef("cluster name is required")
	}
	if err := ValidateServer(server); err != nil {
		return err
	}
	if err := checkTransport(server, insecureHTTP); err != nil {
		return err
	}
	s.doc.Clusters[name] = Cluster{Server: strings.TrimRight(strings.TrimSpace(server), "/"), InsecureHTTP: insecureHTTP}
	return s.save()
}

// SetUser adds or updates credentials and saves the config securely.
func (s *Store) SetUser(name, token string) error {
	name = strings.TrimSpace(name)
	token = strings.TrimSpace(token)
	if name == "" || token == "" {
		return clierr.Usagef("credential name and non-empty token are required")
	}
	if err := ValidateToken(token); err != nil {
		return err
	}
	s.doc.Users[name] = User{Token: token}
	return s.save()
}

// SetContext adds or updates a context that references existing cluster and user entries.
func (s *Store) SetContext(name, cluster, user, environment string) error {
	name = strings.TrimSpace(name)
	cluster = strings.TrimSpace(cluster)
	user = strings.TrimSpace(user)
	environment = strings.TrimSpace(environment)
	if name == "" || cluster == "" || user == "" {
		return clierr.Usagef("context name, cluster, and user are required")
	}
	if _, ok := s.doc.Clusters[cluster]; !ok {
		return clierr.Usagef("cluster %q does not exist; create it with 'machinarchy config set-cluster'", cluster)
	}
	if _, ok := s.doc.Users[user]; !ok {
		return clierr.Usagef("user %q does not exist; create it with 'machinarchy config set-credentials'", user)
	}
	s.doc.Contexts[name] = Context{Cluster: cluster, User: user, Environment: environment}
	return s.save()
}

// UseContext persists the selected current context.
func (s *Store) UseContext(name string) error {
	name = strings.TrimSpace(name)
	if _, ok := s.doc.Contexts[name]; !ok {
		return clierr.Usagef("context %q does not exist", name)
	}
	s.doc.CurrentContext = name
	return s.save()
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

// checkTransport refuses to send a bearer token over plain HTTP to a host
// that is not loopback, where anyone on the path can read it (AUDIT S-18).
// insecureAllowed is the cluster's explicit --insecure-http opt-in.
func checkTransport(server string, insecureAllowed bool) error {
	parsed, err := url.Parse(strings.TrimSpace(server))
	if err != nil || parsed.Scheme != "http" || insecureAllowed || IsLoopbackHost(parsed.Hostname()) {
		return nil
	}
	return clierr.Wrap(clierr.ExitUsage,
		fmt.Errorf("refusing to send the API token over plain http:// to %s", parsed.Host),
		"Use an https:// server URL. If this network is trusted, opt in for this cluster with:\n"+
			"  machinarchy config set-cluster NAME --server "+server+" --insecure-http")
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
	data, err := yaml.Marshal(s.doc)
	if err != nil {
		return fmt.Errorf("encode CLI config: %w", err)
	}
	if len(data) > maxConfigSize {
		return errors.New("CLI config exceeds the maximum supported size")
	}

	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	file, err := os.CreateTemp(directory, ".machinarchy-config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tempPath := file.Name()
	defer func() {
		if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("failed to remove temporary config file",
				"path", tempPath,
				"error", err,
			)
		}
	}()

	// Write, sync, and close the temporary file.
	writeErr := func() (retErr error) {
		defer func() {
			if err := file.Close(); err != nil {
				retErr = errors.Join(
					retErr,
					fmt.Errorf("close temporary config: %w", err),
				)
			}
		}()

		if err := file.Chmod(0o600); err != nil {
			return fmt.Errorf("secure temporary config: %w", err)
		}

		if _, err := file.Write(data); err != nil {
			return fmt.Errorf("write CLI config: %w", err)
		}

		if err := file.Sync(); err != nil {
			return fmt.Errorf("sync CLI config: %w", err)
		}

		return nil
	}()
	if writeErr != nil {
		return writeErr
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("replace CLI config: %w", err)
	}
	return nil
}
