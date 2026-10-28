package host

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

type Handler struct {
	hosts        domain.HostStore
	jobs         domain.JobStore
	environments domain.EnvironmentStore
	service      service.HostService
}

func NewHandler(hosts domain.HostStore, jobs domain.JobStore, environments domain.EnvironmentStore, hostService service.HostService) *Handler {
	return &Handler{hosts: hosts, jobs: jobs, environments: environments, service: hostService}
}

type HostPayload struct {
	EnvironmentID       int               `json:"environmentId" example:"1"`
	Name                string            `json:"name" binding:"required" example:"web-01"`
	Hostname            string            `json:"hostname,omitempty" example:"web-01.example.com"`
	IP                  string            `json:"ip,omitempty" example:"192.0.2.10"`
	Connection          string            `json:"connection,omitempty" example:"SSH"`
	Labels              map[string]string `json:"labels,omitempty"`
	Port                int               `json:"port,omitempty" example:"22"`
	Username            string            `json:"username" binding:"required" example:"deploy"`
	AuthMethod          string            `json:"authMethod" binding:"required" enums:"PASSWORD,PRIVATE_KEY" example:"PRIVATE_KEY"`
	SecretData          string            `json:"secretData" binding:"required" example:"-----BEGIN OPENSSH PRIVATE KEY-----"`
	Passphrase          string            `json:"passphrase,omitempty" example:"key-passphrase"`
	ExpectedFingerprint string            `json:"expectedFingerprint" binding:"required" example:"SHA256:..."`
}

type HostJobsResponse struct {
	HostID int           `json:"hostId"`
	Data   []*domain.Job `json:"data"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

// Create registers a host and verifies its pinned SSH host key.
// @Summary Register a host
// @Description Registers a host after validating SSH connectivity and the expected host-key fingerprint. SecretData contains the SSH password or private key and is never returned.
// @Tags Hosts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body HostPayload true "Host connection details; provide either ip or hostname. SecretData is the SSH password or private key and is never returned."
// @Success 201 {object} domain.Host
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 412 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/hosts [post]
func (h *Handler) Create(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	var payload HostPayload
	if err := c.Bind(&payload); err != nil {
		return badRequest(c, "invalid request body")
	}

	host, err := payload.host("")
	if err != nil {
		return badRequest(c, err.Error())
	}
	host.WorkspaceID = principal.WorkspaceID
	if err := h.validateEnvironment(c, principal.WorkspaceID, host.EnvironmentID); err != nil {
		return err
	}
	if err := h.service.RegisterHost(c.Request().Context(), host); err != nil {
		return hostServiceError(c, err)
	}
	return c.JSON(http.StatusCreated, host)
}

// List returns registered hosts without SSH credentials.
// @Summary List hosts
// @Tags Hosts
// @Produce json
// @Security BearerAuth
// @Success 200 {array} domain.Host
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/hosts [get]
func (h *Handler) List(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	hosts, err := h.hosts.ListAllHosts(c.Request().Context(), principal.WorkspaceID)
	if err != nil {
		return internalError(c)
	}
	return c.JSON(http.StatusOK, hosts)
}

// Update replaces a host's SSH connection details.
// @Summary Update a host
// @Description Updates host metadata and credentials. SecretData contains the SSH password or private key and is never returned.
// @Tags Hosts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Host ID"
// @Param request body HostPayload true "Updated host details"
// @Success 200 {object} domain.Host
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/hosts/{id} [put]
func (h *Handler) Update(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		return badRequest(c, "invalid host ID")
	}
	var payload HostPayload
	if err := c.Bind(&payload); err != nil {
		return badRequest(c, "invalid request body")
	}
	host, err := payload.host(strconv.Itoa(id))
	if err != nil {
		return badRequest(c, err.Error())
	}
	host.WorkspaceID = principal.WorkspaceID
	if err := h.validateEnvironment(c, principal.WorkspaceID, host.EnvironmentID); err != nil {
		return err
	}
	if err := host.SSHValidate(); err != nil {
		return badRequest(c, err.Error())
	}
	if err := h.hosts.UpdateHost(c.Request().Context(), host); err != nil {
		if errors.Is(err, domain.ErrHostNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "host not found"})
		}
		if errors.Is(err, domain.ErrEnvironmentNotFound) {
			return badRequest(c, "environment not found in this workspace")
		}
		if errors.Is(err, domain.ErrHostAlreadyExists) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "a host with the same name or address:port already exists"})
		}
		return internalError(c)
	}
	return c.JSON(http.StatusOK, host)
}

// Delete removes a host.
// @Summary Delete a host
// @Tags Hosts
// @Produce json
// @Security BearerAuth
// @Param id path int true "Host ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/hosts/{id} [delete]
func (h *Handler) Delete(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		return badRequest(c, "invalid host ID")
	}
	if err := h.hosts.DeleteHost(c.Request().Context(), principal.WorkspaceID, strconv.Itoa(id)); err != nil {
		if errors.Is(err, domain.ErrHostNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "host not found"})
		}
		return internalError(c)
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "host deleted"})
}

// ListJobsForHost returns paginated jobs for a host.
// @Summary List jobs for a host
// @Tags Hosts
// @Produce json
// @Security BearerAuth
// @Param id path int true "Host ID"
// @Param limit query int false "Page size (1-100)" default(10)
// @Param offset query int false "Number of rows to skip" default(0)
// @Success 200 {object} HostJobsResponse
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/hosts/{id}/jobs [get]
func (h *Handler) ListJobsForHost(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	hostID, err := strconv.Atoi(c.Param("id"))
	if err != nil || hostID < 1 {
		return badRequest(c, "invalid host ID")
	}
	limit, err := queryInt(c, "limit", 10)
	if err != nil || limit < 1 || limit > 100 {
		return badRequest(c, "limit must be between 1 and 100")
	}
	offset, err := queryInt(c, "offset", 0)
	if err != nil || offset < 0 {
		return badRequest(c, "offset must be non-negative")
	}
	jobs, total, err := h.jobs.ListJobs(c.Request().Context(), domain.JobFilter{
		WorkspaceID: principal.WorkspaceID,
		HostID:      hostID,
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		return internalError(c)
	}
	return c.JSON(http.StatusOK, HostJobsResponse{
		HostID: hostID, Data: jobs, Total: total, Limit: limit, Offset: offset,
	})
}

// BulkHostsRequest is the body of a bulk registration request.
type BulkHostsRequest struct {
	Hosts []HostPayload `json:"hosts"`
	// ValidateOnly checks records and duplicates without contacting hosts or
	// storing anything.
	ValidateOnly bool `json:"validate_only,omitempty"`
}

// BulkCreate registers up to 100 hosts with per-record results.
// @Summary Bulk register hosts
// @Description Registers many hosts in one request with partial-success semantics: each record is validated, checked for duplicates (name or address:port, against the inventory and earlier records), SSH-verified against its pinned fingerprint, and stored independently. The response is 200 whenever the request itself is valid; inspect each result's status. Set validate_only to check records without contacting hosts or storing anything. SecretData is never returned. No commands are executed.
// @Tags Hosts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body BulkHostsRequest true "Up to 100 host records"
// @Success 200 {object} domain.BulkImportResponse
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/hosts/bulk [post]
func (h *Handler) BulkCreate(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	var req BulkHostsRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "invalid request body")
	}
	if len(req.Hosts) == 0 {
		return badRequest(c, "hosts list cannot be empty")
	}
	if len(req.Hosts) > service.MaxBulkRegisterHosts {
		return badRequest(c, "maximum batch size exceeded (limit: "+strconv.Itoa(service.MaxBulkRegisterHosts)+" hosts per request)")
	}

	// Each record is converted and checked independently; a bad record becomes a
	// per-record failure instead of rejecting the batch. Environment lookups are
	// cached so a large batch does not repeat them.
	environments := make(map[int]error)
	entries := make([]domain.BulkHostEntry, len(req.Hosts))
	for i, payload := range req.Hosts {
		host, err := payload.host("")
		if err != nil {
			entries[i] = domain.BulkHostEntry{PreError: err}
			continue
		}
		host.WorkspaceID = principal.WorkspaceID
		envErr, cached := environments[host.EnvironmentID]
		if !cached {
			envErr = h.checkEnvironment(c, principal.WorkspaceID, host.EnvironmentID)
			environments[host.EnvironmentID] = envErr
		}
		if envErr != nil {
			entries[i] = domain.BulkHostEntry{Host: host, PreError: envErr}
			continue
		}
		entries[i] = domain.BulkHostEntry{Host: host}
	}

	res, err := h.service.BulkRegisterHosts(c.Request().Context(), principal.WorkspaceID, entries,
		service.BulkRegisterOptions{ValidateOnly: req.ValidateOnly})
	if err != nil {
		if errors.Is(err, domain.ErrInvalidInput) {
			return badRequest(c, err.Error())
		}
		return internalError(c)
	}
	return c.JSON(http.StatusOK, res)
}

func (p HostPayload) host(id string) (*domain.Host, error) {
	name := strings.TrimSpace(p.Name)
	address := strings.TrimSpace(p.IP)
	if address == "" {
		address = strings.TrimSpace(p.Hostname)
	}
	username := strings.TrimSpace(p.Username)
	if name == "" || address == "" || username == "" {
		return nil, errors.New("name, host address, and SSH username are required")
	}
	method, err := domain.ParseAuthType(p.AuthMethod)
	if err != nil {
		return nil, err
	}
	auth := domain.SSHAuthType{Method: method, Passphrase: p.Passphrase, ExpectedFingerprint: p.ExpectedFingerprint}
	if method == domain.AuthTypePassword {
		auth.Password = p.SecretData
	} else {
		auth.PrivateKey = p.SecretData
	}
	connection := domain.ConnectionMethod(strings.ToUpper(strings.TrimSpace(p.Connection)))
	if connection == "" {
		connection = domain.MethodSSH
	}
	return &domain.Host{
		ID:            id,
		EnvironmentID: p.EnvironmentID,
		Name:          name,
		Hostname:      strings.TrimSpace(p.Hostname),
		IPAddress:     address,
		Connection:    connection,
		Labels:        p.Labels,
		SSH: &domain.SSHAccess{
			Username: username,
			Port:     p.Port,
			AuthType: auth,
		},
	}, nil
}

func queryInt(c echo.Context, name string, fallback int) (int, error) {
	value := c.QueryParam(name)
	if value == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}

func (h *Handler) validateEnvironment(c echo.Context, workspaceID, environmentID int) error {
	if err := h.checkEnvironment(c, workspaceID, environmentID); err != nil {
		if errors.Is(err, errEnvironmentLookup) {
			return internalError(c)
		}
		return badRequest(c, err.Error())
	}
	return nil
}

// errEnvironmentLookup marks an infrastructure failure, as opposed to a bad
// reference supplied by the client.
var errEnvironmentLookup = errors.New("environment lookup failed")

// checkEnvironment verifies an environment reference without writing a response.
// Client mistakes return a safe message; infrastructure errors wrap
// errEnvironmentLookup.
func (h *Handler) checkEnvironment(c echo.Context, workspaceID, environmentID int) error {
	if environmentID < 0 {
		return errors.New("environment ID must be non-negative")
	}
	if environmentID == 0 {
		return nil
	}
	if h.environments == nil {
		return errEnvironmentLookup
	}
	if _, err := h.environments.GetEnvironmentByID(c.Request().Context(), workspaceID, environmentID); err != nil {
		if errors.Is(err, domain.ErrEnvironmentNotFound) {
			return errors.New("environment not found in this workspace")
		}
		return errEnvironmentLookup
	}
	return nil
}

func hostServiceError(c echo.Context, err error) error {
	var unknownKey *domain.ErrHostKeyUnknown
	switch {
	case errors.As(err, &unknownKey):
		return c.JSON(http.StatusPreconditionFailed, map[string]string{
			"error": "untrusted SSH host key", "algorithm": unknownKey.Algorithm,
			"fingerprint": unknownKey.Fingerprint,
		})
	case errors.Is(err, domain.ErrAuthenticationFailed):
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "SSH authentication failed"})
	case errors.Is(err, domain.ErrTargetNotAllowed):
		return badRequest(c, "target address is not permitted by the network policy")
	case errors.Is(err, domain.ErrHostAlreadyExists):
		return c.JSON(http.StatusConflict, map[string]string{"error": "a host with the same name or address:port already exists"})
	case errors.Is(err, domain.ErrInvalidInput):
		return badRequest(c, err.Error())
	case errors.Is(err, domain.ErrEnvironmentNotFound):
		return badRequest(c, "environment not found in this workspace")
	case errors.Is(err, domain.ErrServiceBusy):
		c.Response().Header().Set(echo.HeaderRetryAfter, "1")
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "host verification capacity is temporarily full"})
	default:
		return internalError(c)
	}
}

func badRequest(c echo.Context, message string) error {
	return c.JSON(http.StatusBadRequest, map[string]string{"error": message})
}

func internalError(c echo.Context) error {
	return c.JSON(http.StatusInternalServerError, map[string]string{"error": "internal server error"})
}
