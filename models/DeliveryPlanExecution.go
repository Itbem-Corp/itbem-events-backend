package models

import (
	"time"

	"github.com/gofrs/uuid"
)

const (
	DeliveryPlanExecutionPending     = "pending"
	DeliveryPlanExecutionDispatching = "dispatching"
	DeliveryPlanExecutionRunning     = "running"
	DeliveryPlanExecutionCompleted   = "completed"
	DeliveryPlanExecutionFailed      = "failed"
	DeliveryPlanExecutionCancelled   = "cancelled"

	// DeliveryPlanExecutionMaxConcurrency caps one approved-plan fan-out.
	// It is a schema-level guardrail, not a scheduler or live capacity signal.
	DeliveryPlanExecutionMaxConcurrency = 64
)

// DeliveryPlanExecution freezes the approved plan identity and approval gate
// used by one parent AutomationTask. The parent task and idempotency key form
// the durable dispatch identity across queue redeliveries. This record only
// defines the persisted contract; it does not implement dispatch, worker
// affinity, capacity scheduling, or fan-in.
//
// Generic JSON serialization is disabled so APIs must explicitly project
// execution metadata and cannot accidentally expose internal identifiers.
type DeliveryPlanExecution struct {
	ID               uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	AutomationTaskID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_plan_execution_parent;uniqueIndex:idx_delivery_plan_execution_idempotency,priority:1;check:delivery_plan_execution_task_id_not_nil,automation_task_id <> '00000000-0000-0000-0000-000000000000'" json:"-"`
	IdempotencyKey   string    `gorm:"type:varchar(128);not null;uniqueIndex:idx_delivery_plan_execution_idempotency,priority:2;check:delivery_plan_execution_idempotency_key_not_blank,length(btrim(idempotency_key)) > 0" json:"-"`
	PlanID           uuid.UUID `gorm:"type:uuid;not null;index" json:"-"`
	PlanVersion      int       `gorm:"not null;check:delivery_plan_execution_version_positive,plan_version > 0" json:"-"`
	ApprovedGateID   uuid.UUID `gorm:"type:uuid;not null;index;check:delivery_plan_execution_gate_id_not_nil,approved_gate_id <> '00000000-0000-0000-0000-000000000000'" json:"-"`
	// PlanHash is the lowercase SHA-256 digest of the exact approved plan snapshot.
	PlanHash       string     `gorm:"type:varchar(64);not null;check:delivery_plan_execution_hash_sha256,plan_hash ~ '^[0-9a-f]{64}$'" json:"-"`
	MaxConcurrency int        `gorm:"not null;default:1;check:delivery_plan_execution_concurrency_bounded,max_concurrency BETWEEN 1 AND 64" json:"-"`
	Status         string     `gorm:"type:varchar(24);not null;default:'pending';index;check:delivery_plan_execution_status_allowed,status IN ('pending','dispatching','running','completed','failed','cancelled')" json:"-"`
	DispatchedAt   *time.Time `gorm:"index" json:"-"`
	StartedAt      *time.Time `gorm:"index" json:"-"`
	CompletedAt    *time.Time `gorm:"index" json:"-"`
	CreatedAt      time.Time  `gorm:"not null;index" json:"-"`
	UpdatedAt      time.Time  `gorm:"not null" json:"-"`

	AutomationTask AutomationTask               `gorm:"foreignKey:AutomationTaskID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
	Plan           DeliveryPlan                 `gorm:"foreignKey:PlanID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
	ApprovedGate   DeliveryGate                 `gorm:"foreignKey:ApprovedGateID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
	Assignments    []DeliveryPlanStepAssignment `gorm:"foreignKey:ExecutionID;references:ID" json:"-"`
}
