package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Event struct {
	ID                 uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ClientID           *uuid.UUID     `gorm:"type:uuid;index" json:"client_id"`
	Client             *Client        `gorm:"foreignKey:ClientID" json:"client,omitempty" validate:"-"`
	Name               string         `gorm:"uniqueIndex" json:"name" validate:"required"`
	Identifier         string         `gorm:"uniqueIndex" json:"identifier"`
	Description        string         `json:"description"`
	CoverImageURL      string         `json:"cover_image_url"`
	CoverImageURL2     string         `json:"cover_image_url2"`
	CustomDomain       string         `json:"custom_domain"`
	Address            string         `json:"address"`
	SecondAddress      string         `json:"second_address"`
	MusicUrl           string         `json:"music_url"` // SDUI: URL del audio para MusicWidget (opcional)
	EventDateTime      time.Time      `json:"event_date_time"`
	Timezone           string         `json:"timezone"`
	Language           string         `json:"language"`
	EventTypeID        uuid.UUID      `gorm:"type:uuid;index" json:"event_type_id"`
	EventType          EventType      `gorm:"foreignKey:EventTypeID" json:"event_type,omitempty" validate:"-"`
	EventConfig        EventConfig    `gorm:"foreignKey:ID;references:ID" json:"event_config,omitempty" validate:"-"`
	OrganizerName      string         `json:"organizer_name"`
	OrganizerEmail     string         `json:"organizer_email" validate:"omitempty,email"`
	OrganizerPhone     string         `json:"organizer_phone"`
	MaxGuests          *int           `json:"max_guests"`
	AllowGuestAccess   bool           `json:"allow_guest_access"`
	SlugLocked         bool           `json:"slug_locked"`
	IsActive           bool           `json:"is_active"`
	PendingMomentCount int64          `gorm:"->;-:migration" json:"pending_moment_count"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	DeletedAt          gorm.DeletedAt `gorm:"index" json:"-"`
}
