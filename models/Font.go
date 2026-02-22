package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Font struct {
	ID         uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ResourceID uuid.UUID      `gorm:"type:uuid;index" json:"resource_id"`
	Resource   Resource       `json:"resource,omitempty"`
	Name       string         `json:"name"` // Ej: "Playfair Display"
	IsSerif    bool           `json:"is_serif"` // Para clasificarlas (útil en UI)
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	DeletedAt  gorm.DeletedAt `gorm:"index" json:"-"`
}
