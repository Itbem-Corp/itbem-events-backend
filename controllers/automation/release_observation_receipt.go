package automation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"events-stocks/models"
	"fmt"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func releaseObservationDigest(task *models.AutomationTask, raw json.RawMessage) (string, error) {
	_, _, input, environment, err := releaseGateCandidateForTask(task, raw)
	if err != nil || environment == nil {
		return "", fmt.Errorf("server release observation requires schema two")
	}
	canonical, err := json.Marshal(map[string]any{"schema_version": 2, "gatekeeper_input": input, "environment_observation": environment})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func recordServerReleaseObservation(db *gorm.DB, task *models.AutomationTask, run string, instance uuid.UUID, raw json.RawMessage, now time.Time) error {
	if db == nil || task == nil || task.ID == uuid.Nil || !validUUIDRun(run) || instance == uuid.Nil || now.IsZero() {
		return fmt.Errorf("release observation identity invalid")
	}
	digest, err := releaseObservationDigest(task, raw)
	if err != nil {
		return err
	}
	row := models.AutomationReleaseObservation{TaskID: task.ID, RunID: run, AgentInstanceID: instance, SubjectDigest: task.EvidenceSubjectDigest, PayloadDigest: digest, ObservedAt: now.UTC()}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	return verifyServerReleaseObservation(db, task, run, instance, raw)
}

func validUUIDRun(run string) bool {
	id, err := uuid.FromString(run)
	return err == nil && id != uuid.Nil && id.String() == run
}

func verifyServerReleaseObservation(db *gorm.DB, task *models.AutomationTask, run string, instance uuid.UUID, raw json.RawMessage) error {
	if db == nil || task == nil || !validUUIDRun(run) || instance == uuid.Nil {
		return fmt.Errorf("release observation identity invalid")
	}
	digest, err := releaseObservationDigest(task, raw)
	if err != nil {
		return err
	}
	var row models.AutomationReleaseObservation
	if err := db.Where("task_id = ? AND run_id = ?", task.ID, run).First(&row).Error; err != nil {
		return err
	}
	if row.AgentInstanceID != instance || row.SubjectDigest != task.EvidenceSubjectDigest || row.PayloadDigest != digest {
		return fmt.Errorf("release observation differs from server evidence")
	}
	return nil
}
