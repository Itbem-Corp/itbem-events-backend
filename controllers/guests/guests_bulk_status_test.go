package guests

import (
	"net/http"
	"sync/atomic"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/models"
	guestsService "events-stocks/services/guests"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// bulkStatusCatalog mirrors SeedGuestStatus: one row per code.
type bulkStatusCatalog struct {
	pending, confirmed, declined models.GuestStatus
}

func newBulkStatusCatalog() bulkStatusCatalog {
	return bulkStatusCatalog{
		pending:   models.GuestStatus{ID: uuid.Must(uuid.NewV4()), Code: "pending"},
		confirmed: models.GuestStatus{ID: uuid.Must(uuid.NewV4()), Code: "confirmed"},
		declined:  models.GuestStatus{ID: uuid.Must(uuid.NewV4()), Code: "declined"},
	}
}

// stubGuestStatusLookup serves the catalog to BulkUpdateGuestStatus; an id
// outside it answers gorm.ErrRecordNotFound like the repository does.
func stubGuestStatusLookup(t *testing.T, catalog bulkStatusCatalog) {
	t.Helper()
	orig := guestStatusByID
	guestStatusByID = func(id uuid.UUID) (*models.GuestStatus, error) {
		for _, status := range []models.GuestStatus{catalog.pending, catalog.confirmed, catalog.declined} {
			if status.ID == id {
				copied := status
				return &copied, nil
			}
		}
		return &models.GuestStatus{}, gorm.ErrRecordNotFound
	}
	t.Cleanup(func() { guestStatusByID = orig })
}

type bulkStatusCall struct {
	eventID    uuid.UUID
	ids        []uuid.UUID
	statusID   uuid.UUID
	rsvpStatus string
	rsvpMethod string
}

// useRecordingBulkStatusService swaps guestSvc for one whose repository records
// the bulk status writes.
func useRecordingBulkStatusService(t *testing.T) *[]bulkStatusCall {
	t.Helper()
	calls := &[]bulkStatusCall{}
	repo := &mockGuestRepo{
		bulkUpdateGuestStatusFunc: func(eventID uuid.UUID, ids []uuid.UUID, statusID uuid.UUID, rsvpStatus, rsvpMethod string) error {
			*calls = append(*calls, bulkStatusCall{eventID, append([]uuid.UUID(nil), ids...), statusID, rsvpStatus, rsvpMethod})
			return nil
		},
	}
	orig := guestSvc
	guestSvc = guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	t.Cleanup(func() { guestSvc = orig })
	return calls
}

// bulkStatusCaller is a standard (non-root) user: clientRole is the role on
// memberOf, eventRole the event assignment ("" means no assignment row).
type bulkStatusCaller struct {
	clientRole string
	eventRole  string
	memberOf   uuid.UUID
}

// setBulkStatusCaller installs authz hooks for a standard user and an event
// owned by eventClientID. It counts event-member role lookups, which happen
// once per RequireEventCapability call.
func setBulkStatusCaller(t *testing.T, eventID, eventClientID uuid.UUID, caller bulkStatusCaller) *atomic.Int64 {
	t.Helper()
	userID := uuid.Must(uuid.NewV4())
	var capabilityChecks atomic.Int64
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			return &models.User{ID: userID}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			clientID := eventClientID
			return &models.Event{ID: id, ClientID: &clientID}, nil
		},
		CheckAccessRecursive: func(gotUserID, clientID uuid.UUID) (bool, string) {
			if gotUserID != userID || clientID != caller.memberOf {
				return false, ""
			}
			return true, caller.clientRole
		},
		GetEventMemberRole: func(gotEventID, gotUserID uuid.UUID) (string, bool, error) {
			capabilityChecks.Add(1)
			if gotEventID != eventID || gotUserID != userID || caller.eventRole == "" {
				return "", false, nil
			}
			return caller.eventRole, true, nil
		},
	})
	t.Cleanup(restore)
	return &capabilityChecks
}

func bulkStatusBody(eventID uuid.UUID, ids []uuid.UUID, statusID uuid.UUID, rsvpStatus string) string {
	return `{"event_id":"` + eventID.String() + `","ids":` + guestIDsJSON(ids...) +
		`,"status_id":"` + statusID.String() + `","guest_status_id":"` + statusID.String() +
		`","rsvp_status":"` + rsvpStatus + `","rsvp_method":"host"}`
}

// The two ways a caller ends up with checkin:run but not guest:manage: the
// organization role is CHECKIN, or an event assignment narrows a broader
// organization role to CHECKIN.
func doorOnlyCallers(clientID uuid.UUID) map[string]bulkStatusCaller {
	return map[string]bulkStatusCaller{
		"organization CHECKIN role":                {clientRole: "CHECKIN", memberOf: clientID},
		"event CHECKIN assignment narrows manager": {clientRole: "EVENT_MANAGER", eventRole: "CHECKIN", memberOf: clientID},
	}
}

func TestBulkUpdateGuestStatus_CheckinOnlyCallerCanCheckGuestsIn(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	for name, caller := range doorOnlyCallers(clientID) {
		t.Run(name, func(t *testing.T) {
			eventID := uuid.Must(uuid.NewV4())
			catalog := newBulkStatusCatalog()
			stubGuestStatusLookup(t, catalog)
			calls := useRecordingBulkStatusService(t)
			setBulkStatusCaller(t, eventID, clientID, caller)
			ids := newGuestIDs(3)

			c, rec := newEchoCtx(http.MethodPatch, "/guests/bulk/status", bulkStatusBody(eventID, ids, catalog.confirmed.ID, "confirmed"))
			c.Set("cognito_sub", "door-sub")

			require.NoError(t, BulkUpdateGuestStatus(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Len(t, *calls, 1)
			assert.Equal(t, bulkStatusCall{eventID, ids, catalog.confirmed.ID, "confirmed", "host"}, (*calls)[0])
		})
	}
}

func TestBulkUpdateGuestStatus_CheckinOnlyCallerCannotRewriteRSVP(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	catalog := newBulkStatusCatalog()
	cases := map[string]struct {
		statusID   uuid.UUID
		rsvpStatus string
	}{
		"pending (undo check-in or reset RSVP)":     {catalog.pending.ID, "pending"},
		"declined":                                  {catalog.declined.ID, "declined"},
		"confirmed rsvp with declined status_id":    {catalog.declined.ID, "confirmed"},
		"confirmed rsvp with pending status_id":     {catalog.pending.ID, "confirmed"},
		"declined rsvp with confirmed status_id":    {catalog.confirmed.ID, "declined"},
		"pending rsvp with confirmed status_id":     {catalog.confirmed.ID, "pending"},
		"confirmed rsvp in upper case, declined id": {catalog.declined.ID, "CONFIRMED"},
	}
	for callerName, caller := range doorOnlyCallers(clientID) {
		for name, tc := range cases {
			t.Run(callerName+"/"+name, func(t *testing.T) {
				eventID := uuid.Must(uuid.NewV4())
				stubGuestStatusLookup(t, catalog)
				calls := useRecordingBulkStatusService(t)
				setBulkStatusCaller(t, eventID, clientID, caller)

				c, rec := newEchoCtx(http.MethodPatch, "/guests/bulk/status", bulkStatusBody(eventID, newGuestIDs(2), tc.statusID, tc.rsvpStatus))
				c.Set("cognito_sub", "door-sub")

				require.NoError(t, BulkUpdateGuestStatus(c))
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Body.String(), "guest:manage")
				assert.Empty(t, *calls, "a check-in-only caller must not reach the write")
			})
		}
	}
}

func TestBulkUpdateGuestStatus_CheckinOnlyCallerUnknownStatusIs400(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	stubGuestStatusLookup(t, newBulkStatusCatalog())
	calls := useRecordingBulkStatusService(t)
	setBulkStatusCaller(t, eventID, clientID, bulkStatusCaller{clientRole: "CHECKIN", memberOf: clientID})

	c, rec := newEchoCtx(http.MethodPatch, "/guests/bulk/status", bulkStatusBody(eventID, newGuestIDs(1), uuid.Must(uuid.NewV4()), "confirmed"))
	c.Set("cognito_sub", "door-sub")

	require.NoError(t, BulkUpdateGuestStatus(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "Invalid status_id")
	assert.Empty(t, *calls)
}

func TestBulkUpdateGuestStatus_GuestManagerKeepsFullBehaviour(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	catalog := newBulkStatusCatalog()
	callers := map[string]bulkStatusCaller{
		"organization EVENT_MANAGER":            {clientRole: "EVENT_MANAGER", memberOf: clientID},
		"organization OWNER":                    {clientRole: "OWNER", memberOf: clientID},
		"inherited ADMIN":                       {clientRole: "INHERITED_ADMIN", memberOf: clientID},
		"event EDITOR under EVENT_MANAGER":      {clientRole: "EVENT_MANAGER", eventRole: "EDITOR", memberOf: clientID},
		"event EVENT_OWNER under EVENT_MANAGER": {clientRole: "EVENT_MANAGER", eventRole: "EVENT_OWNER", memberOf: clientID},
	}
	transitions := map[string]struct {
		statusID   uuid.UUID
		rsvpStatus string
	}{
		"check-in":                     {catalog.confirmed.ID, "confirmed"},
		"undo to pending":              {catalog.pending.ID, "pending"},
		"declined":                     {catalog.declined.ID, "declined"},
		"mismatched status (as today)": {catalog.declined.ID, "confirmed"},
	}
	for callerName, caller := range callers {
		for name, tc := range transitions {
			t.Run(callerName+"/"+name, func(t *testing.T) {
				eventID := uuid.Must(uuid.NewV4())
				stubGuestStatusLookup(t, catalog)
				calls := useRecordingBulkStatusService(t)
				setBulkStatusCaller(t, eventID, clientID, caller)
				ids := newGuestIDs(2)

				c, rec := newEchoCtx(http.MethodPatch, "/guests/bulk/status", bulkStatusBody(eventID, ids, tc.statusID, tc.rsvpStatus))
				c.Set("cognito_sub", "manager-sub")

				require.NoError(t, BulkUpdateGuestStatus(c))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Len(t, *calls, 1)
				assert.Equal(t, bulkStatusCall{eventID, ids, tc.statusID, tc.rsvpStatus, "host"}, (*calls)[0])
			})
		}
	}
}

// A guest:manage role without checkin:run is still refused at the first gate,
// exactly as before: the endpoint's base capability did not change.
func TestBulkUpdateGuestStatus_GuestManageWithoutCheckinStillDenied(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	catalog := newBulkStatusCatalog()
	for _, role := range []string{"EDITOR", "MEMBER"} {
		t.Run(role, func(t *testing.T) {
			eventID := uuid.Must(uuid.NewV4())
			stubGuestStatusLookup(t, catalog)
			calls := useRecordingBulkStatusService(t)
			setBulkStatusCaller(t, eventID, clientID, bulkStatusCaller{clientRole: role, memberOf: clientID})

			c, rec := newEchoCtx(http.MethodPatch, "/guests/bulk/status", bulkStatusBody(eventID, newGuestIDs(1), catalog.confirmed.ID, "confirmed"))
			c.Set("cognito_sub", "editor-sub")

			require.NoError(t, BulkUpdateGuestStatus(c))
			assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Empty(t, *calls)
		})
	}
}

// A caller whose organization does not own the event is refused before any
// status lookup or write, for the check-in transition as well as the others.
func TestBulkUpdateGuestStatus_CallerFromAnotherOrganizationIsDenied(t *testing.T) {
	callerClientID := uuid.Must(uuid.NewV4())
	eventClientID := uuid.Must(uuid.NewV4())
	catalog := newBulkStatusCatalog()
	callers := map[string]bulkStatusCaller{
		"CHECKIN elsewhere":       {clientRole: "CHECKIN", memberOf: callerClientID},
		"EVENT_MANAGER elsewhere": {clientRole: "EVENT_MANAGER", memberOf: callerClientID},
		"OWNER elsewhere":         {clientRole: "OWNER", memberOf: callerClientID},
	}
	for callerName, caller := range callers {
		for _, rsvpStatus := range []string{"confirmed", "pending"} {
			t.Run(callerName+"/"+rsvpStatus, func(t *testing.T) {
				eventID := uuid.Must(uuid.NewV4())
				var lookups atomic.Int64
				orig := guestStatusByID
				guestStatusByID = func(id uuid.UUID) (*models.GuestStatus, error) {
					lookups.Add(1)
					return &catalog.confirmed, nil
				}
				t.Cleanup(func() { guestStatusByID = orig })
				calls := useRecordingBulkStatusService(t)
				setBulkStatusCaller(t, eventID, eventClientID, caller)
				statusID := catalog.confirmed.ID
				if rsvpStatus == "pending" {
					statusID = catalog.pending.ID
				}

				c, rec := newEchoCtx(http.MethodPatch, "/guests/bulk/status", bulkStatusBody(eventID, newGuestIDs(1), statusID, rsvpStatus))
				c.Set("cognito_sub", "outsider-sub")

				require.NoError(t, BulkUpdateGuestStatus(c))
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				assert.Zero(t, lookups.Load())
				assert.Empty(t, *calls)
			})
		}
	}
}

// The check-in path costs one status lookup and no second capability check;
// only a non-check-in transition pays for the guest:manage check.
func TestBulkUpdateGuestStatus_CheckinTransitionSkipsGuestManageCheck(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	catalog := newBulkStatusCatalog()
	stubGuestStatusLookup(t, catalog)
	useRecordingBulkStatusService(t)
	checks := setBulkStatusCaller(t, eventID, clientID, bulkStatusCaller{clientRole: "EVENT_MANAGER", memberOf: clientID})

	c, rec := newEchoCtx(http.MethodPatch, "/guests/bulk/status", bulkStatusBody(eventID, newGuestIDs(1), catalog.confirmed.ID, "confirmed"))
	c.Set("cognito_sub", "manager-sub")
	require.NoError(t, BulkUpdateGuestStatus(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, int64(1), checks.Load())

	c, rec = newEchoCtx(http.MethodPatch, "/guests/bulk/status", bulkStatusBody(eventID, newGuestIDs(1), catalog.pending.ID, "pending"))
	c.Set("cognito_sub", "manager-sub")
	require.NoError(t, BulkUpdateGuestStatus(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, int64(3), checks.Load())
}
