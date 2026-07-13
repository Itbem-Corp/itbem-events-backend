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

func TestNewEventResponseUsesExplicitDashboardContract(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	eventTypeID := uuid.Must(uuid.NewV4())
	maxGuests := 120
	eventDate := time.Date(2026, time.August, 15, 20, 30, 0, 0, time.UTC)

	body := NewEventResponse(&models.Event{
		ID:                 eventID,
		ClientID:           &clientID,
		Client:             &models.Client{ID: clientID, Name: "ITBEM", Code: "itbem"},
		Name:               "Boda Demo",
		Identifier:         "boda-demo",
		Description:        "Celebra con nosotros",
		CoverImageURL:      "https://signed.example.com/cover.webp",
		CoverImageURL2:     "https://signed.example.com/cover-2.webp",
		CustomDomain:       "boda.example.com",
		Address:            "Salon Central",
		SecondAddress:      "Terraza",
		MusicUrl:           "https://cdn.example.com/song.mp3",
		EventDateTime:      eventDate,
		Timezone:           "America/Mexico_City",
		Language:           "es",
		EventTypeID:        eventTypeID,
		EventType:          models.EventType{ID: eventTypeID, Name: "wedding"},
		EventConfig:        models.EventConfig{ID: eventID, IsPublic: true, ShowHeader: true},
		OrganizerName:      "Ana y Luis",
		OrganizerEmail:     "hola@example.com",
		OrganizerPhone:     "+5215555555555",
		MaxGuests:          &maxGuests,
		AllowGuestAccess:   true,
		SlugLocked:         true,
		IsActive:           true,
		PendingMomentCount: 7,
		CreatedAt:          eventDate,
		UpdatedAt:          eventDate,
		DeletedAt:          gorm.DeletedAt{Time: eventDate, Valid: true},
	})

	assert.Equal(t, eventID, body.ID)
	assert.Equal(t, &clientID, body.ClientID)
	require.NotNil(t, body.Client)
	assert.Equal(t, "ITBEM", body.Client.Name)
	assert.Equal(t, "Boda Demo", body.Name)
	assert.Equal(t, "boda-demo", body.Identifier)
	assert.Equal(t, "https://signed.example.com/cover.webp", body.CoverImageURL)
	assert.Equal(t, eventDate, body.EventDateTime)
	assert.Equal(t, "https://cdn.example.com/song.mp3", body.MusicURL)
	assert.Equal(t, int64(7), body.PendingMomentCount)
	require.NotNil(t, body.EventType)
	assert.Equal(t, "wedding", body.EventType.Name)
	require.NotNil(t, body.EventConfig)
	require.NotNil(t, body.Config)
	assert.True(t, body.EventConfig.IsPublic)
	assert.True(t, body.Config.ShowHeader)

	raw, err := json.Marshal(body)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "deleted_at")
}

func TestEventPayloadApplyToNormalizesOptionalDashboardFields(t *testing.T) {
	var payload EventPayload
	err := json.Unmarshal([]byte(`{
		"name":" Demo ",
		"client_id":"",
		"event_type_id":"",
		"max_guests":null,
		"event_date_time":"",
		"music_url":" https://cdn.example.com/song.mp3 ",
		"is_active":true
	}`), &payload)
	require.NoError(t, err)

	clientID := uuid.Must(uuid.NewV4())
	eventTypeID := uuid.Must(uuid.NewV4())
	maxGuests := 100
	event := models.Event{
		ClientID:      &clientID,
		EventTypeID:   eventTypeID,
		MaxGuests:     &maxGuests,
		EventDateTime: time.Date(2026, time.August, 15, 20, 30, 0, 0, time.UTC),
	}

	require.NoError(t, payload.ApplyTo(&event))

	assert.Equal(t, "Demo", event.Name)
	assert.Nil(t, event.ClientID)
	assert.Equal(t, uuid.Nil, event.EventTypeID)
	assert.Nil(t, event.MaxGuests)
	assert.True(t, event.EventDateTime.IsZero())
	assert.Equal(t, "https://cdn.example.com/song.mp3", event.MusicUrl)
	assert.True(t, event.IsActive)
}

func TestEventPayloadApplyToAcceptsFrontendCasingAliases(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	eventTypeID := uuid.Must(uuid.NewV4())
	eventDate := "2026-08-15T20:30:00-06:00"
	var payload EventPayload
	err := json.Unmarshal([]byte(`{
		"clientId":"`+clientID.String()+`",
		"Name":" Boda Alias ",
		"slug":" boda-alias ",
		"Description":" Celebra con nosotros ",
		"customDomain":" boda.example.com ",
		"Address":" Jardin Central ",
		"secondAddress":" Terraza ",
		"musicUrl":" https://cdn.example.com/song.mp3 ",
		"eventDateTime":"`+eventDate+`",
		"timeZone":"America/Mexico_City",
		"Locale":"es",
		"eventTypeId":"`+eventTypeID.String()+`",
		"organizerName":" Ana y Luis ",
		"organizerEmail":" hola@example.com ",
		"organizerPhone":" +5215555555555 ",
		"maxGuests":"120",
		"allowGuestAccess":true,
		"slugLocked":true,
		"isActive":true
	}`), &payload)
	require.NoError(t, err)

	event := models.Event{}
	require.NoError(t, payload.ApplyTo(&event))

	require.NotNil(t, event.ClientID)
	assert.Equal(t, clientID, *event.ClientID)
	assert.Equal(t, "Boda Alias", event.Name)
	assert.Equal(t, "boda-alias", event.Identifier)
	assert.Equal(t, "Celebra con nosotros", event.Description)
	assert.Equal(t, "boda.example.com", event.CustomDomain)
	assert.Equal(t, "Jardin Central", event.Address)
	assert.Equal(t, "Terraza", event.SecondAddress)
	assert.Equal(t, "https://cdn.example.com/song.mp3", event.MusicUrl)
	assert.True(t, event.EventDateTime.Equal(time.Date(2026, time.August, 16, 2, 30, 0, 0, time.UTC)))
	assert.Equal(t, "America/Mexico_City", event.Timezone)
	assert.Equal(t, "es", event.Language)
	assert.Equal(t, eventTypeID, event.EventTypeID)
	assert.Equal(t, "Ana y Luis", event.OrganizerName)
	assert.Equal(t, "hola@example.com", event.OrganizerEmail)
	assert.Equal(t, "+5215555555555", event.OrganizerPhone)
	require.NotNil(t, event.MaxGuests)
	assert.Equal(t, 120, *event.MaxGuests)
	assert.True(t, event.AllowGuestAccess)
	assert.True(t, event.SlugLocked)
	assert.True(t, event.IsActive)
}

func TestEventPayloadCanonicalFieldsWinOverAliases(t *testing.T) {
	canonicalTypeID := uuid.Must(uuid.NewV4())
	aliasTypeID := uuid.Must(uuid.NewV4())
	var payload EventPayload
	err := json.Unmarshal([]byte(`{
		"identifier":"canonical",
		"slug":"alias",
		"event_date_time":"2026-08-15T20:30:00Z",
		"eventDateTime":"2027-08-15T20:30:00Z",
		"event_type_id":"`+canonicalTypeID.String()+`",
		"eventTypeId":"`+aliasTypeID.String()+`"
	}`), &payload)
	require.NoError(t, err)

	event := models.Event{}
	require.NoError(t, payload.ApplyTo(&event))

	assert.Equal(t, "canonical", event.Identifier)
	assert.Equal(t, time.Date(2026, time.August, 15, 20, 30, 0, 0, time.UTC), event.EventDateTime)
	assert.Equal(t, canonicalTypeID, event.EventTypeID)
}

func TestEventPayloadApplyToKeepsAbsentFields(t *testing.T) {
	var payload EventPayload
	err := json.Unmarshal([]byte(`{"name":"Actualizado"}`), &payload)
	require.NoError(t, err)

	clientID := uuid.Must(uuid.NewV4())
	eventTypeID := uuid.Must(uuid.NewV4())
	maxGuests := 100
	event := models.Event{
		ClientID:    &clientID,
		EventTypeID: eventTypeID,
		MaxGuests:   &maxGuests,
	}

	require.NoError(t, payload.ApplyTo(&event))

	assert.Equal(t, "Actualizado", event.Name)
	require.NotNil(t, event.ClientID)
	assert.Equal(t, clientID, *event.ClientID)
	assert.Equal(t, eventTypeID, event.EventTypeID)
	require.NotNil(t, event.MaxGuests)
	assert.Equal(t, 100, *event.MaxGuests)
}

func TestEventPayloadApplyToTreatsNilClientUUIDAsUnset(t *testing.T) {
	var payload EventPayload
	err := json.Unmarshal([]byte(`{"client_id":" 00000000-0000-0000-0000-000000000000 "}`), &payload)
	require.NoError(t, err)

	clientID := uuid.Must(uuid.NewV4())
	event := models.Event{ClientID: &clientID}

	require.NoError(t, payload.ApplyTo(&event))
	assert.Nil(t, event.ClientID)
}

func TestEventPayloadApplyToRejectsInvalidUUID(t *testing.T) {
	var payload EventPayload
	err := json.Unmarshal([]byte(`{"event_type_id":"not-a-uuid"}`), &payload)
	require.NoError(t, err)

	err = payload.ApplyTo(&models.Event{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event_type_id")
}

func TestNewEventResponseOmitsUnloadedRelations(t *testing.T) {
	body := NewEventResponse(&models.Event{Name: "Solo Evento"})

	assert.Equal(t, "Solo Evento", body.Name)
	assert.Nil(t, body.Client)
	assert.Nil(t, body.EventType)
	assert.Nil(t, body.EventConfig)
	assert.Nil(t, body.Config)
}

func TestNewEventResponsesReturnsEmptyArray(t *testing.T) {
	assert.Empty(t, NewEventResponses(nil))
}
