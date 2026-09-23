package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// OutboxEvent is a durable hand-off between a committed API operation and an
// external queue. Delivery is at-least-once; consumers use the deterministic
// job ID in Payload to make repeated deliveries harmless.
type OutboxEvent struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	EventType string    `gorm:"type:varchar(96);not null;index" json:"-"`
	// TargetRuntime and QueueNamespace freeze the route admission decision at
	// enqueue time. They are labels only: never queue URLs, tokens or payloads.
	TargetRuntime  string `gorm:"type:varchar(48);not null;default:'';index" json:"-"`
	QueueNamespace string `gorm:"type:varchar(96);not null;default:'';index" json:"-"`
	// FairnessKey is assigned at enqueue time from the validated payload. It is
	// scheduler metadata only: it never grants access or changes routing.
	// Persisting it lets multiple dispatchers discover all ready lanes without
	// decoding arbitrary JSON while claiming rows.
	FairnessKey   string     `gorm:"type:varchar(256);not null;default:'';index" json:"-"`
	DedupeKey     string     `gorm:"type:varchar(256);not null;uniqueIndex" json:"-"`
	TenantCode    string     `gorm:"type:varchar(32);not null;default:'';index" json:"-"`
	CorrelationID string     `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	Payload       string     `gorm:"type:jsonb;not null" json:"-"`
	State         string     `gorm:"type:varchar(16);not null;index" json:"-"`
	Attempts      int        `gorm:"not null;default:0" json:"-"`
	AvailableAt   time.Time  `gorm:"not null;index" json:"-"`
	LeaseUntil    *time.Time `gorm:"index" json:"-"`
	LastError     string     `gorm:"type:varchar(1024);not null;default:''" json:"-"`
	ProcessedAt   *time.Time `gorm:"index" json:"-"`
	CreatedAt     time.Time  `gorm:"not null;index" json:"-"`
	UpdatedAt     time.Time  `gorm:"not null" json:"-"`
}

// OutboxDispatchCursor serializes the small claim decision across API
// replicas. The cursor is scheduler state only; it does not represent task
// progress and can never grant a transition or publication permission.
type OutboxDispatchCursor struct {
	ID              uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	CursorKey       string    `gorm:"type:varchar(128);not null;uniqueIndex" json:"-"`
	LastFairnessKey string    `gorm:"type:varchar(256);not null;default:''" json:"-"`
	CreatedAt       time.Time `gorm:"not null" json:"-"`
	UpdatedAt       time.Time `gorm:"not null" json:"-"`
}
