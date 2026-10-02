package models

import (
	"github.com/gofrs/uuid"
	"time"
)

// AutomationReleaseObservation binds server-collected evidence to its enrolled
// observer run. Workers cannot create or replace this record through callbacks.
type AutomationReleaseObservation struct {
	ID              uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	TaskID          uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_release_observation_run"`
	RunID           string    `gorm:"type:varchar(36);not null;uniqueIndex:idx_release_observation_run"`
	AgentInstanceID uuid.UUID `gorm:"type:uuid;not null"`
	SubjectDigest   string    `gorm:"type:varchar(64);not null"`
	PayloadDigest   string    `gorm:"type:varchar(64);not null"`
	ObservedAt      time.Time `gorm:"not null"`
}
