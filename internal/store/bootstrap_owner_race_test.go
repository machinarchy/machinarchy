package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/store"
	"github.com/machinarchy/machinarchy/internal/testutil/pgtest"
)

// TestConcurrentBootstrapOwner (AUDIT/20 F20-03): replicas starting together
// all try to create the first owner. Exactly one creates it and every other
// one sees it as the owner, never as a plain user it must refuse.
func TestConcurrentBootstrapOwner(t *testing.T) {
	url := pgtest.URL(t)
	ctx := context.Background()
	for round := range 3 {
		stores, err := store.Open(ctx, store.Config{Engine: store.EnginePostgres, PostgresURL: url,
			EncryptionSecret: "bootstrap-race-secret-long-enough", MaxOpenConns: 16, MaxIdleConns: 16})
		if err != nil {
			t.Fatal(err)
		}
		username := "admin-" + string(rune('a'+round))
		// Each round starts with no owner.
		if _, err := stores.DB.ExecContext(ctx, `DELETE FROM workspace_memberships WHERE role = 'owner'`); err != nil {
			t.Fatal(err)
		}
		const replicas = 8
		var wg sync.WaitGroup
		created := make(chan bool, replicas)
		errs := make(chan error, replicas)
		start := make(chan struct{})
		for range replicas {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				ok, err := service.BootstrapOwner(ctx, stores.Users, username, "", "bootstrap-race-password")
				if err != nil {
					errs <- err
				}
				created <- ok
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		close(created)
		for err := range errs {
			t.Errorf("round %d: a replica failed: %v", round, err)
		}
		n := 0
		for ok := range created {
			if ok {
				n++
			}
		}
		if owners, err := stores.Users.CountWorkspaceOwners(ctx, domain.DefaultWorkspaceID); err != nil || owners != 1 || n != 1 {
			t.Errorf("round %d: %d owners, %d replicas created one (%v)", round, owners, n, err)
		}
		_ = stores.Close()
	}
}
