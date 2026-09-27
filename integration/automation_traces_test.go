//go:build integration

package integration_test

import (
	"encoding/json"
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
)

func TestGlobalAutomationTracesMergesJSONBActivityAndPaginatesSafely(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")

	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	subject := "global-traces-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	clientType := models.ClientType{
		ID: uuid.Must(uuid.NewV4()), Name: "Trace client type " + suffix,
		Code: "TRACE_" + suffix, Level: 10, IsActive: true,
	}
	client := models.Client{
		ID: uuid.Must(uuid.NewV4()), Name: "Trace client " + suffix,
		Code: "trace-" + suffix, ClientTypeID: clientType.ID, IsActive: true,
	}
	project := models.DeliveryProject{
		ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Trace project " + suffix,
		Slug: "trace-project-" + suffix, Status: "active", CreatedBy: subject,
	}
	workItem := models.DeliveryWorkItem{
		ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject,
		Title: "Trace work item " + suffix, ExpectedOutcome: "Exercise global trace SQL",
		State: "implementation",
	}
	plan := models.DeliveryPlan{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: workItem.ID, Version: 1,
		Status: "approved", Summary: "Trace fixture", StructuredJSON: `{}`,
		CreatedAt: now.Add(-time.Minute),
	}
	step := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "trace-regression",
		IdempotencyKey: "trace-regression-" + suffix, DisplayOrder: 1,
		Title: "Trace regression fixture", AcceptanceCriteriaJSON: `[]`,
		Status: models.DeliveryPlanStepCompleted,
	}
	runID := uuid.Must(uuid.NewV4()).String()
	task := models.AutomationTask{
		ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()),
		DeliveryWorkItemID: &workItem.ID, RequestedBy: subject,
		CorrelationID: workItem.ID.String(), Operation: "delivery.implementation",
		AgentKey: "generalist", RunID: runID, Provider: "openai", Model: "task-snapshot-" + suffix,
		InputRef:     "s3://private-trace-test/PRIVATE_TRACE_INPUT_" + suffix,
		ErrorMessage: "PRIVATE_TRACE_ERROR_" + suffix, Status: "completed",
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	execution := models.AutomationExecution{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: task.ID, DeliveryWorkItemID: &workItem.ID,
		RunID: runID, AgentKey: "generalist", Provider: "openai", Model: "openai/gpt-4.1-mini",
		InputTokens: 20, OutputTokens: 5, TotalTokens: 25, TotalCostMicros: 2,
		RequestRef: "s3://private-trace-test/request", ResponseRef: "s3://private-trace-test/response",
		CompletedAt: now.Add(-20 * time.Second), CreatedAt: now.Add(-20 * time.Second),
	}
	workerID := uuid.Must(uuid.NewV4()).String()
	machineID := uuid.Must(uuid.NewV4()).String()
	started := models.DeliveryPlanStepActivityEvent{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: step.ID,
		AutomationTaskID: task.ID, RunID: runID, WorkerID: workerID,
		AgentKey: "generalist", MachineID: machineID, FencingToken: 1,
		Sequence: 1, Action: models.DeliveryPlanStepActivityFileChange,
		Phase: models.DeliveryPlanStepActivityStarted, Summary: "File change started",
		OccurredAt: now.Add(-30 * time.Second), CreatedAt: now.Add(-30 * time.Second),
	}
	completed := models.DeliveryPlanStepActivityEvent{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: step.ID,
		AutomationTaskID: task.ID, RunID: runID, WorkerID: workerID,
		AgentKey: "generalist", MachineID: machineID, FencingToken: 1,
		Sequence: 2, Action: models.DeliveryPlanStepActivityFileChange,
		Phase: models.DeliveryPlanStepActivityCompleted, Summary: "File change completed",
		DetailsJSON: `{"resource_references":["workspace://backend"],"changed_files":["src/api.go"]}`,
		OccurredAt:  now.Add(-10 * time.Second), CreatedAt: now.Add(-10 * time.Second),
	}
	for _, value := range []any{&clientType, &client, &project, &workItem, &plan, &step, &task, &execution, &started, &completed} {
		require.NoError(t, db.Create(value).Error)
	}
	// The activity ledger is append-only. These uniquely keyed
	// fixtures intentionally live only until integration TestMain tears down its
	// disposable PostgreSQL database.

	restoreHooks := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		if cognitoSub != subject {
			t.Fatalf("unexpected cognito subject %q", cognitoSub)
		}
		return &models.User{CognitoSub: subject, IsRoot: true, RootLevel: models.RootLevelOperational, IsActive: true}, nil
	}})
	t.Cleanup(restoreHooks)

	requestPage := func(query url.Values) (int, struct {
		Items []struct {
			ID              uuid.UUID                               `json:"id"`
			Kind            string                                  `json:"kind"`
			EventType       string                                  `json:"event_type"`
			Model           string                                  `json:"model"`
			StepID          uuid.UUID                               `json:"step_id"`
			StepKey         string                                  `json:"step_key"`
			Status          string                                  `json:"status"`
			ActivityAction  string                                  `json:"activity_action"`
			ActivityDetails *models.DeliveryPlanStepActivityDetails `json:"activity_details"`
		} `json:"items"`
		HasMore    bool      `json:"has_more"`
		NextCursor string    `json:"next_cursor"`
		SnapshotAt time.Time `json:"snapshot_at"`
		Limit      int       `json:"limit"`
	}, string) {
		request := httptest.NewRequest(http.MethodGet, "/api/automation/traces?"+query.Encode(), nil)
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(request, recorder)
		ctx.Set("cognito_sub", subject)
		ctx.Set("workspace_mode", "platform")
		require.NoError(t, automation.GetAutomationTraces(ctx))
		require.Equal(t, "private, no-store", recorder.Header().Get(echo.HeaderCacheControl))
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
		var page struct {
			Items []struct {
				ID              uuid.UUID                               `json:"id"`
				Kind            string                                  `json:"kind"`
				EventType       string                                  `json:"event_type"`
				Model           string                                  `json:"model"`
				StepID          uuid.UUID                               `json:"step_id"`
				StepKey         string                                  `json:"step_key"`
				Status          string                                  `json:"status"`
				ActivityAction  string                                  `json:"activity_action"`
				ActivityDetails *models.DeliveryPlanStepActivityDetails `json:"activity_details"`
			} `json:"items"`
			HasMore    bool      `json:"has_more"`
			NextCursor string    `json:"next_cursor"`
			SnapshotAt time.Time `json:"snapshot_at"`
			Limit      int       `json:"limit"`
		}
		require.NoError(t, json.Unmarshal(envelope.Data, &page))
		return recorder.Code, page, recorder.Body.String()
	}

	baseQuery := url.Values{
		"client_id": {client.ID.String()}, "project_id": {project.ID.String()},
		"step_id": {step.ID.String()}, "limit": {"1"},
	}
	firstStatus, first, firstBody := requestPage(baseQuery)
	require.Equal(t, http.StatusOK, firstStatus, firstBody)
	require.Equal(t, 1, first.Limit)
	require.Len(t, first.Items, 1)
	require.Equal(t, "step_activity", first.Items[0].Kind)
	require.Equal(t, step.ID, first.Items[0].StepID)
	require.Equal(t, step.StepKey, first.Items[0].StepKey)
	require.Equal(t, models.DeliveryPlanStepActivityCompleted, first.Items[0].Status)
	require.Equal(t, models.DeliveryPlanStepActivityFileChange, first.Items[0].ActivityAction)
	require.NotNil(t, first.Items[0].ActivityDetails)
	require.Equal(t, []string{"src/api.go"}, first.Items[0].ActivityDetails.ChangedFiles)
	require.Equal(t, []string{"workspace://backend"}, first.Items[0].ActivityDetails.ResourceReferences)
	require.True(t, first.HasMore)
	require.NotEmpty(t, first.NextCursor)
	require.NotContains(t, firstBody, "PRIVATE_TRACE_INPUT_")
	require.NotContains(t, firstBody, "PRIVATE_TRACE_ERROR_")
	require.NotContains(t, firstBody, "s3://private-trace-test")

	secondQuery := url.Values{
		"client_id": {client.ID.String()}, "project_id": {project.ID.String()},
		"step_id": {step.ID.String()}, "limit": {"1"}, "cursor": {first.NextCursor},
		"snapshot_at": {first.SnapshotAt.Format(time.RFC3339Nano)},
	}
	secondStatus, second, secondBody := requestPage(secondQuery)
	require.Equal(t, http.StatusOK, secondStatus, secondBody)
	require.Len(t, second.Items, 1)
	require.Equal(t, "step_activity", second.Items[0].Kind)
	require.Equal(t, models.DeliveryPlanStepActivityStarted, second.Items[0].Status)
	require.Nil(t, second.Items[0].ActivityDetails)
	require.False(t, second.HasMore)
	require.Empty(t, second.NextCursor)
	require.NotContains(t, secondBody, "PRIVATE_TRACE_INPUT_")
	require.NotContains(t, secondBody, "PRIVATE_TRACE_ERROR_")

	// Model filtering is an exact comparison over projected provider-call rows,
	// while run_id correlates all timeline sources for the same execution run.
	modelQuery := url.Values{
		"client_id": {client.ID.String()}, "project_id": {project.ID.String()},
		"model": {"openai/gpt-4.1-mini"}, "limit": {"100"},
	}
	modelStatus, modelPage, modelBody := requestPage(modelQuery)
	require.Equal(t, http.StatusOK, modelStatus, modelBody)
	require.Len(t, modelPage.Items, 1)
	require.Equal(t, execution.ID, modelPage.Items[0].ID)
	require.Equal(t, "inference", modelPage.Items[0].Kind)
	require.Equal(t, "openai/gpt-4.1-mini", modelPage.Items[0].Model)
	require.NotContains(t, modelBody, "PRIVATE_TRACE_INPUT_")
	require.NotContains(t, modelBody, "s3://private-trace-test")

	runQuery := url.Values{
		"client_id": {client.ID.String()}, "project_id": {project.ID.String()},
		"run_id": {runID}, "limit": {"100"},
	}
	runStatus, runPage, runBody := requestPage(runQuery)
	require.Equal(t, http.StatusOK, runStatus, runBody)
	seenRunItems := map[uuid.UUID]string{}
	hasTaskLifecycle := false
	for _, item := range runPage.Items {
		seenRunItems[item.ID] = item.Kind
		if item.Kind == "task_event" && item.EventType == "created" {
			hasTaskLifecycle = true
		}
	}
	require.Equal(t, "inference", seenRunItems[execution.ID], runBody)
	require.True(t, hasTaskLifecycle, "run_id must also filter task lifecycle events: %s", runBody)
	require.Equal(t, "step_activity", seenRunItems[started.ID], runBody)
	require.Equal(t, "step_activity", seenRunItems[completed.ID], runBody)
}
