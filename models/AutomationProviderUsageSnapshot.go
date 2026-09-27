package models

import (
	"errors"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

var ErrProviderUsageSnapshotAppendOnly = errors.New("provider usage snapshots are append-only")

// AutomationProviderUsageSnapshot stores only a normalized, allow-listed
// billing projection. Provider credentials and provider response bodies are
// never persisted. Rows sharing CaptureID belong to one refresh operation.
type AutomationProviderUsageSnapshot struct {
	ID                 uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	CaptureID          uuid.UUID `gorm:"type:uuid;not null;index:idx_provider_usage_capture,priority:1" json:"-"`
	ProjectID          uuid.UUID `gorm:"type:uuid;not null;index:idx_provider_usage_capture,priority:2;index:idx_provider_usage_project_observed,priority:1" json:"project_id"`
	Provider           string    `gorm:"type:varchar(48);not null;index" json:"provider"`
	Status             string    `gorm:"type:varchar(32);not null" json:"status"`
	BillingModel       string    `gorm:"type:varchar(48);not null" json:"billing_model"`
	CredentialScope    string    `gorm:"type:varchar(64);not null" json:"credential_scope"`
	ObservedAt         time.Time `gorm:"not null;index:idx_provider_usage_project_observed,priority:2,sort:desc" json:"observed_at"`
	Currency           string    `gorm:"type:varchar(8);not null;default:''" json:"currency,omitempty"`
	BalanceTotal       *string   `gorm:"type:text" json:"-"`
	BalanceGranted     *string   `gorm:"type:text" json:"-"`
	BalanceToppedUp    *string   `gorm:"type:text" json:"-"`
	BalanceIsAvailable *bool     `json:"-"`
	WindowsJSON        string    `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	ErrorCode          string    `gorm:"type:varchar(64);not null;default:''" json:"error_code,omitempty"`
	CreatedAt          time.Time `gorm:"not null" json:"created_at"`
}

func (*AutomationProviderUsageSnapshot) BeforeUpdate(*gorm.DB) error {
	return ErrProviderUsageSnapshotAppendOnly
}

func (*AutomationProviderUsageSnapshot) BeforeDelete(*gorm.DB) error {
	return ErrProviderUsageSnapshotAppendOnly
}
