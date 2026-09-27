package models

import (
	"context"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

const (
	DeliveryPlanStepEventReady          = "step_ready"
	DeliveryPlanStepEventClaimed        = "step_claimed"
	DeliveryPlanStepEventLeaseReclaimed = "lease_reclaimed"
	DeliveryPlanStepEventLeaseRenewed   = "lease_renewed"
	DeliveryPlanStepEventTransitioned   = "status_transitioned"
)

// DeliveryPlanStepEvent is an append-only, allow-listed step lifecycle record.
// It stores status changes and opaque worker attribution only—never prompts,
// provider payloads, credentials, or arbitrary JSON blobs.
type DeliveryPlanStepEvent struct {
	ID               uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	PlanID           uuid.UUID  `gorm:"type:uuid;not null;index:idx_delivery_plan_step_event_timeline,priority:1" json:"-"`
	StepID           uuid.UUID  `gorm:"type:uuid;not null;index:idx_delivery_plan_step_event_timeline,priority:2" json:"-"`
	EventType        string     `gorm:"type:varchar(32);not null;index" json:"-"`
	FromStatus       string     `gorm:"type:varchar(24);not null;default:''" json:"-"`
	ToStatus         string     `gorm:"type:varchar(24);not null;default:'';index" json:"-"`
	AutomationTaskID uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	RunID            string     `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	WorkerID         string     `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	AgentKey         string     `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	MachineID        string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	AgentInstanceID  *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	LeaseFence       int64      `gorm:"not null;default:0" json:"-"`
	Summary          string     `gorm:"type:varchar(160);not null;default:''" json:"-"`
	OccurredAt       time.Time  `gorm:"not null;index:idx_delivery_plan_step_event_timeline,priority:3" json:"-"`
	CreatedAt        time.Time  `gorm:"not null" json:"-"`

	Plan DeliveryPlan     `gorm:"foreignKey:PlanID;references:ID" json:"-"`
	Step DeliveryPlanStep `gorm:"foreignKey:StepID;references:ID" json:"-"`
}

type deliveryPlanStepAgentInstanceContextKey struct{}

// WithDeliveryPlanStepAgentInstanceID attaches verified callback identity to
// the transaction context so lifecycle rows created deeper in the service
// layer can be attributed without trusting request-body fields.
func WithDeliveryPlanStepAgentInstanceID(ctx context.Context, instanceID uuid.UUID) context.Context {
	if ctx == nil || instanceID == uuid.Nil {
		return ctx
	}
	return context.WithValue(ctx, deliveryPlanStepAgentInstanceContextKey{}, instanceID)
}

// BeforeCreate copies authenticated callback identity from the GORM context
// onto newly appended lifecycle events. Existing records and non-callback
// service writes remain unattributed.
func (event *DeliveryPlanStepEvent) BeforeCreate(tx *gorm.DB) error {
	if event == nil || event.AgentInstanceID != nil || tx == nil || tx.Statement == nil || tx.Statement.Context == nil {
		return nil
	}
	instanceID, ok := tx.Statement.Context.Value(deliveryPlanStepAgentInstanceContextKey{}).(uuid.UUID)
	if ok && instanceID != uuid.Nil {
		event.AgentInstanceID = &instanceID
	}
	return nil
}
