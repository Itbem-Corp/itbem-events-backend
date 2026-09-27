package models

import (
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

// DeliveryEpic is a project-scoped planning container. Requests remain the
// original intake record and work items remain independently executable; an
// epic-to-work-item association is represented by DeliveryEpicWorkItem.
// API handlers intentionally return allow-listed DTOs instead of this model.
type DeliveryEpic struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	ProjectID uuid.UUID `gorm:"type:uuid;not null;index" json:"-"`
	Title     string    `gorm:"type:varchar(180);not null" json:"-"`
	Summary   string    `gorm:"type:text;not null;default:''" json:"-"`
	Status    string    `gorm:"type:varchar(24);not null;default:'planned';index" json:"-"`
	CreatedBy string    `gorm:"type:varchar(128);not null;default:''" json:"-"`
	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
}

// DeliveryEpicWorkItem is the current project-scoped association between an
// epic and one work item. Soft deletion retains the former association row
// when a task is removed or later moved to another epic. API writes lock the
// work item before checking active membership, preventing concurrent requests
// from assigning it to two epics.
type DeliveryEpicWorkItem struct {
	ID         uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	ProjectID  uuid.UUID      `gorm:"type:uuid;not null;index:idx_delivery_epic_membership_project" json:"-"`
	EpicID     uuid.UUID      `gorm:"type:uuid;not null;index:idx_delivery_epic_membership_epic" json:"-"`
	WorkItemID uuid.UUID      `gorm:"type:uuid;not null;index:idx_delivery_epic_membership_work_item" json:"-"`
	CreatedBy  string         `gorm:"type:varchar(128);not null;default:''" json:"-"`
	CreatedAt  time.Time      `json:"-"`
	DeletedAt  gorm.DeletedAt `gorm:"index" json:"-"`
}
