package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// AutomationTask records the cloud-side lifecycle of an ITBEM local-agent
// request. Inputs and outputs are object references, never the heavyweight or
// sensitive payload itself.
type AutomationTask struct {
	ID                  uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	JobID               uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex" json:"job_id"`
	RequestedBy         string     `gorm:"type:varchar(128);not null;index" json:"requested_by"`
	DeliveryWorkItemID  *uuid.UUID `gorm:"type:uuid;index" json:"delivery_work_item_id,omitempty"`
	CorrelationID       string     `gorm:"type:varchar(64);not null;index" json:"correlation_id"`
	Operation           string     `gorm:"type:varchar(96);not null;index" json:"operation"`
	MaxCompletionTokens int        `gorm:"not null;default:0" json:"max_completion_tokens"`
	// BudgetReservationMicros is the conservative upper bound held while this
	// non-deterministic run is queued or running. Once it completes, the
	// immutable execution ledger replaces the reservation with actual cost.
	BudgetReservationMicros int64 `gorm:"not null;default:0" json:"budget_reservation_microusd"`
	// BudgetReservationExpiresAt prevents an unavailable worker or abandoned
	// queue message from indefinitely consuming a project's admission budget.
	// A worker claim renews it alongside the opaque execution lease.
	BudgetReservationExpiresAt *time.Time `gorm:"index" json:"budget_reservation_expires_at,omitempty"`
	InputRef                   string     `gorm:"type:text;not null" json:"input_ref"`
	OutputRef                  string     `gorm:"type:text;not null;default:''" json:"output_ref"`
	Provider                   string     `gorm:"type:varchar(48);not null;default:''" json:"provider,omitempty"`
	Model                      string     `gorm:"type:varchar(128);not null;default:''" json:"model,omitempty"`
	ProviderResponseID         string     `gorm:"type:varchar(128);not null;default:''" json:"provider_response_id,omitempty"`
	UsageJSON                  string     `gorm:"type:jsonb;not null;default:'{}'" json:"usage,omitempty"`
	Status                     string     `gorm:"type:varchar(16);not null;index" json:"status"`
	// RunID and LeaseExpiresAt make a queue redelivery safe. Only the worker
	// holding this opaque run lease may complete or fail a provider call.
	RunID          string     `gorm:"type:varchar(64);not null;default:'';index" json:"-"`
	LeaseExpiresAt *time.Time `gorm:"index" json:"-"`
	AttemptCount   int        `gorm:"not null;default:0" json:"attempt_count"`
	ErrorMessage   string     `gorm:"type:varchar(1024);not null;default:''" json:"error_message,omitempty"`
	CompletedAt    *time.Time `gorm:"index" json:"completed_at,omitempty"`
	CreatedAt      time.Time  `gorm:"not null;index" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"not null" json:"updated_at"`
}
