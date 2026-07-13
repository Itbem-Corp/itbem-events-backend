package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type DesignTemplate struct {
	ID                uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	Name              string         `json:"name"`
	Identifier        string         `gorm:"uniqueIndex" json:"identifier"` // Ej: "classic-elegant", "dark-romantic"
	Description       string         `json:"description"`
	PreviewURL        string         `json:"preview_url"`
	ColorPaletteID    *uuid.UUID     `gorm:"type:uuid;index" json:"color_palette_id"`
	ColorPalette      *ColorPalette  `gorm:"foreignKey:ColorPaletteID" json:"color_palette,omitempty"`
	FontSetID         *uuid.UUID     `gorm:"type:uuid;index" json:"font_set_id"`
	FontSet           *FontSet       `gorm:"foreignKey:FontSetID" json:"font_set,omitempty"`
	AnimationsEnabled bool           `json:"animations_enabled"`
	HasDarkMode       bool           `json:"has_dark_mode"`
	Category          string         `json:"category"` // Ej: "romantic", "minimal", "luxury"
	IsPremium         bool           `json:"is_premium"`
	IsActive          bool           `json:"is_active"` // Permite desactivar un template sin borrarlo
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	DeletedAt         gorm.DeletedAt `gorm:"index" json:"-"`
}
