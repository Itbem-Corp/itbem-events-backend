package automation

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestParseAutomationCostQueryIncludesScopedFiltersAndCursor(t *testing.T) {
	clientID, projectID, epicID, workItemID, agentInstanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	organizationID := uuid.Must(uuid.NewV4())
	fromAt := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC)
	toAt := time.Date(2026, 9, 26, 6, 0, 0, 0, time.UTC)
	cursorSnapshotAt := time.Now().UTC().Truncate(time.Microsecond)
	cursorSource := automationCostExecution{ID: uuid.Must(uuid.NewV4()), CompletedAt: cursorSnapshotAt.Add(-time.Minute)}
	filters := automationCostQuery{Days: 90, FromAt: fromAt, ToAt: toAt, Page: 2, PageSize: 20, WorkItemLimit: 13, ClientID: &clientID, ProjectID: &projectID, EpicID: &epicID, WorkItemID: &workItemID, AgentInstanceID: &agentInstanceID, AgentKey: "backend-engineer", StepKey: "build_api", Provider: "openrouter", Model: "openai/gpt-4.1-mini", SnapshotAt: cursorSnapshotAt}
	scope := automationCostCursorScope(filters, "organization", organizationID, true, "actor-sub")
	cursor, err := encodeAutomationCostCursor(cursorSource, scope, cursorSnapshotAt)
	if err != nil {
		t.Fatal(err)
	}
	workItemCursor, err := encodeAutomationCostWorkItemCursor(automationCostWorkItemCursor{
		SnapshotAt: cursorSnapshotAt, TotalCostMicros: 2500, WorkItemID: workItemID, ScopeHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	context := automationCostFilterContext(fmt.Sprintf("/automation/costs?days=90&from_at=%s&to_at=%s&snapshot_at=%s&client_id=%s&project_id=%s&epic_id=%s&work_item_id=%s&agent_instance_id=%s&agent_key=backend-engineer&step_key=build_api&provider=OPENROUTER&model=openai/gpt-4.1-mini&page=2&page_size=20&cursor=%s&work_item_limit=13&work_item_cursor=%s", fromAt.Format(time.RFC3339Nano), toAt.Format(time.RFC3339Nano), cursorSnapshotAt.Format(time.RFC3339Nano), clientID, projectID, epicID, workItemID, agentInstanceID, cursor, workItemCursor))
	parsed, err := parseAutomationCostQuery(context)
	if err != nil {
		t.Fatalf("parse filtered cost query: %v", err)
	}
	if parsed.Days != 90 || !parsed.FromAt.Equal(fromAt) || !parsed.ToAt.Equal(toAt) || parsed.Page != 2 || parsed.PageSize != 20 || parsed.WorkItemLimit != 13 || parsed.ClientID == nil || *parsed.ClientID != clientID || parsed.ProjectID == nil || *parsed.ProjectID != projectID || parsed.EpicID == nil || *parsed.EpicID != epicID || parsed.WorkItemID == nil || *parsed.WorkItemID != workItemID || parsed.AgentInstanceID == nil || *parsed.AgentInstanceID != agentInstanceID || parsed.AgentKey != "backend-engineer" || parsed.StepKey != "build_api" || parsed.Provider != "openrouter" || parsed.Model != "openai/gpt-4.1-mini" || parsed.Cursor == nil || parsed.WorkItemCursor == nil || parsed.WorkItemCursorToken != workItemCursor || !parsed.SnapshotAt.Equal(cursorSnapshotAt) {
		t.Fatalf("parsed cost query = %#v", parsed)
	}
	if parsed.Cursor.ID != cursorSource.ID || !parsed.Cursor.CompletedAt.Equal(cursorSource.CompletedAt) || !parsed.Cursor.SnapshotAt.Equal(cursorSnapshotAt) {
		t.Fatalf("parsed cursor = %#v", parsed.Cursor)
	}
	if parsed.Cursor.ScopeHash != scope {
		t.Fatalf("parsed cursor scope = %q, want %q", parsed.Cursor.ScopeHash, scope)
	}
	if parsed.WorkItemCursor.WorkItemID != workItemID || parsed.WorkItemCursor.TotalCostMicros != 2500 {
		t.Fatalf("parsed work-item cursor = %#v", parsed.WorkItemCursor)
	}
}

func TestParseAutomationCostQueryDefaultsWorkItemPageLimit(t *testing.T) {
	parsed, err := parseAutomationCostQuery(automationCostFilterContext("/automation/costs"))
	if err != nil {
		t.Fatalf("parse default cost query: %v", err)
	}
	if parsed.WorkItemLimit != defaultAutomationCostWorkItemLimit || parsed.WorkItemCursor != nil || parsed.WorkItemCursorToken != "" {
		t.Fatalf("default work-item pagination = limit %d cursor %#v token %q", parsed.WorkItemLimit, parsed.WorkItemCursor, parsed.WorkItemCursorToken)
	}
}

func TestParseAutomationCostQueryRejectsInvalidFilters(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "range", query: "?days=366"},
		{name: "incomplete custom range", query: "?from_at=2026-09-01T00%3A00%3A00Z"},
		{name: "invalid custom start", query: "?from_at=not-a-time&to_at=2026-09-02T00%3A00%3A00Z"},
		{name: "custom end before start", query: "?from_at=2026-09-02T00%3A00%3A00Z&to_at=2026-09-01T00%3A00%3A00Z"},
		{name: "custom range too large", query: "?from_at=2024-01-01T00%3A00%3A00Z&to_at=2026-09-01T00%3A00%3A00Z"},
		{name: "duplicate custom start", query: "?from_at=2026-09-01T00%3A00%3A00Z&from_at=2026-09-02T00%3A00%3A00Z&to_at=2026-09-03T00%3A00%3A00Z"},
		{name: "page", query: "?page=0"},
		{name: "offset abuse", query: "?page=10001"},
		{name: "page size", query: "?page_size=101"},
		{name: "project id", query: "?project_id=not-a-uuid"},
		{name: "client id", query: "?client_id=not-a-uuid"},
		{name: "duplicate client", query: "?client_id=00000000-0000-4000-8000-000000000001&client_id=00000000-0000-4000-8000-000000000002"},
		{name: "invalid snapshot", query: "?snapshot_at=not-a-time"},
		{name: "duplicate snapshot", query: "?snapshot_at=2026-09-24T12%3A00%3A00Z&snapshot_at=2026-09-24T12%3A01%3A00Z"},
		{name: "work item id", query: "?work_item_id=not-a-uuid"},
		{name: "epic id", query: "?epic_id=not-a-uuid"},
		{name: "agent instance id", query: "?agent_instance_id=not-a-uuid"},
		{name: "agent", query: "?agent_key=../other"},
		{name: "step key", query: "?step_key=../other"},
		{name: "provider", query: "?provider=unknown"},
		{name: "model", query: "?model=bad%20model"},
		{name: "cursor", query: "?cursor=not-base64"},
		{name: "work item limit low", query: "?work_item_limit=0"},
		{name: "work item limit high", query: "?work_item_limit=101"},
		{name: "work item limit sign", query: "?work_item_limit=%2B5"},
		{name: "duplicate work item limit", query: "?work_item_limit=5&work_item_limit=10"},
		{name: "work item cursor", query: "?work_item_cursor=not-base64"},
		{name: "duplicate work item cursor", query: "?work_item_cursor=YQ&work_item_cursor=Yg"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseAutomationCostQuery(automationCostFilterContext("/automation/costs" + test.query)); err == nil {
				t.Fatal("invalid cost query unexpectedly accepted")
			}
		})
	}
}

func TestAutomationCostWorkItemCursorIsStableAndStrict(t *testing.T) {
	original := automationCostWorkItemCursor{
		SnapshotAt: time.Now().UTC().Truncate(time.Microsecond), TotalCostMicros: 2500, WorkItemID: uuid.Must(uuid.NewV4()), ScopeHash: strings.Repeat("b", 64),
	}
	encoded, err := encodeAutomationCostWorkItemCursor(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeAutomationCostWorkItemCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != 1 || !decoded.SnapshotAt.Equal(original.SnapshotAt) || decoded.TotalCostMicros != original.TotalCostMicros || decoded.WorkItemID != original.WorkItemID || decoded.ScopeHash != original.ScopeHash {
		t.Fatalf("decoded work-item cursor = %#v", decoded)
	}
	for _, invalid := range []automationCostWorkItemCursor{
		{SnapshotAt: original.SnapshotAt, TotalCostMicros: 1, WorkItemID: uuid.Nil, ScopeHash: strings.Repeat("b", 64)},
		{SnapshotAt: original.SnapshotAt, TotalCostMicros: -1, WorkItemID: original.WorkItemID, ScopeHash: strings.Repeat("b", 64)},
		{SnapshotAt: original.SnapshotAt, TotalCostMicros: 1, WorkItemID: original.WorkItemID, ScopeHash: "not-hex"},
	} {
		if _, err := encodeAutomationCostWorkItemCursor(invalid); err == nil {
			t.Fatalf("invalid work-item cursor source unexpectedly encoded: %#v", invalid)
		}
	}
	if _, err := decodeAutomationCostWorkItemCursor(strings.Repeat("a", maxAutomationCostWorkItemCursor+1)); err == nil {
		t.Fatal("oversized work-item cursor unexpectedly decoded")
	}
}

func TestAutomationCostWorkItemCursorScopeBindsFiltersWorkspaceAndActor(t *testing.T) {
	organizationID, otherOrganizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	projectID, otherProjectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	workItemID, otherWorkItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	instanceID, otherInstanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	base := automationCostQuery{Days: 30, WorkItemLimit: 20, ProjectID: &projectID, WorkItemID: &workItemID, AgentInstanceID: &instanceID, AgentKey: "generalist", Provider: "openrouter", Model: "cheap-model"}
	scope := automationCostWorkItemCursorScope(base, "organization", organizationID, true, "actor-a")
	variants := []struct {
		name    string
		filters automationCostQuery
		mode    string
		orgID   uuid.UUID
		actor   string
	}{
		{name: "date range", filters: automationCostQuery{Days: 7, WorkItemLimit: 20, ProjectID: &projectID, WorkItemID: &workItemID, AgentKey: "generalist", Provider: "openrouter", Model: "cheap-model"}, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "page limit", filters: automationCostQuery{Days: 30, WorkItemLimit: 10, ProjectID: &projectID, WorkItemID: &workItemID, AgentKey: "generalist", Provider: "openrouter", Model: "cheap-model"}, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "project", filters: automationCostQuery{Days: 30, WorkItemLimit: 20, ProjectID: &otherProjectID, WorkItemID: &workItemID, AgentKey: "generalist", Provider: "openrouter", Model: "cheap-model"}, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "work item", filters: automationCostQuery{Days: 30, WorkItemLimit: 20, ProjectID: &projectID, WorkItemID: &otherWorkItemID, AgentKey: "generalist", Provider: "openrouter", Model: "cheap-model"}, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "agent instance", filters: automationCostQuery{Days: 30, WorkItemLimit: 20, ProjectID: &projectID, WorkItemID: &workItemID, AgentInstanceID: &otherInstanceID, AgentKey: "generalist", Provider: "openrouter", Model: "cheap-model"}, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "agent", filters: automationCostQuery{Days: 30, WorkItemLimit: 20, ProjectID: &projectID, WorkItemID: &workItemID, AgentKey: "qa", Provider: "openrouter", Model: "cheap-model"}, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "provider", filters: automationCostQuery{Days: 30, WorkItemLimit: 20, ProjectID: &projectID, WorkItemID: &workItemID, AgentKey: "generalist", Provider: "deepseek", Model: "cheap-model"}, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "model", filters: automationCostQuery{Days: 30, WorkItemLimit: 20, ProjectID: &projectID, WorkItemID: &workItemID, AgentKey: "generalist", Provider: "openrouter", Model: "another-model"}, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "workspace", filters: base, mode: "platform", orgID: uuid.Nil, actor: "actor-a"},
		{name: "organization", filters: base, mode: "organization", orgID: otherOrganizationID, actor: "actor-a"},
		{name: "actor", filters: base, mode: "organization", orgID: organizationID, actor: "actor-b"},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			if got := automationCostWorkItemCursorScope(variant.filters, variant.mode, variant.orgID, variant.mode == "organization", variant.actor); got == scope {
				t.Fatal("work-item cursor scope unexpectedly remained valid after scope changed")
			}
		})
	}
}

func TestAutomationCostCursorIsStableAndBounded(t *testing.T) {
	snapshotAt := time.Now().UTC().Truncate(time.Microsecond)
	original := automationCostExecution{ID: uuid.Must(uuid.NewV4()), CompletedAt: snapshotAt.Add(-time.Second).In(time.FixedZone("test", -7*60*60))}
	scope := automationCostCursorScope(automationCostQuery{Days: 30, PageSize: 40}, "platform", uuid.Nil, false, "platform-actor")
	encoded, err := encodeAutomationCostCursor(original, scope, snapshotAt)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeAutomationCostCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != original.ID || !decoded.CompletedAt.Equal(original.CompletedAt) || decoded.CompletedAt.Location() != time.UTC || !decoded.SnapshotAt.Equal(snapshotAt) || decoded.ScopeHash != scope {
		t.Fatalf("decoded cursor = %#v", decoded)
	}
	if _, err := decodeAutomationCostCursor(strings.Repeat("a", 1025)); err == nil {
		t.Fatal("oversized cursor unexpectedly accepted")
	}
}

func TestAutomationCostCursorScopeBindsFiltersWorkspaceAndActor(t *testing.T) {
	organizationID, otherOrganizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	projectID, otherProjectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	instanceID, otherInstanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	epicID := uuid.Must(uuid.NewV4())
	base := automationCostQuery{Days: 30, PageSize: 40, ProjectID: &projectID, EpicID: &epicID, AgentInstanceID: &instanceID, StepKey: "build", Provider: "openrouter", Model: "model-a"}
	scope := automationCostCursorScope(base, "organization", organizationID, true, "actor-a")
	variants := []struct {
		name    string
		filters automationCostQuery
		mode    string
		orgID   uuid.UUID
		actor   string
	}{
		{name: "project", filters: automationCostQuery{Days: 30, PageSize: 40, ProjectID: &otherProjectID, EpicID: &epicID, AgentInstanceID: &instanceID, StepKey: "build", Provider: "openrouter", Model: "model-a"}, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "epic", filters: automationCostQuery{Days: 30, PageSize: 40, ProjectID: &projectID, EpicID: func() *uuid.UUID { value := uuid.Must(uuid.NewV4()); return &value }(), AgentInstanceID: &instanceID, StepKey: "build", Provider: "openrouter", Model: "model-a"}, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "step", filters: automationCostQuery{Days: 30, PageSize: 40, ProjectID: &projectID, EpicID: base.EpicID, AgentInstanceID: &instanceID, StepKey: "qa", Provider: "openrouter", Model: "model-a"}, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "provider", filters: automationCostQuery{Days: 30, PageSize: 40, ProjectID: &projectID, Provider: "deepseek", Model: "model-a"}, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "agent instance", filters: automationCostQuery{Days: 30, PageSize: 40, ProjectID: &projectID, AgentInstanceID: &otherInstanceID, Provider: "openrouter", Model: "model-a"}, mode: "organization", orgID: organizationID, actor: "actor-a"},
		{name: "workspace", filters: base, mode: "platform", orgID: uuid.Nil, actor: "actor-a"},
		{name: "organization", filters: base, mode: "organization", orgID: otherOrganizationID, actor: "actor-a"},
		{name: "actor", filters: base, mode: "organization", orgID: organizationID, actor: "actor-b"},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			if got := automationCostCursorScope(variant.filters, variant.mode, variant.orgID, variant.mode == "organization", variant.actor); got == scope {
				t.Fatal("cursor scope unexpectedly remained valid after scope changed")
			}
		})
	}
}

func TestAutomationCostCursorScopesBindCustomDateRange(t *testing.T) {
	organizationID := uuid.Must(uuid.NewV4())
	start := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 26, 6, 0, 0, 0, time.UTC)
	base := automationCostQuery{Days: 30, PageSize: 40, WorkItemLimit: 20, FromAt: start, ToAt: end}
	variants := []automationCostQuery{
		{Days: 30, PageSize: 40, WorkItemLimit: 20, FromAt: start.Add(time.Hour), ToAt: end},
		{Days: 30, PageSize: 40, WorkItemLimit: 20, FromAt: start, ToAt: end.Add(time.Hour)},
	}
	for index, variant := range variants {
		if automationCostCursorScope(variant, "organization", organizationID, true, "actor") == automationCostCursorScope(base, "organization", organizationID, true, "actor") {
			t.Fatalf("execution cursor scope did not change for date variant %d", index)
		}
		if automationCostWorkItemCursorScope(variant, "organization", organizationID, true, "actor") == automationCostWorkItemCursorScope(base, "organization", organizationID, true, "actor") {
			t.Fatalf("work-item cursor scope did not change for date variant %d", index)
		}
	}
}

func TestApplyAutomationCostTimeWindowUsesExclusiveEndAndSnapshotCap(t *testing.T) {
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=localhost"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshotAt := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	fromAt := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC)
	toAt := time.Date(2026, 9, 26, 6, 0, 0, 0, time.UTC)
	filters := automationCostQuery{Days: 30, FromAt: fromAt, ToAt: toAt}
	statement := applyAutomationCostTimeWindow(db.Table("automation_executions AS execution"), snapshotAt, filters).Find(&[]map[string]any{}).Statement
	for _, fragment := range []string{"execution.completed_at >= $1", "execution.completed_at < $2", "execution.created_at <= $3"} {
		if !strings.Contains(statement.SQL.String(), fragment) {
			t.Fatalf("time-window SQL omitted %q: %s", fragment, statement.SQL.String())
		}
	}
	if len(statement.Vars) != 3 || !statement.Vars[0].(time.Time).Equal(fromAt) || !statement.Vars[1].(time.Time).Equal(snapshotAt.Add(time.Microsecond)) || !statement.Vars[2].(time.Time).Equal(snapshotAt) {
		t.Fatalf("time-window parameters = %#v", statement.Vars)
	}
}

func TestAutomationCostWorkItemCursorScopeBindsEpicAndStepFilters(t *testing.T) {
	organizationID := uuid.Must(uuid.NewV4())
	projectID, epicID, otherEpicID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	base := automationCostQuery{Days: 30, WorkItemLimit: 20, ProjectID: &projectID, EpicID: &epicID, StepKey: "build", Provider: "openrouter"}
	scope := automationCostWorkItemCursorScope(base, "organization", organizationID, true, "actor-a")
	variants := []automationCostQuery{
		{Days: 30, WorkItemLimit: 20, ProjectID: &projectID, EpicID: &otherEpicID, StepKey: "build", Provider: "openrouter"},
		{Days: 30, WorkItemLimit: 20, ProjectID: &projectID, EpicID: &epicID, StepKey: "qa", Provider: "openrouter"},
	}
	for index, variant := range variants {
		if got := automationCostWorkItemCursorScope(variant, "organization", organizationID, true, "actor-a"); got == scope {
			t.Fatalf("work-item cursor scope unexpectedly remained valid after epic/step variant %d", index)
		}
	}
}

func TestApplyAutomationCostFiltersUsesBoundedSQLParameters(t *testing.T) {
	projectID, workItemID, instanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=localhost"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	epicID := uuid.Must(uuid.NewV4())
	filters := automationCostQuery{
		ProjectID: &projectID, EpicID: &epicID, WorkItemID: &workItemID, AgentInstanceID: &instanceID,
		AgentKey: "backend-engineer", StepKey: "build_api", Provider: "openrouter", Model: "openai/gpt-4.1-mini",
	}
	query := db.Table("automation_executions AS execution").
		Joins("JOIN automation_tasks AS task ON task.id = execution.automation_task_id").
		Joins("LEFT JOIN delivery_work_items AS work_item ON work_item.id = execution.delivery_work_item_id")
	statement := applyAutomationCostFilters(query, filters).Find(&[]map[string]any{}).Statement
	sql := statement.SQL.String()
	for _, fragment := range []string{
		"work_item.project_id = $1",
		"EXISTS (SELECT 1 FROM delivery_epic_work_items AS epic_membership WHERE epic_membership.work_item_id = execution.delivery_work_item_id AND epic_membership.project_id = work_item.project_id AND epic_membership.epic_id = $2 AND epic_membership.created_at <= execution.completed_at AND (epic_membership.deleted_at IS NULL OR epic_membership.deleted_at > execution.completed_at))",
		"execution.delivery_work_item_id = $3", "execution.agent_key = $4", "execution.agent_instance_id = $5",
		"LOWER(execution.provider) = $6", "LOWER(execution.model) = LOWER($7)", "execution.step_key = $8",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("SQL does not contain %q: %s", fragment, sql)
		}
	}
	if len(statement.Vars) != 8 || statement.Vars[1] != epicID || statement.Vars[2] != workItemID || statement.Vars[3] != "backend-engineer" || statement.Vars[4] != instanceID || statement.Vars[5] != "openrouter" || statement.Vars[6] != "openai/gpt-4.1-mini" || statement.Vars[7] != "build_api" {
		t.Fatalf("query variables = %#v", statement.Vars)
	}
}

func TestApplyAutomationCostCursorUsesStableDescendingKeyset(t *testing.T) {
	cursor := &automationCostCursor{ID: uuid.Must(uuid.NewV4()), CompletedAt: time.Date(2026, 9, 24, 12, 30, 0, 0, time.UTC)}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=localhost"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	query := applyAutomationCostCursor(db.Table("automation_executions AS execution"), cursor)
	statement := query.Find(&[]map[string]any{}).Statement
	if !strings.Contains(statement.SQL.String(), "execution.completed_at < $1") || !strings.Contains(statement.SQL.String(), "execution.completed_at = $2") || !strings.Contains(statement.SQL.String(), "execution.id < $3") {
		t.Fatalf("cursor SQL = %s", statement.SQL.String())
	}
	if len(statement.Vars) != 3 || statement.Vars[2] != cursor.ID {
		t.Fatalf("cursor variables = %#v", statement.Vars)
	}
}

func TestApplyAutomationCostWorkItemCursorUsesStableCostThenUUIDKeyset(t *testing.T) {
	cursor := &automationCostWorkItemCursor{Version: 1, WorkItemID: uuid.Must(uuid.NewV4()), TotalCostMicros: 2500, ScopeHash: strings.Repeat("c", 64)}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=localhost"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	query := db.Table("automation_executions AS execution").
		Joins("JOIN delivery_work_items AS work_item ON work_item.id = execution.delivery_work_item_id").
		Select("work_item.id, SUM(execution.total_cost_micros) AS total_cost_micros").
		Group("work_item.id").
		Order("SUM(execution.total_cost_micros) DESC, work_item.id ASC")
	statement := applyAutomationCostWorkItemCursor(query, cursor).Limit(defaultAutomationCostWorkItemLimit + 1).Find(&[]map[string]any{}).Statement
	sql := statement.SQL.String()
	for _, fragment := range []string{"HAVING SUM(execution.total_cost_micros) < $1", "SUM(execution.total_cost_micros) = $2", "work_item.id > $3", "ORDER BY SUM(execution.total_cost_micros) DESC, work_item.id ASC", "LIMIT $4"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("work-item cursor SQL omitted %q: %s", fragment, sql)
		}
	}
	if len(statement.Vars) != 4 || statement.Vars[0] != int64(2500) || statement.Vars[1] != int64(2500) || statement.Vars[2] != cursor.WorkItemID || statement.Vars[3] != defaultAutomationCostWorkItemLimit+1 {
		t.Fatalf("work-item cursor query variables = %#v", statement.Vars)
	}
}

func TestAutomationCostWorkItemAggregationAppliesScopeAndFiltersBeforePagination(t *testing.T) {
	projectID, workItemID, organizationID, instanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=localhost"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	epicID := uuid.Must(uuid.NewV4())
	filters := automationCostQuery{
		ProjectID: &projectID, EpicID: &epicID, WorkItemID: &workItemID, AgentInstanceID: &instanceID,
		AgentKey: "backend-engineer", StepKey: "build", Provider: "openrouter", Model: "cheap-model",
	}
	cursor := &automationCostWorkItemCursor{
		TotalCostMicros: 2500, WorkItemID: uuid.Must(uuid.NewV4()), ScopeHash: strings.Repeat("d", 64),
	}
	base := db.Table("automation_executions AS execution").
		Joins("JOIN automation_tasks AS task ON task.id = execution.automation_task_id").
		Joins("JOIN delivery_work_items AS work_item ON work_item.id = execution.delivery_work_item_id").
		Joins("JOIN delivery_projects AS project ON project.id = work_item.project_id").
		Where("execution.completed_at >= ?", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)).
		Where("project.client_id = ?", organizationID).
		Where("task.requested_by = ?", "actor-sub")
	query := applyAutomationCostFilters(base, filters).
		Select("project.id AS project_id, work_item.id AS work_item_id, SUM(execution.total_cost_micros) AS total_cost_micros").
		Group("project.id, work_item.id").
		Order("SUM(execution.total_cost_micros) DESC, work_item.id ASC")
	statement := applyAutomationCostWorkItemCursor(query, cursor).Limit(21).Find(&[]map[string]any{}).Statement
	sql := statement.SQL.String()
	whereAt := strings.Index(sql, "WHERE")
	groupAt := strings.Index(sql, "GROUP BY")
	havingAt := strings.Index(sql, "HAVING")
	orderAt := strings.Index(sql, "ORDER BY")
	limitAt := strings.Index(sql, "LIMIT")
	if whereAt < 0 || groupAt <= whereAt || havingAt <= groupAt || orderAt <= havingAt || limitAt <= orderAt {
		t.Fatalf("cost query did not apply filters, keyset, and limit in order: %s", sql)
	}
	for _, fragment := range []string{
		"execution.completed_at >= $1", "project.client_id = $2", "task.requested_by = $3",
		"work_item.project_id = $4", "epic_membership.epic_id = $5", "epic_membership.deleted_at IS NULL",
		"execution.delivery_work_item_id = $6",
		"execution.agent_key = $7", "execution.agent_instance_id = $8", "LOWER(execution.provider) = $9", "LOWER(execution.model) = LOWER($10)", "execution.step_key = $11",
		"HAVING SUM(execution.total_cost_micros) < $12", "work_item.id > $14", "LIMIT $15",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("cost query omitted %q: %s", fragment, sql)
		}
	}
}

func TestAutomationCostWorkspaceScopeIntersectsActorAccessWithOrganization(t *testing.T) {
	organizationID := uuid.Must(uuid.NewV4())
	childClientID := uuid.Must(uuid.NewV4())
	projectID := uuid.Must(uuid.NewV4())
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=localhost"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	query := db.Table("automation_tasks AS task").
		Joins("LEFT JOIN delivery_work_items AS work_item ON work_item.id = task.delivery_work_item_id").
		Joins("LEFT JOIN delivery_projects AS project ON project.id = work_item.project_id")
	query = applyAutomationCostActorScope(query, "actor-sub", []uuid.UUID{projectID}, false)
	clientIDs := []uuid.UUID{organizationID, childClientID}
	query, err = applyAutomationCostWorkspaceScope(query, "organization", organizationID, true, false, clientIDs)
	if err != nil {
		t.Fatalf("apply organization workspace: %v", err)
	}
	statement := query.Find(&[]map[string]any{}).Statement
	sql := statement.SQL.String()
	if !strings.Contains(sql, "((task.requested_by = $1 OR work_item.project_id IN ($2))) AND project.client_id IN ($3,$4)") {
		t.Fatalf("organization and actor predicates are not intersected: %s", sql)
	}
	if len(statement.Vars) != 4 || statement.Vars[0] != "actor-sub" || statement.Vars[1] != projectID || statement.Vars[2] != organizationID || statement.Vars[3] != childClientID {
		t.Fatalf("workspace query variables = %#v", statement.Vars)
	}
}

func TestAutomationCostWorkspaceScopeFailsClosed(t *testing.T) {
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=localhost"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	query := db.Table("delivery_projects AS project")
	for _, test := range []struct {
		name              string
		mode              string
		organizationID    uuid.UUID
		hasOrganizationID bool
		platformAdmin     bool
		clientIDs         []uuid.UUID
	}{
		{name: "missing organization", mode: "organization"},
		{name: "zero organization", mode: "organization", organizationID: uuid.Nil, hasOrganizationID: true, clientIDs: []uuid.UUID{uuid.Must(uuid.NewV4())}},
		{name: "empty organization scope", mode: "organization", organizationID: uuid.Must(uuid.NewV4()), hasOrganizationID: true},
		{name: "non-admin platform", mode: "platform"},
		{name: "unknown mode", mode: "workspace"},
		{name: "missing mode"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := applyAutomationCostWorkspaceScope(query, test.mode, test.organizationID, test.hasOrganizationID, test.platformAdmin, test.clientIDs); err == nil {
				t.Fatal("invalid cost workspace unexpectedly accepted")
			}
		})
	}
	platformQuery, err := applyAutomationCostWorkspaceScope(query, "platform", uuid.Nil, false, true, nil)
	if err != nil {
		t.Fatalf("platform administrator should access platform costs: %v", err)
	}
	if statement := platformQuery.Find(&[]map[string]any{}).Statement; strings.Contains(statement.SQL.String(), "WHERE") {
		t.Fatalf("platform scope should not apply a tenant predicate: %s", statement.SQL.String())
	}
}

func automationCostFilterContext(target string) echo.Context {
	e := echo.New()
	return e.NewContext(httptest.NewRequest(http.MethodGet, target, nil), httptest.NewRecorder())
}
