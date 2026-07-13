package gueststatuses

import (
	"net/http"

	"events-stocks/dtos"
	guestService "events-stocks/services/guests"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
)

// ListGuestStatuses returns the guest status catalog used by dashboard status updates.
// GET /api/catalogs/guest-statuses
func ListGuestStatuses(c echo.Context) error {
	statuses, err := guestService.ListGuestStatuss()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching guest statuses", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Guest statuses loaded", dtos.NewGuestStatusResponses(statuses))
}
