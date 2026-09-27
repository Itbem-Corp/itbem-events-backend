//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
)

func TestDeliveryPlanVersionContentIsAppendOnly(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db)
	// TestMain migrates the disposable PostgreSQL database once. Repeating the
	// startup migration verifies that creating the trigger is idempotent.
	require.NoError(t, configuration.MigrateModelsForTest(db))
	require.NoError(t, configuration.MigrateModelsForTest(db))

	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	now := time.Now().UTC().Truncate(time.Microsecond)
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Plan immutability " + suffix, Code: "PLAN" + suffix, Level: 10, IsActive: true}
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Plan immutability " + suffix, Code: "plan-immutability-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Plan project " + suffix, Slug: "plan-project-" + suffix, Status: "active", CreatedBy: "integration"}
	itemApproved := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: "integration", Title: "Approve plan " + suffix, ExpectedOutcome: "plan remains immutable"}
	itemChanges := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: "integration", Title: "Request plan changes " + suffix, ExpectedOutcome: "plan remains immutable"}
	gate := models.DeliveryGate{ID: uuid.Must(uuid.NewV4()), WorkItemID: itemApproved.ID, Kind: "plan_approval", Decision: "approved", DecidedBy: "integration", DecidedAt: now, CreatedAt: now}
	approvedPlan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: itemApproved.ID, Version: 1, Status: "proposed", Summary: "immutable proposal", StructuredJSON: `{"steps":["review"]}`, ContextDigest: "initial-digest", ProposedBy: "agent", CreatedAt: now}
	changesPlan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: itemChanges.ID, Version: 1, Status: "proposed", Summary: "proposal for changes", StructuredJSON: `{"steps":["revise"]}`, ContextDigest: "changes-digest", ProposedBy: "agent", CreatedAt: now}
	for _, value := range []any{&clientType, &client, &project, &itemApproved, &itemChanges, &gate, &approvedPlan, &changesPlan} {
		require.NoError(t, db.Create(value).Error)
	}

	// Every field defining a version's identity or reviewed content is rejected
	// by PostgreSQL itself, independently of application/controller behavior.
	mutations := []struct {
		field string
		value any
	}{
		{field: "id", value: uuid.Must(uuid.NewV4())},
		{field: "work_item_id", value: itemChanges.ID},
		{field: "version", value: 2},
		{field: "summary", value: "tampered summary"},
		{field: "structured_json", value: `{"tampered":true}`},
		{field: "context_digest", value: "tampered-digest"},
		{field: "proposed_by", value: "tampered-actor"},
		{field: "created_at", value: now.Add(time.Hour)},
	}
	approvedPlanID := approvedPlan.ID
	for _, mutation := range mutations {
		result := db.Model(&models.DeliveryPlan{}).Where("id = ?", approvedPlanID).UpdateColumn(mutation.field, mutation.value)
		require.Error(t, result.Error, "mutation of %s should be rejected", mutation.field)
		require.Contains(t, result.Error.Error(), "delivery plan version content is immutable")
		var persisted models.DeliveryPlan
		require.NoError(t, db.First(&persisted, "id = ?", approvedPlanID).Error, "plan row missing after rejected %s update: %v", mutation.field, result.Error)
		require.Equal(t, approvedPlan.WorkItemID, persisted.WorkItemID)
		require.Equal(t, approvedPlan.Version, persisted.Version)
		require.Equal(t, approvedPlan.Status, persisted.Status)
		require.Equal(t, approvedPlan.Summary, persisted.Summary)
		require.JSONEq(t, approvedPlan.StructuredJSON, persisted.StructuredJSON)
		require.Equal(t, approvedPlan.ContextDigest, persisted.ContextDigest)
		require.Equal(t, approvedPlan.ProposedBy, persisted.ProposedBy)
		require.WithinDuration(t, approvedPlan.CreatedAt, persisted.CreatedAt, time.Microsecond)
	}
	deleteResult := db.Where("id = ?", approvedPlanID).Delete(&models.DeliveryPlan{})
	require.Error(t, deleteResult.Error)
	require.Contains(t, deleteResult.Error.Error(), "delivery plan versions are append-only")

	// The human review decision is operational state, not proposal content.
	// These mirror markPlan's approval and changes-requested updates exactly.
	require.NoError(t, db.Model(&models.DeliveryPlan{}).Where("id = ?", approvedPlanID).Updates(map[string]any{
		"status":           "approved",
		"approved_gate_id": gate.ID,
	}).Error)
	var persistedApproved models.DeliveryPlan
	require.NoError(t, db.First(&persistedApproved, "id = ?", approvedPlanID).Error)
	require.Equal(t, "approved", persistedApproved.Status)
	require.NotNil(t, persistedApproved.ApprovedGateID)
	require.Equal(t, gate.ID, *persistedApproved.ApprovedGateID)

	require.NoError(t, db.Model(&changesPlan).Updates(map[string]any{
		"status":           "changes_requested",
		"approved_gate_id": nil,
	}).Error)
	var persistedChanges models.DeliveryPlan
	require.NoError(t, db.First(&persistedChanges, "id = ?", changesPlan.ID).Error)
	require.Equal(t, "changes_requested", persistedChanges.Status)
	require.Nil(t, persistedChanges.ApprovedGateID)
}
