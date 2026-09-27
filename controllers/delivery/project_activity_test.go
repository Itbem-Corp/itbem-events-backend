package delivery

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
)

func TestProjectActivityFiltersCursorAndPageSizeAreStrict(t *testing.T) {
	projectID, epicID, workItemID, eventID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	until := from.Add(24 * time.Hour)
	ctx, _ := epicTestContext(http.MethodGet, "/api/automation/projects/"+projectID.String()+"/activity?epic_id="+epicID.String()+"&work_item_id="+workItemID.String()+"&agent_key=generalist&kind=activity&action=tool&status=completed&from="+from.Format(time.RFC3339)+"&to="+until.Format(time.RFC3339), []string{"id"}, []string{projectID.String()}, "")
	filters, err := parseProjectActivityFilters(ctx)
	if err != nil || filters.EpicID == nil || *filters.EpicID != epicID || filters.WorkItemID == nil || *filters.WorkItemID != workItemID || filters.AgentKey != "generalist" || filters.Kind != "activity" || filters.Action != "tool" || filters.Status != "completed" || filters.From == nil || filters.Until == nil {
		t.Fatalf("project activity filters did not parse strictly: %#v, %v", filters, err)
	}
	scope := projectActivityCursorScope(projectID, "operator", "organization", uuid.Must(uuid.NewV4()), filters)
	at := from.Add(time.Hour)
	encoded := encodeProjectActivityCursor(deliveryProjectActivityCursor{Version: 1, Scope: scope, OccurredAt: at, EventID: eventID.String(), EventKind: "activity"})
	decoded, err := decodeProjectActivityCursor(encoded, scope)
	if err != nil || decoded == nil || decoded.EventID != eventID.String() || decoded.EventKind != "activity" || !decoded.OccurredAt.Equal(at) {
		t.Fatalf("project activity cursor did not round-trip: %#v, %v", decoded, err)
	}
	if _, err := decodeProjectActivityCursor(encoded, projectActivityCursorScope(projectID, "operator", "organization", uuid.Must(uuid.NewV4()), deliveryProjectActivityFilters{})); err == nil {
		t.Fatal("cursor from another organization/filter scope must be rejected")
	}
	for _, raw := range []string{"0", "-1", "101", "many"} {
		if _, err := projectActivityPageSize(raw); err == nil {
			t.Fatalf("invalid page size %q was accepted", raw)
		}
	}
	for _, query := range []string{
		"?agent_key=Generalist", "?kind=unknown", "?action=private", "?status=private",
		"?from=not-a-date", "?from=2026-09-02T00:00:00Z&to=2026-09-01T00:00:00Z",
	} {
		badContext, _ := epicTestContext(http.MethodGet, "/api/automation/projects/"+projectID.String()+"/activity"+query, []string{"id"}, []string{projectID.String()}, "")
		if _, err := parseProjectActivityFilters(badContext); err == nil {
			t.Fatalf("invalid filter query %q was accepted", query)
		}
	}
}

func TestSafeProjectActivityProjectionRedactsUntrustedFields(t *testing.T) {
	eventID, clientID, projectID, epicID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	planID, stepID, taskID, instanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	row := deliveryProjectActivityRow{
		EventID: eventID, EventKind: "activity", ClientID: clientID, ProjectID: projectID, EpicID: &epicID,
		WorkItemID: workItemID, PlanID: planID, PlanVersion: 7, StepID: stepID, AutomationTaskID: taskID,
		RunID: "Bearer private-run", WorkerID: "not-a-worker-id", AgentKey: "Generalist", MachineID: "C:\\Users\\private",
		AgentInstanceID: &instanceID, EventType: models.DeliveryPlanStepActivityTool, Action: models.DeliveryPlanStepActivityTool,
		Phase: models.DeliveryPlanStepActivityCompleted, ToolName: "read_file", OccurredAt: time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC),
	}
	dto := safeProjectActivityItem(row)
	encoded, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	if dto.Kind != "activity" || dto.EventType != "tool" || dto.Summary != "Tool completed" || dto.ToolName != "read_file" || dto.AgentKey != "" || dto.RunID != "" || dto.WorkerID != "" || dto.MachineID != "" {
		t.Fatalf("unsafe or incomplete activity projection: %#v", dto)
	}
	for _, forbidden := range []string{"Bearer", "private-run", "C:\\\\Users", "reasoning", "details_json", "fencing_token", "authorization"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Fatalf("activity projection exposed %q: %s", forbidden, body)
		}
	}
	if dto.AgentInstanceID == nil || *dto.AgentInstanceID != instanceID || dto.ClientID != clientID || dto.EpicID == nil || *dto.EpicID != epicID {
		t.Fatalf("safe hierarchy/instance IDs were omitted: %#v", dto)
	}

	assignment := safeProjectActivityItem(deliveryProjectActivityRow{
		EventID: eventID, EventKind: "assignment", EventType: models.DeliveryPlanStepAssignmentEventTargetChanged,
		TargetAgentKey: "generalist", PreviousTargetAgent: "backend", TargetMachineID: instanceID.String(),
		PreviousMachineID: uuid.Must(uuid.NewV4()).String(), FromStatus: models.DeliveryPlanStepAssignmentQueued,
		ToStatus: models.DeliveryPlanStepAssignmentDispatched,
	})
	if assignment.Summary != "Assignment target changed" || assignment.TargetAgentKey != "generalist" || assignment.PreviousTargetAgentKey != "backend" || assignment.TargetMachineID == "" || assignment.PreviousTargetMachineID == "" {
		t.Fatalf("assignment projection omitted safe target history: %#v", assignment)
	}
}

func TestListProjectActivityPagesCombinedEventsWithSafeProjection(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configureEpicTestAuth(t)
	mock := *mockPtr
	projectID, clientID, epicID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	workItemID, planID, stepID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	firstID, secondID, instanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	firstAt := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(-time.Minute)
	columns := []string{
		"event_id", "event_kind", "client_id", "project_id", "epic_id", "work_item_id", "plan_id", "plan_version", "step_id", "automation_task_id",
		"run_id", "worker_id", "agent_key", "machine_id", "agent_instance_id", "event_type", "action", "phase", "tool_name", "from_status", "to_status",
		"target_agent_key", "previous_target_agent_key", "target_machine_id", "previous_target_machine_id", "occurred_at",
	}
	activityQuery := `(?s)WITH project_activity_events AS .*ORDER BY e\.occurred_at DESC, e\.event_id DESC, e\.event_kind DESC LIMIT \$\d+`
	mock.ExpectQuery(activityQuery).WithArgs(projectID, 2).WillReturnRows(sqlmock.NewRows(columns).
		AddRow(firstID, "activity", clientID, projectID, epicID, workItemID, planID, 3, stepID, taskID, uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String(), "generalist", uuid.Must(uuid.NewV4()).String(), instanceID, "tool", "tool", "completed", "read_file", "", "", "", "", "", "", firstAt).
		AddRow(secondID, "assignment", clientID, projectID, epicID, workItemID, planID, 3, stepID, taskID, "", "", "generalist", instanceID.String(), nil, models.DeliveryPlanStepAssignmentEventCreated, "", "", "", "", models.DeliveryPlanStepAssignmentPending, "generalist", "", instanceID.String(), "", secondAt))
	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/projects/"+projectID.String()+"/activity?limit=1", []string{"id"}, []string{projectID.String()}, "")
	ctx.Set("workspace_mode", "platform")
	if err := ListProjectActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("activity response status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data deliveryProjectActivityPage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.ProjectID != projectID || len(envelope.Data.Items) != 1 || envelope.Data.Items[0].Kind != "activity" || envelope.Data.Items[0].ToolName != "read_file" || envelope.Data.NextCursor == "" {
		t.Fatalf("activity page was incomplete: %#v", envelope.Data)
	}
	for _, forbidden := range []string{"reasoning", "prompt", "details_json", "command text"} {
		if strings.Contains(strings.ToLower(recorder.Body.String()), forbidden) {
			t.Fatalf("activity API returned forbidden data %q: %s", forbidden, recorder.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListProjectActivityHidesCrossOrganizationProject(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configureEpicTestAuth(t)
	mock := *mockPtr
	projectID, clientID, otherOrganizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expectDeliveryOrganizationClientIDs(mock, otherOrganizationID, otherOrganizationID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, clientID))
	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/projects/"+projectID.String()+"/activity", []string{"id"}, []string{projectID.String()}, "")
	ctx.Set("workspace_mode", "organization")
	ctx.Set("organization_id", otherOrganizationID)
	_ = ListProjectActivity(ctx) // project authorization writes a generic not-found response.
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-organization activity status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{projectID.String(), clientID.String(), otherOrganizationID.String()} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("cross-organization response disclosed %q: %s", forbidden, recorder.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildProjectActivityQueryBindsHierarchyAndCursorFilters(t *testing.T) {
	projectID, epicID, workItemID, instanceID, workerID, machineID, runID, cursorID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	until := from.Add(24 * time.Hour)
	cursor := &deliveryProjectActivityCursor{OccurredAt: until, EventID: cursorID.String(), EventKind: "assignment"}
	filters := deliveryProjectActivityFilters{
		EpicID: &epicID, WorkItemID: &workItemID, AgentKey: "generalist", AgentInstanceID: &instanceID,
		WorkerID: &workerID, MachineID: &machineID, RunID: &runID, Kind: "activity", Action: "tool",
		Status: "completed", From: &from, Until: &until,
	}
	query, args := buildProjectActivityQuery(projectID, filters, cursor, 51)
	for _, expected := range []string{
		"delivery_plan_step_events", "delivery_plan_step_assignment_events", "delivery_plan_step_activity_events",
		"delivery_epic_work_items", "e.agent_instance_id = ?", "e.previous_target_agent_key = ?", "e.occurred_at >= ?",
		"e.occurred_at < ?", "e.event_kind < ?", "ORDER BY e.occurred_at DESC, e.event_id DESC, e.event_kind DESC",
	} {
		if !strings.Contains(query, expected) {
			t.Fatalf("activity query omitted %q", expected)
		}
	}
	if len(args) != 25 || args[0] != projectID || args[1] != epicID || args[2] != workItemID || args[len(args)-1] != 51 {
		t.Fatalf("activity query arguments are not fully bound: %#v", args)
	}
	if strings.Contains(query, "prompt") || strings.Contains(query, "details_json") || strings.Contains(query, "output") {
		t.Fatal("project activity query selected private event payload fields")
	}
}
