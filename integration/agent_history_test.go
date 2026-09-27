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

func TestAgentHistoryCombinesHierarchicalActivityWithCursorPagination(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db)
	suffix := uuid.Must(uuid.NewV4()).String()[:8]
	now := time.Now().UTC().Truncate(time.Microsecond)
	subject := "agent-history-" + suffix
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "History client " + suffix, Code: "HISTORY_" + suffix, Level: 10, IsActive: true}
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "History client " + suffix, Code: "history-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "History project " + suffix, Slug: "history-project-" + suffix, Status: "active", CreatedBy: subject}
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject, Title: "History work item " + suffix, ExpectedOutcome: "safe timeline", State: "implementation", Description: "PRIVATE_PROMPT should never enter this API"}
	completedAt := now.Add(-time.Hour)
	task := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), DeliveryWorkItemID: &item.ID, RequestedBy: subject, CorrelationID: item.ID.String(), Operation: "delivery.implementation", AgentKey: "generalist", RunID: uuid.Must(uuid.NewV4()).String(), Provider: "openrouter", Model: "cheap-model", InputRef: "s3://private-prompt", OutputRef: "s3://private-output", Status: "completed", CompletedAt: &completedAt, CreatedAt: completedAt.Add(-time.Minute), UpdatedAt: completedAt, ErrorMessage: "PRIVATE_OUTPUT must never enter this API"}
	parentTask := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), DeliveryWorkItemID: &item.ID, RequestedBy: subject, CorrelationID: item.ID.String(), Operation: "delivery.implementation", AgentKey: "orchestrator", RunID: "run-parent-" + suffix, InputRef: "s3://private-prompt", Status: "completed", CreatedAt: completedAt.Add(-3 * time.Hour), UpdatedAt: completedAt.Add(-2 * time.Hour)}
	plan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Version: 1, Status: "approved", Summary: "history fixture", StructuredJSON: `{}`, CreatedAt: completedAt.Add(-2 * time.Hour)}
	step := models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "implement", IdempotencyKey: "step-history-" + suffix, DisplayOrder: 1, Title: "Implement", AcceptanceCriteriaJSON: `[]`, Status: models.DeliveryPlanStepCompleted}
	gate := models.DeliveryGate{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Kind: "plan_approval", Decision: "approved", DecidedBy: subject, DecidedAt: completedAt.Add(-2 * time.Hour), CreatedAt: completedAt.Add(-2 * time.Hour)}
	planExecution := models.DeliveryPlanExecution{ID: uuid.Must(uuid.NewV4()), AutomationTaskID: parentTask.ID, IdempotencyKey: "history-execution-" + suffix, PlanID: plan.ID, PlanVersion: 1, ApprovedGateID: gate.ID, PlanHash: strings.Repeat("a", 64), MaxConcurrency: 2, Status: models.DeliveryPlanExecutionCompleted, CreatedAt: completedAt.Add(-2 * time.Hour), UpdatedAt: completedAt.Add(-2 * time.Hour)}
	assignment := models.DeliveryPlanStepAssignment{ID: uuid.Must(uuid.NewV4()), ExecutionID: planExecution.ID, DeliveryPlanStepID: step.ID, ChildAutomationTaskID: task.ID, Status: models.DeliveryPlanStepAssignmentCompleted, CreatedAt: completedAt.Add(-2 * time.Hour), UpdatedAt: completedAt.Add(-2 * time.Hour)}
	workerID, machineID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
	instanceID := uuid.Must(uuid.NewV4())
	execution := models.AutomationExecution{ID: uuid.Must(uuid.NewV4()), AutomationTaskID: task.ID, DeliveryWorkItemID: &item.ID, RunID: task.RunID, WorkerID: workerID, AgentKey: "generalist", MachineID: machineID, AgentInstanceID: &instanceID, StepKey: "generic-implementation", Provider: "openrouter", Model: "cheap-model", InputTokens: 90, OutputTokens: 24, TotalTokens: 114, TotalCostMicros: 17, PricingBasis: "fixture", RequestRef: "s3://private-request", ResponseRef: "s3://private-response", CompletedAt: now.Add(-2 * time.Minute), CreatedAt: now.Add(-2 * time.Minute)}
	toolExecution := models.AutomationToolExecution{ID: uuid.Must(uuid.NewV4()), AutomationTaskID: task.ID, DeliveryWorkItemID: &item.ID, RunID: task.RunID, WorkerID: execution.WorkerID, AgentKey: "generalist", MachineID: execution.MachineID, AgentInstanceID: &instanceID, Tool: "stagehand", CallKey: "assessment", CallStatus: "completed", StepKey: "generic-tool-step", Provider: "openrouter", Model: "cheap-model", InputTokens: 40, OutputTokens: 10, TotalTokens: 50, TotalCostMicros: 5, PricingBasis: "fixture", RequestRef: "s3://private-tool-request", ResponseRef: "s3://private-tool-response", CompletedAt: now.Add(-3 * time.Minute), CreatedAt: now.Add(-3 * time.Minute)}
	stepActivity := models.DeliveryPlanStepActivityEvent{ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: step.ID, AutomationTaskID: task.ID, RunID: task.RunID, WorkerID: execution.WorkerID, AgentKey: "generalist", MachineID: execution.MachineID, AgentInstanceID: &instanceID, FencingToken: 1, Sequence: 1, Action: models.DeliveryPlanStepActivityTool, Phase: models.DeliveryPlanStepActivityStarted, ToolName: "stagehand", Summary: "Tool started", OccurredAt: now.Add(-150 * time.Second)}
	event := models.DeliveryPlanStepEvent{ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: step.ID, EventType: models.DeliveryPlanStepEventTransitioned, FromStatus: models.DeliveryPlanStepRunning, ToStatus: models.DeliveryPlanStepCompleted, AutomationTaskID: uuid.Must(uuid.NewV4()), RunID: task.RunID, WorkerID: execution.WorkerID, AgentKey: "generalist", MachineID: execution.MachineID, AgentInstanceID: &instanceID, LeaseFence: 3, Summary: "safe stored summary", OccurredAt: now.Add(-4 * time.Hour)}
	for _, value := range []any{&clientType, &client, &project, &item, &parentTask, &task, &plan, &step, &gate, &planExecution, &assignment, &execution, &toolExecution, &event, &stepActivity} {
		require.NoError(t, db.Create(value).Error)
	}
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: cognitoSub, IsRoot: true, RootLevel: models.RootLevelOperational, IsActive: true}, nil
	}})
	t.Cleanup(restore)

	getPage := func(query url.Values) (int, struct {
		AgentKey string `json:"agent_key"`
		Items    []struct {
			ID              uuid.UUID  `json:"id"`
			Kind            string     `json:"kind"`
			AgentInstanceID *uuid.UUID `json:"agent_instance_id,omitempty"`
			OccurredAt      time.Time  `json:"occurred_at"`
			TaskID          uuid.UUID  `json:"task_id"`
			ClientName      string     `json:"client_name"`
			ProjectName     string     `json:"project_name"`
			WorkItemTitle   string     `json:"work_item_title"`
			StepKey         string     `json:"step_key"`
			Status          string     `json:"status"`
			EventType       string     `json:"event_type"`
			PreviousStatus  string     `json:"previous_status"`
			CurrentAgentKey string     `json:"current_agent_key"`
			AttemptCount    *int       `json:"attempt_count"`
			ActivityAction  string     `json:"activity_action"`
			TotalCost       int64      `json:"total_cost_microusd"`
		} `json:"items"`
		Limit      int    `json:"limit"`
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}, string) {
		request := httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?"+query.Encode(), nil)
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(request, recorder)
		ctx.SetPath("/api/automation/agents/:agentKey/history")
		ctx.SetParamNames("agentKey")
		ctx.SetParamValues("generalist")
		ctx.Set("cognito_sub", subject)
		ctx.Set("workspace_mode", "platform")
		require.NoError(t, automation.GetAgentHistory(ctx))
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
		var projected map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(envelope.Data, &projected))
		var projectedItems []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(projected["items"], &projectedItems))
		for _, item := range projectedItems {
			for _, forbidden := range []string{"input_ref", "output_ref", "usage", "request_ref", "response_ref", "error_message", "details", "input", "output"} {
				require.NotContains(t, item, forbidden)
			}
		}
		var page struct {
			AgentKey string `json:"agent_key"`
			Items    []struct {
				ID              uuid.UUID  `json:"id"`
				Kind            string     `json:"kind"`
				AgentInstanceID *uuid.UUID `json:"agent_instance_id,omitempty"`
				OccurredAt      time.Time  `json:"occurred_at"`
				TaskID          uuid.UUID  `json:"task_id"`
				ClientName      string     `json:"client_name"`
				ProjectName     string     `json:"project_name"`
				WorkItemTitle   string     `json:"work_item_title"`
				StepKey         string     `json:"step_key"`
				Status          string     `json:"status"`
				EventType       string     `json:"event_type"`
				PreviousStatus  string     `json:"previous_status"`
				CurrentAgentKey string     `json:"current_agent_key"`
				AttemptCount    *int       `json:"attempt_count"`
				ActivityAction  string     `json:"activity_action"`
				TotalCost       int64      `json:"total_cost_microusd"`
			} `json:"items"`
			Limit      int    `json:"limit"`
			HasMore    bool   `json:"has_more"`
			NextCursor string `json:"next_cursor"`
		}
		require.NoError(t, json.Unmarshal(envelope.Data, &page))
		return recorder.Code, page, recorder.Body.String()
	}

	query := url.Values{"limit": {"2"}, "project_id": {project.ID.String()}}
	status, first, firstBody := getPage(query)
	require.Equal(t, http.StatusOK, status, firstBody)
	require.Equal(t, "generalist", first.AgentKey)
	require.Equal(t, 2, first.Limit)
	require.True(t, first.HasMore)
	require.Len(t, first.Items, 2)
	require.Equal(t, "inference", first.Items[0].Kind)
	require.Equal(t, "step_activity", first.Items[1].Kind)
	require.Equal(t, "tool", first.Items[1].ActivityAction)
	require.Equal(t, "started", first.Items[1].Status)
	require.Equal(t, client.Name, first.Items[0].ClientName)
	require.Equal(t, project.Name, first.Items[0].ProjectName)
	require.Equal(t, item.Title, first.Items[0].WorkItemTitle)
	require.Equal(t, "implement", first.Items[0].StepKey)
	require.Equal(t, "implement", first.Items[1].StepKey)
	require.Equal(t, int64(17), first.Items[0].TotalCost)
	require.Zero(t, first.Items[1].TotalCost)
	require.NotEmpty(t, first.NextCursor)
	require.NotContains(t, firstBody, "PRIVATE_PROMPT")
	require.NotContains(t, firstBody, "PRIVATE_OUTPUT")
	require.NotContains(t, firstBody, "s3://")

	secondQuery := url.Values{"limit": {"2"}, "project_id": {project.ID.String()}, "cursor": {first.NextCursor}}
	secondStatus, second, secondBody := getPage(secondQuery)
	require.Equal(t, http.StatusOK, secondStatus, secondBody)
	require.Len(t, second.Items, 2)
	require.Equal(t, "tool_call", second.Items[0].Kind)
	require.Equal(t, "task_event", second.Items[1].Kind)
	require.Equal(t, "created", second.Items[1].EventType)
	require.Equal(t, "completed", second.Items[1].Status)
	require.Equal(t, "generalist", second.Items[1].CurrentAgentKey)
	require.NotNil(t, second.Items[1].AttemptCount)
	require.Zero(t, *second.Items[1].AttemptCount)
	require.Equal(t, client.Name, second.Items[1].ClientName)
	require.Equal(t, project.Name, second.Items[1].ProjectName)
	require.Equal(t, item.Title, second.Items[1].WorkItemTitle)
	require.Equal(t, "implement", second.Items[1].StepKey)
	require.True(t, second.HasMore)
	require.NotEmpty(t, second.NextCursor)
	require.NotContains(t, secondBody, "PRIVATE_PROMPT")
	require.NotContains(t, secondBody, "PRIVATE_OUTPUT")
	require.NotContains(t, secondBody, "s3://")

	thirdQuery := url.Values{"limit": {"2"}, "project_id": {project.ID.String()}, "cursor": {second.NextCursor}}
	thirdStatus, third, thirdBody := getPage(thirdQuery)
	require.Equal(t, http.StatusOK, thirdStatus, thirdBody)
	// Human gate decisions on the same work item are relevant context for the
	// agent's execution history, but remain distinct from agent-authored events.
	require.Len(t, third.Items, 2)
	require.Equal(t, "gate_decision", third.Items[0].Kind)
	require.Equal(t, "approved", third.Items[0].Status)
	require.Equal(t, "step_event", third.Items[1].Kind)
	require.Equal(t, client.Name, third.Items[1].ClientName)
	require.Equal(t, project.Name, third.Items[1].ProjectName)
	require.Equal(t, item.Title, third.Items[1].WorkItemTitle)
	require.Equal(t, "implement", third.Items[1].StepKey)
	require.False(t, third.HasMore)
	require.Empty(t, third.NextCursor)
	require.NotContains(t, thirdBody, "PRIVATE_PROMPT")
	require.NotContains(t, thirdBody, "PRIVATE_OUTPUT")
	require.NotContains(t, thirdBody, "s3://")
	require.True(t, strings.Contains(thirdBody, "Plan step event"))

	instanceFilter := url.Values{"limit": {"2"}, "project_id": {project.ID.String()}, "agent_instance_id": {instanceID.String()}}
	filteredStatus, filteredFirst, filteredBody := getPage(instanceFilter)
	require.Equal(t, http.StatusOK, filteredStatus, filteredBody)
	require.Len(t, filteredFirst.Items, 2)
	require.Equal(t, "inference", filteredFirst.Items[0].Kind)
	require.Equal(t, "step_activity", filteredFirst.Items[1].Kind)
	for _, item := range filteredFirst.Items {
		require.NotNil(t, item.AgentInstanceID)
		require.Equal(t, instanceID, *item.AgentInstanceID)
	}
	filteredSecondQuery := url.Values{"limit": {"2"}, "project_id": {project.ID.String()}, "agent_instance_id": {instanceID.String()}, "cursor": {filteredFirst.NextCursor}}
	filteredSecondStatus, filteredSecond, filteredSecondBody := getPage(filteredSecondQuery)
	require.Equal(t, http.StatusOK, filteredSecondStatus, filteredSecondBody)
	require.Len(t, filteredSecond.Items, 2)
	require.Equal(t, "tool_call", filteredSecond.Items[0].Kind)
	require.Equal(t, "step_event", filteredSecond.Items[1].Kind)
	for _, item := range filteredSecond.Items {
		require.NotNil(t, item.AgentInstanceID)
		require.Equal(t, instanceID, *item.AgentInstanceID)
	}
	require.False(t, filteredSecond.HasMore)

	otherInstanceID := uuid.Must(uuid.NewV4())
	crossScopeQuery := url.Values{"limit": {"2"}, "project_id": {project.ID.String()}, "agent_instance_id": {otherInstanceID.String()}, "cursor": {filteredFirst.NextCursor}}
	crossScopeRecorder := httptest.NewRecorder()
	crossScopeContext := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?"+crossScopeQuery.Encode(), nil), crossScopeRecorder)
	crossScopeContext.SetPath("/api/automation/agents/:agentKey/history")
	crossScopeContext.SetParamNames("agentKey")
	crossScopeContext.SetParamValues("generalist")
	crossScopeContext.Set("cognito_sub", subject)
	crossScopeContext.Set("workspace_mode", "platform")
	require.NoError(t, automation.GetAgentHistory(crossScopeContext))
	require.Equal(t, http.StatusBadRequest, crossScopeRecorder.Code, crossScopeRecorder.Body.String())
}

func TestAgentHistoryRecordsTaskAssignmentAndStatusEvents(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db)
	suffix := uuid.Must(uuid.NewV4()).String()[:8]
	subject := "agent-history-events-" + suffix
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Event client type " + suffix, Code: "EVENT_" + suffix, Level: 10, IsActive: true}
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Event client " + suffix, Code: "event-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Event project " + suffix, Slug: "event-project-" + suffix, Status: "active", CreatedBy: subject}
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject, Title: "Assignment history " + suffix, ExpectedOutcome: "append-only lifecycle", State: "implementation", Description: "safe"}
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := models.AutomationTask{
		ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), DeliveryWorkItemID: &item.ID,
		RequestedBy: subject, CorrelationID: item.ID.String(), Operation: "delivery.implementation",
		AgentKey: "generalist", Status: "queued", InputRef: "s3://private-input",
		CreatedAt: now, UpdatedAt: now,
	}
	for _, value := range []any{&clientType, &client, &project, &item, &task} {
		require.NoError(t, db.Create(value).Error)
	}
	oldWorker, oldMachine, oldInstance := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	newWorker, newMachine, newInstance := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	runID := uuid.Must(uuid.NewV4()).String()
	require.NoError(t, db.Model(&models.AutomationTask{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status": "running", "run_id": runID, "worker_id": oldWorker.String(), "machine_id": oldMachine.String(),
		"agent_instance_id": oldInstance, "attempt_count": 1,
	}).Error)
	require.NoError(t, db.Model(&models.AutomationTask{}).Where("id = ?", task.ID).Updates(map[string]any{
		"agent_key": "specialist", "worker_id": newWorker.String(), "machine_id": newMachine.String(), "agent_instance_id": newInstance,
	}).Error)
	require.NoError(t, db.Model(&models.AutomationTask{}).Where("id = ?", task.ID).Update("status", "completed").Error)

	var events []models.AutomationTaskEvent
	require.NoError(t, db.Where("automation_task_id = ?", task.ID).Order("sequence ASC").Find(&events).Error)
	require.Len(t, events, 4)
	require.Equal(t, []string{"created", "claimed", "assignment_changed", "status_transition"}, []string{events[0].EventType, events[1].EventType, events[2].EventType, events[3].EventType})
	require.Equal(t, []int64{1, 2, 3, 4}, []int64{events[0].Sequence, events[1].Sequence, events[2].Sequence, events[3].Sequence})
	require.Equal(t, "queued", events[0].Status)
	require.Equal(t, "queued", events[1].PreviousStatus)
	require.Equal(t, "running", events[1].Status)
	require.Equal(t, "generalist", events[2].PreviousAgentKey)
	require.Equal(t, "specialist", events[2].AgentKey)
	require.Equal(t, oldInstance, *events[2].PreviousAgentInstanceID)
	require.Equal(t, newInstance, *events[2].AgentInstanceID)
	require.Equal(t, "running", events[3].PreviousStatus)
	require.Equal(t, "completed", events[3].Status)
	// The database trigger, not just a GORM hook, protects the audit ledger.
	require.Error(t, db.Exec("UPDATE automation_task_events SET status = 'failed' WHERE id = ?", events[0].ID).Error)
	require.Error(t, db.Exec("DELETE FROM automation_task_events WHERE id = ?", events[0].ID).Error)

	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: cognitoSub, IsRoot: true, RootLevel: models.RootLevelOperational, IsActive: true}, nil
	}})
	t.Cleanup(restore)
	type historyEntry struct {
		Kind                    string     `json:"kind"`
		EventType               string     `json:"event_type"`
		Status                  string     `json:"status"`
		PreviousStatus          string     `json:"previous_status"`
		CurrentAgentKey         string     `json:"current_agent_key"`
		PreviousAgentKey        string     `json:"previous_agent_key"`
		WorkerID                string     `json:"worker_id"`
		PreviousWorkerID        string     `json:"previous_worker_id"`
		AgentInstanceID         *uuid.UUID `json:"agent_instance_id"`
		PreviousAgentInstanceID *uuid.UUID `json:"previous_agent_instance_id"`
		AttemptCount            *int       `json:"attempt_count"`
		EventSequence           int64      `json:"event_sequence"`
	}
	historyFor := func(agentKey string, query url.Values) (int, []historyEntry, string) {
		query.Set("project_id", project.ID.String())
		request := httptest.NewRequest(http.MethodGet, "/api/automation/agents/"+agentKey+"/history?"+query.Encode(), nil)
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(request, recorder)
		ctx.SetPath("/api/automation/agents/:agentKey/history")
		ctx.SetParamNames("agentKey")
		ctx.SetParamValues(agentKey)
		ctx.Set("cognito_sub", subject)
		ctx.Set("workspace_mode", "platform")
		require.NoError(t, automation.GetAgentHistory(ctx))
		var envelope struct {
			Data struct {
				Items []historyEntry `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
		return recorder.Code, envelope.Data.Items, recorder.Body.String()
	}
	status, generalistEvents, body := historyFor("generalist", url.Values{"limit": {"20"}})
	require.Equal(t, http.StatusOK, status, body)
	require.Len(t, generalistEvents, 3)
	// The newest event is assignment_changed; it appears in both agent histories
	// and retains both sides of the identity transition, newest first.
	require.Equal(t, "assignment_changed", generalistEvents[0].EventType)
	require.Equal(t, "specialist", generalistEvents[0].CurrentAgentKey)
	require.Equal(t, "generalist", generalistEvents[0].PreviousAgentKey)
	require.Equal(t, newWorker.String(), generalistEvents[0].WorkerID)
	require.Equal(t, oldWorker.String(), generalistEvents[0].PreviousWorkerID)
	require.NotContains(t, body, "s3://private-input")
	require.NotNil(t, generalistEvents[1].AttemptCount)
	require.Equal(t, 1, *generalistEvents[1].AttemptCount)

	filteredStatus, filteredEvents, filteredBody := historyFor("generalist", url.Values{"limit": {"20"}, "agent_instance_id": {oldInstance.String()}})
	require.Equal(t, http.StatusOK, filteredStatus, filteredBody)
	require.Len(t, filteredEvents, 2)
	for _, event := range filteredEvents {
		require.Equal(t, "task_event", event.Kind)
	}

	specialistStatus, specialistEvents, specialistBody := historyFor("specialist", url.Values{"limit": {"20"}})
	require.Equal(t, http.StatusOK, specialistStatus, specialistBody)
	require.Len(t, specialistEvents, 2)
	require.Equal(t, "status_transition", specialistEvents[0].EventType)
	require.Equal(t, "completed", specialistEvents[0].Status)
	require.Equal(t, "assignment_changed", specialistEvents[1].EventType)
}
