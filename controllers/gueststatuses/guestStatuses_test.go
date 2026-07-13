package gueststatuses

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"events-stocks/models"
	guestService "events-stocks/services/guests"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockGuestStatusRepo struct {
	items []models.GuestStatus
}

func (m *mockGuestStatusRepo) CreateGuestStatus(_ *models.GuestStatus) error { return nil }
func (m *mockGuestStatusRepo) UpdateGuestStatus(_ *models.GuestStatus) error { return nil }
func (m *mockGuestStatusRepo) DeleteGuestStatus(_ uuid.UUID) error           { return nil }
func (m *mockGuestStatusRepo) GetGuestStatusByID(id uuid.UUID) (*models.GuestStatus, error) {
	for _, item := range m.items {
		if item.ID == id {
			return &item, nil
		}
	}
	return nil, nil
}
func (m *mockGuestStatusRepo) ListGuestStatuss() ([]models.GuestStatus, error) {
	return m.items, nil
}

func TestListGuestStatuses_ReturnsAPIEnvelope(t *testing.T) {
	statusID := uuid.Must(uuid.NewV4())
	guestService.SetDefaultGuestStatusService(guestService.NewGuestStatusService(&mockGuestStatusRepo{
		items: []models.GuestStatus{
			{ID: statusID, Code: "pending", Label: "Pendiente", Color: "amber"},
		},
	}, nil))

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/catalogs/guest-statuses", nil)
	rec := httptest.NewRecorder()

	require.NoError(t, ListGuestStatuses(e.NewContext(req, rec)))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, float64(http.StatusOK), body["status"])
	assert.Equal(t, "Guest statuses loaded", body["message"])

	data, ok := body["data"].([]interface{})
	require.True(t, ok)
	require.Len(t, data, 1)
	assert.Equal(t, "pending", data[0].(map[string]interface{})["code"])
}
