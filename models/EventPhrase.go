package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// EventPhrase holds a curated emotional phrase for a specific event type.
// Used by the public moments gallery to inject memory cards between photos.
type EventPhrase struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventType string    `gorm:"type:varchar(50);not null;index" json:"event_type"`
	Phrase    string    `gorm:"type:text;not null" json:"phrase"`
	CreatedAt time.Time `json:"created_at"`
}
