package events

import (
	"errors"
	"net/http"

	"events-stocks/dtos"
	"events-stocks/internal/authz"
	eventsService "events-stocks/services/events"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

var duplicateSvc *eventsService.DuplicateService

func InitDuplicateController(svc *eventsService.DuplicateService) {
	duplicateSvc = svc
}

// DuplicateEvent creates a fresh event from an existing event's reusable setup.
// Route: POST /api/events/:id/duplicate (protected)
func DuplicateEvent(c echo.Context) error {
	idParam := c.Param("id")
	eventID, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}

	user, existing, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityEventManage)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}

	var payload dtos.EventPayload
	if err := c.Bind(&payload); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	accessCandidate := *existing
	if err := payload.ApplyTo(&accessCandidate); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if err := authz.RequireEventClientForCreate(user, accessCandidate.ClientID); err != nil {
		return authz.Respond(c, err)
	}

	if duplicateSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Duplicate service unavailable", "")
	}

	duplicated, err := duplicateSvc.DuplicateEventByID(eventID, payload)
	if err != nil {
		if errors.Is(err, eventsService.ErrEventNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", err.Error())
		}
		if errors.Is(err, eventsService.ErrDuplicateServiceUnavailable) {
			return utils.Error(c, http.StatusInternalServerError, "Duplicate service unavailable", err.Error())
		}
		return utils.Error(c, http.StatusInternalServerError, "Error duplicating event", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Event duplicated", eventResponseWithCoverView(duplicated))
}
