package http

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestOperationsServerStartsAndShutsDown(t *testing.T) {
	server, err := NewOperationsServer(lifecycleTestDatabase{})
	if err != nil {
		t.Fatalf("NewOperationsServer() error = %v", err)
	}

	ready := make(chan struct{})
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.StartWithReady("127.0.0.1:0", func() { close(ready) })
	}()

	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("operations server did not bind its listener")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-serveErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("StartWithReady() error = %v, want http.ErrServerClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("operations server did not stop serving")
	}
}

func TestOperationsServerReportsListenerFailure(t *testing.T) {
	server, err := NewOperationsServer(lifecycleTestDatabase{})
	if err != nil {
		t.Fatalf("NewOperationsServer() error = %v", err)
	}
	readyCalled := false
	err = server.StartWithReady("invalid-address", func() { readyCalled = true })
	if err == nil {
		t.Fatal("StartWithReady() accepted an invalid listen address")
	}
	if readyCalled {
		t.Fatal("listener readiness was reported after bind failure")
	}
}

type lifecycleTestDatabase struct{}

func (lifecycleTestDatabase) CheckReadiness(ctx context.Context) error { return ctx.Err() }
func (lifecycleTestDatabase) DatabaseStats() sql.DBStats               { return sql.DBStats{} }
