package events

import (
	"encoding/json"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rsvpSpec(t *testing.T, cfg *models.EventConfig, now time.Time) (map[string]any, map[string]any) {
	t.Helper()
	sections := []models.EventSection{{
		ID: testSectionID1, EventID: testEventID, ComponentType: "RSVPConfirmation", Config: `{"title":"Confirma"}`, Order: 1, IsVisible: true,
	}}
	spec, err := buildPageSpecFromEventWithConfig(stubEvent(""), buildSpecDeps{
		getSections: func(uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		now:         now,
	}, cfg)
	require.NoError(t, err)
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	meta := payload["meta"].(map[string]any)
	var section map[string]any
	if list := payload["sections"].([]any); len(list) > 0 {
		section = list[0].(map[string]any)["config"].(map[string]any)
	}
	return meta, section
}

func TestBuildPageSpec_ExposesOpenRSVPWithoutDeadline(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	meta, section := rsvpSpec(t, &models.EventConfig{IsPublic: true, ShowRSVPSection: true}, now)

	assert.Equal(t, true, meta["rsvp_open"])
	assert.Contains(t, meta, "rsvp_deadline")
	assert.Nil(t, meta["rsvp_deadline"])
	require.NotNil(t, section)
	assert.Equal(t, true, section["rsvp_open"])
	assert.Nil(t, section["rsvp_deadline"])
	assert.Equal(t, "Confirma", section["title"], "authored config is preserved")
}

func TestBuildPageSpec_ExposesClosedRSVPAfterDeadline(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	deadline := time.Date(2026, 10, 31, 23, 59, 0, 0, time.FixedZone("CST", -6*3600))
	meta, section := rsvpSpec(t, &models.EventConfig{IsPublic: true, ShowRSVPSection: true, RSVPDeadline: &deadline}, now)

	assert.Equal(t, false, meta["rsvp_open"])
	assert.Equal(t, "2026-11-01T05:59:00Z", meta["rsvp_deadline"])
	assert.Equal(t, false, section["rsvp_open"])
	assert.Equal(t, "2026-11-01T05:59:00Z", section["rsvp_deadline"])
}

func TestBuildPageSpec_RSVPOpenUntilDeadline(t *testing.T) {
	deadline := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	meta, _ := rsvpSpec(t, &models.EventConfig{IsPublic: true, ShowRSVPSection: true, RSVPDeadline: &deadline}, deadline.Add(-time.Second))
	assert.Equal(t, true, meta["rsvp_open"])
}

func TestRSVPOpen_HiddenBlockClosesRSVPButLegacyDefaultsKeepItOpen(t *testing.T) {
	now := time.Now()
	assert.False(t, RSVPOpen(&models.EventConfig{VisibilityConfigured: true, ShowHeader: true}, now))
	assert.True(t, RSVPOpen(&models.EventConfig{}, now), "legacy all-false config gets visibility defaults")
	assert.True(t, RSVPOpen(nil, now))
}
