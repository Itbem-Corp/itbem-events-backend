package eventtypes

import (
	"net/http"

	"events-stocks/dtos"
	eventsService "events-stocks/services/events"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
)

// ListEventTypes devuelve el catálogo de tipos de evento.
// GET /api/event-types
func ListEventTypes(c echo.Context) error {
	types, err := eventsService.ListEventTypes()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching event types", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Event types", dtos.NewEventTypeResponses(types))
}
