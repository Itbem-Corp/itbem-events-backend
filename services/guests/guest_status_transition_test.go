package guests

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// transitionGuestRepo is an in-memory stand-in for
// guestrepository.TransitionGuestStatuses: one mutex plays the row locks, a
// guest of another event fails the whole call, and only the guests apply
// returns are stored.
type transitionGuestRepo struct {
	mockGuestRepo
	mu     sync.Mutex
	guests map[uuid.UUID]models.Guest
	calls  int
}

func newTransitionGuestRepo(guests ...models.Guest) *transitionGuestRepo {
	repo := &transitionGuestRepo{guests: make(map[uuid.UUID]models.Guest, len(guests))}
	for _, guest := range guests {
		repo.guests[guest.ID] = guest
	}
	return repo
}

func (r *transitionGuestRepo) TransitionGuestStatuses(eventID uuid.UUID, ids []uuid.UUID, apply func(locked []models.Guest) ([]models.Guest, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	locked := make([]models.Guest, 0, len(ids))
	for _, id := range ids {
		guest, ok := r.guests[id]
		if !ok || guest.EventID != eventID {
			return errors.New("one or more guests do not belong to event")
		}
		locked = append(locked, guest)
	}
	changed, err := apply(locked)
	if err != nil {
		return err
	}
	for _, guest := range changed {
		r.guests[guest.ID] = guest
	}
	return nil
}

func (r *transitionGuestRepo) guest(id uuid.UUID) models.Guest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.guests[id]
}

// recordingCache records guest cache invalidations ("resource:key") and the
// event list patterns deleted.
type recordingCache struct {
	mockCacheRepo
	mu              sync.Mutex
	invalidated     []string
	deletedPatterns []string
}

func (c *recordingCache) Invalidate(resource, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidated = append(c.invalidated, resource+":"+key)
	return nil
}

func (c *recordingCache) DeleteKeysByPattern(_ context.Context, pattern string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deletedPatterns = append(c.deletedPatterns, pattern)
	return nil
}

func newTransitionService(repo *transitionGuestRepo, cache *recordingCache) *GuestService {
	if cache == nil {
		cache = &recordingCache{}
	}
	return NewGuestService(repo, &mockAccessTokenRepo{}, cache, &mockTransactor{})
}

type statusCatalog struct{ pending, confirmed, declined uuid.UUID }

func newStatusCatalog() statusCatalog {
	return statusCatalog{uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())}
}

func TestBulkUpdateGuestStatus_StampsRSVPAtOnlyWhenTheRSVPStatusChanges(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newStatusCatalog()
	earlier := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
	pending := models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "pending", GuestStatusID: catalog.pending}
	alreadyDeclined := models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "declined", RSVPMethod: "web", RSVPAt: &earlier, GuestStatusID: catalog.declined}
	repo := newTransitionGuestRepo(pending, alreadyDeclined)
	svc := newTransitionService(repo, nil)
	before := time.Now()

	require.NoError(t, svc.BulkUpdateGuestStatus(eventID, []uuid.UUID{pending.ID, alreadyDeclined.ID}, catalog.declined, "declined", "host"))

	moved := repo.guest(pending.ID)
	assert.Equal(t, "declined", moved.RSVPStatus)
	assert.Equal(t, "host", moved.RSVPMethod)
	assert.Equal(t, catalog.declined, moved.GuestStatusID)
	assert.Equal(t, catalog.declined, moved.StatusID)
	require.NotNil(t, moved.RSVPAt)
	assert.False(t, moved.RSVPAt.Before(before))

	unchanged := repo.guest(alreadyDeclined.ID)
	require.NotNil(t, unchanged.RSVPAt)
	assert.True(t, unchanged.RSVPAt.Equal(earlier), "same RSVP status keeps rsvp_at, as PUT /guests/:id does")
	assert.Equal(t, "host", unchanged.RSVPMethod)
}

func TestBulkUpdateGuestStatus_AdjustsRSVPAnalyticsForEachGuestThatMoves(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newStatusCatalog()
	analyticsRepo := &mockEventAnalyticsRepo{current: &models.EventAnalytics{EventID: eventID, RSVPConfirmed: 5, RSVPDeclined: 1}}
	defer withAnalyticsService(analyticsRepo, nil)()
	guests := []models.Guest{
		{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "confirmed"},
		{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "confirmed"},
		{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "pending"},
		{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "declined"},
	}
	repo := newTransitionGuestRepo(guests...)
	svc := newTransitionService(repo, nil)

	ids := []uuid.UUID{guests[0].ID, guests[1].ID, guests[2].ID, guests[3].ID}
	require.NoError(t, svc.BulkUpdateGuestStatus(eventID, ids, catalog.declined, "declined", "host"))

	require.NotNil(t, analyticsRepo.current)
	assert.Equal(t, 3, analyticsRepo.current.RSVPConfirmed, "two confirmed guests left confirmed")
	assert.Equal(t, 4, analyticsRepo.current.RSVPDeclined, "three guests became declined; the declined one did not move")
}

func TestBulkUpdateGuestStatus_GuestOfAnotherEventWritesNothing(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newStatusCatalog()
	analyticsRepo := &mockEventAnalyticsRepo{current: &models.EventAnalytics{EventID: eventID}}
	defer withAnalyticsService(analyticsRepo, nil)()
	ours := models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "pending"}
	theirs := models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: uuid.Must(uuid.NewV4()), RSVPStatus: "pending"}
	repo := newTransitionGuestRepo(ours, theirs)
	cache := &recordingCache{}
	svc := newTransitionService(repo, cache)

	err := svc.BulkUpdateGuestStatus(eventID, []uuid.UUID{ours.ID, theirs.ID}, catalog.declined, "declined", "host")

	require.Error(t, err)
	assert.Equal(t, "pending", repo.guest(ours.ID).RSVPStatus)
	assert.Equal(t, "pending", repo.guest(theirs.ID).RSVPStatus)
	assert.Nil(t, analyticsRepo.updated)
	assert.Empty(t, cache.invalidated)
}

func TestBulkUpdateGuestStatus_InvalidatesEachGuestAndTheEventList(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	catalog := newStatusCatalog()
	guest := models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: eventID, RSVPStatus: "pending"}
	repo := newTransitionGuestRepo(guest)
	cache := &recordingCache{}
	svc := newTransitionService(repo, cache)

	require.NoError(t, svc.BulkUpdateGuestStatus(eventID, []uuid.UUID{guest.ID}, catalog.confirmed, "confirmed", "host"))

	assert.Contains(t, cache.invalidated, "guests:"+guest.ID.String())
	assert.Contains(t, cache.deletedPatterns, "all:"+eventID.String()+":guests")
}
