//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"events-stocks/configuration"
	delivery "events-stocks/controllers/delivery"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestManualWorkItemEpicContextIsAtomic exercises the manual create endpoint,
// which delegates materialization to createWorkItemInTransaction. The outer
// transaction is deliberately rolled back, while the endpoint's own
// transaction runs as a nested savepoint. That lets the test inspect the
// committed-within-transaction contract without leaving fixtures behind.
func TestManualWorkItemEpicContextIsAtomic(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")

	suffix := uuid.Must(uuid.NewV4()).String()[:8]
	actor := "integration-manual-epic-" + suffix
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Manual epic " + suffix, Code: "MANEPIC_" + suffix, Level: 10, IsActive: true}
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Manual epic " + suffix, Code: "manual-epic-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Manual epic project " + suffix, Slug: "manual-epic-" + suffix, Summary: "integration", Status: "active", CreatedBy: actor}
	otherProject := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Other project " + suffix, Slug: "other-epic-" + suffix, Summary: "integration", Status: "active", CreatedBy: actor}
	contextSource := models.DeliveryContextSource{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, Kind: "document", Name: "Manual task source", Reference: "document://manual-epic-fixture", Revision: "r1", Status: "ready", MetadataJSON: `{}`}
	epic := models.DeliveryEpic{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, Title: "API rollout " + suffix, Summary: "Summary captured when the task is created", Status: "planned", CreatedBy: actor}
	foreignEpic := models.DeliveryEpic{ID: uuid.Must(uuid.NewV4()), ProjectID: otherProject.ID, Title: "Foreign epic " + suffix, Summary: "Must not be attached", Status: "planned", CreatedBy: actor}

	restoreHooks := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		if cognitoSub != actor {
			return nil, fmt.Errorf("unexpected cognito subject %q", cognitoSub)
		}
		return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: actor, IsRoot: true, RootLevel: models.RootLevelOperational, IsActive: true}, nil
	}})
	t.Cleanup(restoreHooks)

	intentionalRollback := errors.New("rollback manual work-item epic fixture")
	caseErr := db.Transaction(func(tx *gorm.DB) error {
		for _, fixture := range []any{&clientType, &client, &project, &otherProject, &contextSource, &epic, &foreignEpic} {
			if err := tx.Create(fixture).Error; err != nil {
				return fmt.Errorf("create fixture %T: %w", fixture, err)
			}
		}

		create := func(projectID uuid.UUID, epicID, title string) *httptest.ResponseRecorder {
			t.Helper()
			payload := map[string]any{
				"epic_id": epicID, "context_source_ids": []string{contextSource.ID.String()},
				"title": title, "description": "Create a planning-only task under a project epic",
				"expected_outcome": "An approved plan can be prepared with frozen context",
				"included_scope":   []string{"documented API"}, "excluded_scope": []string{"production changes"},
				"acceptance_criteria": []string{"task and context are persisted together"},
			}
			body, err := json.Marshal(payload)
			require.NoError(t, err)
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/api/automation/projects/"+projectID.String()+"/work-items", bytes.NewReader(body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			recorder := httptest.NewRecorder()
			c := e.NewContext(req, recorder)
			c.SetPath("/api/automation/projects/:id/work-items")
			c.SetParamNames("id")
			c.SetParamValues(projectID.String())
			c.Set("cognito_sub", actor)
			c.Set("tenant_code", "itbem")
			setDeliveryPlatformWorkspace(c)

			// The handler opens its own transaction. Point it at this test's outer
			// transaction so GORM uses a savepoint; the final intentional rollback
			// then removes both fixtures and all successfully materialized rows.
			configuration.DB = tx
			handlerErr := delivery.CreateWorkItem(c)
			configuration.DB = db
			require.NoError(t, handlerErr)
			return recorder
		}

		validTitle := "Manual task with epic " + suffix
		valid := create(project.ID, epic.ID.String(), validTitle)
		require.Equal(t, http.StatusCreated, valid.Code, valid.Body.String())
		var item models.DeliveryWorkItem
		if err := tx.Where("project_id = ? AND title = ?", project.ID, validTitle).First(&item).Error; err != nil {
			return fmt.Errorf("load created work item: %w", err)
		}
		require.Equal(t, deliveryworkflow.StatePlanning, item.State)
		var membership models.DeliveryEpicWorkItem
		if err := tx.Where("project_id = ? AND epic_id = ? AND work_item_id = ?", project.ID, epic.ID, item.ID).First(&membership).Error; err != nil {
			return fmt.Errorf("load epic membership: %w", err)
		}
		require.Equal(t, actor, membership.CreatedBy)
		var snapshot models.DeliveryContextSnapshot
		if err := tx.Where("work_item_id = ? AND source_id = ? AND kind = ?", item.ID, membership.ID, "epic").First(&snapshot).Error; err != nil {
			return fmt.Errorf("load epic snapshot: %w", err)
		}
		require.Equal(t, epic.Title, snapshot.Name)
		require.Equal(t, "epic://"+epic.ID.String(), snapshot.Reference)
		var captured struct {
			EpicID  uuid.UUID `json:"epic_id"`
			Status  string    `json:"status"`
			Summary string    `json:"summary"`
		}
		require.NoError(t, json.Unmarshal([]byte(snapshot.MetadataJSON), &captured))
		require.Equal(t, epic.ID, captured.EpicID)
		require.Equal(t, epic.Status, captured.Status)
		require.Equal(t, epic.Summary, captured.Summary)

		// The planning continuation is durable only alongside the task's frozen
		// epic membership and snapshot; task creation itself does not draft or
		// dispatch a plan/provider job.
		var continuations []models.DeliveryContinuation
		if err := tx.Where("work_item_id = ?", item.ID).Find(&continuations).Error; err != nil {
			return fmt.Errorf("load planning continuation: %w", err)
		}
		require.Len(t, continuations, 1)
		require.Equal(t, "plan", continuations[0].Phase)
		require.Equal(t, "pending", continuations[0].Status)
		var plans, automationTasks int64
		require.NoError(t, tx.Model(&models.DeliveryPlan{}).Where("work_item_id = ?", item.ID).Count(&plans).Error)
		require.NoError(t, tx.Model(&models.AutomationTask{}).Where("delivery_work_item_id = ?", item.ID).Count(&automationTasks).Error)
		require.Zero(t, plans)
		require.Zero(t, automationTasks)

		// Epic edits after creation must not rewrite the task's captured context.
		if err := tx.Model(&models.DeliveryEpic{}).Where("id = ?", epic.ID).Update("summary", "Updated after task creation").Error; err != nil {
			return fmt.Errorf("update source epic summary: %w", err)
		}
		var unchangedSnapshot models.DeliveryContextSnapshot
		if err := tx.First(&unchangedSnapshot, "id = ?", snapshot.ID).Error; err != nil {
			return fmt.Errorf("reload frozen epic snapshot: %w", err)
		}
		require.Equal(t, snapshot.MetadataJSON, unchangedSnapshot.MetadataJSON)
		require.Equal(t, "Summary captured when the task is created", captured.Summary)

		assertNoPartialCreate := func(title string) {
			t.Helper()
			var workItemCount, continuationCount, membershipCount, snapshotCount int64
			require.NoError(t, tx.Model(&models.DeliveryWorkItem{}).Where("project_id = ? AND title = ?", project.ID, title).Count(&workItemCount).Error)
			require.NoError(t, tx.Model(&models.DeliveryContinuation{}).Where("work_item_id IN (SELECT id FROM delivery_work_items WHERE project_id = ? AND title = ?)", project.ID, title).Count(&continuationCount).Error)
			require.NoError(t, tx.Model(&models.DeliveryEpicWorkItem{}).Where("project_id = ? AND work_item_id IN (SELECT id FROM delivery_work_items WHERE project_id = ? AND title = ?)", project.ID, project.ID, title).Count(&membershipCount).Error)
			require.NoError(t, tx.Model(&models.DeliveryContextSnapshot{}).Where("work_item_id IN (SELECT id FROM delivery_work_items WHERE project_id = ? AND title = ?)", project.ID, title).Count(&snapshotCount).Error)
			require.Zero(t, workItemCount)
			require.Zero(t, continuationCount)
			require.Zero(t, membershipCount)
			require.Zero(t, snapshotCount)
		}

		foreignTitle := "Rejected foreign epic task " + suffix
		foreign := create(project.ID, foreignEpic.ID.String(), foreignTitle)
		require.Equal(t, http.StatusNotFound, foreign.Code, foreign.Body.String())
		assertNoPartialCreate(foreignTitle)

		invalidTitle := "Rejected malformed epic task " + suffix
		invalid := create(project.ID, "not-a-uuid", invalidTitle)
		require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())
		assertNoPartialCreate(invalidTitle)

		return intentionalRollback
	})
	require.ErrorIs(t, caseErr, intentionalRollback, "all fixtures and created rows should be rolled back")
}
