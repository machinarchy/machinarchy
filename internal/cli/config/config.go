// Package config handles the CLI's kubeconfig-style contexts independently of
// the server's runtime configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"
)

const (
	defaultAPIVersion = "machinarchy.io/v1"
	maxConfigSize     = 1 << 20
)

// File is the on-disk, kubeconfig-style CLI configuration.
type File struct {
	APIVersion     string             `mapstructure:"apiVersion" yaml:"apiVersion"`
	CurrentContext string             `mapstructure:"current-context" yaml:"current-context"`
	Contexts       map[string]Context `mapstructure:"contexts" yaml:"contexts"`
	Clusters       map[string]Cluster `mapstructure:"clusters" yaml:"clusters"`
	Users          map[string]User    `mapstructure:"users" yaml:"users"`
}

// Context selects a cluster, credential, and optional Machinarchy environment.
type Context struct {
	Cluster     string `mapstructure:"cluster" yaml:"cluster"`
	User        string `mapstructure:"user" yaml:"user"`
	Environment string `mapstructure:"environment" yaml:"environment,omitempty"`
}

// Cluster stores the API server base URL.
type Cluster struct {
	Server string `mapstructure:"server" yaml:"server"`
}

// User stores an API bearer token.
type User struct {
	Token string `mapstructure:"token" yaml:"token"`
}

// Target is the resolved endpoint and credential for a single CLI operation.
type Target struct {
	ContextName string
	Server      string
	Token       string
	Environment string
}

// Store holds one immutable-in-use Viper configuration and its typed document.
// A command updates and saves it only through the methods below.
type Store struct {
	path string
	v    *viper.Viper
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
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(configDir, ".machinarchy", "config.yaml"), nil
}

// Load reads the YAML config, then binds environment variables and command
// flags using Viper's normal precedence rules.
func Load(path string, flags *pflag.FlagSet) (*Store, error) {
	resolvedPath, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}

	v := viper.New()
	v.SetConfigFile(resolvedPath)
	v.SetConfigType("yaml")
	v.SetEnvPrefix("MACHINARCHY_CLI")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read CLI config %q: %w", resolvedPath, err)
		}
	}
	if flags != nil {
		if err := v.BindPFlags(flags); err != nil {
			return nil, fmt.Errorf("bind CLI flags: %w", err)
		}
	}

	doc := File{
		APIVersion: defaultAPIVersion,
		Contexts:   make(map[string]Context),
		Clusters:   make(map[string]Cluster),
		Users:      make(map[string]User),
	}
	if err := v.Unmarshal(&doc); err != nil {
		return nil, fmt.Errorf("decode CLI config %q: %w", resolvedPath, err)
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

	return &Store{path: resolvedPath, v: v, doc: doc}, nil
}

// Resolve combines the selected context with explicit flags and environment
// overrides. The returned token is never included in error messages.
// Resolve returns the target selected by flags, environment, or current context.
func (s *Store) Resolve() (Target, error) {
	if s == nil || s.v == nil {
		return Target{}, errors.New("CLI config is not loaded")
	}

	contextName := strings.TrimSpace(s.v.GetString("context"))
	if contextName == "" {
		contextName = strings.TrimSpace(s.doc.CurrentContext)
	}
	context, ok := s.doc.Contexts[contextName]
	if !ok || contextName == "" {
		return Target{}, errors.New("no active context is configured\n\nSet up an API endpoint, token, and context first:\n  machinarchy config set-cluster local --server http://localhost:8080\n  printf '%s' \"$MACHINARCHY_USER_TOKEN\" | machinarchy config set-credentials local --token-stdin\n  machinarchy config set-context local --cluster local --user local\n  machinarchy config use-context local")
	}

	cluster, clusterOK := s.doc.Clusters[context.Cluster]
	if !clusterOK {
		return Target{}, fmt.Errorf("context %q references unknown cluster %q", contextName, context.Cluster)
	}
	user, userOK := s.doc.Users[context.User]
	if !userOK {
		return Target{}, fmt.Errorf("context %q references unknown user %q", contextName, context.User)
	}

	server := strings.TrimSpace(s.v.GetString("server"))
	if server == "" {
		server = cluster.Server
	}
	if err := ValidateServer(server); err != nil {
		return Target{}, err
	}
	token := strings.TrimSpace(s.v.GetString("token"))
	if token == "" {
		token = user.Token
	}
	if token == "" {
		return Target{}, errors.New("no API token configured; set credentials or MACHINARCHY_CLI_TOKEN")
	}
	if len(token) > 64<<10 || strings.ContainsAny(token, " \t\r\n") {
		return Target{}, errors.New("API token is invalid; tokens must not contain whitespace or exceed 64 KiB")
	}
	environment := strings.TrimSpace(s.v.GetString("env"))
	if environment == "" {
		environment = context.Environment
	}

	return Target{
		ContextName: contextName,
		Server:      strings.TrimRight(server, "/"),
		Token:       token,
		Environment: environment,
	}, nil
}

// ContextName returns the effective context, including a --context override.
// ContextName returns the active context name, including any command override.
func (s *Store) ContextName() string {
	if s == nil || s.v == nil {
		return ""
	}
	if name := strings.TrimSpace(s.v.GetString("context")); name != "" {
		return name
	}
	return s.doc.CurrentContext
}

// Environment returns the effective environment filter, including --env.
// Environment returns the active Machinarchy environment filter.
func (s *Store) Environment() string {
	if s == nil || s.v == nil {
		return ""
	}
	if environment := strings.TrimSpace(s.v.GetString("env")); environment != "" {
		return environment
	}
	if context, ok := s.doc.Contexts[s.ContextName()]; ok {
		return context.Environment
	}
	return ""
}

// OutputFormat returns the selected resource output format.
func (s *Store) OutputFormat() string {
	if s == nil || s.v == nil {
		return "table"
	}
	format := strings.ToLower(strings.TrimSpace(s.v.GetString("output")))
	if format == "" {
		return "table"
	}
	return format
}

// Verbose reports whether debug logging is enabled.
func (s *Store) Verbose() bool {
	return s != nil && s.v != nil && s.v.GetBool("verbose")
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
	context, ok := s.doc.Contexts[name]
	return context, ok
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
func (s *Store) SetCluster(name, server string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("cluster name is required")
	}
	if err := ValidateServer(server); err != nil {
		return err
	}
	s.doc.Clusters[name] = Cluster{Server: strings.TrimRight(strings.TrimSpace(server), "/")}
	return s.save()
}

// SetUser adds or updates credentials and saves the config securely.
func (s *Store) SetUser(name, token string) error {
	name = strings.TrimSpace(name)
	token = strings.TrimSpace(token)
	if name == "" || token == "" {
		return errors.New("credential name and non-empty token are required")
	}
	if len(token) > 64<<10 || strings.ContainsAny(token, " \t\r\n") {
		return errors.New("API token is invalid; tokens must not contain whitespace or exceed 64 KiB")
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
		return errors.New("context name, cluster, and user are required")
	}
	if _, ok := s.doc.Clusters[cluster]; !ok {
		return fmt.Errorf("cluster %q does not exist; create it with 'machinarchy config set-cluster'", cluster)
	}
	if _, ok := s.doc.Users[user]; !ok {
		return fmt.Errorf("user %q does not exist; create it with 'machinarchy config set-credentials'", user)
	}
	s.doc.Contexts[name] = Context{Cluster: cluster, User: user, Environment: environment}
	return s.save()
}

// UseContext persists the selected current context.
func (s *Store) UseContext(name string) error {
	name = strings.TrimSpace(name)
	if _, ok := s.doc.Contexts[name]; !ok {
		return fmt.Errorf("context %q does not exist", name)
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
		return errors.New("API server must be an absolute http:// or https:// URL without credentials, query, or fragment")
	}
	return nil
}

func (s *Store) save() error {
	if s == nil {
		return errors.New("CLI config is not loaded")
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
	defer os.Remove(tempPath)

	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write CLI config: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync CLI config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("replace CLI config: %w", err)
	}
	return nil
}
