package dtos

import (
	"encoding/json"
	"testing"

	"events-stocks/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateEventTimezone(t *testing.T) {
	for _, valid := range []string{"", "  ", "UTC", "America/Mexico_City", "America/Tijuana", "Europe/Madrid"} {
		assert.NoError(t, ValidateEventTimezone(valid), valid)
	}
	for _, invalid := range []string{"Foo/Bar", "Mars/Base", "Local", "local", "../etc/passwd", "America/Mexico City"} {
		assert.ErrorIs(t, ValidateEventTimezone(invalid), ErrInvalidEventTimezone, invalid)
	}
}

// EVT-3 / SET-16: an invalid timezone used to be stored and then broke every
// list query of the organization.
func TestEventPayloadApplyToRejectsInvalidTimezone(t *testing.T) {
	var payload EventPayload
	require.NoError(t, json.Unmarshal([]byte(`{"timezone":"Foo/Bar"}`), &payload))
	event := models.Event{Timezone: "America/Mexico_City"}

	err := payload.ApplyTo(&event)
	assert.ErrorIs(t, err, ErrInvalidEventTimezone)
	assert.Equal(t, "America/Mexico_City", event.Timezone, "the stored value must not change")
}
