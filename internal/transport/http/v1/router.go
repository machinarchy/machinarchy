// Package v1 provides version 1 of the Machinarchy HTTP API,
// including its routes, handlers, and request/response types.
package v1

import (
	"time"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/runtimeconfig"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/sessionkey"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"

	// Sub-domain packages
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/agents"
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
	Users        domain.UserStore
	Environments domain.EnvironmentStore
	Access       domain.AccessStore
	Sessions     domain.SessionStore
	// SessionKeys signs and verifies login sessions; SessionTTL is their lifetime.
	SessionKeys   *sessionkey.Keyring
	SessionTTL    time.Duration
	Audit         domain.AuditStore
	Configuration *runtimeconfig.Service
	Secrets       *secrets.Service
	// OperationData is rewrapped together with secrets (AUDIT S-07).
	OperationData    secretapi.OperationDataRewrapper
	ManagedResources *service.ManagedResourceService
	// Agents lists enrolled agents; AgentGateway is nil where the gateway
	// does not run; AgentKeys is nil when it is not configured.
	Agents        agents.Lister
	AgentGateway  agents.Gateway
	AgentKeys     agents.SigningKeys
	SignupEnabled func() bool
}

// RegisterRoutes mounts the public /auth routes and the authenticated /v1
// routes on the API group. Each package's routes.go owns its prefix and its
// access rule (permission, owner, human session); this function only builds
// the handlers and mounts the packages in one place. The session keys and TTL
// come from repos. Optional packages are mounted only when their dependency is
// configured.
func RegisterRoutes(
	api *echo.Group,
	repos Repositories,
	authServ domain.AuthService,
	secureCookies bool,
	signupEnabled bool,
	tokenService domain.APITokenService,
	memberService domain.MemberService,
	auditService domain.AuditService,
) {
	authHandler := auth.NewHandler(authServ, secureCookies, signupEnabled)
	if repos.SignupEnabled != nil {
		authHandler.SetSignupEnabledProvider(repos.SignupEnabled)
	}

	if repos.Access != nil {
		authHandler.SetTokenLookup(repos.Access)
	}

	// Public: no authentication middleware.
	auth.RegisterRoutes(api, authHandler)

	// Everything else needs a valid session or API token.
	v1 := api.Group("/v1",
		middleware.JWTAuth(repos.SessionKeys, repos.SessionTTL, repos.Access, repos.Sessions), middleware.AuditActor())
	auth.RegisterSessionRoutes(v1, authHandler)
	environments.RegisterRoutes(v1, environments.NewHandler(repos.Environments))
	tokens.RegisterRoutes(v1, tokens.NewHandler(tokenService))
	members.RegisterRoutes(v1, members.NewHandler(memberService))
	audit.RegisterRoutes(v1, audit.NewHandler(auditService))
	if repos.Configuration != nil {
		configuration.RegisterRoutes(v1, configuration.NewHandler(repos.Configuration))
	}
	if repos.Secrets != nil {
		secretapi.RegisterRoutes(v1, secretapi.NewHandler(repos.Secrets, repos.OperationData))
	}
	if repos.Agents != nil {
		agents.RegisterRoutes(v1, agents.NewHandler(repos.Agents, repos.AgentGateway, repos.AgentKeys))
	}
	if repos.ManagedResources != nil {
		managedresource.RegisterRoutes(v1, managedresource.NewHandler(repos.ManagedResources))
	}
}
