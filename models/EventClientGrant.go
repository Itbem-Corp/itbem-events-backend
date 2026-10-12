package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// EventClientGrant gives an external account (an EventiApp "event client")
// event-level access to exactly one event. It is not an organization role:
// the holder has no client_members row, never gains organization reads, and
// the grant is void when the holder has any membership in the organization,
// when the event changes organization or when the organization resolves to a
// product other than EventiApp. Revocation sets RevokedAt; rows are kept.
//
// At most one active (RevokedAt IS NULL) grant exists per event and user,
// enforced by the partial unique index idx_event_client_grants_active.
type EventClientGrant struct {
	ID              uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventID         uuid.UUID  `gorm:"type:uuid;not null;index;uniqueIndex:idx_event_client_grants_active,priority:1,where:revoked_at IS NULL" json:"event_id"`
	UserID          uuid.UUID  `gorm:"type:uuid;not null;index;uniqueIndex:idx_event_client_grants_active,priority:2,where:revoked_at IS NULL" json:"user_id"`
	OrganizationID  uuid.UUID  `gorm:"type:uuid;not null;index" json:"organization_id"`
	GrantedByUserID uuid.UUID  `gorm:"type:uuid;not null" json:"granted_by_user_id"`
	RevokedAt       *time.Time `gorm:"index" json:"revoked_at,omitempty"`
	RevokedByUserID *uuid.UUID `gorm:"type:uuid" json:"revoked_by_user_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (EventClientGrant) TableName() string { return "event_client_grants" }
