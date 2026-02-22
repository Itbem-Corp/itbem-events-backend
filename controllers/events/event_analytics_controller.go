package events

import (
	"errors"
	"events-stocks/models"
	"net/http"

	eventsService "events-stocks/services/events"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// GetEventAnalytics returns the analytics record for a given event.
func GetEventAnalytics(c echo.Context) error {
	idStr := c.Param("id")
	eventID, err := uuid.FromString(idStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}

	analytics, err := eventsService.GetEventAnalyticsByEventID(eventID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Auto-create empty analytics for this event
			newAnalytics := &models.EventAnalytics{EventID: eventID}
			if createErr := eventsService.CreateEventAnalytics(newAnalytics); createErr != nil {
				return utils.Error(c, http.StatusInternalServerError, "Error creating analytics", createErr.Error())
			}
			return utils.Success(c, http.StatusOK, "Analytics loaded", newAnalytics)
		}
		return utils.Error(c, http.StatusInternalServerError, "Error fetching analytics", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Analytics loaded", analytics)
}
