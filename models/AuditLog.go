package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// AuditLog is an append-only security trail for authenticated mutations.
// Request and response bodies are deliberately excluded so credentials,
// invitation tokens, and customer content never become audit payloads.
type AuditLog struct {
	ID              uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	OccurredAt      time.Time  `gorm:"not null;default:CURRENT_TIMESTAMP;index" json:"occurred_at"`
	ActorUserID     *uuid.UUID `gorm:"type:uuid;index" json:"actor_user_id,omitempty"`
	ActorCognitoSub string     `gorm:"type:varchar(128);not null;index" json:"-"`
	TenantCode      string     `gorm:"type:varchar(32);not null;index" json:"tenant_code"`
	Method          string     `gorm:"type:varchar(12);not null" json:"method"`
	Route           string     `gorm:"type:varchar(512);not null;index" json:"route"`
	ResourceType    string     `gorm:"type:varchar(64);not null;default:'';index" json:"resource_type,omitempty"`
	ResourceID      string     `gorm:"type:varchar(128);not null;default:'';index" json:"resource_id,omitempty"`
	Status          int        `gorm:"not null;index" json:"status"`
	Succeeded       bool       `gorm:"not null;index" json:"succeeded"`
	RequestID       string     `gorm:"type:varchar(64);not null;index" json:"request_id"`
	ClientIP        string     `gorm:"type:varchar(64);not null;default:''" json:"client_ip"`
	UserAgent       string     `gorm:"type:varchar(512);not null;default:''" json:"user_agent"`
}
