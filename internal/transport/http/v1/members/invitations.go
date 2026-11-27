package members

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// InvitationHandler handles email invitations (ADR-060).
type InvitationHandler struct {
	service domain.InvitationService
}

// NewInvitationHandler creates the invitation handler.
func NewInvitationHandler(service domain.InvitationService) *InvitationHandler {
	return &InvitationHandler{service: service}
}

// RegisterInvitationRoutes mounts the owner routes under /members/invitations
// on the authenticated v1 group (human owner session only) and the public
// accept route under /auth on the API group.
func RegisterInvitationRoutes(api, v1 *echo.Group, h *InvitationHandler) {
	g := v1.Group("/members/invitations", middleware.RequirePermission(domain.PermissionMembersManage), middleware.RequireHuman())
	g.POST("", h.Invite)
	g.GET("", h.List)
	g.DELETE("/:id", h.Revoke)
	api.POST("/auth/invitations/accept", h.Accept)
}

// InviteRequest is the body of an invitation.
type InviteRequest struct {
	Email string `json:"email" example:"new.person@example.com"`
	Role  string `json:"role" enums:"operator,reader" example:"operator"`
}

// InviteResponse confirms an invitation. It is the same whether or not the
// address already has an account, and carries no token.
type InviteResponse struct {
	Email     string    `json:"email"`
	Role      string    `json:"role" enums:"operator,reader"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// AcceptRequest is the body of an invitation redemption.
type AcceptRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// AcceptResponse describes the new account (never a credential).
type AcceptResponse struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Role     string `json:"role" enums:"operator,reader"`
}

// Invite emails an invitation.
// @Summary Invite a person by email
// @Description Requires a human workspace owner session. Emails a single-use token that expires after 7 days. The role must be operator or reader. The answer is the same when the address already has an account (nothing is sent), so it does not reveal registered addresses. Needs mail settings (MACHINARCHY_SMTP_*); without them it answers 501.
// @Tags Members
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body InviteRequest true "Invitation"
// @Success 202 {object} InviteResponse
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Failure 501 {object} httperr.Body
// @Failure 502 {object} httperr.Body
// @Router /api/v1/members/invitations [post]
func (h *InvitationHandler) Invite(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	var request InviteRequest
	if err := c.Bind(&request); err != nil {
		return httperr.Status(c, http.StatusBadRequest, "invalid request body")
	}
	invitation, err := h.service.Invite(c.Request().Context(), principal, domain.InvitationRequest{
		Email: request.Email, Role: domain.Role(strings.ToLower(strings.TrimSpace(request.Role))),
	})
	if err != nil {
		return respondInvitationError(c, err)
	}
	return c.JSON(http.StatusAccepted, InviteResponse{Email: invitation.Email, Role: string(invitation.Role), ExpiresAt: invitation.ExpiresAt})
}

// List returns pending invitations.
// @Summary List pending invitations
// @Description Requires a human workspace owner session. Tokens are never returned.
// @Tags Members
// @Produce json
// @Security BearerAuth
// @Success 200 {array} domain.Invitation
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Router /api/v1/members/invitations [get]
func (h *InvitationHandler) List(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	invitations, err := h.service.List(c.Request().Context(), principal)
	if err != nil {
		return respondInvitationError(c, err)
	}
	return c.JSON(http.StatusOK, invitations)
}

// Revoke cancels an invitation.
// @Summary Revoke a pending invitation
// @Description Requires a human workspace owner session. The token stops working at once.
// @Tags Members
// @Security BearerAuth
// @Param id path int true "Invitation ID"
// @Success 204 "Revoked"
// @Failure 400 {object} httperr.Body
// @Failure 401 {object} httperr.Body
// @Failure 403 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Router /api/v1/members/invitations/{id} [delete]
func (h *InvitationHandler) Revoke(c echo.Context) error {
	principal, ok := middleware.Principal(c)
	if !ok {
		return httperr.Unauthorized(c)
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return httperr.Status(c, http.StatusBadRequest, "invalid invitation ID")
	}
	if err := h.service.Revoke(c.Request().Context(), principal, id); err != nil {
		return respondInvitationError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// Accept redeems an invitation token.
// @Summary Accept an invitation
// @Description Public. Creates the account with the invited role; the invitee chooses the username and password. The token works once. An unknown, expired, revoked or used token gets one generic answer.
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body AcceptRequest true "Token, username and password"
// @Success 201 {object} AcceptResponse
// @Failure 400 {object} httperr.Body
// @Failure 404 {object} httperr.Body
// @Failure 409 {object} httperr.Body
// @Router /api/auth/invitations/accept [post]
func (h *InvitationHandler) Accept(c echo.Context) error {
	var request AcceptRequest
	if err := c.Bind(&request); err != nil {
		return httperr.Status(c, http.StatusBadRequest, "invalid request body")
	}
	user, err := h.service.Accept(c.Request().Context(), request.Token, request.Username, request.Password)
	if err != nil {
		return respondInvitationError(c, err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusCreated, AcceptResponse{ID: user.ID, Username: user.Username, Email: user.Email, Role: string(user.Role)})
}

var invitationErrorMappings = []httperr.Mapping{
	httperr.Map(domain.ErrInvalidInput, http.StatusBadRequest, ""),
	httperr.Map(domain.ErrInvitationInvalid, http.StatusNotFound, "invitation is invalid or expired"),
	httperr.Map(domain.ErrInvitationNotFound, http.StatusNotFound, "invitation not found"),
	httperr.Map(domain.ErrInvitationLimit, http.StatusConflict, "too many pending invitations; revoke some first"),
	httperr.Map(domain.ErrUserAlreadyExists, http.StatusConflict, "username or email is already registered"),
	httperr.Map(domain.ErrMailNotConfigured, http.StatusNotImplemented, "email delivery is not configured on this server"),
	httperr.Map(domain.ErrMailFailed, http.StatusBadGateway, "the invitation email could not be sent"),
}

func respondInvitationError(c echo.Context, err error) error {
	return httperr.FromError(c, err, invitationErrorMappings...)
}
