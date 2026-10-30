package http

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

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
	engine         *echo.Echo
	httpServer     *http.Server
	metricsHandler http.Handler
	limiter        *custommw.RateLimiter
	readiness      []ReadinessChecker
	alive          atomic.Bool
}

// NewServer builds the HTTP adapter from domain ports and operational health checks.
func NewServer(
	cfg config.Config,
	authService domain.AuthService,
	hostService service.HostService,
	metricsService domain.MetricsService,
	sshCapacity *service.SSHCapacity,
	repositories v1.Repositories,
	database DatabaseReadiness,
	dependencies ...ReadinessChecker,
) (*Server, error) {
	if database == nil || repositories.Users == nil || repositories.Metrics == nil || repositories.Hosts == nil ||
		repositories.Jobs == nil || repositories.Environments == nil || repositories.Access == nil ||
		authService == nil || hostService == nil || metricsService == nil || sshCapacity == nil {
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
	targetService, err := service.NewTargetService(repositories.Hosts, repositories.Environments)
	if err != nil {
		return nil, fmt.Errorf("configure target service: %w", err)
	}
	auditService, err := service.NewAuditService(repositories.Audit)
	if err != nil {
		return nil, fmt.Errorf("configure audit service: %w", err)
	}
	origins, err := allowedOrigins(cfg.Environment)
	if err != nil {
		return nil, err
	}
	limiter, err := custommw.NewRateLimiter(custommw.Config{
		RequestsPerSec:       20,
		Burst:                30,
		InitialBlockDuration: time.Minute,
		MaxBlockDuration:     2 * time.Hour,
	})
	if err != nil {
		return nil, fmt.Errorf("configure API rate limiter: %w", err)
	}

	e := echo.New()
	e.HideBanner = true
	e.IPExtractor = echo.ExtractIPDirect()
	e.Use(echomw.BodyLimit("1M"))
	e.Use(echomw.Recover())
	e.Use(echomw.RequestID())
	e.Use(custommw.Logging())
	e.Use(custommw.CORS(origins))

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	e.Use(registerHTTPMetrics(registry, database.DatabaseStats, sshCapacity))

	metricsHandler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	s := &Server{
		engine:         e,
		metricsHandler: metricsHandler,
		limiter:        limiter,
		readiness: append([]ReadinessChecker{
			readinessCheck{name: "database", check: database.CheckReadiness},
		}, dependencies...),
	}
	s.alive.Store(true)
	e.GET("/healthz", s.liveHandler)
	e.GET("/healthz/live", s.liveHandler)
	e.GET("/healthz/ready", s.readyHandler)
	e.GET("/metrics", s.metricsEndpoint)
	e.GET("/openapi.json", echo.WrapHandler(docs.SpecHandler()))
	e.GET("/scalar", s.scalarReferenceHandler)

	api := e.Group("/api", limiter.Middleware())
	v1.RegisterRoutes(api, repositories, authService,
		metricsService, hostService,
		[]byte(cfg.OperatorToken), cfg.Environment == config.Production, cfg.SignupEnabled, tokenService, memberService, targetService, auditService)

	s.httpServer = &http.Server{
		Addr:              "",
		Handler:           otelhttp.NewHandler(e, "machinarchy.http.server"),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
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
	e.HideBanner = true
	e.IPExtractor = echo.ExtractIPDirect()
	e.Use(echomw.Recover())
	e.Use(echomw.RequestID())
	e.Use(custommw.Logging())

	s := &Server{
		engine:         e,
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

// Start binds the requested address and serves until shutdown or listener failure.
func (s *Server) Start(address string) error {
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
	if err := s.httpServer.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		closeErr := s.httpServer.Close()
		return errors.Join(fmt.Errorf("shutdown HTTP server: %w", err), closeErr)
	}
	return nil
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

func registerHTTPMetrics(registry *prometheus.Registry, databaseStats func() sql.DBStats, sshCapacity *service.SSHCapacity) echo.MiddlewareFunc {
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
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "machinarchy", Name: "ssh_operations_in_flight", Help: "Current outbound SSH operations."}, func() float64 { return float64(sshCapacity.InUse()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "machinarchy", Name: "ssh_operations_limit", Help: "Maximum concurrent outbound SSH operations."}, func() float64 { return float64(sshCapacity.Limit()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Namespace: "machinarchy", Name: "ssh_capacity_rejections_total", Help: "SSH operations rejected because the process bulkhead was full."}, func() float64 { return float64(sshCapacity.Rejected()) }),
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

func allowedOrigins(environment config.Environment) ([]string, error) {
	const defaults = "http://localhost:5173,http://127.0.0.1:5173"
	raw := os.Getenv("CORS_ALLOWED_ORIGINS")
	if strings.TrimSpace(raw) == "" {
		if environment == config.Production {
			return nil, errors.New("CORS_ALLOWED_ORIGINS is required in production")
		}
		raw = defaults
	}
	origins := strings.Split(raw, ",")
	clean := origins[:0]
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
			parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || origin == "*" {
			return nil, fmt.Errorf("invalid CORS origin %q; provide explicit http:// or https:// origins", origin)
		}
		clean = append(clean, origin)
	}
	if len(clean) == 0 {
		return nil, errors.New("CORS_ALLOWED_ORIGINS must contain at least one explicit origin")
	}
	return clean, nil
}
