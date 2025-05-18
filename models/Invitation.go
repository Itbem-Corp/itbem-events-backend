package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Invitation struct {
	ID                      uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	EventID                 uuid.UUID `gorm:"type:uuid;index"`
	Event                   Event     `gorm:"foreignKey:EventID"`
	Type                    string    // e.g., "formal", "casual", etc.
	SubType                 string    // e.g., "family", "friend", "vip"
	InvitationEmailSent     bool
	InvitationWhatsAppSent  bool
	InvitationSent          bool // derivado de los anteriores
	MomentEmailRequested    bool
	MomentWhatsAppRequested bool
	MomentRequestSent       bool // derivado
	MomentEmailDelivered    bool
	MomentWhatsAppDelivered bool
	MomentDelivered         bool // derivado
	EnableEmail             bool
	EnableWhatsApp          bool
	CreatedAt               time.Time
	UpdatedAt               time.Time
	DeletedAt               gorm.DeletedAt `gorm:"index"`
}
