package models

import (
	"errors"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

var ErrAutomationTaskEventAppendOnly = errors.New("automation task events are append-only")

// AutomationTaskEvent is a structured, credential-free record of task state,
// lease, or agent-assignment changes. The database trigger is the authoritative
// writer so every code path that mutates automation_tasks is captured.
type AutomationTaskEvent struct {
	ID                       uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	AutomationTaskID         uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_automation_task_events_task_sequence,priority:1;index" json:"-"`
	Sequence                 int64      `gorm:"not null;uniqueIndex:idx_automation_task_events_task_sequence,priority:2" json:"-"`
	EventType                string     `gorm:"type:varchar(32);not null;index" json:"-"`
	PreviousStatus           string     `gorm:"type:varchar(16);not null;default:''" json:"-"`
	Status                   string     `gorm:"type:varchar(16);not null" json:"-"`
	PreviousRunID            string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	RunID                    string     `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	PreviousWorkerID         string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	WorkerID                 string     `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	PreviousAgentKey         string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	AgentKey                 string     `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	PreviousMachineID        string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	MachineID                string     `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	PreviousAgentInstanceID  *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	AgentInstanceID          *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	AttemptCount             int        `gorm:"not null;default:0" json:"-"`
	OccurredAt               time.Time  `gorm:"not null;index" json:"-"`
	CreatedAt                time.Time  `gorm:"not null" json:"-"`
}

func (AutomationTaskEvent) BeforeUpdate(*gorm.DB) error { return ErrAutomationTaskEventAppendOnly }

func (AutomationTaskEvent) BeforeDelete(*gorm.DB) error { return ErrAutomationTaskEventAppendOnly }
