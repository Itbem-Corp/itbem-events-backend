package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type FontSet struct {
	ID         uuid.UUID        `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Name       string           // Ej: "Serif clásico"
	PreviewURL string           // Imagen que muestra combinación
	Patterns   []FontSetPattern `gorm:"foreignKey:FontSetID"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  gorm.DeletedAt `gorm:"index"`
}
