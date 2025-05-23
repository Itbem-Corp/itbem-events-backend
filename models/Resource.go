package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type Resource struct {
	ID             uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	EventSectionID *uuid.UUID `gorm:"type:uuid;index"` // Opcional
	ResourceTypeID uuid.UUID
	ResourceType   ResourceType `gorm:"foreignKey:ResourceTypeID"`
	Path           string
	AltText        string
	Title          string
	Position       *int // Opcional
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
