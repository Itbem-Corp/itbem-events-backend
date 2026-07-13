package dtos

import "github.com/gofrs/uuid"

// SeatingGuest is the compact projection required by the dashboard seating workspace.
// It intentionally excludes media, notes, tokens, biographies and invitation metadata.
type SeatingGuest struct {
	ID             uuid.UUID  `json:"id"`
	FirstName      string     `json:"first_name"`
	LastName       string     `json:"last_name"`
	Email          string     `json:"email,omitempty"`
	TableID        *uuid.UUID `json:"table_id,omitempty"`
	RSVPStatus     string     `json:"rsvp_status"`
	RSVPGuestCount int        `json:"rsvp_guest_count"`
	GuestsCount    int        `json:"guests_count"`
}

type SeatingWorkspaceResponse struct {
	Tables []EventTableResponse `json:"tables"`
	Guests []SeatingGuest       `json:"guests"`
}

type SeatingPlanSaveResponse struct {
	Tables []EventTableResponse `json:"tables"`
	Guests []SeatingGuest       `json:"guests,omitempty"`
}
