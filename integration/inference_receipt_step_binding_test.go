//go:build integration

package integration_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAutomationInferenceReceiptPlanStepBindingIsImmutable(t *testing.T) {
	rollbackFixture := errors.New("rollback inference receipt fixture")
	var mutationErr error
	err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		stepID := uuid.Must(uuid.NewV4())
		receipt := models.AutomationInferenceReceipt{
			ID: uuid.Must(uuid.NewV4()), AutomationTaskID: uuid.Must(uuid.NewV4()), RunID: uuid.Must(uuid.NewV4()).String(),
			CallID: uuid.Must(uuid.NewV4()), PlanStepID: &stepID, Operation: "delivery.implementation",
			WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "implementation_specialist", MachineID: uuid.Must(uuid.NewV4()).String(),
			PolicySnapshotHash: strings.Repeat("a", 64), QuotaLimit: 6, Status: "reserved", CreatedAt: time.Now().UTC(),
		}
		require.NoError(t, tx.Create(&receipt).Error)
		otherStepID := uuid.Must(uuid.NewV4())
		mutationErr = tx.Model(&models.AutomationInferenceReceipt{}).Where("id = ?", receipt.ID).Updates(map[string]any{
			"status": "accepted", "plan_step_id": otherStepID,
		}).Error
		return rollbackFixture
	})
	require.ErrorIs(t, err, rollbackFixture, "the fixture transaction must roll back")
	require.Error(t, mutationErr)
	require.Contains(t, strings.ToLower(mutationErr.Error()), "automation inference receipt transition is invalid")
}

// A pre-existing receipt row must survive the additive startup migration and
// remain un-attributed: old activity cannot be safely mapped to a step after
// the fact.
func TestInferenceReceiptMigrationAddsNullablePlanStepID(t *testing.T) {
	require.NotNil(t, configuration.DB)
	schemaName := "receipt_step_migration_" + strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")
	require.NoError(t, configuration.DB.Exec("CREATE SCHEMA \""+schemaName+"\"").Error)
	defer func() {
		_ = configuration.DB.Exec("DROP SCHEMA IF EXISTS \"" + schemaName + "\" CASCADE").Error
	}()

	legacyReceiptID, taskID, callID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL search_path TO \"" + schemaName + "\"").Error; err != nil {
			return err
		}
		// This is the pre-StepID schema: all existing columns are present, but
		// the new nullable relationship/index is not.
		if err := tx.Exec(`CREATE TABLE automation_inference_receipts (
			id uuid PRIMARY KEY, automation_task_id uuid NOT NULL, run_id varchar(64) NOT NULL, call_id uuid NOT NULL,
			operation varchar(96) NOT NULL, worker_id varchar(64) NOT NULL DEFAULT '', agent_key varchar(64) NOT NULL DEFAULT '', machine_id varchar(64) NOT NULL DEFAULT '',
			policy_snapshot_hash char(64) NOT NULL, quota_limit integer NOT NULL, status varchar(16) NOT NULL,
			provider varchar(48) NOT NULL DEFAULT '', model varchar(128) NOT NULL DEFAULT '', provider_response_id varchar(128) NOT NULL DEFAULT '',
			input_tokens bigint NOT NULL DEFAULT 0, output_tokens bigint NOT NULL DEFAULT 0, cached_input_tokens bigint NOT NULL DEFAULT 0,
			cache_write_tokens bigint NOT NULL DEFAULT 0, reasoning_tokens bigint NOT NULL DEFAULT 0, total_tokens bigint NOT NULL DEFAULT 0,
			input_cost_micros bigint NOT NULL DEFAULT 0, output_cost_micros bigint NOT NULL DEFAULT 0, cached_cost_micros bigint NOT NULL DEFAULT 0,
			cache_write_cost_micros bigint NOT NULL DEFAULT 0, total_cost_micros bigint NOT NULL DEFAULT 0,
			currency char(3) NOT NULL DEFAULT 'USD', pricing_basis varchar(32) NOT NULL DEFAULT 'unpriced',
			pricing_snapshot_json jsonb NOT NULL DEFAULT '{}', usage_json jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL, resolved_at timestamptz
		)`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO automation_inference_receipts
			(id, automation_task_id, run_id, call_id, operation, policy_snapshot_hash, quota_limit, status, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, legacyReceiptID, taskID, uuid.Must(uuid.NewV4()).String(), callID,
			"delivery.plan", strings.Repeat("a", 64), 1, "accepted", time.Now().UTC()).Error; err != nil {
			return err
		}
		if err := tx.AutoMigrate(&models.AutomationInferenceReceipt{}); err != nil {
			return err
		}
		columnTypes, err := tx.Migrator().ColumnTypes(&models.AutomationInferenceReceipt{})
		if err != nil {
			return err
		}
		found := false
		for _, column := range columnTypes {
			if column.Name() != "plan_step_id" {
				continue
			}
			found = true
			nullable, nullableKnown := column.Nullable()
			if !nullableKnown || !nullable || !strings.EqualFold(column.DatabaseTypeName(), "uuid") {
				return fmt.Errorf("plan_step_id must be a nullable UUID column (nullable=%v known=%v type=%s)", nullable, nullableKnown, column.DatabaseTypeName())
			}
		}
		if !found {
			return fmt.Errorf("AutoMigrate did not add plan_step_id")
		}
		var preserved struct {
			ID         uuid.UUID  `gorm:"column:id"`
			PlanStepID *uuid.UUID `gorm:"column:plan_step_id"`
		}
		if err := tx.Table("automation_inference_receipts").Select("id, plan_step_id").Where("id = ?", legacyReceiptID).Take(&preserved).Error; err != nil {
			return err
		}
		if preserved.ID != legacyReceiptID || preserved.PlanStepID != nil {
			return fmt.Errorf("legacy receipt was changed or back-attributed: %#v", preserved)
		}
		return nil
	})
	require.NoError(t, err)
}
