package automation

import (
	"encoding/json"
	"fmt"
	"time"

	"events-stocks/models"
	"gorm.io/gorm"
)

// Renewed signed authority is checked immediately before publication effects.
// It cannot turn a stale task, revoked grant or modified worktree into a lease.
func validateDelegatedPublicationRuntimeAuthority(tx *gorm.DB, task models.AutomationTask, now time.Time) error {
	if task.DeliveryWorkItemID == nil || task.ContinuationID == nil {
		return fmt.Errorf("publication authority has no durable binding")
	}
	var intent models.DeliveryContinuation
	if err := tx.First(&intent, *task.ContinuationID).Error; err != nil {
		return err
	}
	var item models.DeliveryWorkItem
	if err := tx.First(&item, *task.DeliveryWorkItemID).Error; err != nil {
		return err
	}
	if intent.WorkItemID != item.ID || intent.Epoch != item.AutomationEpoch || intent.Phase != "publish" || (intent.Status != "claimed" && intent.Status != "dispatched") || item.State != "code_review" {
		return fmt.Errorf("publication authority was superseded")
	}
	var grant models.DeliveryPublicationGrant
	if err := tx.Where("id = ? AND work_item_id = ? AND revoked_at IS NULL AND expires_at > ?", intent.PublicationGrantID, item.ID, now).First(&grant).Error; err != nil {
		return err
	}
	if grant.GrantedBy != "delivery-gatekeeper" || grant.ReviewDiffSHA256 != task.EvidenceSubjectDigest {
		return fmt.Errorf("publication authority changed its diff")
	}
	var local models.DeliveryChangeSet
	if err := tx.Where("work_item_id = ? AND repository_ref = ? AND review_type = ?", item.ID, grant.RepositoryRef, "local_worktree").Order("created_at DESC, id DESC").First(&local).Error; err != nil {
		return err
	}
	var metadata map[string]any
	if local.Branch != grant.Branch || local.CIStatus != "passed" || local.CreatedBy != "itbem-local-agent" || json.Unmarshal([]byte(local.MetadataJSON), &metadata) != nil || metadata["verification_source"] != "itbem-local-agent" || metadata["review_diff_sha256"] != grant.ReviewDiffSHA256 || metadata["base_sha"] != grant.BaseSHA || metadata["github_repository"] != grant.GitHubRepository {
		return fmt.Errorf("publication authority lost its validated local diff")
	}
	return nil
}
