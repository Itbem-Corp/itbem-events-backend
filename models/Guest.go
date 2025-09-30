package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Guest struct {
	ID              uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventID         uuid.UUID      `gorm:"type:uuid;index" json:"event_id"`
	Event           Event          `gorm:"foreignKey:EventID" json:"-"`
	InvitationID    *uuid.UUID     `gorm:"type:uuid;index" json:"invitation_id,omitempty"`
	Invitation      *Invitation    `gorm:"foreignKey:InvitationID" json:"-"`
	FirstName       string         `json:"first_name"`
	LastName        string         `json:"last_name"`
	Nickname        string         `json:"nickname"`
	Email           string         `json:"email"`
	Phone           string         `json:"phone"`
	ShowContactInfo bool           `json:"show_contact_info"`
	Role            string         `json:"role"` // Ej: "Graduado", "Novia"
	Bio             string         `json:"bio"`
	Order           int            `json:"order"`
	ImageURL        string         `json:"image_url"`
	Image1URL       string         `json:"image1_url"`
	Image2URL       string         `json:"image2_url"`
	Image3URL       string         `json:"image3_url"`
	Headline        string         `json:"headline"`  // Encabezado personalizado
	Signature       string         `json:"signature"` // Firma visual o textual
	GuestStatusID   uuid.UUID      `gorm:"type:uuid" json:"guest_status_id"`
	GuestStatus     GuestStatus    `gorm:"foreignKey:GuestStatusID" json:"-"`
	IsHost          bool           `json:"is_host"`
	MaxGuests       int            `gorm:"-" json:"max_guests,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`
	RSVPStatus      string         `json:"rsvp_status"` // "pending", "confirmed", "declined"
	RSVPAt          *time.Time     `json:"rsvp_at"`
	RSVPMethod      string         `json:"rsvp_method"` // "web", "app", "host"
	RSVPTokenID     *uuid.UUID     `json:"rsvp_token_id" gorm:"type:uuid"`
	RSVPGuestCount  int            `json:"rsvp_guest_count"`
}
