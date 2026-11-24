// Package agents serves /api/v1/agents: enrolled host agents and synchronous
// capability calls through the agent gateway.
package agents

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// Lister reads enrolled agents and manages enrolment tokens (the store,
// available in every process).
type Lister interface {
	ListAgents(ctx context.Context) ([]agentgateway.Agent, error)
	agentgateway.TokenStore
}

// Gateway revokes agents; it is nil in processes that
// do not run the gateway (the standalone API).
type Gateway interface {
	Revoke(ctx context.Context, agentID string) error
}

// SigningKeys manages the controller signing keys (agentkeys.Manager); nil
// when the gateway is not configured.
type SigningKeys interface {
	// PublicKeys are the keys agents must pin: active, then next.
	PublicKeys() []string
	List(ctx context.Context) ([]agentgateway.SigningKey, error)
	Stage(ctx context.Context, actor domain.Principal) (agentgateway.SigningKey, error)
	Activate(ctx context.Context, actor domain.Principal) (agentgateway.SigningKey, error)
	Discard(ctx context.Context, actor domain.Principal) (agentgateway.SigningKey, error)
}

type Handler struct {
	lister  Lister
	gateway Gateway
	keys    SigningKeys
}

// NewHandler creates the handler. keys is nil when the gateway is not
// configured.
func NewHandler(lister Lister, gateway Gateway, keys SigningKeys) *Handler {
	return &Handler{lister: lister, gateway: gateway, keys: keys}
}

// RegisterRoutes mounts the routes; read needs inventory:read, owner needs
// the workspace owner. Agent work runs as capability Operations
// (/api/v1/managed/operations), not here (ADR-057).
func RegisterRoutes(read, owner *echo.Group, h *Handler) {
	read.GET("", h.list)
	read.GET("/controller-key", h.controllerKey)
	owner.POST("/:id/revoke", h.revoke)
	owner.POST("/enrollment-tokens", h.createToken)
	owner.GET("/enrollment-tokens", h.listTokens)
	owner.DELETE("/enrollment-tokens/:id", h.revokeToken)
	owner.GET("/signing-keys", h.listSigningKeys)
	owner.POST("/signing-keys/next", h.stageSigningKey)
	owner.POST("/signing-keys/next/activate", h.activateSigningKey)
	owner.DELETE("/signing-keys/next", h.discardSigningKey)
}

type createTokenRequest struct {
	// Hostname, when set, is the only hostname this token enrols.
	Hostname string `json:"hostname,omitempty"`
	// TTLSeconds defaults to 3600; 60 to 86400.
	TTLSeconds int `json:"ttlSeconds,omitempty"`
	// HostID binds the agent that enrols with this token to this managed
	// host (its Access Profile uses the agent connector), replacing any
	// earlier binding of the host (ADR-057).
	HostID string `json:"hostId,omitempty"`
}

// tokenView is an enrolment token as the API shows it: never the secret or
// its hash.
type tokenView struct {
	ID          string     `json:"id"`
	Hostname    string     `json:"hostname,omitempty"`
	HostID      string     `json:"hostId,omitempty"`
	CreatedBy   int        `json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	UsedAt      *time.Time `json:"usedAt,omitempty"`
	UsedByAgent string     `json:"usedByAgent,omitempty"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
}

func viewToken(token agentgateway.EnrollmentToken) tokenView {
	return tokenView{ID: token.ID, Hostname: token.Hostname, HostID: token.HostID, CreatedBy: token.CreatedBy, CreatedAt: token.CreatedAt,
		ExpiresAt: token.ExpiresAt, UsedAt: token.UsedAt, UsedByAgent: token.UsedByAgent, RevokedAt: token.RevokedAt}
}

type createTokenResponse struct {
	tokenView
	// Token is the secret, shown only in this response.
	Token string `json:"token"`
}

type tokenList struct {
	Items []tokenView `json:"items"`
}

// createToken godoc
// @Summary Issue a single-use agent enrolment token
// @Description Owner only. The secret is returned once and stored only as a hash. It enrols one agent before it expires
// @Description (default 1 hour), optionally only from the given hostname. Configure it on the host as
// @Description MACHINARCHY_AGENT_ENROLLMENT_TOKEN_FILE.
// @Tags Agents
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body createTokenRequest false "Optional hostname binding and lifetime"
// @Success 201 {object} createTokenResponse
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/agents/enrollment-tokens [post]
func (h *Handler) createToken(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	var request createTokenRequest
	if c.Request().ContentLength != 0 {
		if err := c.Bind(&request); err != nil {
			return httperr.BadRequest(c, "invalid request body")
		}
	}
	if request.TTLSeconds < 0 {
		return httperr.BadRequest(c, "ttlSeconds must be positive")
	}
	secret, token, err := agentgateway.NewEnrollmentToken(request.Hostname, time.Duration(request.TTLSeconds)*time.Second, principal.UserID, time.Now())
	if err != nil {
		return httperr.BadRequest(c, err.Error())
	}
	if request.HostID != "" {
		if resource.ID(request.HostID).Validate() != nil {
			return httperr.BadRequest(c, "hostId must be a managed host ID")
		}
		token.HostID = request.HostID
	}
	if err := h.lister.CreateEnrollmentToken(c.Request().Context(), token); err != nil {
		return mapError(c, err)
	}
	return c.JSON(http.StatusCreated, createTokenResponse{tokenView: viewToken(token), Token: secret})
}

// listTokens godoc
// @Summary List agent enrolment tokens
// @Description Owner only. Secrets are never returned.
// @Tags Agents
// @Produce json
// @Security BearerAuth
// @Success 200 {object} tokenList
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/agents/enrollment-tokens [get]
func (h *Handler) listTokens(c echo.Context) error {
	tokens, err := h.lister.ListEnrollmentTokens(c.Request().Context())
	if err != nil {
		return mapError(c, err)
	}
	views := make([]tokenView, 0, len(tokens))
	for _, token := range tokens {
		views = append(views, viewToken(token))
	}
	return c.JSON(http.StatusOK, tokenList{Items: views})
}

// revokeToken godoc
// @Summary Revoke an unused agent enrolment token
// @Tags Agents
// @Security BearerAuth
// @Param id path string true "Token ID"
// @Success 204
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Router /api/v1/agents/enrollment-tokens/{id} [delete]
func (h *Handler) revokeToken(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	if err := h.lister.RevokeEnrollmentToken(c.Request().Context(), c.Param("id"), principal.UserID, time.Now().UTC()); err != nil {
		if errors.Is(err, agentgateway.ErrEnrollmentTokenNotFound) {
			return httperr.Status(c, http.StatusNotFound, "no unused enrolment token with this ID")
		}
		return mapError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

type agentView struct {
	agentgateway.Agent
	Connected bool `json:"connected"`
}

// list godoc
// @Summary List enrolled agents
// @Tags Agents
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/agents [get]
func (h *Handler) list(c echo.Context) error {
	agents, err := h.lister.ListAgents(c.Request().Context())
	if err != nil {
		return httperr.FromError(c, err)
	}
	now := time.Now()
	items := make([]agentView, len(agents))
	for i, agent := range agents {
		items[i] = agentView{Agent: agent, Connected: agent.Connected(now)}
	}
	return c.JSON(http.StatusOK, map[string]any{"items": items})
}

// controllerKey godoc
// @Summary Get the controller public key agents must trust
// @Tags Agents
// @Produce json
// @Security BearerAuth
// @Success 200 {object} controllerKeyResponse
// @Failure 401 {object} httperr.Body
// @Failure 503 {object} httperr.Body
// @Router /api/v1/agents/controller-key [get]
func (h *Handler) controllerKey(c echo.Context) error {
	var keys []string
	if h.keys != nil {
		keys = h.keys.PublicKeys()
	}
	if len(keys) == 0 {
		return httperr.Status(c, http.StatusServiceUnavailable, "the agent gateway is not configured")
	}
	response := controllerKeyResponse{PublicKey: keys[0], PublicKeys: keys}
	if len(keys) > 1 {
		response.NextPublicKey = keys[1]
	}
	return c.JSON(http.StatusOK, response)
}

// controllerKeyResponse lists the keys agents must pin. During a signing-key
// rotation it holds two: pin both (comma-separated) before the controller
// switches.
type controllerKeyResponse struct {
	// PublicKey signs today.
	PublicKey string `json:"publicKey"`
	// NextPublicKey is announced for a rotation; empty otherwise.
	NextPublicKey string `json:"nextPublicKey,omitempty"`
	// PublicKeys is the value for MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY.
	PublicKeys []string `json:"publicKeys"`
}

// revoke godoc
// @Summary Revoke an agent
// @Tags Agents
// @Security BearerAuth
// @Param id path string true "Agent ID"
// @Success 204
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 503 {object} httperr.Body
// @Router /api/v1/agents/{id}/revoke [post]
func (h *Handler) revoke(c echo.Context) error {
	if h.gateway == nil {
		return httperr.Status(c, http.StatusServiceUnavailable, "this process does not run the agent gateway")
	}
	if err := h.gateway.Revoke(c.Request().Context(), c.Param("id")); err != nil {
		return mapError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func mapError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, agentgateway.ErrAgentNotFound):
		return httperr.Status(c, http.StatusNotFound, "agent not found")
	case errors.Is(err, agentgateway.ErrUnavailable):
		return httperr.Status(c, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return httperr.Status(c, http.StatusGatewayTimeout, "the agent did not answer in time")
	}
	return httperr.FromError(c, err, httperr.Mapping{Target: domain.ErrInvalidInput, Status: http.StatusBadRequest, Code: httperr.CodeInvalidRequest})
}

// signingKeyView is a signing key as the API shows it: public data only,
// never the secret reference or key material.
type signingKeyView struct {
	ID          string     `json:"id"`
	State       string     `json:"state" enums:"next,active,retired"`
	PublicKey   string     `json:"publicKey"`
	Origin      string     `json:"origin" enums:"generated,imported"`
	CreatedBy   int        `json:"createdBy,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	ActivatedAt *time.Time `json:"activatedAt,omitempty"`
	RetiredAt   *time.Time `json:"retiredAt,omitempty"`
}

func viewSigningKey(key agentgateway.SigningKey) signingKeyView {
	return signingKeyView{ID: key.ID, State: string(key.State), PublicKey: agentv1.EncodePublicKey(key.PublicKey),
		Origin: string(key.Origin), CreatedBy: key.CreatedBy, CreatedAt: key.CreatedAt, ActivatedAt: key.ActivatedAt, RetiredAt: key.RetiredAt}
}

// SigningKeysResponse lists the controller signing keys.
type SigningKeysResponse struct {
	Items []signingKeyView `json:"items"`
}

func (h *Handler) signingKeyAction(c echo.Context, act func(context.Context, domain.Principal) (agentgateway.SigningKey, error), status int) error {
	if h.keys == nil {
		return httperr.Status(c, http.StatusServiceUnavailable, "the agent gateway is not configured")
	}
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	key, err := act(c.Request().Context(), principal)
	switch {
	case errors.Is(err, agentgateway.ErrSigningKeyStaged):
		return httperr.Status(c, http.StatusConflict, "a next signing key is already staged; activate or discard it first")
	case errors.Is(err, agentgateway.ErrNoStagedSigningKey):
		return httperr.Status(c, http.StatusConflict, "no next signing key is staged")
	case err != nil:
		return mapError(c, err)
	}
	return c.JSON(status, viewSigningKey(key))
}

// listSigningKeys godoc
// @Summary List the controller signing keys (owner)
// @Description Active signs everything sent to agents; next is announced for a rotation; retired keys are kept for the record. Private keys are never returned.
// @Tags Agents
// @Produce json
// @Security BearerAuth
// @Success 200 {object} SigningKeysResponse
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 503 {object} httperr.Body
// @Router /api/v1/agents/signing-keys [get]
func (h *Handler) listSigningKeys(c echo.Context) error {
	if h.keys == nil {
		return httperr.Status(c, http.StatusServiceUnavailable, "the agent gateway is not configured")
	}
	keys, err := h.keys.List(c.Request().Context())
	if err != nil {
		return mapError(c, err)
	}
	items := make([]signingKeyView, len(keys))
	for i, key := range keys {
		items[i] = viewSigningKey(key)
	}
	return c.JSON(http.StatusOK, SigningKeysResponse{Items: items})
}

// stageSigningKey godoc
// @Summary Stage the next controller signing key (owner)
// @Description Generates a key and announces it at /controller-key. Pin both keys on every agent before activating it.
// @Tags Agents
// @Produce json
// @Security BearerAuth
// @Success 201 {object} signingKeyView
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Failure 503 {object} httperr.Body
// @Router /api/v1/agents/signing-keys/next [post]
func (h *Handler) stageSigningKey(c echo.Context) error {
	return h.signingKeyAction(c, func(ctx context.Context, p domain.Principal) (agentgateway.SigningKey, error) {
		return h.keys.Stage(ctx, p)
	}, http.StatusCreated)
}

// activateSigningKey godoc
// @Summary Activate the next controller signing key (owner)
// @Description The next key starts signing; the active key retires. Agents that do not pin the next key reject every message after this.
// @Tags Agents
// @Produce json
// @Security BearerAuth
// @Success 200 {object} signingKeyView
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Failure 503 {object} httperr.Body
// @Router /api/v1/agents/signing-keys/next/activate [post]
func (h *Handler) activateSigningKey(c echo.Context) error {
	return h.signingKeyAction(c, func(ctx context.Context, p domain.Principal) (agentgateway.SigningKey, error) {
		return h.keys.Activate(ctx, p)
	}, http.StatusOK)
}

// discardSigningKey godoc
// @Summary Discard the next controller signing key (owner)
// @Tags Agents
// @Produce json
// @Security BearerAuth
// @Success 200 {object} signingKeyView
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Failure 503 {object} httperr.Body
// @Router /api/v1/agents/signing-keys/next [delete]
func (h *Handler) discardSigningKey(c echo.Context) error {
	return h.signingKeyAction(c, func(ctx context.Context, p domain.Principal) (agentgateway.SigningKey, error) {
		return h.keys.Discard(ctx, p)
	}, http.StatusOK)
}
