package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type FontSet struct {
	ID        uuid.UUID        `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	Name      string           `json:"name"` // Ej: "Serif clásico"
	Patterns  []FontSetPattern `gorm:"foreignKey:FontSetID" json:"patterns,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	DeletedAt gorm.DeletedAt   `gorm:"index" json:"-"`
}
