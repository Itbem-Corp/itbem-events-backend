package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type InvitationAccessToken struct {
	ID           uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	InvitationID uuid.UUID  `gorm:"type:uuid;uniqueIndex"`
	Invitation   Invitation `gorm:"foreignKey:InvitationID"`
	Token        string     `gorm:"uniqueIndex"` // UUID largo
	PrettyToken  string     `gorm:"index"`       // Código corto, único dentro del Event
	ExpiresAt    *time.Time
	IsUsed       bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
