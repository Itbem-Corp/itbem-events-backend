package guests

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/models"
	guestsService "events-stocks/services/guests"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingBulkDeleteGuestRepo struct {
	mockGuestRepo
	deleted []uuid.UUID
}

func (m *recordingBulkDeleteGuestRepo) BulkDeleteGuests(ids []uuid.UUID) error {
	m.deleted = append([]uuid.UUID(nil), ids...)
	return nil
}

// countGuestAuthzLookups installs root authz hooks that count guest lookups.
func countGuestAuthzLookups(t *testing.T, eventID uuid.UUID) *atomic.Int64 {
	t.Helper()
	var lookups atomic.Int64
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) { return &models.Event{ID: id}, nil },
		GetGuestByID: func(id uuid.UUID) (*models.Guest, error) {
			lookups.Add(1)
			return &models.Guest{ID: id, EventID: eventID, FirstName: "Ana"}, nil
		},
	})
	t.Cleanup(restore)
	return &lookups
}

func guestIDsJSON(ids ...uuid.UUID) string {
	values := make([]string, len(ids))
	for i, id := range ids {
		values[i] = `"` + id.String() + `"`
	}
	return "[" + strings.Join(values, ",") + "]"
}

func newGuestIDs(n int) []uuid.UUID {
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = uuid.Must(uuid.NewV4())
	}
	return ids
}

func TestBulkDeleteGuests_RejectsMoreThan100IDsBeforeAuthz(t *testing.T) {
	repo := &recordingBulkDeleteGuestRepo{}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	t.Cleanup(func() { guestSvc = orig })

	c, rec := newEchoCtx(http.MethodDelete, "/guests/bulk", `{"ids":`+guestIDsJSON(newGuestIDs(101)...)+`}`)
	c.Set("cognito_sub", "test-sub")
	lookups := countGuestAuthzLookups(t, uuid.Must(uuid.NewV4()))

	require.NoError(t, BulkDeleteGuests(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Too many IDs")
	assert.Zero(t, lookups.Load())
	assert.Nil(t, repo.deleted)
}

func TestBulkDeleteGuests_DeduplicatesIDsBeforeAuthz(t *testing.T) {
	repo := &recordingBulkDeleteGuestRepo{}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	t.Cleanup(func() { guestSvc = orig })
	ids := newGuestIDs(2)

	c, rec := newEchoCtx(http.MethodDelete, "/guests/bulk", `{"ids":`+guestIDsJSON(ids[0], ids[1], ids[0], ids[1])+`}`)
	c.Set("cognito_sub", "test-sub")
	lookups := countGuestAuthzLookups(t, uuid.Must(uuid.NewV4()))

	require.NoError(t, BulkDeleteGuests(c))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, int64(2), lookups.Load())
	assert.Equal(t, ids, repo.deleted)
}

func TestBulkDeleteGuests_Accepts100IDs(t *testing.T) {
	repo := &recordingBulkDeleteGuestRepo{}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	t.Cleanup(func() { guestSvc = orig })

	c, rec := newEchoCtx(http.MethodDelete, "/guests/bulk", `{"ids":`+guestIDsJSON(newGuestIDs(100)...)+`}`)
	c.Set("cognito_sub", "test-sub")
	countGuestAuthzLookups(t, uuid.Must(uuid.NewV4()))

	require.NoError(t, BulkDeleteGuests(c))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, repo.deleted, 100)
}

func guestBatchJSON(eventID uuid.UUID, n int) string {
	items := make([]map[string]any, n)
	for i := range items {
		items[i] = map[string]any{"first_name": "Invitado", "event_id": eventID.String(), "guests_count": 1}
	}
	raw, _ := json.Marshal(items)
	return string(raw)
}

func TestCreateGuests_RejectsMoreThan1000GuestsBeforeAuthz(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &uniqueTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	t.Cleanup(func() { guestSvc = orig })
	eventID := uuid.Must(uuid.NewV4())
	var syncs atomic.Int64
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			syncs.Add(1)
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}, nil
		},
	})
	t.Cleanup(restore)

	c, rec := newEchoCtx(http.MethodPost, "/guests/batch", guestBatchJSON(eventID, 1001))
	c.Set("cognito_sub", "test-sub")

	require.NoError(t, CreateGuests(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Too many guests")
	assert.Zero(t, syncs.Load())
}

func TestCreateGuests_Accepts1000Guests(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &uniqueTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	t.Cleanup(func() { guestSvc = orig })
	eventID := uuid.Must(uuid.NewV4())

	c, rec := newEchoCtx(http.MethodPost, "/guests/batch", guestBatchJSON(eventID, 1000))
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, CreateGuests(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
}
