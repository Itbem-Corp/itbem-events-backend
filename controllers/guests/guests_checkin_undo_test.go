package guests

import (
	"net/http"
	"sync"
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func undoCheckinBody(eventID uuid.UUID, ids []uuid.UUID) string {
	return `{"event_id":"` + eventID.String() + `","ids":` + guestIDsJSON(ids...) + `,"action":"undo_checkin"}`
}

func seedPendingGuests(store *bulkStatusStore, eventID uuid.UUID, catalog bulkStatusCatalog, n int) []uuid.UUID {
	ids := newGuestIDs(n)
	for _, id := range ids {
		store.put(models.Guest{ID: id, EventID: eventID, RSVPStatus: "pending", GuestStatusID: catalog.pending.ID, StatusID: catalog.pending.ID})
	}
	return ids
}

func patchBulkStatus(t *testing.T, body string) (int, string) {
	t.Helper()
	c, rec := newEchoCtx(http.MethodPatch, "/guests/bulk/status", body)
	c.Set("cognito_sub", "caller-sub")
	require.NoError(t, BulkUpdateGuestStatus(c))
	return rec.Code, rec.Body.String()
}

func TestBulkUpdateGuestStatus_CheckinOnlyCallerCanUndoADoorCheckin(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	for name, caller := range doorOnlyCallers(clientID) {
		t.Run(name, func(t *testing.T) {
			eventID := uuid.Must(uuid.NewV4())
			catalog := newBulkStatusCatalog()
			stubGuestStatusLookup(t, catalog)
			store := useBulkStatusStore(t)
			setBulkStatusCaller(t, eventID, clientID, caller)
			ids := seedPendingGuests(store, eventID, catalog, 2)

			code, body := patchBulkStatus(t, bulkStatusBody(eventID, ids, catalog.confirmed.ID, "confirmed"))
			require.Equal(t, http.StatusOK, code, body)
			for _, id := range ids {
				checkedIn := store.get(id)
				require.True(t, checkedIn.DoorCheckedIn())
				require.NotNil(t, checkedIn.CheckedInByUserID, "the check-in records who did it")
			}

			code, body = patchBulkStatus(t, undoCheckinBody(eventID, ids))

			require.Equal(t, http.StatusOK, code, body)
			for _, id := range ids {
				got := store.get(id)
				assertGuestRSVP(t, got, catalog.pending, "pending", "")
				assert.Nil(t, got.CheckedInAt)
				assert.Nil(t, got.CheckinPreviousRSVPStatus)
			}
		})
	}
}

// The undo decides the restored values itself: status fields in the body are
// ignored, so they cannot be used to write an arbitrary RSVP.
func TestBulkUpdateGuestStatus_UndoIgnoresStatusFieldsInTheBody(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	catalog := newBulkStatusCatalog()
	stubGuestStatusLookup(t, catalog)
	store := useBulkStatusStore(t)
	setBulkStatusCaller(t, eventID, clientID, bulkStatusCaller{clientRole: "CHECKIN", memberOf: clientID})
	ids := seedPendingGuests(store, eventID, catalog, 1)
	code, body := patchBulkStatus(t, bulkStatusBody(eventID, ids, catalog.confirmed.ID, "confirmed"))
	require.Equal(t, http.StatusOK, code, body)

	code, body = patchBulkStatus(t, `{"event_id":"`+eventID.String()+`","ids":`+guestIDsJSON(ids...)+
		`,"action":" UNDO_CHECKIN ","status_id":"`+catalog.declined.ID.String()+`","rsvp_status":"declined","rsvp_method":"host"}`)

	require.Equal(t, http.StatusOK, code, body)
	assertGuestRSVP(t, store.get(ids[0]), catalog.pending, "pending", "")
}

func TestBulkUpdateGuestStatus_CheckinOnlyCallerCannotUndoAGuestsOwnRSVP(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	for name, caller := range doorOnlyCallers(clientID) {
		t.Run(name, func(t *testing.T) {
			eventID := uuid.Must(uuid.NewV4())
			catalog := newBulkStatusCatalog()
			stubGuestStatusLookup(t, catalog)
			store := useBulkStatusStore(t)
			setBulkStatusCaller(t, eventID, clientID, caller)
			selfConfirmed := models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "confirmed", RSVPMethod: "web", GuestStatusID: catalog.pending.ID}
			store.put(selfConfirmed)

			code, body := patchBulkStatus(t, undoCheckinBody(eventID, []uuid.UUID{selfConfirmed.ID}))

			assert.Equal(t, http.StatusConflict, code, body)
			assert.Contains(t, body, "Check-in cannot be undone")
			assert.Contains(t, body, selfConfirmed.ID.String())
			assert.Equal(t, selfConfirmed, store.get(selfConfirmed.ID))
			assert.Zero(t, store.writes())
		})
	}
}

// A guest:manage caller undoes a door check-in the same way, and keeps full
// control through an explicit status change.
func TestBulkUpdateGuestStatus_GuestManagerCanUndoAndStillOverride(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	catalog := newBulkStatusCatalog()
	stubGuestStatusLookup(t, catalog)
	store := useBulkStatusStore(t)
	setBulkStatusCaller(t, eventID, clientID, bulkStatusCaller{clientRole: "EVENT_MANAGER", memberOf: clientID})
	door := seedPendingGuests(store, eventID, catalog, 1)
	selfConfirmed := models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "confirmed", RSVPMethod: "web", GuestStatusID: catalog.pending.ID}
	store.put(selfConfirmed)

	code, body := patchBulkStatus(t, bulkStatusBody(eventID, door, catalog.confirmed.ID, "confirmed"))
	require.Equal(t, http.StatusOK, code, body)
	code, body = patchBulkStatus(t, undoCheckinBody(eventID, door))
	require.Equal(t, http.StatusOK, code, body)
	assertGuestRSVP(t, store.get(door[0]), catalog.pending, "pending", "")

	code, body = patchBulkStatus(t, undoCheckinBody(eventID, []uuid.UUID{selfConfirmed.ID}))
	assert.Equal(t, http.StatusConflict, code, body)
	code, body = patchBulkStatus(t, bulkStatusBody(eventID, []uuid.UUID{selfConfirmed.ID}, catalog.pending.ID, "pending"))
	require.Equal(t, http.StatusOK, code, body)
	assertGuestRSVP(t, store.get(selfConfirmed.ID), catalog.pending, "pending", "host")
}

func TestBulkUpdateGuestStatus_DoubleAndConcurrentUndoAreIdempotent(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	catalog := newBulkStatusCatalog()
	stubGuestStatusLookup(t, catalog)
	store := useBulkStatusStore(t)
	setBulkStatusCaller(t, eventID, clientID, bulkStatusCaller{clientRole: "CHECKIN", memberOf: clientID})
	ids := seedPendingGuests(store, eventID, catalog, 3)
	code, body := patchBulkStatus(t, bulkStatusBody(eventID, ids, catalog.confirmed.ID, "confirmed"))
	require.Equal(t, http.StatusOK, code, body)

	const callers = 6
	codes := make(chan int, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, rec := newEchoCtx(http.MethodPatch, "/guests/bulk/status", undoCheckinBody(eventID, ids))
			c.Set("cognito_sub", "door-sub")
			if err := BulkUpdateGuestStatus(c); err != nil {
				codes <- -1
				return
			}
			codes <- rec.Code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		assert.Equal(t, http.StatusOK, code)
	}
	code, body = patchBulkStatus(t, undoCheckinBody(eventID, ids))
	assert.Equal(t, http.StatusOK, code, body)

	for _, id := range ids {
		got := store.get(id)
		assertGuestRSVP(t, got, catalog.pending, "pending", "")
		assert.Nil(t, got.CheckedInAt)
	}
}

func TestBulkUpdateGuestStatus_UndoWithAGuestOfAnotherEventIsRejected(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	catalog := newBulkStatusCatalog()
	stubGuestStatusLookup(t, catalog)
	store := useBulkStatusStore(t)
	setBulkStatusCaller(t, eventID, clientID, bulkStatusCaller{clientRole: "CHECKIN", memberOf: clientID})
	ours := seedPendingGuests(store, eventID, catalog, 1)
	code, body := patchBulkStatus(t, bulkStatusBody(eventID, ours, catalog.confirmed.ID, "confirmed"))
	require.Equal(t, http.StatusOK, code, body)
	theirs := seedPendingGuests(store, uuid.Must(uuid.NewV4()), catalog, 1)
	writes := store.writes()

	code, body = patchBulkStatus(t, undoCheckinBody(eventID, append(ours, theirs...)))

	assert.Equal(t, http.StatusBadRequest, code, body)
	assert.Equal(t, writes, store.writes())
	got := store.get(ours[0])
	assert.True(t, got.DoorCheckedIn())
}

// Undo on an event whose organization the caller does not belong to is
// refused before any guest is read.
func TestBulkUpdateGuestStatus_UndoOnAnotherOrganizationsEventIsDenied(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	store := useBulkStatusStore(t)
	setBulkStatusCaller(t, eventID, uuid.Must(uuid.NewV4()), bulkStatusCaller{clientRole: "CHECKIN", memberOf: uuid.Must(uuid.NewV4())})

	code, body := patchBulkStatus(t, undoCheckinBody(eventID, newGuestIDs(1)))

	assert.Equal(t, http.StatusForbidden, code, body)
	assert.Zero(t, store.writes())
}

func TestBulkUpdateGuestStatus_UnknownActionIs400(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	catalog := newBulkStatusCatalog()
	stubGuestStatusLookup(t, catalog)
	store := useBulkStatusStore(t)
	setBulkStatusCaller(t, eventID, clientID, bulkStatusCaller{clientRole: "OWNER", memberOf: clientID})

	for _, action := range []string{"checkin", "undo", "delete"} {
		code, body := patchBulkStatus(t, `{"event_id":"`+eventID.String()+`","ids":`+guestIDsJSON(newGuestIDs(1)...)+
			`,"action":"`+action+`","status_id":"`+catalog.confirmed.ID.String()+`","rsvp_status":"confirmed"}`)
		assert.Equal(t, http.StatusBadRequest, code, body)
		assert.Contains(t, body, "Invalid action")
	}
	assert.Zero(t, store.writes())
}
