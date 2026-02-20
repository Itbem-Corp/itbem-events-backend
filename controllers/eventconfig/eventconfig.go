package eventconfig

import (
	eventsService "events-stocks/services/events"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
)

var eventConfigSvc *eventsService.EventConfigService

func InitEventConfigController(svc *eventsService.EventConfigService) {
	eventConfigSvc = svc
}

// GET /events/:id/config
func GetEventConfig(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	config, err := eventConfigSvc.GetEventConfigByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Event config loaded", config)
}

// PUT /events/:id/config
func UpdateEventConfig(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	config, err := eventConfigSvc.GetEventConfigByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", err.Error())
	}

	if err := c.Bind(config); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	config.ID = id
	if err := eventConfigSvc.UpdateEventConfig(config); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating event config", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Event config updated", config)
}
