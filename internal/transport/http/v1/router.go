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
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/members"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/tokens"
)

// Repositories groups the domain ports required by the v1 HTTP transport.
// Concrete database adapters are assembled at the process composition root.
type Repositories struct {
	Users        domain.UserStore
	Metrics      domain.MetricsStore
	Hosts        domain.HostStore
	Jobs         domain.JobStore
	Environments domain.EnvironmentStore
	Access       domain.AccessStore
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
	signupEnabled bool,
	tokenService domain.APITokenService,
	memberService domain.MemberService,
) {
	// 2. Initialize Sub-Domain Handlers
	authHandler := auth.NewHandler(authServ, secureCookies, signupEnabled)
	envHandler := environments.NewHandler(repos.Environments)
	historyHandler := history.NewHandler(metricsServ, repos.Hosts)
	hostHandler := host.NewHandler(repos.Hosts, repos.Jobs, repos.Environments, hostServ)
	jobHandler := job.NewHandler(repos.Jobs)
	tokenHandler := tokens.NewHandler(tokenService)
	memberHandler := members.NewHandler(memberService)

	// ==========================================
	// PUBLIC ROUTES (No Auth Middleware)
	// ==========================================
	authGroup := g.Group("/auth")
	auth.RegisterRoutes(authGroup, authHandler)

	// ==========================================
	// PROTECTED API ROUTES
	// ==========================================
	apiv1 := g.Group("/v1", middleware.JWTAuth(jwtSecret, repos.Access))
	{
		// User Profile
		apiv1.GET("/me", authHandler.GetMe)

		// Environment Sub-domain Group
		readRoutes := apiv1.Group("", middleware.RequirePermission(domain.PermissionInventoryRead))
		writeRoutes := apiv1.Group("", middleware.RequirePermission(domain.PermissionInventoryWrite))
		envReadGroup := readRoutes.Group("/environments")
		envWriteGroup := writeRoutes.Group("/environments")
		environments.RegisterRoutes(envReadGroup, envWriteGroup, envHandler)

		// Host management routes.
		hostReadGroup := readRoutes.Group("/hosts")
		hostWriteGroup := writeRoutes.Group("/hosts")
		host.RegisterRoutes(hostReadGroup, hostWriteGroup, hostHandler)

		// /api/v1/jobs (workspace-scoped job logs and tracking)
		jobGroup := apiv1.Group("/jobs", middleware.RequirePermission(domain.PermissionOperationsRead))
		job.RegisterRoutes(jobGroup, jobHandler)

		// History & Job Tracking Sub-domain Group
		historyGroup := apiv1.Group("/history", middleware.RequirePermission(domain.PermissionOperationsRead))
		history.RegisterRoutes(historyGroup, historyHandler)

		tokenGroup := apiv1.Group("/tokens",
			middleware.RequirePermission(domain.PermissionTokenManage), middleware.RequireHuman())
		tokens.RegisterRoutes(tokenGroup, tokenHandler)

		// Owner-only member administration; API tokens are refused.
		memberGroup := apiv1.Group("/members",
			middleware.RequirePermission(domain.PermissionMembersManage), middleware.RequireHuman())
		members.RegisterRoutes(memberGroup, memberHandler)

	}
}
