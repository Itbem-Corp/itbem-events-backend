package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type ColorPalette struct {
	ID        uuid.UUID             `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	Name      string                `json:"name"`
	Patterns  []ColorPalettePattern `gorm:"foreignKey:ColorPaletteID" json:"patterns,omitempty"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
	DeletedAt gorm.DeletedAt        `gorm:"index" json:"-"`
}
