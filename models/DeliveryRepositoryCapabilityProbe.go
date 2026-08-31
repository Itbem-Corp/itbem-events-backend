package models

import (
	"errors"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

// DeliveryRepositoryCapabilityProbe is append-only evidence that one isolated
// automation task evaluated one onboarding capability at an exact repository
// SHA. The private artifact remains in object storage; only its digest and the
// canonical subject identity are persisted here.
type DeliveryRepositoryCapabilityProbe struct {
	ID                  uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ProjectID           uuid.UUID `gorm:"type:uuid;not null;index" json:"project_id"`
	OnboardingID        uuid.UUID `gorm:"type:uuid;not null;index;uniqueIndex:idx_delivery_probe_subject" json:"onboarding_id"`
	AutomationTaskID    uuid.UUID `gorm:"type:uuid;not null;index;uniqueIndex:idx_delivery_probe_task_capability" json:"automation_task_id"`
	RepositoryReference string    `gorm:"type:varchar(255);not null;index" json:"repository_reference"`
	Revision            string    `gorm:"type:varchar(40);not null;index" json:"revision"`
	Capability          string    `gorm:"type:varchar(32);not null;index;uniqueIndex:idx_delivery_probe_task_capability" json:"capability"`
	State               string    `gorm:"type:varchar(16);not null;index" json:"state"`
	ExecutorRole        string    `gorm:"type:varchar(24);not null;index" json:"executor_role"`
	EvidenceSHA256      string    `gorm:"type:char(64);not null;index" json:"evidence_sha256"`
	SubjectSHA256       string    `gorm:"type:char(64);not null;index;uniqueIndex:idx_delivery_probe_subject" json:"subject_sha256"`
	Reason              string    `gorm:"type:varchar(500);not null" json:"reason"`
	ObservedAt          time.Time `gorm:"not null;index" json:"observed_at"`
	CreatedAt           time.Time `gorm:"not null;index" json:"created_at"`
}

var ErrImmutableCapabilityProbe = errors.New("repository capability probes are append-only")

func (*DeliveryRepositoryCapabilityProbe) BeforeUpdate(*gorm.DB) error {
	return ErrImmutableCapabilityProbe
}

func (*DeliveryRepositoryCapabilityProbe) BeforeDelete(*gorm.DB) error {
	return ErrImmutableCapabilityProbe
}
