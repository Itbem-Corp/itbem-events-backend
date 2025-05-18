package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Event struct {
	ID               uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Name             string    `gorm:"uniqueIndex"`
	Identifier       string    `gorm:"uniqueIndex"`
	Description      string
	CoverImageURL    string
	CoverImageURL2   string
	CustomDomain     string
	Address          string
	SecondAddress    string
	EventDateTime    time.Time
	Timezone         string
	Language         string
	EventTypeID      uuid.UUID
	EventType        EventType   `gorm:"foreignKey:EventTypeID"`
	EventConfig      EventConfig `gorm:"foreignKey:ID;references:ID"`
	OrganizerName    string
	OrganizerEmail   string
	OrganizerPhone   string
	MaxGuests        *int
	AllowGuestAccess bool
	SlugLocked       bool
	IsActive         bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        gorm.DeletedAt `gorm:"index"`
}
