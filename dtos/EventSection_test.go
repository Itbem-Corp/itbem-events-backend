package dtos

import (
	"encoding/json"
	"events-stocks/models"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestApplyEventSectionPayloadSupportsDashboardAliases(t *testing.T) {
	order := 3
	visible := false
	section := models.EventSection{}

	err := ApplyEventSectionPayload(&section, EventSectionPayload{
		Key:         " hero ",
		Name:        "Portada",
		Type:        "LegacyHero",
		Config:      json.RawMessage(`{"title":"Hola"}`),
		Order:       &order,
		IsVisible:   &visible,
		ContentJSON: nil,
	}, true)

	require.NoError(t, err)
	assert.Equal(t, "hero", section.Key)
	assert.Equal(t, "Portada", section.Title)
	assert.Equal(t, "LegacyHero", section.ComponentType)
	assert.JSONEq(t, `{"title":"Hola"}`, section.Config)
	assert.Equal(t, 3, section.Order)
	assert.False(t, section.IsVisible)
}

func TestApplyEventSectionPayloadAcceptsEncodedJSONAndContentAlias(t *testing.T) {
	section := models.EventSection{}

	err := ApplyEventSectionPayload(&section, EventSectionPayload{
		Title:         "Galeria",
		ComponentType: "LegacyGallery",
		ContentJSON:   json.RawMessage(`"{\"subtitle\":\"Fotos\"}"`),
	}, true)

	require.NoError(t, err)
	assert.Equal(t, "Galeria", section.Title)
	assert.Equal(t, "LegacyGallery", section.ComponentType)
	assert.JSONEq(t, `{"subtitle":"Fotos"}`, section.Config)
	assert.True(t, section.IsVisible)
}

func TestApplyEventSectionPayloadAcceptsTypeScriptAliases(t *testing.T) {
	raw := []byte(`{
		"key":" agenda ",
		"sectionTitle":"Programa",
		"componentType":"AgendaSection",
		"contentJson":{"items":[]},
		"sortOrder":4,
		"isVisible":false
	}`)
	var payload EventSectionPayload
	require.NoError(t, json.Unmarshal(raw, &payload))

	section := models.EventSection{}
	err := ApplyEventSectionPayload(&section, payload, true)

	require.NoError(t, err)
	assert.Equal(t, "agenda", section.Key)
	assert.Equal(t, "Programa", section.Title)
	assert.Equal(t, "AgendaSection", section.ComponentType)
	assert.JSONEq(t, `{"items":[]}`, section.Config)
	assert.Equal(t, 4, section.Order)
	assert.False(t, section.IsVisible)
}

func TestApplyEventSectionPayloadAcceptsPascalAliases(t *testing.T) {
	raw := []byte(`{
		"Key":" rsvp ",
		"SectionTitle":"Confirmacion",
		"ComponentType":"RSVPConfirmation",
		"ContentJSON":{"welcome_message":"Hola"},
		"SortOrder":5,
		"IsVisible":false
	}`)
	var payload EventSectionPayload
	require.NoError(t, json.Unmarshal(raw, &payload))

	section := models.EventSection{}
	err := ApplyEventSectionPayload(&section, payload, true)

	require.NoError(t, err)
	assert.Equal(t, "rsvp", section.Key)
	assert.Equal(t, "Confirmacion", section.Title)
	assert.Equal(t, "RSVPConfirmation", section.ComponentType)
	assert.JSONEq(t, `{"welcome_message":"Hola"}`, section.Config)
	assert.Equal(t, 5, section.Order)
	assert.False(t, section.IsVisible)
}

func TestApplyEventSectionPayloadRejectsInvalidConfig(t *testing.T) {
	err := ApplyEventSectionPayload(&models.EventSection{}, EventSectionPayload{
		Config: json.RawMessage(`"{bad json}"`),
	}, true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "valid JSON")
}

func TestNewEventSectionResponseReturnsObjectConfigAndHidesRelations(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.July, 7, 12, 0, 0, 0, time.UTC)

	body := NewEventSectionResponse(models.EventSection{
		ID:            sectionID,
		EventID:       eventID,
		Event:         models.Event{ID: eventID, Name: "Should not leak"},
		Key:           "venue",
		Title:         "Lugar",
		ComponentType: "EventVenue",
		Config:        `{"venueText":"Salon"}`,
		Order:         2,
		IsVisible:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
		DeletedAt:     gorm.DeletedAt{Time: now, Valid: true},
	})

	assert.Equal(t, sectionID, body.ID)
	assert.Equal(t, "Lugar", body.Name)
	assert.Equal(t, "Lugar", body.Title)
	assert.Equal(t, "EventVenue", body.Type)
	assert.JSONEq(t, `{"venueText":"Salon"}`, string(body.Config))
	assert.JSONEq(t, `{"venueText":"Salon"}`, string(body.ContentJSON))

	raw, err := json.Marshal(body)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "deleted_at")
	assert.NotContains(t, string(raw), "event\":{\"")
}

func TestNewEventSectionResponseUnwrapsLegacyEncodedStringConfig(t *testing.T) {
	body := NewEventSectionResponse(models.EventSection{
		Title:         "Mapa",
		ComponentType: "MAP",
		Config:        `"{\"title\":\"Ubicacion\",\"mapUrl\":\"https://maps.example.com\"}"`,
	})

	assert.JSONEq(t, `{"title":"Ubicacion","mapUrl":"https://maps.example.com"}`, string(body.Config))
	assert.JSONEq(t, `{"title":"Ubicacion","mapUrl":"https://maps.example.com"}`, string(body.ContentJSON))

	raw, err := json.Marshal(body)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"config":{"title":"Ubicacion","mapUrl":"https://maps.example.com"}`)
	assert.NotContains(t, string(raw), `"config":"`)
}

func TestNewEventSectionResponsesReturnsEmptyArray(t *testing.T) {
	assert.Empty(t, NewEventSectionResponses(nil))
}
