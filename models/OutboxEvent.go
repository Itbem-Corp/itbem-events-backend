package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// OutboxEvent is a durable hand-off between a committed API operation and an
// external queue. Delivery is at-least-once; consumers use the deterministic
// job ID in Payload to make repeated deliveries harmless.
type OutboxEvent struct {
	ID          uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	EventType   string     `gorm:"type:varchar(96);not null;index" json:"-"`
	DedupeKey   string     `gorm:"type:varchar(256);not null;uniqueIndex" json:"-"`
	TenantCode  string     `gorm:"type:varchar(32);not null;default:'';index" json:"-"`
	Payload     string     `gorm:"type:jsonb;not null" json:"-"`
	State       string     `gorm:"type:varchar(16);not null;index" json:"-"`
	Attempts    int        `gorm:"not null;default:0" json:"-"`
	AvailableAt time.Time  `gorm:"not null;index" json:"-"`
	LeaseUntil  *time.Time `gorm:"index" json:"-"`
	LastError   string     `gorm:"type:varchar(1024);not null;default:''" json:"-"`
	ProcessedAt *time.Time `gorm:"index" json:"-"`
	CreatedAt   time.Time  `gorm:"not null;index" json:"-"`
	UpdatedAt   time.Time  `gorm:"not null" json:"-"`
}
