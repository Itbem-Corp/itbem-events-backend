package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// EventAnalyticsRollup is derived, recomputable state produced by
// itbem-events-workers. EventAnalytics remains the authoritative home for
// request-time counters such as views; this snapshot owns expensive aggregates.
type EventAnalyticsRollup struct {
	EventID         uuid.UUID  `json:"event_id" gorm:"type:uuid;primaryKey"`
	Event           Event      `json:"-" gorm:"foreignKey:EventID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	JobID           uuid.UUID  `json:"job_id" gorm:"type:uuid;not null"`
	SchemaVersion   int16      `json:"schema_version" gorm:"not null"`
	GuestSummary    string     `json:"guest_summary" gorm:"type:jsonb;not null;default:'{}'"`
	MomentTotal     int        `json:"moment_total" gorm:"not null;default:0"`
	MomentApproved  int        `json:"moment_approved" gorm:"not null;default:0"`
	MomentPending   int        `json:"moment_pending" gorm:"not null;default:0"`
	MomentComments  int        `json:"moment_comments" gorm:"not null;default:0"`
	MomentUploads   int        `json:"moment_uploads" gorm:"not null;default:0"`
	SourceUpdatedAt *time.Time `json:"source_updated_at,omitempty"`
	ComputedAt      time.Time  `json:"computed_at" gorm:"not null;index"`
}

func (EventAnalyticsRollup) TableName() string { return "event_analytics_rollups" }
