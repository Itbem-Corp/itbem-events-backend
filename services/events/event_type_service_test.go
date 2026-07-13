package events

import (
	"context"
	"errors"
	"events-stocks/models"
	"events-stocks/utils"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockEventTypeRepo struct {
	CreateEventTypeFunc  func(m *models.EventType) error
	UpdateEventTypeFunc  func(m *models.EventType) error
	DeleteEventTypeFunc  func(id uuid.UUID) error
	GetEventTypeByIDFunc func(id uuid.UUID) (*models.EventType, error)
	ListEventTypesFunc   func() ([]models.EventType, error)
}

func (m *mockEventTypeRepo) CreateEventType(model *models.EventType) error {
	if m.CreateEventTypeFunc != nil {
		return m.CreateEventTypeFunc(model)
	}
	return nil
}

func (m *mockEventTypeRepo) UpdateEventType(model *models.EventType) error {
	if m.UpdateEventTypeFunc != nil {
		return m.UpdateEventTypeFunc(model)
	}
	return nil
}

func (m *mockEventTypeRepo) DeleteEventType(id uuid.UUID) error {
	if m.DeleteEventTypeFunc != nil {
		return m.DeleteEventTypeFunc(id)
	}
	return nil
}

func (m *mockEventTypeRepo) GetEventTypeByID(id uuid.UUID) (*models.EventType, error) {
	if m.GetEventTypeByIDFunc != nil {
		return m.GetEventTypeByIDFunc(id)
	}
	return &models.EventType{ID: id}, nil
}

func (m *mockEventTypeRepo) ListEventTypes() ([]models.EventType, error) {
	if m.ListEventTypesFunc != nil {
		return m.ListEventTypesFunc()
	}
	return nil, nil
}

func TestEventTypeService_ListEventTypes_UsesNamedCacheKeyAndTTL(t *testing.T) {
	typeID := uuid.Must(uuid.NewV4())
	repo := &mockEventTypeRepo{
		ListEventTypesFunc: func() ([]models.EventType, error) {
			return []models.EventType{{ID: typeID, Name: "wedding"}}, nil
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

	result, err := NewEventTypeService(repo, cache).ListEventTypes()

	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, typeID, result[0].ID)
	assert.Equal(t, "all:"+utils.RedisEventTypesKey, getKey)
	assert.Equal(t, "all:"+utils.RedisEventTypesKey, saveKey)
	assert.Equal(t, utils.CacheTTLs[utils.RedisEventTypesKey], savedTTL)
}

func TestEventTypeService_MutationsInvalidateNamedCache(t *testing.T) {
	var invalidated []string
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource string, key string) error {
			invalidated = append(invalidated, key+":"+resource)
			return nil
		},
	}
	svc := NewEventTypeService(&mockEventTypeRepo{}, cache)
	typeID := uuid.Must(uuid.NewV4())

	require.NoError(t, svc.CreateEventType(&models.EventType{ID: typeID}))
	require.NoError(t, svc.UpdateEventType(&models.EventType{ID: typeID}))
	require.NoError(t, svc.DeleteEventType(typeID))

	assert.Equal(t, []string{
		"all:" + utils.RedisEventTypesKey,
		"all:" + utils.RedisEventTypesKey,
		"all:" + utils.RedisEventTypesKey,
	}, invalidated)
}

func TestEventTypeService_MutationsAllowNilCache(t *testing.T) {
	typeID := uuid.Must(uuid.NewV4())
	svc := NewEventTypeService(&mockEventTypeRepo{}, nil)

	require.NoError(t, svc.CreateEventType(&models.EventType{ID: typeID}))
	require.NoError(t, svc.UpdateEventType(&models.EventType{ID: typeID}))
	require.NoError(t, svc.DeleteEventType(typeID))
}
