package events

import (
	"events-stocks/models"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
