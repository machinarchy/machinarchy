// internal/config/config.go
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

type Environment string

const (
	Development Environment = "development"
	Production  Environment = "production"
)

func (e Environment) Validate() error {
	switch e {
	case Development, Production:
		return nil
	default:
		return fmt.Errorf("MACHINARCHY_ENVIRONMENT must be 'development' or 'production' (got: %q)", e)
	}
}

type DatabaseEngine string

const (
	SQLite   DatabaseEngine = "sqlite"
	Postgres DatabaseEngine = "postgres"
)

func (de DatabaseEngine) Validate() error {
	switch de {
	case SQLite, Postgres:
		return nil
	default:
		return fmt.Errorf("MACHINARCHY_DATABASE_ENGINE must be 'sqlite' or 'postgres' (got: %q)", de)
	}
}

type DatabaseConfig struct {
	Engine          DatabaseEngine
	Path            string
	URL             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnectTimeout  time.Duration
}

type Config struct {
	ListenAddr       string
	Environment      Environment
	SignupEnabled    bool
	OperatorToken    string
	EncryptionSecret string
	LogLevel         string
	Database         DatabaseConfig
	EventBroker      EventBrokerConfig
	BootstrapAdmin   BootstrapAdminConfig
	SSHTargetPolicy  netpolicy.Policy
}

// BootstrapAdminConfig describes the optional first owner account. The account is
// only created when the default workspace has no owner, so leaving the password
// configured after the first start is harmless but unnecessary.
type BootstrapAdminConfig struct {
	Username string
	Email    string
	Password string
}

type EventBrokerConfig struct {
	URL string
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddr:    GetEnv("MACHINARCHY_LISTEN_ADDR", ":8080"),
		Environment:   Environment(strings.ToLower(GetEnv("MACHINARCHY_ENVIRONMENT", string(Development)))),
		OperatorToken: GetEnv("MACHINARCHY_OPERATOR_TOKEN"),
		LogLevel:      strings.ToLower(GetEnv("MACHINARCHY_LOG_LEVEL", "info")),
		Database: DatabaseConfig{
			Engine:         DatabaseEngine(strings.ToLower(GetEnv("MACHINARCHY_DATABASE_ENGINE", string(SQLite)))),
			Path:           GetEnv("MACHINARCHY_DATABASE_PATH", "./data/machinarchy.db"),
			URL:            GetEnv("MACHINARCHY_DATABASE_URL", ""),
			ConnectTimeout: 5 * time.Second,
		},
		EventBroker: EventBrokerConfig{
			URL: GetEnv("MACHINARCHY_BROKER_URL", "nats://localhost:4222"),
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
	signupEnabled, err := strconv.ParseBool(GetEnv("MACHINARCHY_SIGNUP_ENABLED", signupDefault))
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
	if cfg.EncryptionSecret == "" {
		if cfg.Environment == Production {
			return Config{}, errors.New("MACHINARCHY_ENCRYPTION_SECRET is required in production and must be managed independently of the JWT signing secret")
		}
		cfg.EncryptionSecret = cfg.OperatorToken
	}
	if len([]byte(cfg.EncryptionSecret)) < 32 || strings.ContainsAny(cfg.EncryptionSecret, " \t\r\n") {
		return Config{}, errors.New("MACHINARCHY_ENCRYPTION_SECRET must be at least 32 bytes with no whitespace")
	}

	cfg.BootstrapAdmin = BootstrapAdminConfig{
		Username: strings.ToLower(GetEnv("MACHINARCHY_BOOTSTRAP_ADMIN_USERNAME", "admin")),
		Email:    strings.ToLower(GetEnv("MACHINARCHY_BOOTSTRAP_ADMIN_EMAIL")),
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

	targetPolicy, err := loadSSHTargetPolicy()
	if err != nil {
		return Config{}, err
	}
	cfg.SSHTargetPolicy = targetPolicy

	// Log System Levels Check
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, errors.New("MACHINARCHY_LOG_LEVEL must be debug, info, warn, or error")
	}

	// Connect Downstream Storage Optimization Filters
	if err := cfg.loadDatabaseTuning(); err != nil {
		return Config{}, err
	}
	if err := cfg.validateDatabase(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// loadSSHTargetPolicy reads the destination policy applied to every SSH dial.
// Private networks are allowed by default because Machinarchy manages private
// infrastructure; loopback and link-local stay denied unless allowed explicitly.
func loadSSHTargetPolicy() (netpolicy.Policy, error) {
	allowPrivate, err := strconv.ParseBool(GetEnv("MACHINARCHY_SSH_ALLOW_PRIVATE", "true"))
	if err != nil {
		return netpolicy.Policy{}, errors.New("MACHINARCHY_SSH_ALLOW_PRIVATE must be true or false")
	}
	allowLoopback, err := strconv.ParseBool(GetEnv("MACHINARCHY_SSH_ALLOW_LOOPBACK", "false"))
	if err != nil {
		return netpolicy.Policy{}, errors.New("MACHINARCHY_SSH_ALLOW_LOOPBACK must be true or false")
	}
	allow, err := netpolicy.ParsePrefixes(GetEnv("MACHINARCHY_SSH_ALLOWED_CIDRS"))
	if err != nil {
		return netpolicy.Policy{}, fmt.Errorf("MACHINARCHY_SSH_ALLOWED_CIDRS: %w", err)
	}
	deny, err := netpolicy.ParsePrefixes(GetEnv("MACHINARCHY_SSH_DENIED_CIDRS"))
	if err != nil {
		return netpolicy.Policy{}, fmt.Errorf("MACHINARCHY_SSH_DENIED_CIDRS: %w", err)
	}
	return netpolicy.Policy{AllowPrivate: allowPrivate, AllowLoopback: allowLoopback, Allow: allow, Deny: deny}, nil
}

func GetEnv(key string, defaultVal ...string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	if len(defaultVal) > 0 {
		return defaultVal[0]
	}
	return ""
}

func (c *Config) loadDatabaseTuning() error {
	defaultOpen, defaultIdle := 1, 1
	lifetimeDefault := "0s" // SQLite default blocks connection drops to keep memory spaces isolated

	if c.Database.Engine == Postgres {
		defaultOpen, defaultIdle = 20, 5
		lifetimeDefault = "30m"
	}

	maxOpen, err := parseNumericBounds(GetEnv("MACHINARCHY_DATABASE_MAX_OPEN_CONNS", strconv.Itoa(defaultOpen)), "MACHINARCHY_DATABASE_MAX_OPEN_CONNS", 1, false)
	if err != nil {
		return err
	}

	maxIdle, err := parseNumericBounds(GetEnv("MACHINARCHY_DATABASE_MAX_IDLE_CONNS", strconv.Itoa(defaultIdle)), "MACHINARCHY_DATABASE_MAX_IDLE_CONNS", 0, true)
	if err != nil {
		return err
	}

	lifetime, err := parseDurationBounds(GetEnv("MACHINARCHY_DATABASE_CONN_MAX_LIFETIME", lifetimeDefault), "MACHINARCHY_DATABASE_CONN_MAX_LIFETIME", true)
	if err != nil {
		return err
	}

	connectTimeout, err := parseDurationBounds(GetEnv("MACHINARCHY_DATABASE_CONNECT_TIMEOUT", c.Database.ConnectTimeout.String()), "MACHINARCHY_DATABASE_CONNECT_TIMEOUT", false)
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

// Consolidated numeric boundary validator parsing functions
func parseNumericBounds(raw, key string, min int, allowZero bool) (int, error) {
	val, err := strconv.Atoi(raw)
	if err != nil || val < min {
		if allowZero && min == 0 {
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
