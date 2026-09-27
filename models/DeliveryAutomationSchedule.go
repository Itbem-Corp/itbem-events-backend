package models

import (
	"errors"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

const (
	DeliveryAutomationScheduleActive = "active"
	DeliveryAutomationSchedulePaused = "paused"
	DeliveryAutomationScheduleEnded  = "ended"

	DeliveryAutomationScheduleOccurrenceMaterialized = "materialized"
	DeliveryAutomationScheduleOccurrenceBlocked      = "blocked"
	DeliveryAutomationScheduleOccurrenceSkipped      = "skipped"
)

var ErrDeliveryAutomationScheduleEventImmutable = errors.New("delivery automation schedule events are append-only")
var ErrDeliveryAutomationScheduleOccurrenceImmutable = errors.New("delivery automation schedule occurrences are append-only")

// DeliveryAutomationSchedule is a project-scoped template for creating new,
// human-gated work items. Its JSON contract is deliberately projected by the
// delivery controller; serialized model output must never expose stored JSON
// blobs accidentally.
type DeliveryAutomationSchedule struct {
	ID             uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	ProjectID      uuid.UUID  `gorm:"type:uuid;not null;index:idx_delivery_automation_schedule_due,priority:1;index" json:"-"`
	Name           string     `gorm:"type:varchar(180);not null" json:"-"`
	Status         string     `gorm:"type:varchar(16);not null;default:'active';index:idx_delivery_automation_schedule_due,priority:2" json:"-"`
	Revision       int        `gorm:"not null;default:1" json:"-"`
	TemplateJSON   string     `gorm:"type:jsonb;not null;default:'{}'" json:"-"`
	RecurrenceJSON string     `gorm:"type:jsonb;not null;default:'{}'" json:"-"`
	TimeZone       string     `gorm:"type:varchar(128);not null" json:"-"`
	NextRunAt      *time.Time `gorm:"index:idx_delivery_automation_schedule_due,priority:3" json:"-"`
	LastRunAt      *time.Time `json:"-"`
	PausedAt       *time.Time `json:"-"`
	CreatedBy      string     `gorm:"type:varchar(128);not null;index" json:"-"`
	UpdatedBy      string     `gorm:"type:varchar(128);not null;default:''" json:"-"`
	CreatedAt      time.Time  `gorm:"not null;index" json:"-"`
	UpdatedAt      time.Time  `gorm:"not null" json:"-"`

	Project     DeliveryProject                        `gorm:"foreignKey:ProjectID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
	Occurrences []DeliveryAutomationScheduleOccurrence `gorm:"foreignKey:ScheduleID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
}

// DeliveryAutomationScheduleOccurrence is the durable idempotency ledger for
// one intended local wall-clock occurrence. Creation of this row, its work
// item, context snapshots, and the initial plan continuation is one database
// transaction. OccurrenceKey has a database unique index.
type DeliveryAutomationScheduleOccurrence struct {
	ID               uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	ScheduleID       uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_schedule_occurrence_key,priority:1;index:idx_delivery_schedule_occurrence_status,priority:1" json:"-"`
	ScheduleRevision int        `gorm:"not null;uniqueIndex:idx_delivery_schedule_occurrence_key,priority:2" json:"-"`
	OccurrenceKey    string     `gorm:"type:varchar(64);not null;uniqueIndex:idx_delivery_schedule_occurrence_key,priority:3" json:"-"`
	ScheduledFor     time.Time  `gorm:"not null;index" json:"-"`
	LocalOccurrence  string     `gorm:"type:varchar(64);not null" json:"-"`
	TimeZone         string     `gorm:"type:varchar(128);not null" json:"-"`
	TemplateJSON     string     `gorm:"type:jsonb;not null;default:'{}'" json:"-"`
	Status           string     `gorm:"type:varchar(20);not null;index:idx_delivery_schedule_occurrence_status,priority:2" json:"-"`
	WorkItemID       *uuid.UUID `gorm:"type:uuid;uniqueIndex" json:"-"`
	FailureCode      string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	CreatedAt        time.Time  `gorm:"not null;index" json:"-"`
	MaterializedAt   *time.Time `json:"-"`

	Schedule DeliveryAutomationSchedule `gorm:"foreignKey:ScheduleID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
	WorkItem *DeliveryWorkItem          `gorm:"foreignKey:WorkItemID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
}

func (DeliveryAutomationScheduleOccurrence) BeforeUpdate(*gorm.DB) error {
	return ErrDeliveryAutomationScheduleOccurrenceImmutable
}

func (DeliveryAutomationScheduleOccurrence) BeforeDelete(*gorm.DB) error {
	return ErrDeliveryAutomationScheduleOccurrenceImmutable
}

// DeliveryAutomationScheduleEvent is an append-only audit trail for schedule
// mutations and materialization outcomes. DetailsJSON may contain only
// allow-listed, non-secret metadata (revision, status, safe failure code).
type DeliveryAutomationScheduleEvent struct {
	ID           uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	ScheduleID   uuid.UUID  `gorm:"type:uuid;not null;index:idx_delivery_schedule_event_order,priority:1" json:"-"`
	OccurrenceID *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	WorkItemID   *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	EventType    string     `gorm:"type:varchar(40);not null;index" json:"-"`
	ActorSubject string     `gorm:"type:varchar(128);not null;default:''" json:"-"`
	DetailsJSON  string     `gorm:"type:jsonb;not null;default:'{}'" json:"-"`
	OccurredAt   time.Time  `gorm:"not null;index:idx_delivery_schedule_event_order,priority:2" json:"-"`

	Schedule   DeliveryAutomationSchedule            `gorm:"foreignKey:ScheduleID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
	Occurrence *DeliveryAutomationScheduleOccurrence `gorm:"foreignKey:OccurrenceID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
	WorkItem   *DeliveryWorkItem                     `gorm:"foreignKey:WorkItemID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
}

func (DeliveryAutomationScheduleEvent) BeforeUpdate(*gorm.DB) error {
	return ErrDeliveryAutomationScheduleEventImmutable
}

func (DeliveryAutomationScheduleEvent) BeforeDelete(*gorm.DB) error {
	return ErrDeliveryAutomationScheduleEventImmutable
}
