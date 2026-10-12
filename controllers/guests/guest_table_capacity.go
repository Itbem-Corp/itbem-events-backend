package guests

import (
	"errors"
	"net/http"
	"strings"

	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/eventtablerepository"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

// guestTableSeatsFit checks that seats more seats fit at a table of the event,
// not counting the guests in exclude; tests replace it.
var guestTableSeatsFit = func(eventID, tableID uuid.UUID, exclude []uuid.UUID, seats int) error {
	return eventtablerepository.CheckSeatsFit(configuration.DB, eventID, tableID, exclude, seats, 0)
}

// guestSeatingStatus is the guest's status as the seating screen reads it.
func guestSeatingStatus(guest *models.Guest) string {
	if status := strings.TrimSpace(guest.RSVPStatus); status != "" {
		return status
	}
	if code := strings.TrimSpace(guest.GuestStatus.Code); code != "" {
		return code
	}
	return "pending"
}

// guestSeats is the number of seats the guest takes at a table, by the shared
// rule eventtablerepository.SeatingPartySizeSQL. A create may carry only
// guests_count, which becomes its RSVP party size (see
// GuestService.CreateGuestsWithInvitations).
func guestSeats(guest *models.Guest) int {
	rsvpCount := guest.RSVPGuestCount
	if rsvpCount <= 0 && guest.GuestsCount > 0 {
		rsvpCount = guest.GuestsCount
	}
	return eventtablerepository.SeatingPartySize(guestSeatingStatus(guest), rsvpCount, guest.MaxGuests)
}

// checkNewGuestSeats applies the strict capacity rule to guests being created
// with a table_id: the seats of all of them per table, plus the seats already
// taken there, must fit the table's capacity.
func checkNewGuestSeats(eventID uuid.UUID, guests []models.Guest) error {
	seats := make(map[uuid.UUID]int)
	order := make([]uuid.UUID, 0)
	for i := range guests {
		if guests[i].TableID == nil {
			continue
		}
		tableID := *guests[i].TableID
		if _, ok := seats[tableID]; !ok {
			order = append(order, tableID)
		}
		seats[tableID] += guestSeats(&guests[i])
	}
	var over []dtos.TableOverCapacity
	for _, tableID := range order {
		if err := guestTableSeatsFit(eventID, tableID, nil, seats[tableID]); err != nil {
			var tooMany *dtos.TableOverCapacityError
			if !errors.As(err, &tooMany) {
				return err
			}
			over = append(over, tooMany.Tables...)
		}
	}
	if len(over) > 0 {
		return &dtos.TableOverCapacityError{Tables: over}
	}
	return nil
}

// checkUpdatedGuestSeats applies the strict capacity rule to a guest update:
// when the guest moves to a table, or takes more seats at its table than
// before, its seats must fit there.
func checkUpdatedGuestSeats(before, after *models.Guest) error {
	if after.TableID == nil {
		return nil
	}
	moved := before.TableID == nil || *before.TableID != *after.TableID
	seats := guestSeats(after)
	if !moved && seats <= guestSeats(before) {
		return nil
	}
	return guestTableSeatsFit(after.EventID, *after.TableID, []uuid.UUID{after.ID}, seats)
}

// respondGuestSeatsError answers a capacity refusal with the shared 409
// table_over_capacity body, and any other error with a 500.
func respondGuestSeatsError(c echo.Context, err error) error {
	var over *dtos.TableOverCapacityError
	if errors.As(err, &over) {
		return c.JSON(http.StatusConflict, dtos.NewTableOverCapacityResponse(over))
	}
	return c.JSON(http.StatusInternalServerError, map[string]interface{}{
		"status":  http.StatusInternalServerError,
		"message": "Error checking table capacity",
	})
}
