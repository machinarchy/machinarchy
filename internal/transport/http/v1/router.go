// Package v1 provides version 1 of the Machinarchy HTTP API,
// including its routes, handlers, and request/response types.
package v1

import (
	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/runtimeconfig"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"

	// Sub-domain packages
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/audit"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/auth"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/configuration"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/environments"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/managedresource"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/members"
	secretapi "github.com/machinarchy/machinarchy/internal/transport/http/v1/secret"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/tokens"
)

// Repositories groups the domain ports required by the v1 HTTP transport.
// Concrete database adapters are assembled at the process composition root.
type Repositories struct {
	Users            domain.UserStore
	Environments     domain.EnvironmentStore
	Access           domain.AccessStore
	Audit            domain.AuditStore
	Configuration    *runtimeconfig.Service
	Secrets          *secrets.Service
	ManagedResources *service.ManagedResourceService
	SignupEnabled    func() bool
}

// RegisterRoutes sets up all v1 domain routes on the Echo instance with proper dependency injection.
func RegisterRoutes(
	g *echo.Group,
	repos Repositories,
	authServ domain.AuthService,
	jwtSecret []byte,
	secureCookies bool,
	signupEnabled bool,
	tokenService domain.APITokenService,
	memberService domain.MemberService,
	auditService domain.AuditService,
) {
	// 2. Initialize Sub-Domain Handlers
	authHandler := auth.NewHandler(authServ, secureCookies, signupEnabled)
	if repos.SignupEnabled != nil {
		authHandler.SetSignupEnabledProvider(repos.SignupEnabled)
	}
	envHandler := environments.NewHandler(repos.Environments)
	tokenHandler := tokens.NewHandler(tokenService)
	memberHandler := members.NewHandler(memberService)
	auditHandler := audit.NewHandler(auditService)

	// ==========================================
	// PUBLIC ROUTES (No Auth Middleware)
	// ==========================================
	authGroup := g.Group("/auth")
	auth.RegisterRoutes(authGroup, authHandler)

	// ==========================================
	// PROTECTED API ROUTES
	// ==========================================
	apiv1 := g.Group("/v1", middleware.JWTAuth(jwtSecret, repos.Access), middleware.AuditActor())
	{
		// User Profile
		apiv1.GET("/me", authHandler.GetMe)

		// Environment Sub-domain Group
		readRoutes := apiv1.Group("", middleware.RequirePermission(domain.PermissionInventoryRead))
		writeRoutes := apiv1.Group("", middleware.RequirePermission(domain.PermissionInventoryWrite))
		envReadGroup := readRoutes.Group("/environments")
		envWriteGroup := writeRoutes.Group("/environments")
		environments.RegisterRoutes(envReadGroup, envWriteGroup, envHandler)

		tokenGroup := apiv1.Group("/tokens",
			middleware.RequirePermission(domain.PermissionTokenManage), middleware.RequireHuman())
		tokens.RegisterRoutes(tokenGroup, tokenHandler)

		// Owner-only member administration; API tokens are refused.
		memberGroup := apiv1.Group("/members",
			middleware.RequirePermission(domain.PermissionMembersManage), middleware.RequireHuman())
		members.RegisterRoutes(memberGroup, memberHandler)

		// Owner-only audit trail; API tokens are refused.
		auditGroup := apiv1.Group("/audit",
			middleware.RequirePermission(domain.PermissionAuditRead), middleware.RequireHuman())
		audit.RegisterRoutes(auditGroup, auditHandler)

		// Configuration and Secret Store changes require a human workspace owner.
		if repos.Configuration != nil {
			configuration.RegisterRoutes(apiv1.Group("/runtime-config", middleware.RequireOwner(), middleware.RequireHuman()), configuration.NewHandler(repos.Configuration))
		}
		if repos.Secrets != nil {
			secretapi.RegisterRoutes(apiv1.Group("/secrets", middleware.RequireOwner(), middleware.RequireHuman()), secretapi.NewHandler(repos.Secrets))
		}
		if repos.ManagedResources != nil {
			managedresource.RegisterRoutes(apiv1.Group("/managed"), managedresource.NewHandler(repos.ManagedResources))
		}

	}
}
