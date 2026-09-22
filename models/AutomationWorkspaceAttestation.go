package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// AutomationWorkspaceAttestation is a short-lived, metadata-only statement
// from an authenticated local agent about one operator-configured checkout.
// It deliberately stores neither a path, remote URL, command, source excerpt,
// host identity nor credential. Delivery still binds this observation to a
// human-registered workspace source and an independently-read GitHub
// checkpoint before it can be used by a task.
type AutomationWorkspaceAttestation struct {
	ID               uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	WorkerID         string    `gorm:"type:varchar(64);not null;uniqueIndex:automation_workspace_attestation_worker_workspace" json:"-"`
	Role             string    `gorm:"type:varchar(32);not null;default:''" json:"role"`
	Lane             string    `gorm:"type:varchar(32);not null;default:''" json:"lane"`
	WorkspaceID      string    `gorm:"type:varchar(96);not null;uniqueIndex:automation_workspace_attestation_worker_workspace;index" json:"workspace_id"`
	Available        bool      `gorm:"not null;default:false" json:"available"`
	GitHubRepository string    `gorm:"type:varchar(192);not null;default:'';index" json:"github_repository,omitempty"`
	HeadSHA          string    `gorm:"type:char(40);not null;default:'';index" json:"head_sha,omitempty"`
	Branch           string    `gorm:"type:varchar(255);not null;default:''" json:"branch,omitempty"`
	Clean            bool      `gorm:"not null;default:false" json:"clean"`
	ChangeCount      int       `gorm:"not null;default:0" json:"change_count"`
	TrackingBranch   string    `gorm:"type:varchar(255);not null;default:''" json:"tracking_branch,omitempty"`
	LocalAhead       int       `gorm:"not null;default:0" json:"local_ahead"`
	RemoteAhead      int       `gorm:"not null;default:0" json:"remote_ahead"`
	CapabilitiesJSON string    `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	AttestedAt       time.Time `gorm:"not null;index" json:"attested_at"`
	CreatedAt        time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"not null" json:"updated_at"`
}
