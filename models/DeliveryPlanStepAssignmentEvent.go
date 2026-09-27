package models

import (
	"time"

	"github.com/gofrs/uuid"
)

const (
	DeliveryPlanStepAssignmentEventCreated                = "assignment_created"
	DeliveryPlanStepAssignmentEventStatusChanged          = "status_changed"
	DeliveryPlanStepAssignmentEventTargetChanged          = "target_changed"
	DeliveryPlanStepAssignmentEventStatusAndTargetChanged = "status_and_target_changed"
)

// DeliveryPlanStepAssignmentEvent is an append-only database audit record for
// the dispatch state and target of a plan-step assignment. PostgreSQL writes
// these rows from a trigger, so direct SQL and GORM map updates are observed
// equally. It intentionally stores no request, output, credential, path, or
// arbitrary JSON payload.
type DeliveryPlanStepAssignmentEvent struct {
	ID                      uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	AssignmentID            uuid.UUID `gorm:"type:uuid;not null;index:idx_delivery_plan_step_assignment_event_timeline,priority:1" json:"-"`
	ExecutionID             uuid.UUID `gorm:"type:uuid;not null;index" json:"-"`
	PlanID                  uuid.UUID `gorm:"type:uuid;not null;index:idx_delivery_plan_step_assignment_event_timeline,priority:1" json:"-"`
	PlanVersion             int       `gorm:"not null;check:delivery_plan_step_assignment_event_version_positive,plan_version > 0" json:"-"`
	StepID                  uuid.UUID `gorm:"type:uuid;not null;index" json:"-"`
	ParentAutomationTaskID  uuid.UUID `gorm:"type:uuid;not null;index" json:"-"`
	ChildAutomationTaskID   uuid.UUID `gorm:"type:uuid;not null;index" json:"-"`
	EventType               string    `gorm:"type:varchar(40);not null;check:delivery_plan_step_assignment_event_type_allowed,event_type IN ('assignment_created','status_changed','target_changed','status_and_target_changed')" json:"-"`
	PreviousStatus          string    `gorm:"type:varchar(24);not null;default:''" json:"-"`
	Status                  string    `gorm:"type:varchar(24);not null;index" json:"-"`
	PreviousTargetAgentKey  string    `gorm:"type:varchar(64);not null;default:''" json:"-"`
	TargetAgentKey          string    `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	PreviousTargetMachineID string    `gorm:"type:varchar(64);not null;default:''" json:"-"`
	TargetMachineID         string    `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	OccurredAt              time.Time `gorm:"not null;index:idx_delivery_plan_step_assignment_event_timeline,priority:2" json:"-"`
	CreatedAt               time.Time `gorm:"not null" json:"-"`
}
