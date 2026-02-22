package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Moment struct {
	ID           uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	EventID      *uuid.UUID `gorm:"type:uuid;index" json:"event_id,omitempty"`
	InvitationID *uuid.UUID `gorm:"type:uuid;index" json:"invitation_id,omitempty"`                 // nullable for shared QR uploads
	Invitation   Invitation `gorm:"foreignKey:InvitationID" validate:"-" json:"invitation,omitempty"` // omit if nil
	MomentTypeID *uuid.UUID `gorm:"type:uuid;index"`
	MomentType   MomentType `gorm:"foreignKey:MomentTypeID" validate:"-"`
	GuestID      *uuid.UUID
	Guest        *Guest `gorm:"foreignKey:GuestID"`
	Title        string
	Description  string `json:"description"` // texto, caption o nota del invitado
	ContentURL   string `json:"content_url"` // imagen, video o audio en S3
	IsApproved   bool   `gorm:"default:false" json:"is_approved"` // moderación
	// ProcessingStatus tracks async video transcoding: "" | "pending" | "processing" | "done" | "failed"
	// Empty string means no processing needed (images/text moments).
	ProcessingStatus string `gorm:"type:varchar(20);default:''" json:"processing_status,omitempty"`
	// Lambda processing metrics — populated when processing_status = "done"
	ProcessingDurationMs int64 `gorm:"default:0" json:"processing_duration_ms,omitempty"`
	OriginalSizeBytes    int64 `gorm:"default:0" json:"original_size_bytes,omitempty"`
	OptimizedSizeBytes   int64 `gorm:"default:0" json:"optimized_size_bytes,omitempty"`
	// ErrorMessage is populated by Lambda when processing fails.
	// Empty string means no error or not yet processed.
	ErrorMessage string `gorm:"type:varchar(500);default:''" json:"error_message,omitempty"`
	Order        int    `json:"order,omitempty"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        gorm.DeletedAt `gorm:"index"`
}
