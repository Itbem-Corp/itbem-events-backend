package dtos

import (
	"time"

	"github.com/gofrs/uuid"
)

type AnalyticsGuestTable struct {
	Name string `json:"name"`
}

// AnalyticsGuest is the minimal guest projection required by dashboard charts.
// It intentionally excludes contact data, public profile media, notes, and RSVP tokens.
type AnalyticsGuest struct {
	ID                  uuid.UUID            `json:"id"`
	FirstName           string               `json:"first_name"`
	LastName            string               `json:"last_name"`
	Role                string               `json:"role"`
	RSVPStatus          string               `json:"rsvp_status"`
	RSVPAt              *time.Time           `json:"rsvp_at,omitempty"`
	RSVPMethod          string               `json:"rsvp_method"`
	RSVPGuestCount      int                  `json:"rsvp_guest_count"`
	GuestsCount         int                  `json:"guests_count"`
	DietaryRestrictions string               `json:"dietary_restrictions"`
	TableName           string               `json:"-"`
	Table               *AnalyticsGuestTable `json:"table,omitempty" gorm:"-"`
}

func HydrateAnalyticsGuestTables(guests []AnalyticsGuest) {
	for i := range guests {
		if guests[i].TableName != "" {
			guests[i].Table = &AnalyticsGuestTable{Name: guests[i].TableName}
		}
	}
}
