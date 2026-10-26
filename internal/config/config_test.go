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
