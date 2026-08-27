package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// AutomationExecution is the immutable financial and technical ledger entry
// for one billable model call. It also records a provider response that later
// fails a workflow contract, because its tokens and cost are still real.
// AutomationTask is the durable workflow unit; a task may gain more than one
// execution as the agent becomes multi-step or retries after a recovered lease.
// Amounts use integer micro-USD so the audit trail never depends on floats.
type AutomationExecution struct {
	ID                 uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	AutomationTaskID   uuid.UUID  `gorm:"type:uuid;not null;index;uniqueIndex:automation_execution_run" json:"automation_task_id"`
	DeliveryWorkItemID *uuid.UUID `gorm:"type:uuid;index" json:"delivery_work_item_id,omitempty"`
	// RunID is the immutable worker lease that produced this call. It is the
	// ledger idempotency boundary: providers such as MiniMax may omit a stable
	// response ID, while a task may validly have multiple calls or retries.
	RunID                string `gorm:"type:varchar(64);not null;default:'';uniqueIndex:automation_execution_run" json:"-"`
	StepKey              string `gorm:"type:varchar(64);not null;default:'';index" json:"step_key,omitempty"`
	Provider             string `gorm:"type:varchar(48);not null" json:"provider"`
	Model                string `gorm:"type:varchar(128);not null" json:"model"`
	ProviderResponseID   string `gorm:"type:varchar(128);not null;default:'';index" json:"provider_response_id,omitempty"`
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
	// UsageJSON is internal provider accounting metadata. The trace endpoint
	// projects an explicitly allow-listed outcome from it; never serialize the
	// raw object because provider-specific extensions are not a browser API.
	UsageJSON string `gorm:"type:jsonb;not null;default:'{}'" json:"-"`
	// Object references are deliberately never serialized in a task trace.
	// Authorized users inspect their bounded request/result through dedicated
	// API endpoints, which validate access and read the immutable object. This
	// avoids exposing internal bucket/key topology to browsers or logs.
	RequestRef  string    `gorm:"type:text;not null" json:"-"`
	ResponseRef string    `gorm:"type:text;not null" json:"-"`
	CompletedAt time.Time `gorm:"not null;index" json:"completed_at"`
	CreatedAt   time.Time `gorm:"not null;index" json:"created_at"`
}
