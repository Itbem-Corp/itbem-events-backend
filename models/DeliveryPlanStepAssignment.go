package models

import (
	"time"

	"github.com/gofrs/uuid"
)

const (
	DeliveryPlanStepAssignmentPending    = "pending"
	DeliveryPlanStepAssignmentQueued     = "queued"
	DeliveryPlanStepAssignmentDispatched = "dispatched"
	DeliveryPlanStepAssignmentRunning    = "running"
	DeliveryPlanStepAssignmentBlocked    = "blocked"
	DeliveryPlanStepAssignmentCompleted  = "completed"
	DeliveryPlanStepAssignmentFailed     = "failed"
	DeliveryPlanStepAssignmentCancelled  = "cancelled"
)

// DeliveryPlanStepAssignment binds one step in a frozen plan execution to
// exactly one child AutomationTask. Its unique constraints let dispatchers
// safely retry inserts and let fan-in locate the one outcome per step. This
// row is the authoritative dispatch binding for a targeted child task,
// including its stable machine/profile target. WorkerID stays ephemeral and
// lives only on the task lease, so a same-machine replacement can recover it.
//
// Generic JSON serialization is disabled. APIs must explicitly project this
// operational metadata and never expose internal queue/task identifiers by
// accident.
type DeliveryPlanStepAssignment struct {
	ID                    uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	ExecutionID           uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_plan_step_assignment_execution_step,priority:1;index:idx_delivery_plan_step_assignment_queue,priority:1" json:"-"`
	DeliveryPlanStepID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_plan_step_assignment_execution_step,priority:2;index" json:"-"`
	ChildAutomationTaskID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_plan_step_assignment_child_task" json:"-"`
	// TargetMachineID is a stable, opaque machine identity selected from recent
	// server-side heartbeats. WorkerID is deliberately not persisted here: it is
	// process-scoped and changes after restart, while the machine target remains
	// recoverable by a replacement worker on the same local team machine.
	TargetMachineID string     `gorm:"type:varchar(64);not null;default:'';index:idx_delivery_plan_step_assignment_target,priority:1" json:"-"`
	TargetAgentKey  string     `gorm:"type:varchar(64);not null;default:'';index:idx_delivery_plan_step_assignment_target,priority:2" json:"-"`
	Status          string     `gorm:"type:varchar(24);not null;default:'pending';index:idx_delivery_plan_step_assignment_queue,priority:2;check:delivery_plan_step_assignment_status_allowed,status IN ('pending','queued','dispatched','running','blocked','completed','failed','cancelled')" json:"-"`
	QueuedAt        *time.Time `gorm:"index" json:"-"`
	DispatchedAt    *time.Time `gorm:"index" json:"-"`
	StartedAt       *time.Time `gorm:"index" json:"-"`
	CompletedAt     *time.Time `gorm:"index" json:"-"`
	CreatedAt       time.Time  `gorm:"not null;index" json:"-"`
	UpdatedAt       time.Time  `gorm:"not null" json:"-"`

	Execution           DeliveryPlanExecution `gorm:"foreignKey:ExecutionID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
	Step                DeliveryPlanStep      `gorm:"foreignKey:DeliveryPlanStepID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
	ChildAutomationTask AutomationTask        `gorm:"foreignKey:ChildAutomationTaskID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
}
