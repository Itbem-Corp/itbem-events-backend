package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type InvitationAccessToken struct {
	ID           uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	InvitationID uuid.UUID  `gorm:"type:uuid;uniqueIndex"` // uno por invitación
	Invitation   Invitation `gorm:"foreignKey:InvitationID"`
	Token        string     `gorm:"uniqueIndex"` // puede ser UUID o string generado
	ExpiresAt    *time.Time // opcional
	IsUsed       bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
