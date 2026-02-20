package eventsection

import (
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
)

var eventSectionSvc *eventsService.EventSectionService

func InitEventSectionController(svc *eventsService.EventSectionService) {
	eventSectionSvc = svc
}

// GET /events/:id/sections
func ListSectionsByEvent(c echo.Context) error {
	idParam := c.Param("id")
	eventID, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event UUID", err.Error())
	}

	sections, err := eventSectionSvc.ListByEventID(eventID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading sections", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Sections loaded", sections)
}

// POST /events/:id/sections
func CreateSection(c echo.Context) error {
	idParam := c.Param("id")
	eventID, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event UUID", err.Error())
	}

	var section models.EventSection
	if err := c.Bind(&section); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	section.EventID = eventID
	if err := eventSectionSvc.CreateEventSection(&section); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating section", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Section created", section)
}

// PUT /sections/:id
func UpdateSection(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	var section models.EventSection
	if err := c.Bind(&section); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	section.ID = id
	if err := eventSectionSvc.UpdateEventSection(&section); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating section", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Section updated", section)
}

// DELETE /sections/:id
func DeleteSection(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	if err := eventSectionSvc.DeleteEventSection(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting section", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Section deleted", nil)
}
