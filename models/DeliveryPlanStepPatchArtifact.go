package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// DeliveryPlanStepPatchArtifact is an append-only server-verified reference to
// one bounded unified Git patch uploaded by an authenticated worker. Neither
// the patch bytes nor its private S3 key are exposed through JSON APIs.
type DeliveryPlanStepPatchArtifact struct {
	ID               uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	PlanID           uuid.UUID  `gorm:"type:uuid;not null;index:idx_delivery_step_patch_plan_run_step,priority:1" json:"-"`
	AutomationTaskID uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	RunID            string     `gorm:"type:uuid;not null;index:idx_delivery_step_patch_plan_run_step,priority:2;uniqueIndex:idx_delivery_step_patch_run_step_repo,priority:1" json:"-"`
	StepID           uuid.UUID  `gorm:"type:uuid;not null;index:idx_delivery_step_patch_plan_run_step,priority:3;uniqueIndex:idx_delivery_step_patch_run_step_repo,priority:2" json:"-"`
	WorkerID         string     `gorm:"type:uuid;not null;index" json:"-"`
	AgentKey         string     `gorm:"type:varchar(64);not null;index" json:"-"`
	MachineID        string     `gorm:"type:varchar(64);not null" json:"-"`
	AgentInstanceID  *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	FencingToken     int64      `gorm:"not null;check:delivery_step_patch_fence_positive,fencing_token > 0" json:"-"`
	RepositoryRef    string     `gorm:"type:varchar(256);not null;uniqueIndex:idx_delivery_step_patch_run_step_repo,priority:3" json:"-"`
	BaseSHA          string     `gorm:"type:varchar(64);not null;check:delivery_step_patch_base_sha,base_sha ~ '^(?:[0-9a-f]{40}|[0-9a-f]{64})$'" json:"-"`
	Bucket           string     `gorm:"type:varchar(255);not null" json:"-"`
	ObjectKey        string     `gorm:"type:text;not null" json:"-"`
	SHA256           string     `gorm:"type:varchar(64);not null;index;check:delivery_step_patch_sha256,sha256 ~ '^[0-9a-f]{64}$'" json:"-"`
	SizeBytes        int64      `gorm:"not null;check:delivery_step_patch_size_bounded,size_bytes BETWEEN 1 AND 4194304" json:"-"`
	CreatedAt        time.Time  `gorm:"not null;index" json:"-"`
}
