package models

import "time"

// PublicPerformanceWindowBucket is an aggregate-only operational histogram.
// It deliberately has no event, visitor, token, IP, user-agent, or URL fields.
type PublicPerformanceWindowBucket struct {
	BucketStart time.Time `gorm:"primaryKey;type:timestamptz" json:"bucket_start"`
	Route       string    `gorm:"primaryKey;size:32" json:"route"`
	Metric      string    `gorm:"primaryKey;size:32" json:"metric"`
	BucketIndex int       `gorm:"primaryKey" json:"bucket_index"`
	UpperBound  float64   `gorm:"not null" json:"upper_bound"`
	SampleCount int64     `gorm:"not null;default:0" json:"sample_count"`
	UpdatedAt   time.Time `json:"updated_at"`
}
