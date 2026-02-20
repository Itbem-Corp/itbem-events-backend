package models

import (
	"github.com/gofrs/uuid"
	"time"
)

// Constantes para usar en la lógica de negocio (Backend)
const (
	ClientTypeCodePlatform = "PLATFORM"
	ClientTypeCodeAgency   = "AGENCY"
	ClientTypeCodeCustomer = "CUSTOMER"
)

type ClientType struct {
	ID          uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Name        string    `gorm:"not null"`             // Ej: "Agencia", "Cliente Final"
	Code        string    `gorm:"uniqueIndex;not null"` // Ej: "AGENCY" (Para lógica interna)
	Description string    `gorm:"text"`
	Level       int       `gorm:"not null;default:10"`
	IsActive    bool      `gorm:"default:true"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
