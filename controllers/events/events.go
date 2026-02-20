package events

import (
	"encoding/json"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
)

var eventSvc *eventsService.EventService

func InitEventsController(svc *eventsService.EventService) {
	eventSvc = svc
}

// GET /events/:key
func GetEvents(c echo.Context) error {
	keyParam := c.Param("key")
	redisKey := keyParam + ":" + utils.RedisServiceEventsKey

	dataStr, ok := c.Get(redisKey).(string)
	if !ok {
		return utils.Success(c, http.StatusOK, "No data loaded", nil)
	}

	if keyParam == "all" {
		var events []models.Event
		if err := json.Unmarshal([]byte(dataStr), &events); err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error parsing data", err.Error())
		}
		return utils.Success(c, http.StatusOK, "Events loaded", events)
	}

	var event []models.Event
	if err := json.Unmarshal([]byte(dataStr), &event); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error parsing data", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Event loaded", event)
}

// POST /events
func CreateEvent(c echo.Context) error {
	var event models.Event
	if err := c.Bind(&event); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if err := c.Validate(&event); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}

	if err := eventSvc.CreateEvent(&event); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating event", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Event created", event)
}

// PUT /events/:id
func UpdateEvent(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	var event models.Event
	if err := c.Bind(&event); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if err := c.Validate(&event); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}

	event.ID = id
	if err := eventSvc.UpdateEvent(&event); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating event", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Event updated", event)
}

// DELETE /events/:id
func DeleteEvent(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	if err := eventSvc.DeleteEvent(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting event", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Event deleted", nil)
}

// GET /events/page-spec?token=...
// Public endpoint — returns the SDUI PageSpec for the event associated with the given invitation token.
func GetPageSpec(c echo.Context) error {
	token := c.QueryParam("token")
	if token == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing token parameter", "")
	}

	spec, err := eventsService.GetPageSpecByToken(token)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Page spec not found", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Page spec loaded", spec)
}
