package delivery

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
)

func configurePlanStepsTestDB(t *testing.T) (*sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	return &mock, func() { configuration.DB = previousDB }
}

func configurePlanStepsNonRootAuth(t *testing.T) {
	t.Helper()
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(sub string) (*models.User, error) {
		return &models.User{CognitoSub: sub}, nil
	}})
	t.Cleanup(restore)
}

func planStepsPlanRows(planID, workItemID uuid.UUID, version int, structuredJSON string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "work_item_id", "version", "status", "summary", "structured_json", "context_digest", "proposed_by", "approved_gate_id", "created_at"}).
		AddRow(planID, workItemID, version, "proposed", "Plan summary", structuredJSON, "digest", "planner", nil, time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC))
}

func expectPlanStepsPlan(mock sqlmock.Sqlmock, planID, workItemID uuid.UUID, version int, structuredJSON string, locked bool) {
	query := `SELECT \* FROM "delivery_plans" WHERE id = \$1 ORDER BY "delivery_plans"\."id" LIMIT \$2`
	if locked {
		query += ` FOR UPDATE`
	}
	mock.ExpectQuery(query).WithArgs(planID, 1).WillReturnRows(planStepsPlanRows(planID, workItemID, version, structuredJSON))
}

func expectPlanStepsWorkItem(mock sqlmock.Sqlmock, workItemID, projectID uuid.UUID) {
	mock.ExpectQuery(`SELECT "id","project_id" FROM "delivery_work_items" WHERE id = \$1.*ORDER BY "delivery_work_items"\."id" LIMIT \$2`).
		WithArgs(workItemID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id"}).AddRow(workItemID, projectID))
}

func planStepsStepRows(stepID, planID uuid.UUID, criteria string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "plan_id", "step_key", "idempotency_key", "role", "display_order", "title", "objective", "acceptance_criteria_json", "status",
		"agent_key", "worker_id", "machine_id", "automation_task_id", "automation_execution_id", "run_id", "created_by", "started_at", "completed_at", "created_at", "updated_at",
	}).AddRow(
		stepID, planID, "prepare", "step:prepare", models.DeliveryPlanStepRoleImplementation, 0, "Prepare", "Review the plan", criteria, models.DeliveryPlanStepPlanned,
		"", "", "", nil, nil, "", "operator", nil, nil,
		time.Date(2026, 9, 23, 12, 1, 0, 0, time.UTC), time.Date(2026, 9, 23, 12, 1, 0, 0, time.UTC),
	)
}

func planStepsExecutionGraphRows(stepID, integrationID, planID uuid.UUID) *sqlmock.Rows {
	createdAt := time.Date(2026, 9, 23, 12, 1, 0, 0, time.UTC)
	columns := []string{
		"id", "plan_id", "step_key", "idempotency_key", "role", "display_order", "title", "objective", "acceptance_criteria_json", "status",
		"agent_key", "worker_id", "machine_id", "automation_task_id", "automation_execution_id", "run_id", "created_by", "started_at", "completed_at", "created_at", "updated_at",
	}
	return sqlmock.NewRows(columns).
		AddRow(stepID, planID, "prepare", "step:prepare", models.DeliveryPlanStepRoleImplementation, 0, "Prepare", "Create the reviewed change", `["Reviewed"]`, models.DeliveryPlanStepPlanned, "", "", "", nil, nil, "", "operator", nil, nil, createdAt, createdAt).
		AddRow(integrationID, planID, "integrate", "step:integrate", models.DeliveryPlanStepRoleIntegration, 1, "Integrate", "Merge and verify the reviewed change", `["Reviewed"]`, models.DeliveryPlanStepPlanned, "", "", "", nil, nil, "", "operator", nil, nil, createdAt, createdAt)
}

func planStepsExecutionDependencyRows(dependencyID, planID, integrationID, stepID uuid.UUID) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "plan_id", "step_id", "depends_on_step_id", "created_at"}).
		AddRow(dependencyID, planID, integrationID, stepID, time.Date(2026, 9, 23, 12, 1, 0, 0, time.UTC))
}

func TestListPlanStepsReturnsNotFoundWhenPlanDoesNotExist(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	mock := *mockPtr
	configureEpicTestAuth(t)
	planID := uuid.Must(uuid.NewV4())
	mock.ExpectQuery(`SELECT \* FROM "delivery_plans" WHERE id = \$1 ORDER BY "delivery_plans"\."id" LIMIT \$2`).
		WithArgs(planID, 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))

	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/delivery/plans/"+planID.String()+"/steps", []string{"id"}, []string{planID.String()}, "")
	ctx.Set("workspace_mode", "platform")
	var handlerPanic any
	func() {
		defer func() { handlerPanic = recover() }()
		if err := ListPlanSteps(ctx); err != nil {
			t.Errorf("ListPlanSteps returned an error: %v", err)
		}
	}()
	if handlerPanic != nil {
		t.Errorf("missing plan must return 404 without panicking; got panic: %v", handlerPanic)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPlanStepsReturnsEmptyForLegacyPlanAndSafeRowsForMaterializedPlan(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		materialized  bool
		wantStepTitle string
	}{
		{name: "legacy plan", materialized: false},
		{name: "materialized plan", materialized: true, wantStepTitle: "Prepare"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			mockPtr, restoreDB := configurePlanStepsTestDB(t)
			defer restoreDB()
			mock := *mockPtr
			configureEpicTestAuth(t)

			planID, workItemID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			structuredJSON := `{"implementation_steps":["Prepare"],"acceptance_criteria":["Reviewed"]}`
			expectPlanStepsPlan(mock, planID, workItemID, 7, structuredJSON, false)
			expectPlanStepsWorkItem(mock, workItemID, projectID)
			stepID := uuid.Must(uuid.NewV4())
			steps := sqlmock.NewRows([]string{"id"})
			if scenario.materialized {
				mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 ORDER BY display_order ASC`).
					WithArgs(planID).WillReturnRows(planStepsStepRows(stepID, planID, `["Reviewed"]`))
				mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_dependencies" WHERE plan_id = \$1 ORDER BY step_id ASC, depends_on_step_id ASC`).
					WithArgs(planID).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			} else {
				mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 ORDER BY display_order ASC`).
					WithArgs(planID).WillReturnRows(steps)
			}

			ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/delivery/plans/"+planID.String()+"/steps", []string{"id"}, []string{planID.String()}, "")
			ctx.Set("workspace_mode", "platform")
			if err := ListPlanSteps(ctx); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
			}
			var envelope struct {
				Data deliveryPlanStepsResponse `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Data.PlanID != planID || envelope.Data.PlanVersion != 7 {
				t.Fatalf("plan identity/version missing from response: %#v", envelope.Data)
			}
			if !scenario.materialized {
				if envelope.Data.Total != 0 || len(envelope.Data.Items) != 0 {
					t.Fatalf("legacy plan should return an empty step list: %#v", envelope.Data)
				}
			} else if envelope.Data.Total != 1 || len(envelope.Data.Items) != 1 || envelope.Data.Items[0].Title != scenario.wantStepTitle {
				t.Fatalf("unexpected materialized steps: %#v", envelope.Data)
			}
			for _, forbidden := range []string{"worker_id", "machine_id", "run_id", "operator", "s3://", "prompt", "reasoning"} {
				if strings.Contains(recorder.Body.String(), forbidden) {
					t.Fatalf("step listing exposed %q: %s", forbidden, recorder.Body.String())
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMaterializePlanStepsRequiresManagePermissionInOwningProject(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	mock := *mockPtr
	configurePlanStepsNonRootAuth(t)

	planID, workItemID, owningProjectID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	otherProjectID := uuid.Must(uuid.NewV4())
	structuredJSON := `{"execution_steps":[]}`
	expectPlanStepsPlan(mock, planID, workItemID, 1, structuredJSON, false)
	expectPlanStepsWorkItem(mock, workItemID, owningProjectID)
	expectDeliveryOrganizationClientIDs(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(owningProjectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(owningProjectID, organizationID))
	mock.ExpectQuery(`SELECT \* FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(owningProjectID, "epic-test-operator", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	ctx, recorder := epicTestContext(http.MethodPost, "/api/automation/delivery/plans/"+planID.String()+"/steps/materialize", []string{"id"}, []string{planID.String()}, "{}")
	ctx.Set("workspace_mode", "organization")
	ctx.Set("organization_id", organizationID)
	_ = MaterializePlanSteps(ctx) // A denied project authorization now returns a status-bearing Echo error.
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unassigned actor status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), otherProjectID.String()) {
		t.Fatalf("response disclosed unrelated project: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializePlanStepsRejectsInvalidPlanID(t *testing.T) {
	ctx, recorder := epicTestContext(http.MethodPost, "/api/automation/delivery/plans/not-a-uuid/steps/materialize", []string{"id"}, []string{"not-a-uuid"}, "{}")
	if err := MaterializePlanSteps(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid plan id status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
}

func TestMaterializePlanStepsRejectsInvalidPersistedPlanWithoutWriting(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	mock := *mockPtr
	configureEpicTestAuth(t)

	planID, workItemID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	structuredJSON := `{"execution_steps":"not-a-list"}`
	expectPlanStepsPlan(mock, planID, workItemID, 2, structuredJSON, false)
	expectPlanStepsWorkItem(mock, workItemID, projectID)
	mock.ExpectBegin()
	expectPlanStepsPlan(mock, planID, workItemID, 2, structuredJSON, true)
	mock.ExpectRollback()

	ctx, recorder := epicTestContext(http.MethodPost, "/api/automation/delivery/plans/"+planID.String()+"/steps/materialize", []string{"id"}, []string{planID.String()}, "{}")
	ctx.Set("workspace_mode", "platform")
	if err := MaterializePlanSteps(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("invalid persisted execution contract status = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "not-a-list") {
		t.Fatalf("response echoed attacker-controlled plan content: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializePlanStepsReturnsExistingGraphOnIdempotentRetry(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	mock := *mockPtr
	configureEpicTestAuth(t)

	planID, workItemID, projectID, stepID, integrationID, dependencyID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	structuredJSON := `{"acceptance_criteria":["Reviewed"],"execution_steps":[{"step_key":"prepare","role":"implementation","order":0,"title":"Prepare","objective":"Create the reviewed change","acceptance_criteria":["Reviewed"]},{"step_key":"integrate","role":"integration","order":1,"title":"Integrate","objective":"Merge and verify the reviewed change","acceptance_criteria":["Reviewed"],"depends_on":["prepare"]}]}`
	expectPlanStepsPlan(mock, planID, workItemID, 3, structuredJSON, false)
	expectPlanStepsWorkItem(mock, workItemID, projectID)
	mock.ExpectBegin()
	expectPlanStepsPlan(mock, planID, workItemID, 3, structuredJSON, true)
	expectPlanStepsPlan(mock, planID, workItemID, 3, structuredJSON, true)
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 ORDER BY display_order ASC`).
		WithArgs(planID).WillReturnRows(planStepsExecutionGraphRows(stepID, integrationID, planID))
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_dependencies" WHERE plan_id = \$1`).
		WithArgs(planID).WillReturnRows(planStepsExecutionDependencyRows(dependencyID, planID, integrationID, stepID))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 ORDER BY display_order ASC`).
		WithArgs(planID).WillReturnRows(planStepsExecutionGraphRows(stepID, integrationID, planID))
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_dependencies" WHERE plan_id = \$1 ORDER BY step_id ASC, depends_on_step_id ASC`).
		WithArgs(planID).WillReturnRows(planStepsExecutionDependencyRows(dependencyID, planID, integrationID, stepID))

	ctx, recorder := epicTestContext(http.MethodPost, "/api/automation/delivery/plans/"+planID.String()+"/steps/materialize", []string{"id"}, []string{planID.String()}, "{}")
	ctx.Set("workspace_mode", "platform")
	if err := MaterializePlanSteps(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("idempotent replay status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data struct {
			deliveryPlanStepsResponse
			Created bool `json:"created"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Created || envelope.Data.Total != 2 || len(envelope.Data.Items) != 2 || envelope.Data.Items[0].ID != stepID.String() || envelope.Data.Items[0].Role != models.DeliveryPlanStepRoleImplementation || envelope.Data.Items[1].ID != integrationID.String() || envelope.Data.Items[1].Role != models.DeliveryPlanStepRoleIntegration || len(envelope.Data.Items[1].DependsOn) != 1 || envelope.Data.Items[1].DependsOn[0] != "prepare" {
		t.Fatalf("idempotent replay should return the persisted graph with created=false: %#v", envelope.Data)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializePlanStepsFailsClosedForLegacyPlanWithoutExplicitIntegration(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	mock := *mockPtr
	configureEpicTestAuth(t)

	planID, workItemID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	legacyJSON := `{"implementation_steps":["Prepare"],"acceptance_criteria":["Reviewed"]}`
	expectPlanStepsPlan(mock, planID, workItemID, 7, legacyJSON, false)
	expectPlanStepsWorkItem(mock, workItemID, projectID)
	mock.ExpectBegin()
	expectPlanStepsPlan(mock, planID, workItemID, 7, legacyJSON, true)
	mock.ExpectRollback()

	ctx, recorder := epicTestContext(http.MethodPost, "/api/automation/delivery/plans/"+planID.String()+"/steps/materialize", []string{"id"}, []string{planID.String()}, "{}")
	ctx.Set("workspace_mode", "platform")
	if err := MaterializePlanSteps(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("legacy execution plan must require a new approved version, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "explicit, role-bound integration DAG") {
		t.Fatalf("legacy plan conflict should explain the missing immutable execution graph: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
