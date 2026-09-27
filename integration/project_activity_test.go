//go:build integration

package integration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"events-stocks/configuration"
	delivery "events-stocks/controllers/delivery"
	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProjectActivityCombinesHierarchicalEventsWithCursorAndSafeProjection(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")
	suffix := uuid.Must(uuid.NewV4()).String()[:8]
	subject := "project-activity-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	rollback := errors.New("rollback project activity fixture")

	restoreAuth := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: cognitoSub, IsRoot: true, RootLevel: models.RootLevelPrimary, IsActive: true}, nil
	}})
	t.Cleanup(restoreAuth)

	err := db.Transaction(func(tx *gorm.DB) error {
		clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Activity " + suffix, Code: "ACTIVITY_" + suffix, Level: 10, IsActive: true}
		client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Activity client " + suffix, Code: "activity-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
		project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Activity project " + suffix, Slug: "activity-project-" + suffix, Status: "active", CreatedBy: subject}
		item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject, Title: "Activity work item", State: "implementation"}
		plan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Version: 1, Status: "approved", Summary: "private plan summary", StructuredJSON: `{}`, CreatedAt: now.Add(-5 * time.Minute)}
		step := models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "implement", IdempotencyKey: "project-activity-" + suffix, Role: models.DeliveryPlanStepRoleImplementation, DisplayOrder: 1, Title: "Implement", AcceptanceCriteriaJSON: `[]`, Status: models.DeliveryPlanStepRunning, CreatedAt: now.Add(-5 * time.Minute), UpdatedAt: now.Add(-5 * time.Minute)}
		epic := models.DeliveryEpic{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, Title: "Activity epic", Status: "active", CreatedBy: subject, CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-10 * time.Minute)}
		membership := models.DeliveryEpicWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, EpicID: epic.ID, WorkItemID: item.ID, CreatedBy: subject, CreatedAt: now.Add(-10 * time.Minute)}
		for _, value := range []any{&clientType, &client, &project, &item, &plan, &step, &epic, &membership} {
			if err := tx.Create(value).Error; err != nil {
				return err
			}
		}
		workerID, machineID, instanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
		stepEvent := models.DeliveryPlanStepEvent{
			ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: step.ID, EventType: models.DeliveryPlanStepEventTransitioned,
			FromStatus: models.DeliveryPlanStepRunning, ToStatus: models.DeliveryPlanStepCompleted, AutomationTaskID: uuid.Must(uuid.NewV4()),
			RunID: uuid.Must(uuid.NewV4()).String(), WorkerID: workerID.String(), AgentKey: "generalist", MachineID: machineID.String(),
			AgentInstanceID: &instanceID, Summary: "PRIVATE_LIFECYCLE_SENTINEL", OccurredAt: now.Add(-3 * time.Minute),
		}
		assignmentEvent := models.DeliveryPlanStepAssignmentEvent{
			ID: uuid.Must(uuid.NewV4()), AssignmentID: uuid.Must(uuid.NewV4()), ExecutionID: uuid.Must(uuid.NewV4()), PlanID: plan.ID,
			PlanVersion: 1, StepID: step.ID, ParentAutomationTaskID: uuid.Must(uuid.NewV4()), ChildAutomationTaskID: uuid.Must(uuid.NewV4()),
			EventType: models.DeliveryPlanStepAssignmentEventCreated, Status: models.DeliveryPlanStepAssignmentDispatched,
			TargetAgentKey: "generalist", TargetMachineID: machineID.String(), OccurredAt: now.Add(-2 * time.Minute),
		}
		runID := uuid.Must(uuid.NewV4())
		activity := models.DeliveryPlanStepActivityEvent{
			ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: step.ID, AutomationTaskID: uuid.Must(uuid.NewV4()), RunID: runID.String(),
			WorkerID: workerID.String(), AgentKey: "generalist", MachineID: machineID.String(), AgentInstanceID: &instanceID,
			FencingToken: 1, Sequence: 1, Action: models.DeliveryPlanStepActivityTool, Phase: models.DeliveryPlanStepActivityCompleted,
			ToolName: "stagehand", Summary: "PRIVATE_ACTIVITY_SENTINEL", DetailsJSON: `{"changed_files":["PRIVATE_DETAIL_SENTINEL"]}`,
			OccurredAt: now.Add(-time.Minute),
		}
		for _, event := range []any{&stepEvent, &assignmentEvent, &activity} {
			if err := tx.Create(event).Error; err != nil {
				return err
			}
		}

		previousDB := configuration.DB
		configuration.DB = tx
		defer func() { configuration.DB = previousDB }()
		getPage := func(query url.Values) (int, deliveryPage, string) {
			target := fmt.Sprintf("/api/automation/projects/%s/activity?%s", project.ID, query.Encode())
			request := httptest.NewRequest(http.MethodGet, target, nil)
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(request, recorder)
			ctx.SetParamNames("id")
			ctx.SetParamValues(project.ID.String())
			ctx.Set("cognito_sub", subject)
			ctx.Set("workspace_mode", "platform")
			require.NoError(t, delivery.ListProjectActivity(ctx))
			var envelope struct {
				Data json.RawMessage `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
			require.NotEmpty(t, envelope.Data, "project activity returned no data: status=%d body=%s", recorder.Code, recorder.Body.String())
			var page deliveryPage
			require.NoError(t, json.Unmarshal(envelope.Data, &page))
			return recorder.Code, page, recorder.Body.String()
		}

		firstStatus, first, firstBody := getPage(url.Values{"limit": {"2"}, "epic_id": {epic.ID.String()}})
		require.Equal(t, http.StatusOK, firstStatus, firstBody)
		require.Equal(t, project.ID, first.ProjectID)
		require.Len(t, first.Items, 2)
		require.Equal(t, "activity", first.Items[0].Kind)
		require.Equal(t, "tool", first.Items[0].Action)
		require.Equal(t, "Tool completed", first.Items[0].Summary)
		require.Equal(t, "assignment", first.Items[1].Kind)
		require.Equal(t, epic.ID, *first.Items[0].EpicID)
		require.NotEmpty(t, first.NextCursor)
		for _, privateValue := range []string{"PRIVATE_LIFECYCLE_SENTINEL", "PRIVATE_ACTIVITY_SENTINEL", "PRIVATE_DETAIL_SENTINEL", "changed_files", "DetailsJSON"} {
			require.NotContains(t, firstBody, privateValue)
		}

		secondStatus, second, secondBody := getPage(url.Values{"limit": {"2"}, "epic_id": {epic.ID.String()}, "cursor": {first.NextCursor}})
		require.Equal(t, http.StatusOK, secondStatus, secondBody)
		require.Len(t, second.Items, 1)
		require.Equal(t, "step", second.Items[0].Kind)
		require.Equal(t, "status_transitioned", second.Items[0].EventType)
		require.Equal(t, models.DeliveryPlanStepRunning, second.Items[0].FromStatus)
		require.Equal(t, models.DeliveryPlanStepCompleted, second.Items[0].ToStatus)
		require.Empty(t, second.NextCursor)
		return rollback
	})
	require.ErrorIs(t, err, rollback, "the disposable hierarchical fixture must roll back")
}

type deliveryPage struct {
	ProjectID uuid.UUID `json:"project_id"`
	Items     []struct {
		Kind       string     `json:"kind"`
		EventType  string     `json:"event_type"`
		Action     string     `json:"action"`
		Summary    string     `json:"summary"`
		FromStatus string     `json:"from_status"`
		ToStatus   string     `json:"to_status"`
		EpicID     *uuid.UUID `json:"epic_id"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
}
