//go:build integration

package integration_test

import (
	"errors"
	"strings"
	"testing"

	"events-stocks/configuration"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAppendOnlyLedgersRejectTruncate(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db)
	// Exercise the same migration path used by startup, not a test-only trigger
	// setup, so this also detects omissions in the production migration.
	require.NoError(t, configuration.MigrateModelsForTest(db))

	protectedTables := []struct {
		table       string
		triggerName string
	}{
		{table: "automation_task_events", triggerName: "automation_task_events_no_truncate"},
		{table: "audit_logs", triggerName: "audit_logs_no_truncate"},
		{table: "automation_executions", triggerName: "automation_executions_no_truncate"},
		{table: "automation_tool_executions", triggerName: "automation_tool_executions_no_truncate"},
		{table: "automation_provider_usage_snapshots", triggerName: "automation_provider_usage_snapshots_no_truncate"},
		{table: "automation_inference_receipts", triggerName: "automation_inference_receipts_no_truncate"},
		{table: "delivery_plan_step_events", triggerName: "delivery_plan_step_events_no_truncate"},
		{table: "delivery_plan_step_activity_events", triggerName: "delivery_plan_step_activity_events_no_truncate"},
		{table: "delivery_plan_step_patch_artifacts", triggerName: "delivery_plan_step_patch_artifacts_no_truncate"},
		{table: "delivery_plan_step_evidences", triggerName: "delivery_plan_step_evidences_no_truncate"},
		{table: "delivery_plan_step_dependency_patches", triggerName: "delivery_plan_step_dependency_patches_no_truncate"},
		{table: "automation_ai_action_policy_revisions", triggerName: "automation_ai_action_policy_revisions_no_truncate"},
		{table: "automation_inference_attempt_policies", triggerName: "automation_inference_attempt_policies_no_truncate"},
		{table: "delivery_plan_executions", triggerName: "delivery_plan_executions_no_truncate"},
		{table: "delivery_plan_step_assignments", triggerName: "delivery_plan_step_assignments_no_truncate"},
		{table: "delivery_plan_step_assignment_events", triggerName: "delivery_plan_step_assignment_events_no_truncate"},
		{table: "delivery_plans", triggerName: "delivery_plans_no_truncate"},
		{table: "delivery_automation_schedule_events", triggerName: "delivery_automation_schedule_events_no_truncate"},
		{table: "delivery_automation_schedule_occurrences", triggerName: "delivery_automation_schedule_occurrences_no_truncate"},
	}
	for _, protected := range protectedTables {
		t.Run(protected.table, func(t *testing.T) {
			var installed bool
			err := db.Raw(`
				SELECT EXISTS (
					SELECT 1
					FROM pg_trigger
					WHERE tgrelid = to_regclass(?)
					  AND tgname = ?
					  AND (tgtype & 32) = 32 -- TRUNCATE event
					  AND (tgtype & 2) = 2   -- BEFORE trigger
					  AND (tgtype & 1) = 0   -- statement-level
					  AND NOT tgisinternal
				)`, protected.table, protected.triggerName).Scan(&installed).Error
			require.NoError(t, err)
			require.True(t, installed, "migration must install a BEFORE TRUNCATE statement trigger on %s", protected.table)
		})
	}

	// Prove the PostgreSQL triggers themselves reject real TRUNCATE statements.
	// Each attempt runs in a transaction so a missing guard can never erase data.
	for _, protected := range []struct {
		table string
		cause string
	}{
		{table: "audit_logs", cause: "audit_logs are append-only"},
		{table: "delivery_automation_schedule_events", cause: "delivery automation schedule events are append-only"},
	} {
		t.Run("truncate_"+protected.table, func(t *testing.T) {
			rollback := errors.New("rollback successful truncate")
			var truncateErr error
			err := db.Transaction(func(tx *gorm.DB) error {
				truncateErr = tx.Exec("TRUNCATE TABLE " + protected.table).Error
				if truncateErr != nil {
					return truncateErr
				}
				return rollback
			})
			require.Error(t, truncateErr, "PostgreSQL should reject TRUNCATE even when the table is empty")
			require.Contains(t, strings.ToLower(truncateErr.Error()), protected.cause)
			require.Error(t, err)
			require.False(t, errors.Is(err, rollback), "a successful TRUNCATE must never commit")
		})
	}
}
