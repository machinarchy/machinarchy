package job

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
)

type Handler struct {
	jobs domain.JobStore
}

type ListResponse struct {
	Data   []*domain.Job `json:"data"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

func NewHandler(jobs domain.JobStore) *Handler {
	return &Handler{jobs: jobs}
}

// GetByID returns a job record and its captured output.
// @Summary Get a job
// @Tags Jobs
// @Produce json
// @Security BearerAuth
// @Param id path int true "Job ID"
// @Success 200 {object} domain.Job
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/jobs/{id} [get]
func (h *Handler) GetByID(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid URL request job ID format"})
	}

	job, err := h.jobs.GetJobByID(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "Target execution job record could not be found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "unable to retrieve job"})
	}

	return c.JSON(http.StatusOK, job)
}

// List returns paginated job history, optionally filtered by host.
// @Summary List jobs
// @Tags Jobs
// @Produce json
// @Security BearerAuth
// @Param hostId query int false "Filter by host ID"
// @Param limit query int false "Page size (1-100)" default(10)
// @Param offset query int false "Number of rows to skip" default(0)
// @Success 200 {object} ListResponse
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/jobs [get]
func (h *Handler) List(c echo.Context) error {
	hostID, err := queryInt(c, "hostId", 0)
	if err != nil || hostID < 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "hostId must be a non-negative integer"})
	}
	limit, err := queryInt(c, "limit", 10)
	if err != nil || limit < 1 || limit > 100 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and 100"})
	}
	offset, err := queryInt(c, "offset", 0)
	if err != nil || offset < 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "offset must be non-negative"})
	}

	filter := domain.JobFilter{
		HostID: hostID,
		Limit:  limit,
		Offset: offset,
	}

	data, total, err := h.jobs.ListJobs(c.Request().Context(), filter)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "unable to retrieve jobs"})
	}

	return c.JSON(http.StatusOK, ListResponse{Data: data, Total: total, Limit: limit, Offset: offset})
}

func queryInt(c echo.Context, name string, fallback int) (int, error) {
	value := c.QueryParam(name)
	if value == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}
