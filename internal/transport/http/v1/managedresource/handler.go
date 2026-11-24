// Package managedresource serves /api/v1/managed: managed Hosts, Access
// Profiles and Operations. Authorization is enforced by the application
// service; every error uses the httperr format.
package managedresource

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

type Handler struct {
	service *service.ManagedResourceService
}

func NewHandler(application *service.ManagedResourceService) *Handler {
	return &Handler{service: application}
}

type hostRequest struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Spec   resource.HostSpec `json:"spec"`
}
type hostUpdateRequest struct {
	Revision   uint64            `json:"revision"`
	Generation uint64            `json:"generation"`
	Spec       resource.HostSpec `json:"spec"`
}
type labelsRequest struct {
	Revision uint64            `json:"revision"`
	Labels   map[string]string `json:"labels"`
}
type profileRequest struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Spec        resource.AccessProfileSpec `json:"spec"`
}
type profileUpdateRequest struct {
	ExpectedRevision uint64                     `json:"expectedRevision"`
	Spec             resource.AccessProfileSpec `json:"spec"`
}

// operationRequest targets explicit hostIds, or a rule (selector, include,
// exclude, all) resolved at submission. expectTargetsHash, from a preview,
// makes the submission fail with 409 targets_changed if the set differs.
// Exactly one of run (a program over SSH) and capability (an agent
// capability, ADR-057) is given.
type operationRequest struct {
	HostIDs           []resource.ID            `json:"hostIds,omitempty"`
	Targets           *domain.TargetRule       `json:"targets,omitempty"`
	ExpectTargetsHash string                   `json:"expectTargetsHash,omitempty"`
	Run               *resource.RunSpec        `json:"run,omitempty"`
	Capability        *resource.CapabilitySpec `json:"capability,omitempty"`
}

// trustRequest approves SSH host keys for a Host.
type trustRequest struct {
	// Generation is the Host spec generation the caller reviewed.
	Generation uint64 `json:"generation"`
	// Fingerprints are the exact SHA256:… fingerprints to trust, normally the
	// Host's status.observedHostKeys as reviewed.
	Fingerprints []string `json:"fingerprints"`
	// Replace must be set to change keys the Host already trusts.
	Replace bool `json:"replace,omitempty"`
}
type previewRequest struct {
	Targets domain.TargetRule `json:"targets"`
}

func RegisterRoutes(group *echo.Group, handler *Handler) {
	group.GET("/hosts", handler.listHosts)
	group.POST("/hosts", handler.createHost)
	group.GET("/hosts/:id", handler.getHost)
	group.PUT("/hosts/:id", handler.updateHost)
	group.DELETE("/hosts/:id", handler.deleteHost)
	group.PATCH("/hosts/:id/labels", handler.setHostLabels)
	group.POST("/hosts/:id/trust", handler.trustHostKeys)
	group.GET("/access-profiles", handler.listProfiles)
	group.POST("/access-profiles", handler.createProfile)
	group.GET("/access-profiles/:id", handler.getProfile)
	group.PUT("/access-profiles/:id", handler.updateProfile)
	group.DELETE("/access-profiles/:id", handler.deleteProfile)
	group.GET("/operations", handler.listOperations)
	group.POST("/operations", handler.submitOperation)
	group.POST("/operations/preview", handler.previewOperation)
	group.GET("/operations/:id", handler.getOperation)
	group.POST("/operations/:id/cancel", handler.cancelOperation)
	group.GET("/operations/:id/targets/:hostId/output", handler.targetOutput)
}

// call runs one use case: it resolves the principal, lets parse read the
// request, and writes either the result with status or a mapped error.
func call[T any](c echo.Context, status int, parse func() error, run func(context.Context, domain.Principal) (T, error)) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	if parse != nil {
		if err := parse(); err != nil {
			return httperr.BadRequest(c, "")
		}
	}
	result, err := run(c.Request().Context(), principal)
	if err != nil {
		return httperr.FromError(c, err, httperr.Mapping{Target: service.ErrAccessProfileOwnerRequired,
			Status: http.StatusForbidden, Code: httperr.CodePermissionDenied, Message: "workspace owner permission is required"})
	}
	if status == http.StatusNoContent {
		return c.NoContent(status)
	}
	return c.JSON(status, result)
}

func bind(c echo.Context, target any) func() error { return func() error { return c.Bind(target) } }

func pathID(c echo.Context, id *resource.ID) func() error {
	return func() (err error) {
		*id, err = resource.ParseResourceID(c.Param("id"))
		return err
	}
}

func all(steps ...func() error) func() error {
	return func() error {
		for _, step := range steps {
			if err := step(); err != nil {
				return err
			}
		}
		return nil
	}
}

func queryInt(c echo.Context, name string, target *int) func() error {
	return func() error {
		raw := c.QueryParam(name)
		if raw == "" {
			return nil
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return strconv.ErrSyntax
		}
		*target = value
		return nil
	}
}

func queryTime(c echo.Context, name string, target *time.Time) func() error {
	return func() (err error) {
		if raw := c.QueryParam(name); raw != "" {
			*target, err = time.Parse(time.RFC3339, raw)
		}
		return err
	}
}

// @Summary List managed hosts
// @Description Keyset-paginated; filter by selector, environment ID and phase.
// @Tags Managed
// @Produce json
// @Security BearerAuth
// @Param selector query string false "Selector, for example tier=web"
// @Param environment query int false "Environment ID"
// @Param phase query string false "Pending, Active or Deleting"
// @Param cursor query string false "nextCursor of the previous page"
// @Param limit query int false "Page size, at most 500"
// @Success 200 {object} resource.Page[resource.Host]
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/managed/hosts [get]
func (h *Handler) listHosts(c echo.Context) error {
	query := service.HostQuery{Selector: c.QueryParam("selector"), Phase: resource.HostPhase(c.QueryParam("phase")),
		After: resource.ID(c.QueryParam("cursor"))}
	return call(c, http.StatusOK, all(queryInt(c, "environment", &query.EnvironmentID), queryInt(c, "limit", &query.Limit)),
		func(ctx context.Context, p domain.Principal) (*resource.Page[resource.Host], error) {
			return h.service.ListHosts(ctx, p, query)
		})
}

// @Summary Create a managed host
// @Description Binding an Access Profile needs the workspace owner.
// @Tags Managed
// @Produce json
// @Accept json
// @Security BearerAuth
// @Param request body hostRequest true "Request"
// @Success 201 {object} resource.Host
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/managed/hosts [post]
func (h *Handler) createHost(c echo.Context) error {
	var request hostRequest
	return call(c, http.StatusCreated, bind(c, &request), func(ctx context.Context, p domain.Principal) (*resource.Host, error) {
		return h.service.CreateHost(ctx, p, request.Name, request.Labels, request.Spec)
	})
}

// @Summary Get a managed host
// @Tags Managed
// @Produce json
// @Security BearerAuth
// @Param id path string true "Host ID"
// @Success 200 {object} resource.Host
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Router /api/v1/managed/hosts/{id} [get]
func (h *Handler) getHost(c echo.Context) error {
	var id resource.ID
	return call(c, http.StatusOK, pathID(c, &id), func(ctx context.Context, p domain.Principal) (*resource.Host, error) {
		return h.service.GetHost(ctx, p, id)
	})
}

// @Summary Replace a managed host spec
// @Description Changing the profile, endpoint or trust of a bound host needs the owner.
// @Tags Managed
// @Produce json
// @Accept json
// @Security BearerAuth
// @Param id path string true "Host ID"
// @Param request body hostUpdateRequest true "Request"
// @Success 204
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/managed/hosts/{id} [put]
func (h *Handler) updateHost(c echo.Context) error {
	var id resource.ID
	var request hostUpdateRequest
	return call(c, http.StatusNoContent, all(pathID(c, &id), bind(c, &request)), func(ctx context.Context, p domain.Principal) (any, error) {
		return nil, h.service.UpdateHost(ctx, p, id, request.Revision, request.Generation, request.Spec)
	})
}

// @Summary Delete a managed host
// @Description Refused with 409 while an operation targets the host.
// @Tags Managed
// @Produce json
// @Security BearerAuth
// @Param id path string true "Host ID"
// @Param revision query int true "Current revision"
// @Success 204
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/managed/hosts/{id} [delete]
func (h *Handler) deleteHost(c echo.Context) error {
	var id resource.ID
	var revision int
	return call(c, http.StatusNoContent, all(pathID(c, &id), queryInt(c, "revision", &revision)), func(ctx context.Context, p domain.Principal) (any, error) {
		if revision <= 0 {
			return nil, domain.ErrInvalidInput
		}
		return nil, h.service.DeleteHost(ctx, p, id, uint64(revision))
	})
}

// @Summary Replace a host's labels
// @Tags Managed
// @Produce json
// @Accept json
// @Security BearerAuth
// @Param id path string true "Host ID"
// @Param request body labelsRequest true "Request"
// @Success 200 {object} resource.Host
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/managed/hosts/{id}/labels [patch]
func (h *Handler) setHostLabels(c echo.Context) error {
	var id resource.ID
	var request labelsRequest
	return call(c, http.StatusOK, all(pathID(c, &id), bind(c, &request)), func(ctx context.Context, p domain.Principal) (*resource.Host, error) {
		return h.service.SetHostLabels(ctx, p, id, request.Revision, request.Labels)
	})
}

// @Summary Trust a host's SSH host keys
// @Description Workspace owner only. Name the exact fingerprints you reviewed (usually status.observedHostKeys) and the
// @Description spec generation you saw. A Host that already trusts keys needs replace=true, so a changed key is never
// @Description accepted by accident. The Host is then verified again with the trusted keys.
// @Tags Managed
// @Produce json
// @Accept json
// @Security BearerAuth
// @Param id path string true "Host ID"
// @Param request body trustRequest true "Reviewed fingerprints"
// @Success 200 {object} resource.Host
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/managed/hosts/{id}/trust [post]
func (h *Handler) trustHostKeys(c echo.Context) error {
	var id resource.ID
	var request trustRequest
	return call(c, http.StatusOK, all(pathID(c, &id), bind(c, &request)), func(ctx context.Context, p domain.Principal) (*resource.Host, error) {
		return h.service.TrustHostKeys(ctx, p, id, request.Generation, request.Fingerprints, request.Replace)
	})
}

// @Summary List Access Profiles
// @Description Returns SecretRefs only, never secret values.
// @Tags Managed
// @Produce json
// @Security BearerAuth
// @Param cursor query string false "nextCursor of the previous page"
// @Param limit query int false "Page size, at most 500"
// @Success 200 {object} resource.Page[resource.AccessProfile]
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/managed/access-profiles [get]
func (h *Handler) listProfiles(c echo.Context) error {
	var limit int
	return call(c, http.StatusOK, queryInt(c, "limit", &limit), func(ctx context.Context, p domain.Principal) (*resource.Page[resource.AccessProfile], error) {
		return h.service.ListAccessProfiles(ctx, p, resource.ID(c.QueryParam("cursor")), limit)
	})
}

// @Summary Create an Access Profile
// @Description Workspace owner only.
// @Tags Managed
// @Produce json
// @Accept json
// @Security BearerAuth
// @Param request body profileRequest true "Request"
// @Success 201 {object} resource.AccessProfile
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/managed/access-profiles [post]
func (h *Handler) createProfile(c echo.Context) error {
	var request profileRequest
	return call(c, http.StatusCreated, bind(c, &request), func(ctx context.Context, p domain.Principal) (*resource.AccessProfile, error) {
		return h.service.CreateAccessProfile(ctx, p, request.Name, request.Description, request.Spec)
	})
}

// @Summary Get an Access Profile with its current spec
// @Tags Managed
// @Produce json
// @Security BearerAuth
// @Param id path string true "Access Profile ID"
// @Success 200 {object} service.AccessProfileView
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Router /api/v1/managed/access-profiles/{id} [get]
func (h *Handler) getProfile(c echo.Context) error {
	var id resource.ID
	return call(c, http.StatusOK, pathID(c, &id), func(ctx context.Context, p domain.Principal) (*service.AccessProfileView, error) {
		return h.service.GetAccessProfile(ctx, p, id)
	})
}

// @Summary Create a new Access Profile revision
// @Description Workspace owner only.
// @Tags Managed
// @Produce json
// @Accept json
// @Security BearerAuth
// @Param id path string true "Access Profile ID"
// @Param request body profileUpdateRequest true "Request"
// @Success 201 {object} resource.AccessProfileRevision
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/managed/access-profiles/{id} [put]
func (h *Handler) updateProfile(c echo.Context) error {
	var id resource.ID
	var request profileUpdateRequest
	return call(c, http.StatusCreated, all(pathID(c, &id), bind(c, &request)), func(ctx context.Context, p domain.Principal) (*resource.AccessProfileRevision, error) {
		return h.service.UpdateAccessProfile(ctx, p, id, request.ExpectedRevision, request.Spec)
	})
}

// @Summary Delete an Access Profile
// @Description Owner only; refused with 409 while a host is bound to it.
// @Tags Managed
// @Produce json
// @Security BearerAuth
// @Param id path string true "Access Profile ID"
// @Success 204
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/managed/access-profiles/{id} [delete]
func (h *Handler) deleteProfile(c echo.Context) error {
	var id resource.ID
	return call(c, http.StatusNoContent, pathID(c, &id), func(ctx context.Context, p domain.Principal) (any, error) {
		return nil, h.service.DeleteAccessProfile(ctx, p, id)
	})
}

// @Summary List operations
// @Description Newest first; summaries never contain argv or output.
// @Tags Managed
// @Produce json
// @Security BearerAuth
// @Param state query string false "Operation state"
// @Param host query string false "Host ID"
// @Param createdAfter query string false "RFC 3339 time"
// @Param createdBefore query string false "RFC 3339 time"
// @Param cursor query string false "nextCursor of the previous page"
// @Param limit query int false "Page size, at most 500"
// @Success 200 {object} resource.Page[resource.OperationSummary]
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/managed/operations [get]
func (h *Handler) listOperations(c echo.Context) error {
	filter := resource.OperationListFilter{State: resource.OperationState(c.QueryParam("state")),
		HostID: resource.ID(c.QueryParam("host")), After: c.QueryParam("cursor")}
	parse := all(queryInt(c, "limit", &filter.Limit), queryTime(c, "createdAfter", &filter.CreatedAfter),
		queryTime(c, "createdBefore", &filter.CreatedBefore))
	return call(c, http.StatusOK, parse, func(ctx context.Context, p domain.Principal) (*resource.Page[resource.OperationSummary], error) {
		return h.service.ListOperations(ctx, p, filter)
	})
}

// @Summary Submit an operation
// @Description Target hostIds, or a rule; expectTargetsHash binds the run to a preview (409 targets_changed).
// @Description Give run (argv over SSH, hosts with an ssh Access Profile) or capability (an agent capability such as
// @Description runtime.info or service.status, hosts with an agent Access Profile), not both. Results arrive per target.
// @Tags Managed
// @Produce json
// @Accept json
// @Security BearerAuth
// @Param request body operationRequest true "Request"
// @Success 202 {object} resource.Operation
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/managed/operations [post]
func (h *Handler) submitOperation(c echo.Context) error {
	var request operationRequest
	return call(c, http.StatusAccepted, bind(c, &request), func(ctx context.Context, p domain.Principal) (*resource.Operation, error) {
		// Permission first: a reader learns nothing about the request's shape.
		if !p.Role.Allows(domain.PermissionRunsExecute) {
			return nil, domain.ErrPermissionDenied
		}
		switch {
		case (request.Run == nil) == (request.Capability == nil):
			return nil, domainInput("give either run or capability")
		case request.Targets != nil && len(request.HostIDs) > 0:
			return nil, domainInput("give either hostIds or targets, not both")
		case request.Targets == nil && request.ExpectTargetsHash != "":
			return nil, domainInput("expectTargetsHash requires targets")
		case request.Capability != nil && request.Targets != nil:
			return h.service.SubmitCapabilityForRule(ctx, p, *request.Targets, request.ExpectTargetsHash, *request.Capability)
		case request.Capability != nil:
			return h.service.SubmitCapability(ctx, p, request.HostIDs, *request.Capability)
		case request.Targets != nil:
			return h.service.SubmitOperationForRule(ctx, p, *request.Targets, request.ExpectTargetsHash, *request.Run)
		default:
			return h.service.SubmitOperation(ctx, p, request.HostIDs, *request.Run)
		}
	})
}

// @Summary Preview the hosts a target rule selects
// @Tags Managed
// @Produce json
// @Accept json
// @Security BearerAuth
// @Param request body previewRequest true "Request"
// @Success 200 {object} resource.HostTargetSet
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/managed/operations/preview [post]
func (h *Handler) previewOperation(c echo.Context) error {
	var request previewRequest
	return call(c, http.StatusOK, bind(c, &request), func(ctx context.Context, p domain.Principal) (*resource.HostTargetSet, error) {
		return h.service.PreviewTargets(ctx, p, request.Targets)
	})
}

// @Summary Get an operation
// @Description Readers see no argv or output.
// @Tags Managed
// @Produce json
// @Security BearerAuth
// @Param id path string true "Operation ID"
// @Success 200 {object} resource.Operation
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Router /api/v1/managed/operations/{id} [get]
func (h *Handler) getOperation(c echo.Context) error {
	var id resource.ID
	return call(c, http.StatusOK, pathID(c, &id), func(ctx context.Context, p domain.Principal) (*resource.Operation, error) {
		return h.service.GetOperation(ctx, p, id)
	})
}

// @Summary Get one target's output
// @Description One stream of one target's output: live while a detached command runs (poll it to follow), final afterwards. Long output keeps its first and last 15 KiB; bytes is the full size. Only callers allowed to execute runs may read output.
// @Tags Managed
// @Produce json
// @Security BearerAuth
// @Param id path string true "Operation ID"
// @Param hostId path string true "Target Host ID"
// @Param stream query string false "stdout (default) or stderr"
// @Param from query int false "Stream byte offset to start from (the previous response's next)"
// @Success 200 {object} resource.TargetOutput
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Router /api/v1/managed/operations/{id}/targets/{hostId}/output [get]
func (h *Handler) targetOutput(c echo.Context) error {
	var id, hostID resource.ID
	stream := resource.OutputStream(c.QueryParam("stream"))
	if stream == "" {
		stream = resource.StreamStdout
	}
	var from int64
	parse := all(pathID(c, &id), func() (err error) {
		hostID, err = resource.ParseResourceID(c.Param("hostId"))
		return err
	}, func() (err error) {
		if raw := c.QueryParam("from"); raw != "" {
			from, err = strconv.ParseInt(raw, 10, 64)
		}
		return err
	})
	return call(c, http.StatusOK, parse, func(ctx context.Context, p domain.Principal) (*resource.TargetOutput, error) {
		return h.service.TargetOutput(ctx, p, id, hostID, stream, from)
	})
}

// @Summary Request cancellation of an operation
// @Tags Managed
// @Produce json
// @Security BearerAuth
// @Param id path string true "Operation ID"
// @Success 202 {object} resource.Operation
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/v1/managed/operations/{id}/cancel [post]
func (h *Handler) cancelOperation(c echo.Context) error {
	var id resource.ID
	return call(c, http.StatusAccepted, pathID(c, &id), func(ctx context.Context, p domain.Principal) (*resource.Operation, error) {
		return h.service.CancelOperation(ctx, p, id)
	})
}

type inputError string

func (e inputError) Error() string        { return string(e) }
func (e inputError) Is(target error) bool { return target == domain.ErrInvalidInput }

func domainInput(message string) error { return inputError(message) }
