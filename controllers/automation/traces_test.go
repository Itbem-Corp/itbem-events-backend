package automation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestGetAutomationTracesDeniesUnauthorizedTenantProject(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	projectID, organizationID, otherClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "trace-explorer-unassigned"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, otherClientID))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/automation/traces?project_id="+projectID.String(), nil)
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", subject)
	setAgentHistoryOrganizationWorkspace(ctx, organizationID)
	if err := GetAutomationTraces(ctx); err == nil {
		t.Fatal("unauthorized project trace query unexpectedly succeeded")
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unauthorized project status = %d, want not found: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAutomationTracesKeepsPlatformAdminInsideSelectedOrganization(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID := uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	mock.ExpectQuery(`(?s)WITH activity AS \(.*FROM activity WHERE TRUE AND occurred_at <= \$2 AND recorded_at <= \$3 AND client_id IN \(.*WITH RECURSIVE organization_clients.*SELECT id FROM organization_clients.*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$5`).
		WithArgs("", sqlmock.AnyArg(), sqlmock.AnyArg(), organizationID, agentHistoryDefaultLimit+1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	request := httptest.NewRequest(http.MethodGet, "/api/automation/traces", nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", "trace-root-customer-workspace")
	setAgentHistoryOrganizationWorkspace(ctx, organizationID)
	if err := GetAutomationTraces(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("tenant-scoped root trace status = %d: %s; SQL mocks: %v", recorder.Code, recorder.Body.String(), mock.ExpectationsWereMet())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAutomationTracesCanFilterOrganizationWithoutChangingWorkspaceScope(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID := uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	mock.ExpectQuery(`(?s)WITH activity AS \(.*FROM activity WHERE TRUE AND occurred_at <= \$2 AND recorded_at <= \$3 AND client_id IN \(.*WITH RECURSIVE organization_clients.*WHERE clients.id = \$4.*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$5`).
		WithArgs("", sqlmock.AnyArg(), sqlmock.AnyArg(), organizationID, agentHistoryDefaultLimit+1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	request := httptest.NewRequest(http.MethodGet, "/api/automation/traces?organization_id="+organizationID.String(), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", "trace-root")
	setAgentHistoryPlatformWorkspace(ctx)
	if err := GetAutomationTraces(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("organization-filtered platform traces status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// A URL parameter cannot switch an organization workspace away from the
	// server-selected organization, even for a platform administrator.
	selectedID, otherID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	badRequest := httptest.NewRequest(http.MethodGet, "/api/automation/traces?organization_id="+otherID.String(), nil)
	badRecorder := httptest.NewRecorder()
	badContext := echo.New().NewContext(badRequest, badRecorder)
	badContext.Set("cognito_sub", "trace-root")
	badContext.Set("workspace_mode", "organization")
	badContext.Set("organization_id", selectedID)
	_ = GetAutomationTraces(badContext)
	if badRecorder.Code != http.StatusNotFound {
		t.Fatalf("cross-organization filter status = %d, want not found: %s", badRecorder.Code, badRecorder.Body.String())
	}
}

func TestGetAutomationTracesSnapshotBoundsOccurredAndRecordedTimes(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	snapshotAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	mock.ExpectQuery(`(?s)WITH activity AS \(.*FROM activity WHERE TRUE AND occurred_at <= \$2 AND recorded_at <= \$3.*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$4`).
		WithArgs("", snapshotAt, snapshotAt, agentHistoryDefaultLimit+1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	request := httptest.NewRequest(http.MethodGet, "/api/automation/traces?snapshot_at="+snapshotAt.Format(time.RFC3339Nano), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", "trace-root")
	setAgentHistoryPlatformWorkspace(ctx)
	if err := GetAutomationTraces(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("snapshot-bounded traces status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAutomationTracesUsesOneGlobalSnapshotCursorAndSafeAgentProjection(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	now := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	snapshotAt := now.Add(30 * time.Second)
	eventID, nextEventID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	runID := uuid.Must(uuid.NewV4()).String()
	columns := []string{"id", "kind", "occurred_at", "task_id", "run_id", "current_agent_key", "operation", "status", "provider", "model", "input_tokens", "output_tokens", "total_cost_micros", "pricing_basis", "currency", "summary", "activity_action"}
	rows := sqlmock.NewRows(columns).
		AddRow(eventID, "inference", now, taskID, runID, "atlas", "delivery.implementation", "completed", "openrouter", "cheap-model", int64(90), int64(24), int64(17), "official_api_price", "USD", "Inference call", "").
		AddRow(nextEventID, "tool_call", now.Add(-time.Minute), taskID, runID, "atlas", "delivery.implementation", "completed", "openrouter", "cheap-model", int64(40), int64(10), int64(5), "official_api_price", "USD", "AI tool call", "stagehand")
	mock.ExpectQuery(`(?s)WITH activity AS \(.*AS pricing_basis,.*AS currency,.*FROM activity WHERE TRUE AND occurred_at <= \$2 AND recorded_at <= \$3 AND \(current_agent_key = \$4 OR \(kind = 'task_event' AND previous_agent_key = \$5\)\).*kind = 'tool_call' AND activity_action = \$6.*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$7`).
		WithArgs("", sqlmock.AnyArg(), sqlmock.AnyArg(), "atlas", "atlas", "stagehand", 2).
		WillReturnRows(rows)

	request := httptest.NewRequest(http.MethodGet, "/api/automation/traces?limit=1&agent_key=atlas&tool=stagehand&snapshot_at="+snapshotAt.Format(time.RFC3339Nano), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", "trace-root")
	setAgentHistoryPlatformWorkspace(ctx)
	if err := GetAutomationTraces(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("global traces status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("trace cache policy = %q, want private, no-store", recorder.Header().Get("Cache-Control"))
	}
	var envelope struct {
		Data automationTraceResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode trace response: %v", err)
	}
	page := envelope.Data
	if len(page.Items) != 1 || page.Items[0].ID != eventID || page.Items[0].AgentKey != "atlas" || page.Items[0].Kind != "inference" || page.Items[0].AutomationTaskID == nil || *page.Items[0].AutomationTaskID != taskID || page.Items[0].RunID != runID || page.Items[0].InputTokens == nil || *page.Items[0].InputTokens != 90 || page.Items[0].TotalCostMicros == nil || *page.Items[0].TotalCostMicros != 17 || page.Items[0].CostPricingStatus != "verified_usd" || page.CostCoverage.Scope != "returned_page" || page.CostCoverage.VerifiedUSDExecutions != 1 || page.CostCoverage.UnpricedExecutions != 0 || !page.HasMore || page.NextCursor == "" || page.Limit != 1 || !page.SnapshotAt.Equal(snapshotAt) {
		t.Fatalf("unexpected first global page: %#v", page)
	}
	if strings.Contains(strings.ToLower(recorder.Body.String()), "current_agent_key") || strings.Contains(strings.ToLower(recorder.Body.String()), "request_ref") {
		t.Fatalf("internal or private trace fields leaked: %s", recorder.Body.String())
	}
	cursor, err := decodeAgentHistoryCursor(page.NextCursor)
	if err != nil || cursor.ID != eventID || cursor.Kind != "inference" || !cursor.SnapshotAt.Equal(page.SnapshotAt) {
		t.Fatalf("cursor does not preserve global page position/snapshot: %#v, %v", cursor, err)
	}
	if strings.Contains(recorder.Body.String(), taskID.String()) == false || !strings.Contains(recorder.Body.String(), runID) {
		t.Fatalf("trace response omitted safe task/run correlation identifiers: %s", recorder.Body.String())
	}

	// A changed snapshot must fail before another database query is issued.
	changedSnapshot := page.SnapshotAt.Add(time.Second).Format(time.RFC3339Nano)
	badRecorder := httptest.NewRecorder()
	badRequest := httptest.NewRequest(http.MethodGet, "/api/automation/traces?limit=1&agent_key=atlas&tool=stagehand&cursor="+page.NextCursor+"&snapshot_at="+changedSnapshot, nil)
	badContext := echo.New().NewContext(badRequest, badRecorder)
	badContext.Set("cognito_sub", "trace-root")
	setAgentHistoryPlatformWorkspace(badContext)
	_ = GetAutomationTraces(badContext)
	if badRecorder.Code != http.StatusBadRequest {
		t.Fatalf("changed cursor snapshot status = %d, want 400: %s", badRecorder.Code, badRecorder.Body.String())
	}
	filterRecorder := httptest.NewRecorder()
	filterRequest := httptest.NewRequest(http.MethodGet, "/api/automation/traces?limit=1&agent_key=atlas&tool=agent_loop&cursor="+page.NextCursor+"&snapshot_at="+page.SnapshotAt.Format(time.RFC3339Nano), nil)
	filterContext := echo.New().NewContext(filterRequest, filterRecorder)
	filterContext.Set("cognito_sub", "trace-root")
	setAgentHistoryPlatformWorkspace(filterContext)
	_ = GetAutomationTraces(filterContext)
	if filterRecorder.Code != http.StatusBadRequest {
		t.Fatalf("changed cursor filter status = %d, want 400: %s", filterRecorder.Code, filterRecorder.Body.String())
	}

	// Continue the same global keyset page under its original snapshot.
	mock.ExpectQuery(`(?s)WITH activity AS \(.*FROM activity WHERE TRUE AND occurred_at <= \$2 AND recorded_at <= \$3 AND \(current_agent_key = \$4 OR \(kind = 'task_event' AND previous_agent_key = \$5\)\).*kind = 'tool_call' AND activity_action = \$6.*\(occurred_at, id, kind\) < \(\$7, \$8, \$9\).*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$10`).
		WithArgs("", sqlmock.AnyArg(), sqlmock.AnyArg(), "atlas", "atlas", "stagehand", now, eventID, "inference", 2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "kind", "occurred_at", "task_id", "run_id", "current_agent_key", "input_tokens", "output_tokens", "total_cost_micros", "pricing_basis", "currency", "activity_action"}).AddRow(nextEventID, "tool_call", now.Add(-time.Minute), taskID, runID, "atlas", int64(40), int64(10), int64(5), "official_api_price", "USD", "stagehand"))
	secondRecorder := httptest.NewRecorder()
	secondRequest := httptest.NewRequest(http.MethodGet, "/api/automation/traces?limit=1&agent_key=atlas&tool=stagehand&cursor="+page.NextCursor+"&snapshot_at="+page.SnapshotAt.Format(time.RFC3339Nano), nil)
	secondContext := echo.New().NewContext(secondRequest, secondRecorder)
	secondContext.Set("cognito_sub", "trace-root")
	setAgentHistoryPlatformWorkspace(secondContext)
	if err := GetAutomationTraces(secondContext); err != nil {
		t.Fatalf("continue trace cursor: %v", err)
	}
	var secondEnvelope struct {
		Data automationTraceResponse `json:"data"`
	}
	if err := json.Unmarshal(secondRecorder.Body.Bytes(), &secondEnvelope); err != nil {
		t.Fatalf("decode second trace page: %v", err)
	}
	if len(secondEnvelope.Data.Items) != 1 || secondEnvelope.Data.Items[0].ID != nextEventID || secondEnvelope.Data.Items[0].Tool != "stagehand" || secondEnvelope.Data.Items[0].InputTokens == nil || *secondEnvelope.Data.Items[0].InputTokens != 40 || secondEnvelope.Data.Items[0].TotalCostMicros == nil || *secondEnvelope.Data.Items[0].TotalCostMicros != 5 || secondEnvelope.Data.Items[0].CostPricingStatus != "verified_usd" || secondEnvelope.Data.CostCoverage.VerifiedUSDExecutions != 1 || secondEnvelope.Data.CostCoverage.UnpricedExecutions != 0 || !secondEnvelope.Data.SnapshotAt.Equal(page.SnapshotAt) {
		t.Fatalf("unexpected second global page: %#v", secondEnvelope.Data)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSafeAutomationTraceTaskIDOmitsSyntheticOrMissingTaskLinks(t *testing.T) {
	taskID := uuid.Must(uuid.NewV4())
	if got := safeAutomationTraceTaskID(agentHistoryItem{Kind: "gate_decision", TaskID: taskID}); got != nil {
		t.Fatalf("synthetic gate-decision task link = %v, want omitted", got)
	}
	if got := safeAutomationTraceTaskID(agentHistoryItem{Kind: "step_event", TaskID: uuid.Nil}); got != nil {
		t.Fatalf("missing task link = %v, want omitted", got)
	}
	if got := safeAutomationTraceTaskID(agentHistoryItem{Kind: "step_event", TaskID: taskID}); got == nil || *got != taskID {
		t.Fatalf("real task link = %v, want %s", got, taskID)
	}
}

func TestSafeAutomationTraceCostDistinguishesUnknownFromRecordedZero(t *testing.T) {
	zero := int64(0)
	for _, basis := range []string{"", "unpriced", "legacy", " LEGACY "} {
		if got := safeAutomationTraceCost(agentHistoryItem{Kind: "inference", PricingBasis: basis, Currency: "USD", TotalCostMicros: &zero}); got != nil {
			t.Errorf("pricing basis %q provider call cost = %v, want unknown", basis, got)
		}
	}
	if got := safeAutomationTraceCost(agentHistoryItem{Kind: "inference", PricingBasis: "official_api_price", Currency: "EUR", TotalCostMicros: &zero}); got != nil {
		t.Fatalf("non-USD provider call cost = %v, want unknown", got)
	}
	if got := safeAutomationTraceCost(agentHistoryItem{Kind: "step_event", PricingBasis: "official_api_price", Currency: "USD", TotalCostMicros: &zero}); got != nil {
		t.Fatalf("non-provider lifecycle cost = %v, want omitted", got)
	}
	if got := safeAutomationTraceCost(agentHistoryItem{Kind: "tool_call", PricingBasis: "official_api_price", Currency: "USD", TotalCostMicros: &zero}); got == nil || *got != 0 {
		t.Fatalf("priced zero-cost provider call = %v, want explicit zero", got)
	}
}

func TestGetAutomationTracesKeepsTokensAndReportsUnpricedNonUSDCost(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	eventID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	runID := uuid.Must(uuid.NewV4()).String()
	occurredAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	mock.ExpectQuery("(?s)WITH activity AS \\(.*AS pricing_basis,.*AS currency,.*FROM activity WHERE TRUE AND occurred_at <= \\$2 AND recorded_at <= \\$3.*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \\$4").
		WithArgs("", sqlmock.AnyArg(), sqlmock.AnyArg(), 2).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "kind", "occurred_at", "task_id", "run_id", "current_agent_key",
			"input_tokens", "output_tokens", "total_cost_micros", "pricing_basis", "currency",
		}).AddRow(eventID, "inference", occurredAt, taskID, runID, "atlas", int64(11), int64(3), int64(0), "official_api_price", "EUR"))

	request := httptest.NewRequest(http.MethodGet, "/api/automation/traces?limit=1", nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", "trace-root")
	setAgentHistoryPlatformWorkspace(ctx)
	if err := GetAutomationTraces(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("non-USD provider trace status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode non-USD provider trace envelope: %v", err)
	}
	var page automationTraceResponse
	if err := json.Unmarshal(response["data"], &page); err != nil {
		t.Fatalf("decode legacy provider trace page: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("unpriced provider trace count = %d, want 1", len(page.Items))
	}
	item := page.Items[0]
	if item.InputTokens == nil || *item.InputTokens != 11 || item.OutputTokens == nil || *item.OutputTokens != 3 {
		t.Fatalf("unpriced call should preserve observed tokens: %#v", item)
	}
	if item.TotalCostMicros != nil || item.CostPricingStatus != "unknown" || page.CostCoverage.Scope != "returned_page" || page.CostCoverage.UnpricedExecutions != 1 || page.CostCoverage.VerifiedUSDExecutions != 0 {
		t.Fatalf("non-USD cost must be omitted while retaining page coverage: %#v", page)
	}
	if strings.Contains(recorder.Body.String(), "total_cost_microusd") {
		t.Fatalf("non-USD provider cost must be omitted, not represented as zero: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAutomationTracesKeepsLifecycleKindAndAlwaysReturnsCursorField(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	eventID := uuid.Must(uuid.NewV4())
	occurredAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	mock.ExpectQuery(`(?s)WITH activity AS \(.*FROM activity WHERE TRUE AND occurred_at <= \$2 AND recorded_at <= \$3.*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$4`).
		WithArgs("", sqlmock.AnyArg(), sqlmock.AnyArg(), 2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "kind", "occurred_at", "event_type", "status", "current_agent_key"}).
			AddRow(eventID, "task_event", occurredAt, "status_transition", "running", "atlas"))

	request := httptest.NewRequest(http.MethodGet, "/api/automation/traces?limit=1", nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", "trace-root")
	setAgentHistoryPlatformWorkspace(ctx)
	if err := GetAutomationTraces(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("task lifecycle trace status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode task lifecycle trace envelope: %v", err)
	}
	var page struct {
		Items      []automationTraceItem `json:"items"`
		HasMore    bool                  `json:"has_more"`
		NextCursor *string               `json:"next_cursor"`
	}
	if err := json.Unmarshal(envelope.Data, &page); err != nil {
		t.Fatalf("decode task lifecycle trace page: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Kind != "task_event" || page.Items[0].EventType != "status_transition" {
		t.Fatalf("lifecycle source kind and event type must remain separate: %#v", page.Items)
	}
	if page.HasMore || page.NextCursor == nil || *page.NextCursor != "" {
		t.Fatalf("terminal page must include an empty next_cursor field: %#v", page)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAutomationTracesRedactsUnvalidatedActivityAndRejectsMalformedFilters(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	now, eventID := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Second), uuid.Must(uuid.NewV4())
	columns := []string{"id", "kind", "occurred_at", "current_agent_key", "status", "activity_action", "activity_details_json"}
	unsafeDetails := `{"credential":"bearer secret-never-return","prompt":"private reasoning"}`
	mock.ExpectQuery(`(?s)WITH activity AS \(.*FROM activity WHERE TRUE AND occurred_at <= \$2 AND recorded_at <= \$3.*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$4`).
		WithArgs("", sqlmock.AnyArg(), sqlmock.AnyArg(), 2).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(eventID, "step_activity", now, "atlas", "completed", "evidence", unsafeDetails))
	request := httptest.NewRequest(http.MethodGet, "/api/automation/traces?limit=1", nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", "trace-root")
	setAgentHistoryPlatformWorkspace(ctx)
	if err := GetAutomationTraces(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(recorder.Body.String(), "secret-never-return") || strings.Contains(strings.ToLower(recorder.Body.String()), "reasoning") || strings.Contains(strings.ToLower(recorder.Body.String()), "activity_details_json") {
		t.Fatalf("trace response exposed raw activity JSON: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "request_ref") || strings.Contains(recorder.Body.String(), "response_ref") {
		t.Fatalf("trace response exposed private object references: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "total_cost_microusd") || strings.Contains(recorder.Body.String(), "input_tokens") || strings.Contains(recorder.Body.String(), "output_tokens") {
		t.Fatalf("non-billable activity was projected as zero-cost provider usage: %s", recorder.Body.String())
	}

	for _, raw := range []string{
		"limit=101", "project_id=not-a-uuid", "step_key=bad%20key", "agent_key=bad%20key", "tool=browser.inspect", "tool=stagehand%3Bdrop", "status=unknown", "cursor=not-base64", "snapshot_at=yesterday", "limit=1&limit=2",
		"model=bad%20model%20id", "model=" + strings.Repeat("a", 129), "run_id=bad%20run%20id", "run_id=" + strings.Repeat("a", 65),
		"model=one&model=two", "run_id=one&run_id=two", "q=one&q=two", "q=" + strings.Repeat("a", 121), "q=bad%0Asearch",
		"organization_id=not-a-uuid",
	} {
		t.Run(raw, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/automation/traces?"+raw, nil)
			ctx := echo.New().NewContext(request, httptest.NewRecorder())
			if _, _, err := parseAutomationTraceFilters(ctx); err == nil {
				t.Fatalf("malformed query %q unexpectedly parsed", raw)
			}
		})
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAutomationTraceSearchIsBoundedAndPartOfCursorScope(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/automation/traces?q=worker%20activity", nil)
	ctx := echo.New().NewContext(request, httptest.NewRecorder())
	filters, _, err := parseAutomationTraceFilters(ctx)
	if err != nil {
		t.Fatalf("parse bounded trace search: %v", err)
	}
	if filters.Search != "worker activity" {
		t.Fatalf("trace search = %q, want normalized search", filters.Search)
	}
	snapshot := time.Now().UTC().Truncate(time.Microsecond)
	filters.SnapshotAt = &snapshot
	otherFilters := filters
	otherFilters.Search = "different query"
	if automationTraceCursorScope(filters) == automationTraceCursorScope(otherFilters) {
		t.Fatal("a cursor must not be reusable for a different search query")
	}
	organizationID := uuid.Must(uuid.NewV4())
	otherFilters = filters
	otherFilters.OrganizationID = &organizationID
	if automationTraceCursorScope(filters) == automationTraceCursorScope(otherFilters) {
		t.Fatal("a cursor must not be reusable for a different organization")
	}
}

func TestGetAutomationTracesFiltersExactModelAndRunIDAndBindsCursor(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	model, runID := "OpenAI/gpt-4.1", "run-123"
	now := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	snapshotAt := now.Add(time.Second)
	firstID, secondID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	columns := []string{"id", "kind", "occurred_at", "task_id", "run_id", "current_agent_key", "operation", "status", "provider", "model", "input_tokens", "output_tokens", "total_cost_micros", "summary", "activity_action"}
	firstRows := sqlmock.NewRows(columns).
		AddRow(firstID, "inference", now, taskID, runID, "atlas", "delivery.implementation", "completed", "openai", model, int64(20), int64(4), int64(1), "Inference call", "").
		AddRow(secondID, "tool_call", now.Add(-time.Second), taskID, runID, "atlas", "delivery.implementation", "completed", "openai", model, int64(10), int64(2), int64(1), "AI tool call", "stagehand")
	mock.ExpectQuery(`(?s)WITH activity AS \(.*FROM activity WHERE TRUE AND occurred_at <= \$2 AND recorded_at <= \$3 AND \(run_id = \$4 OR \(kind = 'task_event' AND previous_run_id = \$5\)\) AND model = \$6.*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$7`).
		WithArgs("", sqlmock.AnyArg(), sqlmock.AnyArg(), runID, runID, model, 2).
		WillReturnRows(firstRows)

	request := httptest.NewRequest(http.MethodGet, "/api/automation/traces?model=%20OpenAI/gpt-4.1%20&run_id=%20run-123%20&limit=1&snapshot_at="+snapshotAt.Format(time.RFC3339Nano), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", "trace-root")
	setAgentHistoryPlatformWorkspace(ctx)
	if err := GetAutomationTraces(ctx); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data automationTraceResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode trace response: %v", err)
	}
	page := envelope.Data
	if recorder.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0].ID != firstID || page.Items[0].Model != model || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("unexpected first filtered page (%d): %s", recorder.Code, recorder.Body.String())
	}

	// Cursor continuation must use the same exact model/run filters; modifying
	// either filter is rejected before another database query is issued.
	for _, query := range []string{
		"model=Other/model&run_id=" + runID,
		"model=" + model + "&run_id=other-run",
	} {
		badRecorder := httptest.NewRecorder()
		badRequest := httptest.NewRequest(http.MethodGet, "/api/automation/traces?limit=1&cursor="+page.NextCursor+"&snapshot_at="+page.SnapshotAt.Format(time.RFC3339Nano)+"&"+query, nil)
		badContext := echo.New().NewContext(badRequest, badRecorder)
		badContext.Set("cognito_sub", "trace-root")
		setAgentHistoryPlatformWorkspace(badContext)
		_ = GetAutomationTraces(badContext)
		if badRecorder.Code != http.StatusBadRequest {
			t.Fatalf("changed model/run cursor filter status = %d, want 400: %s", badRecorder.Code, badRecorder.Body.String())
		}
	}

	mock.ExpectQuery(`(?s)WITH activity AS \(.*FROM activity WHERE TRUE AND occurred_at <= \$2 AND recorded_at <= \$3 AND \(run_id = \$4 OR \(kind = 'task_event' AND previous_run_id = \$5\)\) AND model = \$6.*\(occurred_at, id, kind\) < \(\$7, \$8, \$9\).*ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT \$10`).
		WithArgs("", sqlmock.AnyArg(), sqlmock.AnyArg(), runID, runID, model, now, firstID, "inference", 2).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(secondID, "tool_call", now.Add(-time.Second), taskID, runID, "atlas", "delivery.implementation", "completed", "openai", model, int64(10), int64(2), int64(1), "AI tool call", "stagehand"))
	secondRequest := httptest.NewRequest(http.MethodGet, "/api/automation/traces?model="+model+"&run_id="+runID+"&limit=1&cursor="+page.NextCursor+"&snapshot_at="+page.SnapshotAt.Format(time.RFC3339Nano), nil)
	secondRecorder := httptest.NewRecorder()
	secondContext := echo.New().NewContext(secondRequest, secondRecorder)
	secondContext.Set("cognito_sub", "trace-root")
	setAgentHistoryPlatformWorkspace(secondContext)
	if err := GetAutomationTraces(secondContext); err != nil {
		t.Fatalf("continue filtered cursor: %v", err)
	}
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("filtered cursor continuation status = %d: %s", secondRecorder.Code, secondRecorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
