package events

import (
	"net/http"
	"testing"
	"time"

	"events-stocks/models"
	eventsService "events-stocks/services/events"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetEventMeta_ExposesRSVPDeadlineAndOpenFlag(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
	})
	eventID := uuid.Must(uuid.NewV4())
	deadline := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{ID: eventID, Name: "Boda", Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: true, RSVPDeadline: &deadline},
	}, &mockCacheRepo{})

	c, rec := newEchoCtx(http.MethodGet, "/api/events/boda/meta", "")
	c.SetParamNames("identifier")
	c.SetParamValues("boda")
	require.NoError(t, GetEventMeta(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"rsvp_open":false`)
	assert.Contains(t, rec.Body.String(), `"rsvp_deadline":"`+deadline.Format(time.RFC3339)+`"`)
}
