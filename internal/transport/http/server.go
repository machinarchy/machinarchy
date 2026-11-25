// Package http provides the HTTP transport layer for Machinarchy,
// including API routing, handlers, middleware, and API documentation.
package http

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/machinarchy/machinarchy/docs"
	"github.com/machinarchy/machinarchy/internal/config"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/service"
	custommw "github.com/machinarchy/machinarchy/internal/transport/http/middleware"
	v1 "github.com/machinarchy/machinarchy/internal/transport/http/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const readinessTimeout = 2 * time.Second

// ReadinessChecker is implemented by downstream dependencies whose availability
// is required before this process should receive traffic.
type ReadinessChecker interface {
	ReadinessName() string
	CheckReadiness(context.Context) error
}

// DatabaseReadiness exposes only the database operations needed by health
// checks and connection-pool metrics.
type DatabaseReadiness interface {
	CheckReadiness(context.Context) error
	DatabaseStats() sql.DBStats
}

type readinessCheck struct {
	name  string
	check func(context.Context) error
}

func (c readinessCheck) ReadinessName() string { return c.name }
func (c readinessCheck) CheckReadiness(ctx context.Context) error {
	return c.check(ctx)
}

// Server owns the HTTP listener and its process-local middleware resources.
type Server struct {
	registry       *prometheus.Registry
	httpServer     *http.Server
	metricsHandler http.Handler
	limiter        *custommw.RateLimiter
	readiness      []ReadinessChecker
	alive          atomic.Bool

	// metricsServer serves /metrics on its own listener when configured
	// (AUDIT S-17); /metrics is then absent from the API listener.
	metricsServer *http.Server
	metricsAddr   string
	metricsBound  string // actual address once bound, for logs and tests
}

// NewServer builds the HTTP adapter from domain ports and operational health checks.
func NewServer(
	cfg config.Config,
	authService domain.AuthService,
	repositories v1.Repositories,
	database DatabaseReadiness,
	dependencies ...ReadinessChecker,
) (*Server, error) {
	if database == nil || repositories.Users == nil || repositories.Environments == nil || repositories.Access == nil ||
		repositories.Sessions == nil || repositories.SessionKeys == nil || authService == nil {
		return nil, errors.New("HTTP server dependencies are required")
	}
	tokenService, err := service.NewAPITokenService(repositories.Access)
	if err != nil {
		return nil, fmt.Errorf("configure API token service: %w", err)
	}
	memberService, err := service.NewMemberService(repositories.Users)
	if err != nil {
		return nil, fmt.Errorf("configure member service: %w", err)
	}
	auditService, err := service.NewAuditService(repositories.Audit)
	if err != nil {
		return nil, fmt.Errorf("configure audit service: %w", err)
	}
	httpCfg := cfg.HTTP.WithDefaults()
	origins, err := allowedOrigins(cfg.Environment, httpCfg.CORSAllowedOrigins)
	if err != nil {
		return nil, err
	}
	limiter, err := custommw.NewRateLimiter(custommw.Config{RequestsPerSec: httpCfg.RateLimitRPS, Burst: httpCfg.RateLimitBurst})
	if err != nil {
		return nil, fmt.Errorf("configure API rate limiter: %w", err)
	}

	e := echo.New()
	e.HTTPErrorHandler = httperr.Handler
	e.HideBanner = true
	e.IPExtractor = ClientIPExtractor(httpCfg.TrustedProxies)
	e.Use(echomw.BodyLimit(strconv.FormatInt(httpCfg.MaxBodyBytes, 10)))
	e.Use(echomw.Recover())
	e.Use(echomw.RequestID())
	e.Use(custommw.Logging())
	e.Use(custommw.SecurityHeaders(cfg.Environment == config.Production))
	e.Use(custommw.CORS(origins))

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	e.Use(registerHTTPMetrics(registry, database.DatabaseStats))

	metricsHandler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	s := &Server{
		registry:       registry,
		metricsHandler: metricsHandler,
		limiter:        limiter,
		readiness: append([]ReadinessChecker{
			readinessCheck{name: "database", check: database.CheckReadiness},
		}, dependencies...),
	}
	s.alive.Store(true)
	s.registerOperationalRoutes(e, cfg.APIDocsEnabled, cfg.MetricsListenAddr == "")
	if cfg.MetricsListenAddr != "" {
		s.useMetricsListener(cfg.MetricsListenAddr)
	}

	api := e.Group("/api", limiter.Middleware(), requestDeadline(httpCfg.RequestTimeout))
	v1.RegisterRoutes(api, repositories, authService,
		cfg.Environment == config.Production, cfg.SignupEnabled, tokenService, memberService, auditService)

	s.httpServer = &http.Server{
		Addr:              "",
		Handler:           otelhttp.NewHandler(e, "machinarchy.http.server"),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      WriteTimeoutFor(httpCfg.RequestTimeout),
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
	}
	if err := validateReadinessCheckers(s.readiness); err != nil {
		limiter.Stop()
		return nil, err
	}
	return s, nil
}

// registerOperationalRoutes adds the unauthenticated routes of the API
// listener: health always, metrics unless they have their own listener, and
// the OpenAPI spec and reference only when API docs are enabled (AUDIT S-17).
func (s *Server) registerOperationalRoutes(e *echo.Echo, apiDocs, metrics bool) {
	e.GET("/healthz", s.liveHandler)
	e.GET("/healthz/live", s.liveHandler)
	e.GET("/healthz/ready", s.readyHandler)
	if metrics {
		e.GET("/metrics", s.metricsEndpoint)
	}
	if apiDocs {
		e.GET("/openapi.json", echo.WrapHandler(docs.SpecHandler()))
		e.GET("/scalar", s.scalarReferenceHandler)
		e.GET("/scalar/init.js", s.scalarInitHandler)
		e.GET(scalarScriptPath, s.scalarBundleHandler)
	}
}

// useMetricsListener serves /metrics, and nothing else, on address. It is
// bound and drained together with the API listener.
func (s *Server) useMetricsListener(address string) {
	s.metricsAddr = address
	s.metricsServer = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/metrics" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
				http.NotFound(w, r)
				return
			}
			s.metricsHandler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
	}
}

// NewOperationsServer creates health and metrics endpoints for a background
// worker process. It deliberately does not register application API routes.
func NewOperationsServer(database DatabaseReadiness, dependencies ...ReadinessChecker) (*Server, error) {
	if database == nil {
		return nil, errors.New("operations server database dependency is required")
	}

	readiness := make([]ReadinessChecker, 0, 1+len(dependencies))
	readiness = append(readiness, readinessCheck{name: "database", check: database.CheckReadiness})
	readiness = append(readiness, dependencies...)
	if err := validateReadinessCheckers(readiness); err != nil {
		return nil, err
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "machinarchy", Name: "db_connections_open",
			Help: "Open database connections.",
		}, func() float64 { return float64(database.DatabaseStats().OpenConnections) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "machinarchy", Name: "db_connections_in_use",
			Help: "Database connections currently in use.",
		}, func() float64 { return float64(database.DatabaseStats().InUse) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "machinarchy", Name: "db_connections_idle",
			Help: "Idle database connections.",
		}, func() float64 { return float64(database.DatabaseStats().Idle) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "machinarchy", Name: "db_connections_max_open",
			Help: "Configured maximum open database connections.",
		}, func() float64 { return float64(database.DatabaseStats().MaxOpenConnections) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Namespace: "machinarchy", Name: "db_connection_waits_total",
			Help: "Cumulative waits for an available database connection.",
		}, func() float64 { return float64(database.DatabaseStats().WaitCount) }),
	)

	e := echo.New()
	e.HTTPErrorHandler = httperr.Handler
	e.HideBanner = true
	e.IPExtractor = echo.ExtractIPDirect()
	e.Use(echomw.Recover())
	e.Use(echomw.RequestID())
	e.Use(custommw.Logging())
	e.Use(custommw.SecurityHeaders(false))

	s := &Server{
		registry:       registry,
		metricsHandler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
		readiness:      readiness,
	}
	s.alive.Store(true)
	e.GET("/healthz", s.liveHandler)
	e.GET("/healthz/live", s.liveHandler)
	e.GET("/healthz/ready", s.readyHandler)
	e.GET("/metrics", s.metricsEndpoint)
	s.httpServer = &http.Server{
		Handler:           otelhttp.NewHandler(e, "machinarchy.http.server"),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
	}
	return s, nil
}

// liveHandler reports whether the process is shutting down.
// @Summary Liveness check
// @Tags Operations
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /healthz [get]
// @Router /healthz/live [get]
func (s *Server) liveHandler(c echo.Context) error {
	if !s.alive.Load() {
		return c.JSON(http.StatusServiceUnavailable, echo.Map{"status": "stopping"})
	}
	return c.JSON(http.StatusOK, echo.Map{"status": "alive"})
}

// readyHandler reports readiness of the process and required dependencies.
// @Summary Readiness check
// @Tags Operations
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 503 {object} map[string]interface{}
// @Router /healthz/ready [get]
func (s *Server) readyHandler(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), readinessTimeout)
	defer cancel()

	statuses := make(map[string]string, len(s.readiness))
	ready := true
	for _, dependency := range s.readiness {
		if err := dependency.CheckReadiness(ctx); err != nil {
			statuses[dependency.ReadinessName()] = "unavailable"
			ready = false
			slog.Warn("readiness check failed", "dependency", dependency.ReadinessName(), "error", err)
			continue
		}
		statuses[dependency.ReadinessName()] = "ready"
	}
	if !s.alive.Load() {
		statuses["process"] = "stopping"
		ready = false
	}
	status := http.StatusOK
	state := "ready"
	if !ready {
		status = http.StatusServiceUnavailable
		state = "not_ready"
	}
	return c.JSON(status, echo.Map{"status": state, "checks": statuses})
}

// AddMetrics also serves the collectors of component (background worker,
// agent gateway, Control Manager) on /metrics. Call it before serving.
func (s *Server) AddMetrics(component prometheus.Gatherer) {
	if s == nil || component == nil {
		return
	}
	s.metricsHandler = promhttp.HandlerFor(prometheus.Gatherers{s.registry, component}, promhttp.HandlerOpts{})
}

// metricsEndpoint serves Prometheus metrics.
// @Summary Prometheus metrics
// @Tags Operations
// @Produce plain
// @Success 200 {string} string "Prometheus exposition format"
// @Router /metrics [get]
func (s *Server) metricsEndpoint(c echo.Context) error {
	s.metricsHandler.ServeHTTP(c.Response(), c.Request())
	return nil
}

// Handler returns the API listener's handler, for in-process tests of the
// fully wired server.
func (s *Server) Handler() http.Handler {
	if s == nil || s.httpServer == nil {
		return nil
	}
	return s.httpServer.Handler
}

// Start binds the requested address and serves until shutdown or listener failure.
func (s *Server) Start(address string) error {
	return s.StartWithReady(address, nil)
}

// StartWithReady binds before invoking ready, allowing the process lifecycle to
// avoid racing shutdown against listener creation.
func (s *Server) StartWithReady(address string, ready func()) error {
	if s == nil || s.httpServer == nil {
		return errors.New("HTTP server is not initialized")
	}
	if strings.TrimSpace(address) == "" {
		return errors.New("HTTP listen address is required")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", address, err)
	}
	s.httpServer.Addr = address
	if s.metricsServer != nil {
		metricsListener, err := net.Listen("tcp", s.metricsAddr)
		if err != nil {
			_ = listener.Close()
			return fmt.Errorf("listen for metrics on %q: %w", s.metricsAddr, err)
		}
		s.metricsBound = metricsListener.Addr().String()
		slog.Info("metrics listening", "address", s.metricsBound)
		go func() {
			if err := s.metricsServer.Serve(metricsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("metrics listener stopped", "error", err)
			}
		}()
	}
	if ready != nil {
		ready()
	}
	slog.Info("HTTP server listening", "address", listener.Addr().String())
	return s.httpServer.Serve(listener)
}

// Shutdown stops accepting requests and drains active requests until ctx expires.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("HTTP shutdown context is required")
	}
	s.alive.Store(false)
	if s.limiter != nil {
		s.limiter.Stop()
	}
	if s.httpServer == nil {
		return nil
	}
	var metricsErr error
	if s.metricsServer != nil {
		if err := s.metricsServer.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			metricsErr = errors.Join(fmt.Errorf("shutdown metrics listener: %w", err), s.metricsServer.Close())
		}
	}
	if err := s.httpServer.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		closeErr := s.httpServer.Close()
		return errors.Join(fmt.Errorf("shutdown HTTP server: %w", err), closeErr, metricsErr)
	}
	return metricsErr
}

func validateReadinessCheckers(checks []ReadinessChecker) error {
	seen := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		if check == nil || strings.TrimSpace(check.ReadinessName()) == "" {
			return errors.New("readiness checkers must be non-nil and have a name")
		}
		name := check.ReadinessName()
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate readiness checker name %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func registerHTTPMetrics(registry *prometheus.Registry, databaseStats func() sql.DBStats) echo.MiddlewareFunc {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "machinarchy",
		Name:      "http_requests_total",
		Help:      "Total HTTP requests served, partitioned by method, route, and status code.",
	}, []string{"method", "route", "status"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "machinarchy",
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request duration in seconds, partitioned by method and route.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "route"})
	registry.MustRegister(requests, duration)
	registry.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "machinarchy", Name: "db_connections_open", Help: "Open database connections."}, func() float64 { return float64(databaseStats().OpenConnections) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "machinarchy", Name: "db_connections_in_use", Help: "Database connections currently in use."}, func() float64 { return float64(databaseStats().InUse) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "machinarchy", Name: "db_connections_idle", Help: "Idle database connections."}, func() float64 { return float64(databaseStats().Idle) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "machinarchy", Name: "db_connections_max_open", Help: "Configured maximum open database connections."}, func() float64 { return float64(databaseStats().MaxOpenConnections) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Namespace: "machinarchy", Name: "db_connection_waits_total", Help: "Cumulative waits for an available database connection."}, func() float64 { return float64(databaseStats().WaitCount) }),
	)

	// One middleware instance shares bounded-cardinality instruments across requests.
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().URL.Path == "/metrics" {
				return next(c)
			}
			started := time.Now()
			err := next(c)
			statusCode := c.Response().Status
			if err != nil && statusCode < http.StatusBadRequest {
				statusCode = http.StatusInternalServerError
				var httpErr *echo.HTTPError
				if errors.As(err, &httpErr) {
					statusCode = httpErr.Code
				}
			} else if statusCode == 0 {
				statusCode = http.StatusOK
			}
			route := c.Path()
			if route == "" {
				route = "unmatched"
			}
			method := c.Request().Method
			requests.WithLabelValues(method, route, strconv.Itoa(statusCode)).Inc()
			duration.WithLabelValues(method, route).Observe(time.Since(started).Seconds())
			return err
		}
	}
}

// allowedOrigins returns the configured CORS origins; outside production an
// empty list means the web console's dev server.
func allowedOrigins(environment config.Environment, configured []string) ([]string, error) {
	if len(configured) > 0 {
		return configured, nil
	}
	if environment == config.Production {
		return nil, errors.New("MACHINARCHY_CORS_ALLOWED_ORIGINS is required in production")
	}
	return config.DevelopmentCORSOrigins, nil
}

// ClientIPExtractor returns how the client address is found (AUDIT S-10).
// With no trusted proxies it is the TCP peer. Otherwise X-Forwarded-For is
// read from the right, skipping only the listed proxies, so a client cannot
// choose its own address by sending the header: the first address not in
// the list is the client. Loopback, link-local and private ranges are not
// trusted unless listed.
func ClientIPExtractor(trusted []netip.Prefix) echo.IPExtractor {
	if len(trusted) == 0 {
		return echo.ExtractIPDirect()
	}
	options := []echo.TrustOption{echo.TrustLoopback(false), echo.TrustLinkLocal(false), echo.TrustPrivateNet(false)}
	for _, prefix := range trusted {
		options = append(options, echo.TrustIPRange(&net.IPNet{
			IP: net.IP(prefix.Addr().AsSlice()), Mask: net.CIDRMask(prefix.Bits(), prefix.Addr().BitLen()),
		}))
	}
	return echo.ExtractIPFromXFFHeader(options...)
}

// WriteTimeoutFor is the listener's write timeout for a request deadline:
// longer than the deadline, so database work for a request the client can no
// longer receive is cancelled instead of running on (AUDIT Q-20).
func WriteTimeoutFor(requestTimeout time.Duration) time.Duration {
	return requestTimeout + 5*time.Second
}

func requestDeadline(timeout time.Duration) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx, cancel := context.WithTimeout(c.Request().Context(), timeout)
			defer cancel()
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}
