package automation

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestGetAgentHistoryDeniesUnassignedProjectMember(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	projectID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "agent-history-unassigned"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, organizationID))
	mock.ExpectQuery(`SELECT \* FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectID, subject, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?project_id="+projectID.String(), nil), recorder)
	ctx.SetPath("/api/automation/agents/:agentKey/history")
	ctx.SetParamNames("agentKey")
	ctx.SetParamValues("generalist")
	ctx.Set("cognito_sub", "agent-history-test")
	setAgentHistoryOrganizationWorkspace(ctx, organizationID)
	if err := GetAgentHistory(ctx); err == nil {
		t.Fatal("denied project membership did not stop the handler")
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unassigned project member status = %d, want %d: %s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAgentHistoryDoesNotCarryPlatformRootIntoCustomerTenant(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	projectID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "agent-history-tenant-root"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, organizationID))
	mock.ExpectQuery(`SELECT \* FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectID, subject, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?project_id="+projectID.String(), nil), recorder)
	ctx.SetPath("/api/automation/agents/:agentKey/history")
	ctx.SetParamNames("agentKey")
	ctx.SetParamValues("generalist")
	ctx.Set("cognito_sub", "agent-history-test")
	ctx.Set("tenant_code", "customer-portal")
	setAgentHistoryOrganizationWorkspace(ctx, organizationID)
	if err := GetAgentHistory(ctx); err == nil {
		t.Fatal("tenant-scoped root without project membership did not stop")
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("root user on customer tenant status = %d, want %d: %s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAgentHistoryRequiresProjectScopeForNonRootCaller(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: "agent-history-member", IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)

	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history", nil), recorder)
	ctx.SetPath("/api/automation/agents/:agentKey/history")
	ctx.SetParamNames("agentKey")
	ctx.SetParamValues("generalist")
	ctx.Set("cognito_sub", "agent-history-member")
	setAgentHistoryOrganizationWorkspace(ctx, uuid.Must(uuid.NewV4()))
	if err := GetAgentHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing project scope status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAgentHistoryPlatformAdminCanQueryAcrossProjects(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	mock.ExpectQuery(`(?s)WITH activity AS \(.*FROM activity WHERE TRUE AND occurred_at <= \$5 AND recorded_at <= \$6 ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$7`).
		WithArgs("generalist", "generalist", "generalist", "generalist", sqlmock.AnyArg(), sqlmock.AnyArg(), agentHistoryDefaultLimit+1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history", nil), recorder)
	ctx.SetPath("/api/automation/agents/:agentKey/history")
	ctx.SetParamNames("agentKey")
	ctx.SetParamValues("generalist")
	ctx.Set("cognito_sub", "agent-history-root")
	setAgentHistoryPlatformWorkspace(ctx)
	if err := GetAgentHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("platform admin without project scope status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAgentHistoryAuthorizedProjectMemberIsConstrainedToThatProject(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	projectID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	memberID := uuid.Must(uuid.NewV4())
	subject := "agent-history-project-viewer"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, organizationID))
	mock.ExpectQuery(`SELECT \* FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectID, subject, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "cognito_sub", "role", "permissions", "created_by", "created_at", "updated_at"}).
			AddRow(memberID, projectID, subject, "viewer", `[]`, "project-admin", time.Now().UTC(), time.Now().UTC()))
	now := time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC)
	taskID, eventID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	columns := []string{
		"id", "kind", "occurred_at", "task_id", "run_id", "worker_id", "machine_id", "operation", "status", "client_id", "client_name", "project_id", "project_name",
		"work_item_id", "work_item_title", "epic_id", "epic_title", "step_id", "step_key", "provider", "model", "input_tokens", "output_tokens", "total_cost_micros", "pricing_basis", "summary", "activity_action",
	}
	mock.ExpectQuery(`(?s)WITH activity AS \(.*FROM activity WHERE TRUE AND occurred_at <= \$5 AND recorded_at <= \$6 AND client_id IN \(.*\) AND project_id = \$8 ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$9`).
		WithArgs("generalist", "generalist", "generalist", "generalist", sqlmock.AnyArg(), sqlmock.AnyArg(), organizationID, projectID, agentHistoryDefaultLimit+1).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(eventID, "task", now, taskID, "run-project", "", "", "delivery.implementation", "running", nil, "", projectID, "Project member project", nil, "", nil, "", nil, "", "", "", int64(0), int64(0), int64(0), "", "Automation task", ""))

	request := httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?project_id="+projectID.String(), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.SetPath("/api/automation/agents/:agentKey/history")
	ctx.SetParamNames("agentKey")
	ctx.SetParamValues("generalist")
	ctx.Set("cognito_sub", subject)
	setAgentHistoryOrganizationWorkspace(ctx, organizationID)
	if err := GetAgentHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("authorized project member status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data agentHistoryResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Items) != 1 || envelope.Data.Items[0].ProjectID == nil || *envelope.Data.Items[0].ProjectID != projectID {
		t.Fatalf("history did not retain the authorized project scope: %#v", envelope.Data.Items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAgentHistoryRejectsProjectOutsideSelectedOrganization(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	projectID, owningOrganizationID, selectedOrganizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "agent-history-wrong-org"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	expectAgentHistoryOrganizationClients(mock, selectedOrganizationID, selectedOrganizationID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, owningOrganizationID))

	request := httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?project_id="+projectID.String(), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.SetPath("/api/automation/agents/:agentKey/history")
	ctx.SetParamNames("agentKey")
	ctx.SetParamValues("generalist")
	ctx.Set("cognito_sub", subject)
	setAgentHistoryOrganizationWorkspace(ctx, selectedOrganizationID)
	if err := GetAgentHistory(ctx); err == nil {
		t.Fatal("project outside the selected organization did not stop the handler")
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-organization project status = %d, want generic 404: %s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{projectID.String(), owningOrganizationID.String(), selectedOrganizationID.String()} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("cross-organization history response disclosed %q: %s", forbidden, recorder.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAgentHistoryPlatformAdminInOrganizationWorkspaceIsTenantScoped(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID := uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	mock.ExpectQuery(`(?s)WITH activity AS \(.*FROM activity WHERE TRUE AND occurred_at <= \$5 AND recorded_at <= \$6 AND client_id IN \(.*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$8`).
		WithArgs("generalist", "generalist", "generalist", "generalist", sqlmock.AnyArg(), sqlmock.AnyArg(), organizationID, agentHistoryDefaultLimit+1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	request := httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history", nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.SetPath("/api/automation/agents/:agentKey/history")
	ctx.SetParamNames("agentKey")
	ctx.SetParamValues("generalist")
	ctx.Set("cognito_sub", "agent-history-root")
	setAgentHistoryOrganizationWorkspace(ctx, organizationID)
	if err := GetAgentHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("organization-scoped platform admin status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAgentHistoryCannotOverrideSelectedOrganizationWithClientFilter(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID, otherClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	request := httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?client_id="+otherClientID.String(), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.SetPath("/api/automation/agents/:agentKey/history")
	ctx.SetParamNames("agentKey")
	ctx.SetParamValues("generalist")
	ctx.Set("cognito_sub", "agent-history-root")
	setAgentHistoryOrganizationWorkspace(ctx, organizationID)
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID)
	expectAgentHistoryOrganizationClients(mock, otherClientID, otherClientID)
	if err := GetAgentHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("out-of-scope client filter should fail closed before history query, status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAgentHistoryRejectsProjectOutsideSelectedClientSubtree(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	clientID, childClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	projectID, otherClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	expectAgentHistoryOrganizationClients(mock, clientID, clientID, childClientID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, otherClientID))
	request := httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?client_id="+clientID.String()+"&project_id="+projectID.String(), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.SetPath("/api/automation/agents/:agentKey/history")
	ctx.SetParamNames("agentKey")
	ctx.SetParamValues("generalist")
	ctx.Set("cognito_sub", "agent-history-root")
	setAgentHistoryPlatformWorkspace(ctx)
	if err := GetAgentHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("project/client mismatch returned %d, want generic 404: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAgentHistoryClientViewIsIntersectedWithProjectMembership(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID, childClientID, visibleProjectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "agent-history-client-member"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID, childClientID)
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID, childClientID)
	mock.ExpectQuery(`(?s)SELECT delivery_projects\.id AS project_id, delivery_project_members\.role, delivery_project_members\.permissions FROM "delivery_projects" JOIN delivery_project_members ON .*WHERE delivery_projects\.deleted_at IS NULL AND delivery_projects\.client_id IN \(\$2,\$3\) ORDER BY delivery_projects\.id`).
		WithArgs(subject, organizationID, childClientID).
		WillReturnRows(sqlmock.NewRows([]string{"project_id", "role", "permissions"}).AddRow(visibleProjectID, "viewer", `[]`))
	mock.ExpectQuery(`(?s)WITH activity AS \(.*FROM activity WHERE TRUE.*client_id IN .*project_id IN .*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT`).
		WithArgs("generalist", "generalist", "generalist", "generalist", sqlmock.AnyArg(), sqlmock.AnyArg(), organizationID, organizationID, childClientID, visibleProjectID, agentHistoryDefaultLimit+1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	request := httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?client_id="+organizationID.String(), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.SetPath("/api/automation/agents/:agentKey/history")
	ctx.SetParamNames("agentKey")
	ctx.SetParamValues("generalist")
	ctx.Set("cognito_sub", subject)
	setAgentHistoryOrganizationWorkspace(ctx, organizationID)
	if err := GetAgentHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("authorized company history status = %d, want 200: %s; pending SQL expectations: %v", recorder.Code, recorder.Body.String(), mock.ExpectationsWereMet())
	}
	if !strings.Contains(recorder.Body.String(), `"items":[]`) {
		t.Fatalf("expected a valid empty, membership-scoped history page: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAgentHistoryFailsClosedWithoutWorkspaceOrganization(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	request := httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history", nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.SetPath("/api/automation/agents/:agentKey/history")
	ctx.SetParamNames("agentKey")
	ctx.SetParamValues("generalist")
	ctx.Set("cognito_sub", "agent-history-root")
	ctx.Set("workspace_mode", "organization")
	_ = GetAgentHistory(ctx)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing organization scope status = %d, want generic 404: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAgentHistoryRejectsInvalidFiltersBeforeQuery(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelOperational}, nil
	}})
	t.Cleanup(restore)

	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?from=2026-10-02T00:00:00Z&to=2026-10-01T00:00:00Z", nil), recorder)
	ctx.SetPath("/api/automation/agents/:agentKey/history")
	ctx.SetParamNames("agentKey")
	ctx.SetParamValues("generalist")
	ctx.Set("cognito_sub", "agent-history-test")
	setAgentHistoryPlatformWorkspace(ctx)
	if err := GetAgentHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid range status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestParseAgentHistoryFiltersRejectsMalformedWorkerMachineAndRunIDs(t *testing.T) {
	for _, query := range []string{
		"epic_id=not-a-uuid",
		"worker_id=not-a-uuid",
		"machine_id=not-a-uuid",
		"step_id=not-a-uuid",
		"step_key=invalid%2Fkey",
		"run_id=run%2Fwith%2Fslashes",
		"status=administrator_override",
		"provider=attacker-controlled",
	} {
		t.Run(query, func(t *testing.T) {
			ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?"+query, nil), httptest.NewRecorder())
			if _, err := parseAgentHistoryFilters(ctx); err == nil {
				t.Fatalf("malformed instance/run filter %q was accepted", query)
			}
		})
	}
}

func TestParseAgentHistoryFiltersAllowsOnlyKnownStatusesAndProviders(t *testing.T) {
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?status=COMPLETED&provider=OpenRouter", nil), httptest.NewRecorder())
	filters, err := parseAgentHistoryFilters(ctx)
	if err != nil {
		t.Fatalf("supported case-insensitive filters were rejected: %v", err)
	}
	if filters.Status != "completed" || filters.Provider != "openrouter" {
		t.Fatalf("filters were not canonicalized: %#v", filters)
	}
}

func TestQueryAgentHistoryIntersectsOrganizationAndFullWorkHierarchy(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	now := time.Date(2026, 9, 24, 19, 0, 0, 0, time.UTC)
	organizationID, clientID, projectID, epicID, workItemID, stepID :=
		uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	instanceID, eventID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	columns := []string{
		"id", "kind", "occurred_at", "task_id", "operation", "status", "client_id", "client_name", "project_id", "project_name",
		"work_item_id", "work_item_title", "epic_id", "epic_title", "step_id", "step_key", "provider", "model", "input_tokens", "output_tokens", "total_cost_micros", "pricing_basis", "currency", "summary", "activity_action",
	}
	rows := sqlmock.NewRows(columns).AddRow(eventID, "inference", now, taskID, "delivery.implementation", "completed", clientID, "ITBEM", projectID, "Agent Studio", workItemID, "Implement agent history", epicID, "Agent operations", stepID, "implement", "openrouter", "cheap-model", int64(12), int64(4), int64(3), "official_api_price", "USD", "Inference call", "")
	mock.ExpectQuery(`(?s)WITH activity AS \(.*delivery_epic_work_items.*AS pricing_basis,.*AS currency,.*FROM activity WHERE TRUE AND occurred_at <= \$5 AND recorded_at <= \$6 AND client_id IN \(.*\) AND client_id = \$8 AND project_id = \$9 AND epic_id = \$10 AND work_item_id = \$11 AND step_id = \$12 AND step_key = \$13 AND \(agent_instance_id = \$14 OR \(kind = 'task_event' AND previous_agent_instance_id = \$15\)\).*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$16`).
		WithArgs("generalist", "generalist", "generalist", "generalist", sqlmock.AnyArg(), sqlmock.AnyArg(), organizationID, clientID, projectID, epicID, workItemID, stepID, "implement", instanceID, instanceID, 2).
		WillReturnRows(rows)
	filters := agentHistoryFilters{
		Limit: 1, OrganizationID: &organizationID, ClientID: &clientID, ProjectID: &projectID, EpicID: &epicID,
		WorkItemID: &workItemID, StepID: &stepID, StepKey: "implement", AgentInstanceID: &instanceID,
	}
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history", nil), httptest.NewRecorder())
	response, err := queryAgentHistory(ctx, db, "generalist", filters)
	if err != nil {
		t.Fatalf("query hierarchy-scoped history: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Kind != "inference" || response.Items[0].EpicID == nil || *response.Items[0].EpicID != epicID || response.Items[0].StepID == nil || *response.Items[0].StepID != stepID || response.Items[0].CostPricingStatus != "verified_usd" || response.CostCoverage.Scope != "returned_page" || response.CostCoverage.VerifiedUSDExecutions != 1 || response.CostCoverage.UnpricedExecutions != 0 {
		t.Fatalf("expected event in intersected organization/client/project/epic/work-item/step/instance scope: %#v", response.Items)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"details_json", "prompt", "reasoning", "tool_output", "authorization", "secret"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("history included non-allow-listed evidence field %q: %s", forbidden, encoded)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentHistoryReturnsOnlyValidatedEvidenceMetadata(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	now := time.Date(2026, 9, 24, 20, 0, 0, 0, time.UTC)
	activityID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	criterionDigest, diffDigest := strings.Repeat("a", 64), strings.Repeat("b", 64)
	detailsJSON := `{"acceptance_checks":[{"criterion_sha256":"` + criterionDigest + `","passed":true}],"review_diff_sha256":"` + diffDigest + `"}`
	columns := []string{"id", "kind", "occurred_at", "task_id", "operation", "status", "activity_action", "activity_details_json"}
	rows := sqlmock.NewRows(columns).AddRow(activityID, "step_activity", now, taskID, "delivery.implementation", "completed", "evidence", detailsJSON)
	mock.ExpectQuery(`(?s)WITH activity AS \(.*FROM delivery_plan_step_activity_events e.*FROM activity WHERE TRUE AND occurred_at <= \$5 AND recorded_at <= \$6.*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$7`).
		WithArgs("generalist", "generalist", "generalist", "generalist", sqlmock.AnyArg(), sqlmock.AnyArg(), 2).
		WillReturnRows(rows)
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?limit=1", nil), httptest.NewRecorder())
	page, err := queryAgentHistory(ctx, db, "generalist", agentHistoryFilters{Limit: 1})
	if err != nil {
		t.Fatalf("query safe evidence history: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ActivityAction != "evidence" || page.Items[0].ActivityDetails == nil || len(page.Items[0].ActivityDetails.AcceptanceChecks) != 1 || page.Items[0].ActivityDetails.AcceptanceChecks[0].CriterionSHA256 != criterionDigest || !page.Items[0].ActivityDetails.AcceptanceChecks[0].Passed || page.Items[0].ActivityDetails.ReviewDiffSHA256 != diffDigest || page.Items[0].ActivityDetailsJSON != "" {
		t.Fatalf("expected validated digest-only evidence details: %#v", page.Items)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"acceptance text", "criterion text", "raw_output", "command_args", "reasoning", "details_json"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("evidence history exposed forbidden content %q: %s", forbidden, encoded)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAgentHistoryReturnsCursorPageWithoutPrivatePayloads(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	now := time.Date(2026, 9, 23, 18, 30, 0, 0, time.UTC)
	from := now.Add(-time.Hour)
	clientID, projectID, workItemID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	inferenceID, toolID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	workerID, machineID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, clientID))
	rows := sqlmock.NewRows([]string{
		"id", "kind", "occurred_at", "task_id", "run_id", "worker_id", "machine_id", "operation", "status", "client_id", "client_name", "project_id", "project_name",
		"work_item_id", "work_item_title", "epic_id", "epic_title", "step_id", "step_key", "provider", "model", "input_tokens", "output_tokens", "total_cost_micros", "pricing_basis", "currency", "summary", "activity_action",
	})
	epicID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	rows.AddRow(inferenceID, "inference", now, taskID, "run-2", workerID.String(), machineID.String(), "delivery.implementation", "completed", clientID, "ITBEM", projectID, "Agent Studio", workItemID, "Create history API", epicID, "Agent operations", stepID, "implement", "openrouter", "cheap-model", int64(90), int64(24), int64(17), "official_api_price", "USD", "Inference call", "")
	rows.AddRow(toolID, "tool_call", now.Add(-time.Minute), taskID, "run-2", workerID.String(), machineID.String(), "delivery.implementation", "completed", clientID, "ITBEM", projectID, "Agent Studio", workItemID, "Create history API", epicID, "Agent operations", stepID, "implement", "openrouter", "cheap-model", int64(40), int64(10), int64(5), "official_api_price", "USD", "AI tool call", "")
	mock.ExpectQuery(`(?s)WITH activity AS \(.*WITH requested_agent AS \(.*FROM delivery_plan_step_events.*FROM delivery_plan_step_activity_events.*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$\d+`).
		WithArgs("generalist", "generalist", "generalist", "generalist", from, sqlmock.AnyArg(), sqlmock.AnyArg(), projectID,
			workerID.String(), workerID.String(), machineID.String(), machineID.String(), "run-2", "run-2", 2).
		WillReturnRows(rows)

	request := httptest.NewRequest(http.MethodGet,
		"/api/automation/agents/generalist/history?limit=1&from="+from.Format(time.RFC3339Nano)+"&project_id="+projectID.String()+"&worker_id="+workerID.String()+"&machine_id="+machineID.String()+"&run_id=run-2", nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.SetPath("/api/automation/agents/:agentKey/history")
	ctx.SetParamNames("agentKey")
	ctx.SetParamValues("generalist")
	ctx.Set("cognito_sub", "agent-history-test")
	setAgentHistoryPlatformWorkspace(ctx)
	if err := GetAgentHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data agentHistoryResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.AgentKey != "generalist" || envelope.Data.Limit != 1 || !envelope.Data.HasMore || len(envelope.Data.Items) != 1 || envelope.Data.Items[0].Kind != "inference" {
		t.Fatalf("unexpected history page: %#v", envelope.Data)
	}
	cursorScope := agentHistoryCursorScope("generalist", agentHistoryFilters{
		Limit: 1, From: &from, ProjectID: &projectID, WorkerID: &workerID, MachineID: &machineID, RunID: "run-2",
		ActorSub: "agent-history-test", WorkspaceMode: "platform",
	})
	cursor, err := decodeAgentHistoryCursor(envelope.Data.NextCursor)
	if err != nil || cursor.Scope != cursorScope || cursor.ID != inferenceID || !cursor.OccurredAt.Equal(now) || cursor.SnapshotAt.IsZero() || cursor.SnapshotAt.Before(cursor.OccurredAt) || cursor.Kind != "inference" {
		t.Fatalf("next cursor = %#v, %v", cursor, err)
	}
	if envelope.Data.Items[0].ClientName != "ITBEM" || envelope.Data.Items[0].ProjectName != "Agent Studio" || envelope.Data.Items[0].EpicID == nil || *envelope.Data.Items[0].EpicID != epicID || envelope.Data.Items[0].EpicTitle != "Agent operations" || envelope.Data.Items[0].WorkItemTitle != "Create history API" || envelope.Data.Items[0].StepID == nil || *envelope.Data.Items[0].StepID != stepID || envelope.Data.Items[0].TotalCostMicros == nil || *envelope.Data.Items[0].TotalCostMicros != 17 || envelope.Data.Items[0].WorkerID != workerID.String() || envelope.Data.Items[0].MachineID != machineID.String() {
		t.Fatalf("hierarchical context/cost missing: %#v", envelope.Data.Items[0])
	}
	if regexp.MustCompile(`(?i)prompt|secret|credential|output_ref|request_ref|reasoning`).MatchString(recorder.Body.String()) {
		t.Fatalf("response exposed private field names: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentHistoryCursorRejectsTampering(t *testing.T) {
	for _, raw := range []string{"not-base64", ""} {
		if _, err := decodeAgentHistoryCursor(raw); err == nil {
			t.Fatalf("cursor %q unexpectedly accepted", raw)
		}
	}
	cursorTime := time.Now().UTC()
	valid := encodeAgentHistoryCursor(agentHistoryCursor{Version: 2, Scope: strings.Repeat("a", 64), OccurredAt: cursorTime, SnapshotAt: cursorTime.Add(time.Second), ID: uuid.Must(uuid.NewV4()), Kind: "prompt"})
	if _, err := decodeAgentHistoryCursor(valid); err == nil {
		t.Fatal("cursor with an unsupported source kind unexpectedly accepted")
	}
	validActivity := encodeAgentHistoryCursor(agentHistoryCursor{Version: 2, Scope: strings.Repeat("a", 64), OccurredAt: cursorTime, SnapshotAt: cursorTime.Add(time.Second), ID: uuid.Must(uuid.NewV4()), Kind: "step_activity"})
	if _, err := decodeAgentHistoryCursor(validActivity); err != nil {
		t.Fatalf("step activity cursor was rejected: %v", err)
	}
	validTaskEvent := encodeAgentHistoryCursor(agentHistoryCursor{Version: 2, Scope: strings.Repeat("a", 64), OccurredAt: cursorTime, SnapshotAt: cursorTime.Add(time.Second), ID: uuid.Must(uuid.NewV4()), Kind: "task_event"})
	if _, err := decodeAgentHistoryCursor(validTaskEvent); err != nil {
		t.Fatalf("task lifecycle event cursor was rejected: %v", err)
	}
	for name, payload := range map[string]string{
		"unknown field":            `{"version":2,"scope":"` + strings.Repeat("a", 64) + `","occurred_at":"2026-09-24T12:00:00Z","snapshot_at":"2026-09-24T12:01:00Z","id":"` + uuid.Must(uuid.NewV4()).String() + `","kind":"task","secret":"private"}`,
		"trailing object":          `{"version":2,"scope":"` + strings.Repeat("a", 64) + `","occurred_at":"2026-09-24T12:00:00Z","snapshot_at":"2026-09-24T12:01:00Z","id":"` + uuid.Must(uuid.NewV4()).String() + `","kind":"task"}{}`,
		"snapshot before position": `{"version":2,"scope":"` + strings.Repeat("a", 64) + `","occurred_at":"2026-09-24T12:00:00Z","snapshot_at":"2026-09-24T11:59:59Z","id":"` + uuid.Must(uuid.NewV4()).String() + `","kind":"task"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeAgentHistoryCursor(base64.RawURLEncoding.EncodeToString([]byte(payload))); err == nil {
				t.Fatal("cursor with unknown or trailing JSON was accepted")
			}
		})
	}
}

func TestAgentHistoryUsageSeparatesMissingAndExplicitZeroCost(t *testing.T) {
	inputTokens, zeroCost := int64(12), int64(0)
	unpriced := agentHistoryItem{
		Kind: "inference", InputTokens: &inputTokens, TotalCostMicros: &zeroCost, PricingBasis: "unpriced", Currency: "USD",
	}
	normalizeAgentHistoryUsage(&unpriced)
	if unpriced.InputTokens == nil || *unpriced.InputTokens != 12 || unpriced.TotalCostMicros != nil || unpriced.CostPricingStatus != "unknown" {
		t.Fatalf("unpriced call should retain usage while omitting unknown cost: %#v", unpriced)
	}

	pricedZero := agentHistoryItem{Kind: "tool_call", TotalCostMicros: &zeroCost, PricingBasis: "official_api_price", Currency: "USD"}
	normalizeAgentHistoryUsage(&pricedZero)
	if pricedZero.TotalCostMicros == nil || *pricedZero.TotalCostMicros != 0 || pricedZero.CostPricingStatus != "verified_usd" {
		t.Fatalf("priced zero-cost call must retain explicit zero: %#v", pricedZero)
	}

	pricedEUR := agentHistoryItem{Kind: "inference", TotalCostMicros: &zeroCost, PricingBasis: "official_api_price", Currency: "EUR"}
	normalizeAgentHistoryUsage(&pricedEUR)
	if pricedEUR.TotalCostMicros != nil || pricedEUR.CostPricingStatus != "unknown" {
		t.Fatalf("non-USD ledger row must not be presented as USD cost: %#v", pricedEUR)
	}

	lifecycle := agentHistoryItem{Kind: "step_event", InputTokens: &inputTokens, TotalCostMicros: &zeroCost}
	normalizeAgentHistoryUsage(&lifecycle)
	if lifecycle.InputTokens != nil || lifecycle.TotalCostMicros != nil {
		t.Fatalf("non-provider lifecycle event must omit usage and cost: %#v", lifecycle)
	}
}

func TestQueryAgentHistoryReportsUnknownCurrencyCoverageWithoutUSDAmounts(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	now := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC)
	firstID, secondID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	rows := sqlmock.NewRows([]string{"id", "kind", "occurred_at", "input_tokens", "output_tokens", "total_cost_micros", "pricing_basis", "currency"}).
		AddRow(firstID, "inference", now.Add(-time.Minute), int64(12), int64(4), int64(900), "official_api_price", "EUR").
		AddRow(secondID, "tool_call", now.Add(-2*time.Minute), int64(8), int64(2), int64(500), "official_api_price", "")
	mock.ExpectQuery(`(?s)WITH activity AS \(.*AS pricing_basis,.*AS currency,.*FROM activity WHERE TRUE AND occurred_at <= \$5 AND recorded_at <= \$6.*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$7`).
		WithArgs("generalist", "generalist", "generalist", "generalist", now, now, 11).
		WillReturnRows(rows)
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?limit=10", nil), httptest.NewRecorder())
	page, err := queryAgentHistory(ctx, db, "generalist", agentHistoryFilters{Limit: 10, SnapshotAt: &now})
	if err != nil {
		t.Fatalf("query currency coverage: %v", err)
	}
	if len(page.Items) != 2 || page.CostCoverage.Scope != "returned_page" || page.CostCoverage.VerifiedUSDExecutions != 0 || page.CostCoverage.UnpricedExecutions != 2 {
		t.Fatalf("EUR and empty-currency calls must remain visible as unknown page coverage: %#v", page)
	}
	for _, item := range page.Items {
		if item.CostPricingStatus != "unknown" || item.TotalCostMicros != nil || item.InputTokens == nil || item.OutputTokens == nil {
			t.Fatalf("unknown currency must hide USD amount but preserve usage and coverage status: %#v", item)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSafeAgentHistoryTaskEventTypeAllowList(t *testing.T) {
	for _, eventType := range []string{"created", "claimed", "status_transition", "lease_reclaimed", "assignment_changed", "attempt_updated"} {
		if got := safeAgentHistoryTaskEventType(" " + eventType + " "); got != eventType {
			t.Fatalf("allow-listed task event %q changed to %q", eventType, got)
		}
	}
	for _, eventType := range []string{"raw error: private", "prompt", "Authorization: Bearer secret", "legacy_snapshot"} {
		if got := safeAgentHistoryTaskEventType(eventType); got != "recorded" {
			t.Fatalf("untrusted task event type %q escaped as %q", eventType, got)
		}
	}
}

func TestGetAgentHistoryRejectsCursorFromDifferentAgentOrFilters(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	projectID := uuid.Must(uuid.NewV4())
	otherProjectID := uuid.Must(uuid.NewV4())
	queryFilters := agentHistoryFilters{Limit: 1, ProjectID: &projectID, ActorSub: "agent-history-test", WorkspaceMode: "platform"}
	for _, mismatch := range []struct {
		name          string
		cursorKey     string
		cursorFilters agentHistoryFilters
	}{
		{name: "agent", cursorKey: "different-agent", cursorFilters: queryFilters},
		{name: "project filter", cursorKey: "generalist", cursorFilters: agentHistoryFilters{Limit: 1, ProjectID: &otherProjectID, ActorSub: "agent-history-test", WorkspaceMode: "platform"}},
		{name: "actor", cursorKey: "generalist", cursorFilters: agentHistoryFilters{Limit: 1, ProjectID: &projectID, ActorSub: "different-actor", WorkspaceMode: "platform"}},
	} {
		t.Run(mismatch.name, func(t *testing.T) {
			cursor := encodeAgentHistoryCursor(agentHistoryCursor{
				Version: 2, Scope: agentHistoryCursorScope(mismatch.cursorKey, mismatch.cursorFilters),
				OccurredAt: time.Now().UTC(), SnapshotAt: time.Now().UTC().Add(time.Minute), ID: uuid.Must(uuid.NewV4()), Kind: "task",
			})
			request := httptest.NewRequest(http.MethodGet,
				"/api/automation/agents/generalist/history?limit=1&project_id="+projectID.String()+"&cursor="+cursor, nil)
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(request, recorder)
			ctx.SetPath("/api/automation/agents/:agentKey/history")
			ctx.SetParamNames("agentKey")
			ctx.SetParamValues("generalist")
			ctx.Set("cognito_sub", "agent-history-test")
			setAgentHistoryPlatformWorkspace(ctx)
			mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
				WithArgs(projectID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, uuid.Must(uuid.NewV4())))
			if err := GetAgentHistory(ctx); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("cross-agent/filter cursor status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentHistoryCursorScopeBindsEveryFilter(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	organizationID, clientID, projectID, epicID, workItemID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	base := agentHistoryFilters{
		Limit: 50, From: &from, To: &to, OrganizationID: &organizationID, ClientID: &clientID, ProjectID: &projectID, EpicID: &epicID, WorkItemID: &workItemID, StepID: &stepID, StepKey: "implement",
		ActorSub: "actor-a", WorkspaceMode: "organization", TenantCode: "itbem",
		RunID:     "run-2",
		Operation: "delivery.implementation", Status: "completed", Provider: "openrouter",
	}
	base.ClientScopeIDs = []uuid.UUID{clientID}
	base.AuthorizedProjectIDs = []uuid.UUID{projectID}
	base.AuthorizedProjectsScoped = true
	workerID, machineID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	base.WorkerID, base.MachineID = &workerID, &machineID
	baseScope := agentHistoryCursorScope("generalist", base)
	changes := []struct {
		name   string
		change func(*agentHistoryFilters)
	}{
		{name: "agent", change: func(filters *agentHistoryFilters) {}},
		{name: "limit", change: func(filters *agentHistoryFilters) { filters.Limit++ }},
		{name: "actor", change: func(filters *agentHistoryFilters) { filters.ActorSub = "actor-b" }},
		{name: "workspace", change: func(filters *agentHistoryFilters) { filters.WorkspaceMode = "platform" }},
		{name: "tenant", change: func(filters *agentHistoryFilters) { filters.TenantCode = "other" }},
		{name: "organization", change: func(filters *agentHistoryFilters) {
			changed := uuid.Must(uuid.NewV4())
			filters.OrganizationID = &changed
		}},
		{name: "from", change: func(filters *agentHistoryFilters) { changed := from.Add(time.Minute); filters.From = &changed }},
		{name: "to", change: func(filters *agentHistoryFilters) { changed := to.Add(time.Minute); filters.To = &changed }},
		{name: "client", change: func(filters *agentHistoryFilters) { changed := uuid.Must(uuid.NewV4()); filters.ClientID = &changed }},
		{name: "project", change: func(filters *agentHistoryFilters) { changed := uuid.Must(uuid.NewV4()); filters.ProjectID = &changed }},
		{name: "client descendants", change: func(filters *agentHistoryFilters) { filters.ClientScopeIDs = []uuid.UUID{uuid.Must(uuid.NewV4())} }},
		{name: "authorized projects", change: func(filters *agentHistoryFilters) {
			filters.AuthorizedProjectIDs = []uuid.UUID{uuid.Must(uuid.NewV4())}
		}},
		{name: "epic", change: func(filters *agentHistoryFilters) { changed := uuid.Must(uuid.NewV4()); filters.EpicID = &changed }},
		{name: "work item", change: func(filters *agentHistoryFilters) { changed := uuid.Must(uuid.NewV4()); filters.WorkItemID = &changed }},
		{name: "step id", change: func(filters *agentHistoryFilters) { changed := uuid.Must(uuid.NewV4()); filters.StepID = &changed }},
		{name: "step key", change: func(filters *agentHistoryFilters) { filters.StepKey = "qa" }},
		{name: "worker", change: func(filters *agentHistoryFilters) { changed := uuid.Must(uuid.NewV4()); filters.WorkerID = &changed }},
		{name: "machine", change: func(filters *agentHistoryFilters) { changed := uuid.Must(uuid.NewV4()); filters.MachineID = &changed }},
		{name: "run", change: func(filters *agentHistoryFilters) { filters.RunID = "different-run" }},
		{name: "operation", change: func(filters *agentHistoryFilters) { filters.Operation = "delivery.qa" }},
		{name: "status", change: func(filters *agentHistoryFilters) { filters.Status = "failed" }},
		{name: "provider", change: func(filters *agentHistoryFilters) { filters.Provider = "deepseek" }},
	}
	for _, test := range changes {
		t.Run(test.name, func(t *testing.T) {
			filters := base
			agentKey := "generalist"
			if test.name == "agent" {
				agentKey = "different-agent"
			} else {
				test.change(&filters)
			}
			if got := agentHistoryCursorScope(agentKey, filters); got == baseScope {
				t.Fatal("cursor scope did not change with the selected agent/filter")
			}
		})
	}
}

func TestAgentHistoryIncludesAllowListedStepActivityWithStableCursor(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	now := time.Date(2026, 9, 23, 18, 30, 0, 0, time.UTC)
	agentKey := "generalist"
	taskID, clientID, projectID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	activityID := uuid.Must(uuid.FromString("00000000-0000-4000-8000-000000000002"))
	olderEventID := uuid.Must(uuid.FromString("00000000-0000-4000-8000-000000000001"))
	columns := []string{
		"id", "kind", "occurred_at", "task_id", "run_id", "worker_id", "machine_id", "operation", "status", "client_id", "client_name", "project_id", "project_name",
		"work_item_id", "work_item_title", "epic_id", "epic_title", "step_id", "step_key", "provider", "model", "input_tokens", "output_tokens", "total_cost_micros", "summary", "activity_action", "activity_details_json",
	}
	unsafeRows := sqlmock.NewRows(columns).
		AddRow(activityID, "step_activity", now, taskID, "run-safe", "worker-opaque", "machine-opaque", "not.allowed", `C:\\Users\\sensitive`, clientID, "ITBEM", projectID, "Agent Studio", workItemID, "Authorization: Bearer private-value", nil, "", nil, "qa", "unregistered-provider", "api_key=private-value", int64(-4), int64(-2), int64(-1), "Plan step activity", "Authorization: Bearer private-value", `{"private_reasoning":"never expose this","authorization":"Bearer private-value"}`).
		AddRow(olderEventID, "step_event", now, taskID, "run-safe", "worker-opaque", "machine-opaque", "delivery.implementation", "completed", clientID, "ITBEM", projectID, "Agent Studio", workItemID, "Validate deployment", nil, "", nil, "qa", "", "", int64(0), int64(0), int64(0), "Plan step event", "", "{}")
	baseQuery := `(?s)WITH activity AS \(.*WITH requested_agent AS \(.*FROM delivery_plan_step_activity_events e.*WHERE e.agent_key = \(SELECT agent_key FROM requested_agent\).*FROM activity WHERE TRUE AND occurred_at <= \$5 AND recorded_at <= \$6.*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$\d+`
	mock.ExpectQuery(baseQuery).
		WithArgs(agentKey, agentKey, agentKey, agentKey, now, now, 2).
		WillReturnRows(unsafeRows)
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/generalist/history?limit=1", nil), httptest.NewRecorder())
	filters := agentHistoryFilters{Limit: 1, SnapshotAt: &now}
	first, err := queryAgentHistory(ctx, db, agentKey, filters)
	if err != nil {
		t.Fatalf("query activity history: %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].Kind != "step_activity" || first.Items[0].ActivityAction != "activity" || first.Items[0].Status != "recorded" {
		t.Fatalf("unsafe activity fields were not replaced with safe allow-list values: %#v", first.Items)
	}
	if first.Items[0].Summary != "Plan step activity" || first.Items[0].StepKey != "qa" || !first.HasMore {
		t.Fatalf("activity context or pagination metadata missing: %#v", first)
	}
	if first.Items[0].Operation != "unknown" || first.Items[0].RunID != "run-safe" || first.Items[0].WorkerID != "" || first.Items[0].MachineID != "" || first.Items[0].Provider != "" {
		t.Fatalf("untrusted identifiers/provider/operation were not projected safely: %#v", first.Items[0])
	}
	if first.Items[0].ActivityDetails != nil || first.Items[0].ActivityDetailsJSON != "" {
		t.Fatalf("unrecognized or private activity metadata escaped the strict allow-list: %#v", first.Items[0])
	}
	if !strings.Contains(first.Items[0].WorkItemTitle, "<redacted>") || strings.Contains(first.Items[0].WorkItemTitle, "private-value") || first.Items[0].Model != "api_key=<redacted>" || first.Items[0].InputTokens != nil || first.Items[0].OutputTokens != nil || first.Items[0].TotalCostMicros != nil {
		t.Fatalf("text redaction or nonnegative telemetry projection failed: %#v", first.Items[0])
	}
	if strings.Contains(first.Items[0].ActivityAction, "private-value") || strings.Contains(first.Items[0].Status, "Users") {
		t.Fatalf("untrusted activity content escaped projection: %#v", first.Items[0])
	}
	if got := safeAgentHistoryActivityAction(" tool "); got != "tool" {
		t.Fatalf("allow-listed action was not preserved: %q", got)
	}
	if got := safeAgentHistoryActivityPhase(" completed "); got != "completed" {
		t.Fatalf("allow-listed phase was not preserved: %q", got)
	}
	cursor, err := decodeAgentHistoryCursor(first.NextCursor)
	if err != nil || cursor.Scope != agentHistoryCursorScope(agentKey, filters) || cursor.ID != activityID || cursor.Kind != "step_activity" || !cursor.OccurredAt.Equal(now) {
		t.Fatalf("activity next cursor is not stable: %#v, %v", cursor, err)
	}

	nextRows := sqlmock.NewRows(columns).
		AddRow(olderEventID, "step_event", now, taskID, "run-safe", "worker-opaque", "machine-opaque", "delivery.implementation", "completed", clientID, "ITBEM", projectID, "Agent Studio", workItemID, "Validate deployment", nil, "", nil, "qa", "", "", int64(0), int64(0), int64(0), "Plan step event", "", "{}")
	mock.ExpectQuery(`(?s)WITH activity AS \(.*WITH requested_agent AS \(.*FROM delivery_plan_step_activity_events e.*FROM activity WHERE TRUE AND occurred_at <= \$5 AND recorded_at <= \$6.*AND \(occurred_at, id, kind\) < \(\$7, \$8, \$9\).*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$10`).
		WithArgs(agentKey, agentKey, agentKey, agentKey, cursor.SnapshotAt, cursor.SnapshotAt, cursor.OccurredAt, cursor.ID, cursor.Kind, 2).
		WillReturnRows(nextRows)
	filters.Cursor = &cursor
	second, err := queryAgentHistory(ctx, db, agentKey, filters)
	if err != nil {
		t.Fatalf("query activity history continuation: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != olderEventID || second.HasMore || second.NextCursor != "" {
		t.Fatalf("activity cursor continuation duplicated or skipped the expected event: %#v", second)
	}
	serialized, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-value", "never expose this", "activity_details_json", "C:\\\\Users", "prompt", "reasoning", "request_ref", "tool_name", "worker-opaque", "machine-opaque", "unregistered-provider"} {
		if strings.Contains(strings.ToLower(string(serialized)), strings.ToLower(forbidden)) {
			t.Fatalf("step activity response exposed %q: %s", forbidden, serialized)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func setAgentHistoryPlatformWorkspace(ctx echo.Context) {
	ctx.Set("workspace_mode", "platform")
}

func setAgentHistoryOrganizationWorkspace(ctx echo.Context, organizationID uuid.UUID) {
	ctx.Set("workspace_mode", "organization")
	ctx.Set("organization_id", organizationID)
}

func expectAgentHistoryOrganizationClients(mock sqlmock.Sqlmock, organizationID uuid.UUID, clientIDs ...uuid.UUID) {
	rows := sqlmock.NewRows([]string{"id"})
	for _, clientID := range clientIDs {
		rows.AddRow(clientID)
	}
	mock.ExpectQuery(`(?s)WITH RECURSIVE organization_clients\(id\) AS \(.*SELECT id FROM organization_clients`).
		WithArgs(organizationID).
		WillReturnRows(rows)
}
