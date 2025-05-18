package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type ColorPalette struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Name      string
	Patterns  []ColorPalettePattern `gorm:"foreignKey:ColorPaletteID"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}
