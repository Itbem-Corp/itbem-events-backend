package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type Resource struct {
	ID             uuid.UUID    `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventSectionID *uuid.UUID   `gorm:"type:uuid;index" json:"event_section_id,omitempty"`
	ResourceTypeID uuid.UUID    `gorm:"type:uuid;index" json:"resource_type_id"`
	ResourceType   ResourceType `gorm:"foreignKey:ResourceTypeID" json:"resource_type,omitempty"`
	Path           string       `json:"path"`
	MediaBucket    string       `gorm:"type:varchar(255);not null;default:'';index" json:"-"`
	AltText        string       `json:"alt_text"`
	Title          string       `json:"title"`
	Position       *int         `json:"position,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}
