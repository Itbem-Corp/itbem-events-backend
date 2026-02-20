package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type InvitationAccessToken struct {
	ID           uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	InvitationID uuid.UUID  `gorm:"type:uuid;uniqueIndex" json:"invitation_id"`
	Invitation   Invitation `gorm:"foreignKey:InvitationID" json:"invitation,omitempty"`
	Token        string     `gorm:"uniqueIndex" json:"-"`          // UUID largo — no exponer
	PrettyToken  string     `gorm:"index" json:"pretty_token"`     // Código corto, único dentro del Event
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	IsUsed       bool       `json:"is_used"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}
