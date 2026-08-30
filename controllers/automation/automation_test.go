package automation

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"events-stocks/models"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestGitHubReviewWebhookAdmissionIsExplicitAndSignatureBound(t *testing.T) {
	cfg := &models.Config{GitHubReviewWebhookSecret: "webhook-secret", GitHubReviewRepositories: "itbem/backend, ITBEM/dashboard"}
	if !githubReviewWebhookConfigured(cfg) || !githubReviewRepositoryAllowed(cfg.GitHubReviewRepositories, "itbem/dashboard") || githubReviewRepositoryAllowed(cfg.GitHubReviewRepositories, "other/repo") {
		t.Fatal("review webhook must require an explicit normalized repository allow-list")
	}
	if !githubReviewRepositoryAllowed("itbem/*", "itbem/new-repository") || githubReviewRepositoryAllowed("itbem/*", "other/repository") {
		t.Fatal("organization wildcard must remain scoped to its explicitly allowed owner")
	}
	body := []byte(`{"action":"synchronize","number":42}`)
	mac := hmac.New(sha256.New, []byte(cfg.GitHubReviewWebhookSecret))
	_, _ = mac.Write(body)
	signature := "sha256=" + fmt.Sprintf("%x", mac.Sum(nil))
	if !validGitHubWebhookSignature(body, signature, cfg.GitHubReviewWebhookSecret) || validGitHubWebhookSignature(append(body, 'x'), signature, cfg.GitHubReviewWebhookSecret) {
		t.Fatal("GitHub webhook signature must bind the exact raw body")
	}
	if !githubReviewActionAllowed("synchronize") || githubReviewActionAllowed("closed") {
		t.Fatal("only safe active pull-request actions may enqueue a new review")
	}
	var decoded githubPullRequestWebhook
	decoder := json.NewDecoder(strings.NewReader(`{"action":"synchronize"} {}`))
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		// The handler must reject this condition; this assertion proves the
		// decoder sees the appended second JSON value rather than ignoring it.
	} else {
		t.Fatal("concatenated GitHub webhook JSON must remain detectable")
	}
}

func TestSupersedeQueuedGitHubReviewsTargetsOnlyOlderQueuedHeadsForTheSamePR(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	replacement := uuid.Must(uuid.NewV4())
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "automation_tasks"`).
		WithArgs(sqlmock.AnyArg(), 0, sqlmock.AnyArg(), "Superseded by a newer pull-request commit before review began", sqlmock.AnyArg(), "cancelled", sqlmock.AnyArg(), "code.review", "github-app-review", "queued", "github-pr:itbem/backend:42:%", replacement).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := gormDB.Transaction(func(tx *gorm.DB) error {
		return supersedeQueuedGitHubReviews(tx, "itbem/backend:42", replacement, time.Now().UTC())
	}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if err := supersedeQueuedGitHubReviews(nil, "itbem/backend:42", replacement, time.Now().UTC()); err == nil {
		t.Fatal("supersession must reject an absent transaction")
	}
}

func TestRetryCodeReviewIsNarrowAndPreservesTheFrozenInputBoundary(t *testing.T) {
	failed := &models.AutomationTask{RequestedBy: "github-app-review", Operation: "code.review", Status: "failed", InputRef: "s3://itbem-ai-inputs-local/automation/inputs/original/input.json"}
	if !retryableCodeReviewTask(failed) {
		t.Fatal("a failed frozen code review must be retryable")
	}
	for _, task := range []*models.AutomationTask{
		{Operation: "code.review", Status: "completed", InputRef: failed.InputRef},
		{Operation: "ai.chat", Status: "failed", InputRef: failed.InputRef},
		{Operation: "code.review", Status: "failed"},
	} {
		if retryableCodeReviewTask(task) {
			t.Fatalf("unexpected retry eligibility: %#v", task)
		}
	}
}

func TestAutomationReviewIngressStatusReportsOnlySafeReadiness(t *testing.T) {
	t.Setenv("ITBEM_GITHUB_APP_ID", "")
	t.Setenv("ITBEM_GITHUB_INSTALLATION_ID", "")
	t.Setenv("ITBEM_GITHUB_APP_PRIVATE_KEY", "")
	status := automationReviewIngressStatus(&models.Config{GitHubReviewWebhookSecret: "secret", GitHubReviewRepositories: "itbem/backend"}, 1)
	if !status.Enabled || status.GitHubAppConfigured || !status.WorkerAvailable || status.Ready || status.AllowedRepositoryCount != 1 {
		t.Fatalf("incomplete review ingress must be visible without claiming ready: %#v", status)
	}
	status = automationReviewIngressStatus(&models.Config{}, 0)
	if status.Enabled || status.WorkerAvailable || status.Ready || status.AllowedRepositoryCount != 0 {
		t.Fatalf("disabled ingress status leaked a readiness claim: %#v", status)
	}
}

func TestValidCallbackSecretSupportsRotation(t *testing.T) {
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "current")
	t.Setenv("AUTOMATION_CALLBACK_SECRET_PREVIOUS", "previous")
	if !validCallbackSecret("current") || !validCallbackSecret("previous") {
		t.Fatal("expected active and previous automation secrets to be valid")
	}
	if validCallbackSecret("wrong") || validCallbackSecret("") {
		t.Fatal("unexpected automation callback secret accepted")
	}
	if err := os.Unsetenv("AUTOMATION_CALLBACK_SECRET"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("AUTOMATION_CALLBACK_SECRET_PREVIOUS"); err != nil {
		t.Fatal(err)
	}
	if validCallbackSecret("current") {
		t.Fatal("empty configuration must fail closed")
	}
}

func TestValidPublicationPRURLBindsTheApprovedRepository(t *testing.T) {
	approved := "itbem-corp/itbem-events-backend"
	for _, value := range []string{
		"https://github.com/itbem-corp/itbem-events-backend/pull/34",
		"https://github.com/ITBEM-CORP/ITBEM-EVENTS-BACKEND/pull/34",
	} {
		if !validPublicationPRURL(value, approved) {
			t.Fatalf("expected approved repository PR accepted: %q", value)
		}
	}
	for _, value := range []string{
		"https://github.com/itbem-corp/other-repository/pull/34",
		"https://github.com/itbem-corp/itbem-events-backend/pulls/34",
		"https://github.com/itbem-corp/itbem-events-backend/pull/0",
		"https://github.com/itbem-corp/itbem-events-backend/pull/34?redirect=1",
	} {
		if validPublicationPRURL(value, approved) {
			t.Fatalf("expected unrelated or non-canonical PR rejected: %q", value)
		}
	}
}

func TestWorkerWorkspaceReadinessRejectsImpossibleOrUnboundedStates(t *testing.T) {
	valid := []automationWorkspaceHealth{{
		ID: "eventiapp-dashboard", Ready: true, QAReady: true, VisualQAReady: true,
		PublicationReady: true, ValidationCommandCount: 2, QACommandCount: 1,
	}}
	if err := validateWorkerWorkspaceReadiness(valid); err != nil {
		t.Fatalf("expected safe readiness accepted: %v", err)
	}
	if err := validateWorkerWorkspaceReadiness([]automationWorkspaceHealth{{ID: "stagehand-only", Ready: true, VisualQAReady: true}}); err != nil {
		t.Fatalf("a visual browser harness can be ready without traditional QA commands: %v", err)
	}
	for _, invalid := range [][]automationWorkspaceHealth{
		{{ID: "../unsafe", Ready: true}},
		{{ID: "same", Ready: true}, {ID: "same", Ready: true}},
		{{ID: "qa-without-workspace", QAReady: true}},
		{{ID: "visual-without-workspace", VisualQAReady: true}},
		{{ID: "too-many-commands", Ready: true, ValidationCommandCount: 65}},
	} {
		if err := validateWorkerWorkspaceReadiness(invalid); err == nil {
			t.Fatalf("expected invalid readiness rejected: %#v", invalid)
		}
	}
}

func TestNormalizeWorkerRoleLaneKeepsLegacyVisibleAndRejectsCrossRoleIdentity(t *testing.T) {
	if role, lane, err := normalizeWorkerRoleLane("", ""); err != nil || role != "" || lane != "" {
		t.Fatalf("legacy combined worker rejected: %q %q %v", role, lane, err)
	}
	if role, lane, err := normalizeWorkerRoleLane(" reviewer ", " review "); err != nil || role != "reviewer" || lane != "review" {
		t.Fatalf("review worker rejected: %q %q %v", role, lane, err)
	}
	for _, identity := range [][2]string{{"reviewer", "release"}, {"admin", "production"}, {"principal_engineer", ""}, {"", "qa"}} {
		if _, _, err := normalizeWorkerRoleLane(identity[0], identity[1]); err == nil {
			t.Fatalf("invalid worker identity accepted: %#v", identity)
		}
	}
}

func TestValidWorkerProviderAllowsProviderlessReleaseOnly(t *testing.T) {
	if !validWorkerProvider("release_manager", "release", "", "") {
		t.Fatal("providerless deterministic release worker was rejected")
	}
	if validWorkerProvider("release_manager", "release", "minimax", "MiniMax-M3") {
		t.Fatal("release worker retained an unnecessary model credential")
	}
	if validWorkerProvider("reviewer", "review", "", "") {
		t.Fatal("review worker was allowed without a model provider")
	}
	if !validWorkerProvider("reviewer", "review", "minimax", "MiniMax-M3") {
		t.Fatal("configured review provider was rejected")
	}
}

func TestRecentCostLedgerSelectionIncludesEveryBillableComponentWithoutPrivateReferences(t *testing.T) {
	for _, column := range []string{
		"execution.input_cost_micros",
		"execution.output_cost_micros",
		"execution.cached_cost_micros",
		"execution.cache_write_cost_micros",
		"execution.pricing_basis",
	} {
		if !strings.Contains(automationCostRecentExecutionSelect, column) {
			t.Fatalf("recent cost selection omitted %s", column)
		}
	}
	for _, privateColumn := range []string{"execution.request_ref", "execution.response_ref", "task.input_ref"} {
		if strings.Contains(automationCostRecentExecutionSelect, privateColumn) {
			t.Fatalf("recent cost selection leaked private reference %s", privateColumn)
		}
	}
}

func TestCostExecutionScanIgnoresDerivedProviderOutcome(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT execution.id, execution.automation_task_id, execution.completed_at FROM automation_executions AS execution")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id", "completed_at"}))

	var executions []automationCostExecution
	if err := db.Table("automation_executions AS execution").
		Select("execution.id, execution.automation_task_id, execution.completed_at").
		Scan(&executions).Error; err != nil {
		t.Fatalf("cost summary scan must ignore the trace-only provider outcome: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAutomationWorkerLastSeenAllowsNoHeartbeats(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT MAX(last_seen_at) FROM \"automation_agent_heartbeats\"")).
		WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(nil))

	lastSeen, err := automationWorkerLastSeen(db.Table("automation_agent_heartbeats"))
	if err != nil {
		t.Fatalf("empty heartbeat table must be a valid health state: %v", err)
	}
	if lastSeen != nil {
		t.Fatalf("empty heartbeat table returned an unexpected timestamp: %v", lastSeen)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestToolExecutionLedgerCostsOnlyUploadedStagehandReport(t *testing.T) {
	taskID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	task := &models.AutomationTask{ID: taskID, DeliveryWorkItemID: &workItemID, Operation: "delivery.qa"}
	reference := "s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/01-dashboard-semantic-qa.json"
	artifacts := []callbackArtifact{{Name: "01-dashboard-semantic-qa.json", Reference: reference, ContentType: "application/json", SizeBytes: 120, SHA256: strings.Repeat("a", 64)}}
	usage := json.RawMessage(`{"input_tokens":120,"output_tokens":40,"cached_input_tokens":10,"total_tokens":160}`)
	rows, err := buildToolExecutionLedger(nil, task, uuid.Must(uuid.NewV4()).String(), "completed", []callbackToolExecution{{Tool: "stagehand", CallKey: "semantic-assessment", StepKey: "qa.semantic_browser", Provider: "minimax", Model: "MiniMax-M3", Usage: usage, RequestRef: reference, ResponseRef: reference}}, artifacts, time.Now().UTC())
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected one costed Stagehand row: %#v / %v", rows, err)
	}
	if rows[0].CallKey != "semantic-assessment" || rows[0].InputTokens != 120 || rows[0].OutputTokens != 40 || rows[0].CachedInputTokens != 10 || rows[0].TotalCostMicros <= 0 || rows[0].RequestRef != reference || rows[0].ResponseRef != reference {
		t.Fatalf("Stagehand row lost accounting or audit linkage: %#v", rows[0])
	}
	_, err = buildToolExecutionLedger(nil, task, uuid.Must(uuid.NewV4()).String(), "completed", []callbackToolExecution{{Tool: "stagehand", StepKey: "qa.semantic_browser", Provider: "minimax", Model: "MiniMax-M3", Usage: usage, RequestRef: reference, ResponseRef: "s3://arbitrary/report.json"}}, artifacts, time.Now().UTC())
	if err == nil {
		t.Fatal("expected arbitrary tool response reference to be rejected")
	}
}

func TestToolExecutionLedgerAcceptsDistinctCallsAndRejectsDuplicates(t *testing.T) {
	taskID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	task := &models.AutomationTask{ID: taskID, DeliveryWorkItemID: &workItemID, Operation: "delivery.qa"}
	reference := "s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/01-dashboard-semantic-qa.json"
	artifacts := []callbackArtifact{{Name: "01-dashboard-semantic-qa.json", Reference: reference, ContentType: "application/json", SizeBytes: 120, SHA256: strings.Repeat("a", 64)}}
	usage := json.RawMessage(`{"input_tokens":12,"output_tokens":4,"total_tokens":16}`)
	calls := []callbackToolExecution{
		{Tool: "stagehand", CallKey: "semantic-assessment", StepKey: "qa.semantic_browser", Provider: "minimax", Model: "MiniMax-M3", Usage: usage, RequestRef: reference, ResponseRef: reference},
		{Tool: "stagehand", CallKey: "semantic-retry", CallStatus: "failed", StepKey: "qa.semantic_browser", Provider: "minimax", Model: "MiniMax-M3", Usage: usage, RequestRef: reference, ResponseRef: reference},
	}
	rows, err := buildToolExecutionLedger(nil, task, uuid.Must(uuid.NewV4()).String(), "completed", calls, artifacts, time.Now().UTC())
	if err != nil || len(rows) != 2 || rows[0].CallKey == rows[1].CallKey || rows[1].CallStatus != "failed" {
		t.Fatalf("distinct tool calls must remain distinct: %#v / %v", rows, err)
	}
	calls[1].CallKey = "semantic-assessment"
	if _, err = buildToolExecutionLedger(nil, task, uuid.Must(uuid.NewV4()).String(), "completed", calls, artifacts, time.Now().UTC()); err == nil {
		t.Fatal("duplicate tool call keys must be rejected before persistence")
	}
}

func TestCostLedgerUnionIncludesPrimaryAndToolExecutions(t *testing.T) {
	for _, expected := range []string{"FROM automation_executions", "FROM automation_tool_executions", "'agent' AS execution_kind", "'tool' AS execution_kind", "call_key"} {
		if !strings.Contains(automationCostLedgerUnion, expected) {
			t.Fatalf("ledger union omitted %q: %s", expected, automationCostLedgerUnion)
		}
	}
	for _, forbidden := range []string{"request_ref", "response_ref", "usage_json"} {
		if strings.Contains(automationCostLedgerUnion, forbidden) {
			t.Fatalf("ledger union must not expose private %q: %s", forbidden, automationCostLedgerUnion)
		}
	}
}

func costLedgerColumns(names ...string) map[string]struct{} {
	columns := make(map[string]struct{}, len(names))
	for _, name := range names {
		columns[name] = struct{}{}
	}
	return columns
}

func automationCostLedgerTestDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, mock
}

func TestCostLedgerSourceFallsBackToPersistedAgentLedgerWhenToolMigrationIsAbsent(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	rows := sqlmock.NewRows([]string{"table_name", "column_name"})
	for _, column := range []string{"id", "automation_task_id", "total_cost_micros", "completed_at"} {
		rows.AddRow(automationExecutionLedgerTable, column)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT table_name, column_name")+`\s+FROM information_schema\.columns`).
		WithArgs(automationExecutionLedgerTable, automationToolExecutionLedgerTable).
		WillReturnRows(rows)

	source, coverage, err := automationCostLedgerSource(db)
	if err != nil {
		t.Fatalf("agent ledger fallback should be available: %v", err)
	}
	if coverage.State != "partial" || !coverage.AgentLedger || coverage.ToolLedger {
		t.Fatalf("coverage should identify the missing optional tool migration: %#v", coverage)
	}
	if strings.Contains(source, automationToolExecutionLedgerTable) || !strings.Contains(source, automationExecutionLedgerTable+".total_cost_micros") {
		t.Fatalf("source must use the persisted agent ledger only: %s", source)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCostLedgerProjectionKeepsLegacyAgentTotalsWithoutInventingToolCalls(t *testing.T) {
	projection, missing, ok := automationCostLedgerProjection(automationExecutionLedgerTable, costLedgerColumns(
		"id", "automation_task_id", "total_cost_micros", "completed_at",
	), "agent")
	if !ok {
		t.Fatalf("legacy primary ledger should remain readable: missing=%#v", missing)
	}
	for _, expected := range []string{
		"automation_executions.total_cost_micros",
		"0::bigint AS cached_input_tokens",
		"'legacy'::text AS pricing_basis",
		"'agent'::text AS execution_kind",
		"''::text AS tool",
	} {
		if !strings.Contains(projection, expected) {
			t.Fatalf("legacy primary projection omitted %q: %s", expected, projection)
		}
	}
	if strings.Contains(projection, automationToolExecutionLedgerTable) {
		t.Fatalf("agent-only projection must not imply a tool ledger: %s", projection)
	}
	if !strings.Contains(strings.Join(missing, ","), "cached_input_tokens") {
		t.Fatalf("legacy dimensions must be marked rather than silently claimed complete: %#v", missing)
	}
}

func TestCostLedgerProjectionRequiresAnAuthoritativePrimaryCost(t *testing.T) {
	projection, missing, ok := automationCostLedgerProjection(automationExecutionLedgerTable, costLedgerColumns(
		"id", "automation_task_id", "completed_at",
	), "agent")
	if ok || projection != "" {
		t.Fatalf("projection without persisted total cost must be unavailable, got %q", projection)
	}
	if !strings.Contains(strings.Join(missing, ","), "total_cost_micros") {
		t.Fatalf("missing authoritative cost was not reported: %#v", missing)
	}
}

func TestCostLedgerProjectionReadsLegacyToolRowsWithoutLosingCost(t *testing.T) {
	projection, missing, ok := automationCostLedgerProjection(automationToolExecutionLedgerTable, costLedgerColumns(
		"id", "automation_task_id", "total_cost_micros", "completed_at", "tool",
	), "tool")
	if !ok {
		t.Fatalf("legacy tool ledger should remain readable: missing=%#v", missing)
	}
	for _, expected := range []string{
		"automation_tool_executions.total_cost_micros",
		"automation_tool_executions.tool",
		"''::text AS call_key",
		"'completed'::text AS call_status",
		"'tool'::text AS execution_kind",
	} {
		if !strings.Contains(projection, expected) {
			t.Fatalf("legacy tool projection omitted %q: %s", expected, projection)
		}
	}
	for _, expectedMissing := range []string{"call_key", "call_status"} {
		if !strings.Contains(strings.Join(missing, ","), expectedMissing) {
			t.Fatalf("legacy tool metadata must be marked as unavailable: %#v", missing)
		}
	}
}

func TestCostBreakdownHasRuntimeIdentityForStepAccounting(t *testing.T) {
	breakdown := automationCostBreakdown{Key: "qa.semantic_browser", ExecutionKind: "tool", Tool: "stagehand"}
	if breakdown.ExecutionKind != "tool" || breakdown.Tool != "stagehand" {
		t.Fatalf("step breakdown cannot distinguish a tool call: %#v", breakdown)
	}
}

func TestCostBreakdownsExposeEveryTokenAndPriceDimension(t *testing.T) {
	breakdown := automationCostBreakdown{
		InputTokens: 120, OutputTokens: 40, CachedInputTokens: 25, CacheWriteTokens: 10, ReasoningTokens: 12,
		InputCostMicros: 17, OutputCostMicros: 9, CachedCostMicros: 2, CacheWriteCostMicros: 1,
	}
	encoded, err := json.Marshal(breakdown)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"input_tokens", "output_tokens", "cached_input_tokens", "cache_write_tokens", "reasoning_tokens", "input_cost_microusd", "output_cost_microusd", "cached_cost_microusd", "cache_write_cost_microusd"} {
		if !strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("global cost breakdown omitted %s: %s", field, encoded)
		}
	}
}

func TestDeliveryTaskMemberCanManageCancellationWithoutGrantingViewersControl(t *testing.T) {
	for _, member := range []models.DeliveryProjectMember{
		{Role: "owner"}, {Role: "delivery_manager"}, {Role: "viewer", Permissions: `["delivery:manage"]`},
	} {
		if !deliveryTaskMemberCanManage(member) {
			t.Fatalf("expected cancellation authority for %#v", member)
		}
	}
	for _, member := range []models.DeliveryProjectMember{
		{Role: "reviewer"}, {Role: "qa_reviewer"}, {Role: "viewer"}, {Role: "requester", Permissions: `["delivery:view"]`},
	} {
		if deliveryTaskMemberCanManage(member) {
			t.Fatalf("unexpected cancellation authority for %#v", member)
		}
	}
}

func TestCancelledTaskResultRemainsInspectableWithoutMakingItSuccessful(t *testing.T) {
	for _, status := range []string{"completed", "failed", "cancelled"} {
		if !taskResultIsInspectable(status) {
			t.Fatalf("expected %q output to remain auditable", status)
		}
	}
	for _, status := range []string{"queued", "running", "cancel_requested", "dispatch_failed"} {
		if taskResultIsInspectable(status) {
			t.Fatalf("unexpected inspectable output for unsettled state %q", status)
		}
	}
}

func TestCancellationRequestedTaskRetainsItsBudgetAdmissionHold(t *testing.T) {
	statuses := activeAutomationBudgetStatuses()
	if !strings.Contains(strings.Join(statuses, ","), "cancel_requested") {
		t.Fatalf("a cancelling in-flight task must retain its reservation: %#v", statuses)
	}
	if strings.Contains(strings.Join(statuses, ","), "cancelled") {
		t.Fatalf("a settled cancellation must release its reservation: %#v", statuses)
	}
}

func TestCanonicalTraceEntriesKeepKindsAndPrivateReferencesOutOfTheResponse(t *testing.T) {
	finishedAt := time.Now().UTC()
	taskID := uuid.Must(uuid.NewV4())
	agent := traceEntryFromAgentExecution(models.AutomationExecution{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: taskID, StepKey: "delivery.plan", Provider: "minimax", Model: "MiniMax-M3",
		InputTokens: 100, OutputTokens: 20, TotalTokens: 120, TotalCostMicros: 47, PricingBasis: "snapshot", CompletedAt: finishedAt,
		RequestRef: "s3://private/request.json", ResponseRef: "s3://private/response.json",
		UsageJSON: `{"input_tokens":100,"_itbem_provider":{"finish_reason":"stop","input_sensitive":true,"status_code":200,"ignored":"must-not-leak"}}`,
	})
	tool := traceEntryFromToolExecution(models.AutomationToolExecution{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: taskID, Tool: "stagehand", StepKey: "qa.semantic_browser", Provider: "minimax", Model: "MiniMax-M3",
		InputTokens: 50, OutputTokens: 10, TotalTokens: 60, TotalCostMicros: 23, PricingBasis: "snapshot", CompletedAt: finishedAt,
		RequestRef: "s3://private/report.json", ResponseRef: "s3://private/report.json",
		UsageJSON: `{"_itbem_provider":{"finish_reason":"length","output_sensitive":true,"status_code":429}}`,
	})
	if agent.ExecutionKind != "agent" || tool.ExecutionKind != "tool" || tool.Tool != "stagehand" || agent.TotalCostMicros != 47 || tool.TotalTokens != 60 || agent.ProviderOutcome == nil || agent.ProviderOutcome.FinishReason != "stop" || tool.ProviderOutcome == nil || tool.ProviderOutcome.StatusCode != 429 {
		t.Fatalf("canonical entries lost billing metadata: agent=%#v tool=%#v", agent, tool)
	}
	encoded, err := json.Marshal([]automationCostExecution{agent, tool})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"request_ref", "response_ref", "s3://private", "ignored"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("canonical trace entry leaked private data %q: %s", forbidden, encoded)
		}
	}
}

func TestFinalizeBudgetWatchClassifiesHardBudgetWithoutFloatingPointDrift(t *testing.T) {
	tests := []struct {
		name       string
		spent      int64
		wantStatus string
		wantUse    int
		wantLeft   int64
	}{
		{name: "healthy", spent: 799, wantStatus: "healthy", wantUse: 79, wantLeft: 201},
		{name: "alert threshold", spent: 800, wantStatus: "attention", wantUse: 80, wantLeft: 200},
		{name: "exceeded", spent: 1200, wantStatus: "exceeded", wantUse: 100, wantLeft: 0},
		{name: "negative input", spent: -1, wantStatus: "healthy", wantUse: 0, wantLeft: 1000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			watch := finalizeBudgetWatch(automationCostBudgetWatch{
				MonthlyBudgetMicros: 1000,
				AlertPercent:        80,
				SpentMicros:         test.spent,
			})
			if watch.Status != test.wantStatus || watch.UsagePercent != test.wantUse || watch.RemainingMicros != test.wantLeft {
				t.Fatalf("watch = %#v", watch)
			}
		})
	}
}

func TestFinalizeBudgetWatchCountsPendingReservations(t *testing.T) {
	watch := finalizeBudgetWatch(automationCostBudgetWatch{
		MonthlyBudgetMicros: 1000,
		AlertPercent:        80,
		SpentMicros:         300,
		ReservedMicros:      500,
	})
	if watch.AllocatedMicros != 800 || watch.RemainingMicros != 200 || watch.UsagePercent != 80 || watch.Status != "attention" {
		t.Fatalf("reservation-aware watch = %#v", watch)
	}
}

func TestFinalizeTaskBudgetWatchUsesLifetimeTaskCapAndReservations(t *testing.T) {
	watch := finalizeTaskBudgetWatch(automationCostTaskBudgetWatch{
		BudgetMicros:   1000,
		AlertPercent:   80,
		SpentMicros:    300,
		ReservedMicros: 500,
	})
	if watch.AllocatedMicros != 800 || watch.RemainingMicros != 200 || watch.UsagePercent != 80 || watch.Status != "attention" {
		t.Fatalf("task reservation-aware watch = %#v", watch)
	}

	exceeded := finalizeTaskBudgetWatch(automationCostTaskBudgetWatch{
		BudgetMicros: 1000,
		AlertPercent: 80,
		SpentMicros:  1001,
	})
	if exceeded.Status != "exceeded" || exceeded.RemainingMicros != 0 || exceeded.UsagePercent != 100 {
		t.Fatalf("task cap was not enforced: %#v", exceeded)
	}
}

func TestInputReferenceMatchesDedicatedBucketAndPrefix(t *testing.T) {
	cfg := &models.Config{AutomationInputBucket: "itbem-ai-inputs-prod-123-us-east-2"}
	valid := "s3://itbem-ai-inputs-prod-123-us-east-2/automation/inputs/task-1/input.json"
	if !inputReferenceMatches(cfg, valid) {
		t.Fatal("expected generated input reference to be accepted")
	}
	for _, reference := range []string{
		"s3://itbem-ai-inputs-prod-123-us-east-2/other/input.json",
		"s3://itbem-ai-inputs-other/automation/inputs/task-1/input.json",
		"s3://itbem-ai-inputs-prod-123-us-east-2/automation/inputs/task-1/source.txt",
	} {
		if inputReferenceMatches(cfg, reference) {
			t.Fatalf("unexpected input reference accepted: %s", reference)
		}
	}
}

func TestOutputReferenceMatchesTaskScopedLegacyOrImmutableRunResult(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	runID := uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationOutputBucket: "itbem-ai-outputs-prod-123-us-east-2"}
	valid := "s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/result.json"
	if !outputReferenceMatches(cfg, id, valid) {
		t.Fatal("expected legacy output reference to be accepted")
	}
	immutable := "s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + runID.String() + "/result.json"
	if !outputReferenceMatches(cfg, id, immutable) {
		t.Fatal("expected immutable execution output reference to be accepted")
	}
	if outputReferenceMatches(cfg, id, immutable+".bak") || outputReferenceMatches(cfg, id, "s3://itbem-ai-outputs-prod-123-us-east-2/automation/"+id.String()+"/runs/not-a-run/result.json") {
		t.Fatal("unexpected output reference accepted")
	}
}

func TestExecutionRequestReferenceMatchesOnlyItsOwnRun(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	runID := uuid.Must(uuid.NewV4())
	otherRunID := uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationOutputBucket: "itbem-ai-outputs-prod-123-us-east-2"}
	valid := "s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + runID.String() + "/request.json"
	if !executionRequestReferenceMatches(cfg, id, runID.String(), valid) {
		t.Fatal("expected exact immutable execution request to be accepted")
	}
	for _, reference := range []string{
		"s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + otherRunID.String() + "/request.json",
		"s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + runID.String() + "/result.json",
		"s3://itbem-ai-outputs-other/automation/" + id.String() + "/runs/" + runID.String() + "/request.json",
	} {
		if executionRequestReferenceMatches(cfg, id, runID.String(), reference) {
			t.Fatalf("unexpected execution request reference accepted: %s", reference)
		}
	}
}

func TestExecutionResultReferenceMatchesOnlyItsOriginalRun(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	runID := uuid.Must(uuid.NewV4())
	otherRunID := uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationOutputBucket: "itbem-ai-outputs-prod-123-us-east-2"}
	valid := "s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + runID.String() + "/result.json"
	if !executionResultReferenceMatches(cfg, id, runID.String(), valid) {
		t.Fatal("expected exact immutable execution result to be accepted")
	}
	for _, reference := range []string{
		"s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + otherRunID.String() + "/result.json",
		"s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + runID.String() + "/request.json",
		"s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/result.json",
	} {
		if executionResultReferenceMatches(cfg, id, runID.String(), reference) {
			t.Fatalf("unexpected execution result reference accepted: %s", reference)
		}
	}
}

func TestDeliveryOperationsRemainExplicitlyAllowlisted(t *testing.T) {
	for _, operation := range []string{
		"ai.chat", "document.analyze", "code.review", "product.ideate",
		"delivery.plan", "delivery.implementation", "delivery.publish", "delivery.qa", "delivery.summary",
	} {
		if _, allowed := allowedOperations[operation]; !allowed {
			t.Fatalf("expected operation to be enabled: %s", operation)
		}
	}
	if _, allowed := allowedOperations["shell.execute"]; allowed {
		t.Fatal("arbitrary command execution must never be enabled")
	}
}

func TestGenericTaskOperationsCannotBypassDeliveryGates(t *testing.T) {
	for _, operation := range []string{"ai.chat", "document.analyze", "code.review", "product.ideate"} {
		if !genericTaskOperationAllowed(operation) {
			t.Fatalf("generic operation %s should remain available", operation)
		}
	}
	for _, operation := range []string{"delivery.plan", "delivery.implementation", "delivery.publish", "delivery.qa", "delivery.summary", "shell.execute"} {
		if genericTaskOperationAllowed(operation) {
			t.Fatalf("operation %s must not be started through the generic task endpoint", operation)
		}
	}
}

func TestProviderAllowedUsesStrictNormalizedAllowlist(t *testing.T) {
	for _, provider := range []string{"minimax", " OpenAI ", "ANTHROPIC"} {
		if !providerAllowed(provider) {
			t.Fatalf("expected provider to be allowed: %s", provider)
		}
	}
	for _, provider := range []string{"", "azure-openai", "unknown"} {
		if providerAllowed(provider) {
			t.Fatalf("unexpected provider allowed: %s", provider)
		}
	}
}

func TestArtifactNamePatternRejectsTraversalAndPaths(t *testing.T) {
	for _, value := range []string{"01-screen.png", "report.webm", "result.json"} {
		if !artifactNamePattern.MatchString(value) {
			t.Fatalf("expected valid artifact name: %s", value)
		}
	}
	for _, value := range []string{"../secret", "nested/file.png", "", ".hidden"} {
		if artifactNamePattern.MatchString(value) {
			t.Fatalf("unexpected valid artifact name: %s", value)
		}
	}
}

func TestTaskOwnerAccessDoesNotNeedRootLookup(t *testing.T) {
	context := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	task := &models.AutomationTask{RequestedBy: "owner"}
	if !mayAccessTask(context, task, "owner") {
		t.Fatal("task owner should be allowed")
	}
}

func TestDeliveryTaskMemberCanViewPrivateExecutionEvidence(t *testing.T) {
	for _, member := range []models.DeliveryProjectMember{
		{Role: "owner"},
		{Role: "delivery_manager"},
		{Role: "reviewer"},
		{Role: "qa_reviewer"},
		{Role: "requester"},
		{Role: "viewer"},
		{Role: "custom", Permissions: `["delivery:view"]`},
	} {
		if !deliveryTaskMemberCanView(member) {
			t.Fatalf("delivery reader was denied private execution evidence: %#v", member)
		}
	}
	for _, member := range []models.DeliveryProjectMember{
		{Role: "custom"},
		{Role: "custom", Permissions: `["manage"]`},
		{Role: "custom", Permissions: `invalid`},
	} {
		if deliveryTaskMemberCanView(member) {
			t.Fatalf("unscoped project member gained private execution access: %#v", member)
		}
	}
}

func TestValidateCallbackArtifactsBindsQAAssetsToTheirTask(t *testing.T) {
	taskID := uuid.Must(uuid.NewV4())
	workItemID := uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationOutputBucket: "itbem-ai-outputs-local"}
	task := &models.AutomationTask{Operation: "delivery.qa", DeliveryWorkItemID: &workItemID}
	valid := callbackArtifact{
		Name:        "01-preview.png",
		Reference:   "s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/01-preview.png",
		ContentType: "image/png",
		SizeBytes:   120,
		SHA256:      strings.Repeat("a", 64),
	}
	if err := validateCallbackArtifacts(cfg, task, taskID, []callbackArtifact{valid}); err != nil {
		t.Fatalf("expected bounded QA artifact to be accepted: %v", err)
	}
	invalid := valid
	invalid.Reference = "s3://itbem-ai-outputs-local/automation/other/artifacts/01-preview.png"
	if err := validateCallbackArtifacts(cfg, task, taskID, []callbackArtifact{invalid}); err == nil {
		t.Fatal("expected artifact from another task to be rejected")
	}
	invalid = valid
	invalid.SHA256 = "untrusted"
	if err := validateCallbackArtifacts(cfg, task, taskID, []callbackArtifact{invalid}); err == nil {
		t.Fatal("expected artifact without a SHA-256 integrity digest to be rejected")
	}
	task.Operation = "delivery.plan"
	if err := validateCallbackArtifacts(cfg, task, taskID, []callbackArtifact{valid}); err == nil {
		t.Fatal("expected non-QA artifact callback to be rejected")
	}
}

func TestToolReportReferenceStaysInsideTheTaskArtifactNamespace(t *testing.T) {
	taskID := uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationOutputBucket: "itbem-ai-outputs-local"}
	valid := "s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/dashboard-semantic-qa.json"
	if !toolReportReferenceMatches(cfg, taskID, valid) {
		t.Fatal("expected the uploaded Stagehand report reference to be inspectable")
	}
	for _, reference := range []string{
		"s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/runs/other/result.json",
		"s3://itbem-ai-outputs-local/automation/other/artifacts/dashboard-semantic-qa.json",
		"s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/nested/semantic-qa.json",
		"s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/preview.png",
	} {
		if toolReportReferenceMatches(cfg, taskID, reference) {
			t.Fatalf("tool report inspector accepted an unrelated reference: %s", reference)
		}
	}
}

func TestImplementationHandoffCreatesAnAuditableLocalChangeSet(t *testing.T) {
	taskID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	branch := "itbem-agent/" + taskID.String()
	raw, err := json.Marshal(map[string]any{
		"workspace":          "workspace://events-backend",
		"worktree":           "workspace://events-backend#" + branch,
		"branch":             branch,
		"base_sha":           "0123456789abcdef0123456789abcdef01234567",
		"github_repository":  "itbem-corp/itbem-events-backend",
		"review_diff_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"diff_check_passed":  true,
		"validations":        []map[string]bool{{"passed": true}, {"passed": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	change, err := implementationChangeSetForHandoff(&models.AutomationTask{ID: taskID, Operation: "delivery.implementation", DeliveryWorkItemID: &workItemID}, raw, time.Now().UTC())
	if err != nil {
		t.Fatalf("expected bounded implementation handoff: %v", err)
	}
	if change.ReviewType != "local_worktree" || change.CIStatus != "passed" || change.RepositoryRef != "workspace://events-backend" {
		t.Fatalf("unexpected change set: %#v", change)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(change.MetadataJSON), &metadata); err != nil || metadata["verification_source"] != "itbem-local-agent" {
		t.Fatalf("missing verification provenance: %s", change.MetadataJSON)
	}
}

func TestImplementationHandoffRejectsMismatchedWorktree(t *testing.T) {
	taskID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	raw := []byte(`{"workspace":"workspace://events-backend","worktree":"workspace://events-backend#itbem-agent/other","branch":"itbem-agent/` + taskID.String() + `","base_sha":"0123456789abcdef0123456789abcdef01234567","diff_check_passed":true,"validations":[{"passed":true}]}`)
	if _, err := implementationChangeSetForHandoff(&models.AutomationTask{ID: taskID, Operation: "delivery.implementation", DeliveryWorkItemID: &workItemID}, raw, time.Now().UTC()); err == nil {
		t.Fatal("expected mismatched worktree handoff to be rejected")
	}
}

func TestImplementationHandoffCreatesOneImmutableReviewPerRepository(t *testing.T) {
	taskID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	branch := "itbem-agent/" + taskID.String()
	changeSet := func(workspace, digest string) map[string]any {
		return map[string]any{
			"workspace": workspace, "worktree": workspace + "#" + branch, "branch": branch,
			"base_sha": "0123456789abcdef0123456789abcdef01234567", "github_repository": "itbem-corp/" + strings.TrimPrefix(workspace, "workspace://"),
			"review_diff_sha256": digest, "diff_check_passed": true, "validations": []map[string]bool{{"passed": true}},
		}
	}
	raw, err := json.Marshal(map[string]any{"change_sets": []map[string]any{
		changeSet("workspace://backend", strings.Repeat("a", 64)),
		changeSet("workspace://dashboard", strings.Repeat("b", 64)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	changes, err := implementationChangeSetsForHandoff(&models.AutomationTask{ID: taskID, Operation: "delivery.implementation", DeliveryWorkItemID: &workItemID}, raw, time.Now().UTC())
	if err != nil || len(changes) != 2 || changes[0].RepositoryRef != "workspace://backend" || changes[1].RepositoryRef != "workspace://dashboard" {
		t.Fatalf("multi-repository handoff must persist independent reviews: %#v / %v", changes, err)
	}
	duplicate, _ := json.Marshal(map[string]any{"change_sets": []map[string]any{
		changeSet("workspace://backend", strings.Repeat("a", 64)),
		changeSet("workspace://backend", strings.Repeat("b", 64)),
	}})
	if _, err := implementationChangeSetsForHandoff(&models.AutomationTask{ID: taskID, Operation: "delivery.implementation", DeliveryWorkItemID: &workItemID}, duplicate, time.Now().UTC()); err == nil {
		t.Fatal("a repeated repository must fail closed")
	}
}

func TestDeliveryQAEvidenceTitlesDescribeResponsiveScreenshots(t *testing.T) {
	for name, expected := range map[string]string{
		"backend-preview-desktop.png":  "QA visual · Escritorio",
		"dashboard-preview-mobile.png": "QA visual · Móvil",
		"preview.png":                  "QA visual · Preview",
	} {
		if got := deliveryQAEvidenceTitle(name, "screenshot"); got != expected {
			t.Fatalf("title for %q = %q; want %q", name, got, expected)
		}
	}
	if got := deliveryQAEvidenceTitle("report.json", "artifact"); got != "Evidencia QA: report.json" {
		t.Fatalf("non-visual artifact title changed: %q", got)
	}
	for name, expected := range map[string]string{
		"dashboard-semantic-qa-case-01-before.png": "QA visual · Caso 01 · Antes",
		"dashboard-semantic-qa-case-01-after.png":  "QA visual · Caso 01 · Después",
	} {
		if got := deliveryQAEvidenceTitle(name, "screenshot"); got != expected {
			t.Fatalf("comparison title for %q = %q; want %q", name, got, expected)
		}
	}
	if key, role := deliveryQAEvidenceComparison("dashboard-semantic-qa-case-02-before.png"); key != "case-02" || role != "before" {
		t.Fatalf("case evidence pair metadata was not derived: %q / %q", key, role)
	}
	if key, role := deliveryQAEvidenceComparison("dashboard-semantic-qa-case-untrusted-before.png"); key != "" || role != "" {
		t.Fatalf("unbounded evidence name must not become a comparison pair: %q / %q", key, role)
	}
}

func TestLocalAutomationInputProxyIsExplicitlyLocalOnly(t *testing.T) {
	t.Setenv("ENV", "local")
	if !localAutomationInputProxyAllowed() {
		t.Fatal("expected the authenticated input proxy to be available in local development")
	}
	for _, environment := range []string{"", "development", "staging", "production"} {
		t.Setenv("ENV", environment)
		if localAutomationInputProxyAllowed() {
			t.Fatalf("input proxy must not be available when ENV=%q", environment)
		}
	}
}
