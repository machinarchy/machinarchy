package v1

import (
	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"

	// Sub-domain packages
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/auth"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/environments"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/history"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/host"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/job"
)

// Repositories groups the domain ports required by the v1 HTTP transport.
// Concrete database adapters are assembled at the process composition root.
type Repositories struct {
	Users        domain.UserStore
	Metrics      domain.MetricsStore
	Hosts        domain.HostStore
	Jobs         domain.JobStore
	Environments domain.EnvironmentStore
}

// RegisterRoutes sets up all v1 domain routes on the Echo instance with proper dependency injection.
func RegisterRoutes(
	g *echo.Group,
	repos Repositories,
	authServ domain.AuthService,
	metricsServ domain.MetricsService,
	hostServ service.HostService,
	jwtSecret []byte,
	secureCookies bool,
) {
	// 2. Initialize Sub-Domain Handlers
	authHandler := auth.NewHandler(authServ, secureCookies)
	envHandler := environments.NewHandler(repos.Environments)
	historyHandler := history.NewHandler(metricsServ, repos.Hosts)
	hostHandler := host.NewHandler(repos.Hosts, repos.Jobs, hostServ)
	jobHandler := job.NewHandler(repos.Jobs)

	// ==========================================
	// PUBLIC ROUTES (No Auth Middleware)
	// ==========================================
	authGroup := g.Group("/auth")
	auth.RegisterRoutes(authGroup, authHandler)

	// ==========================================
	// PROTECTED API ROUTES
	// ==========================================
	apiv1 := g.Group("/v1", middleware.JWTAuth(jwtSecret))
	{
		// User Profile
		apiv1.GET("/me", authHandler.GetMe)

		// Environment Sub-domain Group
		envGroup := apiv1.Group("/environments")
		environments.RegisterRoutes(envGroup, envHandler)

		// Host management routes.
		hostGroup := apiv1.Group("/hosts")
		host.RegisterRoutes(hostGroup, hostHandler)

		// /api/v1/jobs (Global job logs & tracking)
		jobGroup := apiv1.Group("/jobs")
		job.RegisterRoutes(jobGroup, jobHandler)

		// History & Job Tracking Sub-domain Group
		historyGroup := apiv1.Group("/history")
		history.RegisterRoutes(historyGroup, historyHandler)

	}
}
