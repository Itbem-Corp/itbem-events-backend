package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// AutomationAIActionPolicyRevision is an immutable, credential-free snapshot
// of one administrator-approved action routing policy revision.
type AutomationAIActionPolicyRevision struct {
	ID         uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	PolicyID   uuid.UUID `gorm:"type:uuid;not null;index" json:"-"`
	Operation  string    `gorm:"type:varchar(96);not null;uniqueIndex:idx_automation_ai_policy_revision,priority:1" json:"operation"`
	Revision   int64     `gorm:"not null;uniqueIndex:idx_automation_ai_policy_revision,priority:2" json:"revision"`
	RoutesJSON string    `gorm:"type:jsonb;not null" json:"-"`
	RoutesHash string    `gorm:"type:char(64);not null" json:"routes_hash"`
	ChangedBy  string    `gorm:"type:varchar(128);not null" json:"changed_by"`
	CreatedAt  time.Time `gorm:"not null;index" json:"created_at"`
}
