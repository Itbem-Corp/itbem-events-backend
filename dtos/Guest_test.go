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

// checkin:run is granted to door-only roles, so the check-in projection must
// carry exactly what the dashboard check-in page renders: name, table, party
// size and RSVP state. Contact data, notes and every token stay out.
func TestNewCheckinGuestResponsesCarryOnlyDoorFields(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	tableID := uuid.Must(uuid.NewV4())
	statusID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	rsvpAt := time.Date(2026, 7, 7, 18, 30, 0, 0, time.UTC)

	body := NewCheckinGuestResponses([]models.Guest{{
		ID:                  guestID,
		EventID:             eventID,
		TableID:             &tableID,
		Table:               &models.EventTable{ID: tableID, EventID: eventID, Name: "Mesa VIP", Capacity: 8},
		TableNumber:         "7",
		InvitationID:        &invitationID,
		PrettyToken:         "PRETTY42",
		FirstName:           "Ana",
		LastName:            "Garcia",
		Nickname:            "Anita",
		Email:               "ana.door@example.com",
		Phone:               "+525500000042",
		ShowContactInfo:     true,
		Role:                "guest",
		Bio:                 "Bio privada",
		ImageURL:            "profile.webp",
		Headline:            "Titular",
		Signature:           "Firma",
		GuestStatusID:       statusID,
		GuestStatus:         models.GuestStatus{ID: statusID, Code: "confirmed", Label: "Confirmado"},
		GuestsCount:         3,
		IsHost:              true,
		MaxGuests:           5,
		RSVPStatus:          "confirmed",
		RSVPAt:              &rsvpAt,
		RSVPMethod:          "web",
		RSVPTokenID:         &tokenID,
		RSVPGuestCount:      3,
		DietaryRestrictions: "Sin nueces",
		RSVPNotes:           "Nota RSVP privada",
		Notes:               "Nota interna privada",
	}})
	require.Len(t, body, 1)

	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	var decoded []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	keys := make([]string, 0, len(decoded[0]))
	for key := range decoded[0] {
		keys = append(keys, key)
	}
	assert.ElementsMatch(t, []string{
		"id", "event_id", "first_name", "last_name", "table_id", "table", "table_number",
		"guest_status_id", "guest_status", "status_id", "status",
		"guests_count", "rsvp_status", "rsvp_guest_count",
	}, keys)

	guest := body[0]
	assert.Equal(t, guestID, guest.ID)
	assert.Equal(t, "Ana", guest.FirstName)
	assert.Equal(t, "Garcia", guest.LastName)
	require.NotNil(t, guest.Table)
	assert.Equal(t, "Mesa VIP", guest.Table.Name)
	require.NotNil(t, guest.Status)
	assert.Equal(t, "confirmed", guest.Status.Code)
	assert.Equal(t, statusID, guest.StatusID)
	assert.Equal(t, 3, guest.GuestsCount)
	assert.Equal(t, 3, guest.RSVPGuestCount)
	assert.Equal(t, "confirmed", guest.RSVPStatus)

	for _, secret := range []string{
		"ana.door@example.com", "+525500000042", "PRETTY42", invitationID.String(), tokenID.String(),
		"Nota RSVP privada", "Nota interna privada", "Sin nueces", "Bio privada",
	} {
		assert.NotContains(t, string(encoded), secret)
	}
}

func TestNewCheckinGuestResponsesReturnsEmptyArray(t *testing.T) {
	encoded, err := json.Marshal(NewCheckinGuestResponses(nil))
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(encoded))
}
