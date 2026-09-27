//go:build integration

package integration_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	automation "events-stocks/controllers/automation"
	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type automationCostEpicAttributionResponse struct {
	Data struct {
		AppliedFilters struct {
			ProjectID *uuid.UUID `json:"project_id"`
			EpicID    *uuid.UUID `json:"epic_id"`
		} `json:"applied_filters"`
		Summary struct {
			Executions      int64 `json:"executions"`
			Tasks           int64 `json:"tasks"`
			TotalTokens     int64 `json:"total_tokens"`
			TotalCostMicros int64 `json:"total_cost_microusd"`
		} `json:"summary"`
		ByWorkItem []struct {
			WorkItemID      uuid.UUID `json:"work_item_id"`
			Executions      int64     `json:"executions"`
			TotalTokens     int64     `json:"total_tokens"`
			TotalCostMicros int64     `json:"total_cost_microusd"`
		} `json:"by_work_item"`
		ByModel []struct {
			Provider        string `json:"provider"`
			Model           string `json:"model"`
			Executions      int64  `json:"executions"`
			TotalTokens     int64  `json:"total_tokens"`
			TotalCostMicros int64  `json:"total_cost_microusd"`
		} `json:"by_model"`
		RecentExecutions []struct {
			ID                 uuid.UUID  `json:"id"`
			DeliveryWorkItemID *uuid.UUID `json:"delivery_work_item_id"`
			CompletedAt        time.Time  `json:"completed_at"`
			TotalCostMicros    int64      `json:"total_cost_microusd"`
		} `json:"recent_executions"`
	} `json:"data"`
}

// TestAutomationCostOverviewAttributesEpicSpendAtExecutionCompletion verifies
// that the real cost endpoint uses an execution's completed_at to resolve the
// historical epic membership of a task that moved between epics. The exact
// move boundary belongs to the new epic: the previous membership is
// end-exclusive and the replacement membership is start-inclusive.
func TestAutomationCostOverviewAttributesEpicSpendAtExecutionCompletion(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")

	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	actor := "epic-cost-actor-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	boundary := now.Add(-time.Minute)
	rollback := errors.New("rollback epic cost attribution fixture")

	restoreAuth := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		if cognitoSub != actor {
			t.Fatalf("unexpected cognito subject %q", cognitoSub)
		}
		return &models.User{
			ID: uuid.Must(uuid.NewV4()), CognitoSub: actor,
			IsRoot: false, RootLevel: models.RootLevelNone, IsActive: true,
		}, nil
	}})
	t.Cleanup(restoreAuth)

	err := db.Transaction(func(tx *gorm.DB) error {
		clientType := models.ClientType{
			ID: uuid.Must(uuid.NewV4()), Name: "Epic cost type " + suffix,
			Code: "EPICCOST_" + strings.ToUpper(suffix), Level: 10, IsActive: true,
		}
		client := models.Client{
			ID: uuid.Must(uuid.NewV4()), Name: "Epic cost organization " + suffix,
			Code: "epic-cost-org-" + suffix, ClientTypeID: clientType.ID, IsActive: true,
		}
		project := models.DeliveryProject{
			ID: uuid.Must(uuid.NewV4()), ClientID: client.ID,
			Name: "Epic cost project " + suffix, Slug: "epic-cost-project-" + suffix,
			Summary: "Historical epic cost attribution fixture", Status: "active",
			CreatedBy: actor, CreatedAt: now, UpdatedAt: now,
		}
		workItem := models.DeliveryWorkItem{
			ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: actor,
			Title:       "Work item moved between epics " + suffix,
			Description: "Synthetic integration fixture", ExpectedOutcome: "Cost remains attributed to the epic at execution completion",
			State: "implementation", IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`,
			AcceptanceJSON: `[]`, ClientContextJSON: `{}`, PlanJSON: `{}`,
			CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now,
		}
		oldEpic := models.DeliveryEpic{
			ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID,
			Title: "Previous epic " + suffix, Status: "active", CreatedBy: actor,
			CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now,
		}
		newEpic := models.DeliveryEpic{
			ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID,
			Title: "Current epic " + suffix, Status: "active", CreatedBy: actor,
			CreatedAt: boundary, UpdatedAt: now,
		}
		projectMember := models.DeliveryProjectMember{
			ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, CognitoSub: actor,
			Role: "viewer", Permissions: `[]`, CreatedBy: actor,
			CreatedAt: now, UpdatedAt: now,
		}
		for _, value := range []any{&clientType, &client, &project, &workItem, &oldEpic, &newEpic, &projectMember} {
			if err := tx.Create(value).Error; err != nil {
				return err
			}
		}

		// These rows are the persisted history of one epic move at boundary.
		// Keep their timestamps exactly equal so equality behavior is exercised
		// against PostgreSQL timestamp precision, not application-side guesses.
		oldMembership := models.DeliveryEpicWorkItem{
			ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, EpicID: oldEpic.ID,
			WorkItemID: workItem.ID, CreatedBy: actor,
			CreatedAt: boundary.Add(-time.Hour),
			DeletedAt: gorm.DeletedAt{Time: boundary, Valid: true},
		}
		newMembership := models.DeliveryEpicWorkItem{
			ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, EpicID: newEpic.ID,
			WorkItemID: workItem.ID, CreatedBy: actor,
			CreatedAt: boundary,
		}
		if err := tx.Create(&oldMembership).Error; err != nil {
			return err
		}
		if err := tx.Create(&newMembership).Error; err != nil {
			return err
		}

		promptRef := "PRIVATE_PROMPT_REF_" + suffix
		outputRef := "PRIVATE_OUTPUT_REF_" + suffix
		requestRef := "PRIVATE_REQUEST_REF_" + suffix
		responseRef := "PRIVATE_RESPONSE_REF_" + suffix
		createExecution := func(label string, completedAt time.Time, costMicros, tokens int64) (uuid.UUID, error) {
			taskID := uuid.Must(uuid.NewV4())
			runID := uuid.Must(uuid.NewV4()).String()
			task := models.AutomationTask{
				ID: taskID, JobID: uuid.Must(uuid.NewV4()), RequestedBy: actor,
				DeliveryWorkItemID: &workItem.ID, CorrelationID: workItem.ID.String(),
				Operation: "delivery.implementation", InputRef: "s3://private/" + promptRef + "/" + label,
				OutputRef: "s3://private/" + outputRef + "/" + label,
				Provider:  "openrouter", Model: "epic-attribution-test-model", Status: "completed",
				RunID: runID, CompletedAt: &completedAt,
				CreatedAt: completedAt.Add(-time.Minute), UpdatedAt: completedAt,
			}
			if err := tx.Create(&task).Error; err != nil {
				return uuid.Nil, err
			}
			execution := models.AutomationExecution{
				ID: uuid.Must(uuid.NewV4()), AutomationTaskID: task.ID,
				DeliveryWorkItemID: &workItem.ID, RunID: runID,
				WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist",
				MachineID: uuid.Must(uuid.NewV4()).String(), StepKey: "implementation",
				Provider: "openrouter", Model: "epic-attribution-test-model",
				InputTokens: tokens / 2, OutputTokens: tokens - tokens/2, TotalTokens: tokens,
				TotalCostMicros: costMicros, Currency: "USD", PricingBasis: "integration-fixture",
				RequestRef:  "s3://private/" + requestRef + "/" + label,
				ResponseRef: "s3://private/" + responseRef + "/" + label,
				CompletedAt: completedAt, CreatedAt: completedAt,
			}
			if err := tx.Create(&execution).Error; err != nil {
				return uuid.Nil, err
			}
			return execution.ID, nil
		}
		beforeID, err := createExecution("before-move", boundary.Add(-time.Second), 100, 10)
		if err != nil {
			return err
		}
		atBoundaryID, err := createExecution("at-move", boundary, 200, 20)
		if err != nil {
			return err
		}
		afterID, err := createExecution("after-move", boundary.Add(time.Second), 300, 30)
		if err != nil {
			return err
		}

		previousDB := configuration.DB
		configuration.DB = tx
		defer func() { configuration.DB = previousDB }()

		getCosts := func(epicID uuid.UUID) (automationCostEpicAttributionResponse, string) {
			query := url.Values{
				"days": {"30"}, "project_id": {project.ID.String()}, "epic_id": {epicID.String()},
			}
			request := httptest.NewRequest(http.MethodGet, "/api/automation/costs?"+query.Encode(), nil)
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(request, recorder)
			ctx.Set("cognito_sub", actor)
			ctx.Set("workspace_mode", "organization")
			ctx.Set("organization_id", client.ID)
			require.NoError(t, automation.CostOverview(ctx))
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Equal(t, "private, no-store", recorder.Header().Get(echo.HeaderCacheControl))
			var response automationCostEpicAttributionResponse
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
			return response, recorder.Body.String()
		}

		oldResponse, oldBody := getCosts(oldEpic.ID)
		require.NotNil(t, oldResponse.Data.AppliedFilters.ProjectID)
		require.Equal(t, project.ID, *oldResponse.Data.AppliedFilters.ProjectID)
		require.NotNil(t, oldResponse.Data.AppliedFilters.EpicID)
		require.Equal(t, oldEpic.ID, *oldResponse.Data.AppliedFilters.EpicID)
		require.EqualValues(t, 1, oldResponse.Data.Summary.Executions)
		require.EqualValues(t, 1, oldResponse.Data.Summary.Tasks)
		require.EqualValues(t, 10, oldResponse.Data.Summary.TotalTokens)
		require.EqualValues(t, 100, oldResponse.Data.Summary.TotalCostMicros)
		require.Len(t, oldResponse.Data.ByWorkItem, 1)
		require.Equal(t, workItem.ID, oldResponse.Data.ByWorkItem[0].WorkItemID)
		require.EqualValues(t, 1, oldResponse.Data.ByWorkItem[0].Executions)
		require.EqualValues(t, 10, oldResponse.Data.ByWorkItem[0].TotalTokens)
		require.EqualValues(t, 100, oldResponse.Data.ByWorkItem[0].TotalCostMicros)
		require.Len(t, oldResponse.Data.ByModel, 1)
		require.EqualValues(t, 1, oldResponse.Data.ByModel[0].Executions)
		require.EqualValues(t, 100, oldResponse.Data.ByModel[0].TotalCostMicros)
		require.Len(t, oldResponse.Data.RecentExecutions, 1)
		require.Equal(t, beforeID, oldResponse.Data.RecentExecutions[0].ID)
		require.Equal(t, workItem.ID, *oldResponse.Data.RecentExecutions[0].DeliveryWorkItemID)
		require.True(t, oldResponse.Data.RecentExecutions[0].CompletedAt.Equal(boundary.Add(-time.Second)))
		require.EqualValues(t, 100, oldResponse.Data.RecentExecutions[0].TotalCostMicros)

		newResponse, newBody := getCosts(newEpic.ID)
		require.NotNil(t, newResponse.Data.AppliedFilters.ProjectID)
		require.Equal(t, project.ID, *newResponse.Data.AppliedFilters.ProjectID)
		require.NotNil(t, newResponse.Data.AppliedFilters.EpicID)
		require.Equal(t, newEpic.ID, *newResponse.Data.AppliedFilters.EpicID)
		require.EqualValues(t, 2, newResponse.Data.Summary.Executions)
		require.EqualValues(t, 2, newResponse.Data.Summary.Tasks)
		require.EqualValues(t, 50, newResponse.Data.Summary.TotalTokens)
		require.EqualValues(t, 500, newResponse.Data.Summary.TotalCostMicros)
		require.Len(t, newResponse.Data.ByWorkItem, 1)
		require.Equal(t, workItem.ID, newResponse.Data.ByWorkItem[0].WorkItemID)
		require.EqualValues(t, 2, newResponse.Data.ByWorkItem[0].Executions)
		require.EqualValues(t, 50, newResponse.Data.ByWorkItem[0].TotalTokens)
		require.EqualValues(t, 500, newResponse.Data.ByWorkItem[0].TotalCostMicros)
		require.Len(t, newResponse.Data.ByModel, 1)
		require.EqualValues(t, 2, newResponse.Data.ByModel[0].Executions)
		require.EqualValues(t, 500, newResponse.Data.ByModel[0].TotalCostMicros)
		require.Len(t, newResponse.Data.RecentExecutions, 2)
		require.Equal(t, afterID, newResponse.Data.RecentExecutions[0].ID)
		require.True(t, newResponse.Data.RecentExecutions[0].CompletedAt.Equal(boundary.Add(time.Second)))
		require.Equal(t, atBoundaryID, newResponse.Data.RecentExecutions[1].ID,
			"completion exactly at the move boundary must belong to the new epic")
		require.True(t, newResponse.Data.RecentExecutions[1].CompletedAt.Equal(boundary))
		require.EqualValues(t, 200, newResponse.Data.RecentExecutions[1].TotalCostMicros)

		for _, body := range []string{oldBody, newBody} {
			lowerBody := strings.ToLower(body)
			for _, privateValue := range []string{
				promptRef, outputRef, requestRef, responseRef,
				"input_ref", "output_ref", "request_ref", "response_ref",
			} {
				require.NotContains(t, lowerBody, strings.ToLower(privateValue), "cost overview leaked a private task or execution reference")
			}
		}

		return rollback
	})
	require.ErrorIs(t, err, rollback, "the disposable epic-cost attribution fixture must roll back")
}
