package automation

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"github.com/DATA-DOG/go-sqlmock"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestAutomationHealthOrganizationWorkspaceOmitsGlobalTelemetry(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID := uuid.Must(uuid.NewV4())
	rootLevel := models.RootLevelPrimary
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: rootLevel, CognitoSub: "organization-health-test"}, nil
	}})
	t.Cleanup(restore)

	clientRows := sqlmock.NewRows([]string{"id"}).AddRow(organizationID)
	mock.ExpectQuery(`(?s)WITH RECURSIVE organization_clients.*SELECT id FROM organization_clients`).
		WithArgs(organizationID).WillReturnRows(clientRows)
	for _, test := range []struct {
		status string
		value  int64
		filter string
	}{
		{status: "queued", value: 2, filter: `automation_tasks\.status = \$2`},
		{status: "running", value: 3, filter: `automation_tasks\.status = \$2`},
		{status: "queued", value: 5, filter: `automation_tasks\.status IN \(\$2,\$3,\$4\)`},
		{status: "failed", value: 1, filter: `automation_tasks\.status = \$2 AND automation_tasks\.completed_at >= \$3`},
		{status: "running", value: 0, filter: `automation_tasks\.status = \$2 AND automation_tasks\.lease_expires_at IS NOT NULL AND automation_tasks\.lease_expires_at <= \$3`},
	} {
		mock.ExpectQuery(`(?s)SELECT count\(\*\) FROM "automation_tasks" JOIN delivery_work_items AS health_work_item.*JOIN delivery_projects AS health_project.*health_project\.client_id IN \(\$1\).*` + test.filter).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(test.value))
	}
	columns := sqlmock.NewRows([]string{"table_name", "column_name"}).
		AddRow(automationExecutionLedgerTable, "id").
		AddRow(automationExecutionLedgerTable, "automation_task_id").
		AddRow(automationExecutionLedgerTable, "total_cost_micros").
		AddRow(automationExecutionLedgerTable, "completed_at")
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT table_name, column_name`)+`\s+FROM information_schema\.columns`).
		WithArgs(automationExecutionLedgerTable, automationToolExecutionLedgerTable).WillReturnRows(columns)
	mock.ExpectQuery(`(?s)SELECT COALESCE\(SUM\(execution\.total_cost_micros\), 0\) AS spend_last_day FROM \(SELECT .*FROM automation_executions\) AS execution JOIN automation_tasks AS task ON task\.id = execution\.automation_task_id JOIN delivery_work_items AS health_work_item.*JOIN delivery_projects AS health_project.*execution\.completed_at >= \$1 AND health_project\.client_id IN \(\$2\)`).
		WillReturnRows(sqlmock.NewRows([]string{"spend_last_day"}).AddRow(int64(77)))

	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/health", nil), recorder)
	ctx.Set("cognito_sub", "organization-health-test")
	ctx.Set("workspace_mode", "organization")
	ctx.Set("organization_id", organizationID)
	ctx.Set("config", &models.Config{AutomationGlobalActiveLimit: 99, AutomationProjectActiveLimit: 88, AutomationQueueDepthLimit: 77})
	if err := Health(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	var result automationHealth
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		t.Fatalf("decode health data: %v", err)
	}
	if result.Queued != 2 || result.Running != 3 || result.ActiveTasks != 5 || result.FailedLastDay != 1 || result.SpendLastDay != 77 {
		t.Fatalf("organization-scoped task and spend aggregates = %#v", result)
	}
	if result.GlobalActiveLimit != 0 || result.ProjectActiveLimit != 0 || result.QueueDepthLimit != 0 || result.ActiveWorkers != 0 || result.OperationalTelemetryAvailable || result.QueueTelemetry || len(result.Workers) != 0 {
		t.Fatalf("organization health exposed platform limits or fleet telemetry: %#v", result)
	}
	if result.Scaling.Mode != "unavailable" || result.Scaling.Reason != "platform_workspace_required" {
		t.Fatalf("organization health should explain platform-only scaling telemetry: %#v", result.Scaling)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("health queries did not honor organization scope or unexpectedly queried global telemetry: %v", err)
	}
}

func TestAutomationHealthOrganizationScopeRestrictsClientAndVisibleProjects(t *testing.T) {
	organizationID := uuid.Must(uuid.NewV4())
	childClientID := uuid.Must(uuid.NewV4())
	visibleProjectID := uuid.Must(uuid.NewV4())
	otherProjectID := uuid.Must(uuid.NewV4())
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=localhost"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	workspace := automationHealthWorkspace{
		Mode:                   "organization",
		ClientIDs:              []uuid.UUID{organizationID, childClientID},
		AuthorizedProjectIDs:   []uuid.UUID{visibleProjectID, otherProjectID},
		AuthorizedProjectsOnly: true,
	}

	taskQuery, err := applyAutomationHealthTaskWorkspaceScope(db.Model(&models.AutomationTask{}), workspace)
	if err != nil {
		t.Fatalf("apply task workspace: %v", err)
	}
	taskQuery.Where("automation_tasks.status = ?", "queued").Find(&[]models.AutomationTask{})
	taskStatement := taskQuery.Statement
	if sql := taskStatement.SQL.String(); !strings.Contains(sql, "health_project.client_id IN ($1,$2)") || !strings.Contains(sql, "health_project.id IN ($3,$4)") {
		t.Fatalf("task health query is not intersected with client and visible-project scope: %s", sql)
	}
	if len(taskStatement.Vars) != 5 || taskStatement.Vars[0] != organizationID || taskStatement.Vars[1] != childClientID ||
		taskStatement.Vars[2] != visibleProjectID || taskStatement.Vars[3] != otherProjectID || taskStatement.Vars[4] != "queued" {
		t.Fatalf("task health scope variables = %#v", taskStatement.Vars)
	}

	spendQuery, err := applyAutomationHealthSpendWorkspaceScope(
		db.Table("(SELECT automation_task_id, delivery_work_item_id, total_cost_micros FROM automation_executions) AS execution").
			Joins("JOIN automation_tasks AS task ON task.id = execution.automation_task_id"),
		workspace,
	)
	if err != nil {
		t.Fatalf("apply spend workspace: %v", err)
	}
	spendQuery.Find(&[]map[string]any{})
	spendStatement := spendQuery.Statement
	if sql := spendStatement.SQL.String(); !strings.Contains(sql, "health_project.client_id IN ($1,$2)") || !strings.Contains(sql, "health_project.id IN ($3,$4)") {
		t.Fatalf("spend health query is not intersected with client and visible-project scope: %s", sql)
	}
}

func TestAutomationHealthOrganizationScopeWithNoVisibleProjectsFailsClosed(t *testing.T) {
	organizationID := uuid.Must(uuid.NewV4())
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=localhost"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	workspace := automationHealthWorkspace{Mode: "organization", ClientIDs: []uuid.UUID{organizationID}, AuthorizedProjectsOnly: true}

	taskQuery, err := applyAutomationHealthTaskWorkspaceScope(db.Model(&models.AutomationTask{}), workspace)
	if err != nil {
		t.Fatalf("apply task workspace: %v", err)
	}
	taskQuery.Find(&[]models.AutomationTask{})
	if sql := taskQuery.Statement.SQL.String(); !strings.Contains(sql, "1 = 0") {
		t.Fatalf("empty project access must produce a no-rows predicate: %s", sql)
	}

	spendQuery, err := applyAutomationHealthSpendWorkspaceScope(db.Table("automation_executions AS execution"), workspace)
	if err != nil {
		t.Fatalf("apply spend workspace: %v", err)
	}
	spendQuery.Find(&[]map[string]any{})
	if sql := spendQuery.Statement.SQL.String(); !strings.Contains(sql, "1 = 0") {
		t.Fatalf("empty project access must produce a no-rows spend predicate: %s", sql)
	}
}

func TestAutomationHealthRequiresExplicitAuthorizedWorkspace(t *testing.T) {
	e := echo.New()
	ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/health", nil), httptest.NewRecorder())
	admin := &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}

	if _, err := resolveAutomationHealthWorkspace(ctx, admin); !errors.Is(err, errAutomationHealthWorkspaceUnavailable) {
		t.Fatalf("missing workspace mode error = %v, want fail-closed", err)
	}
	ctx.Set("workspace_mode", "platform")
	if _, err := resolveAutomationHealthWorkspace(ctx, &models.User{IsRoot: false, RootLevel: models.RootLevelNone}); !errors.Is(err, errAutomationHealthWorkspaceUnavailable) {
		t.Fatalf("non-admin platform workspace error = %v, want fail-closed", err)
	}
	if _, err := applyAutomationHealthTaskWorkspaceScope(nil, automationHealthWorkspace{Mode: "platform"}); !errors.Is(err, errAutomationHealthWorkspaceUnavailable) {
		t.Fatalf("unverified platform telemetry scope error = %v, want fail-closed", err)
	}
}
