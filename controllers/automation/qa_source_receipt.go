package automation

import (
	"encoding/json"
	"fmt"
	"time"

	"events-stocks/internal/qaevidence"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func verifyServerQASourceReceipts(db *gorm.DB, task *models.AutomationTask, run string, instance uuid.UUID, raw json.RawMessage) error {
	if db == nil || task == nil || task.Operation != "delivery.qa" || !validUUIDRun(run) || instance == uuid.Nil {
		return fmt.Errorf("QA source provenance identity invalid")
	}
	observation, err := qaevidence.Decode(raw)
	if err != nil || observation.TaskID != task.ID.String() || observation.MatrixDigest != task.EvidenceSubjectDigest {
		return fmt.Errorf("QA source provenance subject changed")
	}
	var receipts []models.AutomationQASourceReceipt
	if err := db.Where("task_id = ? AND run_id = ?", task.ID, run).Find(&receipts).Error; err != nil {
		return err
	}
	if len(receipts) != len(observation.Repositories) {
		return fmt.Errorf("QA source provenance matrix incomplete")
	}
	seen := map[string]bool{}
	for _, repository := range observation.Repositories {
		matches := 0
		for _, receipt := range receipts {
			if receipt.Reference == repository.Reference && receipt.Branch == repository.Branch && receipt.AgentInstanceID == instance && receipt.MatrixDigest == task.EvidenceSubjectDigest && artifactDigestPattern.MatchString(receipt.PackSHA256) && receipt.PackBytes >= 12 && receipt.PackBytes <= 64<<20 {
				matches++
			}
		}
		if matches != 1 || seen[repository.Reference] {
			return fmt.Errorf("QA source provenance does not match the original acquisition")
		}
		seen[repository.Reference] = true
	}
	return nil
}

func recordServerQASourceReceipt(db *gorm.DB, expected *models.AutomationTask, lease gatewayLease, gateway gatewayIdentity, actor authenticatedAgentCallback, subject qaSourceSubject, packDigest string, packBytes int64, now time.Time) error {
	if db == nil || expected == nil || expected.ID == uuid.Nil || !validUUIDRun(expected.RunID) || !artifactDigestPattern.MatchString(packDigest) || packBytes < 12 || packBytes > 64<<20 || len(subject.Reference) > 256 || now.IsZero() {
		return fmt.Errorf("QA source receipt identity invalid")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var current models.AutomationTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, expected.ID).Error; err != nil {
			return err
		}
		var instance models.AutomationAgentInstance
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ?", actor.InstanceID, "active").First(&instance).Error; err != nil {
			return err
		}
		if instance.AgentKey != actor.AgentKey || instance.MachineID != actor.MachineID || current.EvidenceSubjectDigest != expected.EvidenceSubjectDigest || current.InputRef != expected.InputRef || current.DeliveryWorkItemID == nil || expected.DeliveryWorkItemID == nil || *current.DeliveryWorkItemID != *expected.DeliveryWorkItemID {
			return fmt.Errorf("QA source receipt authority changed")
		}
		if err := validateQASourceTask(&current, lease, gateway, actor, expected.RunID, time.Now().UTC()); err != nil {
			return err
		}
		row := models.AutomationQASourceReceipt{TaskID: current.ID, RunID: current.RunID, Reference: subject.Reference, AgentInstanceID: actor.InstanceID, MatrixDigest: current.EvidenceSubjectDigest, Repository: subject.Repository, Branch: subject.Branch, CommitSHA: subject.SHA, PackSHA256: packDigest, PackBytes: packBytes, AcquiredAt: now.UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		var saved models.AutomationQASourceReceipt
		if err := tx.Where("task_id = ? AND run_id = ? AND reference = ?", row.TaskID, row.RunID, row.Reference).First(&saved).Error; err != nil {
			return err
		}
		if saved.AgentInstanceID != row.AgentInstanceID || saved.MatrixDigest != row.MatrixDigest || saved.Repository != row.Repository || saved.Branch != row.Branch || saved.CommitSHA != row.CommitSHA || saved.PackSHA256 != row.PackSHA256 || saved.PackBytes != row.PackBytes {
			return fmt.Errorf("QA source acquisition differs from its immutable receipt")
		}
		return nil
	})
}
