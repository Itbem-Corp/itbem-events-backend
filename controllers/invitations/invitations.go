package invitations

import (
	invitationService "events-stocks/services/invitations"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
	"net/http"
)

// Soporte para snake_case y camelCase
type RSVPRequest struct {
	PrettyToken    string `json:"pretty_token" form:"pretty_token" query:"pretty_token"`
	PrettyTokenAlt string `json:"prettyToken" form:"prettyToken" query:"prettyToken"`
	Status         string `json:"status" form:"status" query:"status"`
	Method         string `json:"method" form:"method" query:"method"`
	GuestCount     int    `json:"guest_count"`
}

// GET /invitations/ByToken/:token
func GetInvitationByToken(c echo.Context) error {
	token := c.Param("token")
	if token == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing token", "")
	}

	result, err := invitationService.GetInvitationByToken(token)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "Invalid or expired token", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Invitation loaded", result)
}

// POST /invitations/rsvp
func ConfirmRSVP(c echo.Context) error {
	var req RSVPRequest
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	// soportar ambas variantes de token
	token := req.PrettyToken
	if token == "" {
		token = req.PrettyTokenAlt
	}

	if token == "" || req.Status == "" {
		return utils.Error(c, http.StatusBadRequest, "PrettyToken and status are required", "")
	}

	if req.Method == "" {
		req.Method = "web" // default
	}

	guest, err := invitationService.ConfirmRSVP(req.PrettyToken, req.Status, req.Method, req.GuestCount)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "RSVP confirmation failed", err.Error())
	}

	return utils.Success(c, http.StatusOK, "RSVP confirmed", guest)
}
