package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// IdempotencyRecord protects critical authenticated mutations from being
// executed twice when a browser, proxy, or user retries after an ambiguous
// network failure. Successful responses are replayed without invoking the
// controller again.
type IdempotencyRecord struct {
	ID           uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	TenantCode   string    `gorm:"type:varchar(32);not null;uniqueIndex:uidx_idempotency_scope,priority:1" json:"-"`
	ActorSub     string    `gorm:"type:varchar(128);not null;uniqueIndex:uidx_idempotency_scope,priority:2" json:"-"`
	Method       string    `gorm:"type:varchar(12);not null;uniqueIndex:uidx_idempotency_scope,priority:3" json:"-"`
	Route        string    `gorm:"type:varchar(512);not null;uniqueIndex:uidx_idempotency_scope,priority:4" json:"-"`
	Key          string    `gorm:"type:varchar(128);not null;uniqueIndex:uidx_idempotency_scope,priority:5" json:"-"`
	RequestHash  string    `gorm:"type:char(64);not null" json:"-"`
	State        string    `gorm:"type:varchar(16);not null;index" json:"-"`
	StatusCode   int       `gorm:"not null;default:0" json:"-"`
	ContentType  string    `gorm:"type:varchar(128);not null;default:''" json:"-"`
	ResponseBody []byte    `gorm:"type:bytea" json:"-"`
	CreatedAt    time.Time `gorm:"not null;index" json:"-"`
	UpdatedAt    time.Time `gorm:"not null" json:"-"`
	ExpiresAt    time.Time `gorm:"not null;index" json:"-"`
}
