package models

import (
	"time"

	"github.com/gofrs/uuid"
)

const (
	DeliveryPlanStepRoleImplementation = "implementation"
	DeliveryPlanStepRoleIntegration    = "integration"

	DeliveryPlanStepPlanned   = "planned"
	DeliveryPlanStepReady     = "ready"
	DeliveryPlanStepRunning   = "running"
	DeliveryPlanStepBlocked   = "blocked"
	DeliveryPlanStepCompleted = "completed"
	DeliveryPlanStepFailed    = "failed"
	DeliveryPlanStepSkipped   = "skipped"
)

// DeliveryPlanStep is a normalized, version-scoped unit of approved plan
// content. A step belongs to a DeliveryPlan (and therefore to its work item),
// so standalone work items do not need an Epic or Decomposition. Generic JSON
// serialization is disabled: delivery APIs must use an explicit DTO and never
// expose private execution metadata or future internal fields by accident.
type DeliveryPlanStep struct {
	ID                       uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	PlanID                   uuid.UUID  `gorm:"type:uuid;not null;index:idx_delivery_plan_step_plan_order,priority:1;uniqueIndex:idx_delivery_plan_step_key,priority:1;uniqueIndex:idx_delivery_plan_step_idempotency,priority:1" json:"-"`
	StepKey                  string     `gorm:"type:varchar(64);not null;uniqueIndex:idx_delivery_plan_step_key,priority:2" json:"-"`
	IdempotencyKey           string     `gorm:"type:varchar(128);not null;uniqueIndex:idx_delivery_plan_step_idempotency,priority:2" json:"-"`
	Role                     string     `gorm:"type:varchar(24);not null;default:'implementation';check:delivery_plan_step_role_allowed,role IN ('implementation','integration')" json:"-"`
	DisplayOrder             int        `gorm:"not null;uniqueIndex:idx_delivery_plan_step_plan_order,priority:2" json:"-"`
	Title                    string     `gorm:"type:varchar(240);not null" json:"-"`
	Objective                string     `gorm:"type:text;not null;default:''" json:"-"`
	AcceptanceCriteriaJSON   string     `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	EvidenceRequirementsJSON string     `gorm:"type:jsonb;not null;default:'[]';check:delivery_plan_step_evidence_requirements_array,jsonb_typeof(evidence_requirements_json) = 'array'" json:"-"`
	Status                   string     `gorm:"type:varchar(24);not null;default:'planned';index" json:"-"`
	AgentKey                 string     `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	WorkerID                 string     `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	MachineID                string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	AutomationTaskID         *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	AutomationExecutionID    *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	RunID                    string     `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	LeaseFence               int64      `gorm:"not null;default:0" json:"-"`
	LeaseExpiresAt           *time.Time `gorm:"index" json:"-"`
	CreatedBy                string     `gorm:"type:varchar(128);not null;default:''" json:"-"`
	StartedAt                *time.Time `gorm:"index" json:"-"`
	CompletedAt              *time.Time `gorm:"index" json:"-"`
	CreatedAt                time.Time  `gorm:"not null" json:"-"`
	UpdatedAt                time.Time  `gorm:"not null" json:"-"`

	Plan         DeliveryPlan                 `gorm:"foreignKey:PlanID;references:ID" json:"-"`
	Dependencies []DeliveryPlanStepDependency `gorm:"foreignKey:StepID;references:ID" json:"-"`
}

// DeliveryPlanStepDependency is a normalized directed edge. The service
// validates that both endpoints share PlanID and that the combined graph is
// acyclic before persisting; the check constraint prevents self-dependencies
// even for writers that bypass that service.
type DeliveryPlanStepDependency struct {
	ID              uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	PlanID          uuid.UUID `gorm:"type:uuid;not null;index:idx_delivery_plan_step_dependency_plan;uniqueIndex:idx_delivery_plan_step_dependency,priority:1" json:"-"`
	StepID          uuid.UUID `gorm:"type:uuid;not null;index;uniqueIndex:idx_delivery_plan_step_dependency,priority:2;check:delivery_plan_step_dependency_not_self,step_id <> depends_on_step_id" json:"-"`
	DependsOnStepID uuid.UUID `gorm:"type:uuid;not null;index;uniqueIndex:idx_delivery_plan_step_dependency,priority:3" json:"-"`
	CreatedAt       time.Time `gorm:"not null" json:"-"`

	Plan          DeliveryPlan     `gorm:"foreignKey:PlanID;references:ID" json:"-"`
	Step          DeliveryPlanStep `gorm:"foreignKey:StepID;references:ID" json:"-"`
	DependsOnStep DeliveryPlanStep `gorm:"foreignKey:DependsOnStepID;references:ID" json:"-"`
}
