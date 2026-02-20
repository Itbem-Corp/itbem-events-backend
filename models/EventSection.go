package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type EventSection struct {
	ID            uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventID       uuid.UUID      `gorm:"type:uuid;index" json:"event_id"`
	Event         Event          `gorm:"foreignKey:EventID" json:"event,omitempty"`
	Key           string         `json:"key"`            // Ej: "countdown", "location", "rsvp", "gallery"
	Title         string         `json:"title"`          // Texto visible en frontend, ej: "¿Dónde será?"
	ComponentType string         `json:"component_type"` // SDUI: tipo de sección, ej: "GraduationHero"
	Config        string         `json:"config" gorm:"type:jsonb;default:'{}'"`  // SDUI: config JSON de la sección
	Order         int            `json:"order"`          // Orden en que aparece
	IsVisible     bool           `json:"is_visible"`     // Si debe renderizarse
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}
