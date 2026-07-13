package events

import (
	"net/http"

	"events-stocks/internal/authz"
	eventsService "events-stocks/services/events"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

var repairSvc *eventsService.RepairService

func InitRepairController(svc *eventsService.RepairService) {
	repairSvc = svc
}

// RepairEvent validates and fixes event data integrity issues.
// Route: POST /api/events/:id/repair (protected)
func RepairEvent(c echo.Context) error {
	idParam := c.Param("id")
	eventID, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityEventManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	if repairSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Repair service unavailable", "")
	}
	result, err := repairSvc.RepairEventByID(eventID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Repair failed", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Event repair complete", result)
}
