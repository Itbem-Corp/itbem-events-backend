package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type ClientRole struct {
	ID          uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Name        string    `gorm:"not null"`             // Ej: "Propietario", "Administrador"
	Code        string    `gorm:"uniqueIndex;not null"` // Ej: "OWNER", "ADMIN", "MEMBER"
	Description string    `gorm:"text"`
	Hierarchy   int       `gorm:"not null;default:100"`
	IsActive    bool      `gorm:"default:true"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
