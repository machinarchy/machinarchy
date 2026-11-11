package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/config"
)

func TestRunComponentsGracefulShutdown(t *testing.T) {
	tests := []struct {
		name      string
		hasWorker bool
	}{
		{name: "API"},
		{name: "Control Manager", hasWorker: true},
		{name: "all-in-one", hasWorker: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			server := newFakeHTTP()
			worker := &fakeWorker{started: make(chan struct{}), stopped: make(chan struct{})}
			var workerLifecycle workerLifecycle
			if test.hasWorker {
				workerLifecycle = worker
			}

			done := make(chan error, 1)
			go func() { done <- runComponents(ctx, "127.0.0.1:0", server, workerLifecycle) }()

			select {
			case <-server.ready:
			case <-time.After(time.Second):
				t.Fatal("HTTP listener did not reach ready state")
			}
			if test.hasWorker {
				select {
				case <-worker.started:
				case <-time.After(time.Second):
					t.Fatal("background worker did not start")
				}
			}

			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("runComponents() error = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("process components did not stop")
			}
			if !server.shutdownCalled.Load() {
				t.Fatal("HTTP server shutdown was not called")
			}
			if test.hasWorker {
				select {
				case <-worker.stopped:
				default:
					t.Fatal("background worker did not observe cancellation")
				}
			}
		})
	}
}

func TestControlManagerRuntimeUsesDatabaseOutboxWithoutNATS(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime, err := OpenControlManager(context.Background(), config.Config{
		Database:         config.DatabaseConfig{Engine: config.SQLite, Path: filepath.Join(dir, "control.db"), ConnectTimeout: time.Second},
		EncryptionSecret: "control-manager-test-secret-that-is-at-least-32-bytes",
	})
	if err != nil {
		t.Fatalf("OpenControlManager() without broker URL: %v", err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("close runtime: %v", err)
		}
	}()
	engine, err := runtime.NewControlManager()
	if err != nil {
		t.Fatalf("NewControlManager(): %v", err)
	}
	if _, err := runtime.NewControlManagerOperationsServer(engine); err != nil {
		t.Fatalf("create Control Manager health server: %v", err)
	}
}

func TestRunComponentsReportsUnexpectedWorkerStopAndShutsDownServer(t *testing.T) {
	server := newFakeHTTP()
	wantErr := errors.New("worker startup failed")
	worker := &fakeWorker{started: make(chan struct{}), stopped: make(chan struct{}), result: wantErr}
	err := runComponents(context.Background(), "127.0.0.1:0", server, worker)
	if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("runComponents() error = %v, want worker startup failure", err)
	}
	if !server.shutdownCalled.Load() {
		t.Fatal("HTTP server was not shut down after worker failure")
	}
}

func TestRunComponentsDoesNotStartWorkerAfterStartupCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := newFakeHTTP()
	worker := &fakeWorker{started: make(chan struct{}), stopped: make(chan struct{})}
	err := runComponents(ctx, "127.0.0.1:0", server, worker)
	if err != nil {
		t.Fatalf("runComponents() error = %v", err)
	}
	select {
	case <-worker.started:
		t.Fatal("worker started after startup context was canceled")
	default:
	}
	if !server.shutdownCalled.Load() {
		t.Fatal("HTTP server shutdown was not called after startup cancellation")
	}
}

func TestRunComponentsReportsListenerStartupFailure(t *testing.T) {
	wantErr := errors.New("address already in use")
	server := &fakeHTTP{startErr: wantErr, ready: make(chan struct{}), stopped: make(chan struct{})}
	err := runComponents(context.Background(), "127.0.0.1:0", server, nil)
	if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("runComponents() error = %v, want listener startup error", err)
	}
	if !server.shutdownCalled.Load() {
		t.Fatal("HTTP server shutdown was not called after listener failure")
	}
}

func TestRunComponentsRejectsMissingStartupDependencies(t *testing.T) {
	if err := runComponents(context.Background(), "127.0.0.1:0", nil, nil); err == nil {
		t.Fatal("runComponents accepted a nil HTTP server")
	}
	if err := runComponents(context.Background(), " ", newFakeHTTP(), nil); err == nil {
		t.Fatal("runComponents accepted an empty listen address")
	}
	if err := runComponents(nil, "127.0.0.1:0", newFakeHTTP(), nil); err == nil {
		t.Fatal("runComponents accepted a nil context")
	}
}

func TestOpenRejectsMissingOrCanceledStartupContext(t *testing.T) {
	if _, err := Open(nil, config.Config{}); err == nil || !strings.Contains(err.Error(), "runtime context") {
		t.Fatalf("Open(nil, ...) error = %v, want a useful context validation error", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, config.Config{}); err == nil || !strings.Contains(err.Error(), "startup canceled") {
		t.Fatalf("Open(canceled, ...) error = %v, want startup cancellation", err)
	}
}

type fakeHTTP struct {
	ready          chan struct{}
	stopped        chan struct{}
	startErr       error
	shutdownCalled atomic.Bool
	stopOnce       sync.Once
}

func newFakeHTTP() *fakeHTTP {
	return &fakeHTTP{ready: make(chan struct{}), stopped: make(chan struct{})}
}

func (s *fakeHTTP) StartWithReady(_ string, ready func()) error {
	if s.startErr != nil {
		return s.startErr
	}
	ready()
	close(s.ready)
	<-s.stopped
	return http.ErrServerClosed
}

func (s *fakeHTTP) Shutdown(ctx context.Context) error {
	s.shutdownCalled.Store(true)
	s.stopOnce.Do(func() { close(s.stopped) })
	return ctx.Err()
}

type fakeWorker struct {
	started chan struct{}
	stopped chan struct{}
	result  error
}

func (w *fakeWorker) Start(ctx context.Context) error {
	close(w.started)
	if w.result != nil {
		close(w.stopped)
		return w.result
	}
	<-ctx.Done()
	close(w.stopped)
	return nil
}
