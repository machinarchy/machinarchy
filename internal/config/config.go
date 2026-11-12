// Package config provides configuration parsing and validation for the application.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/utils"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

// Environment represents the execution environment (e.g., development, production).
type Environment string

// Supported execution environments.
const (
	Development Environment = "development"
	Production  Environment = "production"
)

// Validate checks if the Environment value is valid (either Development or Production).
func (e Environment) Validate() error {
	switch e {
	case Development, Production:
		return nil
	default:
		return fmt.Errorf("MACHINARCHY_ENVIRONMENT must be 'development' or 'production' (got: %q)", e)
	}
}

// DatabaseEngine represents the type of database engine used (e.g., SQLite, Postgres).
type DatabaseEngine string

// Supported database engines.
const (
	SQLite   DatabaseEngine = "sqlite"   // SQLite is the default database engine for local development and testing.
	Postgres DatabaseEngine = "postgres" // Postgres is the recommended database engine for production deployments, offering better performance and scalability.
)

// Validate checks if the DatabaseEngine value is valid (either SQLite or Postgres).
func (de DatabaseEngine) Validate() error {
	switch de {
	case SQLite, Postgres:
		return nil
	default:
		return fmt.Errorf("MACHINARCHY_DATABASE_ENGINE must be 'sqlite' or 'postgres' (got: %q)", de)
	}
}

// DatabaseConfig holds the configuration for the database connection.
type DatabaseConfig struct {
	Engine          DatabaseEngine
	Path            string
	URL             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnectTimeout  time.Duration
}

// Config holds the overall application configuration, including server settings, database configuration, and other operational parameters.
type Config struct {
	ListenAddr             string
	Environment            Environment
	SignupEnabled          bool
	OperatorToken          string
	EncryptionSecret       string
	EncryptionKeyID        string
	EncryptionPreviousKeys map[string]string
	LogLevel               string
	Database               DatabaseConfig
	EventBroker            EventBrokerConfig
	BootstrapAdmin         BootstrapAdminConfig
	SSHTargetPolicy        netpolicy.Policy
	Execution              domain.ExecutionPolicy
}

// BootstrapAdminConfig describes the optional first owner account. The account is
// only created when the default workspace has no owner, so leaving the password
// configured after the first start is harmless but unnecessary.
type BootstrapAdminConfig struct {
	Username string
	Email    string
	Password string
}

// EventBrokerConfig holds the configuration for the event broker connection.
type EventBrokerConfig struct {
	URL string
}

// Load reads the configuration from environment variables, applies defaults, and validates the configuration values. It returns a Config struct or an error if validation fails.
func Load() (Config, error) {
	return load(true)
}

// LoadWithOptions loads immutable bootstrap settings with precedence flags,
// environment, file, then defaults. Secret values are never accepted from
// flags or the configuration file.
type LoadOptions struct {
	FilePath            string
	Flags               map[string]string
	RequireLegacyBroker bool
}

func LoadWithOptions(options LoadOptions) (Config, error) {
	fileValues := map[string]string{}
	if options.FilePath != "" {
		f, err := os.Open(options.FilePath)
		if err != nil {
			return Config{}, errors.New("unable to open bootstrap configuration file")
		}
		defer utils.CloseWithLog(f, options.FilePath)
		dec := json.NewDecoder(io.LimitReader(f, 1<<20))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&fileValues); err != nil {
			return Config{}, errors.New("bootstrap configuration file must be a JSON object of string values")
		}
		for key := range fileValues {
			u := strings.ToUpper(key)
			if strings.Contains(u, "_SECRET") || strings.Contains(u, "_PASSWORD") || strings.Contains(u, "_TOKEN") {
				return Config{}, fmt.Errorf("bootstrap file must refer to secret files instead of containing %s values", key)
			}
		}
	}
	get := func(key string, defaults ...string) string {
		if v, ok := options.Flags[key]; ok && strings.TrimSpace(v) != "" {
			return v
		}
		if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
			return v
		}
		if v, ok := fileValues[key]; ok && strings.TrimSpace(v) != "" {
			return v
		}
		if len(defaults) > 0 {
			return defaults[0]
		}
		return ""
	}
	return loadWithGetter(options.RequireLegacyBroker, get)
}

// LoadCommandLine supports non-secret startup flags and a JSON bootstrap file.
func LoadCommandLine(args []string, requireLegacyBroker bool) (Config, error) {
	flags := map[string]string{}
	file := ""
	allowed := map[string]string{"--listen-addr": "MACHINARCHY_LISTEN_ADDR", "--environment": "MACHINARCHY_ENVIRONMENT", "--database-engine": "MACHINARCHY_DATABASE_ENGINE", "--database-path": "MACHINARCHY_DATABASE_PATH", "--database-url": "MACHINARCHY_DATABASE_URL", "--database-max-open-conns": "MACHINARCHY_DATABASE_MAX_OPEN_CONNS", "--database-max-idle-conns": "MACHINARCHY_DATABASE_MAX_IDLE_CONNS", "--database-conn-max-lifetime": "MACHINARCHY_DATABASE_CONN_MAX_LIFETIME", "--database-connect-timeout": "MACHINARCHY_DATABASE_CONNECT_TIMEOUT", "--log-level": "MACHINARCHY_LOG_LEVEL", "--signup-enabled": "MACHINARCHY_SIGNUP_ENABLED", "--broker-url": "MACHINARCHY_BROKER_URL", "--encryption-key-id": "MACHINARCHY_ENCRYPTION_KEY_ID", "--encryption-secret-file": "MACHINARCHY_ENCRYPTION_SECRET_FILE", "--encryption-keyring-file": "MACHINARCHY_ENCRYPTION_KEYRING_FILE", "--ssh-allow-private": "MACHINARCHY_SSH_ALLOW_PRIVATE", "--ssh-allow-loopback": "MACHINARCHY_SSH_ALLOW_LOOPBACK", "--ssh-allowed-cidrs": "MACHINARCHY_SSH_ALLOWED_CIDRS", "--ssh-denied-cidrs": "MACHINARCHY_SSH_DENIED_CIDRS", "--execution-enabled": "MACHINARCHY_EXECUTION_ENABLED", "--execution-allowed-programs": "MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--config" {
			if i+1 >= len(args) {
				return Config{}, errors.New("--config requires a file path")
			}
			i++
			file = args[i]
			continue
		}
		if strings.HasPrefix(arg, "--config=") {
			file = strings.TrimPrefix(arg, "--config=")
			continue
		}
		name, value, has := strings.Cut(arg, "=")
		envKey, ok := allowed[name]
		if !ok {
			return Config{}, fmt.Errorf("unknown startup flag %q", name)
		}
		if !has {
			if i+1 >= len(args) {
				return Config{}, fmt.Errorf("%s requires a value", name)
			}
			i++
			value = args[i]
		}
		flags[envKey] = value
	}
	return LoadWithOptions(LoadOptions{FilePath: file, Flags: flags, RequireLegacyBroker: requireLegacyBroker})
}

// LoadControlManager loads only settings required by the SQL-backed control
// loop. The standalone manager does not connect to the legacy NATS adapter.
func LoadControlManager() (Config, error) {
	return load(false)
}

func load(requireLegacyBroker bool) (Config, error) {
	return loadWithGetter(requireLegacyBroker, GetEnv)
}

func loadWithGetter(requireLegacyBroker bool, get func(string, ...string) string) (Config, error) {
	cfg := Config{
		ListenAddr:    get("MACHINARCHY_LISTEN_ADDR", ":8080"),
		Environment:   Environment(strings.ToLower(get("MACHINARCHY_ENVIRONMENT", string(Development)))),
		OperatorToken: get("MACHINARCHY_OPERATOR_TOKEN"),
		LogLevel:      strings.ToLower(get("MACHINARCHY_LOG_LEVEL", "info")),
		Database: DatabaseConfig{
			Engine:         DatabaseEngine(strings.ToLower(get("MACHINARCHY_DATABASE_ENGINE", string(SQLite)))),
			Path:           get("MACHINARCHY_DATABASE_PATH", "./data/machinarchy.db"),
			URL:            get("MACHINARCHY_DATABASE_URL", ""),
			ConnectTimeout: 5 * time.Second,
		},
		EventBroker: EventBrokerConfig{
			URL: get("MACHINARCHY_BROKER_URL", "nats://localhost:4222"),
		},
	}

	// Structural Domain Validation
	if err := cfg.Environment.Validate(); err != nil {
		return Config{}, err
	}

	// Public self-registration is useful for local development, but a new
	// account currently has the same API access as every existing account.
	// Require an explicit production opt-in until resource-level authorization
	// is implemented.
	signupDefault := "true"
	if cfg.Environment == Production {
		signupDefault = "false"
	}
	signupEnabled, err := strconv.ParseBool(get("MACHINARCHY_SIGNUP_ENABLED", signupDefault))
	if err != nil {
		return Config{}, errors.New("MACHINARCHY_SIGNUP_ENABLED must be true or false")
	}
	cfg.SignupEnabled = signupEnabled

	if err := cfg.Database.Engine.Validate(); err != nil {
		return Config{}, err
	}

	if err := validateListenAddr(cfg.ListenAddr); err != nil {
		return Config{}, err
	}

	// Configuration loading returns errors to the caller; it must never exit the
	// process or silently substitute a predictable signing/encryption secret.
	if cfg.OperatorToken == "" {
		return Config{}, errors.New("MACHINARCHY_OPERATOR_TOKEN is required")
	}
	if len(cfg.OperatorToken) < 32 || strings.ContainsAny(cfg.OperatorToken, " \t\r\n") {
		return Config{}, errors.New("MACHINARCHY_OPERATOR_TOKEN must be at least 32 characters with no whitespace")
	}
	cfg.EncryptionSecret = GetEnv("MACHINARCHY_ENCRYPTION_SECRET")
	if keyFile := strings.TrimSpace(get("MACHINARCHY_ENCRYPTION_SECRET_FILE")); keyFile != "" {
		info, statErr := os.Lstat(keyFile)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return Config{}, errors.New("MACHINARCHY_ENCRYPTION_SECRET_FILE must be a regular file with permissions 0600 or stricter")
		}
		data, readErr := os.ReadFile(keyFile)
		if readErr != nil {
			return Config{}, errors.New("unable to read encryption key file")
		}
		fileKey := strings.TrimSpace(string(data))
		if cfg.EncryptionSecret != "" && cfg.EncryptionSecret != fileKey {
			return Config{}, errors.New("encryption secret environment value and key file disagree")
		}
		cfg.EncryptionSecret = fileKey
	}
	cfg.EncryptionKeyID = strings.TrimSpace(get("MACHINARCHY_ENCRYPTION_KEY_ID", "bootstrap-v1"))
	if cfg.EncryptionKeyID == "" || len(cfg.EncryptionKeyID) > 128 || strings.ContainsAny(cfg.EncryptionKeyID, " \t\r\n") {
		return Config{}, errors.New("MACHINARCHY_ENCRYPTION_KEY_ID must be a non-empty identifier without whitespace")
	}
	cfg.EncryptionPreviousKeys = map[string]string{}
	if ringFile := strings.TrimSpace(get("MACHINARCHY_ENCRYPTION_KEYRING_FILE")); ringFile != "" {
		info, statErr := os.Lstat(ringFile)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return Config{}, errors.New("MACHINARCHY_ENCRYPTION_KEYRING_FILE must be a regular file with permissions 0600 or stricter")
		}
		raw, readErr := os.ReadFile(ringFile)
		if readErr != nil {
			return Config{}, errors.New("unable to read encryption keyring file")
		}
		if err = json.Unmarshal(raw, &cfg.EncryptionPreviousKeys); err != nil {
			return Config{}, errors.New("encryption keyring file must contain a JSON object of key identifiers and values")
		}
		for id, key := range cfg.EncryptionPreviousKeys {
			if id == "" || id == cfg.EncryptionKeyID || len(id) > 128 || len([]byte(key)) < 32 || strings.ContainsAny(id+key, " \t\r\n") {
				return Config{}, errors.New("encryption keyring contains an invalid previous key entry")
			}
		}
	}
	if cfg.EncryptionSecret == "" {
		return Config{}, errors.New("MACHINARCHY_ENCRYPTION_SECRET or MACHINARCHY_ENCRYPTION_SECRET_FILE is required and must be managed independently of the JWT signing secret")
	}
	if len([]byte(cfg.EncryptionSecret)) < 32 || strings.ContainsAny(cfg.EncryptionSecret, " \t\r\n") {
		return Config{}, errors.New("MACHINARCHY_ENCRYPTION_SECRET must be at least 32 bytes with no whitespace")
	}

	cfg.BootstrapAdmin = BootstrapAdminConfig{
		Username: strings.ToLower(get("MACHINARCHY_BOOTSTRAP_ADMIN_USERNAME", "admin")),
		Email:    strings.ToLower(get("MACHINARCHY_BOOTSTRAP_ADMIN_EMAIL")),
		Password: GetEnv("MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD"),
	}
	if cfg.BootstrapAdmin.Password != "" {
		if n := len([]byte(cfg.BootstrapAdmin.Password)); n < 12 || n > 72 {
			return Config{}, errors.New("MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD must be between 12 and 72 bytes")
		}
		if strings.ContainsAny(cfg.BootstrapAdmin.Username, " \t\r\n") {
			return Config{}, errors.New("MACHINARCHY_BOOTSTRAP_ADMIN_USERNAME must not contain whitespace")
		}
	}

	targetPolicy, err := loadSSHTargetPolicy(get)
	if err != nil {
		return Config{}, err
	}
	cfg.SSHTargetPolicy = targetPolicy

	executionEnabled, err := strconv.ParseBool(get("MACHINARCHY_EXECUTION_ENABLED", "false"))
	if err != nil {
		return Config{}, errors.New("MACHINARCHY_EXECUTION_ENABLED must be true or false")
	}
	execution, err := domain.NewExecutionPolicy(executionEnabled,
		strings.Split(get("MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS"), ","))
	if err != nil {
		return Config{}, fmt.Errorf("MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS: %w", err)
	}
	cfg.Execution = execution

	// Log System Levels Check
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, errors.New("MACHINARCHY_LOG_LEVEL must be debug, info, warn, or error")
	}

	// Connect Downstream Storage Optimization Filters
	if err := cfg.loadDatabaseTuning(get); err != nil {
		return Config{}, err
	}
	if err := cfg.validateDatabase(); err != nil {
		return Config{}, err
	}
	if requireLegacyBroker {
		if err := validateEventBrokerURL(cfg.EventBroker.URL); err != nil {
			return Config{}, err
		}
	}

	return cfg, nil
}

func validateEventBrokerURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("MACHINARCHY_BROKER_URL is required")
	}
	for _, endpoint := range strings.Split(raw, ",") {
		parsed, err := url.Parse(strings.TrimSpace(endpoint))
		if err != nil || (parsed.Scheme != "nats" && parsed.Scheme != "tls") || parsed.Hostname() == "" {
			return errors.New("MACHINARCHY_BROKER_URL must contain NATS endpoints using nats:// or tls:// with a host")
		}
	}
	return nil
}

// loadSSHTargetPolicy reads the destination policy applied to every SSH dial.
// Private networks are allowed by default because Machinarchy manages private
// infrastructure; loopback and link-local stay denied unless allowed explicitly.
func loadSSHTargetPolicy(get func(string, ...string) string) (netpolicy.Policy, error) {
	allowPrivate, err := strconv.ParseBool(get("MACHINARCHY_SSH_ALLOW_PRIVATE", "true"))
	if err != nil {
		return netpolicy.Policy{}, errors.New("MACHINARCHY_SSH_ALLOW_PRIVATE must be true or false")
	}
	allowLoopback, err := strconv.ParseBool(get("MACHINARCHY_SSH_ALLOW_LOOPBACK", "false"))
	if err != nil {
		return netpolicy.Policy{}, errors.New("MACHINARCHY_SSH_ALLOW_LOOPBACK must be true or false")
	}
	allow, err := netpolicy.ParsePrefixes(get("MACHINARCHY_SSH_ALLOWED_CIDRS"))
	if err != nil {
		return netpolicy.Policy{}, fmt.Errorf("MACHINARCHY_SSH_ALLOWED_CIDRS: %w", err)
	}
	deny, err := netpolicy.ParsePrefixes(get("MACHINARCHY_SSH_DENIED_CIDRS"))
	if err != nil {
		return netpolicy.Policy{}, fmt.Errorf("MACHINARCHY_SSH_DENIED_CIDRS: %w", err)
	}
	return netpolicy.Policy{AllowPrivate: allowPrivate, AllowLoopback: allowLoopback, Allow: allow, Deny: deny}, nil
}

// GetEnv retrieves the value of the environment variable named by the key. If the variable is present in the environment, its value is returned. Otherwise, it returns the provided default value if specified, or an empty string if no default is given.
func GetEnv(key string, defaultVal ...string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	if len(defaultVal) > 0 {
		return defaultVal[0]
	}
	return ""
}

func (c *Config) loadDatabaseTuning(get func(string, ...string) string) error {
	defaultOpen, defaultIdle := 1, 1
	lifetimeDefault := "0s" // SQLite default blocks connection drops to keep memory spaces isolated

	if c.Database.Engine == Postgres {
		defaultOpen, defaultIdle = 20, 5
		lifetimeDefault = "30m"
	}

	maxOpen, err := parseNumericBounds(get("MACHINARCHY_DATABASE_MAX_OPEN_CONNS", strconv.Itoa(defaultOpen)), "MACHINARCHY_DATABASE_MAX_OPEN_CONNS", 1, false)
	if err != nil {
		return err
	}

	maxIdle, err := parseNumericBounds(get("MACHINARCHY_DATABASE_MAX_IDLE_CONNS", strconv.Itoa(defaultIdle)), "MACHINARCHY_DATABASE_MAX_IDLE_CONNS", 0, true)
	if err != nil {
		return err
	}

	lifetime, err := parseDurationBounds(get("MACHINARCHY_DATABASE_CONN_MAX_LIFETIME", lifetimeDefault), "MACHINARCHY_DATABASE_CONN_MAX_LIFETIME", true)
	if err != nil {
		return err
	}

	connectTimeout, err := parseDurationBounds(get("MACHINARCHY_DATABASE_CONNECT_TIMEOUT", c.Database.ConnectTimeout.String()), "MACHINARCHY_DATABASE_CONNECT_TIMEOUT", false)
	if err != nil {
		return err
	}

	// Business rule constraint evaluation
	if maxIdle > maxOpen {
		return errors.New("MACHINARCHY_DATABASE_MAX_IDLE_CONNS must not exceed MACHINARCHY_DATABASE_MAX_OPEN_CONNS")
	}

	if c.Database.Engine == SQLite && (maxOpen != 1 || maxIdle != 1) {
		return errors.New("sqlite runtime requirements constraint: MAX_OPEN_CONNS and MAX_IDLE_CONNS must equal 1")
	}

	c.Database.MaxOpenConns = maxOpen
	c.Database.MaxIdleConns = maxIdle
	c.Database.ConnMaxLifetime = lifetime
	c.Database.ConnectTimeout = connectTimeout
	return nil
}

func (c *Config) validateDatabase() error {
	switch c.Database.Engine {
	case SQLite:
		if strings.TrimSpace(c.Database.Path) == "" {
			return errors.New("MACHINARCHY_DATABASE_PATH is required when using sqlite engines")
		}
	case Postgres:
		if strings.TrimSpace(c.Database.URL) == "" {
			return errors.New("MACHINARCHY_DATABASE_URL engine target parameters required when using postgres drivers")
		}
		u, err := url.Parse(c.Database.URL)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
			return errors.New("MACHINARCHY_DATABASE_URL must map out to a valid postgres:// or postgresql:// configuration target")
		}
		if c.Environment == Production && u.Query().Get("sslmode") != "verify-full" {
			return errors.New("MACHINARCHY_DATABASE_URL must set sslmode=verify-full in production")
		}
	default:
		return fmt.Errorf("MACHINARCHY_DATABASE_ENGINE must match values %q or %q", SQLite, Postgres)
	}
	return nil
}

// parseNumericBounds parses a string as an integer and checks if it falls within the specified bounds. It returns the parsed integer or an error if the value is invalid.
func parseNumericBounds(raw, key string, minValue int, allowZero bool) (int, error) {
	val, err := strconv.Atoi(raw)
	if err != nil || val < minValue {
		if allowZero && minValue == 0 {
			return 0, fmt.Errorf("%s must be a non-negative integer configurations constraint error", key)
		}
		return 0, fmt.Errorf("%s must evaluate as a positive integer above threshold parameters", key)
	}
	return val, nil
}

// Consolidated duration parser matching bounds restrictions
func parseDurationBounds(raw, key string, allowZero bool) (time.Duration, error) {
	val, err := time.ParseDuration(raw)
	if err != nil || (!allowZero && val <= 0) || (allowZero && val < 0) {
		label := "positive"
		if allowZero {
			label = "non-negative"
		}
		return 0, fmt.Errorf("%s must resolve into a valid %s duration format matching (e.g. 5s, 30m)", key, label)
	}
	return val, nil
}

func validateListenAddr(address string) error {
	_, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("MACHINARCHY_LISTEN_ADDR must map perfectly as a host:port value configuration blueprint (e.g., :8080)")
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("MACHINARCHY_LISTEN_ADDR port configuration out of structural boundaries [1-65535]")
	}
	return nil
}
