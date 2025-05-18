package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type Resource struct {
	ID             uuid.UUID    `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	EventSectionID uuid.UUID    `gorm:"type:uuid;index"`
	EventSection   EventSection `gorm:"foreignKey:EventSectionID"`
	ResourceTypeID uuid.UUID
	ResourceType   ResourceType `gorm:"foreignKey:ResourceTypeID"`
	URL            string
	AltText        string
	Title          string
	Position       int // Orden en que se mostrará en la sección
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
