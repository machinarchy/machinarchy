package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/machinarchy/machinarchy/internal/config"
)

func TestRequestDeadlineBoundsHandlers(t *testing.T) {
	e := echo.New()
	var deadline time.Time
	handler := requestDeadline(time.Second)(func(c echo.Context) error {
		deadline, _ = c.Request().Context().Deadline()
		return nil
	})
	recorder := httptest.NewRecorder()
	if err := handler(e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/me", nil), recorder)); err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > time.Second {
		t.Fatalf("request deadline in %s, want within 1s", remaining)
	}
	if WriteTimeoutFor(config.DefaultHTTPRequestTimeout) <= config.DefaultHTTPRequestTimeout {
		t.Fatal("the request deadline must be shorter than the write timeout")
	}
}

func TestOperationalRoutesFollowConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name          string
		docs, metrics bool
	}{
		{"production defaults: no docs, metrics elsewhere", false, false},
		{"development: docs and metrics", true, true},
		{"docs only", true, false},
		{"metrics only", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{metricsHandler: promhttp.HandlerFor(prometheus.NewRegistry(), promhttp.HandlerOpts{})}
			s.alive.Store(true)
			e := echo.New()
			s.registerOperationalRoutes(e, tc.docs, tc.metrics)
			for path, want := range map[string]bool{
				"/healthz/live":   true,
				"/metrics":        tc.metrics,
				"/openapi.json":   tc.docs,
				"/scalar":         tc.docs,
				"/scalar/init.js": tc.docs,
			} {
				recorder := httptest.NewRecorder()
				e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
				if got := recorder.Code == http.StatusOK; got != want {
					t.Errorf("GET %s = %d, want served=%v", path, recorder.Code, want)
				}
			}
		})
	}
}

func TestMetricsListenerServesOnlyMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{Name: "p11_probe_total", Help: "test"}))
	s := &Server{metricsHandler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{})}
	e := echo.New()
	s.registerOperationalRoutes(e, false, false)
	s.httpServer = &http.Server{Handler: e, ReadHeaderTimeout: time.Second}
	s.useMetricsListener("127.0.0.1:0")

	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- s.StartWithReady("127.0.0.1:0", func() { close(ready) }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	}
	get := func(path string) (int, string) {
		t.Helper()
		response, err := http.Get("http://" + s.metricsBound + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(body)
	}
	if code, body := get("/metrics"); code != http.StatusOK || !strings.Contains(body, "p11_probe_total") {
		t.Fatalf("metrics listener /metrics = %d %q", code, body)
	}
	if code, _ := get("/healthz/live"); code != http.StatusNotFound {
		t.Fatalf("metrics listener served /healthz/live: %d", code)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve returned %v", err)
	}
	if response, err := http.Get("http://" + s.metricsBound + "/metrics"); err == nil {
		_ = response.Body.Close()
		t.Fatal("metrics listener still accepts connections after shutdown")
	}
}
