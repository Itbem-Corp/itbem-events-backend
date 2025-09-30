package guests

import (
	"encoding/json"
	"events-stocks/models"
	guestService "events-stocks/services/guests"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"strings"
)

// GET /guests/:key
func GetGuests(c echo.Context) error {
	keyParam := c.Param("key")
	redisKey := keyParam + ":" + utils.RedisGuestsKey

	dataStr, ok := c.Get(redisKey).(string)
	if !ok {
		return utils.Success(c, http.StatusOK, "No data loaded", nil)
	}

	// Caso: all:<eventId>
	if strings.HasPrefix(keyParam, "all:") {
		var guests []models.Guest
		if err := json.Unmarshal([]byte(dataStr), &guests); err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error parsing data", err.Error())
		}
		return utils.Success(c, http.StatusOK, "Guests loaded", guests)
	}

	// Caso: guestId (único invitado)
	var guest []models.Guest
	if err := json.Unmarshal([]byte(dataStr), &guest); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error parsing data", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Guest loaded", guest)
}

// POST /guests
func CreateGuest(c echo.Context) error {
	var guest models.Guest
	if err := c.Bind(&guest); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	if err := guestService.CreateGuest(&guest); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating guest", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Guest created", guest)
}

func CreateGuests(c echo.Context) error {
	var guests []models.Guest
	if err := c.Bind(&guests); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	if len(guests) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No guests provided", "")
	}

	// ⚡ Usamos el nuevo service
	if err := guestService.CreateGuestsWithInvitations(guests); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating guests with invitations", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Guests and invitations created", guests)
}

// PUT /guests/:id
func UpdateGuest(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	var guest models.Guest
	if err := c.Bind(&guest); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	guest.ID = id
	if err := guestService.UpdateGuest(&guest); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating guest", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Guest updated", guest)
}

// DELETE /guests/:id
func DeleteGuest(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	if err := guestService.DeleteGuest(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting guest", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Guest deleted", nil)
}
