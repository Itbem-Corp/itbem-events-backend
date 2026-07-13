package dtos

import "github.com/gofrs/uuid"

type MomentSummary struct {
	EventID      uuid.UUID `json:"event_id" gorm:"column:event_id"`
	PendingCount int64     `json:"pending_count" gorm:"column:pending_count"`
}
