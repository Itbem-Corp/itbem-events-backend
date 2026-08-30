package models

import (
	"fmt"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

// AutomationCodeReviewPublication is append-only proof that the isolated
// Reviewer identity relayed one validated result to GitHub for an exact PR
// head. Findings and model prose stay in private object storage; this table
// contains only public GitHub identity and deterministic digests.
type AutomationCodeReviewPublication struct {
	ID               uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	AutomationTaskID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"automation_task_id"`
	Repository       string    `gorm:"type:varchar(255);not null;index" json:"repository"`
	PullRequest      int       `gorm:"not null;index" json:"pull_request"`
	HeadSHA          string    `gorm:"type:char(40);not null;index" json:"head_sha"`
	PatchSHA256      string    `gorm:"type:char(64);not null" json:"patch_sha256"`
	SubjectSHA256    string    `gorm:"type:char(64);not null;uniqueIndex" json:"subject_sha256"`
	PayloadSHA256    string    `gorm:"type:char(64);not null" json:"payload_sha256"`
	Verdict          string    `gorm:"type:varchar(24);not null" json:"verdict"`
	Event            string    `gorm:"type:varchar(24);not null" json:"event"`
	ReviewID         int64     `gorm:"not null;uniqueIndex" json:"review_id"`
	ReviewURL        string    `gorm:"type:text;not null" json:"review_url"`
	ReviewerActor    string    `gorm:"type:varchar(128);not null;index" json:"reviewer_actor"`
	AuthorActor      string    `gorm:"type:varchar(128);not null;default:''" json:"author_actor,omitempty"`
	PublishedAt      time.Time `gorm:"not null;index" json:"published_at"`
	CreatedAt        time.Time `gorm:"not null" json:"created_at"`
}

func (*AutomationCodeReviewPublication) BeforeUpdate(*gorm.DB) error {
	return fmt.Errorf("code review publications are append-only")
}

func (*AutomationCodeReviewPublication) BeforeDelete(*gorm.DB) error {
	return fmt.Errorf("code review publications are append-only")
}
