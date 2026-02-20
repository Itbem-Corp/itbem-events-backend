package guests

import (
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/eventsectionrepository"
	guestrepository "events-stocks/repositories/guestrepository"
	guestService "events-stocks/services/guests"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"strings"
)

var guestSvc *guestService.GuestService

func InitGuestsController(svc *guestService.GuestService) { guestSvc = svc }

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
	if err := c.Validate(&guest); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}
	if err := guestSvc.CreateGuest(&guest); err != nil {
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
	if err := guestSvc.CreateGuestsWithInvitations(guests); err != nil {
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
	if err := c.Validate(&guest); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}
	guest.ID = id
	if err := guestSvc.UpdateGuest(&guest); err != nil {
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

	if err := guestSvc.DeleteGuest(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting guest", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Guest deleted", nil)
}

// AttendeeDTO contains the public-safe fields returned by the attendees endpoint.
type AttendeeDTO struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Nickname  string `json:"nickname"`
	Role      string `json:"role"`
	Order     int    `json:"order"`
}

// attendeeDeps allows injecting repository calls for testing.
type attendeeDeps struct {
	getSection   func(id uuid.UUID) (*models.EventSection, error)
	getAttendees func(eventID uuid.UUID) ([]models.Guest, error)
}

func handleGetAttendees(deps attendeeDeps, c echo.Context) error {
	sectionID, err := uuid.FromString(c.Param("sectionId"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid section UUID", err.Error())
	}
	section, err := deps.getSection(sectionID)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Section not found", err.Error())
	}
	attendees, err := deps.getAttendees(section.EventID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching attendees", err.Error())
	}
	dtos := make([]AttendeeDTO, 0, len(attendees))
	for _, g := range attendees {
		dtos = append(dtos, AttendeeDTO{FirstName: g.FirstName, LastName: g.LastName, Nickname: g.Nickname, Role: g.Role, Order: g.Order})
	}
	return utils.Success(c, http.StatusOK, "Attendees loaded", dtos)
}

// GET /events/section/:sectionId/attendees
// Public: returns guests for the event linked to the given section, ordered by display order.
func GetAttendees(c echo.Context) error {
	return handleGetAttendees(attendeeDeps{
		getSection:   eventsectionrepository.GetEventSectionByID,
		getAttendees: guestrepository.ListAttendeesByEventID,
	}, c)
}
