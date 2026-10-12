package events

import (
	"log/slog"

	"events-stocks/dtos"
	"events-stocks/internal/products/eventiapp"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

// EventClientDeps connects the events controller to event-client grants.
// GrantedEventIDs marks list rows; RevokeAllForEvent clears grants after an
// event changes organization.
type EventClientDeps struct {
	GrantedEventIDs   func(userID uuid.UUID, eventIDs []uuid.UUID) (map[uuid.UUID]struct{}, error)
	RevokeAllForEvent func(eventID, actorID uuid.UUID) error
}

var eventClientDeps EventClientDeps

func InitEventClientAccess(deps EventClientDeps) {
	eventClientDeps = deps
}

// markViewerAccess sets viewer_access on user-scoped list rows while event
// clients are enabled: "event_client" for rows reached through a grant,
// "organization" otherwise. Roots and client_id-scoped lists get no marker.
func markViewerAccess(user *models.User, responses []dtos.EventResponse) error {
	if user == nil || user.IsPlatformAdmin() || len(responses) == 0 ||
		!eventiapp.EventClientAccessEnabled() || eventClientDeps.GrantedEventIDs == nil {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(responses))
	for _, response := range responses {
		ids = append(ids, response.ID)
	}
	granted, err := eventClientDeps.GrantedEventIDs(user.ID, ids)
	if err != nil {
		return err
	}
	for index := range responses {
		if _, ok := granted[responses[index].ID]; ok {
			responses[index].ViewerAccess = dtos.EventViewerAccessEventClient
		} else {
			responses[index].ViewerAccess = dtos.EventViewerAccessOrganization
		}
	}
	return nil
}

// revokeEventClientsAfterMove runs whether or not the flag is on: grants
// whose organization no longer owns the event are void, and revoking them
// keeps the table honest. A failure is logged; authorization already ignores
// such rows because the grant organization must equal the event organization.
func revokeEventClientsAfterMove(c echo.Context, eventID uuid.UUID, original, updated *uuid.UUID, actorID uuid.UUID) {
	if eventClientDeps.RevokeAllForEvent == nil || sameClient(original, updated) {
		return
	}
	if err := eventClientDeps.RevokeAllForEvent(eventID, actorID); err != nil {
		slog.Error("event client grants were not revoked after an organization change",
			"request_id", c.Response().Header().Get(echo.HeaderXRequestID), "event_id", eventID, "error", err)
	}
}

func sameClient(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
