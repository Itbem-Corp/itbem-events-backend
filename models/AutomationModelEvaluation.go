package models

import (
	"github.com/gofrs/uuid"
	"time"
)

// AutomationModelEvaluation reserves an entire server-authored synthetic batch.
// No provider credentials, arbitrary client routes or private reasoning belong here.
type AutomationModelEvaluation struct {
	ID                uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	RequestedBy       string    `gorm:"type:varchar(128);not null" json:"requested_by"`
	CorpusVersion     string    `gorm:"type:varchar(96);not null" json:"corpus_version"`
	CorpusHash        string    `gorm:"type:varchar(64);not null" json:"corpus_hash"`
	BudgetMicros      int64     `gorm:"not null" json:"budget_microusd"`
	ReservationMicros int64     `gorm:"not null" json:"reservation_microusd"`
	PricingJSON       string    `gorm:"type:text;not null" json:"-"`
	Status            string    `gorm:"type:varchar(16);not null;index" json:"status"`
	CreatedAt         time.Time `gorm:"not null" json:"created_at"`
}

// AutomationModelEvaluationCall is append-only admission metadata for one task.
// Candidate is resolved using the server allowlist, never worker routing input.
type AutomationModelEvaluationCall struct {
	AutomationTaskID  uuid.UUID `gorm:"type:uuid;primaryKey" json:"task_id"`
	EvaluationID      uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:evaluation_sequence" json:"evaluation_id"`
	Sequence          int       `gorm:"not null;uniqueIndex:evaluation_sequence" json:"sequence"`
	CaseID            string    `gorm:"type:varchar(64);not null" json:"case_id"`
	Candidate         string    `gorm:"type:varchar(64);not null" json:"candidate"`
	PromptHash        string    `gorm:"type:varchar(64);not null" json:"prompt_sha256"`
	MessagesHash      string    `gorm:"type:varchar(64);not null" json:"messages_sha256"`
	RouteHash         string    `gorm:"type:varchar(64);not null" json:"route_sha256"`
	ReservationMicros int64     `gorm:"not null" json:"reservation_microusd"`
	CreatedAt         time.Time `gorm:"not null" json:"created_at"`
}
