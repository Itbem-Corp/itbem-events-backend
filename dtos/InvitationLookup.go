package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

// InvitationLookup is the public-safe payload consumed by RSVP, MomentWall and OG flows.
type InvitationLookup struct {
	Invitation  InvitationLookupInvitation `json:"invitation"`
	Guest       InvitationLookupGuest      `json:"guest"`
	PrettyToken string                     `json:"pretty_token,omitempty"`
	Event       *EventMeta                 `json:"event,omitempty"`
}

type InvitationLookupInvitation struct {
	ID        uuid.UUID  `json:"id"`
	EventID   uuid.UUID  `json:"event_id"`
	MaxGuests int        `json:"max_guests"`
	Event     *EventMeta `json:"event,omitempty"`
}

type InvitationLookupGuest struct {
	ID                  uuid.UUID  `json:"id"`
	EventID             uuid.UUID  `json:"event_id,omitempty"`
	InvitationID        *uuid.UUID `json:"invitation_id,omitempty"`
	FirstName           string     `json:"first_name"`
	LastName            string     `json:"last_name,omitempty"`
	RSVPStatus          string     `json:"rsvp_status,omitempty"`
	RSVPAt              *time.Time `json:"rsvp_at,omitempty"`
	RSVPMethod          string     `json:"rsvp_method,omitempty"`
	RSVPGuestCount      int        `json:"rsvp_guest_count"`
	DietaryRestrictions string     `json:"dietary_restrictions"`
	RSVPNotes           string     `json:"rsvp_notes"`
}

type RSVPConfirmationResponse struct {
	Guest       InvitationLookupGuest `json:"guest"`
	PrettyToken string                `json:"pretty_token,omitempty"`
}

func NewRSVPConfirmationResponse(guest *models.Guest, prettyToken string) RSVPConfirmationResponse {
	response := RSVPConfirmationResponse{PrettyToken: prettyToken}
	if guest == nil {
		return response
	}

	response.Guest = InvitationLookupGuest{
		ID:                  guest.ID,
		EventID:             guest.EventID,
		InvitationID:        guest.InvitationID,
		FirstName:           guest.FirstName,
		LastName:            guest.LastName,
		RSVPStatus:          guest.RSVPStatus,
		RSVPAt:              guest.RSVPAt,
		RSVPMethod:          guest.RSVPMethod,
		RSVPGuestCount:      guest.RSVPGuestCount,
		DietaryRestrictions: guest.DietaryRestrictions,
		RSVPNotes:           guest.RSVPNotes,
	}
	return response
}
