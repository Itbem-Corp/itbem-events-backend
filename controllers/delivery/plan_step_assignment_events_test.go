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

func TestPlanStepAssignmentEventCursorAndFiltersAreBounded(t *testing.T) {
	planID, actorID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	assignmentID, taskID, stepID, machineID, eventID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	filters := deliveryPlanStepAssignmentEventFilters{StepID: &stepID, AssignmentID: &assignmentID, TaskID: &taskID, Status: models.DeliveryPlanStepAssignmentRunning, AgentKey: "generalist", MachineID: &machineID}
	scope := planStepAssignmentEventCursorScope(planID, actorID.String(), "organization", organizationID, filters)
	occurredAt := time.Date(2026, 9, 24, 12, 30, 5, 123000000, time.UTC)
	encoded := encodePlanStepAssignmentEventCursor(deliveryPlanStepAssignmentEventCursor{Version: 1, Scope: scope, OccurredAt: occurredAt, ID: eventID.String()})
	decoded, err := decodePlanStepAssignmentEventCursor(encoded, scope)
	if err != nil || decoded == nil || decoded.ID != eventID.String() || !decoded.OccurredAt.Equal(occurredAt) {
		t.Fatalf("cursor did not round-trip: %#v, %v", decoded, err)
	}
	if _, err := decodePlanStepAssignmentEventCursor(encoded, planStepAssignmentEventCursorScope(uuid.Must(uuid.NewV4()), actorID.String(), "organization", organizationID, filters)); err == nil {
		t.Fatal("cursor from another plan must be rejected")
	}
	if _, err := decodePlanStepAssignmentEventCursor("not-a-cursor", scope); err == nil {
		t.Fatal("malformed cursor must be rejected")
	}
	if _, err := decodePlanStepAssignmentEventCursor(strings.Repeat("a", maxPlanStepAssignmentEventCursorBytes+1), scope); err == nil {
		t.Fatal("oversized cursor must be rejected")
	}
	if got, err := planStepAssignmentEventPageSize(""); err != nil || got != defaultPlanStepAssignmentEventPageSize {
		t.Fatalf("default page size = %d, %v", got, err)
	}
	for _, raw := range []string{"0", "-1", "101", "many"} {
		if _, err := planStepAssignmentEventPageSize(raw); err == nil {
			t.Fatalf("invalid page size %q was accepted", raw)
		}
	}
	for _, status := range []string{"running", "completed", "cancelled"} {
		if !validDeliveryPlanStepAssignmentStatus(status) {
			t.Fatalf("known status %q was rejected", status)
		}
	}
	if validDeliveryPlanStepAssignmentStatus("prompt=secret") {
		t.Fatal("arbitrary status was accepted")
	}
	filterContext, _ := epicTestContext(http.MethodGet, "/?step_id="+stepID.String()+"&assignment_id="+assignmentID.String()+"&task_id="+taskID.String()+"&machine_id="+machineID.String()+"&status=running&agent_key=generalist", nil, nil, "")
	parsedFilters, err := parsePlanStepAssignmentEventFilters(filterContext)
	if err != nil || parsedFilters.StepID == nil || *parsedFilters.StepID != stepID || parsedFilters.AssignmentID == nil || *parsedFilters.AssignmentID != assignmentID || parsedFilters.TaskID == nil || *parsedFilters.TaskID != taskID || parsedFilters.MachineID == nil || *parsedFilters.MachineID != machineID || parsedFilters.Status != "running" || parsedFilters.AgentKey != "generalist" {
		t.Fatalf("valid filters failed to parse: %#v, %v", parsedFilters, err)
	}
	invalidFilterContext, _ := epicTestContext(http.MethodGet, "/?status=raw-prompt-secret", nil, nil, "")
	if _, err := parsePlanStepAssignmentEventFilters(invalidFilterContext); err == nil {
		t.Fatal("unsupported status filter was accepted")
	}
}

func TestListPlanStepAssignmentEventsReturnsSafeCursorPage(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configureEpicTestAuth(t)
	mock := *mockPtr
	planID, workItemID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	firstID, secondID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	assignmentID, executionID, stepID, parentTaskID, childTaskID, machineID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT \* FROM "delivery_plans" WHERE id = \$1 ORDER BY "delivery_plans"\."id" LIMIT \$2`).
		WithArgs(planID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "version"}).AddRow(planID, workItemID, 3))
	mock.ExpectQuery(`SELECT "id","project_id" FROM "delivery_work_items" WHERE id = \$1 AND "delivery_work_items"\."deleted_at" IS NULL ORDER BY "delivery_work_items"\."id" LIMIT \$2`).
		WithArgs(workItemID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "project_id"}).AddRow(workItemID, projectID))
	mock.ExpectQuery(`SELECT "id","assignment_id","execution_id","step_id","parent_automation_task_id","child_automation_task_id","event_type","previous_status","status","previous_target_agent_key","target_agent_key","previous_target_machine_id","target_machine_id","occurred_at" FROM "delivery_plan_step_assignment_events" WHERE plan_id = \$1 ORDER BY occurred_at DESC, id DESC LIMIT \$2`).
		WithArgs(planID, 2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "assignment_id", "execution_id", "step_id", "parent_automation_task_id", "child_automation_task_id", "event_type", "previous_status", "status", "previous_target_agent_key", "target_agent_key", "previous_target_machine_id", "target_machine_id", "occurred_at"}).
			AddRow(firstID, assignmentID, executionID, stepID, parentTaskID, childTaskID, models.DeliveryPlanStepAssignmentEventStatusAndTargetChanged, "queued", "running", "generalist", "reviewer", machineID.String(), machineID.String(), createdAt).
			AddRow(secondID, uuid.Must(uuid.NewV4()), executionID, stepID, parentTaskID, uuid.Must(uuid.NewV4()), models.DeliveryPlanStepAssignmentEventCreated, "untrusted-status", "queued", "", "generalist", "not-a-uuid", machineID.String(), createdAt.Add(-time.Minute)))

	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/assignment-events?limit=1", []string{"id"}, []string{planID.String()}, "")
	ctx.Set("workspace_mode", "platform")
	if err := ListPlanStepAssignmentEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data deliveryPlanStepAssignmentEventPage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.PlanID != planID || envelope.Data.PlanVersion != 3 || len(envelope.Data.Items) != 1 || envelope.Data.Items[0].ID != firstID || envelope.Data.NextCursor == "" {
		t.Fatalf("unexpected assignment event page: %#v", envelope.Data)
	}
	item := envelope.Data.Items[0]
	if item.AssignmentID != assignmentID || item.ExecutionID != executionID || item.StepID != stepID || item.ParentTaskID != parentTaskID || item.TaskID != childTaskID || item.PreviousStatus != "queued" || item.Status != "running" || item.TargetAgentKey != "reviewer" || item.TargetMachineID != machineID.String() {
		t.Fatalf("unexpected safe event projection: %#v", item)
	}
	if strings.Contains(recorder.Body.String(), "untrusted-status") || strings.Contains(recorder.Body.String(), "not-a-uuid") || strings.Contains(recorder.Body.String(), "prompt") || strings.Contains(recorder.Body.String(), "credential") {
		t.Fatalf("assignment history exposed untrusted or private data: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPlanStepAssignmentEventsEnforcesOrganizationScope(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configureEpicTestAuth(t)
	mock := *mockPtr
	planID, workItemID, projectID, owningOrganizationID, selectedOrganizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	mock.ExpectQuery(`SELECT \* FROM "delivery_plans" WHERE id = \$1 ORDER BY "delivery_plans"\."id" LIMIT \$2`).
		WithArgs(planID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "version"}).AddRow(planID, workItemID, 2))
	mock.ExpectQuery(`SELECT "id","project_id" FROM "delivery_work_items" WHERE id = \$1 AND "delivery_work_items"\."deleted_at" IS NULL ORDER BY "delivery_work_items"\."id" LIMIT \$2`).
		WithArgs(workItemID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "project_id"}).AddRow(workItemID, projectID))
	expectDeliveryOrganizationClientIDs(mock, selectedOrganizationID, selectedOrganizationID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, owningOrganizationID))

	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/assignment-events", []string{"id"}, []string{planID.String()}, "")
	ctx.Set("workspace_mode", "organization")
	ctx.Set("organization_id", selectedOrganizationID)
	_ = ListPlanStepAssignmentEvents(ctx) // Scope denial writes a status-bearing response before returning.
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-organization status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{planID.String(), projectID.String(), owningOrganizationID.String(), selectedOrganizationID.String()} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("cross-organization response disclosed %q: %s", forbidden, recorder.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPlanStepAssignmentEventsRequiresProjectMembership(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configurePlanStepsNonRootAuth(t)
	mock := *mockPtr
	planID, workItemID, projectID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	mock.ExpectQuery(`SELECT \* FROM "delivery_plans" WHERE id = \$1 ORDER BY "delivery_plans"\."id" LIMIT \$2`).
		WithArgs(planID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "version"}).AddRow(planID, workItemID, 2))
	mock.ExpectQuery(`SELECT "id","project_id" FROM "delivery_work_items" WHERE id = \$1 AND "delivery_work_items"\."deleted_at" IS NULL ORDER BY "delivery_work_items"\."id" LIMIT \$2`).
		WithArgs(workItemID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "project_id"}).AddRow(workItemID, projectID))
	expectDeliveryOrganizationClientIDs(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, organizationID))
	mock.ExpectQuery(`SELECT \* FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectID, "epic-test-operator", 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))

	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/assignment-events", []string{"id"}, []string{planID.String()}, "")
	ctx.Set("workspace_mode", "organization")
	ctx.Set("organization_id", organizationID)
	_ = ListPlanStepAssignmentEvents(ctx)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-member status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
