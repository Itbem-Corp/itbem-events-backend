package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Invitation struct {
	ID                      uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventID                 uuid.UUID      `gorm:"type:uuid;index" json:"event_id"`
	Event                   Event          `gorm:"foreignKey:EventID" json:"event,omitempty" validate:"-"`
	Type                    string         `json:"type"`    // e.g., "formal", "casual", etc.
	SubType                 string         `json:"sub_type"` // e.g., "family", "friend", "vip"
	InvitationEmailSent     bool           `json:"invitation_email_sent"`
	InvitationWhatsAppSent  bool           `json:"invitation_whatsapp_sent"`
	InvitationSent          bool           `json:"invitation_sent"` // derivado de los anteriores
	MaxGuests               int            `json:"max_guests"`
	MomentEmailRequested    bool           `json:"moment_email_requested"`
	MomentWhatsAppRequested bool           `json:"moment_whatsapp_requested"`
	MomentRequestSent       bool           `json:"moment_request_sent"` // derivado
	MomentEmailDelivered    bool           `json:"moment_email_delivered"`
	MomentWhatsAppDelivered bool           `json:"moment_whatsapp_delivered"`
	MomentDelivered         bool           `json:"moment_delivered"` // derivado
	EnableEmail             bool           `json:"enable_email"`
	EnableWhatsApp          bool           `json:"enable_whatsapp"`
	CreatedAt               time.Time      `json:"created_at"`
	UpdatedAt               time.Time      `json:"updated_at"`
	DeletedAt               gorm.DeletedAt `gorm:"index" json:"-"`
}
