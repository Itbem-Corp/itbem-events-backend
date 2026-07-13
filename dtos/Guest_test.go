package dtos

import (
	"encoding/json"
	"events-stocks/models"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGuestResponseUsesExplicitDashboardContract(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	tableID := uuid.Must(uuid.NewV4())
	statusID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)
	rsvpAt := createdAt.Add(30 * time.Minute)

	body := NewGuestResponse(&models.Guest{
		ID:                  guestID,
		EventID:             eventID,
		TableID:             &tableID,
		Table:               &models.EventTable{ID: tableID, EventID: eventID, Name: "Mesa VIP", Capacity: 8, SortOrder: 1, CreatedAt: createdAt, UpdatedAt: updatedAt},
		TableNumber:         "7",
		InvitationID:        &invitationID,
		PrettyToken:         "ABC123",
		FirstName:           "Ana",
		LastName:            "Garcia",
		Nickname:            "Anita",
		Email:               "ana@example.com",
		Phone:               "+525511111111",
		ShowContactInfo:     true,
		Role:                "guest",
		Bio:                 "Bio publica",
		Order:               3,
		ImageURL:            "profile.webp",
		Image1URL:           "one.webp",
		Image2URL:           "two.webp",
		Image3URL:           "three.webp",
		Headline:            "Titular",
		Signature:           "Firma",
		GuestStatusID:       statusID,
		GuestStatus:         models.GuestStatus{ID: statusID, Code: "confirmed", Label: "Confirmado", Color: "green", CreatedAt: createdAt, UpdatedAt: updatedAt},
		GuestsCount:         4,
		IsHost:              true,
		MaxGuests:           5,
		CreatedAt:           createdAt,
		UpdatedAt:           updatedAt,
		RSVPStatus:          "confirmed",
		RSVPAt:              &rsvpAt,
		RSVPMethod:          "web",
		RSVPTokenID:         &tokenID,
		RSVPGuestCount:      4,
		DietaryRestrictions: "Sin nueces",
		RSVPNotes:           "Mesa cerca de la pista",
		Notes:               "Prefiere asiento cerca de familia",
	})

	require.NotNil(t, body.Table)
	require.NotNil(t, body.GuestStatus)
	require.NotNil(t, body.Status)

	assert.Equal(t, statusID, body.GuestStatusID)
	assert.Equal(t, statusID, body.StatusID)
	assert.Equal(t, body.GuestStatus, body.Status)
	assert.Equal(t, "one.webp", body.Image1URL)
	assert.Equal(t, "one.webp", body.Image1AliasURL)
	assert.Equal(t, "two.webp", body.Image2AliasURL)
	assert.Equal(t, "three.webp", body.Image3AliasURL)
	assert.Equal(t, "Mesa VIP", body.Table.Name)

	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	assert.Contains(t, string(encoded), `"guest_status_id"`)
	assert.Contains(t, string(encoded), `"status_id"`)
	assert.Contains(t, string(encoded), `"guest_status"`)
	assert.Contains(t, string(encoded), `"status"`)
	assert.Contains(t, string(encoded), `"image1_url"`)
	assert.Contains(t, string(encoded), `"image_1_url"`)
	assert.Contains(t, string(encoded), `"table"`)
	assert.Contains(t, string(encoded), `"rsvp_notes":"Mesa cerca de la pista"`)
	assert.Contains(t, string(encoded), `"notes":"Prefiere asiento cerca de familia"`)
	assert.NotContains(t, string(encoded), "DeletedAt")
	assert.NotContains(t, string(encoded), "deleted_at")
	assert.NotContains(t, string(encoded), `"event":`)
	assert.NotContains(t, string(encoded), `"invitation":`)
}

func TestNewGuestResponsePrefersIncomingStatusID(t *testing.T) {
	guestStatusID := uuid.Must(uuid.NewV4())
	incomingStatusID := uuid.Must(uuid.NewV4())

	body := NewGuestResponse(&models.Guest{
		GuestStatusID: guestStatusID,
		StatusID:      incomingStatusID,
	})

	assert.Equal(t, incomingStatusID, body.GuestStatusID)
	assert.Equal(t, incomingStatusID, body.StatusID)
}

func TestNewGuestResponsesReturnsEmptyArray(t *testing.T) {
	assert.Empty(t, NewGuestResponses(nil))
}
