package invitations

import (
	"encoding/json"
	"errors"
	"events-stocks/dtos"
	invitationsService "events-stocks/services/invitations"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
	"net/http"
	"strconv"
	"strings"
)

var invitationSvc *invitationsService.InvitationService

func InitInvitationsController(svc *invitationsService.InvitationService) {
	invitationSvc = svc
}

type RSVPRequest struct {
	PrettyToken           string  `json:"pretty_token" form:"pretty_token" query:"pretty_token"`
	PrettyTokenAlt        string  `json:"prettyToken" form:"prettyToken" query:"prettyToken"`
	PrettyTokenPascal     string  `json:"PrettyToken" form:"PrettyToken" query:"PrettyToken"`
	Token                 string  `json:"token" form:"token" query:"token"`
	TokenPascal           string  `json:"Token" form:"Token" query:"Token"`
	InvitationToken       string  `json:"invitation_token" form:"invitation_token" query:"invitation_token"`
	InvitationTokenAlt    string  `json:"invitationToken" form:"invitationToken" query:"invitationToken"`
	InvitationTokenPascal string  `json:"InvitationToken" form:"InvitationToken" query:"InvitationToken"`
	Status                string  `json:"status" form:"status" query:"status" validate:"required,oneof=confirmed declined pending"`
	StatusPascal          string  `json:"Status" form:"Status" query:"Status"`
	RSVPStatus            string  `json:"rsvp_status" form:"rsvp_status" query:"rsvp_status"`
	RSVPStatusAlt         string  `json:"rsvpStatus" form:"rsvpStatus" query:"rsvpStatus"`
	RSVPStatusPascal      string  `json:"RSVPStatus" form:"RSVPStatus" query:"RSVPStatus"`
	Method                string  `json:"method" form:"method" query:"method"`
	MethodPascal          string  `json:"Method" form:"Method" query:"Method"`
	RSVPMethod            string  `json:"rsvp_method" form:"rsvp_method" query:"rsvp_method"`
	RSVPMethodAlt         string  `json:"rsvpMethod" form:"rsvpMethod" query:"rsvpMethod"`
	RSVPMethodPascal      string  `json:"RSVPMethod" form:"RSVPMethod" query:"RSVPMethod"`
	GuestCount            rsvpInt `json:"guest_count" validate:"min=0"`
	GuestCountAlt         rsvpInt `json:"guestCount" validate:"min=0"`
	GuestCountPascal      rsvpInt `json:"GuestCount" validate:"min=0"`
	GuestsCount           rsvpInt `json:"guests_count" validate:"min=0"`
	GuestsCountAlt        rsvpInt `json:"guestsCount" validate:"min=0"`
	GuestsCountPascal     rsvpInt `json:"GuestsCount" validate:"min=0"`
	RSVPGuestCount        rsvpInt `json:"rsvp_guest_count" validate:"min=0"`
	RSVPGuestAlt          rsvpInt `json:"rsvpGuestCount" validate:"min=0"`
	RSVPGuestPascal       rsvpInt `json:"RSVPGuestCount" validate:"min=0"`
	RSVPNotes             *string `json:"rsvp_notes" form:"rsvp_notes" query:"rsvp_notes"`
	RSVPNotesAlt          *string `json:"rsvpNotes" form:"rsvpNotes" query:"rsvpNotes"`
	RSVPNotesPascal       *string `json:"RSVPNotes" form:"RSVPNotes" query:"RSVPNotes"`
	Notes                 *string `json:"notes" form:"notes" query:"notes"`
	NotesPascal           *string `json:"Notes" form:"Notes" query:"Notes"`
	DietaryNotes          *string `json:"dietary_restrictions" form:"dietary_restrictions" query:"dietary_restrictions"`
	DietaryAlt            *string `json:"dietaryRestrictions" form:"dietaryRestrictions" query:"dietaryRestrictions"`
	DietaryPascal         *string `json:"DietaryRestrictions" form:"DietaryRestrictions" query:"DietaryRestrictions"`
}

type rsvpInt int

func (value *rsvpInt) UnmarshalJSON(raw []byte) error {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		*value = 0
		return nil
	}

	var numeric int
	if err := json.Unmarshal(raw, &numeric); err == nil {
		*value = rsvpInt(numeric)
		return nil
	}

	var quoted string
	if err := json.Unmarshal(raw, &quoted); err != nil {
		return err
	}

	quoted = strings.TrimSpace(quoted)
	if quoted == "" {
		*value = 0
		return nil
	}

	parsed, err := strconv.Atoi(quoted)
	if err != nil {
		return err
	}
	*value = rsvpInt(parsed)
	return nil
}

func firstRSVPString(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstRSVPOptionalString(values ...*string) (string, bool) {
	for _, value := range values {
		if value == nil {
			continue
		}
		return strings.TrimSpace(*value), true
	}
	return "", false
}

func (r RSVPRequest) guestCount() int {
	if r.GuestCount > 0 {
		return int(r.GuestCount)
	}
	if r.GuestCountAlt > 0 {
		return int(r.GuestCountAlt)
	}
	if r.GuestCountPascal > 0 {
		return int(r.GuestCountPascal)
	}
	if r.GuestsCount > 0 {
		return int(r.GuestsCount)
	}
	if r.GuestsCountAlt > 0 {
		return int(r.GuestsCountAlt)
	}
	if r.GuestsCountPascal > 0 {
		return int(r.GuestsCountPascal)
	}
	if r.RSVPGuestCount > 0 {
		return int(r.RSVPGuestCount)
	}
	if r.RSVPGuestAlt > 0 {
		return int(r.RSVPGuestAlt)
	}
	if r.RSVPGuestPascal > 0 {
		return int(r.RSVPGuestPascal)
	}
	return int(r.GuestCount)
}

func (r RSVPRequest) rsvpTextFields() (dietaryRestrictions string, notes string, hasSeparateNotes bool) {
	dietaryRestrictions, hasDietaryRestrictions := firstRSVPOptionalString(
		r.DietaryNotes,
		r.DietaryAlt,
		r.DietaryPascal,
	)
	notes, hasNotes := firstRSVPOptionalString(r.Notes, r.NotesPascal)
	rsvpNotes, hasRSVPNotes := firstRSVPOptionalString(
		r.RSVPNotes,
		r.RSVPNotesAlt,
		r.RSVPNotesPascal,
	)
	if hasDietaryRestrictions {
		if hasRSVPNotes {
			return dietaryRestrictions, rsvpNotes, true
		}
		return dietaryRestrictions, notes, true
	}
	if hasRSVPNotes {
		return "", rsvpNotes, true
	}
	if hasNotes {
		return notes, "", false
	}
	return "", "", false
}

func GetInvitationByToken(c echo.Context) error {
	token := utils.PublicInvitationToken(c)
	if token == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing token", "")
	}
	result, err := invitationSvc.GetInvitationByToken(token)
	if err != nil {
		if errors.Is(err, invitationsService.ErrInvitationEventUnavailable) {
			return utils.Error(c, http.StatusForbidden, "Event is not public", "")
		}
		if errors.Is(err, invitationsService.ErrInvitationEventAccessFailed) {
			return utils.Error(c, http.StatusInternalServerError, "Error loading event access", err.Error())
		}
		return utils.Error(c, http.StatusUnauthorized, "Invalid or expired token", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Invitation loaded", result)
}

func ConfirmRSVP(c echo.Context) error {
	var req RSVPRequest
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	token := firstRSVPString(
		req.PrettyToken,
		req.PrettyTokenAlt,
		req.PrettyTokenPascal,
		req.Token,
		req.TokenPascal,
		req.InvitationToken,
		req.InvitationTokenAlt,
		req.InvitationTokenPascal,
	)
	if token == "" {
		return utils.Error(c, http.StatusBadRequest, "Token is required", "")
	}
	req.Status = strings.ToLower(firstRSVPString(req.Status, req.StatusPascal, req.RSVPStatus, req.RSVPStatusAlt, req.RSVPStatusPascal))
	req.Method = strings.ToLower(firstRSVPString(req.Method, req.MethodPascal, req.RSVPMethod, req.RSVPMethodAlt, req.RSVPMethodPascal))
	if err := c.Validate(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}
	if req.Method == "" {
		req.Method = "web"
	}
	dietaryRestrictions, notes, hasSeparateNotes := req.rsvpTextFields()
	noteArgs := []string(nil)
	if hasSeparateNotes {
		noteArgs = append(noteArgs, notes)
	}
	confirmation, err := invitationSvc.ConfirmRSVPWithResult(token, req.Status, req.Method, req.guestCount(), dietaryRestrictions, noteArgs...)
	if err != nil {
		if errors.Is(err, invitationsService.ErrInvitationEventUnavailable) {
			return utils.Error(c, http.StatusForbidden, "Event is not public", "")
		}
		if errors.Is(err, invitationsService.ErrInvitationEventAccessFailed) {
			return utils.Error(c, http.StatusInternalServerError, "Error loading event access", err.Error())
		}
		if errors.Is(err, invitationsService.ErrInvalidRSVPRequest) {
			return utils.Error(c, http.StatusBadRequest, "Invalid RSVP request", err.Error())
		}
		return utils.Error(c, http.StatusUnauthorized, "RSVP confirmation failed", err.Error())
	}

	return utils.Success(c, http.StatusOK, "RSVP confirmed", dtos.NewRSVPConfirmationResponse(confirmation.Guest, confirmation.PrettyToken))
}
