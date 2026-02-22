package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type ColorPalettePattern struct {
	ID             uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ColorPaletteID uuid.UUID      `gorm:"type:uuid;index" json:"color_palette_id"`
	ColorID        uuid.UUID      `gorm:"type:uuid;index" json:"color_id"`
	Key            string         `json:"key"`
	Order          int            `json:"order"`
	Color          Color          `gorm:"foreignKey:ColorID" json:"color,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`
}
