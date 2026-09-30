package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// AutomationInferenceReceipt is the durable at-most-once reservation and
// accounting record for one gateway call. It intentionally has no prompt,
// completion body, or provider credential. A reserved row consumes one slot;
// only the gateway may transition it once to an observed outcome.
type AutomationInferenceReceipt struct {
	ID               uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	AutomationTaskID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_automation_inference_receipt_call,priority:1;index" json:"-"`
	RunID            string    `gorm:"type:varchar(64);not null;uniqueIndex:idx_automation_inference_receipt_call,priority:2;index" json:"-"`
	CallID           uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_automation_inference_receipt_call,priority:3" json:"-"`
	// PlanStepID is nullable for planning and other task-level inference. When
	// present, the gateway populated it only after validating the live step
	// lease/assignment for this exact task run and worker identity.
	PlanStepID           *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	Operation            string     `gorm:"type:varchar(96);not null" json:"-"`
	WorkerID             string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	AgentKey             string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	MachineID            string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	PolicySnapshotHash   string     `gorm:"type:char(64);not null" json:"-"`
	QuotaLimit           int        `gorm:"not null" json:"-"`
	Status               string     `gorm:"type:varchar(16);not null;index" json:"status"`
	Provider             string     `gorm:"type:varchar(48);not null;default:''" json:"provider,omitempty"`
	Model                string     `gorm:"type:varchar(128);not null;default:''" json:"model,omitempty"`
	ProviderResponseID   string     `gorm:"type:varchar(128);not null;default:''" json:"provider_response_id,omitempty"`
	InputTokens          int64      `gorm:"not null;default:0" json:"input_tokens"`
	OutputTokens         int64      `gorm:"not null;default:0" json:"output_tokens"`
	CachedInputTokens    int64      `gorm:"not null;default:0" json:"cached_input_tokens"`
	CacheWriteTokens     int64      `gorm:"not null;default:0" json:"cache_write_tokens"`
	ReasoningTokens      int64      `gorm:"not null;default:0" json:"reasoning_tokens"`
	TotalTokens          int64      `gorm:"not null;default:0" json:"total_tokens"`
	InputCostMicros      int64      `gorm:"not null;default:0" json:"input_cost_microusd"`
	OutputCostMicros     int64      `gorm:"not null;default:0" json:"output_cost_microusd"`
	CachedCostMicros     int64      `gorm:"not null;default:0" json:"cached_cost_microusd"`
	CacheWriteCostMicros int64      `gorm:"not null;default:0" json:"cache_write_cost_microusd"`
	TotalCostMicros      int64      `gorm:"not null;default:0" json:"total_cost_microusd"`
	Currency             string     `gorm:"type:char(3);not null;default:'USD'" json:"currency"`
	PricingBasis         string     `gorm:"type:text;not null;default:'unpriced'" json:"pricing_basis"`
	PricingSnapshotJSON  string     `gorm:"type:jsonb;not null;default:'{}'" json:"pricing_snapshot,omitempty"`
	// UsageJSON is provider accounting metadata only; prompts and answers are
	// never copied into this ledger.
	UsageJSON  string     `gorm:"type:jsonb;not null;default:'{}'" json:"-"`
	CreatedAt  time.Time  `gorm:"not null;index" json:"created_at"`
	ResolvedAt *time.Time `gorm:"index" json:"resolved_at,omitempty"`
}
