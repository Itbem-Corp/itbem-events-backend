package events

import (
	"events-stocks/dtos"
	"events-stocks/models"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardCalendarDaysUntilUsesEventCalendarDay(t *testing.T) {
	now := time.Date(2026, time.July, 11, 4, 30, 0, 0, time.UTC) // July 10, 22:30 in Mexico City
	eventTime := time.Date(2026, time.July, 11, 5, 0, 0, 0, time.UTC)

	assert.Equal(t, 0, dashboardCalendarDaysUntil(eventTime, "America/Mexico_City", now))
	assert.Equal(t, 1, dashboardCalendarDaysUntil(eventTime.Add(24*time.Hour), "America/Mexico_City", now))
}

func TestListEventNotificationsFallbackKeepsOnlyCalendarWindow(t *testing.T) {
	now := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	repo := &mockEventsRepo{GetAllEventsDashboardFunc: func() ([]models.Event, error) {
		return []models.Event{
			{Name: "Past edge", EventDateTime: now.AddDate(0, 0, -3), Timezone: "UTC"},
			{Name: "Today", EventDateTime: now, Timezone: "UTC"},
			{Name: "Future edge", EventDateTime: now.AddDate(0, 0, 3), Timezone: "UTC"},
			{Name: "Too old", EventDateTime: now.AddDate(0, 0, -4), Timezone: "UTC"},
			{Name: "Too far", EventDateTime: now.AddDate(0, 0, 4), Timezone: "UTC"},
		}, nil
	}}

	result, err := NewEventService(repo, nil).ListEventNotifications(nil, nil, now)
	assert.NoError(t, err)
	assert.Equal(t, []string{"Past edge", "Today", "Future edge"}, []string{result[0].Name, result[1].Name, result[2].Name})
}

// Adapters without the optimized projections must still list the
// caller-access portfolio through the request application, never through the
// platform list.
func TestCallerAccessFallbacksPassTheRequestApplication(t *testing.T) {
	now := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	access := dtos.EventCallerAccess{UserID: uuid.Must(uuid.NewV4()), ApplicationID: uuid.Must(uuid.NewV4())}
	var received []dtos.EventCallerAccess
	repo := &mockEventsRepo{
		GetEventsForUserFunc: func(got dtos.EventCallerAccess) ([]models.Event, error) {
			received = append(received, got)
			return []models.Event{{Name: "Allowed", EventDateTime: now, Timezone: "UTC", IsActive: true}}, nil
		},
		GetAllEventsDashboardFunc: func() ([]models.Event, error) {
			t.Fatal("caller-access fallback must not read the platform portfolio")
			return nil, nil
		},
	}
	service := NewEventService(repo, nil)

	overview, err := service.GetDashboardOverview(nil, &access, now)
	require.NoError(t, err)
	assert.Equal(t, 1, overview.Metrics.Total)
	_, total, _, err := service.ListEventPage(nil, &access, dtos.EventListQuery{Page: 1, PageSize: 10, Filter: "all", Now: now})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	notifications, err := service.ListEventNotifications(nil, &access, now)
	require.NoError(t, err)
	assert.Len(t, notifications, 1)
	events, err := service.ListEventsForUser(access)
	require.NoError(t, err)
	assert.Len(t, events, 1)

	assert.Equal(t, []dtos.EventCallerAccess{access, access, access, access}, received)
}
