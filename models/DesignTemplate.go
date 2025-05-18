package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type DesignTemplate struct {
	ID                uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Name              string
	Identifier        string `gorm:"uniqueIndex"` // Ej: "classic-elegant", "dark-romantic"
	Description       string
	PreviewURL        string
	ColorPaletteID    uuid.UUID
	ColorPalette      ColorPalette `gorm:"foreignKey:ColorPaletteID"`
	FontSetID         uuid.UUID
	FontSet           FontSet `gorm:"foreignKey:FontSetID"`
	AnimationsEnabled bool
	HasDarkMode       bool
	Category          string // Ej: "romantic", "minimal", "luxury"
	IsPremium         bool
	IsActive          bool // Permite desactivar un template sin borrarlo
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         gorm.DeletedAt `gorm:"index"`
}
