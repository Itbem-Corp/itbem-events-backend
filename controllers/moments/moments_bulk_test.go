package moments

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/models"
	momentsService "events-stocks/services/moments"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// bulkMomentFixture stores moments for the batch lookup and counts the authz
// lookups a bulk handler makes.
type bulkMomentFixture struct {
	moments       map[uuid.UUID]models.Moment
	batchLookups  atomic.Int64
	eventLookups  atomic.Int64
	momentLookups atomic.Int64
	approved      []uuid.UUID
	deleted       []uuid.UUID
	ordered       map[uuid.UUID]int
}

func newBulkMomentFixture(t *testing.T, moments ...models.Moment) *bulkMomentFixture {
	t.Helper()
	f := &bulkMomentFixture{moments: make(map[uuid.UUID]models.Moment, len(moments))}
	for _, moment := range moments {
		f.moments[moment.ID] = moment
	}
	repo := &mockMomentRepo{
		GetMomentsByIDsFunc: func(ids []uuid.UUID) ([]models.Moment, error) {
			f.batchLookups.Add(1)
			found := make([]models.Moment, 0, len(ids))
			for _, id := range ids {
				if moment, ok := f.moments[id]; ok {
					found = append(found, moment)
				}
			}
			return found, nil
		},
		BulkUpdateApprovalFunc: func(ids []uuid.UUID, _ bool) error {
			f.approved = append([]uuid.UUID(nil), ids...)
			return nil
		},
		BulkDeleteMomentsFunc: func(ids []uuid.UUID) error {
			f.deleted = append([]uuid.UUID(nil), ids...)
			return nil
		},
		BulkUpdateOrderFunc: func(updates map[uuid.UUID]int) error {
			f.ordered = updates
			return nil
		},
	}
	orig := momentSvc
	momentSvc = momentsService.NewMomentService(repo, &mockCacheRepo{})
	t.Cleanup(func() { momentSvc = orig })
	return f
}

// authorize installs authz hooks for a root user, or for a user whose client
// access is denied when root is false.
func (f *bulkMomentFixture) authorize(t *testing.T, c echo.Context, root bool) {
	t.Helper()
	c.Set("cognito_sub", "test-sub")
	clientID := uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: root}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			f.eventLookups.Add(1)
			return &models.Event{ID: id, ClientID: &clientID}, nil
		},
		GetMomentByID: func(id uuid.UUID) (*models.Moment, error) {
			f.momentLookups.Add(1)
			if moment, ok := f.moments[id]; ok {
				return &moment, nil
			}
			return nil, gorm.ErrRecordNotFound
		},
		CheckAccessRecursive: func(uuid.UUID, uuid.UUID) (bool, string) { return false, "" },
	})
	t.Cleanup(restore)
}

func momentsInEvents(perEvent int, events int) ([]models.Moment, []uuid.UUID) {
	moments := make([]models.Moment, 0, perEvent*events)
	ids := make([]uuid.UUID, 0, perEvent*events)
	for e := 0; e < events; e++ {
		eventID := uuid.Must(uuid.NewV4())
		for i := 0; i < perEvent; i++ {
			id := uuid.Must(uuid.NewV4())
			moments = append(moments, models.Moment{ID: id, EventID: &eventID})
			ids = append(ids, id)
		}
	}
	return moments, ids
}

func momentIDsBody(ids ...uuid.UUID) string {
	values := make([]string, len(ids))
	for i, id := range ids {
		values[i] = `"` + id.String() + `"`
	}
	return `{"ids":[` + strings.Join(values, ",") + `]}`
}

func reorderBody(t *testing.T, items ...map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(items)
	require.NoError(t, err)
	return string(raw)
}

func TestBulkApproveRejectMoments_AuthorizesOncePerEvent(t *testing.T) {
	moments, ids := momentsInEvents(3, 2)
	f := newBulkMomentFixture(t, moments...)
	c, rec := newEchoCtx(http.MethodPost, "/moments/bulk-approve", strings.TrimSuffix(momentIDsBody(ids...), "}")+`,"is_approved":true}`)
	f.authorize(t, c, true)

	require.NoError(t, BulkApproveRejectMoments(c))

	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, int64(1), f.batchLookups.Load())
	assert.Equal(t, int64(2), f.eventLookups.Load(), "one event check per distinct event")
	assert.Zero(t, f.momentLookups.Load())
	assert.Equal(t, ids, f.approved)
}

func TestBulkApproveRejectMoments_DeduplicatesIDs(t *testing.T) {
	moments, ids := momentsInEvents(2, 1)
	f := newBulkMomentFixture(t, moments...)
	c, rec := newEchoCtx(http.MethodPost, "/moments/bulk-approve", momentIDsBody(ids[0], ids[1], ids[0]))
	f.authorize(t, c, true)

	require.NoError(t, BulkApproveRejectMoments(c))

	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, ids, f.approved)
}

func TestBulkApproveRejectMoments_DeniedEventStopsTheBatch(t *testing.T) {
	moments, ids := momentsInEvents(2, 1)
	f := newBulkMomentFixture(t, moments...)
	c, rec := newEchoCtx(http.MethodPost, "/moments/bulk-approve", momentIDsBody(ids...))
	f.authorize(t, c, false)

	require.NoError(t, BulkApproveRejectMoments(c))

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Nil(t, f.approved)
}

func TestBulkApproveRejectMoments_MissingMomentReturns404(t *testing.T) {
	moments, ids := momentsInEvents(1, 1)
	f := newBulkMomentFixture(t, moments...)
	missing := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPost, "/moments/bulk-approve", momentIDsBody(ids[0], missing))
	f.authorize(t, c, true)

	require.NoError(t, BulkApproveRejectMoments(c))

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Nil(t, f.approved)
}

func TestBulkApproveRejectMoments_MomentWithoutEventUsesPerMomentCheck(t *testing.T) {
	unlinked := models.Moment{ID: uuid.Must(uuid.NewV4())}
	f := newBulkMomentFixture(t, unlinked)
	c, rec := newEchoCtx(http.MethodPost, "/moments/bulk-approve", momentIDsBody(unlinked.ID))
	f.authorize(t, c, false)

	require.NoError(t, BulkApproveRejectMoments(c))

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, int64(1), f.momentLookups.Load())
	assert.Nil(t, f.approved)
}

func TestBulkApproveRejectMoments_RejectsMoreThan5000IDs(t *testing.T) {
	_, ids := momentsInEvents(5001, 1)
	f := newBulkMomentFixture(t)
	c, rec := newEchoCtx(http.MethodPost, "/moments/bulk-approve", momentIDsBody(ids...))
	f.authorize(t, c, true)

	require.NoError(t, BulkApproveRejectMoments(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Too many IDs")
	assert.Zero(t, f.batchLookups.Load())
	assert.Zero(t, f.eventLookups.Load())
}

func TestBulkApproveRejectMoments_Accepts5000IDs(t *testing.T) {
	moments, ids := momentsInEvents(5000, 1)
	f := newBulkMomentFixture(t, moments...)
	c, rec := newEchoCtx(http.MethodPost, "/moments/bulk-approve", momentIDsBody(ids...))
	f.authorize(t, c, true)

	require.NoError(t, BulkApproveRejectMoments(c))

	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, int64(1), f.eventLookups.Load())
	assert.Len(t, f.approved, 5000)
}

func TestBulkDeleteMoments_AuthorizesOncePerEventAndDeduplicates(t *testing.T) {
	moments, ids := momentsInEvents(2, 2)
	f := newBulkMomentFixture(t, moments...)
	c, rec := newEchoCtx(http.MethodDelete, "/moments/bulk", momentIDsBody(append(ids, ids[0])...))
	f.authorize(t, c, true)

	require.NoError(t, BulkDeleteMoments(c))

	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, int64(2), f.eventLookups.Load())
	assert.Zero(t, f.momentLookups.Load())
	assert.Equal(t, ids, f.deleted)
}

func TestBulkDeleteMoments_RejectsMoreThan5000IDs(t *testing.T) {
	_, ids := momentsInEvents(5001, 1)
	f := newBulkMomentFixture(t)
	c, rec := newEchoCtx(http.MethodDelete, "/moments/bulk", momentIDsBody(ids...))
	f.authorize(t, c, true)

	require.NoError(t, BulkDeleteMoments(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Zero(t, f.batchLookups.Load())
	assert.Nil(t, f.deleted)
}

func TestBulkDeleteMoments_DeniedEventStopsTheBatch(t *testing.T) {
	moments, ids := momentsInEvents(2, 1)
	f := newBulkMomentFixture(t, moments...)
	c, rec := newEchoCtx(http.MethodDelete, "/moments/bulk", momentIDsBody(ids...))
	f.authorize(t, c, false)

	require.NoError(t, BulkDeleteMoments(c))

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Nil(t, f.deleted)
}

func TestReorderMoments_AuthorizesOncePerEvent(t *testing.T) {
	moments, ids := momentsInEvents(3, 1)
	f := newBulkMomentFixture(t, moments...)
	body := reorderBody(t,
		map[string]any{"id": ids[0].String(), "order": 2},
		map[string]any{"id": ids[1].String(), "order": 0},
		map[string]any{"id": ids[2].String(), "order": 1},
	)
	c, rec := newEchoCtx(http.MethodPatch, "/moments/reorder", body)
	f.authorize(t, c, true)

	require.NoError(t, ReorderMoments(c))

	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, int64(1), f.eventLookups.Load())
	assert.Zero(t, f.momentLookups.Load())
	assert.Equal(t, map[uuid.UUID]int{ids[0]: 2, ids[1]: 0, ids[2]: 1}, f.ordered)
}

func TestReorderMoments_SameIDSameOrderCollapses(t *testing.T) {
	moments, ids := momentsInEvents(1, 1)
	f := newBulkMomentFixture(t, moments...)
	body := reorderBody(t,
		map[string]any{"id": ids[0].String(), "order": 4},
		map[string]any{"id": ids[0].String(), "order": 4},
	)
	c, rec := newEchoCtx(http.MethodPatch, "/moments/reorder", body)
	f.authorize(t, c, true)

	require.NoError(t, ReorderMoments(c))

	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, map[uuid.UUID]int{ids[0]: 4}, f.ordered)
}

func TestReorderMoments_ConflictingOrderForSameIDReturns400(t *testing.T) {
	moments, ids := momentsInEvents(1, 1)
	f := newBulkMomentFixture(t, moments...)
	body := reorderBody(t,
		map[string]any{"id": ids[0].String(), "order": 1},
		map[string]any{"id": ids[0].String(), "order": 2},
	)
	c, rec := newEchoCtx(http.MethodPatch, "/moments/reorder", body)
	f.authorize(t, c, true)

	require.NoError(t, ReorderMoments(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Zero(t, f.batchLookups.Load())
	assert.Nil(t, f.ordered)
}

func TestReorderMoments_RejectsMoreThan5000Items(t *testing.T) {
	_, ids := momentsInEvents(5001, 1)
	f := newBulkMomentFixture(t)
	items := make([]map[string]any, len(ids))
	for i, id := range ids {
		items[i] = map[string]any{"id": id.String(), "order": i}
	}
	c, rec := newEchoCtx(http.MethodPatch, "/moments/reorder", reorderBody(t, items...))
	f.authorize(t, c, true)

	require.NoError(t, ReorderMoments(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Too many IDs")
	assert.Zero(t, f.batchLookups.Load())
	assert.Nil(t, f.ordered)
}

func TestReorderMoments_DeniedEventStopsTheBatch(t *testing.T) {
	moments, ids := momentsInEvents(1, 1)
	f := newBulkMomentFixture(t, moments...)
	c, rec := newEchoCtx(http.MethodPatch, "/moments/reorder", reorderBody(t, map[string]any{"id": ids[0].String(), "order": 1}))
	f.authorize(t, c, false)

	require.NoError(t, ReorderMoments(c))

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Nil(t, f.ordered)
}
