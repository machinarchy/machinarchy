package service_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/store"
	"github.com/machinarchy/machinarchy/internal/testutil/pgtest"
)

// BenchmarkHostSelectors measures label selectors over large inventories
// (AUDIT A-12). Labels: tier (4 values), zone (100 values) and canary on 1%
// of hosts, so "tier=db,zone=z9" matches 1 in 100.
//
//	go test -run '^$' -bench HostSelectors -benchtime 20x ./internal/service/
//
// PostgreSQL runs too when MACHINARCHY_TEST_POSTGRES_URL is set.
func BenchmarkHostSelectors(b *testing.B) {
	engines := []string{store.EngineSQLite}
	if os.Getenv("MACHINARCHY_TEST_POSTGRES_URL") != "" {
		engines = append(engines, store.EnginePostgres)
	}
	for _, engine := range engines {
		for _, size := range []int{10_000, 50_000} {
			b.Run(fmt.Sprintf("%s/%dk", engine, size/1000), func(b *testing.B) {
				app := seededInventory(b, engine, size)
				ctx := context.Background()
				reader := domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleReader}
				b.Run("list page tier=db,zone=z9", func(b *testing.B) {
					for b.Loop() {
						page, err := app.ListHosts(ctx, reader, service.HostQuery{Selector: "tier=db,zone=z9", Limit: 100})
						if err != nil || len(page.Items) != min(100, size/100) {
							b.Fatalf("page: %d items, %v", len(page.Items), err)
						}
					}
				})
				b.Run("preview tier=db,zone=z9", func(b *testing.B) {
					for b.Loop() {
						set, err := app.PreviewTargets(ctx, reader, domain.TargetRule{Selector: "tier=db,zone=z9"})
						if err != nil || len(set.Matched) != size/100 {
							b.Fatalf("preview: %v", err)
						}
					}
				})
				b.Run("preview canary,tier!=batch", func(b *testing.B) {
					for b.Loop() {
						if _, err := app.PreviewTargets(ctx, reader, domain.TargetRule{Selector: "canary,tier!=batch"}); err != nil {
							b.Fatal(err)
						}
					}
				})
				b.Run("list page name=host-4999* (no label: full scan)", func(b *testing.B) {
					for b.Loop() {
						if _, err := app.ListHosts(ctx, reader, service.HostQuery{Selector: "name=host-4999*", Limit: 100}); err != nil {
							b.Fatal(err)
						}
					}
				})
			})
		}
	}
}

// seededInventory opens a store with size hosts written straight into the
// tables (the API path writes audit and outbox rows per host, which would
// make seeding the slow part).
func seededInventory(b *testing.B, engine string, size int) *service.ManagedResourceService {
	b.Helper()
	ctx := context.Background()
	config := store.Config{Engine: store.EngineSQLite, EncryptionSecret: "selector-benchmark-secret-long-enough"}
	if engine == store.EnginePostgres {
		config.Engine, config.PostgresURL, config.MaxOpenConns, config.MaxIdleConns = store.EnginePostgres, pgtest.URL(b), 8, 8
	} else {
		dir := b.TempDir()
		if err := os.Chmod(dir, 0o700); err != nil {
			b.Fatal(err)
		}
		config.SQLitePath = filepath.Join(dir, "bench.db")
	}
	stores, err := store.Open(ctx, config)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = stores.Close() })
	began := time.Now()
	tx, err := stores.DB.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	const batch = 500
	created := time.Now().UTC().Format(time.RFC3339Nano)
	for start := 0; start < size; start += batch {
		var hostRows, labelRows []string
		for i := start; i < min(start+batch, size); i++ {
			id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
			labels := map[string]string{"tier": []string{"web", "db", "cache", "batch"}[i%4], "zone": fmt.Sprintf("z%d", i%100)}
			if i%100 == 0 {
				labels["canary"] = "yes"
			}
			var pairs []string
			for key, value := range labels {
				pairs = append(pairs, fmt.Sprintf("%q:%q", key, value))
				labelRows = append(labelRows, fmt.Sprintf("('%s', 1, '%s', '%s')", id, key, value))
			}
			hostRows = append(hostRows, fmt.Sprintf("('%s', 1, 'host-%05d', '{%s}', 'Managed', 1, 1, 'Active', '%s', '%s')",
				id, i, strings.Join(pairs, ","), created, created))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO managed_hosts (id, workspace_id, name, labels, desired_presence, generation, revision, phase, created_at, updated_at) VALUES `+
			strings.Join(hostRows, ",")); err != nil {
			b.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO managed_host_labels (host_id, workspace_id, label_key, label_value) VALUES `+
			strings.Join(labelRows, ",")); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	if engine == store.EnginePostgres {
		if _, err := stores.DB.ExecContext(ctx, `ANALYZE managed_hosts; ANALYZE managed_host_labels`); err != nil {
			b.Fatal(err)
		}
	} else if _, err := stores.DB.ExecContext(ctx, `ANALYZE`); err != nil {
		b.Fatal(err)
	}
	b.Logf("seeded %d hosts on %s in %s", size, engine, time.Since(began).Round(time.Millisecond))
	app, err := service.NewManagedResourceService(stores.ResourceHosts, stores.ResourceProfiles, stores.Operations,
		func() domain.ExecutionPolicy { return domain.ExecutionPolicy{} })
	if err != nil {
		b.Fatal(err)
	}
	return app
}
