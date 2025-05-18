package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type EventSection struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	EventID   uuid.UUID `gorm:"type:uuid;index"`
	Event     Event     `gorm:"foreignKey:EventID"`
	Key       string    // Ej: "countdown", "location", "rsvp", "gallery", "guestbook"
	Title     string    // Texto visible en frontend, ej: "¿Dónde será?"
	Order     int       // Orden en que aparece
	IsVisible bool      // Si debe renderizarse
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}
