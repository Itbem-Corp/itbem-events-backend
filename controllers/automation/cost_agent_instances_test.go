package automation

import (
	"encoding/json"
	"fmt"
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
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestParseAutomationCostAgentInstancePaginationIsBoundedAndCursorOnly(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{name: "defaults", query: "", wantErr: false},
		{name: "custom limit", query: "?agent_instance_limit=7", wantErr: false},
		{name: "zero limit", query: "?agent_instance_limit=0", wantErr: true},
		{name: "high limit", query: "?agent_instance_limit=101", wantErr: true},
		{name: "signed limit", query: "?agent_instance_limit=%2B2", wantErr: true},
		{name: "duplicate limit", query: "?agent_instance_limit=2&agent_instance_limit=3", wantErr: true},
		{name: "duplicate cursor", query: "?agent_instance_cursor=YQ&agent_instance_cursor=Yg", wantErr: true},
		{name: "wrong endpoint cursor", query: "?cursor=opaque", wantErr: true},
		{name: "offset pagination", query: "?page=2&page_size=20", wantErr: true},
		{name: "oversized cursor", query: "?agent_instance_cursor=" + strings.Repeat("a", maxAutomationCostAgentInstanceCursor+1), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := automationCostFilterContext("/api/automation/costs/agent-instances" + test.query)
			limit, _, err := parseAutomationCostAgentInstancePagination(ctx)
			if (err != nil) != test.wantErr {
				t.Fatalf("parse pagination error = %v, want error %t", err, test.wantErr)
			}
			if err == nil && test.name == "defaults" && limit != defaultAutomationCostAgentInstanceLimit {
				t.Fatalf("default limit = %d, want %d", limit, defaultAutomationCostAgentInstanceLimit)
			}
			if err == nil && test.name == "custom limit" && limit != 7 {
				t.Fatalf("custom limit = %d, want 7", limit)
			}
		})
	}
}

func TestAutomationCostAgentInstanceCursorRoundTripsAndBindsQueryScope(t *testing.T) {
	organizationID, otherOrganizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	projectID, otherProjectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	instanceID := uuid.Must(uuid.NewV4())
	snapshotAt := time.Date(2026, 9, 24, 12, 30, 0, 0, time.UTC)
	filters := automationCostQuery{Days: 30, PageSize: 40, ProjectID: &projectID, AgentInstanceID: &instanceID, AgentKey: "generalist", Provider: "openrouter", Model: "cheap-model"}
	scope := automationCostAgentInstanceCursorScope(filters, 25, "organization", organizationID, true, "actor-a")
	original := automationCostAgentInstanceCursor{SnapshotAt: snapshotAt, TotalCostMicros: 500, AgentKey: "generalist", InstanceID: instanceID.String(), ScopeHash: scope}
	token, err := encodeAutomationCostAgentInstanceCursor(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeAutomationCostAgentInstanceCursor(token)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != 2 || !decoded.SnapshotAt.Equal(snapshotAt) || decoded.TotalCostMicros != original.TotalCostMicros || decoded.AgentKey != original.AgentKey || decoded.InstanceID != instanceID.String() || decoded.ScopeHash != scope {
		t.Fatalf("decoded cursor = %#v", decoded)
	}
	unattributedToken, err := encodeAutomationCostAgentInstanceCursor(automationCostAgentInstanceCursor{
		SnapshotAt: snapshotAt, TotalCostMicros: 100, AgentKey: "generalist", ScopeHash: scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	unattributed, err := decodeAutomationCostAgentInstanceCursor(unattributedToken)
	if err != nil || unattributed.InstanceID != "" {
		t.Fatalf("unattributed instance cursor = %#v, error %v", unattributed, err)
	}
	if len(token) > maxAutomationCostAgentInstanceCursor {
		t.Fatalf("encoded cursor exceeds bounded input limit: %d", len(token))
	}
	if _, err := decodeAutomationCostAgentInstanceCursor(strings.Repeat("a", maxAutomationCostAgentInstanceCursor+1)); err == nil {
		t.Fatal("oversized cursor was accepted")
	}
	for _, invalid := range []automationCostAgentInstanceCursor{
		{SnapshotAt: snapshotAt, TotalCostMicros: -1, AgentKey: "generalist", ScopeHash: scope},
		{SnapshotAt: snapshotAt, TotalCostMicros: 1, AgentKey: strings.Repeat("a", 129), ScopeHash: scope},
		{SnapshotAt: snapshotAt, TotalCostMicros: 1, AgentKey: "generalist", InstanceID: "not-a-uuid", ScopeHash: scope},
		{SnapshotAt: snapshotAt, TotalCostMicros: 1, AgentKey: "generalist", ScopeHash: "not-hex"},
		{TotalCostMicros: 1, AgentKey: "generalist", ScopeHash: scope},
	} {
		if _, err := encodeAutomationCostAgentInstanceCursor(invalid); err == nil {
			t.Fatalf("invalid cursor was encoded: %#v", invalid)
		}
	}

	variants := []struct {
		name    string
		filters automationCostQuery
		limit   int
		mode    string
		orgID   uuid.UUID
		actor   string
	}{
		{name: "days", filters: automationCostQuery{Days: 7, PageSize: 40, ProjectID: &projectID, AgentInstanceID: &instanceID, AgentKey: "generalist", Provider: "openrouter", Model: "cheap-model"}, limit: 25, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "page limit", filters: filters, limit: 10, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "project", filters: automationCostQuery{Days: 30, PageSize: 40, ProjectID: &otherProjectID, AgentInstanceID: &instanceID, AgentKey: "generalist", Provider: "openrouter", Model: "cheap-model"}, limit: 25, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "workspace", filters: filters, limit: 25, mode: "platform", actor: "actor-a"},
		{name: "organization", filters: filters, limit: 25, mode: "organization", orgID: otherOrganizationID, actor: "actor-a"},
		{name: "actor", filters: filters, limit: 25, mode: "organization", orgID: organizationID, actor: "actor-b"},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			got := automationCostAgentInstanceCursorScope(variant.filters, variant.limit, variant.mode, variant.orgID, variant.mode == "organization", variant.actor)
			if got == scope {
				t.Fatal("cursor scope did not change when query authorization scope changed")
			}
		})
	}
}

func TestAutomationCostAgentInstanceAggregateQueryAppliesScopeKeysetAndLimit(t *testing.T) {
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=localhost"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	organizationID, childClientID, projectID, clientFilterID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	snapshotAt := time.Date(2026, 9, 24, 12, 30, 0, 0, time.UTC)
	query := db.Table("(SELECT * FROM automation_executions) AS execution").
		Joins("JOIN automation_tasks AS task ON task.id = execution.automation_task_id").
		Joins("LEFT JOIN delivery_work_items AS work_item ON work_item.id = execution.delivery_work_item_id").
		Joins("LEFT JOIN delivery_projects AS project ON project.id = work_item.project_id")
	query = applyAutomationCostAgentInstanceSnapshot(query, snapshotAt, automationCostQuery{Days: 30})
	query, err = applyAutomationCostWorkspaceScope(query, "organization", organizationID, true, false, []uuid.UUID{organizationID, childClientID})
	if err != nil {
		t.Fatal(err)
	}
	query = applyAutomationCostActorScope(query, "actor-sub", []uuid.UUID{projectID}, false)
	query = applyAutomationCostFilters(query, automationCostQuery{ClientID: &clientFilterID, ProjectID: &projectID, AgentKey: "generalist"})
	cursor := &automationCostAgentInstanceCursor{SnapshotAt: snapshotAt, TotalCostMicros: 500, AgentKey: "generalist", ScopeHash: strings.Repeat("a", 64)}
	statement := automationCostAgentInstanceAggregateQuery(query, cursor, 26).Find(&[]automationCostAgentInstanceRow{}).Statement
	sql := statement.SQL.String()
	whereAt, groupAt, havingAt, orderAt, limitAt := strings.Index(sql, "WHERE"), strings.Index(sql, "GROUP BY"), strings.Index(sql, "HAVING"), strings.Index(sql, "ORDER BY"), strings.Index(sql, "LIMIT")
	if whereAt < 0 || groupAt <= whereAt || havingAt <= groupAt || orderAt <= havingAt || limitAt <= orderAt {
		t.Fatalf("aggregation did not apply authorization, grouping, keyset and limit in order: %s", sql)
	}
	for _, fragment := range []string{
		"execution.completed_at >= $1", "execution.completed_at < $2", "execution.created_at <= $3",
		"project.client_id IN ($4,$5)", "task.requested_by = $6", "work_item.project_id IN ($7)",
		"project.client_id = $8", "work_item.project_id = $9", "execution.agent_key = $10", "GROUP BY execution.agent_key, execution.agent_instance_id",
		"SUM(execution.total_cost_micros) < $11", "execution.agent_key > $13", "COALESCE(execution.agent_instance_id::text, '') > $15",
		"ORDER BY SUM(execution.total_cost_micros) DESC, execution.agent_key ASC, COALESCE(execution.agent_instance_id::text, '') ASC", "LIMIT $16",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("aggregation SQL omitted %q: %s", fragment, sql)
		}
	}
	if len(statement.Vars) != 16 || statement.Vars[len(statement.Vars)-1] != 26 {
		t.Fatalf("aggregation variables = %#v", statement.Vars)
	}
}

func TestCostAgentInstancesRequiresAuthenticatedSubject(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/costs/agent-instances", nil), recorder)
	if err := CostAgentInstances(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
	if got := recorder.Header().Get(echo.HeaderCacheControl); got != "private, no-store" {
		t.Fatalf("unauthenticated cost response cache policy = %q, want private, no-store", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCostOverviewRequiresAuthenticatedSubjectAndDisablesCaching(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/costs", nil), recorder)
	if err := CostOverview(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
	if got := recorder.Header().Get(echo.HeaderCacheControl); got != "private, no-store" {
		t.Fatalf("unauthenticated cost response cache policy = %q, want private, no-store", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCostAgentInstancesOrganizationScopeAndLegacyBucket(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	projectID, organizationID, childClientID, instanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "agent-instance-cost-actor"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)

	mock.ExpectQuery(`(?s)WITH RECURSIVE organization_clients.*SELECT id FROM organization_clients`).
		WithArgs(organizationID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(organizationID).AddRow(childClientID))
	ledgerColumns := sqlmock.NewRows([]string{"table_name", "column_name"})
	for _, column := range []string{"id", "automation_task_id", "delivery_work_item_id", "agent_key", "agent_instance_id", "total_tokens", "total_cost_micros", "completed_at", "created_at"} {
		ledgerColumns.AddRow(automationExecutionLedgerTable, column)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT table_name, column_name")+`\s+FROM information_schema\.columns`).
		WithArgs(automationExecutionLedgerTable, automationToolExecutionLedgerTable).
		WillReturnRows(ledgerColumns)
	membershipRows := sqlmock.NewRows([]string{"id", "project_id", "cognito_sub", "role", "permissions", "created_by", "created_at", "updated_at"}).
		AddRow(uuid.Must(uuid.NewV4()), projectID, subject, "viewer", `[]`, subject, time.Now(), time.Now())
	mock.ExpectQuery(`SELECT \* FROM "delivery_project_members" WHERE cognito_sub = \$1`).
		WithArgs(subject).WillReturnRows(membershipRows)
	rows := sqlmock.NewRows([]string{"agent_key", "agent_instance_id", "executions", "total_tokens", "total_cost_microusd"}).
		AddRow("generalist", "", int64(2), int64(150), int64(1500)).
		AddRow("generalist", instanceID.String(), int64(1), int64(100), int64(1000))
	mock.ExpectQuery(`SELECT .*agent_key.*`).
		WillReturnRows(rows)

	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/automation/costs/agent-instances?days=30&project_id=%s&agent_instance_limit=1", projectID), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", subject)
	ctx.Set("workspace_mode", "organization")
	ctx.Set("organization_id", organizationID)
	if err := CostAgentInstances(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data automationCostAgentInstancePage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	page := envelope.Data
	if page.RangeDays != 30 || page.SnapshotAt.IsZero() || page.Limit != 1 || !page.HasMore || page.NextCursor == "" || len(page.Items) != 1 {
		t.Fatalf("page metadata = %#v", page)
	}
	if page.Items[0].AgentKey != "generalist" || page.Items[0].AgentInstanceID != nil || page.Items[0].InstanceAttributed || page.Items[0].TotalCostMicros != 1500 || page.Items[0].TotalTokens != 150 {
		t.Fatalf("legacy identity bucket = %#v", page.Items[0])
	}
	decoded, err := decodeAutomationCostAgentInstanceCursor(page.NextCursor)
	if err != nil || !decoded.SnapshotAt.Equal(page.SnapshotAt) || decoded.InstanceID != "" || decoded.TotalCostMicros != 1500 {
		t.Fatalf("next cursor for legacy bucket = %#v, error %v", decoded, err)
	}
	if decoded.ScopeHash != automationCostAgentInstanceCursorScope(automationCostQuery{Days: 30, Page: 1, PageSize: 40, WorkItemLimit: defaultAutomationCostWorkItemLimit, ProjectID: &projectID, WorkspaceClientIDs: []uuid.UUID{organizationID, childClientID}, SnapshotAt: page.SnapshotAt}, 1, "organization", organizationID, true, subject) {
		t.Fatal("next cursor does not bind the active organization, actor, and project filters")
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt", "request_ref", "response_ref", "api_key", "secret", "PRIVATE"} {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
			t.Fatalf("response contains forbidden/private field %q: %s", forbidden, encoded)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCostAgentInstancesRejectsCursorFromOtherActorBeforeLedgerRead(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID := uuid.Must(uuid.NewV4())
	mock.ExpectQuery(`(?s)WITH RECURSIVE organization_clients.*SELECT id FROM organization_clients`).
		WithArgs(organizationID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(organizationID))
	subject := "agent-instance-cursor-actor"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	foreignScope := automationCostAgentInstanceCursorScope(automationCostQuery{Days: 30, Page: 1, PageSize: 40, WorkItemLimit: defaultAutomationCostWorkItemLimit}, defaultAutomationCostAgentInstanceLimit, "organization", organizationID, true, "another-actor")
	token, err := encodeAutomationCostAgentInstanceCursor(automationCostAgentInstanceCursor{SnapshotAt: time.Now().UTC(), TotalCostMicros: 50, AgentKey: "generalist", ScopeHash: foreignScope})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/automation/costs/agent-instances?agent_instance_cursor="+token, nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", subject)
	ctx.Set("workspace_mode", "organization")
	ctx.Set("organization_id", organizationID)
	if err := CostAgentInstances(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "Cursor does not match") {
		t.Fatalf("foreign cursor response = %d %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
