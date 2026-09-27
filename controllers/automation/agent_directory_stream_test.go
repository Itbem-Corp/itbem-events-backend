package automation

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

var agentDirectoryStreamTestColumns = []string{
	"profile_count", "profile_updated_at", "heartbeat_count", "heartbeat_updated_at", "heartbeat_last_seen_at",
	"queued_task_count", "oldest_queued_at", "active_task_count", "active_task_updated_at", "active_task_lease_at",
	"active_progress_total", "assigned_task_count", "active_step_count", "active_step_updated_at", "active_step_lease_at",
	"active_assignment_count", "active_assignment_updated_at",
	"ledger_entry_count", "ledger_spend_micros", "ledger_latest_at", "work_item_updated_at", "project_updated_at", "client_updated_at",
}

func agentDirectoryStreamTestProjection(activeTasks, profiles int64, updated time.Time) *sqlmock.Rows {
	values := []driver.Value{
		profiles, updated, int64(1), updated, updated,
		int64(3), updated, activeTasks, updated, updated,
		int64(2), activeTasks, int64(2), updated, updated,
		int64(2), updated,
		int64(5), int64(47), updated, updated, updated, updated,
	}
	return sqlmock.NewRows(agentDirectoryStreamTestColumns).AddRow(values...)
}

func expectAgentDirectoryStreamProjection(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectQuery(`(?s)SELECT\s+\(SELECT COUNT\(\*\) FROM automation_agent_profiles\).*AS client_updated_at`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(rows)
}

func withAgentDirectoryStreamRoot(t *testing.T, database *gorm.DB) {
	t.Helper()
	previousDB := configuration.DB
	configuration.DB = database
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelOperational}, nil
	}})
	t.Cleanup(restore)
}

type agentDirectoryStreamCapture struct {
	header  http.Header
	mu      sync.Mutex
	status  int
	body    strings.Builder
	flushed chan struct{}
}

func newAgentDirectoryStreamCapture() *agentDirectoryStreamCapture {
	return &agentDirectoryStreamCapture{header: make(http.Header), flushed: make(chan struct{}, 8)}
}

func (writer *agentDirectoryStreamCapture) Header() http.Header    { return writer.header }
func (writer *agentDirectoryStreamCapture) WriteHeader(status int) { writer.status = status }
func (writer *agentDirectoryStreamCapture) Write(payload []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.body.Write(payload)
}
func (writer *agentDirectoryStreamCapture) Flush() {
	select {
	case writer.flushed <- struct{}{}:
	default:
	}
}
func (writer *agentDirectoryStreamCapture) bodyText() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.body.String()
}

type agentDirectoryStreamPlainWriter struct {
	header http.Header
	status int
}

func (writer *agentDirectoryStreamPlainWriter) Header() http.Header    { return writer.header }
func (writer *agentDirectoryStreamPlainWriter) WriteHeader(status int) { writer.status = status }
func (writer *agentDirectoryStreamPlainWriter) Write(payload []byte) (int, error) {
	return len(payload), nil
}

func TestStreamAgentDirectoryRequiresPlatformRoot(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream", nil), recorder)
	ctx.Set("cognito_sub", "agent-directory-stream-test")
	ctx.Set("workspace_mode", "platform")
	if err := StreamAgentDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-root status = %d, want %d: %s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestStreamAgentDirectoryEmitsOnlyRevisionSnapshotAndChangedUpdate(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	withAgentDirectoryStreamRoot(t, db)
	base := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	expectAgentDirectoryStreamProjection(mock, agentDirectoryStreamTestProjection(1, 1, base))
	expectAgentDirectoryStreamProjection(mock, agentDirectoryStreamTestProjection(1, 1, base))
	expectAgentDirectoryStreamProjection(mock, agentDirectoryStreamTestProjection(2, 2, base.Add(time.Second)))

	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream", nil).WithContext(requestContext)
	writer := newAgentDirectoryStreamCapture()
	ctx := echo.New().NewContext(request, writer)
	ctx.Set("cognito_sub", "agent-directory-stream-test")
	ctx.Set("workspace_mode", "platform")
	finished := make(chan error, 1)
	go func() { finished <- StreamAgentDirectory(ctx) }()

	deadline := time.After(6 * time.Second)
	for {
		select {
		case <-writer.flushed:
			if strings.Contains(writer.bodyText(), "event: update\n") {
				cancel()
				goto complete
			}
		case <-deadline:
			cancel()
			t.Fatalf("stream did not emit the changed revision; body: %s", writer.bodyText())
		}
	}

complete:
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("stream returned an error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after client disconnect")
	}
	if writer.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", writer.status)
	}
	for header, want := range map[string]string{
		echo.HeaderContentType:     "text/event-stream; charset=utf-8",
		echo.HeaderCacheControl:    "private, no-store, no-cache, no-transform",
		"X-Accel-Buffering":        "no",
		echo.HeaderContentEncoding: "identity",
	} {
		if got := writer.header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	body := writer.bodyText()
	if strings.Count(body, "event: snapshot\n") != 1 || strings.Count(body, "event: update\n") != 1 {
		t.Fatalf("expected one initial snapshot and one update after an unchanged poll: %s", body)
	}
	for _, forbidden := range []string{"prompt", "result", "usage_json", "workspace_readiness", "provider", "project_id", "task_id", "secret", "credential"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("stream payload contains forbidden directory field %q: %s", forbidden, body)
		}
	}
	var payloads []map[string]json.RawMessage
	for _, event := range []string{"snapshot", "update"} {
		start := strings.Index(body, "event: "+event+"\n")
		if start < 0 {
			t.Fatalf("missing %s event: %s", event, body)
		}
		lineStart := strings.Index(body[start:], "data: ") + start + len("data: ")
		lineEnd := strings.Index(body[lineStart:], "\n") + lineStart
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body[lineStart:lineEnd]), &payload); err != nil {
			t.Fatalf("decode %s data: %v", event, err)
		}
		if len(payload) != 2 || payload["revision"] == nil || payload["generated_at"] == nil {
			t.Fatalf("%s JSON keys = %#v, want exactly revision and generated_at", event, payload)
		}
		var revision string
		if err := json.Unmarshal(payload["revision"], &revision); err != nil {
			t.Fatal(err)
		}
		if len(revision) != sha256.Size*2 {
			t.Fatalf("revision length = %d, want opaque %d-character hex token", len(revision), sha256.Size*2)
		}
		if _, err := hex.DecodeString(revision); err != nil {
			t.Fatalf("revision is not an opaque hex token: %v", err)
		}
		var generatedAt string
		if err := json.Unmarshal(payload["generated_at"], &generatedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := time.Parse(time.RFC3339Nano, generatedAt); err != nil {
			t.Fatalf("generated_at is not RFC3339Nano: %q", generatedAt)
		}
		payloads = append(payloads, payload)
	}
	if string(payloads[0]["revision"]) == string(payloads[1]["revision"]) {
		t.Fatalf("revision did not change between events: %#v", payloads)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentDirectoryStreamPublicRevisionDoesNotExposeFingerprint(t *testing.T) {
	projection := agentDirectoryStreamProjection{ProfileCount: 4, QueuedTaskCount: 9, ActiveTaskCount: 2}
	first, err := newAgentDirectoryStreamEvent(projection, time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	second, err := newAgentDirectoryStreamEvent(projection, time.Date(2026, 9, 24, 12, 0, 1, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if first.fingerprint != second.fingerprint {
		t.Fatal("same operational projection produced a different internal fingerprint")
	}
	if first.Revision == second.Revision {
		t.Fatal("public revision should be a fresh opaque token, not a deterministic projection hash")
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || payload["fingerprint"] != nil {
		t.Fatalf("public event contains private fingerprint or extra fields: %s", encoded)
	}
}

func TestStreamAgentDirectoryRevalidatesPlatformAuthorizationDuringSession(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	previousHeartbeat := agentDirectoryStreamHeartbeat
	agentDirectoryStreamHeartbeat = 5 * time.Millisecond
	t.Cleanup(func() { agentDirectoryStreamHeartbeat = previousHeartbeat })
	var authChecks atomic.Int32
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		// Initial platform resolution performs Root validation and then the
		// canonical directory's CurrentUser lookup. The third lookup is the
		// periodic authorization check and simulates a revoked role.
		if authChecks.Add(1) <= 2 {
			return &models.User{IsRoot: true, RootLevel: models.RootLevelOperational}, nil
		}
		return &models.User{IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	base := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	expectAgentDirectoryStreamProjection(mock, agentDirectoryStreamTestProjection(1, 1, base))

	request := httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream", nil)
	writer := newAgentDirectoryStreamCapture()
	ctx := echo.New().NewContext(request, writer)
	ctx.Set("cognito_sub", "agent-directory-stream-test")
	ctx.Set("workspace_mode", "platform")
	finished := make(chan error, 1)
	go func() { finished <- StreamAgentDirectory(ctx) }()

	// The first call to SyncUser authorizes the connection; the hook simulates
	// revocation/downgrade on the next in-session lookup.
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("stream returned an error after authorization was revoked: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream continued after the periodic authorization check")
	}
	if authChecks.Load() < 3 {
		t.Fatalf("authorization checks = %d, want initial resolution and in-session revalidation", authChecks.Load())
	}
	if strings.Contains(writer.bodyText(), ": keepalive") || strings.Count(writer.bodyText(), "event: snapshot\n") != 1 {
		t.Fatalf("stream should close without sending another heartbeat after revocation: %s", writer.bodyText())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamAgentDirectoryRejectsPlatformRootFromTenantSurface(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream", nil), recorder)
	ctx.Set("cognito_sub", "agent-directory-stream-test")
	ctx.Set("tenant_code", "caffetton")
	ctx.Set("workspace_mode", "platform")
	if err := StreamAgentDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("platform root on tenant surface status = %d, want %d: %s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestStreamAgentDirectoryRequiresPlatformWorkspace(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	organizationRecorder := httptest.NewRecorder()
	organizationContext := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream", nil), organizationRecorder)
	organizationContext.Set("cognito_sub", "agent-directory-stream-test")
	organizationContext.Set("workspace_mode", "organization")
	organizationContext.Set("organization_id", uuid.Must(uuid.NewV4()))
	if err := StreamAgentDirectory(organizationContext); err != nil {
		t.Fatal(err)
	}
	if organizationRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("organization workspace should reach DB readiness check, got %d: %s", organizationRecorder.Code, organizationRecorder.Body.String())
	}

	platformRecorder := httptest.NewRecorder()
	platformContext := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream", nil), platformRecorder)
	platformContext.Set("cognito_sub", "agent-directory-stream-test")
	platformContext.Set("workspace_mode", "platform")
	if err := StreamAgentDirectory(platformContext); err != nil {
		t.Fatal(err)
	}
	if platformRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("platform workspace status = %d, want DB readiness check %d: %s", platformRecorder.Code, http.StatusServiceUnavailable, platformRecorder.Body.String())
	}
}

func TestStreamAgentDirectoryFailsWhenStreamingIsUnsupported(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	withAgentDirectoryStreamRoot(t, db)
	writer := &agentDirectoryStreamPlainWriter{header: make(http.Header)}
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream", nil), writer)
	ctx.Set("cognito_sub", "agent-directory-stream-test")
	ctx.Set("workspace_mode", "platform")
	if err := StreamAgentDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	if writer.status != http.StatusInternalServerError {
		t.Fatalf("unsupported stream status = %d, want 500", writer.status)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamAgentDirectoryReturnsSanitizedDatabaseFailure(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	withAgentDirectoryStreamRoot(t, db)
	mock.ExpectQuery(`(?s)SELECT\s+\(SELECT COUNT\(\*\) FROM automation_agent_profiles\).*AS client_updated_at`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(errors.New("private database detail"))
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream", nil), recorder)
	ctx.Set("cognito_sub", "agent-directory-stream-test")
	ctx.Set("workspace_mode", "platform")
	if err := StreamAgentDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "private database detail") {
		t.Fatalf("DB failure must be sanitized, status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamAgentDirectorySupportsAuthorizedOrganizationClientAndProjectScope(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID, childClientID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "agent-directory-stream-org-admin"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	query := "/api/automation/agents/stream?client_id=" + organizationID.String() + "&project_id=" + projectID.String()
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID, childClientID) // selected client tree
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, childClientID))
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID, childClientID) // authenticated organization boundary
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID, childClientID) // canonical project-view policy
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, childClientID))
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, childClientID))
	expectAgentDirectoryStreamScopedBuild(mock, organizationID, childClientID, projectID)

	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, query, nil).WithContext(requestContext)
	writer := newAgentDirectoryStreamCapture()
	ctx := echo.New().NewContext(request, writer)
	ctx.Set("cognito_sub", subject)
	setAgentHistoryOrganizationWorkspace(ctx, organizationID)
	finished := make(chan error, 1)
	go func() { finished <- StreamAgentDirectory(ctx) }()
	deadline := time.After(2 * time.Second)
	for !strings.Contains(writer.bodyText(), "event: snapshot\n") {
		select {
		case <-writer.flushed:
		case <-deadline:
			cancel()
			t.Fatalf("organization-scoped stream did not emit snapshot: %s", writer.bodyText())
		}
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("organization-scoped stream returned an error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("organization-scoped stream did not stop after client disconnect")
	}
	if writer.status != http.StatusOK {
		t.Fatalf("organization-scoped stream status = %d, want 200", writer.status)
	}
	assertAgentDirectoryStreamOpaquePayload(t, writer.bodyText())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectAgentDirectoryStreamScopedBuild(mock sqlmock.Sqlmock, organizationID, childClientID, projectID uuid.UUID) {
	profileColumns := []string{"id", "agent_key", "name", "specialty", "description", "operations_json", "capabilities_json", "active", "created_at", "updated_at"}
	mock.ExpectQuery(`SELECT \* FROM "automation_agent_profiles" ORDER BY agent_key ASC`).
		WillReturnRows(sqlmock.NewRows(profileColumns))
	activeColumns := []string{"task_id", "run_id", "operation", "status", "worker_id", "agent_key", "step_key", "created_at", "started_at", "client_id", "client_name", "project_id", "project_name", "work_item_id", "work_item_title", "epic_id", "epic_title"}
	mock.ExpectQuery(`(?s)SELECT t\.id AS task_id.*WHERE t\.status IN \(\$1,\$2\) AND p\.client_id IN \(\$3,\$4\) AND p\.id = \$5 ORDER BY t\.created_at ASC`).
		WithArgs("running", "cancel_requested", organizationID, childClientID, projectID).
		WillReturnRows(sqlmock.NewRows(activeColumns))
	mock.ExpectQuery(`(?s)SELECT t\.operation, COUNT\(\*\) AS count, MIN\(t\.created_at\) AS oldest FROM .*delivery_work_items.*delivery_projects.*WHERE t\.status = \$1 AND p\.client_id IN \(\$2,\$3\) AND p\.id = \$4 GROUP BY .*`).
		WithArgs("queued", organizationID, childClientID, projectID).
		WillReturnRows(sqlmock.NewRows([]string{"operation", "count", "oldest"}))
	usageQuery := `(?s)SELECT ledger\.agent_key, COUNT\(DISTINCT \(ledger\.automation_task_id, ledger\.run_id\)\).*JOIN automation_tasks AS t.*JOIN delivery_work_items AS w.*JOIN delivery_projects AS p.*WHERE TRUE AND p\.client_id IN \(\$3,\$4\) AND p\.id = \$5 GROUP BY ledger\.agent_key`
	mock.ExpectQuery(usageQuery).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), organizationID, childClientID, projectID).
		WillReturnRows(sqlmock.NewRows([]string{"agent_key", "run_count", "spend_micros"}))
}

func assertAgentDirectoryStreamOpaquePayload(t *testing.T, body string) {
	t.Helper()
	if strings.Count(body, "event: snapshot\n") != 1 {
		t.Fatalf("expected exactly one snapshot event: %s", body)
	}
	for _, forbidden := range []string{"agents", "task_id", "project_id", "provider", "model", "prompt", "credential", "directory", "queued_tasks"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("stream payload contains directory/task/provider data %q: %s", forbidden, body)
		}
	}
	start := strings.Index(body, "event: snapshot\n")
	lineStart := strings.Index(body[start:], "data: ") + start + len("data: ")
	lineEnd := strings.Index(body[lineStart:], "\n") + lineStart
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body[lineStart:lineEnd]), &payload); err != nil {
		t.Fatalf("decode snapshot data: %v", err)
	}
	if len(payload) != 2 || payload["revision"] == nil || payload["generated_at"] == nil {
		t.Fatalf("snapshot JSON keys = %#v, want exactly revision and generated_at", payload)
	}
}

func TestStreamAgentDirectoryRejectsMalformedFiltersAndMissingOrganizationScope(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	rootRestore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelOperational}, nil
	}})
	malformed := httptest.NewRecorder()
	malformedContext := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream?client_id=not-a-uuid", nil), malformed)
	malformedContext.Set("cognito_sub", "agent-directory-stream-malformed")
	malformedContext.Set("workspace_mode", "platform")
	if err := StreamAgentDirectory(malformedContext); err != nil {
		t.Fatal(err)
	}
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed filter status = %d, want 400: %s", malformed.Code, malformed.Body.String())
	}
	malformedProject := httptest.NewRecorder()
	malformedProjectContext := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream?project_id=invalid", nil), malformedProject)
	malformedProjectContext.Set("cognito_sub", "agent-directory-stream-malformed")
	malformedProjectContext.Set("workspace_mode", "platform")
	if err := StreamAgentDirectory(malformedProjectContext); err != nil {
		t.Fatal(err)
	}
	if malformedProject.Code != http.StatusBadRequest {
		t.Fatalf("malformed project filter status = %d, want 400: %s", malformedProject.Code, malformedProject.Body.String())
	}
	rootRestore()

	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: "agent-directory-stream-no-scope", IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	missingScope := httptest.NewRecorder()
	missingContext := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream", nil), missingScope)
	missingContext.Set("cognito_sub", "agent-directory-stream-no-scope")
	setAgentHistoryOrganizationWorkspace(missingContext, uuid.Must(uuid.NewV4()))
	if err := StreamAgentDirectory(missingContext); err != nil {
		t.Fatal(err)
	}
	if missingScope.Code != http.StatusBadRequest {
		t.Fatalf("organization caller without a client/project scope status = %d, want 400: %s", missingScope.Code, missingScope.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamAgentDirectoryDeniesProjectOutsideActorMembership(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID, projectID, clientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "agent-directory-stream-project-denied"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, clientID))
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID, clientID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, clientID))
	mock.ExpectQuery(`SELECT \* FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectID, subject, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "cognito_sub", "role", "permissions"}))
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream?project_id="+projectID.String(), nil), recorder)
	ctx.Set("cognito_sub", subject)
	setAgentHistoryOrganizationWorkspace(ctx, organizationID)
	if err := StreamAgentDirectory(ctx); err != nil && recorder.Code == 0 {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("project outside actor membership status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamAgentDirectoryDeniesCrossOrganizationClientAndProjectScopes(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	organizationID, otherClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expectAgentHistoryOrganizationClients(mock, otherClientID, otherClientID)
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID)
	crossOrganization := httptest.NewRecorder()
	crossOrganizationContext := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream?client_id="+otherClientID.String(), nil), crossOrganization)
	crossOrganizationContext.Set("cognito_sub", "agent-directory-stream-cross-org")
	setAgentHistoryOrganizationWorkspace(crossOrganizationContext, organizationID)
	if err := StreamAgentDirectory(crossOrganizationContext); err != nil && crossOrganization.Code == 0 {
		t.Fatal(err)
	}
	if crossOrganization.Code != http.StatusNotFound {
		t.Fatalf("cross-organization client status = %d, want 404: %s", crossOrganization.Code, crossOrganization.Body.String())
	}

	foreignProjectID, foreignProjectClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(foreignProjectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(foreignProjectID, foreignProjectClientID))
	expectAgentHistoryOrganizationClients(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(foreignProjectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(foreignProjectID, foreignProjectClientID))
	crossOrganizationProject := httptest.NewRecorder()
	crossOrganizationProjectContext := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream?project_id="+foreignProjectID.String(), nil), crossOrganizationProject)
	crossOrganizationProjectContext.Set("cognito_sub", "agent-directory-stream-cross-org-project")
	setAgentHistoryOrganizationWorkspace(crossOrganizationProjectContext, organizationID)
	if err := StreamAgentDirectory(crossOrganizationProjectContext); err != nil && crossOrganizationProject.Code == 0 {
		t.Fatal(err)
	}
	if crossOrganizationProject.Code != http.StatusNotFound {
		t.Fatalf("project outside organization status = %d, want 404: %s", crossOrganizationProject.Code, crossOrganizationProject.Body.String())
	}

	selectedClientID, projectID, projectClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expectAgentHistoryOrganizationClients(mock, selectedClientID, selectedClientID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, projectClientID))
	crossClientProject := httptest.NewRecorder()
	crossClientContext := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/agents/stream?client_id="+selectedClientID.String()+"&project_id="+projectID.String(), nil), crossClientProject)
	crossClientContext.Set("cognito_sub", "agent-directory-stream-cross-project")
	crossClientContext.Set("workspace_mode", "platform")
	if err := StreamAgentDirectory(crossClientContext); err != nil && crossClientProject.Code == 0 {
		t.Fatal(err)
	}
	if crossClientProject.Code != http.StatusNotFound {
		t.Fatalf("project outside selected client status = %d, want 404: %s", crossClientProject.Code, crossClientProject.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
