package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type FontSetPattern struct {
	ID        uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	Key       string         `json:"key"` // Ej: "title", "body", "accent"
	FontSetID uuid.UUID      `gorm:"type:uuid;index" json:"font_set_id"`
	Order     int            `json:"order"`
	FontID    uuid.UUID      `gorm:"type:uuid;index" json:"font_id"`
	Font      Font           `gorm:"foreignKey:FontID" json:"font,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}
