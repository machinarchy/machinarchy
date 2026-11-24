package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"

	"github.com/nats-io/nkeys"
)

// setCommonEnv provisions the baseline test environment variables cleanly using a loop.
func setCommonEnv(t *testing.T) {
	t.Helper()
	envs := map[string]string{
		"MACHINARCHY_LISTEN_ADDR":                    "",
		"MACHINARCHY_ENVIRONMENT":                    "development",
		"MACHINARCHY_SIGNUP_ENABLED":                 "",
		"MACHINARCHY_API_DOCS_ENABLED":               "",
		"MACHINARCHY_METRICS_LISTEN_ADDR":            "",
		"MACHINARCHY_SSH_HOST_KEY_POLICY":            "",
		"MACHINARCHY_DATABASE_ENGINE":                "sqlite",
		"MACHINARCHY_DATABASE_PATH":                  "",
		"MACHINARCHY_DATABASE_URL":                   "",
		"MACHINARCHY_DATABASE_MAX_OPEN_CONNS":        "",
		"MACHINARCHY_DATABASE_MAX_IDLE_CONNS":        "",
		"MACHINARCHY_DATABASE_CONN_MAX_LIFETIME":     "",
		"MACHINARCHY_DATABASE_CONNECT_TIMEOUT":       "",
		"MACHINARCHY_DATABASE_MIGRATION_TIMEOUT":     "",
		"MACHINARCHY_SESSION_TTL":                    "",
		"MACHINARCHY_SESSION_PREVIOUS_KEYS":          "",
		"MACHINARCHY_LOGIN_FREE_FAILURES":            "",
		"MACHINARCHY_LOGIN_MAX_BACKOFF":              "",
		"MACHINARCHY_LOGIN_IP_MAX_FAILURES":          "",
		"MACHINARCHY_LOGIN_FAILURE_WINDOW":           "",
		"MACHINARCHY_CORS_ALLOWED_ORIGINS":           "",
		"CORS_ALLOWED_ORIGINS":                       "",
		"MACHINARCHY_TRUSTED_PROXIES":                "",
		"MACHINARCHY_RATE_LIMIT_RPS":                 "",
		"MACHINARCHY_RATE_LIMIT_BURST":               "",
		"MACHINARCHY_HTTP_MAX_BODY_BYTES":            "",
		"MACHINARCHY_HTTP_REQUEST_TIMEOUT":           "",
		"MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE": "",
		"MACHINARCHY_KMS_PROVIDER":                   "",
		"MACHINARCHY_OPERATOR_TOKEN":                 "0123456789abcdef0123456789abcdef",
		"MACHINARCHY_ENCRYPTION_SECRET":              "abcdef0123456789abcdef0123456789",
		"MACHINARCHY_LOG_LEVEL":                      "",
	}

	for key, value := range envs {
		t.Setenv(key, value)
	}
}

func TestBootstrapSourcesPrecedenceAndSecretRejection(t *testing.T) {
	setCommonEnv(t)
	path := filepath.Join(t.TempDir(), "bootstrap.json")
	if err := os.WriteFile(path, []byte(`{"MACHINARCHY_LISTEN_ADDR":":9000","MACHINARCHY_LOG_LEVEL":"warn"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACHINARCHY_LISTEN_ADDR", ":7000")
	cfg, err := LoadCommandLine([]string{"--config", path, "--listen-addr", ":6000"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":6000" || cfg.LogLevel != "warn" {
		t.Fatalf("precedence result=%+v", cfg)
	}
	if err = os.WriteFile(path, []byte(`{"MACHINARCHY_OPERATOR_TOKEN":"0123456789abcdef0123456789abcdef"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadWithOptions(LoadOptions{FilePath: path}); err == nil {
		t.Fatal("bootstrap file accepted secret material")
	}
}

func TestEncryptionKeyMustBeIndependentAndFileProtected(t *testing.T) {
	setCommonEnv(t)
	t.Setenv("MACHINARCHY_ENCRYPTION_SECRET", "")
	t.Setenv("MACHINARCHY_ENCRYPTION_SECRET_FILE", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "independently") {
		t.Fatalf("missing external encryption key err=%v", err)
	}
	path := filepath.Join(t.TempDir(), "root.key")
	if err := os.WriteFile(path, []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACHINARCHY_ENCRYPTION_SECRET_FILE", path)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSignupDefaults(t *testing.T) {
	t.Run("disabled in development", func(t *testing.T) {
		setCommonEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.SignupEnabled {
			t.Fatal("signup must be opt-in in every environment (AUDIT S-03)")
		}
	})

	t.Run("environment defaults to production", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_ENVIRONMENT", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Environment != Production || cfg.SignupEnabled {
			t.Fatalf("unset environment gave %q (signup=%v), want production with signup disabled", cfg.Environment, cfg.SignupEnabled)
		}
	})

	t.Run("disabled in production", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_ENVIRONMENT", "production")
		t.Setenv("MACHINARCHY_ENCRYPTION_SECRET", "abcdef0123456789abcdef0123456789")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.SignupEnabled {
			t.Fatal("signup should default to disabled in production")
		}
	})

	t.Run("explicit override", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_ENVIRONMENT", "production")
		t.Setenv("MACHINARCHY_ENCRYPTION_SECRET", "abcdef0123456789abcdef0123456789")
		t.Setenv("MACHINARCHY_SIGNUP_ENABLED", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !cfg.SignupEnabled {
			t.Fatal("explicit signup setting was ignored")
		}
	})

	t.Run("reject invalid value", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_SIGNUP_ENABLED", "sometimes")
		if _, err := Load(); err == nil {
			t.Fatal("expected an invalid signup setting to be rejected")
		}
	})
}

func TestLoadSQLiteDefaults(t *testing.T) {
	setCommonEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("failed to load SQLite defaults: %v", err)
	}

	if cfg.ListenAddr != ":8080" || cfg.Database.Engine != SQLite || cfg.Database.Path != "./data/machinarchy.db" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadPostgresRequirements(t *testing.T) {
	t.Run("missing URL path is rejected", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_DATABASE_ENGINE", "postgres")

		if _, err := Load(); err == nil {
			t.Fatal("expected missing PostgreSQL URL to be rejected")
		}
	})

	t.Run("valid URL path satisfies configuration requirements", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_DATABASE_ENGINE", "postgres")
		t.Setenv("MACHINARCHY_DATABASE_URL", "postgres://machinarchy:secret@localhost:5432/machinarchy?sslmode=disable")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("failed to load valid Postgres config: %v", err)
		}

		if cfg.Database.Engine != Postgres || cfg.Database.MaxOpenConns != 20 || cfg.Database.MaxIdleConns != 5 {
			t.Fatalf("unexpected PostgreSQL config: %+v", cfg.Database)
		}
	})
}

// TestLoadIgnoresLegacyBrokerSettings proves no process requires NATS any
// more (AUDIT A-04/R-05): a leftover or malformed MACHINARCHY_BROKER_URL must
// not block startup, and the removed --broker-url flag is rejected.
func TestLoadIgnoresLegacyBrokerSettings(t *testing.T) {
	setCommonEnv(t)
	t.Setenv("MACHINARCHY_BROKER_URL", "not-a-nats-url")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v; the API must not depend on NATS", err)
	}
	if _, err := LoadCommandLine([]string{"--broker-url", "nats://x:4222"}); err == nil {
		t.Fatal("removed --broker-url flag was accepted")
	}
}

func TestLoadOperatorTokenValidation(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"weak token size", "short"},
		{"empty string value", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setCommonEnv(t)
			t.Setenv("MACHINARCHY_OPERATOR_TOKEN", tt.token)

			if _, err := Load(); err == nil {
				t.Fatalf("expected token value %q to be rejected", tt.token)
			}
		})
	}
}

func TestLoadValidationRules(t *testing.T) {
	t.Run("rejects SQLite multi-connection pool", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_DATABASE_MAX_OPEN_CONNS", "2")
		if _, err := Load(); err == nil {
			t.Fatal("expected SQLite multi-connection pool to be rejected")
		}
	})

	t.Run("rejects unknown environment names", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_ENVIRONMENT", "prod")
		if _, err := Load(); err == nil {
			t.Fatal("expected unknown environment to be rejected")
		}
	})

	t.Run("rejects invalid listen address string structures", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_LISTEN_ADDR", "localhost:not-a-port")
		if _, err := Load(); err == nil {
			t.Fatal("expected invalid listen address to be rejected")
		}
	})

	t.Run("rejects zero-value idle configurations for SQLite", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_DATABASE_MAX_IDLE_CONNS", "0")
		if _, err := Load(); err == nil {
			t.Fatal("expected SQLite idle connection setting to be rejected")
		}
	})
}

func TestSQLiteConnectionLifetimeDefaultsToDisabled(t *testing.T) {
	setCommonEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("failed to load configuration: %v", err)
	}

	if cfg.Database.ConnMaxLifetime != 0 {
		t.Fatalf("SQLite connection lifetime = %s, want 0s", cfg.Database.ConnMaxLifetime)
	}
}

func TestLoadBootstrapAdmin(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.BootstrapAdmin.Password != "" {
			t.Fatal("a bootstrap password must never have a built-in default")
		}
	})

	t.Run("configured", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_BOOTSTRAP_ADMIN_USERNAME", " Root ")
		t.Setenv("MACHINARCHY_BOOTSTRAP_ADMIN_EMAIL", "Root@Example.test")
		t.Setenv("MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD", "a-long-test-password")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.BootstrapAdmin.Username != "root" || cfg.BootstrapAdmin.Email != "root@example.test" {
			t.Fatalf("bootstrap identity not normalized: %+v", cfg.BootstrapAdmin)
		}
	})

	t.Run("rejects a short password", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD", "short")
		if _, err := Load(); err == nil {
			t.Fatal("Load() accepted a bootstrap password shorter than 12 bytes")
		}
	})
}

func TestLoadSSHTargetPolicy(t *testing.T) {
	t.Run("defaults deny private and loopback networks", func(t *testing.T) {
		setCommonEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		policy := cfg.SSHTargetPolicy
		if policy.AllowPrivate || policy.AllowLoopback || len(policy.Allow) != 0 || len(policy.Deny) != 0 || len(policy.AllowedPorts) != 0 {
			t.Fatalf("unexpected default policy: %+v", policy)
		}
	})

	t.Run("explicit configuration", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_SSH_ALLOW_PRIVATE", "false")
		t.Setenv("MACHINARCHY_SSH_ALLOW_LOOPBACK", "true")
		t.Setenv("MACHINARCHY_SSH_ALLOWED_CIDRS", "10.20.0.0/16, 192.0.2.7")
		t.Setenv("MACHINARCHY_SSH_DENIED_CIDRS", "10.20.99.0/24")
		t.Setenv("MACHINARCHY_SSH_ALLOWED_PORTS", "22,2222")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		policy := cfg.SSHTargetPolicy
		if policy.AllowPrivate || !policy.AllowLoopback || len(policy.Allow) != 2 || len(policy.Deny) != 1 || len(policy.AllowedPorts) != 2 || policy.AllowedPorts[0] != 22 || policy.AllowedPorts[1] != 2222 {
			t.Fatalf("unexpected policy: %+v", policy)
		}
	})

	t.Run("rejects invalid values", func(t *testing.T) {
		for key, value := range map[string]string{
			"MACHINARCHY_SSH_ALLOW_PRIVATE":  "maybe",
			"MACHINARCHY_SSH_ALLOW_LOOPBACK": "sometimes",
			"MACHINARCHY_SSH_ALLOWED_CIDRS":  "10.0.0.0/8,banana",
			"MACHINARCHY_SSH_DENIED_CIDRS":   "300.1.1.1/8",
			"MACHINARCHY_SSH_ALLOWED_PORTS":  "22,65536",
		} {
			t.Run(key, func(t *testing.T) {
				setCommonEnv(t)
				t.Setenv(key, value)
				if _, err := Load(); err == nil {
					t.Errorf("Load() accepted %s=%q", key, value)
				}
			})
		}
	})
}

func TestLoadExecutionPolicy(t *testing.T) {
	t.Run("enabled with an explicit program list", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_EXECUTION_ENABLED", "true")
		t.Setenv("MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS", "uptime, /usr/bin/df -h {path}, systemctl is-active {unit}")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Execution.Check([]string{"uptime"}) != nil || cfg.Execution.Check([]string{"/usr/bin/df", "-h", "/"}) != nil || cfg.Execution.Check([]string{"systemctl", "is-active", "nginx"}) != nil ||
			cfg.Execution.Check([]string{"systemctl", "stop", "sshd"}) == nil || cfg.Execution.Check([]string{"uptime", "-p"}) == nil || cfg.Execution.Check([]string{"rm"}) == nil {
			t.Errorf("unexpected policy: %+v", cfg.Execution)
		}
	})

	t.Run("allowing every program must be explicit", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_EXECUTION_ENABLED", "true")
		t.Setenv("MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS", "*")
		cfg, err := Load()
		if err != nil || !cfg.Execution.AllowAny {
			t.Fatalf("Load() = %+v, %v", cfg.Execution, err)
		}
	})
}

// TestLoadShutdownTimeout covers AUDIT Q-01: one shutdown budget, defaulting
// below Kubernetes' 30 s grace period and bounded to sane values.
func TestLoadShutdownTimeout(t *testing.T) {
	setCommonEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ShutdownTimeout != 25*time.Second {
		t.Fatalf("default ShutdownTimeout = %s, want 25s", cfg.ShutdownTimeout)
	}
	t.Setenv("MACHINARCHY_SHUTDOWN_TIMEOUT", "40s")
	if cfg, err = Load(); err != nil || cfg.ShutdownTimeout != 40*time.Second {
		t.Fatalf("Load() = %s, %v; want 40s", cfg.ShutdownTimeout, err)
	}
	for _, bad := range []string{"1s", "0s", "-5s", "11m", "soon"} {
		t.Setenv("MACHINARCHY_SHUTDOWN_TIMEOUT", bad)
		if _, err := Load(); err == nil {
			t.Fatalf("MACHINARCHY_SHUTDOWN_TIMEOUT=%q accepted", bad)
		}
	}
}

// TestLoadAgentGateway covers the agent gateway settings: off by default,
// key material only from 0600 files, TLS required in production.
func TestLoadAgentGateway(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	seed := write("seed", base64.StdEncoding.EncodeToString(make([]byte, 32)), 0o600)
	token := write("token", strings.Repeat("t", 40), 0o600)
	open := write("open-seed", base64.StdEncoding.EncodeToString(make([]byte, 32)), 0o644)
	short := write("short-token", "short", 0o600)

	setCommonEnv(t)
	cfg, err := Load()
	if err != nil || cfg.AgentGateway.Enabled() {
		t.Fatalf("default = %+v, %v; want disabled", cfg.AgentGateway, err)
	}

	valid := map[string]string{
		"MACHINARCHY_AGENT_GATEWAY_NATS_URL":              "nats://127.0.0.1:4222",
		"MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE":      seed,
		"MACHINARCHY_AGENT_GATEWAY_ENROLLMENT_TOKEN_FILE": token,
	}
	for name, tc := range map[string]struct {
		override map[string]string
		ok       bool
	}{
		"valid":              {nil, true},
		"bad scheme":         {map[string]string{"MACHINARCHY_AGENT_GATEWAY_NATS_URL": "http://x:4222"}, false},
		"credentials in url": {map[string]string{"MACHINARCHY_AGENT_GATEWAY_NATS_URL": "nats://u:p@x:4222"}, false},
		"no key file":        {map[string]string{"MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE": ""}, true},
		"world-readable key": {map[string]string{"MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE": open}, false},
		"short token":        {map[string]string{"MACHINARCHY_AGENT_GATEWAY_ENROLLMENT_TOKEN_FILE": short}, false},
		"no shared token":    {map[string]string{"MACHINARCHY_AGENT_GATEWAY_ENROLLMENT_TOKEN_FILE": ""}, true},
		"heartbeat too long": {map[string]string{"MACHINARCHY_AGENT_GATEWAY_HEARTBEAT": "40s"}, false},
		"cert without key":   {map[string]string{"MACHINARCHY_AGENT_GATEWAY_NATS_CERT_FILE": "/c"}, false},
		"custom lease":       {map[string]string{"MACHINARCHY_AGENT_GATEWAY_LEASE_TTL": "2m", "MACHINARCHY_AGENT_GATEWAY_HEARTBEAT": "30s"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			setCommonEnv(t)
			for k, v := range valid {
				t.Setenv(k, v)
			}
			for k, v := range tc.override {
				t.Setenv(k, v)
			}
			cfg, err := Load()
			if (err == nil) != tc.ok {
				t.Fatalf("Load() error = %v, want ok=%v", err, tc.ok)
			}
			// The key file is optional since Phase 16 (imported once).
			wantSeed := 32
			if v, set := tc.override["MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE"]; set && v == "" {
				wantSeed = 0
			}
			if tc.ok && (!cfg.AgentGateway.Enabled() || len(cfg.AgentGateway.SigningKeySeed) != wantSeed) {
				t.Fatalf("gateway config = %+v", cfg.AgentGateway)
			}
		})
	}

	t.Run("production requires TLS", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_ENVIRONMENT", "production")
		t.Setenv("MACHINARCHY_ENCRYPTION_SECRET", "abcdef0123456789abcdef0123456789")
		for k, v := range valid {
			t.Setenv(k, v)
		}
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TLS") {
			t.Fatalf("plain NATS in production: %v", err)
		}
		t.Setenv("MACHINARCHY_AGENT_GATEWAY_NATS_URL", "tls://nats.example:4222")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "development only") {
			t.Fatalf("shared enrolment token in production: %v", err)
		}
		t.Setenv("MACHINARCHY_AGENT_GATEWAY_ENROLLMENT_TOKEN_FILE", "")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "per-agent NATS credentials") {
			t.Fatalf("production without NATS account settings: %v", err)
		}
		account, signingSeed := natsAccountKeys(t)
		t.Setenv("MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT", account)
		t.Setenv("MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE", write("prod-signing", signingSeed, 0o600))
		cfg, err := Load()
		if err != nil {
			t.Fatalf("tls:// with NATS account settings in production: %v", err)
		}
		if cfg.AgentGateway.NATSAccount != account || cfg.AgentGateway.AgentJWTTTL != time.Hour {
			t.Fatalf("issuer settings = %q %s", cfg.AgentGateway.NATSAccount, cfg.AgentGateway.AgentJWTTTL)
		}
	})

	account, signingSeed := natsAccountKeys(t)
	user, err := nkeys.CreateUser()
	if err != nil {
		t.Fatal(err)
	}
	userSeed, _ := user.Seed()
	signing := write("signing", signingSeed, 0o600)
	for name, tc := range map[string]struct {
		override map[string]string
		ok       bool
	}{
		"issuer":                {map[string]string{"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT": account, "MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE": signing}, true},
		"account without key":   {map[string]string{"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT": account}, false},
		"key without account":   {map[string]string{"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE": signing}, false},
		"not an account key":    {map[string]string{"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT": "UABC", "MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE": signing}, false},
		"user seed as signer":   {map[string]string{"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT": account, "MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE": write("user-seed", string(userSeed), 0o600)}, false},
		"world-readable signer": {map[string]string{"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT": account, "MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE": write("open-signing", signingSeed, 0o644)}, false},
		"jwt ttl too short":     {map[string]string{"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT": account, "MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE": signing, "MACHINARCHY_AGENT_GATEWAY_AGENT_JWT_TTL": "1m"}, false},
		"jwt ttl too long":      {map[string]string{"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT": account, "MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE": signing, "MACHINARCHY_AGENT_GATEWAY_AGENT_JWT_TTL": "48h"}, false},
		"jwt ttl custom":        {map[string]string{"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT": account, "MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE": signing, "MACHINARCHY_AGENT_GATEWAY_AGENT_JWT_TTL": "10m"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			setCommonEnv(t)
			for k, v := range valid {
				t.Setenv(k, v)
			}
			for k, v := range tc.override {
				t.Setenv(k, v)
			}
			cfg, err := Load()
			if (err == nil) != tc.ok {
				t.Fatalf("Load() error = %v, want ok=%v", err, tc.ok)
			}
			if tc.ok && (cfg.AgentGateway.NATSAccount != account || len(cfg.AgentGateway.NATSAccountSigningKey) == 0) {
				t.Fatalf("issuer settings missing: %+v", cfg.AgentGateway)
			}
		})
	}
}

// natsAccountKeys returns an account public key and the seed of a second
// account key used as its signing key.
func natsAccountKeys(t *testing.T) (string, string) {
	t.Helper()
	account, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	public, _ := account.PublicKey()
	signer, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := signer.Seed()
	return public, string(seed)
}

func TestLoadOperationalEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name        string
		env         map[string]string
		wantDocs    bool
		wantMetrics string
		wantErr     string
	}{
		{name: "development serves docs", env: map[string]string{}, wantDocs: true},
		{name: "production hides docs", env: map[string]string{"MACHINARCHY_ENVIRONMENT": "production"}},
		{name: "production opt-in", env: map[string]string{"MACHINARCHY_ENVIRONMENT": "production", "MACHINARCHY_API_DOCS_ENABLED": "true"}, wantDocs: true},
		{name: "development opt-out", env: map[string]string{"MACHINARCHY_API_DOCS_ENABLED": "false"}},
		{name: "bad docs flag", env: map[string]string{"MACHINARCHY_API_DOCS_ENABLED": "maybe"}, wantErr: "MACHINARCHY_API_DOCS_ENABLED"},
		{name: "separate metrics listener", env: map[string]string{"MACHINARCHY_METRICS_LISTEN_ADDR": "127.0.0.1:9090"}, wantDocs: true, wantMetrics: "127.0.0.1:9090"},
		{name: "metrics on the API port", env: map[string]string{"MACHINARCHY_METRICS_LISTEN_ADDR": ":8080"}, wantErr: "different port"},
		{name: "metrics address without port", env: map[string]string{"MACHINARCHY_METRICS_LISTEN_ADDR": "localhost"}, wantErr: "MACHINARCHY_METRICS_LISTEN_ADDR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setCommonEnv(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			cfg, err := Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.APIDocsEnabled != tc.wantDocs || cfg.MetricsListenAddr != tc.wantMetrics {
				t.Fatalf("docs=%v metrics=%q, want %v %q", cfg.APIDocsEnabled, cfg.MetricsListenAddr, tc.wantDocs, tc.wantMetrics)
			}
		})
	}
}

func TestLoadSSHHostKeyPolicy(t *testing.T) {
	for name, tc := range map[string]struct {
		env     map[string]string
		want    string
		wantErr bool
	}{
		"development pins on first use": {map[string]string{}, "tofu", false},
		"production needs approval":     {map[string]string{"MACHINARCHY_ENVIRONMENT": "production"}, "approve", false},
		"explicit pinned":               {map[string]string{"MACHINARCHY_SSH_HOST_KEY_POLICY": "Pinned"}, "pinned", false},
		"ca is per profile only":        {map[string]string{"MACHINARCHY_SSH_HOST_KEY_POLICY": "ca"}, "", true},
		"unknown":                       {map[string]string{"MACHINARCHY_SSH_HOST_KEY_POLICY": "always"}, "", true},
	} {
		t.Run(name, func(t *testing.T) {
			setCommonEnv(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			cfg, err := Load()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && cfg.SSHHostKeyPolicy != tc.want {
				t.Fatalf("policy = %q, want %q", cfg.SSHHostKeyPolicy, tc.want)
			}
		})
	}
}

func TestLoadDatabaseMigrationTimeout(t *testing.T) {
	setCommonEnv(t)
	cfg, err := Load()
	if err != nil || cfg.Database.MigrationTimeout != 10*time.Minute || cfg.Database.ConnectTimeout != 5*time.Second {
		t.Fatalf("defaults: migration %v, connect %v, %v", cfg.Database.MigrationTimeout, cfg.Database.ConnectTimeout, err)
	}
	t.Setenv("MACHINARCHY_DATABASE_MIGRATION_TIMEOUT", "45m")
	if cfg, err = Load(); err != nil || cfg.Database.MigrationTimeout != 45*time.Minute {
		t.Fatalf("override: %v, %v", cfg.Database.MigrationTimeout, err)
	}
	for _, bad := range []string{"0s", "-1m", "soon"} {
		t.Setenv("MACHINARCHY_DATABASE_MIGRATION_TIMEOUT", bad)
		if _, err := Load(); err == nil {
			t.Errorf("migration timeout %q accepted", bad)
		}
	}
}

func TestLoadSessionAndLoginSettings(t *testing.T) {
	setCommonEnv(t)
	cfg, err := Load()
	if err != nil || cfg.SessionTTL != 24*time.Hour || cfg.LoginThrottle != domain.DefaultLoginThrottlePolicy() || len(cfg.SessionPreviousKeys) != 0 {
		t.Fatalf("defaults: %v %+v %v, %v", cfg.SessionTTL, cfg.LoginThrottle, cfg.SessionPreviousKeys, err)
	}
	t.Setenv("MACHINARCHY_SESSION_TTL", "8h")
	t.Setenv("MACHINARCHY_SESSION_PREVIOUS_KEYS", " previous-key-0123456789abcdef0123456789 , ,0123456789abcdef0123456789abcdef")
	t.Setenv("MACHINARCHY_LOGIN_FREE_FAILURES", "10")
	t.Setenv("MACHINARCHY_LOGIN_MAX_BACKOFF", "1h")
	t.Setenv("MACHINARCHY_LOGIN_IP_MAX_FAILURES", "100")
	t.Setenv("MACHINARCHY_LOGIN_FAILURE_WINDOW", "30m")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	// The current token is dropped from the previous keys.
	if cfg.SessionTTL != 8*time.Hour || len(cfg.SessionPreviousKeys) != 1 || cfg.SessionPreviousKeys[0] != "previous-key-0123456789abcdef0123456789" {
		t.Fatalf("session settings: %v %v", cfg.SessionTTL, cfg.SessionPreviousKeys)
	}
	if p := cfg.LoginThrottle; p.FreeFailures != 10 || p.MaxDelay != time.Hour || p.IPMaxFailures != 100 || p.Window != 30*time.Minute {
		t.Fatalf("login throttle = %+v", p)
	}
	for key, bad := range map[string]string{
		"MACHINARCHY_SESSION_TTL":           "1m",
		"MACHINARCHY_SESSION_PREVIOUS_KEYS": "short",
		"MACHINARCHY_LOGIN_FREE_FAILURES":   "0",
		"MACHINARCHY_LOGIN_MAX_BACKOFF":     "48h",
		"MACHINARCHY_LOGIN_FAILURE_WINDOW":  "10s",
	} {
		setCommonEnv(t)
		t.Setenv(key, bad)
		if _, err := Load(); err == nil {
			t.Errorf("%s=%s accepted", key, bad)
		}
	}
}

// S-10 and the configuration inventory: the API listener's settings come
// from configuration, CORS included.
func TestLoadHTTPSettings(t *testing.T) {
	setCommonEnv(t)
	cfg, err := Load()
	want := HTTPConfig{RateLimitRPS: DefaultRateLimitRPS, RateLimitBurst: DefaultRateLimitBurst,
		MaxBodyBytes: DefaultMaxBodyBytes, RequestTimeout: DefaultHTTPRequestTimeout}
	if err != nil || len(cfg.HTTP.CORSAllowedOrigins) != 0 || len(cfg.HTTP.TrustedProxies) != 0 ||
		cfg.HTTP.RateLimitRPS != want.RateLimitRPS || cfg.HTTP.RateLimitBurst != want.RateLimitBurst ||
		cfg.HTTP.MaxBodyBytes != want.MaxBodyBytes || cfg.HTTP.RequestTimeout != want.RequestTimeout {
		t.Fatalf("defaults: %+v, %v", cfg.HTTP, err)
	}

	// The earlier unprefixed name still works; the prefixed one wins.
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://old.example")
	if cfg, err = Load(); err != nil || len(cfg.HTTP.CORSAllowedOrigins) != 1 || cfg.HTTP.CORSAllowedOrigins[0] != "https://old.example" {
		t.Fatalf("legacy CORS name: %v, %v", cfg.HTTP.CORSAllowedOrigins, err)
	}
	t.Setenv("MACHINARCHY_CORS_ALLOWED_ORIGINS", "https://console.example, http://localhost:5173")
	t.Setenv("MACHINARCHY_TRUSTED_PROXIES", "10.0.0.0/8, 192.0.2.7, ::ffff:198.51.100.1, 2001:db8::/32")
	t.Setenv("MACHINARCHY_RATE_LIMIT_RPS", "2.5")
	t.Setenv("MACHINARCHY_RATE_LIMIT_BURST", "7")
	t.Setenv("MACHINARCHY_HTTP_MAX_BODY_BYTES", "2097152")
	t.Setenv("MACHINARCHY_HTTP_REQUEST_TIMEOUT", "40s")
	if cfg, err = Load(); err != nil {
		t.Fatal(err)
	}
	h := cfg.HTTP
	if len(h.CORSAllowedOrigins) != 2 || h.CORSAllowedOrigins[0] != "https://console.example" {
		t.Fatalf("origins = %v", h.CORSAllowedOrigins)
	}
	var proxies []string
	for _, p := range h.TrustedProxies {
		proxies = append(proxies, p.String())
	}
	if strings.Join(proxies, " ") != "10.0.0.0/8 192.0.2.7/32 198.51.100.1/32 2001:db8::/32" {
		t.Fatalf("trusted proxies = %v", proxies)
	}
	if h.RateLimitRPS != 2.5 || h.RateLimitBurst != 7 || h.MaxBodyBytes != 2<<20 || h.RequestTimeout != 40*time.Second {
		t.Fatalf("limits = %+v", h)
	}

	for key, bad := range map[string]string{
		"MACHINARCHY_CORS_ALLOWED_ORIGINS": "*",
		"MACHINARCHY_TRUSTED_PROXIES":      "0.0.0.0/0",
		"MACHINARCHY_RATE_LIMIT_RPS":       "0",
		"MACHINARCHY_RATE_LIMIT_BURST":     "-1",
		"MACHINARCHY_HTTP_MAX_BODY_BYTES":  "10",
		"MACHINARCHY_HTTP_REQUEST_TIMEOUT": "10m",
	} {
		setCommonEnv(t)
		t.Setenv(key, bad)
		if _, err := Load(); err == nil {
			t.Errorf("%s=%s accepted", key, bad)
		}
	}
	setCommonEnv(t)
	t.Setenv("MACHINARCHY_TRUSTED_PROXIES", "proxy.internal")
	if _, err := Load(); err == nil {
		t.Error("a host name was accepted as a trusted proxy")
	}
}

// Phase 16: the built-in provider is the default and, for now, the only one.
func TestKMSProvider(t *testing.T) {
	setCommonEnv(t)
	if cfg, err := Load(); err != nil || cfg.KMSProvider != KMSProviderLocal {
		t.Fatalf("default = %q, %v", cfg.KMSProvider, err)
	}
	t.Setenv("MACHINARCHY_KMS_PROVIDER", "Local")
	if cfg, err := Load(); err != nil || cfg.KMSProvider != KMSProviderLocal {
		t.Fatalf("explicit local = %q, %v", cfg.KMSProvider, err)
	}
	t.Setenv("MACHINARCHY_KMS_PROVIDER", "vault")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("an unavailable provider: %v", err)
	}
	// Key IDs are stored as "local:<id>" (VARCHAR(128) on PostgreSQL).
	setCommonEnv(t)
	for _, bad := range []string{strings.Repeat("k", 123), "prod:2026"} {
		t.Setenv("MACHINARCHY_ENCRYPTION_KEY_ID", bad)
		if _, err := Load(); err == nil {
			t.Errorf("key ID %q accepted", bad)
		}
	}
	t.Setenv("MACHINARCHY_ENCRYPTION_KEY_ID", strings.Repeat("k", 122))
	if _, err := Load(); err != nil {
		t.Errorf("a 122-character key ID: %v", err)
	}
}
