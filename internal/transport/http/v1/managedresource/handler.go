package managedresource

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/service"
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
type profileRequest struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Spec        resource.AccessProfileSpec `json:"spec"`
}
type operationRequest struct {
	HostIDs []resource.ID    `json:"hostIds"`
	Run     resource.RunSpec `json:"run"`
}

func RegisterRoutes(group *echo.Group, handler *Handler) {
	group.POST("/hosts", handler.createHost)
	group.GET("/hosts/:id", handler.getHost)
	group.PUT("/hosts/:id", handler.updateHost)
	group.POST("/access-profiles", handler.createProfile)
	group.PUT("/access-profiles/:id", handler.updateProfile)
	group.POST("/operations", handler.submitOperation)
	group.GET("/operations/:id", handler.getOperation)
	group.POST("/operations/:id/cancel", handler.cancelOperation)
}

func (h *Handler) createHost(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return unauthorized(c)
	}
	var request hostRequest
	if err := c.Bind(&request); err != nil {
		return badRequest(c)
	}
	host, err := h.service.CreateHost(c.Request().Context(), principal, request.Name, request.Labels, request.Spec)
	if err != nil {
		return applicationError(c, err)
	}
	return c.JSON(http.StatusCreated, host)
}
func (h *Handler) getHost(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return unauthorized(c)
	}
	id, err := resource.ParseResourceID(c.Param("id"))
	if err != nil {
		return badRequest(c)
	}
	host, err := h.service.GetHost(c.Request().Context(), principal, id)
	if err != nil {
		return applicationError(c, err)
	}
	return c.JSON(http.StatusOK, host)
}
func (h *Handler) updateHost(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return unauthorized(c)
	}
	id, err := resource.ParseResourceID(c.Param("id"))
	if err != nil {
		return badRequest(c)
	}
	var request struct {
		Revision   uint64            `json:"revision"`
		Generation uint64            `json:"generation"`
		Spec       resource.HostSpec `json:"spec"`
	}
	if err = c.Bind(&request); err != nil {
		return badRequest(c)
	}
	if err = h.service.UpdateHost(c.Request().Context(), principal, id, request.Revision, request.Generation, request.Spec); err != nil {
		return applicationError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
func (h *Handler) createProfile(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return unauthorized(c)
	}
	var request profileRequest
	if err := c.Bind(&request); err != nil {
		return badRequest(c)
	}
	profile, err := h.service.CreateAccessProfile(c.Request().Context(), principal, request.Name, request.Description, request.Spec)
	if err != nil {
		return applicationError(c, err)
	}
	return c.JSON(http.StatusCreated, profile)
}
func (h *Handler) updateProfile(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return unauthorized(c)
	}
	id, err := resource.ParseResourceID(c.Param("id"))
	if err != nil {
		return badRequest(c)
	}
	var request struct {
		ExpectedRevision uint64                     `json:"expectedRevision"`
		Spec             resource.AccessProfileSpec `json:"spec"`
	}
	if err = c.Bind(&request); err != nil {
		return badRequest(c)
	}
	revision, err := h.service.UpdateAccessProfile(c.Request().Context(), principal, id, request.ExpectedRevision, request.Spec)
	if err != nil {
		return applicationError(c, err)
	}
	return c.JSON(http.StatusCreated, revision)
}
func (h *Handler) submitOperation(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return unauthorized(c)
	}
	var request operationRequest
	if err := c.Bind(&request); err != nil {
		return badRequest(c)
	}
	op, err := h.service.SubmitOperation(c.Request().Context(), principal, request.HostIDs, request.Run)
	if err != nil {
		return applicationError(c, err)
	}
	return c.JSON(http.StatusAccepted, op)
}
func (h *Handler) getOperation(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return unauthorized(c)
	}
	id, err := resource.ParseResourceID(c.Param("id"))
	if err != nil {
		return badRequest(c)
	}
	op, err := h.service.GetOperation(c.Request().Context(), principal, id)
	if err != nil {
		return applicationError(c, err)
	}
	return c.JSON(http.StatusOK, op)
}
func (h *Handler) cancelOperation(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return unauthorized(c)
	}
	id, err := resource.ParseResourceID(c.Param("id"))
	if err != nil {
		return badRequest(c)
	}
	op, err := h.service.CancelOperation(c.Request().Context(), principal, id)
	if err != nil {
		return applicationError(c, err)
	}
	return c.JSON(http.StatusAccepted, op)
}

func unauthorized(c echo.Context) error {
	return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
}
func badRequest(c echo.Context) error {
	return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
}
func applicationError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, domain.ErrPermissionDenied), errors.Is(err, service.ErrAccessProfileOwnerRequired):
		return c.JSON(http.StatusForbidden, map[string]string{"error": "permission denied"})
	case errors.Is(err, resource.ErrConflict):
		return c.JSON(http.StatusConflict, map[string]string{"error": "resource revision conflict"})
	case errors.Is(err, resource.ErrHostNotFound), errors.Is(err, resource.ErrOperationNotFound), errors.Is(err, resource.ErrAccessProfileNotFound):
		return c.JSON(http.StatusNotFound, map[string]string{"error": "resource not found"})
	case errors.Is(err, resource.ErrInvalidResource), errors.Is(err, domain.ErrInvalidInput):
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid resource request"})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "managed resource request failed"})
	}
}
