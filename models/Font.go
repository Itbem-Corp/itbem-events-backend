package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Font struct {
	ID         uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	ResourceID uuid.UUID `gorm:"type:uuid;index"`
	Resource   Resource
	Name       string // Ej: "Playfair Display"
	IsSerif    bool   // Para clasificarlas (útil en UI)
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  gorm.DeletedAt `gorm:"index"`
}
