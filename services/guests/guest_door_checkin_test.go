package guests

import (
	"errors"
	"sync"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pendingGuest(eventID uuid.UUID, catalog statusCatalog) models.Guest {
	return models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "pending", GuestStatusID: catalog.pending, StatusID: catalog.pending}
}

// selfConfirmedGuest answered the public RSVP: rsvp_status confirmed through
// the web. The public RSVP does not move guest_status_id.
func selfConfirmedGuest(eventID uuid.UUID, catalog statusCatalog, at time.Time) models.Guest {
	return models.Guest{
		ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "confirmed", RSVPMethod: "web", RSVPAt: &at,
		GuestStatusID: catalog.pending, StatusID: catalog.pending,
	}
}

func assertNoDoorCheckin(t *testing.T, guest models.Guest) {
	t.Helper()
	assert.Nil(t, guest.CheckedInAt, "checked_in_at")
	assert.Nil(t, guest.CheckedInByUserID, "checked_in_by_user_id")
	assert.Nil(t, guest.CheckinPreviousRSVPStatus, "checkin_previous_rsvp_status")
	assert.Nil(t, guest.CheckinPreviousGuestStatusID, "checkin_previous_guest_status_id")
	assert.Nil(t, guest.CheckinPreviousRSVPMethod, "checkin_previous_rsvp_method")
	assert.Nil(t, guest.CheckinPreviousRSVPAt, "checkin_previous_rsvp_at")
}

func TestCheckInGuests_RecordsWhatTheUndoRestores(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newStatusCatalog()
	guest := pendingGuest(eventID, catalog)
	repo := newTransitionGuestRepo(guest)
	svc := newTransitionService(repo, nil)
	staff := uuid.Must(uuid.NewV4())
	before := time.Now()

	require.NoError(t, svc.CheckInGuests(eventID, []uuid.UUID{guest.ID}, catalog.confirmed, staff))

	got := repo.guest(guest.ID)
	assert.Equal(t, "confirmed", got.RSVPStatus)
	assert.Equal(t, "host", got.RSVPMethod)
	assert.Equal(t, catalog.confirmed, got.GuestStatusID)
	require.NotNil(t, got.RSVPAt)
	require.NotNil(t, got.CheckedInAt)
	assert.False(t, got.CheckedInAt.Before(before))
	require.NotNil(t, got.CheckedInByUserID)
	assert.Equal(t, staff, *got.CheckedInByUserID)
	require.NotNil(t, got.CheckinPreviousRSVPStatus)
	assert.Equal(t, "pending", *got.CheckinPreviousRSVPStatus)
	require.NotNil(t, got.CheckinPreviousRSVPMethod)
	assert.Equal(t, "", *got.CheckinPreviousRSVPMethod)
	require.NotNil(t, got.CheckinPreviousGuestStatusID)
	assert.Equal(t, catalog.pending, *got.CheckinPreviousGuestStatusID)
	assert.Nil(t, got.CheckinPreviousRSVPAt)
	assert.True(t, got.DoorCheckedIn())
}

func TestCheckInGuests_SecondCheckInKeepsTheFirstProvenanceAndCountsOnce(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newStatusCatalog()
	analyticsRepo := &mockEventAnalyticsRepo{current: &models.EventAnalytics{EventID: eventID, RSVPConfirmed: 2}}
	defer withAnalyticsService(analyticsRepo, nil)()
	guest := pendingGuest(eventID, catalog)
	repo := newTransitionGuestRepo(guest)
	svc := newTransitionService(repo, nil)
	first, second := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())

	require.NoError(t, svc.CheckInGuests(eventID, []uuid.UUID{guest.ID}, catalog.confirmed, first))
	afterFirst := repo.guest(guest.ID)
	require.NoError(t, svc.CheckInGuests(eventID, []uuid.UUID{guest.ID}, catalog.confirmed, second))

	assert.Equal(t, afterFirst, repo.guest(guest.ID), "a repeat check-in writes nothing")
	assert.Equal(t, first, *afterFirst.CheckedInByUserID)
	assert.Equal(t, "pending", *afterFirst.CheckinPreviousRSVPStatus)
	assert.Equal(t, 3, analyticsRepo.current.RSVPConfirmed)
}

func TestUndoGuestCheckins_RestoresThePreviousRSVPAndClearsProvenance(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newStatusCatalog()
	analyticsRepo := &mockEventAnalyticsRepo{current: &models.EventAnalytics{EventID: eventID, RSVPConfirmed: 4}}
	defer withAnalyticsService(analyticsRepo, nil)()
	guest := pendingGuest(eventID, catalog)
	repo := newTransitionGuestRepo(guest)
	svc := newTransitionService(repo, nil)

	require.NoError(t, svc.CheckInGuests(eventID, []uuid.UUID{guest.ID}, catalog.confirmed, uuid.Must(uuid.NewV4())))
	require.Equal(t, 5, analyticsRepo.current.RSVPConfirmed)
	require.NoError(t, svc.UndoGuestCheckins(eventID, []uuid.UUID{guest.ID}))

	got := repo.guest(guest.ID)
	assert.Equal(t, "pending", got.RSVPStatus)
	assert.Equal(t, "", got.RSVPMethod)
	assert.Equal(t, catalog.pending, got.GuestStatusID)
	assert.Equal(t, catalog.pending, got.StatusID)
	assert.Nil(t, got.RSVPAt)
	assertNoDoorCheckin(t, got)
	assert.Equal(t, 4, analyticsRepo.current.RSVPConfirmed)
}

// A guest who confirmed online and is then checked in at the door gets their
// own answer (method and timestamp) back on undo, never pending.
func TestUndoGuestCheckins_GivesASelfConfirmedGuestTheirOwnRSVPBack(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newStatusCatalog()
	analyticsRepo := &mockEventAnalyticsRepo{current: &models.EventAnalytics{EventID: eventID, RSVPConfirmed: 1}}
	defer withAnalyticsService(analyticsRepo, nil)()
	answeredAt := time.Date(2026, time.April, 2, 18, 30, 0, 0, time.UTC)
	guest := selfConfirmedGuest(eventID, catalog, answeredAt)
	repo := newTransitionGuestRepo(guest)
	svc := newTransitionService(repo, nil)

	require.NoError(t, svc.CheckInGuests(eventID, []uuid.UUID{guest.ID}, catalog.confirmed, uuid.Must(uuid.NewV4())))
	checkedIn := repo.guest(guest.ID)
	assert.Equal(t, "host", checkedIn.RSVPMethod)
	assert.True(t, checkedIn.RSVPAt.Equal(answeredAt), "confirmed to confirmed keeps rsvp_at")

	require.NoError(t, svc.UndoGuestCheckins(eventID, []uuid.UUID{guest.ID}))

	got := repo.guest(guest.ID)
	assert.Equal(t, "confirmed", got.RSVPStatus)
	assert.Equal(t, "web", got.RSVPMethod)
	assert.Equal(t, catalog.pending, got.GuestStatusID)
	require.NotNil(t, got.RSVPAt)
	assert.True(t, got.RSVPAt.Equal(answeredAt))
	assertNoDoorCheckin(t, got)
	assert.Nil(t, analyticsRepo.updated, "confirmed stayed confirmed throughout")
}

// Only a door check-in can be undone: a guest confirmed any other way (their
// own RSVP, the host in the dashboard, only through the catalog status, or
// their own RSVP given after a door check-in) is refused, and the whole
// request writes nothing.
func TestUndoGuestCheckins_RefusesConfirmationsThatAreNotADoorCheckin(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newStatusCatalog()
	answeredAt := time.Date(2026, time.April, 2, 18, 30, 0, 0, time.UTC)
	byHostWithoutDoor := models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "confirmed", RSVPMethod: "host", GuestStatusID: catalog.confirmed}
	byStatusOnly := models.Guest{
		ID: uuid.Must(uuid.NewV4()), EventID: eventID, GuestStatusID: catalog.confirmed,
		GuestStatus: models.GuestStatus{ID: catalog.confirmed, Code: "confirmed"},
	}
	ownAnswerAfterDoor := selfConfirmedGuest(eventID, catalog, answeredAt)
	checkedInAt := answeredAt.Add(-time.Hour)
	previous := "pending"
	ownAnswerAfterDoor.CheckedInAt = &checkedInAt
	ownAnswerAfterDoor.CheckinPreviousRSVPStatus = &previous
	cases := map[string]models.Guest{
		"own RSVP":                             selfConfirmedGuest(eventID, catalog, answeredAt),
		"host confirmation without a check-in": byHostWithoutDoor,
		"confirmed only through the status":    byStatusOnly,
		"own RSVP given after a door check-in": ownAnswerAfterDoor,
	}
	for name, refused := range cases {
		t.Run(name, func(t *testing.T) {
			door := pendingGuest(eventID, catalog)
			repo := newTransitionGuestRepo(door, refused)
			svc := newTransitionService(repo, nil)
			require.NoError(t, svc.CheckInGuests(eventID, []uuid.UUID{door.ID}, catalog.confirmed, uuid.Must(uuid.NewV4())))

			err := svc.UndoGuestCheckins(eventID, []uuid.UUID{door.ID, refused.ID})

			require.ErrorIs(t, err, ErrCheckinNotUndoable)
			assert.Contains(t, err.Error(), refused.ID.String())
			door = repo.guest(door.ID)
			assert.True(t, door.DoorCheckedIn(), "the batch is all or nothing")
			assert.Equal(t, refused, repo.guest(refused.ID))
		})
	}
}

// Undo means "no longer marked as arrived": a guest who is not confirmed has
// nothing to undo, so a repeated or late undo succeeds without writing.
func TestUndoGuestCheckins_GuestWhoIsNotConfirmedIsANoOp(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newStatusCatalog()
	declinedOnline := models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "declined", RSVPMethod: "web", GuestStatusID: catalog.pending}
	guests := []models.Guest{pendingGuest(eventID, catalog), declinedOnline}
	repo := newTransitionGuestRepo(guests...)
	svc := newTransitionService(repo, nil)

	require.NoError(t, svc.UndoGuestCheckins(eventID, []uuid.UUID{guests[0].ID, guests[1].ID}))

	assert.Equal(t, guests[0], repo.guest(guests[0].ID))
	assert.Equal(t, guests[1], repo.guest(guests[1].ID))
}

func TestUndoGuestCheckins_DoubleUndoIsIdempotent(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newStatusCatalog()
	analyticsRepo := &mockEventAnalyticsRepo{current: &models.EventAnalytics{EventID: eventID, RSVPConfirmed: 7}}
	defer withAnalyticsService(analyticsRepo, nil)()
	guest := pendingGuest(eventID, catalog)
	repo := newTransitionGuestRepo(guest)
	svc := newTransitionService(repo, nil)
	require.NoError(t, svc.CheckInGuests(eventID, []uuid.UUID{guest.ID}, catalog.confirmed, uuid.Must(uuid.NewV4())))

	require.NoError(t, svc.UndoGuestCheckins(eventID, []uuid.UUID{guest.ID}))
	afterFirst := repo.guest(guest.ID)
	require.NoError(t, svc.UndoGuestCheckins(eventID, []uuid.UUID{guest.ID}))

	assert.Equal(t, afterFirst, repo.guest(guest.ID))
	assert.Equal(t, 7, analyticsRepo.current.RSVPConfirmed)
}

// Concurrent undos are serialized by the row lock: exactly one restores and
// the others find nothing to undo. When the guest's own answer was confirmed
// the others are refused instead; the guest keeps their answer either way and
// the analytics move once.
func TestUndoGuestCheckins_ConcurrentUndosRestoreExactlyOnce(t *testing.T) {
	answeredAt := time.Date(2026, time.April, 2, 18, 30, 0, 0, time.UTC)
	for name, ownAnswer := range map[string]bool{"pending before check-in": false, "self-confirmed before check-in": true} {
		t.Run(name, func(t *testing.T) {
			eventID := uuid.Must(uuid.NewV4())
			catalog := newStatusCatalog()
			analyticsRepo := &mockEventAnalyticsRepo{current: &models.EventAnalytics{EventID: eventID, RSVPConfirmed: 3}}
			defer withAnalyticsService(analyticsRepo, nil)()
			guest := pendingGuest(eventID, catalog)
			if ownAnswer {
				guest = selfConfirmedGuest(eventID, catalog, answeredAt)
			}
			repo := newTransitionGuestRepo(guest)
			svc := newTransitionService(repo, nil)
			require.NoError(t, svc.CheckInGuests(eventID, []uuid.UUID{guest.ID}, catalog.confirmed, uuid.Must(uuid.NewV4())))

			const callers = 8
			errs := make(chan error, callers)
			var wg sync.WaitGroup
			for range callers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- svc.UndoGuestCheckins(eventID, []uuid.UUID{guest.ID})
				}()
			}
			wg.Wait()
			close(errs)

			succeeded, refused := 0, 0
			for err := range errs {
				switch {
				case err == nil:
					succeeded++
				case errors.Is(err, ErrCheckinNotUndoable):
					refused++
				default:
					t.Fatalf("unexpected error: %v", err)
				}
			}
			if ownAnswer {
				assert.Equal(t, 1, succeeded)
				assert.Equal(t, callers-1, refused)
			} else {
				assert.Equal(t, callers, succeeded)
			}
			got := repo.guest(guest.ID)
			assert.Equal(t, guest.RSVPStatus, got.RSVPStatus)
			assert.Equal(t, guest.RSVPMethod, got.RSVPMethod)
			assert.Equal(t, guest.GuestStatusID, got.GuestStatusID)
			assertNoDoorCheckin(t, got)
			assert.Equal(t, 3, analyticsRepo.current.RSVPConfirmed)
		})
	}
}

func TestUndoGuestCheckins_GuestOfAnotherEventWritesNothing(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newStatusCatalog()
	ours := pendingGuest(eventID, catalog)
	theirs := pendingGuest(uuid.Must(uuid.NewV4()), catalog)
	repo := newTransitionGuestRepo(ours, theirs)
	svc := newTransitionService(repo, nil)
	require.NoError(t, svc.CheckInGuests(eventID, []uuid.UUID{ours.ID}, catalog.confirmed, uuid.Must(uuid.NewV4())))

	require.Error(t, svc.UndoGuestCheckins(eventID, []uuid.UUID{ours.ID, theirs.ID}))

	ours = repo.guest(ours.ID)
	assert.True(t, ours.DoorCheckedIn())
}

// A guest:manage status change replaces whatever the door wrote, so the door
// can no longer undo it.
func TestBulkUpdateGuestStatus_ClearsDoorCheckinProvenance(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newStatusCatalog()
	guest := pendingGuest(eventID, catalog)
	repo := newTransitionGuestRepo(guest)
	svc := newTransitionService(repo, nil)
	require.NoError(t, svc.CheckInGuests(eventID, []uuid.UUID{guest.ID}, catalog.confirmed, uuid.Must(uuid.NewV4())))

	require.NoError(t, svc.BulkUpdateGuestStatus(eventID, []uuid.UUID{guest.ID}, catalog.declined, "declined", "host"))

	got := repo.guest(guest.ID)
	assert.Equal(t, "declined", got.RSVPStatus)
	assertNoDoorCheckin(t, got)
}

// PUT /guests/:id that changes the RSVP replaces the door's answer; one that
// leaves the RSVP alone keeps the provenance.
func TestUpdateGuest_RSVPChangeClearsDoorCheckinProvenance(t *testing.T) {
	statusID := uuid.Must(uuid.NewV4())
	checkedInAt := time.Date(2026, time.May, 1, 20, 0, 0, 0, time.UTC)
	previous := "pending"
	doorGuest := func() *models.Guest {
		at := checkedInAt
		prev := previous
		return &models.Guest{
			ID: uuid.Must(uuid.NewV4()), EventID: uuid.Must(uuid.NewV4()), FirstName: "Ana",
			RSVPStatus: "confirmed", RSVPMethod: "host", GuestStatusID: statusID,
			CheckedInAt: &at, CheckinPreviousRSVPStatus: &prev,
		}
	}
	cases := map[string]struct {
		edit      func(g *models.Guest)
		keepsDoor bool
	}{
		"notes only":           {func(g *models.Guest) { g.Notes = "VIP" }, true},
		"rsvp_status declined": {func(g *models.Guest) { g.RSVPStatus = "declined" }, false},
		"rsvp_method web":      {func(g *models.Guest) { g.RSVPMethod = "web" }, false},
		"another status id":    {func(g *models.Guest) { g.GuestStatusID = uuid.Must(uuid.NewV4()) }, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stored := doorGuest()
			var written *models.Guest
			repo := &mockGuestRepo{
				GetGuestByIDFunc: func(uuid.UUID) (*models.Guest, error) {
					copied := *stored
					return &copied, nil
				},
				UpdateGuestFunc: func(g *models.Guest) error {
					copied := *g
					written = &copied
					return nil
				},
			}
			svc := newGuestService(repo, nil, nil, nil)
			update := *stored
			tc.edit(&update)

			require.NoError(t, svc.UpdateGuest(&update))

			require.NotNil(t, written)
			if tc.keepsDoor {
				require.NotNil(t, written.CheckedInAt)
				assert.True(t, written.CheckedInAt.Equal(checkedInAt))
				assert.Equal(t, "pending", *written.CheckinPreviousRSVPStatus)
			} else {
				assertNoDoorCheckin(t, *written)
			}
		})
	}
}
