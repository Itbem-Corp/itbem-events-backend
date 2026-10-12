package events

import (
	"errors"
	"net/http"

	"events-stocks/repositories/eventsrepository"
	"events-stocks/utils"

	"github.com/labstack/echo/v4"
)

// Stable codes in the "error" field of event write failures, so the dashboard
// can attach the message to the right form field without parsing text.
const (
	eventErrorNameTaken       = "event_name_taken"
	eventErrorIdentifierTaken = "event_identifier_taken"
)

// respondEventWriteError maps a failed create, update or duplicate of an event
// to its HTTP answer: a unique violation is the caller's conflict (409, with a
// Spanish message), a vanished row is 404, anything else stays a 500.
func respondEventWriteError(c echo.Context, err error, fallback string) error {
	err = eventsrepository.ValidateError(err)
	switch {
	case errors.Is(err, eventsrepository.ErrEventNameAlreadyExists):
		return utils.Error(c, http.StatusConflict, "Ya existe un evento con ese nombre en esta organización.", eventErrorNameTaken)
	case errors.Is(err, eventsrepository.ErrEventIdentifierAlreadyExists):
		return utils.Error(c, http.StatusConflict, "Esa URL del evento ya está en uso. Elige otra.", eventErrorIdentifierTaken)
	case err != nil && err.Error() == "record not found":
		return utils.Error(c, http.StatusNotFound, "Evento no encontrado", "")
	}
	return utils.Error(c, http.StatusInternalServerError, fallback, err.Error())
}
