package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type InvitationLog struct {
	ID           uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	InvitationID uuid.UUID  `gorm:"type:uuid;index"`
	Invitation   Invitation `gorm:"foreignKey:InvitationID"`
	Channel      string     // "email", "whatsapp"
	Action       string     // "sent", "failed", "resent", "moment_request", etc.
	Status       string     // "success", "error"
	Response     string     // Mensaje de error o éxito (opcional)
	Timestamp    time.Time
	CreatedAt    time.Time
}
