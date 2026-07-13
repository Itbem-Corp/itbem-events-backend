package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type GuestStatus struct {
	ID        uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	Code      string         `gorm:"uniqueIndex" json:"code"` // Ej: "confirmed", "pending"
	Label     string         `json:"label"`                   // Ej: "Confirmed", "Awaiting response"
	Color     string         `json:"color"`                   // Hex o clase tailwind (opcional)
	Order     int            `json:"order"`                   // Para ordenarlos en UI
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}
