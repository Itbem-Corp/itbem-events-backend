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

type mockEventAnalyticsDeltaRepo struct {
	adjustCalls int
	getCalls    int
	createCalls int
	updateCalls int

	eventID uuid.UUID
	field   string
	delta   int
	err     error

	listResponse []models.EventAnalytics
}

func (m *mockEventAnalyticsDeltaRepo) AdjustEventAnalytics(eventID uuid.UUID, field string, delta int) error {
	m.adjustCalls++
	m.eventID = eventID
	m.field = field
	m.delta = delta
	return m.err
}

func (m *mockEventAnalyticsDeltaRepo) CreateEventAnalytics(a *models.EventAnalytics) error {
	m.createCalls++
	return nil
}

func (m *mockEventAnalyticsDeltaRepo) UpdateEventAnalytics(a *models.EventAnalytics) error {
	m.updateCalls++
	return nil
}

func (m *mockEventAnalyticsDeltaRepo) DeleteEventAnalytics(id uuid.UUID) error { return nil }

func (m *mockEventAnalyticsDeltaRepo) GetEventAnalyticsByID(id uuid.UUID) (*models.EventAnalytics, error) {
	return nil, errors.New("not implemented")
}

func (m *mockEventAnalyticsDeltaRepo) GetEventAnalyticsByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	m.getCalls++
	return nil, errors.New("not found")
}

func (m *mockEventAnalyticsDeltaRepo) ListEventAnalyticss() ([]models.EventAnalytics, error) {
	if m.listResponse != nil {
		return m.listResponse, nil
	}
	return nil, nil
}

func TestAdjustAnalyticsUsesAtomicDeltaRepository(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	repo := &mockEventAnalyticsDeltaRepo{}
	var invalidated []string
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource string, key string) error {
			invalidated = append(invalidated, resource+":"+key)
			return nil
		},
	}

	svc := NewEventAnalyticsService(repo, cache)
	svc.AdjustAnalytics(eventID, "views", 3)

	require.Equal(t, 1, repo.adjustCalls)
	assert.Equal(t, eventID, repo.eventID)
	assert.Equal(t, "views", repo.field)
	assert.Equal(t, 3, repo.delta)
	assert.Zero(t, repo.getCalls)
	assert.Zero(t, repo.createCalls)
	assert.Zero(t, repo.updateCalls)
	assert.Equal(t, []string{utils.RedisEventAnalyticsKey + ":all"}, invalidated)
}

func TestEventAnalyticsService_ListEventAnalyticss_UsesNamedCacheKeyAndTTL(t *testing.T) {
	analyticsID := uuid.Must(uuid.NewV4())
	repo := &mockEventAnalyticsDeltaRepo{
		listResponse: []models.EventAnalytics{{ID: analyticsID, Views: 5}},
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

	result, err := NewEventAnalyticsService(repo, cache).ListEventAnalyticss()

	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, analyticsID, result[0].ID)
	assert.Equal(t, "all:"+utils.RedisEventAnalyticsKey, getKey)
	assert.Equal(t, "all:"+utils.RedisEventAnalyticsKey, saveKey)
	assert.Equal(t, utils.CacheTTLs[utils.RedisEventAnalyticsKey], savedTTL)
}

func TestApplyDeltaSupportsMomentComments(t *testing.T) {
	analytics := &models.EventAnalytics{MomentComments: 1}

	applyDelta(analytics, "moment_comments", 3)
	assert.Equal(t, 4, analytics.MomentComments)

	applyDelta(analytics, "moment_comments", -10)
	assert.Zero(t, analytics.MomentComments)
}

func TestEventAnalyticsServiceAllowsNilCache(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	repo := &mockEventAnalyticsDeltaRepo{}
	svc := NewEventAnalyticsService(repo, nil)

	var err error
	assert.NotPanics(t, func() {
		_, err = svc.ListEventAnalyticss()
	})
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		err = svc.CreateEventAnalytics(&models.EventAnalytics{EventID: eventID})
	})
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		err = svc.UpdateEventAnalytics(&models.EventAnalytics{EventID: eventID})
	})
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		err = svc.DeleteEventAnalytics(eventID)
	})
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		svc.AdjustAnalytics(eventID, "views", 1)
	})
	assert.Equal(t, 1, repo.adjustCalls)
}
