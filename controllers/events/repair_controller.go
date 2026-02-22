package events

import (
	"net/http"

	"events-stocks/configuration"
	eventsService "events-stocks/services/events"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

// RepairEvent validates and fixes event data integrity issues.
// Route: POST /api/events/:id/repair (protected)
func RepairEvent(c echo.Context) error {
	idParam := c.Param("id")
	eventID, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}

	result, err := eventsService.RepairEvent(configuration.DB, eventID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Repair failed", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Event repair complete", result)
}
