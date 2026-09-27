package automation

import (
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
)

// automationTraceTask is an explicit browser/API projection of an automation
// task. In particular, it must never inherit JSON serialization from
// AutomationTask: input/output object references, provider metadata, raw usage,
// requesters, and diagnostics are not part of this trace envelope.
type automationTraceTask struct {
	ID                 uuid.UUID  `json:"id"`
	DeliveryWorkItemID *uuid.UUID `json:"delivery_work_item_id,omitempty"`
	CorrelationID      string     `json:"correlation_id"`
	Operation          string     `json:"operation"`
	Status             string     `json:"status"`
	ProgressStep       string     `json:"progress_step,omitempty"`
	ProgressCall       int        `json:"progress_call,omitempty"`
	AttemptCount       int        `json:"attempt_count"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

func automationTraceTaskFrom(task models.AutomationTask) automationTraceTask {
	return automationTraceTask{
		ID: task.ID, DeliveryWorkItemID: task.DeliveryWorkItemID,
		CorrelationID: task.CorrelationID, Operation: task.Operation, Status: task.Status,
		ProgressStep: task.ProgressStep, ProgressCall: task.ProgressCall, AttemptCount: task.AttemptCount,
		CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt, CompletedAt: task.CompletedAt,
	}
}
