package delivery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestPlanStepEventCursorScopeAndPageSize(t *testing.T) {
	planID, stepID, eventID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	occurredAt := time.Date(2026, 9, 23, 14, 30, 5, 123000000, time.UTC)
	scope := planStepEventCursorScope(planID, stepID)
	encoded := encodePlanStepEventCursor(deliveryPlanStepEventCursor{Version: 1, Scope: scope, OccurredAt: occurredAt, ID: eventID.String()})
	decoded, err := decodePlanStepEventCursor(encoded, scope)
	if err != nil || decoded == nil || decoded.ID != eventID.String() || !decoded.OccurredAt.Equal(occurredAt) {
		t.Fatalf("cursor did not round-trip: %#v, %v", decoded, err)
	}
	if _, err := decodePlanStepEventCursor(encoded, planStepEventCursorScope(planID, uuid.Must(uuid.NewV4()))); err == nil {
		t.Fatal("cursor from a different step must be rejected")
	}
	if _, err := decodePlanStepEventCursor("not-a-cursor", scope); err == nil {
		t.Fatal("malformed cursor must be rejected")
	}
	if _, err := decodePlanStepEventCursor(strings.Repeat("a", maxPlanStepEventCursorBytes+1), scope); err == nil {
		t.Fatal("oversized cursor must be rejected")
	}
	if got, err := planStepEventPageSize(""); err != nil || got != defaultPlanStepEventPageSize {
		t.Fatalf("default page size = %d, %v", got, err)
	}
	for _, raw := range []string{"0", "-1", "101", "many"} {
		if _, err := planStepEventPageSize(raw); err == nil {
			t.Fatalf("invalid page size %q was accepted", raw)
		}
	}
}

func TestListPlanStepEventsReturnsNotFoundForMissingPlan(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configureEpicTestAuth(t)
	planID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	(*mockPtr).ExpectQuery(`SELECT \* FROM "delivery_plans" WHERE id = \$1 ORDER BY "delivery_plans"\."id" LIMIT \$2`).
		WithArgs(planID, 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))

	ctx, recorder := planStepEventTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/events", []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
	_ = ListPlanStepEvents(ctx) // The shared project authorizer returns a status-bearing Echo error after writing its response.
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	if err := (*mockPtr).ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPlanStepEventsRequiresProjectMembership(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configurePlanStepsNonRootAuth(t)
	mock := *mockPtr
	planID, workItemID, projectID, clientID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expectPlanStepEventOrganizationScope(mock, planID, workItemID, projectID, clientID)
	expectDeliveryOrganizationClientIDs(mock, clientID, clientID)
	expectPlanStepsPlan(mock, planID, workItemID, 4, `{}`, false)
	expectPlanStepsWorkItem(mock, workItemID, projectID)
	expectDeliveryOrganizationClientIDs(mock, clientID, clientID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"."id" LIMIT \$2`).
		WithArgs(projectID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, clientID))
	mock.ExpectQuery(`SELECT \* FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectID, "epic-test-operator", 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))

	ctx, recorder := planStepEventTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/events", []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
	setPlanStepEventTestOrganizationContext(ctx, clientID)
	_ = ListPlanStepEvents(ctx) // Authorization denial writes its response and returns a status-bearing Echo error.
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unassigned actor status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPlanStepEventsRejectsOrganizationMismatchEvenForProjectMember(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configurePlanStepsNonRootAuth(t)
	mock := *mockPtr
	planID, workItemID, projectID, owningClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	otherOrganizationID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())

	assertPlanStepEventProjectMember(t, mock, projectID)
	expectPlanStepEventOrganizationScope(mock, planID, workItemID, projectID, owningClientID)
	// The scoped guard must reject before the shared permission loader or any
	// project-membership/event query is reached.
	expectDeliveryOrganizationClientIDs(mock, otherOrganizationID, otherOrganizationID)
	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/events", []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
	setPlanStepEventTestOrganizationContext(ctx, otherOrganizationID)
	_ = ListPlanStepEvents(ctx) // The generic fail-closed denial is surfaced through the response recorder.
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-organization status = %d, want generic 404: %s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{planID.String(), stepID.String(), projectID.String(), owningClientID.String(), otherOrganizationID.String()} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("cross-organization denial disclosed identifier %q: %s", forbidden, recorder.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPlanStepEventsFailsClosedWithoutOrganizationContextForProjectMember(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configurePlanStepsNonRootAuth(t)
	mock := *mockPtr
	projectID := uuid.Must(uuid.NewV4())
	assertPlanStepEventProjectMember(t, mock, projectID)
	planID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/events", []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
	ctx.Set("workspace_mode", "organization")
	// No organization_id: plan, project, membership and event data are not read.
	_ = ListPlanStepEvents(ctx) // The generic fail-closed denial is surfaced through the response recorder.
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing organization context status = %d, want generic 404: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), planID.String()) || strings.Contains(recorder.Body.String(), stepID.String()) {
		t.Fatalf("missing-context denial disclosed resource IDs: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlanStepEventsAndActivityAllowAuthorizedDescendantOrganizations(t *testing.T) {
	for _, test := range []struct {
		name        string
		pathSuffix  string
		call        func(echo.Context) error
		timelineSQL string
	}{
		{
			name:       "events",
			pathSuffix: "events",
			call:       ListPlanStepEvents,
			timelineSQL: `SELECT "id","event_type","from_status","to_status","automation_task_id","run_id","worker_id","agent_key","machine_id","agent_instance_id","summary","occurred_at" ` +
				`FROM "delivery_plan_step_events" WHERE plan_id = $1 AND step_id = $2 ORDER BY occurred_at DESC, id DESC LIMIT $3`,
		},
		{
			name:       "activity",
			pathSuffix: "activity",
			call:       ListPlanStepActivity,
			timelineSQL: `SELECT "id","step_id","automation_task_id","run_id","worker_id","agent_key","machine_id","agent_instance_id","sequence","action","phase","tool_name","duration_ms","summary","details_json","occurred_at" ` +
				`FROM "delivery_plan_step_activity_events" WHERE plan_id = $1 AND step_id = $2 ORDER BY occurred_at DESC, id DESC LIMIT $3`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mockPtr, restoreDB := configurePlanStepsTestDB(t)
			defer restoreDB()
			configureEpicTestAuth(t)
			mock := *mockPtr
			organizationID, descendantClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			planID, workItemID, projectID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())

			expectPlanStepEventOrganizationScope(mock, planID, workItemID, projectID, descendantClientID)
			expectDeliveryOrganizationClientIDs(mock, organizationID, organizationID, descendantClientID)
			expectPlanStepsPlan(mock, planID, workItemID, 4, `{}`, false)
			expectPlanStepsWorkItem(mock, workItemID, projectID)
			expectDeliveryOrganizationClientIDs(mock, organizationID, organizationID, descendantClientID)
			mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
				WithArgs(projectID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, descendantClientID))
			mock.ExpectQuery(`SELECT "id","plan_id" FROM "delivery_plan_steps" WHERE plan_id = \$1 AND id = \$2 ORDER BY "delivery_plan_steps"\."id" LIMIT \$3`).
				WithArgs(planID, stepID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id"}).AddRow(stepID, planID))
			mock.ExpectQuery(regexp.QuoteMeta(test.timelineSQL)).
				WithArgs(planID, stepID, defaultPlanStepEventPageSize+1).
				WillReturnRows(sqlmock.NewRows([]string{"id"}))

			ctx, recorder := planStepEventTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/"+test.pathSuffix, []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
			setPlanStepEventTestOrganizationContext(ctx, organizationID)
			if err := test.call(ctx); err != nil {
				t.Fatalf("descendant %s request returned error: %v", test.name, err)
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("authorized descendant %s status = %d, want 200: %s", test.name, recorder.Code, recorder.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestListPlanStepEventsRejectsInvalidCursorBeforeReadingTimeline(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configureEpicTestAuth(t)
	mock := *mockPtr
	planID, workItemID, projectID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expectPlanStepsPlan(mock, planID, workItemID, 4, `{}`, false)
	expectPlanStepsWorkItem(mock, workItemID, projectID)
	mock.ExpectQuery(`SELECT "id","plan_id" FROM "delivery_plan_steps" WHERE plan_id = \$1 AND id = \$2 ORDER BY "delivery_plan_steps"\."id" LIMIT \$3`).
		WithArgs(planID, stepID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id"}).AddRow(stepID, planID))

	ctx, recorder := planStepEventTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/events?cursor=not-a-cursor", []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
	if err := ListPlanStepEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPlanStepEventsHidesStepOutsidePlan(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configureEpicTestAuth(t)
	mock := *mockPtr
	planID, workItemID, projectID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expectPlanStepsPlan(mock, planID, workItemID, 4, `{}`, false)
	expectPlanStepsWorkItem(mock, workItemID, projectID)
	mock.ExpectQuery(`SELECT "id","plan_id" FROM "delivery_plan_steps" WHERE plan_id = \$1 AND id = \$2 ORDER BY "delivery_plan_steps"\."id" LIMIT \$3`).
		WithArgs(planID, stepID, 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))

	ctx, recorder := planStepEventTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/events", []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
	if err := ListPlanStepEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign step status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPlanStepEventsPagesInStableOrderAndRedactsUntrustedFields(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configureEpicTestAuth(t)
	mock := *mockPtr
	planID, workItemID, projectID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	firstID, secondID, thirdID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	agentInstanceID := uuid.Must(uuid.NewV4())
	taskID, runID, workerID, machineID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	firstAt := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(-time.Minute)
	thirdAt := firstAt.Add(-2 * time.Minute)
	expectPlanStepsPlan(mock, planID, workItemID, 9, `{}`, false)
	expectPlanStepsWorkItem(mock, workItemID, projectID)
	mock.ExpectQuery(`SELECT "id","plan_id" FROM "delivery_plan_steps" WHERE plan_id = \$1 AND id = \$2 ORDER BY "delivery_plan_steps"\."id" LIMIT \$3`).
		WithArgs(planID, stepID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id"}).AddRow(stepID, planID))
	eventQuery := regexp.QuoteMeta(`SELECT "id","event_type","from_status","to_status","automation_task_id","run_id","worker_id","agent_key","machine_id","agent_instance_id","summary","occurred_at" FROM "delivery_plan_step_events" WHERE plan_id = $1 AND step_id = $2 ORDER BY occurred_at DESC, id DESC LIMIT $3`)
	mock.ExpectQuery(eventQuery).WithArgs(planID, stepID, 3).WillReturnRows(planStepEventRows(
		planStepEventRow(firstID, models.DeliveryPlanStepEventClaimed, "ready", "running", taskID, runID, workerID, machineID, "Step claimed", firstAt, agentInstanceID),
		planStepEventRow(secondID, models.DeliveryPlanStepEventTransitioned, "running", "completed", taskID, runID, workerID, machineID, "Bearer secret-must-not-escape", secondAt),
		planStepEventRow(thirdID, models.DeliveryPlanStepEventTransitioned, "running", "failed", taskID, runID, workerID, machineID, "Step failed", thirdAt),
	))

	ctx, recorder := planStepEventTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/events?limit=2", []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
	if err := ListPlanStepEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data deliveryPlanStepEventPage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	page := envelope.Data
	if page.PlanID != planID || page.PlanVersion != 9 || page.StepID != stepID || len(page.Items) != 2 || page.Items[0].ID != firstID || page.Items[1].ID != secondID || page.Items[0].AgentInstanceID == nil || *page.Items[0].AgentInstanceID != agentInstanceID || page.NextCursor == "" {
		t.Fatalf("unexpected event page/order: %#v", page)
	}
	if page.Items[1].Summary != "Step event recorded" || page.Items[1].EventType != "step_event" {
		t.Fatalf("untrusted stored summary/type was not replaced with a fixed safe label: %#v", page.Items[1])
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"lease_fence", "secret-must-not-escape", "Bearer", "private_key", "prompt", "reasoning"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("event response exposed %q: %s", forbidden, body)
		}
	}
	for _, expected := range []string{"plan_id", "plan_version", "step_id", "items", "next_cursor", "event_type", "automation_task_id", "worker_id", "agent_instance_id", "occurred_at"} {
		if !strings.Contains(body, `"`+expected+`"`) {
			t.Fatalf("event response is missing %q: %s", expected, body)
		}
	}
	if !strings.Contains(body, `"worker_id":"`+workerID.String()+`"`) {
		t.Fatalf("event response omitted opaque worker identity: %s", body)
	}

	cursor, err := decodePlanStepEventCursor(page.NextCursor, planStepEventCursorScope(planID, stepID))
	if err != nil || cursor == nil || cursor.ID != secondID.String() {
		t.Fatalf("next cursor does not anchor the final returned row: %#v, %v", cursor, err)
	}
	secondPageQuery := regexp.QuoteMeta(`SELECT "id","event_type","from_status","to_status","automation_task_id","run_id","worker_id","agent_key","machine_id","agent_instance_id","summary","occurred_at" FROM "delivery_plan_step_events" WHERE (plan_id = $1 AND step_id = $2) AND ((occurred_at < $3 OR (occurred_at = $4 AND id < $5))) ORDER BY occurred_at DESC, id DESC LIMIT $6`)
	expectPlanStepsPlan(mock, planID, workItemID, 9, `{}`, false)
	expectPlanStepsWorkItem(mock, workItemID, projectID)
	mock.ExpectQuery(`SELECT "id","plan_id" FROM "delivery_plan_steps" WHERE plan_id = \$1 AND id = \$2 ORDER BY "delivery_plan_steps"\."id" LIMIT \$3`).
		WithArgs(planID, stepID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id"}).AddRow(stepID, planID))
	mock.ExpectQuery(secondPageQuery).WithArgs(planID, stepID, secondAt, secondAt, secondID, 3).WillReturnRows(planStepEventRows(
		planStepEventRow(thirdID, models.DeliveryPlanStepEventTransitioned, "running", "failed", taskID, runID, workerID, machineID, "Step failed", thirdAt),
	))
	nextCtx, nextRecorder := planStepEventTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/events?limit=2&cursor="+page.NextCursor, []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
	if err := ListPlanStepEvents(nextCtx); err != nil {
		t.Fatal(err)
	}
	if nextRecorder.Code != http.StatusOK {
		t.Fatalf("second page status = %d, want 200: %s", nextRecorder.Code, nextRecorder.Body.String())
	}
	var nextEnvelope struct {
		Data deliveryPlanStepEventPage `json:"data"`
	}
	if err := json.Unmarshal(nextRecorder.Body.Bytes(), &nextEnvelope); err != nil {
		t.Fatal(err)
	}
	if len(nextEnvelope.Data.Items) != 1 || nextEnvelope.Data.Items[0].ID != thirdID || nextEnvelope.Data.NextCursor != "" {
		t.Fatalf("unexpected second page: %#v", nextEnvelope.Data)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPlanStepEventsUsesIDAsTieBreakerForEqualTimestamps(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configureEpicTestAuth(t)
	mock := *mockPtr
	planID, workItemID, projectID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	taskID, runID, workerID, machineID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	firstID := uuid.Must(uuid.FromString("00000000-0000-4000-8000-000000000003"))
	secondID := uuid.Must(uuid.FromString("00000000-0000-4000-8000-000000000002"))
	thirdID := uuid.Must(uuid.FromString("00000000-0000-4000-8000-000000000001"))
	agentInstanceID := uuid.Must(uuid.NewV4())
	occurredAt := time.Date(2026, 9, 23, 12, 0, 0, 123000000, time.UTC)
	eventQuery := regexp.QuoteMeta(`SELECT "id","event_type","from_status","to_status","automation_task_id","run_id","worker_id","agent_key","machine_id","agent_instance_id","summary","occurred_at" FROM "delivery_plan_step_events" WHERE plan_id = $1 AND step_id = $2 ORDER BY occurred_at DESC, id DESC LIMIT $3`)
	secondPageQuery := regexp.QuoteMeta(`SELECT "id","event_type","from_status","to_status","automation_task_id","run_id","worker_id","agent_key","machine_id","agent_instance_id","summary","occurred_at" FROM "delivery_plan_step_events" WHERE (plan_id = $1 AND step_id = $2) AND ((occurred_at < $3 OR (occurred_at = $4 AND id < $5))) ORDER BY occurred_at DESC, id DESC LIMIT $6`)

	expectPlanStepsPlan(mock, planID, workItemID, 5, `{}`, false)
	expectPlanStepsWorkItem(mock, workItemID, projectID)
	mock.ExpectQuery(`SELECT "id","plan_id" FROM "delivery_plan_steps" WHERE plan_id = \$1 AND id = \$2 ORDER BY "delivery_plan_steps"\."id" LIMIT \$3`).
		WithArgs(planID, stepID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id"}).AddRow(stepID, planID))
	mock.ExpectQuery(eventQuery).WithArgs(planID, stepID, 3).WillReturnRows(planStepEventRows(
		planStepEventRow(firstID, models.DeliveryPlanStepEventClaimed, "ready", "running", taskID, runID, workerID, machineID, "Step claimed", occurredAt, agentInstanceID),
		planStepEventRow(secondID, models.DeliveryPlanStepEventLeaseRenewed, "running", "running", taskID, runID, workerID, machineID, "Step lease renewed", occurredAt),
		planStepEventRow(thirdID, models.DeliveryPlanStepEventTransitioned, "running", "completed", taskID, runID, workerID, machineID, "Step completed", occurredAt),
	))
	ctx, recorder := planStepEventTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/events?limit=2", []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
	if err := ListPlanStepEvents(ctx); err != nil {
		t.Fatal(err)
	}
	var firstPage struct {
		Data deliveryPlanStepEventPage `json:"data"`
	}
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &firstPage) != nil {
		t.Fatalf("first page status = %d, body: %s", recorder.Code, recorder.Body.String())
	}
	if len(firstPage.Data.Items) != 2 || firstPage.Data.Items[0].ID != firstID || firstPage.Data.Items[1].ID != secondID || firstPage.Data.Items[0].AgentInstanceID == nil || *firstPage.Data.Items[0].AgentInstanceID != agentInstanceID || firstPage.Data.NextCursor == "" {
		t.Fatalf("equal-time page should sort IDs DESC: %#v", firstPage.Data)
	}
	cursor, err := decodePlanStepEventCursor(firstPage.Data.NextCursor, planStepEventCursorScope(planID, stepID))
	if err != nil || cursor == nil || cursor.ID != secondID.String() || !cursor.OccurredAt.Equal(occurredAt) {
		t.Fatalf("equal-time cursor did not preserve the last tuple: %#v, %v", cursor, err)
	}

	expectPlanStepsPlan(mock, planID, workItemID, 5, `{}`, false)
	expectPlanStepsWorkItem(mock, workItemID, projectID)
	mock.ExpectQuery(`SELECT "id","plan_id" FROM "delivery_plan_steps" WHERE plan_id = \$1 AND id = \$2 ORDER BY "delivery_plan_steps"\."id" LIMIT \$3`).
		WithArgs(planID, stepID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id"}).AddRow(stepID, planID))
	mock.ExpectQuery(secondPageQuery).WithArgs(planID, stepID, occurredAt, occurredAt, secondID, 3).WillReturnRows(planStepEventRows(
		planStepEventRow(thirdID, models.DeliveryPlanStepEventTransitioned, "running", "completed", taskID, runID, workerID, machineID, "Step completed", occurredAt),
	))
	secondCtx, secondRecorder := planStepEventTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/events?limit=2&cursor="+firstPage.Data.NextCursor, []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
	if err := ListPlanStepEvents(secondCtx); err != nil {
		t.Fatal(err)
	}
	var secondPage struct {
		Data deliveryPlanStepEventPage `json:"data"`
	}
	if secondRecorder.Code != http.StatusOK || json.Unmarshal(secondRecorder.Body.Bytes(), &secondPage) != nil {
		t.Fatalf("second page status = %d, body: %s", secondRecorder.Code, secondRecorder.Body.String())
	}
	if len(secondPage.Data.Items) != 1 || secondPage.Data.Items[0].ID != thirdID || secondPage.Data.NextCursor != "" {
		t.Fatalf("equal-time keyset boundary duplicated or skipped an event: %#v", secondPage.Data)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func planStepEventRows(items ...models.DeliveryPlanStepEvent) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id", "event_type", "from_status", "to_status", "automation_task_id", "run_id", "worker_id", "agent_key", "machine_id", "agent_instance_id", "summary", "occurred_at"})
	for _, item := range items {
		var instanceID any
		if item.AgentInstanceID != nil {
			instanceID = item.AgentInstanceID.String()
		}
		rows.AddRow(item.ID, item.EventType, item.FromStatus, item.ToStatus, item.AutomationTaskID, item.RunID, item.WorkerID, item.AgentKey, item.MachineID, instanceID, item.Summary, item.OccurredAt)
	}
	return rows
}

func planStepEventRow(id uuid.UUID, eventType, fromStatus, toStatus string, taskID, runID, workerID, machineID uuid.UUID, summary string, occurredAt time.Time, instanceIDs ...uuid.UUID) models.DeliveryPlanStepEvent {
	event := models.DeliveryPlanStepEvent{
		ID: id, EventType: eventType, FromStatus: fromStatus, ToStatus: toStatus,
		AutomationTaskID: taskID, RunID: runID.String(), WorkerID: workerID.String(), AgentKey: "generalist", MachineID: machineID.String(),
		Summary: summary, OccurredAt: occurredAt,
	}
	if len(instanceIDs) > 0 && instanceIDs[0] != uuid.Nil {
		instanceID := instanceIDs[0]
		event.AgentInstanceID = &instanceID
	}
	return event
}

func planStepEventTestContext(method, target string, paramNames, paramValues []string, body string) (echo.Context, *httptest.ResponseRecorder) {
	ctx, recorder := epicTestContext(method, target, paramNames, paramValues, body)
	// Simulate the explicit platform mode that applicationaccess middleware
	// only sets after validating this mode against the authenticated session.
	ctx.Set("workspace_mode", "platform")
	return ctx, recorder
}

func setPlanStepEventTestOrganizationContext(ctx echo.Context, organizationID uuid.UUID) {
	ctx.Set("workspace_mode", "organization")
	ctx.Set("organization_id", organizationID)
}

func expectPlanStepEventOrganizationScope(mock sqlmock.Sqlmock, planID, workItemID, projectID, clientID uuid.UUID) {
	mock.ExpectQuery(`SELECT "id","work_item_id" FROM "delivery_plans" WHERE id = \$1 ORDER BY "delivery_plans"\."id" LIMIT \$2`).
		WithArgs(planID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id"}).AddRow(planID, workItemID))
	mock.ExpectQuery(`SELECT "id","project_id" FROM "delivery_work_items" WHERE id = \$1 AND "delivery_work_items"\."deleted_at" IS NULL ORDER BY "delivery_work_items"\."id" LIMIT \$2`).
		WithArgs(workItemID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id"}).AddRow(workItemID, projectID))
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, clientID))
}

func assertPlanStepEventProjectMember(t *testing.T, mock sqlmock.Sqlmock, projectID uuid.UUID) {
	t.Helper()
	membershipID := uuid.Must(uuid.NewV4())
	organizationID := uuid.Must(uuid.NewV4())
	expectDeliveryOrganizationClientIDs(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"."id" LIMIT \$2`).
		WithArgs(projectID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, organizationID))
	mock.ExpectQuery(`SELECT \* FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectID, "epic-test-operator", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "cognito_sub", "role", "permissions"}).AddRow(membershipID, projectID, "epic-test-operator", "viewer", `[]`))
	ctx, _ := epicTestContext(http.MethodGet, "/api/automation/projects/"+projectID.String(), []string{"id"}, []string{projectID.String()}, "")
	setPlanStepEventTestOrganizationContext(ctx, organizationID)
	if _, err := projectActor(ctx, projectID, deliveryView); err != nil {
		t.Fatalf("test setup failed: actor should be a member of project %s: %v", projectID, err)
	}
}
