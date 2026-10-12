package events

import (
	"errors"
	"log/slog"
	"net/http"

	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/eventsrepository"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// Stable codes in the "error" field of event write failures, so the dashboard
// can attach the message to the right form field without parsing text.
const (
	eventErrorNameTaken       = "event_name_taken"
	eventErrorIdentifierTaken = "event_identifier_taken"
	eventErrorInvalidTimezone = "invalid_timezone"
	eventErrorNameTooLong     = "event_name_too_long"
	// eventErrorListFailed is the code of a failed event read (list,
	// dashboard, notifications). The cause is logged, never returned: driver
	// errors carry SQL and column names (ROLES-68).
	eventErrorListFailed = "events_load_failed"
	// eventErrorWriteFailed: an unexpected create/update/duplicate failure;
	// the cause is logged only.
	eventErrorWriteFailed = "event_write_failed"
)

// respondEventListError answers a failed GET /api/events.
func respondEventListError(c echo.Context, err error) error {
	return respondEventReadError(c, err, "events list")
}

// respondEventReadError logs a failed protected event read and answers a 500
// with a Spanish message and a stable code, without the internal error.
func respondEventReadError(c echo.Context, err error, what string) error {
	slog.Error("event read failed", "read", what, "path", c.Path(), "error", err)
	return utils.Error(c, http.StatusInternalServerError, "No pudimos cargar los eventos. Intenta de nuevo en unos segundos.", eventErrorListFailed)
}

// respondEventPayloadError answers a payload that could not be applied to an
// event: 400 always, with a Spanish message for the cases the form can fix.
func respondEventPayloadError(c echo.Context, err error) error {
	if errors.Is(err, dtos.ErrEventNameTooLong) {
		return utils.Error(c, http.StatusBadRequest, "El nombre del evento admite máximo 120 caracteres.", eventErrorNameTooLong)
	}
	if errors.Is(err, dtos.ErrInvalidEventTimezone) {
		return utils.Error(c, http.StatusBadRequest, "Zona horaria inválida", eventErrorInvalidTimezone)
	}
	return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
}

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
	case errors.Is(err, gorm.ErrRecordNotFound):
		return utils.Error(c, http.StatusNotFound, "Evento no encontrado", "")
	}
	slog.Error("event write failed", "operation", fallback, "error", err)
	return utils.Error(c, http.StatusInternalServerError, fallback, eventErrorWriteFailed)
}

// keepClientWhenCleared treats client_id: null (or "") on an update as
// "unchanged": the dashboard sends null when the organization picker is empty,
// and storing it orphaned the event (EVT-4). Moving to another organization
// still goes through authz.RequireClientMoveAccess.
func keepClientWhenCleared(event *models.Event, originalClientID *uuid.UUID) {
	if event.ClientID == nil && originalClientID != nil {
		clientID := *originalClientID
		event.ClientID = &clientID
	}
}

func sameClientID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
