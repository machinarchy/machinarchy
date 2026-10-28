package config

import (
	"testing"
)

// setCommonEnv provisions the baseline test environment variables cleanly using a loop.
func setCommonEnv(t *testing.T) {
	t.Helper()
	envs := map[string]string{
		"MACHINARCHY_LISTEN_ADDR":                "",
		"MACHINARCHY_ENVIRONMENT":                "development",
		"MACHINARCHY_SIGNUP_ENABLED":             "",
		"MACHINARCHY_DATABASE_ENGINE":            "sqlite",
		"MACHINARCHY_DATABASE_PATH":              "",
		"MACHINARCHY_DATABASE_URL":               "",
		"MACHINARCHY_DATABASE_MAX_OPEN_CONNS":    "",
		"MACHINARCHY_DATABASE_MAX_IDLE_CONNS":    "",
		"MACHINARCHY_DATABASE_CONN_MAX_LIFETIME": "",
		"MACHINARCHY_DATABASE_CONNECT_TIMEOUT":   "",
		"MACHINARCHY_OPERATOR_TOKEN":             "0123456789abcdef0123456789abcdef",
		"MACHINARCHY_LOG_LEVEL":                  "",
		"MACHINARCHY_SSH_KNOWN_HOSTS":            "",
		"MACHINARCHY_SSH_CONNECT_TIMEOUT":        "",
		"MACHINARCHY_SSH_USE_AGENT":              "",
	}

	for key, value := range envs {
		t.Setenv(key, value)
	}
}

func TestLoadSignupDefaults(t *testing.T) {
	t.Run("enabled in development", func(t *testing.T) {
		setCommonEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !cfg.SignupEnabled {
			t.Fatal("signup should default to enabled in development")
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
	t.Run("defaults allow private networks only", func(t *testing.T) {
		setCommonEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		policy := cfg.SSHTargetPolicy
		if !policy.AllowPrivate || policy.AllowLoopback || len(policy.Allow) != 0 || len(policy.Deny) != 0 {
			t.Fatalf("unexpected default policy: %+v", policy)
		}
	})

	t.Run("explicit configuration", func(t *testing.T) {
		setCommonEnv(t)
		t.Setenv("MACHINARCHY_SSH_ALLOW_PRIVATE", "false")
		t.Setenv("MACHINARCHY_SSH_ALLOW_LOOPBACK", "true")
		t.Setenv("MACHINARCHY_SSH_ALLOWED_CIDRS", "10.20.0.0/16, 192.0.2.7")
		t.Setenv("MACHINARCHY_SSH_DENIED_CIDRS", "10.20.99.0/24")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		policy := cfg.SSHTargetPolicy
		if policy.AllowPrivate || !policy.AllowLoopback || len(policy.Allow) != 2 || len(policy.Deny) != 1 {
			t.Fatalf("unexpected policy: %+v", policy)
		}
	})

	t.Run("rejects invalid values", func(t *testing.T) {
		for key, value := range map[string]string{
			"MACHINARCHY_SSH_ALLOW_PRIVATE":  "maybe",
			"MACHINARCHY_SSH_ALLOW_LOOPBACK": "sometimes",
			"MACHINARCHY_SSH_ALLOWED_CIDRS":  "10.0.0.0/8,banana",
			"MACHINARCHY_SSH_DENIED_CIDRS":   "300.1.1.1/8",
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
