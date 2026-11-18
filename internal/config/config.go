// Package config provides configuration parsing and validation for the application.
package config

import (
	"encoding/base64"
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

	"github.com/nats-io/nkeys"

	"github.com/machinarchy/machinarchy/internal/domain"
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
	MetricsListenAddr      string // when set, /metrics is served only here, not on ListenAddr
	APIDocsEnabled         bool   // serve /openapi.json and /scalar
	Environment            Environment
	SignupEnabled          bool
	OperatorToken          string
	EncryptionSecret       string
	EncryptionKeyID        string
	EncryptionPreviousKeys map[string]string
	LogLevel               string
	ShutdownTimeout        time.Duration
	Database               DatabaseConfig
	BootstrapAdmin         BootstrapAdminConfig
	SSHTargetPolicy        netpolicy.Policy
	// SSHHostKeyPolicy is the host-key trust policy for Access Profiles that
	// do not set one: tofu, approve or pinned (AUDIT/10b §2).
	SSHHostKeyPolicy string
	Execution        domain.ExecutionPolicy
	AgentGateway     AgentGatewayConfig
}

// AgentGatewayConfig enables the controller side of the agent protocol. The
// gateway is off unless NATSURL is set.
type AgentGatewayConfig struct {
	NATSURL             string
	SigningKeySeed      []byte // 32-byte Ed25519 seed
	EnrollmentToken     string
	NATSCredentialsFile string
	NATSCAFile          string
	NATSCertFile        string
	NATSKeyFile         string
	LeaseTTL            time.Duration
	Heartbeat           time.Duration
	// NATSAccount and NATSAccountSigningKey enable per-agent NATS user JWTs
	// (operator mode, AUDIT M-10). Required in production.
	NATSAccount           string
	NATSAccountSigningKey []byte // nkey seed (SA...)
	AgentJWTTTL           time.Duration
}

// Enabled reports whether the gateway is configured.
func (c AgentGatewayConfig) Enabled() bool { return c.NATSURL != "" }

// BootstrapAdminConfig describes the optional first owner account. The account is
// only created when the default workspace has no owner, so leaving the password
// configured after the first start is harmless but unnecessary.
type BootstrapAdminConfig struct {
	Username string
	Email    string
	Password string
}

// Load reads the configuration from environment variables, applies defaults, and validates the configuration values. It returns a Config struct or an error if validation fails.
func Load() (Config, error) {
	return load()
}

// LoadWithOptions loads immutable bootstrap settings with precedence flags,
// environment, file, then defaults. Secret values are never accepted from
// flags or the configuration file.
type LoadOptions struct {
	FilePath string
	Flags    map[string]string
}

func LoadWithOptions(options LoadOptions) (Config, error) {
	fileValues := map[string]string{}
	if options.FilePath != "" {
		f, err := os.Open(options.FilePath)
		if err != nil {
			return Config{}, errors.New("unable to open bootstrap configuration file")
		}
		defer func() { _ = f.Close() }() // read-only: a close error cannot lose data
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
	return loadWithGetter(get)
}

// LoadCommandLine supports non-secret startup flags and a JSON bootstrap file.
func LoadCommandLine(args []string) (Config, error) {
	flags := map[string]string{}
	file := ""
	allowed := map[string]string{
		"--listen-addr":    "MACHINARCHY_LISTEN_ADDR",
		"--environment":    "MACHINARCHY_ENVIRONMENT",
		"--log-level":      "MACHINARCHY_LOG_LEVEL",
		"--signup-enabled": "MACHINARCHY_SIGNUP_ENABLED",

		"--metrics-listen-addr": "MACHINARCHY_METRICS_LISTEN_ADDR",
		"--api-docs-enabled":    "MACHINARCHY_API_DOCS_ENABLED",

		"--database-engine":            "MACHINARCHY_DATABASE_ENGINE",
		"--database-path":              "MACHINARCHY_DATABASE_PATH",
		"--database-url":               "MACHINARCHY_DATABASE_URL",
		"--database-max-open-conns":    "MACHINARCHY_DATABASE_MAX_OPEN_CONNS",
		"--database-max-idle-conns":    "MACHINARCHY_DATABASE_MAX_IDLE_CONNS",
		"--database-conn-max-lifetime": "MACHINARCHY_DATABASE_CONN_MAX_LIFETIME",
		"--database-connect-timeout":   "MACHINARCHY_DATABASE_CONNECT_TIMEOUT",

		"--encryption-key-id":       "MACHINARCHY_ENCRYPTION_KEY_ID",      // #nosec G101 -- key identifier, not key material
		"--encryption-secret-file":  "MACHINARCHY_ENCRYPTION_SECRET_FILE", // #nosec G101 -- configuration setting, not secret material
		"--encryption-keyring-file": "MACHINARCHY_ENCRYPTION_KEYRING_FILE",

		"--ssh-allow-private":          "MACHINARCHY_SSH_ALLOW_PRIVATE",
		"--ssh-allow-loopback":         "MACHINARCHY_SSH_ALLOW_LOOPBACK",
		"--ssh-allowed-cidrs":          "MACHINARCHY_SSH_ALLOWED_CIDRS",
		"--ssh-denied-cidrs":           "MACHINARCHY_SSH_DENIED_CIDRS",
		"--ssh-host-key-policy":        "MACHINARCHY_SSH_HOST_KEY_POLICY",
		"--execution-enabled":          "MACHINARCHY_EXECUTION_ENABLED",
		"--execution-allowed-programs": "MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS",
	}
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]

		if arg == "--config" {
			if len(args) == 0 {
				return Config{}, errors.New("--config requires a file path")
			}

			file = args[0]
			args = args[1:]
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
			if len(args) == 0 {
				return Config{}, fmt.Errorf("%s requires a value", name)
			}

			value = args[0]
			args = args[1:]
		}

		flags[envKey] = value
	}
	return LoadWithOptions(LoadOptions{FilePath: file, Flags: flags})
}

func load() (Config, error) {
	return loadWithGetter(GetEnv)
}

func loadWithGetter(get func(string, ...string) string) (Config, error) {
	cfg := Config{
		ListenAddr:    get("MACHINARCHY_LISTEN_ADDR", ":8080"),
		Environment:   Environment(strings.ToLower(get("MACHINARCHY_ENVIRONMENT", string(Production)))),
		OperatorToken: get("MACHINARCHY_OPERATOR_TOKEN"),
		LogLevel:      strings.ToLower(get("MACHINARCHY_LOG_LEVEL", "info")),
		Database: DatabaseConfig{
			Engine:         DatabaseEngine(strings.ToLower(get("MACHINARCHY_DATABASE_ENGINE", string(SQLite)))),
			Path:           get("MACHINARCHY_DATABASE_PATH", "./data/machinarchy.db"),
			URL:            get("MACHINARCHY_DATABASE_URL", ""),
			ConnectTimeout: 5 * time.Second,
		},
	}

	// Structural Domain Validation
	if err := cfg.Environment.Validate(); err != nil {
		return Config{}, err
	}

	// One budget for stopping every component (AUDIT Q-01). Keep it below the
	// orchestrator's grace period (Docker stop_grace_period, Kubernetes
	// terminationGracePeriodSeconds) so SIGKILL never lands mid-write.
	shutdownTimeout, err := parseDurationBounds(get("MACHINARCHY_SHUTDOWN_TIMEOUT", "25s"), "MACHINARCHY_SHUTDOWN_TIMEOUT", false)
	if err != nil {
		return Config{}, err
	}
	if shutdownTimeout < 5*time.Second || shutdownTimeout > 10*time.Minute {
		return Config{}, errors.New("MACHINARCHY_SHUTDOWN_TIMEOUT must be between 5s and 10m")
	}
	cfg.ShutdownTimeout = shutdownTimeout

	// Secure by default (AUDIT S-03): the environment defaults to production
	// (secure cookies, mandatory CORS origins and database TLS) and public
	// self-registration is off everywhere. Every new account joins the shared
	// workspace as a reader, which exposes the whole inventory, so signup must
	// be an explicit decision even in development.
	signupEnabled, err := strconv.ParseBool(get("MACHINARCHY_SIGNUP_ENABLED", "false"))
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

	// Operational endpoints on the public listener (AUDIT S-17). The API
	// reference loads a third-party script and the spec maps every route, so
	// both are off in production unless enabled. Metrics can move to their own
	// listener so the API port exposes nothing unauthenticated but health.
	apiDocsDefault := "false"
	if cfg.Environment == Development {
		apiDocsDefault = "true"
	}
	apiDocsEnabled, err := strconv.ParseBool(get("MACHINARCHY_API_DOCS_ENABLED", apiDocsDefault))
	if err != nil {
		return Config{}, errors.New("MACHINARCHY_API_DOCS_ENABLED must be true or false")
	}
	cfg.APIDocsEnabled = apiDocsEnabled
	cfg.MetricsListenAddr = get("MACHINARCHY_METRICS_LISTEN_ADDR", "")
	if cfg.MetricsListenAddr != "" {
		if err := validateMetricsListenAddr(cfg.MetricsListenAddr, cfg.ListenAddr); err != nil {
			return Config{}, err
		}
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
		data, err := readSecretFile(keyFile, "MACHINARCHY_ENCRYPTION_SECRET_FILE")
		if err != nil {
			return Config{}, err
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
		raw, err := readSecretFile(ringFile, "MACHINARCHY_ENCRYPTION_KEYRING_FILE")
		if err != nil {
			return Config{}, err
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
	// Development pins the keys seen on first contact; production waits for
	// an owner to approve them. ca needs per-profile keys, so it is not a
	// deployment default.
	hostKeyDefault := "approve"
	if cfg.Environment == Development {
		hostKeyDefault = "tofu"
	}
	cfg.SSHHostKeyPolicy = strings.ToLower(get("MACHINARCHY_SSH_HOST_KEY_POLICY", hostKeyDefault))
	switch cfg.SSHHostKeyPolicy {
	case "tofu", "approve", "pinned":
	default:
		return Config{}, errors.New("MACHINARCHY_SSH_HOST_KEY_POLICY must be tofu, approve or pinned")
	}

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

	if cfg.AgentGateway, err = loadAgentGateway(get, cfg.Environment); err != nil {
		return Config{}, err
	}

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
	return cfg, nil
}

// loadSSHTargetPolicy reads the destination policy applied to every SSH dial.
// Destination access is deny-by-default. Operators explicitly enable private
// ranges or configure permitted CIDRs for the deployment.
func loadSSHTargetPolicy(get func(string, ...string) string) (netpolicy.Policy, error) {
	allowPrivate, err := strconv.ParseBool(get("MACHINARCHY_SSH_ALLOW_PRIVATE", "false"))
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
	ports, err := parseAllowedPorts(get("MACHINARCHY_SSH_ALLOWED_PORTS"))
	if err != nil {
		return netpolicy.Policy{}, fmt.Errorf("MACHINARCHY_SSH_ALLOWED_PORTS: %w", err)
	}
	return netpolicy.Policy{AllowPrivate: allowPrivate, AllowLoopback: allowLoopback, Allow: allow, Deny: deny, AllowedPorts: ports}, nil
}

func parseAllowedPorts(value string) ([]uint16, error) {
	var ports []uint16
	seen := make(map[uint16]struct{})
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		port, err := strconv.Atoi(item)
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("ports must be integers from 1 through 65535")
		}
		parsed := uint16(port)
		if _, exists := seen[parsed]; exists {
			return nil, errors.New("ports must be unique")
		}
		seen[parsed] = struct{}{}
		ports = append(ports, parsed)
	}
	return ports, nil
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

func validateMetricsListenAddr(address, apiAddress string) error {
	_, rawPort, err := net.SplitHostPort(address)
	port, convErr := strconv.Atoi(rawPort)
	if err != nil || convErr != nil || port < 1 || port > 65535 {
		return errors.New("MACHINARCHY_METRICS_LISTEN_ADDR must be host:port with a port in 1-65535 (e.g. :9090)")
	}
	// apiAddress was validated before this is called.
	if _, apiPort, err := net.SplitHostPort(apiAddress); err == nil && rawPort == apiPort {
		return errors.New("MACHINARCHY_METRICS_LISTEN_ADDR must use a different port from MACHINARCHY_LISTEN_ADDR")
	}
	return nil
}

// readSecretFile reads key material from an operator-provided file, which must
// be a regular file (not a symlink) readable by its owner only (AUDIT X-14).
func readSecretFile(path, setting string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must be a regular file with permissions 0600 or stricter", setting)
	}
	// #nosec G304 -- the path is operator configuration, checked above.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("unable to read the file named by %s", setting)
	}
	return data, nil
}

// loadAgentGateway reads MACHINARCHY_AGENT_GATEWAY_*. All key material comes
// from 0600 files. Production requires TLS to NATS (a tls:// URL or a CA file).
func loadAgentGateway(get func(string, ...string) string, environment Environment) (AgentGatewayConfig, error) {
	cfg := AgentGatewayConfig{NATSURL: strings.TrimSpace(get("MACHINARCHY_AGENT_GATEWAY_NATS_URL"))}
	if cfg.NATSURL == "" {
		return cfg, nil
	}
	parsed, err := url.Parse(cfg.NATSURL)
	if err != nil || (parsed.Scheme != "nats" && parsed.Scheme != "tls") || parsed.Host == "" {
		return cfg, errors.New("MACHINARCHY_AGENT_GATEWAY_NATS_URL must be a nats:// or tls:// URL")
	}
	if parsed.User != nil {
		return cfg, errors.New("MACHINARCHY_AGENT_GATEWAY_NATS_URL must not embed credentials; use MACHINARCHY_AGENT_GATEWAY_NATS_CREDENTIALS_FILE")
	}
	cfg.NATSCredentialsFile = strings.TrimSpace(get("MACHINARCHY_AGENT_GATEWAY_NATS_CREDENTIALS_FILE"))
	cfg.NATSCAFile = strings.TrimSpace(get("MACHINARCHY_AGENT_GATEWAY_NATS_CA_FILE"))
	cfg.NATSCertFile = strings.TrimSpace(get("MACHINARCHY_AGENT_GATEWAY_NATS_CERT_FILE"))
	cfg.NATSKeyFile = strings.TrimSpace(get("MACHINARCHY_AGENT_GATEWAY_NATS_KEY_FILE"))
	if (cfg.NATSCertFile == "") != (cfg.NATSKeyFile == "") {
		return cfg, errors.New("MACHINARCHY_AGENT_GATEWAY_NATS_CERT_FILE and _KEY_FILE must be set together")
	}
	if environment == Production && parsed.Scheme != "tls" && cfg.NATSCAFile == "" {
		return cfg, errors.New("production requires TLS to NATS: use a tls:// URL or set MACHINARCHY_AGENT_GATEWAY_NATS_CA_FILE")
	}
	keyFile := strings.TrimSpace(get("MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE"))
	if keyFile == "" {
		return cfg, errors.New("MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE is required when the agent gateway is enabled")
	}
	raw, err := readSecretFile(keyFile, "MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE")
	if err != nil {
		return cfg, err
	}
	if cfg.SigningKeySeed, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw))); err != nil || len(cfg.SigningKeySeed) != 32 {
		return cfg, errors.New("MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE must hold a base64-encoded 32-byte Ed25519 seed (openssl rand -base64 32)")
	}
	// Hosts enrol with owner-issued, single-use tokens (POST
	// /api/v1/agents/enrollment-tokens). A shared token accepted from any host
	// remains for local development only (AUDIT M-4).
	if tokenFile := strings.TrimSpace(get("MACHINARCHY_AGENT_GATEWAY_ENROLLMENT_TOKEN_FILE")); tokenFile != "" {
		if environment == Production {
			return cfg, errors.New("MACHINARCHY_AGENT_GATEWAY_ENROLLMENT_TOKEN_FILE (a shared token) is for development only; issue per-host tokens with POST /api/v1/agents/enrollment-tokens")
		}
		token, err := readSecretFile(tokenFile, "MACHINARCHY_AGENT_GATEWAY_ENROLLMENT_TOKEN_FILE")
		if err != nil {
			return cfg, err
		}
		cfg.EnrollmentToken = strings.TrimSpace(string(token))
		if len(cfg.EnrollmentToken) < 32 || len(cfg.EnrollmentToken) > 256 || strings.ContainsAny(cfg.EnrollmentToken, " \t\r\n") {
			return cfg, errors.New("the agent enrollment token must be 32 to 256 characters without whitespace")
		}
	}
	if cfg.LeaseTTL, err = parseDurationBounds(get("MACHINARCHY_AGENT_GATEWAY_LEASE_TTL", "60s"), "MACHINARCHY_AGENT_GATEWAY_LEASE_TTL", false); err != nil {
		return cfg, err
	}
	if cfg.Heartbeat, err = parseDurationBounds(get("MACHINARCHY_AGENT_GATEWAY_HEARTBEAT", "15s"), "MACHINARCHY_AGENT_GATEWAY_HEARTBEAT", false); err != nil {
		return cfg, err
	}
	if cfg.Heartbeat < 5*time.Second || cfg.Heartbeat*2 > cfg.LeaseTTL {
		return cfg, errors.New("MACHINARCHY_AGENT_GATEWAY_HEARTBEAT must be at least 5s and at most half the lease TTL")
	}
	if err := loadAgentNATSIssuer(&cfg, get, environment); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// loadAgentNATSIssuer reads the NATS account settings that let the gateway
// issue each agent its own NATS user JWT. Without them every agent shares the
// bootstrap credential's permissions, so production requires them.
func loadAgentNATSIssuer(cfg *AgentGatewayConfig, get func(string, ...string) string, environment Environment) error {
	cfg.NATSAccount = strings.TrimSpace(get("MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT"))
	keyFile := strings.TrimSpace(get("MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE"))
	if cfg.NATSAccount == "" && keyFile == "" {
		if environment == Production {
			return errors.New("production requires per-agent NATS credentials: set MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT and _NATS_ACCOUNT_SIGNING_KEY_FILE (see docs/operations.md#agent-gateway)")
		}
		return nil
	}
	if cfg.NATSAccount == "" || keyFile == "" {
		return errors.New("MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT and _NATS_ACCOUNT_SIGNING_KEY_FILE must be set together")
	}
	if !nkeys.IsValidPublicAccountKey(cfg.NATSAccount) {
		return errors.New("MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT must be a NATS account public key (starting with A)")
	}
	seed, err := readSecretFile(keyFile, "MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE")
	if err != nil {
		return err
	}
	seed = []byte(strings.TrimSpace(string(seed)))
	signer, err := nkeys.FromSeed(seed)
	if err != nil {
		return errors.New("MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE must hold an account nkey seed (starting with SA)")
	}
	if public, err := signer.PublicKey(); err != nil || !nkeys.IsValidPublicAccountKey(public) {
		return errors.New("MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE must hold an account nkey seed (starting with SA)")
	}
	signer.Wipe()
	cfg.NATSAccountSigningKey = seed
	if cfg.AgentJWTTTL, err = parseDurationBounds(get("MACHINARCHY_AGENT_GATEWAY_AGENT_JWT_TTL", "1h"), "MACHINARCHY_AGENT_GATEWAY_AGENT_JWT_TTL", false); err != nil {
		return err
	}
	if cfg.AgentJWTTTL < 5*time.Minute || cfg.AgentJWTTTL > 24*time.Hour || cfg.AgentJWTTTL < 2*cfg.Heartbeat {
		return errors.New("MACHINARCHY_AGENT_GATEWAY_AGENT_JWT_TTL must be between 5m and 24h and at least twice the heartbeat")
	}
	return nil
}
