package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Guest struct {
	ID              uuid.UUID   `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	EventID         uuid.UUID   `gorm:"type:uuid;index"`
	Event           Event       `gorm:"foreignKey:EventID"`
	InvitationID    *uuid.UUID  `gorm:"type:uuid;index"` // opcional si es específico de una invitación
	Invitation      *Invitation `gorm:"foreignKey:InvitationID"`
	FirstName       string
	LastName        string
	Nickname        string
	Email           string
	Phone           string
	ShowContactInfo bool
	Role            string // Ej: "Graduado", "Novia"
	Bio             string
	Order           int
	ImageURL        string
	Image1URL       string
	Image2URL       string
	Image3URL       string
	Headline        string // Encabezado personalizado
	Signature       string // Firma visual o textual
	GuestStatusID   uuid.UUID
	GuestStatus     GuestStatus `gorm:"foreignKey:GuestStatusID"`
	IsHost          bool        // capitalizado para exportación
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       gorm.DeletedAt `gorm:"index"`
}
