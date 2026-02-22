package events

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
// Shared mock: CacheRepository
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
// Mock: EventsRepository
// ---------------------------------------------------------------------------

type mockEventsRepo struct {
	CreateEventFunc func(event *models.Event) error
	UpdateEventFunc func(event *models.Event) error
	DeleteEventFunc func(id uuid.UUID) error
	ListEventsFunc  func(page int, pageSize int, name string) ([]models.Event, error)
	GetEventByIDFunc      func(id uuid.UUID) (string, error)
	IdentifierExistsFunc  func(identifier string) bool
}

func (m *mockEventsRepo) CreateEvent(event *models.Event) error {
	if m.CreateEventFunc != nil {
		return m.CreateEventFunc(event)
	}
	return nil
}
func (m *mockEventsRepo) UpdateEvent(event *models.Event) error {
	if m.UpdateEventFunc != nil {
		return m.UpdateEventFunc(event)
	}
	return nil
}
func (m *mockEventsRepo) DeleteEvent(id uuid.UUID) error {
	if m.DeleteEventFunc != nil {
		return m.DeleteEventFunc(id)
	}
	return nil
}
func (m *mockEventsRepo) ListEvents(page int, pageSize int, name string) ([]models.Event, error) {
	if m.ListEventsFunc != nil {
		return m.ListEventsFunc(page, pageSize, name)
	}
	return nil, nil
}
func (m *mockEventsRepo) GetEventByID(id uuid.UUID) (string, error) {
	if m.GetEventByIDFunc != nil {
		return m.GetEventByIDFunc(id)
	}
	return "{}", nil
}
func (m *mockEventsRepo) IdentifierExists(identifier string) bool {
	if m.IdentifierExistsFunc != nil {
		return m.IdentifierExistsFunc(identifier)
	}
	return false
}

var _ ports.EventsRepository = (*mockEventsRepo)(nil)

// ---------------------------------------------------------------------------
// Mock: EventConfigRepository
// ---------------------------------------------------------------------------

type mockEventConfigRepo struct {
	CreateEventConfigFunc func(m *models.EventConfig) error
	UpdateEventConfigFunc func(m *models.EventConfig) error
	DeleteEventConfigFunc func(id uuid.UUID) error
	GetEventConfigByIDFunc func(id uuid.UUID) (*models.EventConfig, error)
}

func (m *mockEventConfigRepo) CreateEventConfig(obj *models.EventConfig) error {
	if m.CreateEventConfigFunc != nil {
		return m.CreateEventConfigFunc(obj)
	}
	return nil
}
func (m *mockEventConfigRepo) UpdateEventConfig(obj *models.EventConfig) error {
	if m.UpdateEventConfigFunc != nil {
		return m.UpdateEventConfigFunc(obj)
	}
	return nil
}
func (m *mockEventConfigRepo) DeleteEventConfig(id uuid.UUID) error {
	if m.DeleteEventConfigFunc != nil {
		return m.DeleteEventConfigFunc(id)
	}
	return nil
}
func (m *mockEventConfigRepo) GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	if m.GetEventConfigByIDFunc != nil {
		return m.GetEventConfigByIDFunc(id)
	}
	return &models.EventConfig{}, nil
}

var _ ports.EventConfigRepository = (*mockEventConfigRepo)(nil)

// ---------------------------------------------------------------------------
// Mock: EventSectionRepository
// ---------------------------------------------------------------------------

type mockEventSectionRepo struct {
	CreateEventSectionFunc func(m *models.EventSection) error
	UpdateEventSectionFunc func(m *models.EventSection) error
	DeleteEventSectionFunc func(id uuid.UUID) error
	GetEventSectionByIDFunc func(id uuid.UUID) (*models.EventSection, error)
	ListEventSectionsFunc  func() ([]models.EventSection, error)
	ListByEventIDFunc      func(eventID uuid.UUID) ([]models.EventSection, error)
}

func (m *mockEventSectionRepo) CreateEventSection(obj *models.EventSection) error {
	if m.CreateEventSectionFunc != nil {
		return m.CreateEventSectionFunc(obj)
	}
	return nil
}
func (m *mockEventSectionRepo) UpdateEventSection(obj *models.EventSection) error {
	if m.UpdateEventSectionFunc != nil {
		return m.UpdateEventSectionFunc(obj)
	}
	return nil
}
func (m *mockEventSectionRepo) DeleteEventSection(id uuid.UUID) error {
	if m.DeleteEventSectionFunc != nil {
		return m.DeleteEventSectionFunc(id)
	}
	return nil
}
func (m *mockEventSectionRepo) GetEventSectionByID(id uuid.UUID) (*models.EventSection, error) {
	if m.GetEventSectionByIDFunc != nil {
		return m.GetEventSectionByIDFunc(id)
	}
	return &models.EventSection{}, nil
}
func (m *mockEventSectionRepo) ListEventSections() ([]models.EventSection, error) {
	if m.ListEventSectionsFunc != nil {
		return m.ListEventSectionsFunc()
	}
	return nil, nil
}
func (m *mockEventSectionRepo) ListByEventID(eventID uuid.UUID) ([]models.EventSection, error) {
	if m.ListByEventIDFunc != nil {
		return m.ListByEventIDFunc(eventID)
	}
	return nil, nil
}

var _ ports.EventSectionRepository = (*mockEventSectionRepo)(nil)

// ---------------------------------------------------------------------------
// EventService tests
// ---------------------------------------------------------------------------

func TestEventService_CreateEvent_Success(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			assert.Equal(t, "events", resource)
			assert.Equal(t, "all", key)
			return nil
		},
	}
	repoCalled := false
	repo := &mockEventsRepo{
		CreateEventFunc: func(event *models.Event) error {
			repoCalled = true
			return nil
		},
	}

	svc := NewEventService(repo, cache)
	err := svc.CreateEvent(&models.Event{})

	require.NoError(t, err)
	assert.True(t, repoCalled, "repo.CreateEvent must be called")
	assert.True(t, invalidateCalled, "cache must be invalidated after create")
}

func TestEventService_CreateEvent_RepoError_NoCacheInvalidation(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repo := &mockEventsRepo{
		CreateEventFunc: func(event *models.Event) error {
			return errors.New("db write failed")
		},
	}

	svc := NewEventService(repo, cache)
	err := svc.CreateEvent(&models.Event{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "db write failed")
	assert.False(t, invalidateCalled, "cache must NOT be invalidated when repo fails")
}

func TestEventService_UpdateEvent_Success(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repo := &mockEventsRepo{
		UpdateEventFunc: func(event *models.Event) error { return nil },
	}

	svc := NewEventService(repo, cache)
	err := svc.UpdateEvent(&models.Event{})

	require.NoError(t, err)
	assert.True(t, invalidateCalled)
}

func TestEventService_UpdateEvent_RepoError(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repo := &mockEventsRepo{
		UpdateEventFunc: func(event *models.Event) error {
			return errors.New("constraint violation")
		},
	}

	svc := NewEventService(repo, cache)
	err := svc.UpdateEvent(&models.Event{})

	require.Error(t, err)
	assert.False(t, invalidateCalled)
}

func TestEventService_DeleteEvent_Success(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	var deletedID uuid.UUID
	invalidateCalled := false

	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repo := &mockEventsRepo{
		DeleteEventFunc: func(i uuid.UUID) error {
			deletedID = i
			return nil
		},
	}

	svc := NewEventService(repo, cache)
	err := svc.DeleteEvent(id)

	require.NoError(t, err)
	assert.Equal(t, id, deletedID)
	assert.True(t, invalidateCalled)
}

func TestEventService_DeleteEvent_RepoError(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repo := &mockEventsRepo{
		DeleteEventFunc: func(id uuid.UUID) error {
			return errors.New("record not found")
		},
	}

	svc := NewEventService(repo, cache)
	err := svc.DeleteEvent(uuid.Must(uuid.NewV4()))

	require.Error(t, err)
	assert.False(t, invalidateCalled)
}

func TestEventService_ListEvents_CacheHit(t *testing.T) {
	repoCallCount := 0
	repo := &mockEventsRepo{
		ListEventsFunc: func(page, pageSize int, name string) ([]models.Event, error) {
			repoCallCount++
			return nil, nil
		},
	}
	// Return valid empty JSON array — cache hit
	cache := &mockCacheRepo{
		GetKeyFunc: func(ctx context.Context, key string) (string, error) {
			return `[]`, nil
		},
	}

	svc := NewEventService(repo, cache)
	result, err := svc.ListEvents()

	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Equal(t, 0, repoCallCount, "repo must NOT be called when cache hits")
}

func TestEventService_ListEvents_CacheMiss_CallsRepo(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	repoCalled := false
	saveKeyCalled := false

	repo := &mockEventsRepo{
		ListEventsFunc: func(page, pageSize int, name string) ([]models.Event, error) {
			repoCalled = true
			return []models.Event{{ID: eventID}}, nil
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

	svc := NewEventService(repo, cache)
	result, err := svc.ListEvents()

	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, eventID, result[0].ID)
	assert.True(t, repoCalled, "repo must be called on cache miss")
	assert.True(t, saveKeyCalled, "result must be stored in cache after repo call")
}

func TestEventService_ListEvents_CacheMiss_RepoError(t *testing.T) {
	repo := &mockEventsRepo{
		ListEventsFunc: func(page, pageSize int, name string) ([]models.Event, error) {
			return nil, errors.New("db connection lost")
		},
	}
	cache := &mockCacheRepo{
		GetKeyFunc: func(ctx context.Context, key string) (string, error) {
			return "", errors.New("cache miss")
		},
	}

	svc := NewEventService(repo, cache)
	result, err := svc.ListEvents()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "db connection lost")
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// EventConfigService tests
// ---------------------------------------------------------------------------

func TestEventConfigService_CreateEventConfig_Success(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repoCalled := false
	repo := &mockEventConfigRepo{
		CreateEventConfigFunc: func(obj *models.EventConfig) error {
			repoCalled = true
			return nil
		},
	}

	svc := NewEventConfigService(repo, cache)
	err := svc.CreateEventConfig(&models.EventConfig{})

	require.NoError(t, err)
	assert.True(t, repoCalled)
	assert.True(t, invalidateCalled)
}

func TestEventConfigService_CreateEventConfig_RepoError(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repo := &mockEventConfigRepo{
		CreateEventConfigFunc: func(obj *models.EventConfig) error {
			return errors.New("foreign key violation")
		},
	}

	svc := NewEventConfigService(repo, cache)
	err := svc.CreateEventConfig(&models.EventConfig{})

	require.Error(t, err)
	assert.False(t, invalidateCalled)
}

func TestEventConfigService_UpdateEventConfig_InvalidatesCache(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	svc := NewEventConfigService(&mockEventConfigRepo{}, cache)
	err := svc.UpdateEventConfig(&models.EventConfig{})

	require.NoError(t, err)
	assert.True(t, invalidateCalled)
}

func TestEventConfigService_DeleteEventConfig_InvalidatesCache(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	svc := NewEventConfigService(&mockEventConfigRepo{}, cache)
	err := svc.DeleteEventConfig(uuid.Must(uuid.NewV4()))

	require.NoError(t, err)
	assert.True(t, invalidateCalled)
}

func TestEventConfigService_GetEventConfigByID_Success(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	repo := &mockEventConfigRepo{
		GetEventConfigByIDFunc: func(i uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{ID: i}, nil
		},
	}

	svc := NewEventConfigService(repo, &mockCacheRepo{})
	result, err := svc.GetEventConfigByID(id)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, id, result.ID)
}

func TestEventConfigService_GetEventConfigByID_NotFound(t *testing.T) {
	repo := &mockEventConfigRepo{
		GetEventConfigByIDFunc: func(id uuid.UUID) (*models.EventConfig, error) {
			return nil, errors.New("record not found")
		},
	}

	svc := NewEventConfigService(repo, &mockCacheRepo{})
	result, err := svc.GetEventConfigByID(uuid.Must(uuid.NewV4()))

	require.Error(t, err)
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// EventSectionService tests
// ---------------------------------------------------------------------------

func TestEventSectionService_CreateEventSection_Success(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repoCalled := false
	repo := &mockEventSectionRepo{
		CreateEventSectionFunc: func(obj *models.EventSection) error {
			repoCalled = true
			return nil
		},
	}

	svc := NewEventSectionService(repo, cache)
	err := svc.CreateEventSection(&models.EventSection{})

	require.NoError(t, err)
	assert.True(t, repoCalled)
	assert.True(t, invalidateCalled)
}

func TestEventSectionService_CreateEventSection_RepoError(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	repo := &mockEventSectionRepo{
		CreateEventSectionFunc: func(obj *models.EventSection) error {
			return errors.New("insert failed")
		},
	}

	svc := NewEventSectionService(repo, cache)
	err := svc.CreateEventSection(&models.EventSection{})

	require.Error(t, err)
	assert.False(t, invalidateCalled)
}

func TestEventSectionService_DeleteEventSection_InvalidatesCache(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	svc := NewEventSectionService(&mockEventSectionRepo{}, cache)
	err := svc.DeleteEventSection(uuid.Must(uuid.NewV4()))

	require.NoError(t, err)
	assert.True(t, invalidateCalled)
}

func TestEventSectionService_UpdateEventSection_InvalidatesCache(t *testing.T) {
	invalidateCalled := false
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}
	svc := NewEventSectionService(&mockEventSectionRepo{}, cache)
	err := svc.UpdateEventSection(&models.EventSection{})

	require.NoError(t, err)
	assert.True(t, invalidateCalled)
}

func TestEventSectionService_ListByEventID_DelegatesToRepo(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	sectionA := uuid.Must(uuid.NewV4())
	sectionB := uuid.Must(uuid.NewV4())

	repo := &mockEventSectionRepo{
		ListByEventIDFunc: func(eID uuid.UUID) ([]models.EventSection, error) {
			assert.Equal(t, eventID, eID)
			return []models.EventSection{
				{ID: sectionA},
				{ID: sectionB},
			}, nil
		},
	}

	svc := NewEventSectionService(repo, &mockCacheRepo{})
	result, err := svc.ListByEventID(eventID)

	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestEventSectionService_ListByEventID_RepoError(t *testing.T) {
	repo := &mockEventSectionRepo{
		ListByEventIDFunc: func(eID uuid.UUID) ([]models.EventSection, error) {
			return nil, errors.New("query failed")
		},
	}

	svc := NewEventSectionService(repo, &mockCacheRepo{})
	result, err := svc.ListByEventID(uuid.Must(uuid.NewV4()))

	require.Error(t, err)
	assert.Nil(t, result)
}
