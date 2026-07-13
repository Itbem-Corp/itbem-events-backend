package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// EventPhrase preserves the curated phrase data already published by the
// production backend. The table has no public write endpoint; seeds only add
// missing catalog entries and never replace customer or operator additions.
type EventPhrase struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventType string    `gorm:"type:varchar(50);not null;index" json:"event_type"`
	Phrase    string    `gorm:"type:text;not null" json:"phrase"`
	CreatedAt time.Time `json:"created_at"`
}

func (EventPhrase) TableName() string {
	return "event_phrases"
}
