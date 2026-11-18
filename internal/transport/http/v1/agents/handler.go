// Package agents serves /api/v1/agents: enrolled host agents and synchronous
// capability calls through the agent gateway.
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// Lister reads enrolled agents (the store, available in every process).
type Lister interface {
	ListAgents(ctx context.Context) ([]agentgateway.Agent, error)
}

// Gateway runs capabilities and revokes agents; it is nil in processes that
// do not run the gateway (the standalone API).
type Gateway interface {
	Invoke(ctx context.Context, agentID, capability string, input json.RawMessage, timeout time.Duration) (*agentv1.Result, error)
	Revoke(ctx context.Context, agentID string) error
}

type Handler struct {
	lister    Lister
	gateway   Gateway
	publicKey string
}

// NewHandler creates the handler. publicKey is the controller key agents must
// trust (MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY), or "" when not configured.
func NewHandler(lister Lister, gateway Gateway, publicKey string) *Handler {
	return &Handler{lister: lister, gateway: gateway, publicKey: publicKey}
}

// RegisterRoutes mounts the routes; read needs inventory:read, execute needs
// runs:execute, owner needs the workspace owner.
func RegisterRoutes(read, execute, owner *echo.Group, h *Handler) {
	read.GET("", h.list)
	read.GET("/controller-key", h.controllerKey)
	execute.POST("/:id/invoke", h.invoke)
	owner.POST("/:id/revoke", h.revoke)
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
// @Success 200 {object} map[string]string
// @Failure 401 {object} httperr.Body
// @Failure 503 {object} httperr.Body
// @Router /api/v1/agents/controller-key [get]
func (h *Handler) controllerKey(c echo.Context) error {
	if h.publicKey == "" {
		return httperr.Status(c, http.StatusServiceUnavailable, "the agent gateway is not configured")
	}
	return c.JSON(http.StatusOK, map[string]string{"publicKey": h.publicKey})
}

type invokeRequest struct {
	Capability     string          `json:"capability"`
	Input          json.RawMessage `json:"input,omitempty" swaggertype:"object"`
	TimeoutSeconds int             `json:"timeoutSeconds"`
}

// invoke godoc
// @Summary Run a capability on an agent and wait for its result
// @Tags Agents
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Agent ID"
// @Param request body invokeRequest true "Capability call"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 503 {object} httperr.Body
// @Router /api/v1/agents/{id}/invoke [post]
func (h *Handler) invoke(c echo.Context) error {
	if _, ok := middleware.Principal(c); !ok {
		return httperr.Unauthorized(c)
	}
	if h.gateway == nil {
		return httperr.Status(c, http.StatusServiceUnavailable, "this process does not run the agent gateway")
	}
	var request invokeRequest
	if err := c.Bind(&request); err != nil || request.Capability == "" {
		return httperr.BadRequest(c, "capability is required")
	}
	timeout := time.Duration(request.TimeoutSeconds) * time.Second
	if request.TimeoutSeconds <= 0 {
		timeout = 15 * time.Second
	}
	// The call is synchronous, so it must finish inside the API's 25s
	// request deadline.
	if timeout > 20*time.Second {
		return httperr.BadRequest(c, "timeoutSeconds must be at most 20")
	}
	result, err := h.gateway.Invoke(c.Request().Context(), c.Param("id"), request.Capability, request.Input, timeout)
	if err != nil {
		return mapError(c, err)
	}
	return c.JSON(http.StatusOK, result)
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
