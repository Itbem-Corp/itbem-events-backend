package dtos

import (
	"encoding/json"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventConfigPatch_RSVPDeadlineSetAndClear(t *testing.T) {
	cfg := &models.EventConfig{}
	require.NoError(t, EventConfigPatch{"rsvp_deadline": json.RawMessage(`"2026-11-20T23:59:00-06:00"`)}.ApplyTo(cfg))
	require.NotNil(t, cfg.RSVPDeadline)
	assert.True(t, cfg.RSVPDeadline.Equal(time.Date(2026, 11, 21, 5, 59, 0, 0, time.UTC)))

	require.NoError(t, EventConfigPatch{"rsvp_deadline": json.RawMessage(`null`)}.ApplyTo(cfg))
	assert.Nil(t, cfg.RSVPDeadline)
}

func TestEventConfigPatch_RSVPDeadlineAcceptsAliasesAndRejectsGarbage(t *testing.T) {
	for _, key := range []string{"rsvpDeadline", "RSVPDeadline", "RsvpDeadline"} {
		cfg := &models.EventConfig{}
		require.NoError(t, EventConfigPatch{key: json.RawMessage(`"2026-11-20T23:59:00Z"`)}.ApplyTo(cfg), key)
		require.NotNil(t, cfg.RSVPDeadline, key)
	}
	err := EventConfigPatch{"rsvp_deadline": json.RawMessage(`"mañana"`)}.ApplyTo(&models.EventConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rsvp_deadline")
}

func TestEventConfigResponse_RSVPDeadlineIsNullOrRFC3339(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	raw, err := json.Marshal(NewEventConfigResponse(&models.EventConfig{ID: eventID}, eventID))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"rsvp_deadline":null`)

	deadline := time.Date(2026, 11, 21, 5, 59, 0, 0, time.UTC)
	raw, err = json.Marshal(NewEventConfigResponse(&models.EventConfig{ID: eventID, RSVPDeadline: &deadline}, eventID))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"rsvp_deadline":"2026-11-21T05:59:00Z"`)
}

func TestEventConfig_RSVPDeadlinePassed(t *testing.T) {
	deadline := time.Date(2026, 11, 21, 5, 59, 0, 0, time.UTC)
	cfg := &models.EventConfig{RSVPDeadline: &deadline}
	assert.False(t, cfg.RSVPDeadlinePassed(deadline.Add(-time.Second)))
	assert.False(t, cfg.RSVPDeadlinePassed(deadline), "the deadline instant itself is still open")
	assert.True(t, cfg.RSVPDeadlinePassed(deadline.Add(time.Second)))
	assert.False(t, (&models.EventConfig{}).RSVPDeadlinePassed(deadline))
}
