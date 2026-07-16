package events

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"fmt"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

func eventCachePatternRecorder(t *testing.T, called *bool) *mockCacheRepo {
	t.Helper()
	return &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			*called = true
			assert.Equal(t, "*:"+utils.RedisServiceEventsKey, pattern)
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// Mock: EventsRepository
// ---------------------------------------------------------------------------

type mockEventsRepo struct {
	CreateEventFunc                  func(event *models.Event) error
	UpdateEventFunc                  func(event *models.Event) error
	DeleteEventFunc                  func(id uuid.UUID) error
	ListEventsFunc                   func(page int, pageSize int, name string) ([]models.Event, error)
	GetEventByIDFunc                 func(id uuid.UUID) (string, error)
	GetEventByIDRawFunc              func(id uuid.UUID) (*models.Event, error)
	GetEventByIDForSpecFunc          func(id uuid.UUID) (*models.Event, error)
	GetEventByIdentifierFunc         func(identifier string) (*models.Event, error)
	GetEventsByClientIDFunc          func(clientID uuid.UUID) ([]models.Event, error)
	GetAllEventsDashboardFunc        func() ([]models.Event, error)
	GetEventsForUserFunc             func(userID uuid.UUID) ([]models.Event, error)
	UpdateEventCoverFunc             func(id uuid.UUID, coverImageURL string) error
	UpdateEventCoverWithVariantsFunc func(id uuid.UUID, coverImageURL string, variants models.MediaVariants) error
	BeginEventCoverProcessingFunc    func(id uuid.UUID, pendingURL, jobID string) (*models.Event, string, error)
	ApplyEventCoverProcessingFunc    func(id uuid.UUID, callback dtos.MediaProcessingCallback) (*models.Event, string, models.MediaVariants, error)
	IdentifierExistsFunc             func(identifier string) bool
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
	return "[{}]", nil
}
func (m *mockEventsRepo) GetEventByIDRaw(id uuid.UUID) (*models.Event, error) {
	if m.GetEventByIDRawFunc != nil {
		return m.GetEventByIDRawFunc(id)
	}
	return &models.Event{ID: id}, nil
}
func (m *mockEventsRepo) GetEventByIDForSpec(id uuid.UUID) (*models.Event, error) {
	if m.GetEventByIDForSpecFunc != nil {
		return m.GetEventByIDForSpecFunc(id)
	}
	return &models.Event{ID: id}, nil
}
func (m *mockEventsRepo) GetEventByIdentifier(identifier string) (*models.Event, error) {
	if m.GetEventByIdentifierFunc != nil {
		return m.GetEventByIdentifierFunc(identifier)
	}
	return &models.Event{}, nil
}
func (m *mockEventsRepo) GetEventsByClientID(clientID uuid.UUID) ([]models.Event, error) {
	if m.GetEventsByClientIDFunc != nil {
		return m.GetEventsByClientIDFunc(clientID)
	}
	return nil, nil
}
func (m *mockEventsRepo) GetAllEventsForDashboard() ([]models.Event, error) {
	if m.GetAllEventsDashboardFunc != nil {
		return m.GetAllEventsDashboardFunc()
	}
	return nil, nil
}
func (m *mockEventsRepo) GetEventsForUser(userID uuid.UUID) ([]models.Event, error) {
	if m.GetEventsForUserFunc != nil {
		return m.GetEventsForUserFunc(userID)
	}
	return nil, nil
}
func (m *mockEventsRepo) UpdateEventCover(id uuid.UUID, coverImageURL string) error {
	if m.UpdateEventCoverFunc != nil {
		return m.UpdateEventCoverFunc(id, coverImageURL)
	}
	return nil
}
func (m *mockEventsRepo) UpdateEventCoverWithVariants(id uuid.UUID, coverImageURL string, variants models.MediaVariants) error {
	if m.UpdateEventCoverWithVariantsFunc != nil {
		return m.UpdateEventCoverWithVariantsFunc(id, coverImageURL, variants)
	}
	return m.UpdateEventCover(id, coverImageURL)
}
func (m *mockEventsRepo) BeginEventCoverProcessing(id uuid.UUID, pendingURL, jobID string) (*models.Event, string, error) {
	if m.BeginEventCoverProcessingFunc != nil {
		return m.BeginEventCoverProcessingFunc(id, pendingURL, jobID)
	}
	return &models.Event{ID: id, CoverPendingURL: pendingURL, CoverProcessingJobID: jobID, CoverProcessingGeneration: 1, CoverProcessingStatus: "pending"}, "", nil
}
func (m *mockEventsRepo) ApplyEventCoverProcessing(id uuid.UUID, callback dtos.MediaProcessingCallback) (*models.Event, string, models.MediaVariants, error) {
	if m.ApplyEventCoverProcessingFunc != nil {
		return m.ApplyEventCoverProcessingFunc(id, callback)
	}
	return &models.Event{ID: id, CoverImageURL: callback.ObjectKey, CoverProcessingStatus: callback.ProcessingStatus}, "", nil, nil
}
func (m *mockEventsRepo) IdentifierExists(identifier string) bool {
	if m.IdentifierExistsFunc != nil {
		return m.IdentifierExistsFunc(identifier)
	}
	return false
}

var _ ports.EventsRepository = (*mockEventsRepo)(nil)
var _ ports.EventCoverVariantsRepository = (*mockEventsRepo)(nil)
var _ ports.EventCoverProcessingRepository = (*mockEventsRepo)(nil)

// ---------------------------------------------------------------------------
// Mock: EventConfigRepository
// ---------------------------------------------------------------------------

type mockEventConfigRepo struct {
	CreateEventConfigFunc  func(m *models.EventConfig) error
	UpdateEventConfigFunc  func(m *models.EventConfig) error
	DeleteEventConfigFunc  func(id uuid.UUID) error
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
	CreateEventSectionFunc   func(m *models.EventSection) error
	UpdateEventSectionFunc   func(m *models.EventSection) error
	DeleteEventSectionFunc   func(id uuid.UUID) error
	BulkUpdateOrderFunc      func(eventID uuid.UUID, updates map[uuid.UUID]int) error
	GetEventSectionByIDFunc  func(id uuid.UUID) (*models.EventSection, error)
	ListEventSectionsFunc    func() ([]models.EventSection, error)
	ListByEventIDFunc        func(eventID uuid.UUID) ([]models.EventSection, error)
	ListByEventIDForSpecFunc func(eventID uuid.UUID) ([]models.EventSection, error)
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
func (m *mockEventSectionRepo) BulkUpdateSectionOrder(eventID uuid.UUID, updates map[uuid.UUID]int) error {
	if m.BulkUpdateOrderFunc != nil {
		return m.BulkUpdateOrderFunc(eventID, updates)
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
func (m *mockEventSectionRepo) ListByEventIDForSpec(eventID uuid.UUID) ([]models.EventSection, error) {
	if m.ListByEventIDForSpecFunc != nil {
		return m.ListByEventIDForSpecFunc(eventID)
	}
	return m.ListByEventID(eventID)
}

var _ ports.EventSectionRepository = (*mockEventSectionRepo)(nil)

// ---------------------------------------------------------------------------
// EventService tests
// ---------------------------------------------------------------------------

func TestEventService_CreateEvent_Success(t *testing.T) {
	invalidateCalled := false
	cache := eventCachePatternRecorder(t, &invalidateCalled)
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
	cache := eventCachePatternRecorder(t, &invalidateCalled)
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
	cache := eventCachePatternRecorder(t, &invalidateCalled)
	repo := &mockEventsRepo{
		UpdateEventFunc: func(event *models.Event) error { return nil },
	}

	svc := NewEventService(repo, cache)
	err := svc.UpdateEvent(&models.Event{})

	require.NoError(t, err)
	assert.True(t, invalidateCalled)
}

func TestEventService_UpdateEvent_IgnoresCacheInvalidationError(t *testing.T) {
	repoCalled := false
	repo := &mockEventsRepo{
		UpdateEventFunc: func(event *models.Event) error {
			repoCalled = true
			return nil
		},
	}
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			return errors.New("redis unavailable")
		},
	}

	svc := NewEventService(repo, cache)
	err := svc.UpdateEvent(&models.Event{})

	require.NoError(t, err)
	assert.True(t, repoCalled)
}

func TestEventService_UpdateEvent_RepoError(t *testing.T) {
	invalidateCalled := false
	cache := eventCachePatternRecorder(t, &invalidateCalled)
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

	cache := eventCachePatternRecorder(t, &invalidateCalled)
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
	cache := eventCachePatternRecorder(t, &invalidateCalled)
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

func TestEventService_ClearCoverImage_ClearsCoverAndInvalidatesCache(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	var updatedID uuid.UUID
	var updatedCover string
	invalidateCalled := false

	repo := &mockEventsRepo{
		GetEventByIDRawFunc: func(id uuid.UUID) (*models.Event, error) {
			require.Equal(t, eventID, id)
			return &models.Event{ID: id, CoverImageURL: "events/old-cover.webp"}, nil
		},
		UpdateEventCoverFunc: func(id uuid.UUID, coverImageURL string) error {
			updatedID = id
			updatedCover = coverImageURL
			return nil
		},
	}
	cache := eventCachePatternRecorder(t, &invalidateCalled)

	svc := NewEventService(repo, cache)
	oldPath, err := svc.ClearCoverImage(eventID)

	require.NoError(t, err)
	assert.Equal(t, "events/old-cover.webp", oldPath)
	assert.Equal(t, eventID, updatedID)
	assert.Empty(t, updatedCover)
	assert.True(t, invalidateCalled)
}

func TestEventService_ClearCoverImage_NoExistingCoverSkipsUpdate(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	updateCalled := false
	invalidateCalled := false

	repo := &mockEventsRepo{
		GetEventByIDRawFunc: func(id uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: id}, nil
		},
		UpdateEventCoverFunc: func(id uuid.UUID, coverImageURL string) error {
			updateCalled = true
			return nil
		},
	}
	cache := eventCachePatternRecorder(t, &invalidateCalled)

	svc := NewEventService(repo, cache)
	oldPath, err := svc.ClearCoverImage(eventID)

	require.NoError(t, err)
	assert.Empty(t, oldPath)
	assert.False(t, updateCalled)
	assert.False(t, invalidateCalled)
}

func TestEventService_ReplaceCoverImagePersistsVariantsAndReturnsOldSet(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	oldVariants := models.MediaVariants{{ObjectKey: "events/old-640.webp", Width: 640, Format: "webp"}}
	newVariants := models.MediaVariants{{ObjectKey: "events/new-640.webp", Width: 640, Format: "webp"}}
	var storedPath string
	var storedVariants models.MediaVariants
	repo := &mockEventsRepo{
		GetEventByIDRawFunc: func(uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: eventID, CoverImageURL: "events/old.webp", CoverVariants: oldVariants}, nil
		},
		UpdateEventCoverWithVariantsFunc: func(id uuid.UUID, path string, variants models.MediaVariants) error {
			require.Equal(t, eventID, id)
			storedPath, storedVariants = path, variants
			return nil
		},
	}

	oldPath, replacedVariants, err := NewEventService(repo, &mockCacheRepo{}).ReplaceCoverImageWithVariants(eventID, "events/new.webp", newVariants)

	require.NoError(t, err)
	assert.Equal(t, "events/old.webp", oldPath)
	assert.Equal(t, oldVariants, replacedVariants)
	assert.Equal(t, "events/new.webp", storedPath)
	assert.Equal(t, newVariants, storedVariants)
}

func TestEventService_ApplyCoverProcessingCallbackValidatesOwnedTerminalKeys(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	jobID := uuid.Must(uuid.NewV4()).String()
	called := false
	repo := &mockEventsRepo{ApplyEventCoverProcessingFunc: func(id uuid.UUID, callback dtos.MediaProcessingCallback) (*models.Event, string, models.MediaVariants, error) {
		called = true
		return &models.Event{ID: id, CoverImageURL: callback.ObjectKey}, "events/old.webp", nil, nil
	}}
	svc := NewEventService(repo, &mockCacheRepo{})
	_, oldPath, _, err := svc.ApplyCoverProcessingCallback(eventID, dtos.MediaProcessingCallback{
		JobID: jobID, Generation: 2, ProcessingStatus: "done",
		ObjectKey:     fmt.Sprintf("events/%s/covers/%s.webp", eventID, jobID),
		MediaVariants: []dtos.MediaVariant{{ObjectKey: fmt.Sprintf("events/%s/covers/%s-640.webp", eventID, jobID), Width: 640, Format: "webp", Bytes: 10}},
	})
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "events/old.webp", oldPath)
}

func TestEventService_ApplyCoverProcessingCallbackRejectsForeignOutput(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	called := false
	repo := &mockEventsRepo{ApplyEventCoverProcessingFunc: func(uuid.UUID, dtos.MediaProcessingCallback) (*models.Event, string, models.MediaVariants, error) {
		called = true
		return nil, "", nil, nil
	}}
	_, _, _, err := NewEventService(repo, nil).ApplyCoverProcessingCallback(eventID, dtos.MediaProcessingCallback{JobID: uuid.Must(uuid.NewV4()).String(), Generation: 1, ProcessingStatus: "done", ObjectKey: "events/another/cover.webp"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCoverProcessingTransition)
	assert.False(t, called)
}

func TestEventService_ApplyCoverProcessingCallbackPreservesOrganizationNamespace(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	jobID := uuid.Must(uuid.NewV4()).String()
	root := "organizations/" + uuid.Must(uuid.NewV4()).String() + "/"
	repo := &mockEventsRepo{
		GetEventByIDRawFunc: func(id uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: id, CoverPendingURL: root + "events/" + id.String() + "/raw/source.jpg"}, nil
		},
		ApplyEventCoverProcessingFunc: func(id uuid.UUID, callback dtos.MediaProcessingCallback) (*models.Event, string, models.MediaVariants, error) {
			return &models.Event{ID: id, CoverImageURL: callback.ObjectKey}, "", nil, nil
		},
	}

	_, _, _, err := NewEventService(repo, &mockCacheRepo{}).ApplyCoverProcessingCallback(eventID, dtos.MediaProcessingCallback{
		JobID: jobID, Generation: 1, ProcessingStatus: "done",
		ObjectKey: root + fmt.Sprintf("events/%s/covers/%s.webp", eventID, jobID),
		MediaVariants: []dtos.MediaVariant{{
			ObjectKey: root + fmt.Sprintf("events/%s/covers/%s-640.webp", eventID, jobID),
			Width:     640, Format: "webp", Bytes: 10,
		}},
	})
	require.NoError(t, err)
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

func TestEventService_ListEvents_UsesNamedCacheKeyAndTTL(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	repo := &mockEventsRepo{
		ListEventsFunc: func(page, pageSize int, name string) ([]models.Event, error) {
			return []models.Event{{ID: eventID}}, nil
		},
	}
	var getKey string
	var saveKey string
	var savedTTL time.Duration
	cache := &mockCacheRepo{
		GetKeyFunc: func(ctx context.Context, key string) (string, error) {
			getKey = key
			return "", errors.New("cache miss")
		},
		SaveKeyFunc: func(ctx context.Context, key string, value string, ttl time.Duration) error {
			saveKey = key
			savedTTL = ttl
			return nil
		},
	}

	result, err := NewEventService(repo, cache).ListEvents()

	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, eventID, result[0].ID)
	assert.Equal(t, "all:"+utils.RedisServiceEventsKey, getKey)
	assert.Equal(t, "all:"+utils.RedisServiceEventsKey, saveKey)
	assert.Equal(t, utils.CacheTTLs[utils.RedisServiceEventsKey], savedTTL)
}

func TestEventService_ListEvents_AllowsNilCache(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	repoCalled := false
	repo := &mockEventsRepo{
		ListEventsFunc: func(page, pageSize int, name string) ([]models.Event, error) {
			repoCalled = true
			return []models.Event{{ID: eventID}}, nil
		},
	}

	svc := NewEventService(repo, nil)
	result, err := svc.ListEvents()

	require.NoError(t, err)
	assert.True(t, repoCalled)
	require.Len(t, result, 1)
	assert.Equal(t, eventID, result[0].ID)
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
	cache := eventCachePatternRecorder(t, &invalidateCalled)
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

func TestNewDefaultEventConfig_MatchesDashboardDefaults(t *testing.T) {
	id := uuid.Must(uuid.NewV4())

	cfg := NewDefaultEventConfig(id)

	require.NotNil(t, cfg)
	assert.Equal(t, id, cfg.ID)
	require.NotNil(t, cfg.DesignTemplateID)
	assert.Equal(t, models.DefaultDesignTemplateID(), *cfg.DesignTemplateID)
	assert.False(t, cfg.IsPublic)
	assert.False(t, cfg.AllowUploads)
	assert.False(t, cfg.AllowMessages)
	assert.False(t, cfg.ShareUploadsEnabled)
	assert.True(t, cfg.ShowCountdown)
	assert.True(t, cfg.ShowRSVPSection)
	assert.True(t, cfg.ShowEventLocation)
	assert.True(t, cfg.ShowSecondLocation)
	assert.True(t, cfg.ShowHostsSection)
	assert.True(t, cfg.ShowPhotoGallery)
	assert.True(t, cfg.ShowMomentWall)
	assert.True(t, cfg.VisibilityConfigured)
	assert.True(t, cfg.ShowContactSection)
	assert.True(t, cfg.ShowEventSchedule)
	assert.Equal(t, 30, cfg.MaxUploadsPerGuest)
}

func TestEventConfigService_GetEventConfigByID_NormalizesLegacyVisibilityDefaults(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	stored := &models.EventConfig{ID: id, IsPublic: true}
	repo := &mockEventConfigRepo{
		GetEventConfigByIDFunc: func(i uuid.UUID) (*models.EventConfig, error) {
			return stored, nil
		},
	}

	svc := NewEventConfigService(repo, &mockCacheRepo{})
	result, err := svc.GetEventConfigByID(id)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsPublic)
	assert.True(t, result.ShowHeader)
	assert.True(t, result.ShowFooter)
	assert.True(t, result.ShowCountdown)
	assert.True(t, result.ShowMomentWall)
	assert.False(t, stored.ShowHeader)
}

func TestEventConfigService_GetEventConfigByID_PreservesOpenUploadsWhenNormalizingLegacyVisibility(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	repo := &mockEventConfigRepo{
		GetEventConfigByIDFunc: func(i uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				ID:                    id,
				AllowUploads:          true,
				ShareUploadsEnabled:   true,
				MaxUploadsPerGuest:    12,
				DefaultWelcomeMessage: "Hola",
			}, nil
		},
	}

	svc := NewEventConfigService(repo, &mockCacheRepo{})
	result, err := svc.GetEventConfigByID(id)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ShowHeader)
	assert.True(t, result.ShowFooter)
	assert.True(t, result.ShowCountdown)
	assert.False(t, result.ShowMomentWall)
	assert.True(t, result.AllowUploads)
	assert.True(t, result.ShareUploadsEnabled)
	assert.Equal(t, 12, result.MaxUploadsPerGuest)
	assert.Equal(t, "Hola", result.DefaultWelcomeMessage)
}

func TestEventConfigService_CreateEventConfig_RepoError(t *testing.T) {
	invalidateCalled := false
	cache := eventCachePatternRecorder(t, &invalidateCalled)
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
	cache := eventCachePatternRecorder(t, &invalidateCalled)
	svc := NewEventConfigService(&mockEventConfigRepo{}, cache)
	err := svc.UpdateEventConfig(&models.EventConfig{})

	require.NoError(t, err)
	assert.True(t, invalidateCalled)
}

func TestEventConfigService_UpdateEventConfig_IgnoresCacheInvalidationError(t *testing.T) {
	repoCalled := false
	repo := &mockEventConfigRepo{
		UpdateEventConfigFunc: func(obj *models.EventConfig) error {
			repoCalled = true
			return nil
		},
	}
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			return errors.New("redis unavailable")
		},
	}

	svc := NewEventConfigService(repo, cache)
	err := svc.UpdateEventConfig(&models.EventConfig{})

	require.NoError(t, err)
	assert.True(t, repoCalled)
}

func TestEventConfigService_DeleteEventConfig_InvalidatesCache(t *testing.T) {
	invalidateCalled := false
	cache := eventCachePatternRecorder(t, &invalidateCalled)
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

func TestEventConfigService_GetEventConfigByID_AutoCreatesDefaultOnRecordNotFound(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	var created *models.EventConfig
	callCount := 0
	repo := &mockEventConfigRepo{
		GetEventConfigByIDFunc: func(i uuid.UUID) (*models.EventConfig, error) {
			callCount++
			if callCount == 1 {
				return nil, gorm.ErrRecordNotFound
			}
			require.NotNil(t, created)
			return created, nil
		},
		CreateEventConfigFunc: func(obj *models.EventConfig) error {
			created = obj
			return nil
		},
	}

	svc := NewEventConfigService(repo, &mockCacheRepo{})
	result, err := svc.GetEventConfigByID(id)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, id, result.ID)
	assert.True(t, result.ShowMomentWall)
	assert.True(t, result.ShowRSVPSection)
	assert.False(t, result.IsPublic)
	assert.Equal(t, 2, callCount)
}

func TestBuildPageSpecInjectsMomentWallRuntimeConfig(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	event := &models.Event{
		ID:         eventID,
		Name:       "Boda Ana y Luis",
		Identifier: "boda-ana-luis",
		Timezone:   "America/Mexico_City",
		EventType:  models.EventType{Name: "wedding"},
	}
	section := models.EventSection{
		ID:            sectionID,
		EventID:       eventID,
		Title:         "Momentos",
		ComponentType: "MomentWall",
		Config:        `{"title":"Momentos","subtitle":"Comparte algo"}`,
		Order:         1,
		IsVisible:     true,
	}
	deps := buildSpecDeps{
		getSections: func(id uuid.UUID) ([]models.EventSection, error) {
			assert.Equal(t, eventID, id)
			return []models.EventSection{section}, nil
		},
	}

	openSpec, err := buildPageSpecFromEventWithConfig(event, deps, &models.EventConfig{
		ID:                          eventID,
		AllowUploads:                true,
		AllowMessages:               true,
		ShareUploadsEnabled:         true,
		MaxUploadsPerGuest:          12,
		DefaultMomentRequestMessage: "Sube tus mejores fotos",
		ShowMomentWall:              false,
		ShowFooter:                  true,
	})
	require.NoError(t, err)
	require.Len(t, openSpec.Sections, 1)
	var openConfig map[string]any
	require.NoError(t, json.Unmarshal(openSpec.Sections[0].Config, &openConfig))
	assert.Equal(t, "boda-ana-luis", openConfig["identifier"])
	assert.Equal(t, true, openConfig["allow_uploads"])
	assert.Equal(t, true, openConfig["allow_messages"])
	assert.Equal(t, true, openConfig["share_uploads_enabled"])
	assert.Equal(t, false, openConfig["published"])
	assert.Equal(t, float64(12), openConfig["max_uploads_per_guest"])
	assert.Equal(t, "Sube tus mejores fotos", openConfig["moment_request_message"])
	assert.Equal(t, "Sube tus mejores fotos", openConfig["subtitle"])

	publishedSpec, err := buildPageSpecFromEventWithConfig(event, deps, &models.EventConfig{
		ID:                  eventID,
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		MaxUploadsPerGuest:  12,
		ShowMomentWall:      true,
		ShowFooter:          true,
	})
	require.NoError(t, err)
	require.Len(t, publishedSpec.Sections, 1)
	var publishedConfig map[string]any
	require.NoError(t, json.Unmarshal(publishedSpec.Sections[0].Config, &publishedConfig))
	assert.Equal(t, true, publishedConfig["published"])
	assert.Equal(t, false, publishedConfig["allow_uploads"])
	assert.Equal(t, false, publishedConfig["share_uploads_enabled"])
	assert.Equal(t, float64(12), publishedConfig["max_uploads_per_guest"])
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

func TestEventSectionService_ListEventSections_AllowsNilCache(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	repoCalled := false
	repo := &mockEventSectionRepo{
		ListEventSectionsFunc: func() ([]models.EventSection, error) {
			repoCalled = true
			return []models.EventSection{{ID: sectionID}}, nil
		},
	}

	svc := NewEventSectionService(repo, nil)
	result, err := svc.ListEventSections()

	require.NoError(t, err)
	assert.True(t, repoCalled)
	require.Len(t, result, 1)
	assert.Equal(t, sectionID, result[0].ID)
}

func TestEventSectionService_ListEventSections_UsesNamedCacheKeyAndTTL(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	repo := &mockEventSectionRepo{
		ListEventSectionsFunc: func() ([]models.EventSection, error) {
			return []models.EventSection{{ID: sectionID}}, nil
		},
	}
	var getKey string
	var saveKey string
	var savedTTL time.Duration
	cache := &mockCacheRepo{
		GetKeyFunc: func(ctx context.Context, key string) (string, error) {
			getKey = key
			return "", errors.New("cache miss")
		},
		SaveKeyFunc: func(ctx context.Context, key string, value string, ttl time.Duration) error {
			saveKey = key
			savedTTL = ttl
			return nil
		},
	}

	result, err := NewEventSectionService(repo, cache).ListEventSections()

	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, sectionID, result[0].ID)
	assert.Equal(t, "all:"+utils.RedisEventSectionsKey, getKey)
	assert.Equal(t, "all:"+utils.RedisEventSectionsKey, saveKey)
	assert.Equal(t, utils.CacheTTLs[utils.RedisEventSectionsKey], savedTTL)
}

func TestEventSectionService_UpdateEventSection_IgnoresCacheInvalidationError(t *testing.T) {
	repoCalled := false
	repo := &mockEventSectionRepo{
		UpdateEventSectionFunc: func(obj *models.EventSection) error {
			repoCalled = true
			return nil
		},
	}
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			return errors.New("redis unavailable")
		},
	}

	svc := NewEventSectionService(repo, cache)
	err := svc.UpdateEventSection(&models.EventSection{})

	require.NoError(t, err)
	assert.True(t, repoCalled)
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

func TestEventSectionService_BulkUpdateSectionOrder_InvalidatesCache(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	updates := map[uuid.UUID]int{sectionID: 2}
	repoCalled := false
	invalidateCalled := false

	repo := &mockEventSectionRepo{
		BulkUpdateOrderFunc: func(eID uuid.UUID, got map[uuid.UUID]int) error {
			repoCalled = true
			assert.Equal(t, eventID, eID)
			assert.Equal(t, updates, got)
			return nil
		},
	}
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			assert.Equal(t, utils.RedisEventSectionsKey, resource)
			assert.Equal(t, "all", key)
			return nil
		},
	}

	svc := NewEventSectionService(repo, cache)
	err := svc.BulkUpdateSectionOrder(eventID, updates)

	require.NoError(t, err)
	assert.True(t, repoCalled)
	assert.True(t, invalidateCalled)
}

func TestEventSectionService_BulkUpdateSectionOrder_RepoError(t *testing.T) {
	invalidateCalled := false
	repo := &mockEventSectionRepo{
		BulkUpdateOrderFunc: func(eventID uuid.UUID, updates map[uuid.UUID]int) error {
			return errors.New("update failed")
		},
	}
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			invalidateCalled = true
			return nil
		},
	}

	svc := NewEventSectionService(repo, cache)
	err := svc.BulkUpdateSectionOrder(uuid.Must(uuid.NewV4()), map[uuid.UUID]int{uuid.Must(uuid.NewV4()): 1})

	require.Error(t, err)
	assert.False(t, invalidateCalled)
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

func TestEventSectionService_ListByEventID_SortsByRenderOrderWithStableTieBreak(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	firstID := uuid.Must(uuid.FromString("00000000-0000-0000-0000-000000000001"))
	secondID := uuid.Must(uuid.FromString("00000000-0000-0000-0000-000000000002"))
	thirdID := uuid.Must(uuid.FromString("00000000-0000-0000-0000-000000000003"))

	repo := &mockEventSectionRepo{
		ListByEventIDFunc: func(eID uuid.UUID) ([]models.EventSection, error) {
			assert.Equal(t, eventID, eID)
			return []models.EventSection{
				{ID: thirdID, Order: 2},
				{ID: secondID, Order: 1},
				{ID: firstID, Order: 1},
			}, nil
		},
	}

	result, err := NewEventSectionService(repo, &mockCacheRepo{}).ListByEventID(eventID)

	require.NoError(t, err)
	require.Len(t, result, 3)
	assert.Equal(t, []uuid.UUID{firstID, secondID, thirdID}, []uuid.UUID{
		result[0].ID,
		result[1].ID,
		result[2].ID,
	})
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
