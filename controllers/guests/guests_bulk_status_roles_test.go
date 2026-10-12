package guests

import (
	"net/http"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setPlatformAdminCaller installs authz hooks for a platform administrator on
// an event owned by some organization the user is not a member of: the
// platform level alone decides, so membership and assignment hooks must not
// be consulted.
func setPlatformAdminCaller(t *testing.T, user models.User) {
	t.Helper()
	user.ID = uuid.Must(uuid.NewV4())
	eventClientID := uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			copied := user
			return &copied, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			clientID := eventClientID
			return &models.Event{ID: id, ClientID: &clientID}, nil
		},
		CheckAccessRecursive: func(uuid.UUID, uuid.UUID) (bool, string) {
			t.Error("a platform administrator must not need an organization membership")
			return false, ""
		},
		GetEventMemberRole: func(uuid.UUID, uuid.UUID) (string, bool, error) {
			t.Error("a platform administrator must not be narrowed by an event assignment")
			return "", false, nil
		},
	})
	t.Cleanup(restore)
}

// bulkStatusRoleCase runs the whole bulk status surface for one caller:
// check-in, undo of that check-in, a plain confirmed body (a door check-in
// without guest:manage, the organizer's answer with it), undo refused for a
// guest's own RSVP, and pending/declined, which need guest:manage.
type bulkStatusRoleCase struct {
	install           func(t *testing.T, eventID uuid.UUID)
	canSetOtherStatus bool
}

func runBulkStatusRoleCase(t *testing.T, tc bulkStatusRoleCase) {
	t.Helper()
	eventID := uuid.Must(uuid.NewV4())
	catalog := newBulkStatusCatalog()
	stubGuestStatusLookup(t, catalog)
	store := useBulkStatusStore(t)
	tc.install(t, eventID)
	door := seedPendingGuests(store, eventID, catalog, 2)
	plain := seedPendingGuests(store, eventID, catalog, 1)
	selfConfirmed := models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "confirmed", RSVPMethod: "web", GuestStatusID: catalog.pending.ID}
	store.put(selfConfirmed)

	code, body := patchBulkStatus(t, checkinActionBody(eventID, door, catalog.confirmed.ID))
	require.Equal(t, http.StatusOK, code, "check-in: %s", body)
	for _, id := range door {
		got := store.get(id)
		require.True(t, got.DoorCheckedIn())
	}

	code, body = patchBulkStatus(t, undoCheckinBody(eventID, door[:1]))
	require.Equal(t, http.StatusOK, code, "undo: %s", body)
	assertGuestRSVP(t, store.get(door[0]), catalog.pending, "pending", "")

	code, body = patchBulkStatus(t, bulkStatusBody(eventID, plain, catalog.confirmed.ID, "confirmed"))
	require.Equal(t, http.StatusOK, code, "plain confirm: %s", body)
	plainGuest := store.get(plain[0])
	assert.Equal(t, !tc.canSetOtherStatus, plainGuest.DoorCheckedIn(), "a plain confirm is a door check-in only without guest:manage")
	code, body = patchBulkStatus(t, undoCheckinBody(eventID, plain))
	if tc.canSetOtherStatus {
		assert.Equal(t, http.StatusConflict, code, "undo of the organizer's confirmation: %s", body)
		assertGuestRSVP(t, store.get(plain[0]), catalog.confirmed, "confirmed", "host")
	} else {
		assert.Equal(t, http.StatusOK, code, "undo of a plain door check-in: %s", body)
		assertGuestRSVP(t, store.get(plain[0]), catalog.pending, "pending", "")
	}

	code, body = patchBulkStatus(t, undoCheckinBody(eventID, []uuid.UUID{selfConfirmed.ID}))
	assert.Equal(t, http.StatusConflict, code, "undo of an own RSVP: %s", body)
	assert.Equal(t, selfConfirmed, store.get(selfConfirmed.ID))

	code, body = patchBulkStatus(t, bulkStatusBody(eventID, door[1:], catalog.declined.ID, "confirmed"))
	assert.Equal(t, http.StatusBadRequest, code, "mismatched status: %s", body)

	for rsvpStatus, status := range map[string]models.GuestStatus{"pending": catalog.pending, "declined": catalog.declined} {
		code, body = patchBulkStatus(t, bulkStatusBody(eventID, []uuid.UUID{selfConfirmed.ID}, status.ID, rsvpStatus))
		if tc.canSetOtherStatus {
			require.Equal(t, http.StatusOK, code, "%s: %s", rsvpStatus, body)
			assertGuestRSVP(t, store.get(selfConfirmed.ID), status, rsvpStatus, "host")
		} else {
			assert.Equal(t, http.StatusForbidden, code, "%s: %s", rsvpStatus, body)
			assert.Equal(t, selfConfirmed, store.get(selfConfirmed.ID))
		}
	}
}

// Primary root (legacy is_root or root_level 1) has every capability and the
// operational root (root_level 2) has checkin:run and guest:manage, so both
// keep full control of guest statuses on any organization's event.
func TestBulkUpdateGuestStatus_PlatformRootsHaveFullControl(t *testing.T) {
	roots := map[string]models.User{
		"primary root, legacy is_root":       {IsRoot: true},
		"primary root, root_level 1":         {RootLevel: models.RootLevelPrimary},
		"operational root, root_level 2":     {RootLevel: models.RootLevelOperational},
		"operational root with is_root, too": {IsRoot: true, RootLevel: models.RootLevelOperational},
	}
	for name, user := range roots {
		t.Run(name, func(t *testing.T) {
			runBulkStatusRoleCase(t, bulkStatusRoleCase{
				install:           func(t *testing.T, _ uuid.UUID) { setPlatformAdminCaller(t, user) },
				canSetOtherStatus: true,
			})
		})
	}
}

// An event assignment can only narrow the organization role: an organization
// CHECKIN member assigned to the event as EDITOR still lacks guest:manage, so
// they check guests in and undo those check-ins, nothing more.
func TestBulkUpdateGuestStatus_OrganizationCheckinWithEventEditorStaysCheckinOnly(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	runBulkStatusRoleCase(t, bulkStatusRoleCase{
		install: func(t *testing.T, eventID uuid.UUID) {
			setBulkStatusCaller(t, eventID, clientID, bulkStatusCaller{clientRole: "CHECKIN", eventRole: "EDITOR", memberOf: clientID})
		},
		canSetOtherStatus: false,
	})
}

// Without a platform root level, an OWNER role in some other organization
// gives nothing on this event: refused at the first gate, nothing written.
func TestBulkUpdateGuestStatus_NonRootWithoutMembershipIsDenied(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newBulkStatusCatalog()
	stubGuestStatusLookup(t, catalog)
	store := useBulkStatusStore(t)
	setBulkStatusCaller(t, eventID, uuid.Must(uuid.NewV4()), bulkStatusCaller{clientRole: "OWNER", memberOf: uuid.Must(uuid.NewV4())})

	code, body := patchBulkStatus(t, bulkStatusBody(eventID, newGuestIDs(1), catalog.confirmed.ID, "confirmed"))

	assert.Equal(t, http.StatusForbidden, code, body)
	assert.Zero(t, store.writes())
}
