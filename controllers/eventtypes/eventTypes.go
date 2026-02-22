package eventtypes

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"events-stocks/utils"
	"net/http"

	"github.com/labstack/echo/v4"
)

// ListEventTypes devuelve el catálogo de tipos de evento.
// GET /api/event-types
func ListEventTypes(c echo.Context) error {
	var types []models.EventType
	if err := gormrepository.DB().Order("name ASC").Find(&types).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching event types", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Event types", types)
}
