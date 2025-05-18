package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type GuestStatus struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Code      string    `gorm:"uniqueIndex"` // Ej: "confirmed", "pending"
	Label     string    // Ej: "Confirmed", "Awaiting response"
	Color     string    // Hex o clase tailwind (opcional)
	Order     int       // Para ordenarlos en UI
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}
