package moments

import (
	"context"
	"errors"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Mock: CacheRepository
// ---------------------------------------------------------------------------

type mockCacheRepo struct {
	GetKeyFunc              func(ctx context.Context, key string) (string, error)
	SaveKeyFunc             func(ctx context.Context, key string, value string, ttl time.Duration) error
	InvalidateFunc          func(resource string, key string) error
	DeleteKeysByPatternFunc func(ctx context.Context, pattern string) error
}

func (m *mockCacheRepo) GetKey(ctx context.Context, key string) (string, error) {
	if m.GetKeyFunc != nil {
		return m.GetKeyFunc(ctx, key)
	}
	return "", errors.New("cache miss")
}
func (m *mockCacheRepo) SaveKey(ctx context.Context, key string, value string, ttl time.Duration) error {
	if m.SaveKeyFunc != nil {
		return m.SaveKeyFunc(ctx, key, value, ttl)
	}
	return nil
}
func (m *mockCacheRepo) Invalidate(resource string, key string) error {
	if m.InvalidateFunc != nil {
		return m.InvalidateFunc(resource, key)
	}
	return nil
}
func (m *mockCacheRepo) DeleteKeysByPattern(ctx context.Context, pattern string) error {
	if m.DeleteKeysByPatternFunc != nil {
		return m.DeleteKeysByPatternFunc(ctx, pattern)
	}
	return nil
}

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

// ---------------------------------------------------------------------------
// Mock: MomentRepository
// ---------------------------------------------------------------------------

type mockMomentRepo struct {
	CreateMomentFunc            func(m *models.Moment) error
	UpdateMomentFunc            func(m *models.Moment) error
	DeleteMomentFunc            func(id uuid.UUID) error
	BulkDeleteFunc              func(ids []uuid.UUID) error
	GetMomentByIDFunc           func(id uuid.UUID) (*models.Moment, error)
	GetByEventAndContentURLFunc func(eventID uuid.UUID, contentURL string) (*models.Moment, error)
	GetMomentsByIDsFunc         func(ids []uuid.UUID) ([]models.Moment, error)
	ListMomentsFunc             func() ([]models.Moment, error)
	ListSummaryFunc             func(eventIDs []uuid.UUID) ([]dtos.MomentSummary, error)
	ListWallFunc                func(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error)
	DistinctEventsFunc          func(ids []uuid.UUID) ([]uuid.UUID, error)
	UpdateContentFunc           func(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error
	BulkApproveFunc             func(ids []uuid.UUID, isApproved bool) error
	BulkUpdateOrderFunc         func(updates map[uuid.UUID]int) error
}

func (m *mockMomentRepo) CreateMoment(obj *models.Moment) error {
	if m.CreateMomentFunc != nil {
		return m.CreateMomentFunc(obj)
	}
	return nil
}
func (m *mockMomentRepo) UpdateMoment(obj *models.Moment) error {
	if m.UpdateMomentFunc != nil {
		return m.UpdateMomentFunc(obj)
	}
	return nil
}
func (m *mockMomentRepo) DeleteMoment(id uuid.UUID) error {
	if m.DeleteMomentFunc != nil {
		return m.DeleteMomentFunc(id)
	}
	return nil
}
func (m *mockMomentRepo) BulkDeleteMoments(ids []uuid.UUID) error {
	if m.BulkDeleteFunc != nil {
		return m.BulkDeleteFunc(ids)
	}
	return nil
}
func (m *mockMomentRepo) GetMomentByID(id uuid.UUID) (*models.Moment, error) {
	if m.GetMomentByIDFunc != nil {
		return m.GetMomentByIDFunc(id)
	}
	return &models.Moment{}, nil
}
func (m *mockMomentRepo) GetMomentByEventIDAndContentURL(eventID uuid.UUID, contentURL string) (*models.Moment, error) {
	if m.GetByEventAndContentURLFunc != nil {
		return m.GetByEventAndContentURLFunc(eventID, contentURL)
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockMomentRepo) ListMoments() ([]models.Moment, error) {
	if m.ListMomentsFunc != nil {
		return m.ListMomentsFunc()
	}
	return nil, nil
}

func (m *mockMomentRepo) ListByEventID(eventID uuid.UUID, approvedOnly bool) ([]models.Moment, error) {
	return nil, nil
}
func (m *mockMomentRepo) UpdateMomentContent(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
	if m.UpdateContentFunc != nil {
		return m.UpdateContentFunc(id, contentURL, processingStatus, thumbnailURL, errorMessage, durationMs, originalBytes, optimizedBytes)
	}
	return nil
}
func (m *mockMomentRepo) ListForDashboard(eventID uuid.UUID) ([]models.Moment, error) {
	return nil, nil
}

func (m *mockMomentRepo) ListForDashboardPage(eventID uuid.UUID, page, pageSize int) ([]models.Moment, dtos.MomentDashboardCounts, error) {
	items, err := m.ListForDashboard(eventID)
	return items, dtos.MomentDashboardCounts{Total: int64(len(items))}, err
}
func (m *mockMomentRepo) ListPendingSummaryByEventIDs(eventIDs []uuid.UUID) ([]dtos.MomentSummary, error) {
	if m.ListSummaryFunc != nil {
		return m.ListSummaryFunc(eventIDs)
	}
	return nil, nil
}
func (m *mockMomentRepo) ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
	if m.ListWallFunc != nil {
		return m.ListWallFunc(eventID, page, limit)
	}
	return nil, 0, nil
}
func (m *mockMomentRepo) BulkUpdateApproval(ids []uuid.UUID, isApproved bool) error {
	if m.BulkApproveFunc != nil {
		return m.BulkApproveFunc(ids, isApproved)
	}
	return nil
}
func (m *mockMomentRepo) GetDistinctEventIDsByMomentIDs(ids []uuid.UUID) ([]uuid.UUID, error) {
	if m.DistinctEventsFunc != nil {
		return m.DistinctEventsFunc(ids)
	}
	return nil, nil
}
func (m *mockMomentRepo) GetMomentsByIDs(ids []uuid.UUID) ([]models.Moment, error) {
	if m.GetMomentsByIDsFunc != nil {
		return m.GetMomentsByIDsFunc(ids)
	}
	return nil, nil
}
func (m *mockMomentRepo) BulkUpdateOrder(updates map[uuid.UUID]int) error {
	if m.BulkUpdateOrderFunc != nil {
		return m.BulkUpdateOrderFunc(updates)
	}
	return nil
}
func (m *mockMomentRepo) ListProcessingByEventID(eventID uuid.UUID, rawOnly bool) ([]models.Moment, error) {
	return nil, nil
}
func (m *mockMomentRepo) ListApprovedForWallCursor(eventID uuid.UUID, afterCreatedAt *time.Time, afterID string, afterOrder *int, limit int) ([]models.Moment, int64, error) {
	return nil, 0, nil
}

var _ ports.MomentRepository = (*mockMomentRepo)(nil)

type mockMediaJobPublisher struct {
	PublishFunc func(msg dtos.MediaProcessMessage) (bool, error)
}

func (m *mockMediaJobPublisher) PublishMediaJob(msg dtos.MediaProcessMessage) (bool, error) {
	if m.PublishFunc != nil {
		return m.PublishFunc(msg)
	}
	return true, nil
}

var _ ports.MediaJobPublisher = (*mockMediaJobPublisher)(nil)

// ---------------------------------------------------------------------------
// MomentService — CRUD + cache invalidation tests
// ---------------------------------------------------------------------------

func TestMomentService_CreateMoment_Success(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	invalidateCalled := false
	var invalidatedPatterns []string
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			invalidatedPatterns = append(invalidatedPatterns, pattern)
			return nil
		},
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			assert.Equal(t, "moments", resource)
			assert.Equal(t, "all", key)
			return nil
		},
	}
	repoCalled := false
	repo := &mockMomentRepo{
		CreateMomentFunc: func(obj *models.Moment) error {
			repoCalled = true
			return nil
		},
	}

	svc := NewMomentService(repo, cache)
	err := svc.CreateMoment(&models.Moment{EventID: &eventID})

	require.NoError(t, err)
	assert.True(t, repoCalled)
	assert.Equal(t, []string{"moments:wall:" + eventID.String() + ":*"}, invalidatedPatterns)
	assert.True(t, invalidateCalled)
}

func TestMomentService_CreateMoment_IgnoresCacheInvalidationError(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	repoCalled := false
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			assert.Equal(t, "moments:wall:"+eventID.String()+":*", pattern)
			return errors.New("redis unavailable")
		},
		InvalidateFunc: func(resource, key string) error {
			assert.Equal(t, "moments", resource)
			assert.Equal(t, "all", key)
			return errors.New("redis unavailable")
		},
	}
	repo := &mockMomentRepo{
		CreateMomentFunc: func(obj *models.Moment) error {
			repoCalled = true
			return nil
		},
	}

	svc := NewMomentService(repo, cache)
	err := svc.CreateMoment(&models.Moment{EventID: &eventID})

	require.NoError(t, err)
	assert.True(t, repoCalled)
}

func TestMomentService_CreateMoment_RepoError_NoCacheInvalidation(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repo := &mockMomentRepo{
		CreateMomentFunc: func(obj *models.Moment) error {
			return errors.New("not null constraint failed")
		},
	}

	svc := NewMomentService(repo, cache)
	err := svc.CreateMoment(&models.Moment{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not null constraint failed")
	assert.False(t, invalidateCalled, "cache must NOT be invalidated when repo fails")
}

func TestMomentService_UpdateMoment_Success(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repo := &mockMomentRepo{
		UpdateMomentFunc: func(obj *models.Moment) error { return nil },
	}

	svc := NewMomentService(repo, cache)
	err := svc.UpdateMoment(&models.Moment{})

	require.NoError(t, err)
	assert.True(t, invalidateCalled)
}

func TestMomentService_UpdateMoment_RepoError(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repo := &mockMomentRepo{
		UpdateMomentFunc: func(obj *models.Moment) error {
			return errors.New("record not found")
		},
	}

	svc := NewMomentService(repo, cache)
	err := svc.UpdateMoment(&models.Moment{})

	require.Error(t, err)
	assert.False(t, invalidateCalled)
}

func TestMomentService_DeleteMoment_Success(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	var deletedID uuid.UUID
	invalidateCalled := false

	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repo := &mockMomentRepo{
		DeleteMomentFunc: func(i uuid.UUID) error {
			deletedID = i
			return nil
		},
	}

	svc := NewMomentService(repo, cache)
	err := svc.DeleteMoment(id)

	require.NoError(t, err)
	assert.Equal(t, id, deletedID)
	assert.True(t, invalidateCalled)
}

func TestMomentService_DeleteMoment_RepoError(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repo := &mockMomentRepo{
		DeleteMomentFunc: func(id uuid.UUID) error {
			return errors.New("foreign key constraint")
		},
	}

	svc := NewMomentService(repo, cache)
	err := svc.DeleteMoment(uuid.Must(uuid.NewV4()))

	require.Error(t, err)
	assert.False(t, invalidateCalled)
}

func TestMomentService_BulkDeleteMoments_SuccessInvalidatesEventWallCaches(t *testing.T) {
	id1 := uuid.Must(uuid.NewV4())
	id2 := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	var deletedIDs []uuid.UUID
	var invalidatedPatterns []string
	cacheInvalidated := false

	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			invalidatedPatterns = append(invalidatedPatterns, pattern)
			return nil
		},
		InvalidateFunc: func(resource, key string) error {
			cacheInvalidated = true
			assert.Equal(t, "moments", resource)
			assert.Equal(t, "all", key)
			return nil
		},
	}
	repo := &mockMomentRepo{
		BulkDeleteFunc: func(ids []uuid.UUID) error {
			deletedIDs = ids
			return nil
		},
		DistinctEventsFunc: func(ids []uuid.UUID) ([]uuid.UUID, error) {
			assert.Equal(t, []uuid.UUID{id1, id2}, ids)
			return []uuid.UUID{eventID}, nil
		},
	}

	svc := NewMomentService(repo, cache)
	err := svc.BulkDeleteMoments([]uuid.UUID{id1, id2})

	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{id1, id2}, deletedIDs)
	assert.Equal(t, []string{"moments:wall:" + eventID.String() + ":*"}, invalidatedPatterns)
	assert.True(t, cacheInvalidated)
}

func TestMomentService_GetMomentByID_Success(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	repo := &mockMomentRepo{
		GetMomentByIDFunc: func(i uuid.UUID) (*models.Moment, error) {
			return &models.Moment{ID: i}, nil
		},
	}

	svc := NewMomentService(repo, &mockCacheRepo{})
	result, err := svc.GetMomentByID(id)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, id, result.ID)
}

func TestMomentService_GetMomentByID_NotFound(t *testing.T) {
	repo := &mockMomentRepo{
		GetMomentByIDFunc: func(id uuid.UUID) (*models.Moment, error) {
			return nil, errors.New("record not found")
		},
	}

	svc := NewMomentService(repo, &mockCacheRepo{})
	result, err := svc.GetMomentByID(uuid.Must(uuid.NewV4()))

	require.Error(t, err)
	assert.Nil(t, result)
}

func TestMomentService_UpdateMomentContent_StoresErrorMessageAndInvalidatesEventWallCache(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	var invalidatedPatterns []string
	cacheInvalidated := false
	var capturedError string

	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			invalidatedPatterns = append(invalidatedPatterns, pattern)
			return nil
		},
		InvalidateFunc: func(resource, key string) error {
			cacheInvalidated = true
			assert.Equal(t, "moments", resource)
			assert.Equal(t, "all", key)
			return nil
		},
	}
	repo := &mockMomentRepo{
		UpdateContentFunc: func(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
			assert.Equal(t, momentID, id)
			assert.Equal(t, "moments/raw/video.mp4", contentURL)
			assert.Equal(t, "failed", processingStatus)
			capturedError = errorMessage
			return nil
		},
		GetMomentByIDFunc: func(id uuid.UUID) (*models.Moment, error) {
			return &models.Moment{ID: id, EventID: &eventID}, nil
		},
	}

	svc := NewMomentService(repo, cache)
	err := svc.UpdateMomentContent(momentID, "moments/raw/video.mp4", "failed", "", "ffmpeg exited with code 1", 0, 0, 0)

	require.NoError(t, err)
	assert.Equal(t, "ffmpeg exited with code 1", capturedError)
	assert.Equal(t, []string{"moments:wall:" + eventID.String() + ":*"}, invalidatedPatterns)
	assert.True(t, cacheInvalidated)
}

// ---------------------------------------------------------------------------
// MomentService — ListMoments cache behavior
// ---------------------------------------------------------------------------

func TestMomentService_UpdateMomentContent_IgnoresCacheInvalidationError(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	var updated bool
	repo := &mockMomentRepo{
		UpdateContentFunc: func(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
			updated = true
			return nil
		},
		GetMomentByIDFunc: func(id uuid.UUID) (*models.Moment, error) {
			return &models.Moment{ID: id, EventID: &eventID}, nil
		},
	}
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			assert.Equal(t, "moments:wall:"+eventID.String()+":*", pattern)
			return errors.New("redis unavailable")
		},
		InvalidateFunc: func(resource, key string) error {
			assert.Equal(t, "moments", resource)
			assert.Equal(t, "all", key)
			return errors.New("redis unavailable")
		},
	}

	svc := NewMomentService(repo, cache)
	err := svc.UpdateMomentContent(momentID, "moments/raw/photo.jpg", "done", "", "", 0, 0, 0)

	require.NoError(t, err)
	assert.True(t, updated)
}

func TestMomentService_UpdateMomentContentNormalizesProcessingStatus(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	var capturedStatus string
	repo := &mockMomentRepo{
		UpdateContentFunc: func(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
			capturedStatus = processingStatus
			return nil
		},
	}

	svc := NewMomentService(repo, nil)
	err := svc.UpdateMomentContent(momentID, "moments/raw/photo.jpg", " DONE ", "", "", 0, 0, 0)

	require.NoError(t, err)
	assert.Equal(t, "done", capturedStatus)
}

func TestMomentService_UpdateMomentContentRejectsInvalidProcessingStatus(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	var updated bool
	repo := &mockMomentRepo{
		UpdateContentFunc: func(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
			updated = true
			return nil
		},
	}

	svc := NewMomentService(repo, nil)
	err := svc.UpdateMomentContent(momentID, "moments/raw/photo.jpg", "ready", "", "", 0, 0, 0)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMomentProcessingStatus)
	assert.False(t, updated)
}

func TestMomentService_ListMoments_CacheHit(t *testing.T) {
	repoCallCount := 0
	repo := &mockMomentRepo{
		ListMomentsFunc: func() ([]models.Moment, error) {
			repoCallCount++
			return nil, nil
		},
	}
	cache := &mockCacheRepo{
		GetKeyFunc: func(ctx context.Context, key string) (string, error) {
			return `[]`, nil // cache hit with empty JSON array
		},
	}

	svc := NewMomentService(repo, cache)
	result, err := svc.ListMoments()

	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Equal(t, 0, repoCallCount, "repo must NOT be called when cache hits")
}

func TestMomentService_ListMoments_CacheMiss_CallsRepo(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	repoCalled := false
	saveKeyCalled := false

	repo := &mockMomentRepo{
		ListMomentsFunc: func() ([]models.Moment, error) {
			repoCalled = true
			return []models.Moment{{ID: momentID}}, nil
		},
	}
	cache := &mockCacheRepo{
		GetKeyFunc: func(ctx context.Context, key string) (string, error) {
			return "", errors.New("cache miss")
		},
		SaveKeyFunc: func(ctx context.Context, key string, value string, ttl time.Duration) error {
			saveKeyCalled = true
			return nil
		},
	}

	svc := NewMomentService(repo, cache)
	result, err := svc.ListMoments()

	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, momentID, result[0].ID)
	assert.True(t, repoCalled, "repo must be called on cache miss")
	assert.True(t, saveKeyCalled, "result must be stored in cache after repo call")
}

func TestMomentService_ListMoments_AllowsNilCache(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	repoCalled := false
	repo := &mockMomentRepo{
		ListMomentsFunc: func() ([]models.Moment, error) {
			repoCalled = true
			return []models.Moment{{ID: momentID}}, nil
		},
	}

	svc := NewMomentService(repo, nil)
	result, err := svc.ListMoments()

	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, momentID, result[0].ID)
	assert.True(t, repoCalled)
}

func TestMomentService_ListMoments_CacheMiss_RepoError(t *testing.T) {
	repo := &mockMomentRepo{
		ListMomentsFunc: func() ([]models.Moment, error) {
			return nil, errors.New("connection timeout")
		},
	}
	cache := &mockCacheRepo{
		GetKeyFunc: func(ctx context.Context, key string) (string, error) {
			return "", errors.New("cache miss")
		},
	}

	svc := NewMomentService(repo, cache)
	result, err := svc.ListMoments()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection timeout")
	assert.Nil(t, result)
}

func TestMomentService_ListApprovedForWall_AllowsNilCache(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	momentID := uuid.Must(uuid.NewV4())
	var called bool
	repo := &mockMomentRepo{
		ListWallFunc: func(id uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
			called = true
			assert.Equal(t, eventID, id)
			assert.Equal(t, 2, page)
			assert.Equal(t, 25, limit)
			return []models.Moment{{ID: momentID, EventID: &eventID}}, 1, nil
		},
	}

	svc := NewMomentService(repo, nil)
	items, total, err := svc.ListApprovedForWall(eventID, 2, 25)

	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, momentID, items[0].ID)
	assert.True(t, called)
}

func TestMomentService_ListPendingSummaryByEventIDs_DelegatesToRepo(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	repoCalled := false
	repo := &mockMomentRepo{
		ListSummaryFunc: func(eventIDs []uuid.UUID) ([]dtos.MomentSummary, error) {
			repoCalled = true
			assert.Equal(t, []uuid.UUID{eventID}, eventIDs)
			return []dtos.MomentSummary{{EventID: eventID, PendingCount: 3}}, nil
		},
	}

	svc := NewMomentService(repo, &mockCacheRepo{})
	result, err := svc.ListPendingSummaryByEventIDs([]uuid.UUID{eventID})

	require.NoError(t, err)
	require.True(t, repoCalled)
	require.Len(t, result, 1)
	assert.Equal(t, int64(3), result[0].PendingCount)
}

func TestMomentService_BatchReoptimize_AllowsDashboardEligibleNonRawMoments(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	tests := []struct {
		name               string
		processingStatus   string
		optimizedSizeBytes int64
	}{
		{
			name:               "done moment without metrics",
			processingStatus:   "done",
			optimizedSizeBytes: 0,
		},
		{
			name:               "failed optimized moment",
			processingStatus:   "failed",
			optimizedSizeBytes: 2048,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			momentID := uuid.Must(uuid.NewV4())
			contentURL := "moments/" + eventID.String() + "/optimized/photo.webp"
			var updatedID uuid.UUID
			var published dtos.MediaProcessMessage

			repo := &mockMomentRepo{
				GetMomentsByIDsFunc: func(ids []uuid.UUID) ([]models.Moment, error) {
					require.Equal(t, []uuid.UUID{momentID}, ids)
					return []models.Moment{{
						ID:                 momentID,
						EventID:            &eventID,
						ContentURL:         contentURL,
						ContentType:        "image/webp",
						ProcessingStatus:   tt.processingStatus,
						OptimizedSizeBytes: tt.optimizedSizeBytes,
					}}, nil
				},
				UpdateContentFunc: func(id uuid.UUID, contentURLArg, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
					updatedID = id
					assert.Equal(t, contentURL, contentURLArg)
					assert.Equal(t, "pending", processingStatus)
					assert.Empty(t, thumbnailURL)
					assert.Empty(t, errorMessage)
					assert.Zero(t, durationMs)
					assert.Zero(t, originalBytes)
					assert.Zero(t, optimizedBytes)
					return nil
				},
			}
			publisher := &mockMediaJobPublisher{
				PublishFunc: func(msg dtos.MediaProcessMessage) (bool, error) {
					published = msg
					return true, nil
				},
			}
			cacheInvalidated := false
			var invalidatedPatterns []string
			cache := &mockCacheRepo{
				DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
					invalidatedPatterns = append(invalidatedPatterns, pattern)
					return nil
				},
				InvalidateFunc: func(resource, key string) error {
					cacheInvalidated = true
					assert.Equal(t, "moments", resource)
					assert.Equal(t, "all", key)
					return nil
				},
			}

			svc := NewMomentService(repo, cache, publisher)
			succeeded, skipped, failed, err := svc.BatchReoptimize([]uuid.UUID{momentID})

			require.NoError(t, err)
			assert.Equal(t, 1, succeeded)
			assert.Equal(t, 0, skipped)
			assert.Equal(t, 0, failed)
			assert.Equal(t, momentID, updatedID)
			assert.Equal(t, momentID.String(), published.MomentID)
			assert.Equal(t, eventID.String(), published.EventID)
			assert.Equal(t, contentURL, published.ObjectKey)
			assert.Equal(t, "image/webp", published.ContentType)
			assert.Equal(t, []string{"moments:wall:" + eventID.String() + ":*"}, invalidatedPatterns)
			assert.True(t, cacheInvalidated)
		})
	}
}

func TestMomentService_RequeueMoment_AllowsNilCache(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	momentID := uuid.Must(uuid.NewV4())
	var updated bool
	var published dtos.MediaProcessMessage
	repo := &mockMomentRepo{
		UpdateContentFunc: func(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
			updated = true
			assert.Equal(t, momentID, id)
			assert.Equal(t, "pending", processingStatus)
			return nil
		},
	}
	publisher := &mockMediaJobPublisher{
		PublishFunc: func(msg dtos.MediaProcessMessage) (bool, error) {
			published = msg
			return true, nil
		},
	}

	svc := NewMomentService(repo, nil, publisher)
	moment := &models.Moment{
		ID:               momentID,
		EventID:          &eventID,
		ContentURL:       "moments/" + eventID.String() + "/raw/photo.jpg",
		ProcessingStatus: "failed",
		ErrorMessage:     "optimizer timeout",
	}
	err := svc.RequeueMoment(moment)

	require.NoError(t, err)
	assert.True(t, updated)
	assert.Equal(t, "pending", moment.ProcessingStatus)
	assert.Empty(t, moment.ErrorMessage)
	assert.Equal(t, momentID.String(), published.MomentID)
	assert.Equal(t, eventID.String(), published.EventID)
}

func TestMomentService_RequeueMomentNormalizesLegacyCDNURLBeforePublishing(t *testing.T) {
	t.Setenv("AWS_BUCKET_NAME", "event-media")
	t.Setenv("CDN_BASE_URL", "https://cdn.eventiapp.com.mx")
	eventID := uuid.Must(uuid.NewV4())
	momentID := uuid.Must(uuid.NewV4())
	legacyURL := "https://cdn.eventiapp.com.mx/moments/" + eventID.String() + "/raw/photo.jpg"
	wantKey := "moments/" + eventID.String() + "/raw/photo.jpg"
	var published dtos.MediaProcessMessage
	repo := &mockMomentRepo{UpdateContentFunc: func(uuid.UUID, string, string, string, string, int64, int64, int64) error {
		return nil
	}}
	publisher := &mockMediaJobPublisher{PublishFunc: func(msg dtos.MediaProcessMessage) (bool, error) {
		published = msg
		return true, nil
	}}
	moment := &models.Moment{ID: momentID, EventID: &eventID, ContentURL: legacyURL, ProcessingStatus: "failed"}

	require.NoError(t, NewMomentService(repo, nil, publisher).RequeueMoment(moment))
	assert.Equal(t, wantKey, published.ObjectKey)
	assert.Equal(t, wantKey, published.RawS3Key)
	assert.Equal(t, "event-media", published.Bucket)
}

func TestMomentService_BatchReoptimizeNormalizesLegacyS3URLBeforePublishing(t *testing.T) {
	t.Setenv("AWS_BUCKET_NAME", "event-media")
	eventID := uuid.Must(uuid.NewV4())
	momentID := uuid.Must(uuid.NewV4())
	wantKey := "moments/" + eventID.String() + "/photos/" + momentID.String() + ".webp"
	legacyURL := "https://event-media.s3.us-east-2.amazonaws.com/" + wantKey
	var published dtos.MediaProcessMessage
	repo := &mockMomentRepo{
		GetMomentsByIDsFunc: func([]uuid.UUID) ([]models.Moment, error) {
			return []models.Moment{{ID: momentID, EventID: &eventID, ContentURL: legacyURL, ContentType: "image/webp", ProcessingStatus: "done"}}, nil
		},
		UpdateContentFunc: func(uuid.UUID, string, string, string, string, int64, int64, int64) error { return nil },
	}
	publisher := &mockMediaJobPublisher{PublishFunc: func(msg dtos.MediaProcessMessage) (bool, error) {
		published = msg
		return true, nil
	}}

	succeeded, skipped, failed, err := NewMomentService(repo, nil, publisher).BatchReoptimize([]uuid.UUID{momentID})
	require.NoError(t, err)
	assert.Equal(t, 1, succeeded)
	assert.Zero(t, skipped)
	assert.Zero(t, failed)
	assert.Equal(t, wantKey, published.ObjectKey)
	assert.Equal(t, wantKey, published.RawS3Key)
}

func TestMomentService_RequeueMomentWithoutPublisherLeavesTerminalState(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	moment := &models.Moment{
		ID:               uuid.Must(uuid.NewV4()),
		EventID:          &eventID,
		ContentURL:       "moments/" + eventID.String() + "/raw/photo.jpg",
		ProcessingStatus: "failed",
		ErrorMessage:     "optimizer timeout",
	}
	repo := &mockMomentRepo{UpdateContentFunc: func(uuid.UUID, string, string, string, string, int64, int64, int64) error {
		t.Fatal("a missing publisher must not move a terminal item back to pending")
		return nil
	}}

	err := NewMomentService(repo, nil).RequeueMoment(moment)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "media publisher is not configured")
	assert.Equal(t, "failed", moment.ProcessingStatus)
	assert.Equal(t, "optimizer timeout", moment.ErrorMessage)
}

func TestMomentService_BulkUpdateApproval_IgnoresCacheInvalidationError(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	momentID := uuid.Must(uuid.NewV4())
	var approved bool
	repo := &mockMomentRepo{
		DistinctEventsFunc: func(ids []uuid.UUID) ([]uuid.UUID, error) {
			assert.Equal(t, []uuid.UUID{momentID}, ids)
			return []uuid.UUID{eventID}, nil
		},
		BulkApproveFunc: func(ids []uuid.UUID, isApproved bool) error {
			approved = true
			assert.True(t, isApproved)
			return nil
		},
	}
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			assert.Equal(t, "moments:wall:"+eventID.String()+":*", pattern)
			return errors.New("redis unavailable")
		},
		InvalidateFunc: func(resource, key string) error {
			assert.Equal(t, "moments", resource)
			assert.Equal(t, "all", key)
			return errors.New("redis unavailable")
		},
	}

	svc := NewMomentService(repo, cache)
	err := svc.BulkUpdateApproval([]uuid.UUID{momentID}, true)

	require.NoError(t, err)
	assert.True(t, approved)
}

func TestMomentService_BulkUpdateOrder_IgnoresCacheInvalidationError(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	momentID := uuid.Must(uuid.NewV4())
	updates := map[uuid.UUID]int{momentID: 2}
	var reordered bool
	repo := &mockMomentRepo{
		DistinctEventsFunc: func(ids []uuid.UUID) ([]uuid.UUID, error) {
			assert.ElementsMatch(t, []uuid.UUID{momentID}, ids)
			return []uuid.UUID{eventID}, nil
		},
		BulkUpdateOrderFunc: func(got map[uuid.UUID]int) error {
			reordered = true
			assert.Equal(t, updates, got)
			return nil
		},
	}
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			assert.Equal(t, "moments:wall:"+eventID.String()+":*", pattern)
			return errors.New("redis unavailable")
		},
		InvalidateFunc: func(resource, key string) error {
			assert.Equal(t, "moments", resource)
			assert.Equal(t, "all", key)
			return errors.New("redis unavailable")
		},
	}

	svc := NewMomentService(repo, cache)
	err := svc.BulkUpdateOrder(updates)

	require.NoError(t, err)
	assert.True(t, reordered)
}

func TestMomentService_BatchReoptimize_AllowsNilCache(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	momentID := uuid.Must(uuid.NewV4())
	contentURL := "moments/" + eventID.String() + "/optimized/photo.webp"
	repo := &mockMomentRepo{
		GetMomentsByIDsFunc: func(ids []uuid.UUID) ([]models.Moment, error) {
			return []models.Moment{{
				ID:               momentID,
				EventID:          &eventID,
				ContentURL:       contentURL,
				ContentType:      "image/webp",
				ProcessingStatus: "done",
			}}, nil
		},
		UpdateContentFunc: func(id uuid.UUID, contentURLArg, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
			return nil
		},
	}
	publisher := &mockMediaJobPublisher{}

	svc := NewMomentService(repo, nil, publisher)
	succeeded, skipped, failed, err := svc.BatchReoptimize([]uuid.UUID{momentID})

	require.NoError(t, err)
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 0, skipped)
	assert.Equal(t, 0, failed)
}
