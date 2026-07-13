package guests

import (
	"context"
	"errors"
	"testing"
	"time"

	"events-stocks/models"
	"events-stocks/services/ports"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type guestStatusRepoMock struct {
	items []models.GuestStatus

	createCalled bool
	updateCalled bool
	deleteCalled bool
}

func (m *guestStatusRepoMock) CreateGuestStatus(_ *models.GuestStatus) error {
	m.createCalled = true
	return nil
}

func (m *guestStatusRepoMock) UpdateGuestStatus(_ *models.GuestStatus) error {
	m.updateCalled = true
	return nil
}

func (m *guestStatusRepoMock) DeleteGuestStatus(_ uuid.UUID) error {
	m.deleteCalled = true
	return nil
}

func (m *guestStatusRepoMock) GetGuestStatusByID(id uuid.UUID) (*models.GuestStatus, error) {
	for _, item := range m.items {
		if item.ID == id {
			return &item, nil
		}
	}
	return nil, nil
}

func (m *guestStatusRepoMock) ListGuestStatuss() ([]models.GuestStatus, error) {
	return m.items, nil
}

var _ ports.GuestStatusRepository = (*guestStatusRepoMock)(nil)

func TestGuestStatusServiceListUsesCatalogCacheKeyAndTTL(t *testing.T) {
	statusID := uuid.Must(uuid.NewV4())
	repo := &guestStatusRepoMock{
		items: []models.GuestStatus{
			{ID: statusID, Code: "confirmed", Label: "Confirmado", Color: "green"},
		},
	}
	var getKey string
	var saveKey string
	var savedTTL time.Duration
	cache := &mockCacheRepo{
		GetKeyFunc: func(_ context.Context, key string) (string, error) {
			getKey = key
			return "", errors.New("cache miss")
		},
		SaveKeyFunc: func(_ context.Context, key string, _ string, ttl time.Duration) error {
			saveKey = key
			savedTTL = ttl
			return nil
		},
	}

	statuses, err := NewGuestStatusService(repo, cache).ListGuestStatuss()

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Equal(t, "confirmed", statuses[0].Code)
	assert.Equal(t, "all:"+utils.RedisGuestStatussKey, getKey)
	assert.Equal(t, "all:"+utils.RedisGuestStatussKey, saveKey)
	assert.Equal(t, utils.CacheTTLs[utils.RedisGuestStatussKey], savedTTL)
}

func TestGuestStatusServiceMutationsInvalidateCatalogCache(t *testing.T) {
	for _, tt := range []struct {
		name   string
		action func(*GuestStatusService) error
	}{
		{
			name: "create",
			action: func(svc *GuestStatusService) error {
				return svc.CreateGuestStatus(&models.GuestStatus{Code: "pending"})
			},
		},
		{
			name: "update",
			action: func(svc *GuestStatusService) error {
				return svc.UpdateGuestStatus(&models.GuestStatus{ID: uuid.Must(uuid.NewV4()), Code: "confirmed"})
			},
		},
		{
			name: "delete",
			action: func(svc *GuestStatusService) error {
				return svc.DeleteGuestStatus(uuid.Must(uuid.NewV4()))
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var invalidatedResource string
			var invalidatedKey string
			cache := &mockCacheRepo{
				InvalidateFunc: func(resource string, key string) error {
					invalidatedResource = resource
					invalidatedKey = key
					return nil
				},
			}

			err := tt.action(NewGuestStatusService(&guestStatusRepoMock{}, cache))

			require.NoError(t, err)
			assert.Equal(t, utils.RedisGuestStatussKey, invalidatedResource)
			assert.Equal(t, "all", invalidatedKey)
		})
	}
}

func TestGuestStatusServiceMutationsWorkWithoutCache(t *testing.T) {
	repo := &guestStatusRepoMock{}
	svc := NewGuestStatusService(repo, nil)

	require.NoError(t, svc.CreateGuestStatus(&models.GuestStatus{Code: "pending"}))
	require.NoError(t, svc.UpdateGuestStatus(&models.GuestStatus{ID: uuid.Must(uuid.NewV4()), Code: "confirmed"}))
	require.NoError(t, svc.DeleteGuestStatus(uuid.Must(uuid.NewV4())))
	assert.True(t, repo.createCalled)
	assert.True(t, repo.updateCalled)
	assert.True(t, repo.deleteCalled)
}
