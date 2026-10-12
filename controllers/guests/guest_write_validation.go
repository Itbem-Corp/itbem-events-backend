package guests

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"events-stocks/models"
	"events-stocks/utils"

	"github.com/labstack/echo/v4"
)

// maxGuestPartySize bounds max_guests and the RSVP party size a host can
// write. The dashboard form allows 20; the server bound is generous so CSV
// imports of large family or company invitations keep working.
const maxGuestPartySize = 100

// guestWriteError is a host guest write the server refuses with a 400. Message
// is Spanish (shown by the dashboard); Code is a stable machine value.
type guestWriteError struct {
	Code    string
	Message string
}

func (e *guestWriteError) Error() string { return e.Code + ": " + e.Message }

// validateGuestWrite checks a guest after a host write. before is the stored
// row on update and nil on create; on update only the fields the write changed
// are checked, so legacy rows stay editable.
//
//   - every free-text column is within models' length limits (GST-7);
//   - rsvp_status is pending, confirmed, declined or empty (GST-15);
//   - max_guests and the party size are within 0..maxGuestPartySize, and the
//     party size of a guest who is not declined never exceeds max_guests.
func validateGuestWrite(before, after *models.Guest) error {
	if err := models.CheckGuestTextLengths(before, after); err != nil {
		var tooLong *models.GuestFieldTooLongError
		if errors.As(err, &tooLong) {
			return &guestWriteError{Code: "guest_field_too_long", Message: tooLong.Message()}
		}
		return err
	}

	status := strings.ToLower(strings.TrimSpace(after.RSVPStatus))
	if before == nil || status != strings.ToLower(strings.TrimSpace(before.RSVPStatus)) {
		switch status {
		case "", "pending", "confirmed", "declined":
		default:
			return &guestWriteError{
				Code:    "invalid_rsvp_status",
				Message: "Estado de confirmación inválido. Usa pendiente, confirmado o declinado.",
			}
		}
	}

	if after.MaxGuests < 0 || after.RSVPGuestCount < 0 || after.GuestsCount < 0 {
		return &guestWriteError{Code: "invalid_party_size", Message: "El número de invitados no puede ser negativo."}
	}
	maxChanged := before == nil || before.MaxGuests != after.MaxGuests
	countChanged := before == nil || before.RSVPGuestCount != after.RSVPGuestCount
	if maxChanged && after.MaxGuests > maxGuestPartySize {
		return &guestWriteError{
			Code:    "invalid_party_size",
			Message: fmt.Sprintf("El máximo de invitados admite hasta %d personas.", maxGuestPartySize),
		}
	}
	if countChanged && after.RSVPGuestCount > maxGuestPartySize {
		return &guestWriteError{
			Code:    "invalid_party_size",
			Message: fmt.Sprintf("Los asistentes admiten hasta %d personas.", maxGuestPartySize),
		}
	}
	if (maxChanged || countChanged) && status != "declined" &&
		after.MaxGuests > 0 && after.RSVPGuestCount > after.MaxGuests {
		return &guestWriteError{
			Code: "party_size_exceeds_max_guests",
			Message: fmt.Sprintf("Los asistentes (%d) superan el máximo de invitados (%d).",
				after.RSVPGuestCount, after.MaxGuests),
		}
	}
	return nil
}

func respondGuestWriteError(c echo.Context, err error) error {
	var invalid *guestWriteError
	if errors.As(err, &invalid) {
		return utils.Error(c, http.StatusBadRequest, invalid.Message, invalid.Code)
	}
	return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
}
