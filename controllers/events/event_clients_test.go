package events

import (
	"errors"
	"net/http"
	"testing"

	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/internal/products/eventiapp"
	"events-stocks/models"
	eventsService "events-stocks/services/events"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withEventClientDeps(t *testing.T, deps EventClientDeps) {
	t.Helper()
	previous := eventClientDeps
	eventClientDeps = deps
	t.Cleanup(func() { eventClientDeps = previous })
}

func withEventClientFlag(t *testing.T, enabled bool) {
	t.Helper()
	previous := eventiapp.EventClientAccessEnabled()
	eventiapp.SetEventClientAccess(enabled)
	t.Cleanup(func() { eventiapp.SetEventClientAccess(previous) })
}

func TestMarkViewerAccess(t *testing.T) {
	granted := uuid.Must(uuid.NewV4())
	member := uuid.Must(uuid.NewV4())
	withEventClientDeps(t, EventClientDeps{GrantedEventIDs: func(uuid.UUID, []uuid.UUID) (map[uuid.UUID]struct{}, error) {
		return map[uuid.UUID]struct{}{granted: {}}, nil
	}})
	rows := func() []dtos.EventResponse { return []dtos.EventResponse{{ID: granted}, {ID: member}} }

	withEventClientFlag(t, true)
	marked := rows()
	require.NoError(t, markViewerAccess(&models.User{ID: uuid.Must(uuid.NewV4())}, marked))
	assert.Equal(t, "event_client", marked[0].ViewerAccess)
	assert.Equal(t, "organization", marked[1].ViewerAccess)

	root := rows()
	require.NoError(t, markViewerAccess(&models.User{RootLevel: models.RootLevelPrimary}, root))
	assert.Empty(t, root[0].ViewerAccess)

	withEventClientFlag(t, false)
	off := rows()
	require.NoError(t, markViewerAccess(&models.User{ID: uuid.Must(uuid.NewV4())}, off))
	assert.Empty(t, off[0].ViewerAccess)
}

func TestMarkViewerAccessPropagatesLookupErrors(t *testing.T) {
	withEventClientFlag(t, true)
	withEventClientDeps(t, EventClientDeps{GrantedEventIDs: func(uuid.UUID, []uuid.UUID) (map[uuid.UUID]struct{}, error) {
		return nil, errors.New("db down")
	}})
	assert.Error(t, markViewerAccess(&models.User{ID: uuid.Must(uuid.NewV4())}, []dtos.EventResponse{{ID: uuid.Must(uuid.NewV4())}}))
}

// moveFixture: the caller is an event client of the event in org P (no
// membership) and EVENT_MANAGER of org Q.
func moveFixture(t *testing.T) (eventID, orgP, orgQ uuid.UUID, updates *int) {
	t.Helper()
	eventID = uuid.Must(uuid.NewV4())
	orgP = uuid.Must(uuid.NewV4())
	orgQ = uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) { return &models.User{ID: userID, IsActive: true}, nil },
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: id, Name: "Boda", Identifier: "boda", ClientID: &orgP}, nil
		},
		CheckAccessRecursiveE: func(_ uuid.UUID, clientID uuid.UUID) (bool, string, error) {
			if clientID == orgQ {
				return true, "EVENT_MANAGER", nil
			}
			return false, "", nil
		},
		HasEventClientGrant:  func(id, _ uuid.UUID) (bool, error) { return id == eventID, nil },
		ResolveClientProduct: func(uuid.UUID) (string, error) { return "eventiapp", nil },
	})
	t.Cleanup(restore)
	count := 0
	updates = &count
	svc := eventsService.NewEventService(&mockEventsRepo{UpdateEventFunc: func(*models.Event) error {
		count++
		return nil
	}}, &mockCacheRepo{})
	previous := eventSvc
	eventSvc = svc
	t.Cleanup(func() { eventSvc = previous })
	return eventID, orgP, orgQ, updates
}

func TestEventClientCannotMoveEventToAnotherOrganization(t *testing.T) {
	eventID, _, orgQ, updates := moveFixture(t)
	c, rec := newEchoCtx(http.MethodPut, "/api/events/x", `{"client_id":"`+orgQ.String()+`"}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	c.Set("cognito_sub", "sub")
	require.NoError(t, UpdateEvent(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Zero(t, *updates, "nothing is written")
}

func TestEventClientCannotDetachEvent(t *testing.T) {
	eventID, _, _, updates := moveFixture(t)
	c, rec := newEchoCtx(http.MethodPut, "/api/events/x", `{"client_id":null}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	c.Set("cognito_sub", "sub")
	require.NoError(t, UpdateEvent(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Zero(t, *updates)
}

func TestEventClientCanEditEventWithinItsOrganization(t *testing.T) {
	eventID, orgP, _, updates := moveFixture(t)
	revoked := 0
	withEventClientDeps(t, EventClientDeps{RevokeAllForEvent: func(uuid.UUID, uuid.UUID) error { revoked++; return nil }})
	c, rec := newEchoCtx(http.MethodPut, "/api/events/x", `{"client_id":"`+orgP.String()+`","name":"Boda Ana"}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	c.Set("cognito_sub", "sub")
	require.NoError(t, UpdateEvent(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, *updates)
	assert.Zero(t, revoked, "no move, no revoke")
}

func TestEventClientCannotDuplicateIntoAnotherOrganization(t *testing.T) {
	eventID, _, orgQ, _ := moveFixture(t)
	previous := duplicateSvc
	duplicateSvc = nil
	t.Cleanup(func() { duplicateSvc = previous })
	c, rec := newEchoCtx(http.MethodPost, "/api/events/x/duplicate", `{"client_id":"`+orgQ.String()+`"}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	c.Set("cognito_sub", "sub")
	require.NoError(t, DuplicateEvent(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRevokeAfterMoveOnlyWhenOrganizationChanges(t *testing.T) {
	calls := 0
	withEventClientDeps(t, EventClientDeps{RevokeAllForEvent: func(uuid.UUID, uuid.UUID) error { calls++; return nil }})
	a, b := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	c, _ := newEchoCtx(http.MethodPut, "/", "")
	revokeEventClientsAfterMove(c, uuid.Must(uuid.NewV4()), &a, &a, uuid.Nil)
	assert.Zero(t, calls)
	revokeEventClientsAfterMove(c, uuid.Must(uuid.NewV4()), &a, &b, uuid.Nil)
	revokeEventClientsAfterMove(c, uuid.Must(uuid.NewV4()), &a, nil, uuid.Nil)
	assert.Equal(t, 2, calls)
}
