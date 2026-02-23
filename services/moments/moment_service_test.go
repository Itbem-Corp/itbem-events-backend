package moments

import (
	"context"
	"errors"
	"events-stocks/models"
	"events-stocks/services/ports"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	CreateMomentFunc func(m *models.Moment) error
	UpdateMomentFunc func(m *models.Moment) error
	DeleteMomentFunc func(id uuid.UUID) error
	GetMomentByIDFunc func(id uuid.UUID) (*models.Moment, error)
	ListMomentsFunc  func() ([]models.Moment, error)
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
func (m *mockMomentRepo) GetMomentByID(id uuid.UUID) (*models.Moment, error) {
	if m.GetMomentByIDFunc != nil {
		return m.GetMomentByIDFunc(id)
	}
	return &models.Moment{}, nil
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
func (m *mockMomentRepo) UpdateMomentContent(id uuid.UUID, contentURL, processingStatus, thumbnailURL string, durationMs, originalBytes, optimizedBytes int64) error {
	return nil
}
func (m *mockMomentRepo) ListForDashboard(eventID uuid.UUID) ([]models.Moment, error) {
	return nil, nil
}
func (m *mockMomentRepo) ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
	return nil, 0, nil
}
func (m *mockMomentRepo) BulkUpdateApproval(ids []uuid.UUID, isApproved bool) error {
	return nil
}
func (m *mockMomentRepo) GetDistinctEventIDsByMomentIDs(ids []uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

var _ ports.MomentRepository = (*mockMomentRepo)(nil)

// ---------------------------------------------------------------------------
// MomentService — CRUD + cache invalidation tests
// ---------------------------------------------------------------------------

func TestMomentService_CreateMoment_Success(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
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
	err := svc.CreateMoment(&models.Moment{})

	require.NoError(t, err)
	assert.True(t, repoCalled)
	assert.True(t, invalidateCalled)
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

// ---------------------------------------------------------------------------
// MomentService — ListMoments cache behavior
// ---------------------------------------------------------------------------

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
