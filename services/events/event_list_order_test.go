package events

import (
	"testing"
	"time"

	"events-stocks/dtos"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// EVT-15: the in-memory fallback must order like the SQL page.
func TestListEventPageFallbackOrdersUpcomingSoonestFirst(t *testing.T) {
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	soon := models.Event{ID: uuid.Must(uuid.FromString("00000000-0000-0000-0000-000000000002")), Name: "soon", EventDateTime: now.AddDate(0, 0, 3)}
	tieLow := models.Event{ID: uuid.Must(uuid.FromString("00000000-0000-0000-0000-000000000001")), Name: "tie-low", EventDateTime: now.AddDate(0, 1, 0)}
	tieHigh := models.Event{ID: uuid.Must(uuid.FromString("00000000-0000-0000-0000-000000000003")), Name: "tie-high", EventDateTime: now.AddDate(0, 1, 0)}
	far := models.Event{ID: uuid.Must(uuid.NewV4()), Name: "far", EventDateTime: now.AddDate(1, 0, 0)}
	past := models.Event{ID: uuid.Must(uuid.NewV4()), Name: "past", EventDateTime: now.AddDate(0, -1, 0)}
	older := models.Event{ID: uuid.Must(uuid.NewV4()), Name: "older", EventDateTime: now.AddDate(-1, 0, 0)}

	clientID := uuid.Must(uuid.NewV4())
	svc := NewEventService(&mockEventsRepo{
		GetEventsByClientIDFunc: func(uuid.UUID) ([]models.Event, error) {
			return []models.Event{far, older, tieHigh, past, soon, tieLow}, nil
		},
	}, nil)

	names := func(events []models.Event) []string {
		out := make([]string, 0, len(events))
		for _, event := range events {
			out = append(out, event.Name)
		}
		return out
	}

	upcoming, _, _, err := svc.ListEventPage(&clientID, nil, dtos.EventListQuery{Page: 1, PageSize: 10, Filter: "upcoming", Now: now})
	require.NoError(t, err)
	assert.Equal(t, []string{"soon", "tie-low", "tie-high", "far"}, names(upcoming))

	pastPage, _, _, err := svc.ListEventPage(&clientID, nil, dtos.EventListQuery{Page: 1, PageSize: 10, Filter: "past", Now: now})
	require.NoError(t, err)
	assert.Equal(t, []string{"past", "older"}, names(pastPage))

	all, _, _, err := svc.ListEventPage(&clientID, nil, dtos.EventListQuery{Page: 1, PageSize: 10, Filter: "all", Now: now})
	require.NoError(t, err)
	assert.Equal(t, []string{"far", "tie-high", "tie-low", "soon", "past", "older"}, names(all))
}
