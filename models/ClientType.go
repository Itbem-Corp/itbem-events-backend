package models

import (
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

// Constantes para usar en la lógica de negocio (Backend)
const (
	ClientTypeCodePlatform = "PLATFORM"
	ClientTypeCodeAgency   = "AGENCY"
	ClientTypeCodeCustomer = "CUSTOMER"
)

// AllowedChildClientTypeCodes is the single source of truth for which
// organization types may be created directly under a parent of type
// parentTypeCode (an empty code means a top-level organization). Customers are
// leaves: the hierarchy is intentionally Platform > Agency > Customer.
func AllowedChildClientTypeCodes(parentTypeCode string) []string {
	switch strings.ToUpper(strings.TrimSpace(parentTypeCode)) {
	case "":
		return []string{ClientTypeCodePlatform}
	case ClientTypeCodePlatform:
		return []string{ClientTypeCodeAgency}
	case ClientTypeCodeAgency:
		return []string{ClientTypeCodeCustomer}
	default:
		return nil
	}
}

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
