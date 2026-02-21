package invitations

import (
	eventService "events-stocks/services/events"
	invitationsService "events-stocks/services/invitations"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
	"net/http"
)

var invitationSvc *invitationsService.InvitationService

func InitInvitationsController(svc *invitationsService.InvitationService) {
	invitationSvc = svc
}

type RSVPRequest struct {
	PrettyToken    string `json:"pretty_token" form:"pretty_token" query:"pretty_token"`
	PrettyTokenAlt string `json:"prettyToken" form:"prettyToken" query:"prettyToken"`
	Status         string `json:"status" form:"status" query:"status" validate:"required,oneof=confirmed declined pending"`
	Method         string `json:"method" form:"method" query:"method"`
	GuestCount     int    `json:"guest_count"`
}

func GetInvitationByToken(c echo.Context) error {
	token := c.Param("token")
	if token == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing token", "")
	}
	result, err := invitationSvc.GetInvitationByToken(token)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "Invalid or expired token", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Invitation loaded", result)
}

func ConfirmRSVP(c echo.Context) error {
	var req RSVPRequest
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	token := req.PrettyToken
	if token == "" {
		token = req.PrettyTokenAlt
	}
	if token == "" {
		return utils.Error(c, http.StatusBadRequest, "PrettyToken is required", "")
	}
	if err := c.Validate(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}
	if req.Method == "" {
		req.Method = "web"
	}
	guest, err := invitationSvc.ConfirmRSVP(token, req.Status, req.Method, req.GuestCount)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "RSVP confirmation failed", err.Error())
	}

	// Fire-and-forget: track RSVP in analytics without blocking response
	go func() {
		field := "rsvp_confirmed"
		if req.Status == "declined" {
			field = "rsvp_declined"
		}
		eventService.IncrementAnalytics(guest.EventID, field)
	}()

	return utils.Success(c, http.StatusOK, "RSVP confirmed", guest)
}
