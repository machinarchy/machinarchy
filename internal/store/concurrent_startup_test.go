package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/machinarchy/machinarchy/internal/store"
	"github.com/machinarchy/machinarchy/internal/testutil/pgtest"
)

// TestConcurrentPostgresStartup (AUDIT/20 F20-01): replicas of a deployment
// start together on an empty database. Each runs the migrations; they must
// take turns, so every replica comes up and the schema is built once.
func TestConcurrentPostgresStartup(t *testing.T) {
	url := pgtest.URL(t)
	const replicas = 4
	var wg sync.WaitGroup
	errs := make(chan error, replicas)
	start := make(chan struct{})
	for range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			stores, err := store.Open(context.Background(), store.Config{Engine: store.EnginePostgres, PostgresURL: url,
				EncryptionSecret: "concurrent-startup-secret-long-enough", MaxOpenConns: 4, MaxIdleConns: 2})
			if err != nil {
				errs <- err
				return
			}
			_ = stores.Close()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a replica failed to start: %v", err)
	}
}
