package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// AutomationInferenceAttemptPolicy is the immutable, credential-free routing
// snapshot captured for one automation task run. It deliberately contains no
// provider credentials and is not exposed through JSON serialization.
type AutomationInferenceAttemptPolicy struct {
	ID                  uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	AutomationTaskID    uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_automation_inference_attempt_policy_task_run,priority:1" json:"-"`
	RunID               string     `gorm:"type:varchar(64);not null;uniqueIndex:idx_automation_inference_attempt_policy_task_run,priority:2" json:"-"`
	Operation           string     `gorm:"type:varchar(96);not null" json:"-"`
	ProjectID           *uuid.UUID `gorm:"type:uuid" json:"-"`
	PolicyRevision      int64      `gorm:"not null" json:"-"`
	RoutesJSON          string     `gorm:"type:jsonb;not null" json:"-"`
	RoutesHash          string     `gorm:"type:char(64);not null" json:"-"`
	MaxCompletionTokens int        `gorm:"not null" json:"-"`
	// MaxInferenceCalls is the per-run durable gateway-call quota. It is part
	// of the signed snapshot so refreshing a worker capability cannot increase it.
	MaxInferenceCalls int       `gorm:"not null;default:0" json:"-"`
	SnapshotHash      string    `gorm:"type:char(64);not null" json:"-"`
	SignatureKeyID    string    `gorm:"type:varchar(16);not null;default:''" json:"-"`
	SnapshotSignature string    `gorm:"type:char(64);not null;default:''" json:"-"`
	CreatedAt         time.Time `gorm:"not null" json:"-"`
}
