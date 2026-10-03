package models

import (
	"github.com/gofrs/uuid"
	"time"
)

// Server-created receipt for one original source package. Callback routes
// cannot create or replace this acquisition provenance.
type AutomationQASourceReceipt struct {
	ID                 uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	TaskID             uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_qa_source_run_reference"`
	RunID              string    `gorm:"type:varchar(36);not null;uniqueIndex:idx_qa_source_run_reference"`
	Reference          string    `gorm:"type:varchar(256);not null;uniqueIndex:idx_qa_source_run_reference"`
	AgentInstanceID    uuid.UUID `gorm:"type:uuid;not null"`
	MatrixDigest       string    `gorm:"type:varchar(64);not null"`
	Repository         string    `gorm:"type:varchar(256);not null"`
	Branch             string    `gorm:"type:varchar(128);not null"`
	CommitSHA          string    `gorm:"type:varchar(40);not null"`
	PackSHA256         string    `gorm:"type:varchar(64);not null"`
	PackBytes          int64     `gorm:"not null"`
	BundleSHA256       string    `gorm:"type:varchar(64);not null;default:''"`
	BundleBytes        int64     `gorm:"not null;default:0"`
	DependencyManifest string    `gorm:"type:text;not null;default:''"`
	AcquiredAt         time.Time `gorm:"not null"`
}
