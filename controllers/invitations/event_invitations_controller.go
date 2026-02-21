package invitations

import (
	"errors"
	invitationsService "events-stocks/services/invitations"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"net/http"
)

// GET /api/events/:id/invitations
// Returns all invitations for the given event. Protected (requires Cognito JWT).
func ListByEvent(c echo.Context) error {
	idStr := c.Param("id")
	eventID, err := uuid.FromString(idStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}

	list, err := invitationsService.ListInvitationsByEventID(eventID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Success(c, http.StatusOK, "No invitations found", []interface{}{})
		}
		return utils.Error(c, http.StatusInternalServerError, "Error fetching invitations", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Invitations loaded", list)
}
