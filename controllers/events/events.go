package events

import (
	"encoding/json"
	"events-stocks/models"
	eventService "events-stocks/repositories/eventsrepository"
	"events-stocks/utils" // Importa tu helper de respuesta
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
)

const RedisServiceEventsKey = "events"

// GET /events/:key
func GetEvents(c echo.Context) error {
	keyParam := c.Param("key")
	redisKey := keyParam + ":" + RedisServiceEventsKey

	dataStr, ok := c.Get(redisKey).(string)
	if !ok {
		return utils.Success(c, http.StatusOK, "No data loaded", nil)
	}

	var eventos []models.Event
	if err := json.Unmarshal([]byte(dataStr), &eventos); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error parsing data", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Events loaded", eventos)
}

// POST /events
func CreateEvent(c echo.Context) error {
	var event models.Event
	if err := c.Bind(&event); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	if err := eventService.CreateEvent(&event); err != nil {
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

	event.ID = id
	if err := eventService.UpdateEvent(&event); err != nil {
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

	if err := eventService.DeleteEvent(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting event", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Event deleted", nil)
}
