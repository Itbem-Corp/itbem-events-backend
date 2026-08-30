package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// UserApplicationPolicy is an explicit, product-scoped access policy managed
// from ITBEM. A missing record means the user retains the legacy inherited
// authorization path; this makes the rollout safe for existing accounts.
type UserApplicationPolicy struct {
	ID              uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	UserID          uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_user_application_policy" json:"user_id"`
	ApplicationID   uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_user_application_policy" json:"application_id"`
	IsActive        bool       `gorm:"not null;default:true" json:"is_active"`
	Capabilities    StringList `gorm:"type:jsonb;not null;default:'[]'" json:"capabilities"`
	CreatedByUserID *uuid.UUID `gorm:"type:uuid;index" json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}
