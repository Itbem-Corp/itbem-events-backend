//go:build integration

package integration_test

import (
	"strings"
	"testing"

	"events-stocks/configuration"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
)

func TestDeliveryEpicMembershipDatabaseIntegrity(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db)
	// TestMain migrates a fresh database once; repeating the production migration
	// here verifies that this migration is safe to apply to an already-current
	// schema as well.
	require.NoError(t, configuration.MigrateModelsForTest(db))

	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Epic integrity " + suffix, Code: "EPICTEST" + suffix, Level: 10, IsActive: true}
	require.NoError(t, db.Create(&clientType).Error)
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Epic integrity " + suffix, Code: "epic-integrity-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	require.NoError(t, db.Create(&client).Error)
	projectA := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Epic project A " + suffix, Slug: "epic-project-a-" + suffix, Status: "active", CreatedBy: "integration"}
	projectB := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Epic project B " + suffix, Slug: "epic-project-b-" + suffix, Status: "active", CreatedBy: "integration"}
	require.NoError(t, db.Create(&projectA).Error)
	require.NoError(t, db.Create(&projectB).Error)

	newEpic := func(projectID uuid.UUID, title string) models.DeliveryEpic {
		epic := models.DeliveryEpic{ID: uuid.Must(uuid.NewV4()), ProjectID: projectID, Title: title + " " + suffix, Status: "planned", CreatedBy: "integration"}
		require.NoError(t, db.Create(&epic).Error)
		return epic
	}
	newWorkItem := func(projectID uuid.UUID, title string) models.DeliveryWorkItem {
		item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: projectID, RequestedBy: "integration", Title: title + " " + suffix, ExpectedOutcome: "integrity fixture"}
		require.NoError(t, db.Create(&item).Error)
		return item
	}
	newMembership := func(projectID, epicID, workItemID uuid.UUID) models.DeliveryEpicWorkItem {
		return models.DeliveryEpicWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: projectID, EpicID: epicID, WorkItemID: workItemID, CreatedBy: "integration"}
	}

	epicA := newEpic(projectA.ID, "Epic A")
	epicA2 := newEpic(projectA.ID, "Epic A2")
	epicB := newEpic(projectB.ID, "Epic B")
	itemA := newWorkItem(projectA.ID, "Work item A")
	itemB := newWorkItem(projectB.ID, "Work item B")

	// Both sides of the project-scoped composite foreign keys are exercised.
	wrongEpicProject := newMembership(projectA.ID, epicB.ID, itemA.ID)
	err := db.Create(&wrongEpicProject).Error
	require.Error(t, err)
	require.Contains(t, err.Error(), "fk_delivery_epic_work_items_project_epic")
	wrongWorkItemProject := newMembership(projectA.ID, epicA.ID, itemB.ID)
	err = db.Create(&wrongWorkItemProject).Error
	require.Error(t, err)
	require.Contains(t, err.Error(), "fk_delivery_epic_work_items_project_work_item")

	active := newMembership(projectA.ID, epicA.ID, itemA.ID)
	require.NoError(t, db.Create(&active).Error)
	duplicateActive := newMembership(projectA.ID, epicA2.ID, itemA.ID)
	err = db.Create(&duplicateActive).Error
	require.Error(t, err)
	require.Contains(t, err.Error(), "uidx_delivery_epic_work_items_active_work_item")

	// A soft-deleted membership remains as history but releases the active slot.
	require.NoError(t, db.Delete(&active).Error)
	reassigned := newMembership(projectA.ID, epicA2.ID, itemA.ID)
	require.NoError(t, db.Create(&reassigned).Error)
	var activeCount, historicalCount int64
	require.NoError(t, db.Model(&models.DeliveryEpicWorkItem{}).Where("work_item_id = ?", itemA.ID).Count(&activeCount).Error)
	require.EqualValues(t, 1, activeCount)
	require.NoError(t, db.Unscoped().Model(&models.DeliveryEpicWorkItem{}).Where("work_item_id = ?", itemA.ID).Count(&historicalCount).Error)
	require.EqualValues(t, 2, historicalCount)

	// Simulate a pre-constraint legacy row. The migration must report it and
	// leave the row untouched rather than silently repairing or deleting it.
	require.NoError(t, db.Exec(`ALTER TABLE public.delivery_epic_work_items DROP CONSTRAINT fk_delivery_epic_work_items_project_work_item`).Error)
	legacy := newMembership(projectA.ID, epicA.ID, itemB.ID)
	require.NoError(t, db.Create(&legacy).Error)
	migrationErr := configuration.MigrateModelsForTest(db)
	require.Error(t, migrationErr)
	require.Contains(t, migrationErr.Error(), "legacy delivery epic membership integrity violation")
	var unchanged models.DeliveryEpicWorkItem
	require.NoError(t, db.Unscoped().First(&unchanged, "id = ?", legacy.ID).Error)
	require.Equal(t, projectA.ID, unchanged.ProjectID)
	require.Equal(t, epicA.ID, unchanged.EpicID)
	require.Equal(t, itemB.ID, unchanged.WorkItemID)

	// Once the operator removes the invalid fixture, the migration restores the
	// constraint and remains repeatable with legitimate soft-deleted history.
	require.NoError(t, db.Unscoped().Delete(&legacy).Error)
	require.NoError(t, configuration.MigrateModelsForTest(db))
	require.NoError(t, configuration.MigrateModelsForTest(db))
}
