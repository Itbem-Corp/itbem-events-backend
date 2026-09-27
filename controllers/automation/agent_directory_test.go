package automation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/agentprotocol"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestGetAgentDirectoryRequiresPlatformRoot(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)

	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents", nil), recorder)
	ctx.Set("cognito_sub", "agent-directory-test")
	ctx.Set("workspace_mode", "platform")
	if err := GetAgentDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-root status = %d, want %d: %s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestGetAgentDirectoryAllowsOperationalRoot(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelOperational}, nil
	}})
	t.Cleanup(restore)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents", nil), recorder)
	ctx.Set("cognito_sub", "agent-directory-test")
	ctx.Set("workspace_mode", "platform")
	if err := GetAgentDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("operational root must pass authorization and reach DB readiness check, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGetAgentDirectoryRequiresPlatformWorkspace(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	organizationRecorder := httptest.NewRecorder()
	organizationContext := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents", nil), organizationRecorder)
	organizationContext.Set("cognito_sub", "agent-directory-test")
	organizationContext.Set("workspace_mode", "organization")
	organizationContext.Set("organization_id", uuid.Must(uuid.NewV4()))
	if err := GetAgentDirectory(organizationContext); err != nil {
		t.Fatal(err)
	}
	if organizationRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("organization workspace should reach DB readiness after resolving the scoped surface, got %d: %s", organizationRecorder.Code, organizationRecorder.Body.String())
	}

	tenantRecorder := httptest.NewRecorder()
	tenantContext := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents", nil), tenantRecorder)
	tenantContext.Set("cognito_sub", "agent-directory-test")
	tenantContext.Set("tenant_code", "caffetton")
	tenantContext.Set("workspace_mode", "platform")
	if err := GetAgentDirectory(tenantContext); err != nil {
		t.Fatal(err)
	}
	if tenantRecorder.Code != http.StatusForbidden {
		t.Fatalf("tenant surface status = %d, want %d: %s", tenantRecorder.Code, http.StatusForbidden, tenantRecorder.Body.String())
	}

	platformRecorder := httptest.NewRecorder()
	platformContext := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents", nil), platformRecorder)
	platformContext.Set("cognito_sub", "agent-directory-test")
	platformContext.Set("workspace_mode", "platform")
	if err := GetAgentDirectory(platformContext); err != nil {
		t.Fatal(err)
	}
	if platformRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("platform workspace status = %d, want DB readiness check %d: %s", platformRecorder.Code, http.StatusServiceUnavailable, platformRecorder.Body.String())
	}
}

func TestAgentIdentityIsOpaqueAndRecoveredLedgerKeepsOriginalWorker(t *testing.T) {
	if !isOpaqueMachineID("a69b7f51-58b9-4f0e-aef3-1fbc23f79827") || isOpaqueMachineID("local-workstation") {
		t.Fatal("machine identity must be an opaque UUID, not a host label")
	}
	current := models.AutomationTask{WorkerID: "a69b7f51-58b9-4f0e-aef3-1fbc23f79826", AgentKey: "generalist", MachineID: "a69b7f51-58b9-4f0e-aef3-1fbc23f79827"}
	origin := &automationagent.AgentIdentity{WorkerID: "b69b7f51-58b9-4f0e-aef3-1fbc23f79826", AgentKey: "generalist", MachineID: "b69b7f51-58b9-4f0e-aef3-1fbc23f79827"}
	identity, err := ledgerAgentIdentity(&current, callbackRequest{
		WorkerID: current.WorkerID, AgentKey: current.AgentKey, MachineID: current.MachineID, ExecutionIdentity: origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.WorkerID != origin.WorkerID || identity.MachineID != origin.MachineID {
		t.Fatalf("recovered ledger identity = %#v, want original worker %#v", identity, origin)
	}
	if _, err := ledgerAgentIdentity(&current, callbackRequest{WorkerID: "c69b7f51-58b9-4f0e-aef3-1fbc23f79826", AgentKey: "generalist"}); err == nil {
		t.Fatal("terminal callback from a different current worker must not complete this run")
	}
}

func TestGetAgentDirectoryV1SnapshotIsBoundedAndDoesNotExposePrivateFields(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	now := time.Now().UTC()
	profileID, workerUUID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	agentInstanceID := uuid.Must(uuid.NewV4())
	taskID, clientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	projectID, itemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	epicID := uuid.Must(uuid.NewV4())
	oldest := now.Add(-7 * time.Minute)
	profileRows := sqlmock.NewRows([]string{"id", "agent_key", "name", "specialty", "description", "operations_json", "capabilities_json", "active", "created_at", "updated_at"}).
		AddRow(profileID, "generalist", "Generalist", "general", "Safe description", `["delivery.plan","not.an.operation"]`, `["model_inference","delivery_orchestration"]`, true, now.Add(-time.Hour), now.Add(-time.Hour))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "automation_agent_profiles" ORDER BY agent_key ASC`)).WillReturnRows(profileRows)
	legacyWorkerUUID := uuid.Must(uuid.NewV4())
	heartbeatRows := sqlmock.NewRows([]string{"id", "worker_id", "agent_key", "machine_id", "agent_instance_id", "provider", "model", "concurrency", "draining", "capabilities_json", "protocols_json", "workspace_readiness", "started_at", "last_seen_at", "created_at", "updated_at"}).
		AddRow(uuid.Must(uuid.NewV4()), workerUUID.String(), "generalist", "a69b7f51-58b9-4f0e-aef3-1fbc23f79827", agentInstanceID, "openrouter", "cheap-test-model", 2, false, `[]`, `["delivery.plan_steps.v1"]`, `[{"hostname":"do-not-return.example","prompt":"PRIVATE_PROMPT","secret":"PRIVATE_TOKEN"}]`, now.Add(-time.Hour), now.Add(-10*time.Second), now.Add(-time.Hour), now.Add(-10*time.Second)).
		AddRow(uuid.Must(uuid.NewV4()), legacyWorkerUUID.String(), "generalist", "legacy-local-hostname", nil, "openrouter", "historical-model", 1, false, `[]`, `[]`, `[]`, now.Add(-time.Hour), now.Add(-2*time.Minute), now.Add(-time.Hour), now.Add(-2*time.Minute))
	activeRows := sqlmock.NewRows([]string{"task_id", "run_id", "operation", "status", "worker_id", "agent_key", "step_key", "created_at", "started_at", "client_id", "client_name", "project_id", "project_name", "work_item_id", "work_item_title", "epic_id", "epic_title"}).
		AddRow(taskID, "run-1", "delivery.plan", "running", workerUUID.String(), "generalist", "planning", now.Add(-5*time.Minute), now.Add(-4*time.Minute), clientID, "ITBEM AKIAIOSFODNN7EXAMPLE", projectID, "Agent Studio token=private-project-token", itemID, "Build api_key=private-work-item-token", nil, nil).
		AddRow(uuid.Must(uuid.NewV4()), "run-2", "delivery.plan", "running", workerUUID.String(), "generalist", "implement", now.Add(-4*time.Minute), nil, clientID, "ITBEM", projectID, "Agent Studio", uuid.Must(uuid.NewV4()), "Task with active epic", epicID, "Epic token=private-epic-token").
		// A deleted membership is deliberately projected as no epic by the
		// current-membership lateral join; it must not leak the former epic.
		AddRow(uuid.Must(uuid.NewV4()), "run-3", "delivery.plan", "running", workerUUID.String(), "generalist", "verify", now.Add(-3*time.Minute), now.Add(-2*time.Minute), clientID, "ITBEM", projectID, "Agent Studio", uuid.Must(uuid.NewV4()), "Task with removed epic", nil, nil)
	mock.ExpectQuery(`(?s)SELECT t\.id AS task_id.*current_claim\.occurred_at AS started_at.*FROM automation_tasks AS t LEFT JOIN delivery_work_items AS w.*LEFT JOIN LATERAL .*event\.automation_task_id = t\.id AND event\.run_id = t\.run_id.*event\.worker_id = t\.worker_id AND event\.status = 'running'.*event\.event_type IN \('claimed', 'lease_reclaimed'\).*ORDER BY event\.sequence DESC.*LIMIT 1.*LEFT JOIN LATERAL .*epic\.project_id = membership\.project_id.*membership\.project_id = w\.project_id.*membership\.deleted_at IS NULL.*LIMIT 1.*WHERE t\.status IN \(\$1,\$2\) ORDER BY t\.created_at ASC`).
		WithArgs("running", "cancel_requested").WillReturnRows(activeRows)
	mock.ExpectQuery(`SELECT \* FROM "automation_agent_heartbeats" WHERE last_seen_at >= \$1 ORDER BY agent_key ASC, last_seen_at DESC`).WillReturnRows(heartbeatRows)
	queuedRows := sqlmock.NewRows([]string{"operation", "count", "oldest"}).AddRow("delivery.plan", 4, oldest)
	mock.ExpectQuery(`SELECT t\.operation, COUNT\(\*\) AS count, MIN\(t\.created_at\) AS oldest FROM .* WHERE .* GROUP BY .*`).
		WithArgs("queued").WillReturnRows(queuedRows)
	ledgerRows := sqlmock.NewRows([]string{"agent_key", "run_count", "spend_micros"}).
		AddRow("generalist", 1, int64(17)).
		AddRow("", 1, int64(3))
	mock.ExpectQuery(`SELECT agent_key, COUNT\(DISTINCT \(automation_task_id, run_id\)\).*FROM \(\s*SELECT agent_key, automation_task_id, run_id, total_cost_micros FROM automation_executions.*UNION ALL.*automation_tool_executions.*GROUP BY agent_key`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(ledgerRows)

	rootLevel := models.RootLevelPrimary
	restorePrimary := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: rootLevel}, nil
	}})
	t.Cleanup(restorePrimary)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents", nil), recorder)
	ctx.Set("cognito_sub", "agent-directory-test")
	ctx.Set("workspace_mode", "platform")
	if err := GetAgentDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s; pending SQL expectations: %v", recorder.Code, recorder.Body.String(), mock.ExpectationsWereMet())
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data, &body); err != nil {
		t.Fatalf("decode v1 data: %v", err)
	}
	var snapshot agentDirectoryResponse
	if err := json.Unmarshal(envelope.Data, &snapshot); err != nil {
		t.Fatalf("decode v1 snapshot: %v", err)
	}
	if snapshot.SchemaVersion != 1 || snapshot.Summary.ProfileCount != 1 || snapshot.Summary.LiveInstances != 1 || snapshot.Summary.ActiveRuns != 3 || snapshot.Summary.AvailableSlots != 0 || snapshot.Summary.QueuedTasks != 4 || snapshot.Summary.Spend30dMicros != 20 {
		t.Fatalf("unexpected summary: %#v", snapshot.Summary)
	}
	if len(snapshot.Agents) != 1 || snapshot.Agents[0].Status != "working" || snapshot.Agents[0].ActiveRunCount != 3 || snapshot.Agents[0].TotalRuns30d != 1 || snapshot.Agents[0].Spend30dMicros != 17 {
		t.Fatalf("unexpected agent: %#v", snapshot.Agents)
	}
	if !snapshot.Agents[0].Active || len(snapshot.Agents[0].Capabilities) != 2 || snapshot.Agents[0].Capabilities[0] != "delivery_orchestration" || snapshot.Agents[0].Capabilities[1] != "model_inference" {
		t.Fatalf("declared capabilities must remain distinct from routing operations: %#v", snapshot.Agents[0])
	}
	if len(snapshot.Agents[0].Operations) != 1 || snapshot.Agents[0].Operations[0] != "delivery.plan" {
		t.Fatalf("operations must only include supported routing operations: %#v", snapshot.Agents[0].Operations)
	}
	if len(snapshot.Agents[0].Instances) != 2 || len(snapshot.Agents[0].Instances[0].ActiveRuns) != 3 {
		t.Fatalf("active run must be linked to its actual instance: %#v", snapshot.Agents[0])
	}
	if snapshot.Agents[0].Instances[0].AgentInstanceID == nil || *snapshot.Agents[0].Instances[0].AgentInstanceID != agentInstanceID {
		t.Fatalf("agent directory omitted registered instance attribution: %#v", snapshot.Agents[0].Instances[0])
	}
	if got := snapshot.Agents[0].Instances[0].Protocols; len(got) != 1 || got[0] != agentprotocol.ProtocolDeliveryPlanStepsV1 {
		t.Fatalf("directory omitted allow-listed runtime protocol metadata: %#v", snapshot.Agents[0].Instances[0])
	}
	if got := snapshot.Agents[0].Instances[1].Protocols; len(got) != 0 {
		t.Fatalf("legacy worker must advertise no versioned protocols: %#v", got)
	}
	run := snapshot.Agents[0].Instances[0].ActiveRuns[0]
	if strings.Contains(run.ClientName, "AKIAIOSFODNN7EXAMPLE") || strings.Contains(run.ProjectName, "private-project-token") || strings.Contains(run.WorkItemTitle, "private-work-item-token") {
		t.Fatalf("active run labels were not redacted: %#v", run)
	}
	if run.EpicID != "" || run.EpicTitle != "" {
		t.Fatalf("run without a current epic must retain empty, backward-compatible epic fields: %#v", run)
	}
	runJSON, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(runJSON), `"epic_id"`) || strings.Contains(string(runJSON), `"epic_title"`) {
		t.Fatalf("runs without an epic must omit optional epic fields: %s", runJSON)
	}
	activeEpicRun := snapshot.Agents[0].Instances[0].ActiveRuns[1]
	if activeEpicRun.EpicID != epicID.String() || activeEpicRun.EpicTitle == "" || strings.Contains(activeEpicRun.EpicTitle, "private-epic-token") {
		t.Fatalf("current epic should be included and sanitized: %#v", activeEpicRun)
	}
	removedEpicRun := snapshot.Agents[0].Instances[0].ActiveRuns[2]
	if removedEpicRun.EpicID != "" || removedEpicRun.EpicTitle != "" {
		t.Fatalf("run with removed membership must not expose former epic: %#v", removedEpicRun)
	}
	if run.StartedAt == nil || !run.StartedAt.Equal(now.Add(-4*time.Minute)) {
		t.Fatalf("active run should use the authoritative current-run claim timestamp: %#v", run)
	}
	legacyClaimRun := snapshot.Agents[0].Instances[0].ActiveRuns[1]
	if legacyClaimRun.StartedAt != nil {
		t.Fatalf("active run without a matching claim event must not fall back to task created_at: %#v", legacyClaimRun)
	}
	if snapshot.Agents[0].Instances[0].MachineID != "a69b7f51-58b9-4f0e-aef3-1fbc23f79827" || snapshot.Agents[0].Instances[1].MachineID != "" {
		t.Fatalf("machine IDs should be canonical UUIDs or omitted: %#v", snapshot.Agents[0].Instances)
	}
	var queueLanes []agentDirectoryQueueLane
	if err := json.Unmarshal(body["queue_lanes"], &queueLanes); err != nil {
		t.Fatal(err)
	}
	if len(queueLanes) == 0 || queueLanes[0].Operation == "" {
		t.Fatalf("shared queue lanes missing: %#v", queueLanes)
	}
	foundQueuedPlan := false
	for _, lane := range queueLanes {
		if lane.Operation == "delivery.plan" {
			foundQueuedPlan = lane.QueuedTasks == 4 && lane.OldestQueuedAt != nil && lane.OldestQueuedAt.Equal(oldest)
		}
	}
	if !foundQueuedPlan {
		t.Fatalf("shared queue lane did not report expected wait: %#v", queueLanes)
	}
	serialized := string(envelope.Data)
	for _, forbidden := range []string{"do-not-return.example", "PRIVATE_PROMPT", "PRIVATE_TOKEN", "AKIAIOSFODNN7EXAMPLE", "private-project-token", "private-work-item-token", "private-epic-token", "legacy-local-hostname", "s3://", "workspace_readiness", "input_ref", "output_ref", "usage_json"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("agent directory exposed private data %q: %s", forbidden, serialized)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizeAgentDirectoryClientScopeUsesVisibleProjectMembership(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID, childClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	visibleProjectID, hiddenProjectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "agent-directory-scoped-member"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID, childClientID)
	mock.ExpectQuery(`(?s)SELECT delivery_projects\.id AS project_id, delivery_project_members\.role, delivery_project_members\.permissions FROM "delivery_projects" JOIN delivery_project_members ON .*WHERE delivery_projects\.deleted_at IS NULL AND delivery_projects\.client_id IN \(\$2,\$3\) ORDER BY delivery_projects\.id`).
		WithArgs(subject, organizationID, childClientID).
		WillReturnRows(sqlmock.NewRows([]string{"project_id", "role", "permissions"}).
			AddRow(visibleProjectID, "viewer", `[]`).
			AddRow(hiddenProjectID, "developer", `[]`))
	clientID := organizationID
	scope := agentDirectoryScope{ClientID: &clientID, ClientIDs: []uuid.UUID{organizationID, childClientID}}
	request := httptest.NewRequest(http.MethodGet, "/api/automation/agents?client_id="+organizationID.String(), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", subject)
	setAgentHistoryOrganizationWorkspace(ctx, organizationID)
	if err := authorizeAgentDirectoryScope(ctx, db, &models.User{CognitoSub: subject}, "organization", &scope); err != nil {
		t.Fatalf("authorize scoped directory: %v (%s)", err, recorder.Body.String())
	}
	if !scope.AuthorizedProjectsScoped || len(scope.AuthorizedProjectIDs) != 1 || scope.AuthorizedProjectIDs[0] != visibleProjectID {
		t.Fatalf("client directory scope did not intersect project role access: %#v", scope)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentDirectoryUsageRestrictsClientCostsToAuthorizedProjects(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	cutoff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	clientID, childClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	visibleProjectID := uuid.Must(uuid.NewV4())
	scope := agentDirectoryScope{
		ClientID: &clientID, ClientIDs: []uuid.UUID{clientID, childClientID},
		AuthorizedProjectIDs: []uuid.UUID{visibleProjectID}, AuthorizedProjectsScoped: true,
	}
	mock.ExpectQuery("(?s)SELECT ledger\\.agent_key, COUNT\\(DISTINCT \\(ledger\\.automation_task_id, ledger\\.run_id\\)\\).*FROM automation_executions WHERE completed_at >= \\$1.*automation_tool_executions WHERE completed_at >= \\$2.*JOIN delivery_projects AS p ON p\\.id = w\\.project_id AND p\\.deleted_at IS NULL.*WHERE TRUE AND p\\.client_id IN \\(\\$3,\\$4\\) AND p\\.id IN \\(\\$5\\) GROUP BY ledger\\.agent_key").
		WithArgs(cutoff, cutoff, clientID, childClientID, visibleProjectID).
		WillReturnRows(sqlmock.NewRows([]string{"agent_key", "run_count", "spend_micros"}).AddRow("generalist", 1, int64(19)))

	costs, runs, total, err := agentDirectoryUsage(context.Background(), db, cutoff, scope)
	if err != nil {
		t.Fatalf("load authorized client-level agent usage: %v", err)
	}
	if total != 19 || costs["generalist"] != 19 || runs["generalist"] != 1 {
		t.Fatalf("authorized project usage = costs %#v, runs %#v, total %d", costs, runs, total)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentDirectoryUsageReturnsNoClientCostsWithoutVisibleProjects(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	cutoff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	clientID := uuid.Must(uuid.NewV4())
	scope := agentDirectoryScope{
		ClientID: &clientID, ClientIDs: []uuid.UUID{clientID},
		AuthorizedProjectsScoped: true,
	}
	mock.ExpectQuery("(?s)SELECT ledger\\.agent_key, COUNT\\(DISTINCT \\(ledger\\.automation_task_id, ledger\\.run_id\\)\\).*WHERE TRUE AND p\\.client_id IN \\(\\$3\\) AND 1 = 0 GROUP BY ledger\\.agent_key").
		WithArgs(cutoff, cutoff, clientID).
		WillReturnRows(sqlmock.NewRows([]string{"agent_key", "run_count", "spend_micros"}))

	costs, runs, total, err := agentDirectoryUsage(context.Background(), db, cutoff, scope)
	if err != nil {
		t.Fatalf("load empty authorized client-level usage: %v", err)
	}
	if total != 0 || len(costs) != 0 || len(runs) != 0 {
		t.Fatalf("unauthorized projects produced usage: costs %#v, runs %#v, total %d", costs, runs, total)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestParseAgentDirectoryScopeRejectsProjectOutsideClientSubtree(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	clientID, childClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	projectID, otherClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expectAgentHistoryOrganizationClients(mock, clientID, clientID, childClientID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, otherClientID))
	request := httptest.NewRequest(http.MethodGet, "/api/automation/agents?client_id="+clientID.String()+"&project_id="+projectID.String(), nil)
	ctx := echo.New().NewContext(request, httptest.NewRecorder())
	scope, status, err := parseAgentDirectoryScope(ctx, db)
	if err == nil || status != http.StatusNotFound || scope.ClientID != nil || scope.ProjectID != nil {
		t.Fatalf("incoherent client/project directory scope = %#v, status %d, err %v", scope, status, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildAgentDirectoryScopesActivityQueueAndSpendAggregates(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	now := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC)
	projectID, clientID, taskID, workerID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	profileID := uuid.Must(uuid.NewV4())
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "automation_agent_profiles" ORDER BY agent_key ASC`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "agent_key", "name", "specialty", "description", "operations_json", "capabilities_json", "active", "created_at", "updated_at"}).
			AddRow(profileID, "generalist", "Generalist", "engineering", "Agent", `[]`, `[]`, true, now, now))
	activeRows := sqlmock.NewRows([]string{"task_id", "run_id", "operation", "status", "worker_id", "agent_key", "step_key", "created_at", "started_at", "client_id", "client_name", "project_id", "project_name", "work_item_id", "work_item_title", "epic_id", "epic_title"}).
		AddRow(taskID, "scoped-run", "delivery.implementation", "running", workerID.String(), "generalist", "implement", now.Add(-time.Minute), now.Add(-30*time.Second), clientID, "Client", projectID, "Project", uuid.Must(uuid.NewV4()), "Work item", nil, nil)
	mock.ExpectQuery(`(?s)SELECT t\.id AS task_id.*FROM automation_tasks AS t.*WHERE t\.status IN \(\$1,\$2\) AND p\.id = \$3 ORDER BY t\.created_at ASC`).
		WithArgs("running", "cancel_requested", projectID).
		WillReturnRows(activeRows)
	heartbeatID := uuid.Must(uuid.NewV4())
	mock.ExpectQuery(`SELECT \* FROM "automation_agent_heartbeats" WHERE last_seen_at >= \$1 AND worker_id IN \(\$2\) ORDER BY agent_key ASC, last_seen_at DESC`).
		WithArgs(sqlmock.AnyArg(), workerID.String()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "worker_id", "agent_key", "machine_id", "agent_instance_id", "provider", "model", "concurrency", "draining", "capabilities_json", "workspace_readiness", "started_at", "last_seen_at", "created_at", "updated_at"}).
			AddRow(heartbeatID, workerID.String(), "generalist", uuid.Must(uuid.NewV4()).String(), nil, "openrouter", "scoped-model", 2, false, `[]`, `[]`, now.Add(-time.Hour), now.Add(-time.Second), now, now))
	queueRows := sqlmock.NewRows([]string{"operation", "count", "oldest"}).AddRow("delivery.implementation", 2, now.Add(-5*time.Minute))
	mock.ExpectQuery(`(?s)SELECT t\.operation, COUNT\(\*\) AS count, MIN\(t\.created_at\) AS oldest FROM .*delivery_work_items.*delivery_projects.*WHERE t\.status = \$1 AND p\.id = \$2 GROUP BY .*`).
		WithArgs("queued", projectID).WillReturnRows(queueRows)
	mock.ExpectQuery(`(?s)SELECT ledger\.agent_key, COUNT\(DISTINCT \(ledger\.automation_task_id, ledger\.run_id\)\).*JOIN automation_tasks AS t.*JOIN delivery_work_items AS w.*JOIN delivery_projects AS p.*WHERE TRUE AND p\.id = \$3 GROUP BY ledger\.agent_key`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), projectID).
		WillReturnRows(sqlmock.NewRows([]string{"agent_key", "run_count", "spend_micros"}).AddRow("generalist", 1, int64(19)))
	projectScope := agentDirectoryScope{ProjectID: &projectID}
	response, err := buildAgentDirectory(context.Background(), db, now, projectScope)
	if err != nil {
		t.Fatalf("build project-scoped directory: %v", err)
	}
	if response.Summary.ActiveRuns != 1 || response.Summary.QueuedTasks != 2 || response.Summary.Spend30dMicros != 19 || response.Summary.LiveInstances != 1 || response.Summary.AvailabilityKnown || response.Summary.AvailableSlots != 0 {
		t.Fatalf("directory aggregates escaped scope or claimed global availability: %#v", response.Summary)
	}
	if len(response.Agents) != 1 || response.Agents[0].ActiveRunCount != 1 || response.Agents[0].Spend30dMicros != 19 || response.Agents[0].Status != "working" {
		t.Fatalf("project-scoped agent projection is incorrect: %#v", response.Agents)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
