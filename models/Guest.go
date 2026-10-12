package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"strings"
	"time"
)

type Guest struct {
	ID                  uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventID             uuid.UUID      `gorm:"type:uuid;index" json:"event_id"`
	Event               Event          `gorm:"foreignKey:EventID" json:"-" validate:"-"`
	TableID             *uuid.UUID     `gorm:"type:uuid;index" json:"table_id,omitempty"`
	Table               *EventTable    `gorm:"foreignKey:TableID" json:"table,omitempty" validate:"-"`
	TableNumber         string         `gorm:"type:varchar(60)" json:"table_number,omitempty"`
	InvitationID        *uuid.UUID     `gorm:"type:uuid;index" json:"invitation_id,omitempty"`
	Invitation          *Invitation    `gorm:"foreignKey:InvitationID" json:"-"`
	PrettyToken         string         `gorm:"->;-:migration" json:"pretty_token,omitempty"`
	FirstName           string         `json:"first_name" validate:"required"`
	LastName            string         `json:"last_name"`
	Nickname            string         `json:"nickname"`
	Email               string         `json:"email" validate:"omitempty,email"`
	Phone               string         `json:"phone"`
	ShowContactInfo     bool           `json:"show_contact_info"`
	Role                string         `json:"role"` // Ej: "Graduado", "Novia"
	Bio                 string         `json:"bio"`
	Order               int            `json:"order"`
	ImageURL            string         `json:"image_url"`
	Image1URL           string         `json:"image1_url"`
	Image2URL           string         `json:"image2_url"`
	Image3URL           string         `json:"image3_url"`
	Headline            string         `json:"headline"`  // Encabezado personalizado
	Signature           string         `json:"signature"` // Firma visual o textual
	GuestStatusID       uuid.UUID      `gorm:"type:uuid;index" json:"guest_status_id"`
	GuestStatus         GuestStatus    `gorm:"foreignKey:GuestStatusID" json:"guest_status" validate:"-"`
	StatusID            uuid.UUID      `gorm:"-" json:"status_id,omitempty"`
	Status              *GuestStatus   `gorm:"-" json:"status,omitempty"`
	GuestsCount         int            `gorm:"-" json:"guests_count"`
	IsHost              bool           `json:"is_host"`
	MaxGuests           int            `gorm:"->;-:migration" json:"max_guests,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	DeletedAt           gorm.DeletedAt `gorm:"index" json:"-"`
	RSVPStatus          string         `json:"rsvp_status"` // "pending", "confirmed", "declined"
	RSVPAt              *time.Time     `json:"rsvp_at"`
	RSVPMethod          string         `json:"rsvp_method"` // "web", "app", "host"
	RSVPTokenID         *uuid.UUID     `json:"rsvp_token_id" gorm:"type:uuid"`
	RSVPGuestCount      int            `json:"rsvp_guest_count"`
	DietaryRestrictions string         `gorm:"type:varchar(255)" json:"dietary_restrictions"`
	RSVPNotes           string         `gorm:"type:text" json:"rsvp_notes"`
	Notes               string         `gorm:"type:text" json:"notes"`

	// Door check-in provenance. Only the check-in transition of PATCH
	// /guests/bulk/status sets these columns; its undo restores the
	// checkin_previous_* values and clears them all, and any other write of the
	// guest's RSVP clears them too. All nullable: AutoMigrate adds them without
	// touching existing rows, and NULL means "not checked in at the door".
	CheckedInAt                  *time.Time `gorm:"column:checked_in_at" json:"checked_in_at,omitempty"`
	CheckedInByUserID            *uuid.UUID `gorm:"type:uuid;column:checked_in_by_user_id" json:"checked_in_by_user_id,omitempty"`
	CheckinPreviousRSVPStatus    *string    `gorm:"type:text;column:checkin_previous_rsvp_status" json:"checkin_previous_rsvp_status,omitempty"`
	CheckinPreviousGuestStatusID *uuid.UUID `gorm:"type:uuid;column:checkin_previous_guest_status_id" json:"checkin_previous_guest_status_id,omitempty"`
	CheckinPreviousRSVPMethod    *string    `gorm:"type:text;column:checkin_previous_rsvp_method" json:"checkin_previous_rsvp_method,omitempty"`
	CheckinPreviousRSVPAt        *time.Time `gorm:"column:checkin_previous_rsvp_at" json:"checkin_previous_rsvp_at,omitempty"`
}

// DoorCheckedIn reports whether the guest's RSVP is still the one a door
// check-in wrote: provenance is recorded and the guest is confirmed by the
// host. Only then may a check-in-only caller undo it.
func (g *Guest) DoorCheckedIn() bool {
	return g.CheckedInAt != nil &&
		strings.EqualFold(strings.TrimSpace(g.RSVPStatus), "confirmed") &&
		strings.EqualFold(strings.TrimSpace(g.RSVPMethod), "host")
}

// ClearDoorCheckin drops the door check-in provenance. Any RSVP write other
// than the check-in transition calls it, so a later undo can never restore an
// answer that has since been replaced (for example the guest's own RSVP).
func (g *Guest) ClearDoorCheckin() {
	g.CheckedInAt = nil
	g.CheckedInByUserID = nil
	g.CheckinPreviousRSVPStatus = nil
	g.CheckinPreviousGuestStatusID = nil
	g.CheckinPreviousRSVPMethod = nil
	g.CheckinPreviousRSVPAt = nil
}

func (g *Guest) BeforeSave(tx *gorm.DB) error {
	if g.StatusID != uuid.Nil {
		g.GuestStatusID = g.StatusID
	}
	return nil
}

func (g *Guest) AfterFind(tx *gorm.DB) error {
	g.StatusID = g.GuestStatusID
	if g.GuestStatus.ID != uuid.Nil {
		g.Status = &g.GuestStatus
	}
	if g.Invitation != nil && g.Invitation.MaxGuests > 0 {
		g.MaxGuests = g.Invitation.MaxGuests
	}
	switch {
	case strings.EqualFold(g.RSVPStatus, "declined"):
		g.GuestsCount = 0
	case g.RSVPGuestCount > 0:
		g.GuestsCount = g.RSVPGuestCount
	case g.MaxGuests > 0:
		g.GuestsCount = g.MaxGuests
	default:
		g.GuestsCount = 1
	}
	return nil
}
