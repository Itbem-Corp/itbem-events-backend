package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// AutomationToolExecution is the immutable ledger for a billable provider
// call made by an approved execution tool (for example Stagehand browser QA).
// It is deliberately separate from AutomationExecution: the latter records
// the task's primary agent response, while a tool can make its own model call
// during the same run without overwriting or conflating either audit trail.
type AutomationToolExecution struct {
	ID                 uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	AutomationTaskID   uuid.UUID  `gorm:"type:uuid;not null;index;uniqueIndex:automation_tool_execution_call" json:"automation_task_id"`
	DeliveryWorkItemID *uuid.UUID `gorm:"type:uuid;index" json:"delivery_work_item_id,omitempty"`
	RunID              string     `gorm:"type:varchar(64);not null;default:'';uniqueIndex:automation_tool_execution_call" json:"-"`
	Tool               string     `gorm:"type:varchar(64);not null;uniqueIndex:automation_tool_execution_call" json:"tool"`
	// CallKey identifies one provider call within a tool run. A semantic QA
	// runner can therefore record each inference separately (for example an
	// assessment and a bounded retry) without merging their cost or evidence.
	CallKey string `gorm:"type:varchar(64);not null;default:'';uniqueIndex:automation_tool_execution_call" json:"call_key"`
	// CallStatus describes this individual provider call, independent from the
	// enclosing QA task. A rejected structured response can still be a real,
	// billable inference and must remain auditable as failed rather than vanish
	// from the ledger.
	CallStatus           string `gorm:"type:varchar(16);not null;default:'completed'" json:"call_status"`
	StepKey              string `gorm:"type:varchar(64);not null;default:'';index" json:"step_key,omitempty"`
	Provider             string `gorm:"type:varchar(48);not null" json:"provider"`
	Model                string `gorm:"type:varchar(128);not null" json:"model"`
	InputTokens          int64  `gorm:"not null;default:0" json:"input_tokens"`
	OutputTokens         int64  `gorm:"not null;default:0" json:"output_tokens"`
	CachedInputTokens    int64  `gorm:"not null;default:0" json:"cached_input_tokens"`
	CacheWriteTokens     int64  `gorm:"not null;default:0" json:"cache_write_tokens"`
	ReasoningTokens      int64  `gorm:"not null;default:0" json:"reasoning_tokens"`
	TotalTokens          int64  `gorm:"not null;default:0" json:"total_tokens"`
	InputCostMicros      int64  `gorm:"not null;default:0" json:"input_cost_microusd"`
	OutputCostMicros     int64  `gorm:"not null;default:0" json:"output_cost_microusd"`
	CachedCostMicros     int64  `gorm:"not null;default:0" json:"cached_cost_microusd"`
	CacheWriteCostMicros int64  `gorm:"not null;default:0" json:"cache_write_cost_microusd"`
	TotalCostMicros      int64  `gorm:"not null;default:0;index" json:"total_cost_microusd"`
	Currency             string `gorm:"type:char(3);not null;default:'USD'" json:"currency"`
	PricingBasis         string `gorm:"type:varchar(32);not null;default:'unpriced'" json:"pricing_basis"`
	PricingSnapshotJSON  string `gorm:"type:jsonb;not null;default:'{}'" json:"pricing_snapshot,omitempty"`
	// The task trace exposes only an allow-listed provider outcome derived from
	// this JSON. Raw provider metadata remains server-side with the ledger.
	UsageJSON string `gorm:"type:jsonb;not null;default:'{}'" json:"-"`
	// Stagehand's request description and sanitized structured result share a
	// private QA report artifact. These references intentionally stay out of
	// serialized traces; reviewers use the task's protected evidence endpoint.
	RequestRef  string    `gorm:"type:text;not null" json:"-"`
	ResponseRef string    `gorm:"type:text;not null" json:"-"`
	CompletedAt time.Time `gorm:"not null;index" json:"completed_at"`
	CreatedAt   time.Time `gorm:"not null;index" json:"created_at"`
}
