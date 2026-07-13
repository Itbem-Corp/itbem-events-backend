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

func TestNewInvitationResponseUsesAdminContract(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.July, 7, 12, 0, 0, 0, time.UTC)

	body := NewInvitationResponse(models.Invitation{
		ID:                      uuid.Must(uuid.NewV4()),
		EventID:                 eventID,
		Event:                   models.Event{ID: eventID, Name: "Should not leak"},
		Type:                    "formal",
		SubType:                 "family",
		InvitationEmailSent:     true,
		InvitationWhatsAppSent:  true,
		InvitationSent:          true,
		MaxGuests:               4,
		MomentEmailRequested:    true,
		MomentWhatsAppRequested: false,
		MomentRequestSent:       true,
		MomentEmailDelivered:    true,
		MomentWhatsAppDelivered: false,
		MomentDelivered:         true,
		EnableEmail:             true,
		EnableWhatsApp:          true,
		CreatedAt:               now,
		UpdatedAt:               now.Add(time.Minute),
		DeletedAt:               gorm.DeletedAt{Time: now, Valid: true},
	})

	assert.Equal(t, eventID, body.EventID)
	assert.Equal(t, "formal", body.Type)
	assert.Equal(t, "family", body.SubType)
	assert.True(t, body.InvitationSent)
	assert.Equal(t, 4, body.MaxGuests)
	assert.True(t, body.MomentDelivered)

	raw, err := json.Marshal(body)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "event\":")
	assert.NotContains(t, string(raw), "Should not leak")
	assert.NotContains(t, string(raw), "deleted_at")
}

func TestNewInvitationResponsesReturnsEmptyArray(t *testing.T) {
	assert.Empty(t, NewInvitationResponses(nil))
}

func TestNewRSVPConfirmationResponseIncludesZeroGuestCount(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	response := NewRSVPConfirmationResponse(&models.Guest{
		ID:                  guestID,
		EventID:             eventID,
		FirstName:           "Ana",
		RSVPStatus:          "declined",
		RSVPMethod:          "web",
		RSVPGuestCount:      0,
		DietaryRestrictions: "Vegano",
		RSVPNotes:           "Mesa cerca",
		Notes:               "Nota interna",
	}, "TOKEN123")

	assert.Equal(t, "TOKEN123", response.PrettyToken)
	assert.Equal(t, guestID, response.Guest.ID)
	assert.Equal(t, eventID, response.Guest.EventID)
	assert.Equal(t, 0, response.Guest.RSVPGuestCount)
	assert.Equal(t, "Vegano", response.Guest.DietaryRestrictions)
	assert.Equal(t, "Mesa cerca", response.Guest.RSVPNotes)

	raw, err := json.Marshal(response)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"rsvp_guest_count":0`)
	assert.Contains(t, string(raw), `"rsvp_notes":"Mesa cerca"`)
	assert.NotContains(t, string(raw), "Nota interna")
}
