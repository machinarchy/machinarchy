// Package runs provides HTTP handlers and routes for run management operations.
package runs

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// Handler handles HTTP requests for run management operations.
type Handler struct {
	service domain.RunService
}

// NewHandler creates a run HTTP handler using the provided run service.
func NewHandler(service domain.RunService) *Handler {
	return &Handler{service: service}
}

// SubmitRequest describes a command and the hosts to run it on.
type SubmitRequest struct {
	// Argv is the program followed by its arguments. There is no shell: each
	// element reaches the program as exactly one argument.
	Argv []string `json:"argv"`
	// Selector, Include, Exclude, and All form the target rule exactly as in the
	// target preview. At least one of selector, include, or all is required.
	Selector string   `json:"selector,omitempty" example:"environment=production,role=web"`
	Include  []string `json:"include,omitempty"`
	Exclude  []string `json:"exclude,omitempty"`
	All      bool     `json:"all,omitempty"`
	// TimeoutSeconds bounds the command on each host (default 60, maximum 900).
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// Concurrency bounds hosts processed at once (default 5, maximum 20).
	Concurrency int `json:"concurrency,omitempty"`
	// ExpectTargetsHash, copied from a preview, makes the request fail with 409
	// unless it would run on exactly the previewed hosts.
	ExpectTargetsHash string `json:"expectTargetsHash,omitempty"`
	// DryRun validates and plans without storing a run or contacting any host.
	DryRun bool `json:"dryRun,omitempty"`
}

// Submit starts a run.
// @Summary Run a command on hosts
// @Description Disabled unless the server enables execution and lists the allowed programs. Requires the runs:execute permission (owners and operators). The command is an argument list with no shell, the targets are resolved and pinned when the run is accepted, and the run executes in the background: poll GET /api/v1/runs/{id}. Output is combined stdout and stderr. Commands are never retried; a timeout or cancellation does not prove the command did not run. The 202 response carries the run; with dryRun the plan is returned with 200 and nothing is stored or contacted.
// @Tags Runs
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body SubmitRequest true "Command and target rule"
// @Success 202 {object} domain.Run
// @Success 200 {object} domain.Run "Dry run plan"
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 409 {object} map[string]interface{} "The targeted hosts changed since the preview"
// @Failure 503 {object} map[string]string
// @Router /api/v1/runs [post]
func (h *Handler) Submit(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	var request SubmitRequest
	if err := c.Bind(&request); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	run, err := h.service.Submit(c.Request().Context(), principal, domain.RunRequest{
		Argv:              request.Argv,
		Rule:              domain.TargetRule{Selector: request.Selector, Include: request.Include, Exclude: request.Exclude, All: request.All},
		TimeoutSeconds:    request.TimeoutSeconds,
		Concurrency:       request.Concurrency,
		ExpectTargetsHash: request.ExpectTargetsHash,
		DryRun:            request.DryRun,
	})
	if err != nil {
		return runError(c, err)
	}
	if request.DryRun {
		return c.JSON(http.StatusOK, run)
	}
	c.Response().Header().Set(echo.HeaderLocation, "/api/v1/runs/"+strconv.FormatInt(run.ID, 10))
	return c.JSON(http.StatusAccepted, run)
}

// List returns recent runs.
// @Summary List runs
// @Description Newest first, without arguments or per-host results. Requires operations read permission.
// @Tags Runs
// @Produce json
// @Security BearerAuth
// @Param status query string false "Only runs with this status"
// @Param before_id query int false "Only runs with a smaller id (paging cursor)"
// @Param limit query int false "Page size (default 50, maximum 200)"
// @Success 200 {array} domain.Run
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /api/v1/runs [get]
func (h *Handler) List(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	filter := domain.RunFilter{Status: c.QueryParam("status")}
	if raw := c.QueryParam("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "limit must be a number"})
		}
		filter.Limit = limit
	}
	if raw := c.QueryParam("before_id"); raw != "" {
		before, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "before_id must be a number"})
		}
		filter.BeforeID = before
	}
	runs, err := h.service.List(c.Request().Context(), principal, filter)
	if err != nil {
		return runError(c, err)
	}
	return c.JSON(http.StatusOK, runs)
}

// Get returns one run with its per-host results.
// @Summary Get a run
// @Description Callers with the runs:execute permission also see the arguments and each host's output; others see only status, exit codes, and errors.
// @Tags Runs
// @Produce json
// @Security BearerAuth
// @Param id path int true "Run ID"
// @Success 200 {object} domain.Run
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/v1/runs/{id} [get]
func (h *Handler) Get(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	id, err := parseID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid run ID"})
	}
	run, err := h.service.Get(c.Request().Context(), principal, id)
	if err != nil {
		return runError(c, err)
	}
	return c.JSON(http.StatusOK, run)
}

// Cancel asks a run to stop.
// @Summary Cancel a run
// @Description Stops new hosts from starting and closes running SSH sessions. A command that was already sent may still have run, or keep running, on its host; cancelled hosts are reported as such. Requires the runs:execute permission.
// @Tags Runs
// @Produce json
// @Security BearerAuth
// @Param id path int true "Run ID"
// @Success 202 {object} domain.Run
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /api/v1/runs/{id}/cancel [post]
func (h *Handler) Cancel(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	id, err := parseID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid run ID"})
	}
	run, err := h.service.Cancel(c.Request().Context(), principal, id)
	if err != nil {
		return runError(c, err)
	}
	return c.JSON(http.StatusAccepted, run)
}

func parseID(c echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, errors.New("invalid ID")
	}
	return id, nil
}

func runError(c echo.Context, err error) error {
	var changed *domain.TargetsChangedError
	switch {
	case errors.As(err, &changed):
		return c.JSON(http.StatusConflict, map[string]any{
			"error":       "the targeted hosts changed since the preview; review the new target set and submit again",
			"targetsHash": changed.TargetsHash, "total": changed.Total,
		})
	case errors.Is(err, domain.ErrInvalidInput):
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, domain.ErrExecutionNotAllowed):
		return c.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
	case errors.Is(err, domain.ErrPermissionDenied):
		return c.JSON(http.StatusForbidden, map[string]string{"error": "permission denied"})
	case errors.Is(err, domain.ErrRunNotFound):
		return c.JSON(http.StatusNotFound, map[string]string{"error": "run not found"})
	case errors.Is(err, domain.ErrRunFinished):
		return c.JSON(http.StatusConflict, map[string]string{"error": "the run has already finished"})
	case errors.Is(err, domain.ErrServiceBusy):
		c.Response().Header().Set("Retry-After", "5")
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "the server is busy or shutting down; retry shortly"})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "unable to process the run request"})
	}
}
