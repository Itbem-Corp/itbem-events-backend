package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// DeliveryPlanStepEvidence is server-verified, append-only metadata for a
// private artifact produced by the worker currently holding the step lease.
// Storage coordinates are server-only and must never appear in API DTOs.
type DeliveryPlanStepEvidence struct {
	ID               uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	EventID          uuid.UUID `gorm:"type:uuid;not null;uniqueIndex;check:delivery_step_evidence_event_id_not_nil,event_id <> '00000000-0000-0000-0000-000000000000'" json:"-"`
	PlanID           uuid.UUID `gorm:"type:uuid;not null;index:idx_delivery_step_evidence_timeline,priority:1" json:"-"`
	PlanVersion      int       `gorm:"not null;check:delivery_step_evidence_plan_version_positive,plan_version > 0" json:"-"`
	StepID           uuid.UUID `gorm:"type:uuid;not null;index:idx_delivery_step_evidence_timeline,priority:2" json:"-"`
	RequirementKey   string    `gorm:"type:varchar(48);not null;index" json:"-"`
	AutomationTaskID uuid.UUID `gorm:"type:uuid;not null;index" json:"-"`
	RunID            string    `gorm:"type:uuid;not null;index:idx_delivery_step_evidence_timeline,priority:3" json:"-"`
	WorkerID         string    `gorm:"type:uuid;not null;index" json:"-"`
	AgentKey         string    `gorm:"type:varchar(64);not null;index" json:"-"`
	MachineID        string    `gorm:"type:varchar(64);not null" json:"-"`
	AgentInstanceID  uuid.UUID `gorm:"type:uuid;not null;index" json:"-"`
	FencingToken     int64     `gorm:"not null;check:delivery_step_evidence_fence_positive,fencing_token > 0" json:"-"`
	FileName         string    `gorm:"type:varchar(160);not null" json:"-"`
	ContentType      string    `gorm:"type:varchar(96);not null" json:"-"`
	Bucket           string    `gorm:"type:varchar(255);not null" json:"-"`
	ObjectKey        string    `gorm:"type:text;not null" json:"-"`
	SHA256           string    `gorm:"type:varchar(64);not null;index;check:delivery_step_evidence_sha256,sha256 ~ '^[0-9a-f]{64}$'" json:"-"`
	SizeBytes        int64     `gorm:"not null;check:delivery_step_evidence_size_bounded,size_bytes BETWEEN 1 AND 1048576" json:"-"`
	CreatedAt        time.Time `gorm:"not null;index:idx_delivery_step_evidence_timeline,priority:4" json:"-"`
}

func (DeliveryPlanStepEvidence) TableName() string { return "delivery_plan_step_evidences" }
