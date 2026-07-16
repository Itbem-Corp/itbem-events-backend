package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Moment struct {
	ID            uuid.UUID     `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventID       *uuid.UUID    `gorm:"type:uuid;index" json:"event_id,omitempty"`
	InvitationID  *uuid.UUID    `gorm:"type:uuid;index" json:"invitation_id,omitempty"`                   // nullable for shared QR uploads
	Invitation    Invitation    `gorm:"foreignKey:InvitationID" validate:"-" json:"invitation,omitempty"` // omit if nil
	MomentTypeID  *uuid.UUID    `gorm:"type:uuid;index" json:"moment_type_id,omitempty"`
	MomentType    MomentType    `gorm:"foreignKey:MomentTypeID" validate:"-" json:"moment_type,omitempty"`
	GuestID       *uuid.UUID    `json:"guest_id,omitempty"`
	Guest         *Guest        `gorm:"foreignKey:GuestID" json:"guest,omitempty"`
	Title         string        `json:"title"`
	Description   string        `json:"description"` // texto, caption o nota del invitado
	ContentURL    string        `json:"content_url"` // imagen, video o audio en S3
	MediaBucket   string        `gorm:"type:varchar(255);not null;default:'';index" json:"-"`
	ThumbnailURL  string        `gorm:"type:varchar(500);default:''" json:"thumbnail_url,omitempty"` // WebP thumbnail for videos (extracted by Lambda)
	MediaVariants MediaVariants `gorm:"type:jsonb;default:'[]';not null" json:"media_variants,omitempty"`
	// Signed URL expirations are response-only metadata for admin/public clients.
	ContentURLExpiresAt   *time.Time `gorm:"-" json:"content_url_expires_at,omitempty"`
	ThumbnailURLExpiresAt *time.Time `gorm:"-" json:"thumbnail_url_expires_at,omitempty"`
	ContentType           string     `gorm:"type:varchar(100);default:''" json:"content_type,omitempty"` // original MIME type from upload
	IsApproved            bool       `gorm:"default:false" json:"is_approved"`                           // moderación
	// ProcessingStatus tracks async video transcoding: "" | "pending" | "processing" | "done" | "failed"
	// Empty string means no processing needed (images/text moments).
	ProcessingStatus string `gorm:"type:varchar(20);default:''" json:"processing_status,omitempty"`
	// ProcessingGeneration and ProcessingJobID identify the only media job
	// currently authorized to mutate this moment. Every requeue increments the
	// generation so delayed callbacks from older SQS deliveries become stale.
	ProcessingGeneration int64  `gorm:"default:0;not null" json:"processing_generation,omitempty"`
	ProcessingJobID      string `gorm:"type:varchar(36);default:'';not null" json:"processing_job_id,omitempty"`
	ProcessingInputKey   string `gorm:"type:varchar(500);default:'';not null" json:"processing_input_key,omitempty"`
	// Lambda processing metrics — populated when processing_status = "done"
	ProcessingDurationMs int64 `gorm:"default:0" json:"processing_duration_ms,omitempty"`
	OriginalSizeBytes    int64 `gorm:"default:0" json:"original_size_bytes,omitempty"`
	OptimizedSizeBytes   int64 `gorm:"default:0" json:"optimized_size_bytes,omitempty"`
	// ErrorMessage is populated by Lambda when processing fails.
	// Empty string means no error or not yet processed.
	ErrorMessage string         `gorm:"type:varchar(500);default:''" json:"error_message,omitempty"`
	Order        int            `json:"order,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}
