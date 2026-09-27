package automation

import (
	"crypto/sha256"
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
)

func TestPersistAIActionPolicyRevisionAppendsMonotonicSnapshots(t *testing.T) {
	mockPtr, restoreDB := configureAIActionPolicyRevisionTestDB(t)
	defer restoreDB()
	mock := *mockPtr
	policyID := uuid.Must(uuid.NewV4())
	actorID := uuid.Must(uuid.NewV4()).String()
	firstRoutes := []models.AutomationAIActionRoute{{Provider: "openrouter", Model: "provider/model-cheap", ReasoningEnabled: false}}
	secondRoutes := []models.AutomationAIActionRoute{{Provider: "deepseek", Model: "deepseek-chat", ReasoningEnabled: true, ReasoningEffort: "high"}}

	expectAIActionPolicyRevisionWrite(mock, policyID, "ai.chat", 0, "legacy-provider", "legacy-model", firstRoutes, actorID)
	first, err := persistAIActionPolicyRevision(configuration.DB, "ai.chat", firstRoutes, actorID)
	if err != nil {
		t.Fatalf("persist first policy revision: %v", err)
	}
	if first.ID != policyID || first.Revision != 1 || first.Provider != firstRoutes[0].Provider || first.RoutesJSON != mustAIActionPolicyRoutesJSON(t, firstRoutes) {
		t.Fatalf("first active revision = %#v", first)
	}

	expectAIActionPolicyRevisionWrite(mock, policyID, "ai.chat", 1, firstRoutes[0].Provider, firstRoutes[0].Model, secondRoutes, actorID)
	second, err := persistAIActionPolicyRevision(configuration.DB, "ai.chat", secondRoutes, actorID)
	if err != nil {
		t.Fatalf("persist second policy revision: %v", err)
	}
	if second.ID != policyID || second.Revision != 2 || second.Provider != secondRoutes[0].Provider || second.Model != secondRoutes[0].Model {
		t.Fatalf("second active revision = %#v", second)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistAIActionPolicyRevisionRollsBackActiveUpdateWhenSnapshotInsertFails(t *testing.T) {
	mockPtr, restoreDB := configureAIActionPolicyRevisionTestDB(t)
	defer restoreDB()
	mock := *mockPtr
	policyID := uuid.Must(uuid.NewV4())
	actorID := uuid.Must(uuid.NewV4()).String()
	routes := []models.AutomationAIActionRoute{{Provider: "openrouter", Model: "provider/model-cheap"}}
	encoded := mustAIActionPolicyRoutesJSON(t, routes)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(encoded)))

	mock.ExpectBegin()
	expectAIActionPolicyBootstrapInsert(mock, routes[0], "ai.chat", actorID)
	mock.ExpectQuery(`SELECT \* FROM "automation_ai_action_policies" WHERE operation = \$1 ORDER BY "automation_ai_action_policies"\."id" LIMIT \$2 FOR UPDATE`).
		WithArgs("ai.chat", 1).
		WillReturnRows(aiActionPolicyRows(policyID, "ai.chat", 0, "old-provider", "old-model", `[]`, actorID))
	mock.ExpectExec(`UPDATE "automation_ai_action_policies" SET `).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO automation_ai_action_policy_revisions`).
		WithArgs(policyID, "ai.chat", int64(1), encoded, hash, actorID, sqlmock.AnyArg()).
		WillReturnError(fmt.Errorf("simulated unique revision conflict"))
	mock.ExpectRollback()

	if _, err := persistAIActionPolicyRevision(configuration.DB, "ai.chat", routes, actorID); err == nil {
		t.Fatal("snapshot insert failure must abort the active policy update")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListAIActionPolicyRevisionsIsPrimaryRootOnlyAndRedactsUnknownRouteFields(t *testing.T) {
	mockPtr, restoreDB := configureAIActionPolicyRevisionTestDB(t)
	defer restoreDB()
	mock := *mockPtr
	configureAIActionPolicyTestRoot(t, models.RootLevelPrimary)

	newestID, olderID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	actorID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	routesWithUnknownSecret := `[{"provider":"openrouter","model":"provider/model-cheap","reasoning_enabled":false,"api_key":"NEVER-LEAK"}]`
	query := regexp.QuoteMeta(`SELECT "id","operation","revision","routes_json","routes_hash","changed_by","created_at" FROM "automation_ai_action_policy_revisions" WHERE operation = $1 ORDER BY revision DESC, id DESC LIMIT $2`)
	mock.ExpectQuery(query).WithArgs("ai.chat", 2).WillReturnRows(sqlmock.NewRows([]string{"id", "operation", "revision", "routes_json", "routes_hash", "changed_by", "created_at"}).
		AddRow(newestID, "ai.chat", int64(7), routesWithUnknownSecret, strings.Repeat("a", 64), actorID.String(), createdAt).
		AddRow(olderID, "ai.chat", int64(6), `[{"provider":"deepseek","model":"deepseek-chat","reasoning_enabled":true,"reasoning_effort":"high"}]`, strings.Repeat("b", 64), actorID.String(), createdAt.Add(-time.Minute)))
	ctx, recorder := aiActionPolicyRevisionTestContext(http.MethodGet, "/api/automation/ai/policies/ai.chat/revisions?limit=1", "ai.chat")
	if err := ListAIActionPolicyRevisions(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data aiActionPolicyRevisionPage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	page := envelope.Data
	if page.Operation != "ai.chat" || page.Limit != 1 || len(page.Items) != 1 || page.Items[0].ID != newestID || page.Items[0].Revision != 7 || page.NextCursor == "" {
		t.Fatalf("unexpected revision page/order: %#v", page)
	}
	if len(page.Items[0].Routes) != 1 || page.Items[0].Routes[0].Model != "provider/model-cheap" {
		t.Fatalf("revision route snapshot missing from response: %#v", page.Items[0])
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"NEVER-LEAK", "api_key", "routes_json", "policy_id", "secret"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("revision API exposed %q: %s", forbidden, body)
		}
	}
	cursor, err := decodeAIActionPolicyRevisionCursor(page.NextCursor, "ai.chat")
	if err != nil || cursor == nil || cursor.Revision != 7 || cursor.ID != newestID.String() {
		t.Fatalf("next cursor does not match last returned revision: %#v, %v", cursor, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListAIActionPolicyRevisionsRejectsNonPrimaryRoot(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	configureAIActionPolicyTestRoot(t, models.RootLevelOperational)
	ctx, recorder := aiActionPolicyRevisionTestContext(http.MethodGet, "/api/automation/ai/policies/ai.chat/revisions", "ai.chat")
	if err := ListAIActionPolicyRevisions(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("operational-root status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAIActionPolicyRevisionPageSizeAndCursorScopeAreBounded(t *testing.T) {
	if got, err := aiActionPolicyRevisionPageSize(""); err != nil || got != defaultAIActionPolicyRevisionPageSize {
		t.Fatalf("default page size = %d, %v", got, err)
	}
	for _, raw := range []string{"0", "-1", "101", "many"} {
		if _, err := aiActionPolicyRevisionPageSize(raw); err == nil {
			t.Fatalf("invalid limit %q was accepted", raw)
		}
	}
	valid := encodeAIActionPolicyRevisionCursor(aiActionPolicyRevisionCursor{Version: 1, Operation: "ai.chat", Revision: 3, ID: uuid.Must(uuid.NewV4()).String()})
	if _, err := decodeAIActionPolicyRevisionCursor(valid, "delivery.plan"); err == nil {
		t.Fatal("cursor from another operation must be rejected")
	}
	if _, err := decodeAIActionPolicyRevisionCursor("not-a-cursor", "ai.chat"); err == nil {
		t.Fatal("malformed cursor must be rejected")
	}
}

func configureAIActionPolicyRevisionTestDB(t *testing.T) (*sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	return &mock, func() { configuration.DB = previousDB }
}

func configureAIActionPolicyTestRoot(t *testing.T, level int) {
	t.Helper()
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{ID: uuid.Must(uuid.FromString("00000000-0000-4000-8000-000000000099")), IsRoot: true, RootLevel: level}, nil
	}})
	t.Cleanup(restore)
}

func aiActionPolicyRevisionTestContext(method, target, operation string) (echo.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(method, target, nil), recorder)
	ctx.Set("cognito_sub", "ai-policy-revision-test")
	ctx.SetParamNames("operation")
	ctx.SetParamValues(operation)
	return ctx, recorder
}

func expectAIActionPolicyRevisionWrite(mock sqlmock.Sqlmock, policyID uuid.UUID, operation string, currentRevision int64, currentProvider, currentModel string, routes []models.AutomationAIActionRoute, actor string) {
	mock.ExpectBegin()
	expectAIActionPolicyBootstrapInsert(mock, routes[0], operation, actor)
	mock.ExpectQuery(`SELECT \* FROM "automation_ai_action_policies" WHERE operation = \$1 ORDER BY "automation_ai_action_policies"\."id" LIMIT \$2 FOR UPDATE`).
		WithArgs(operation, 1).
		WillReturnRows(aiActionPolicyRows(policyID, operation, currentRevision, currentProvider, currentModel, `[]`, actor))
	mock.ExpectExec(`UPDATE "automation_ai_action_policies" SET `).WillReturnResult(sqlmock.NewResult(0, 1))
	encoded := mustAIActionPolicyRoutesJSONForTest(routes)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(encoded)))
	mock.ExpectExec(`INSERT INTO automation_ai_action_policy_revisions`).
		WithArgs(policyID, operation, currentRevision+1, encoded, hash, actor, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
}

func expectAIActionPolicyBootstrapInsert(mock sqlmock.Sqlmock, primary models.AutomationAIActionRoute, operation, actor string) {
	mock.ExpectExec(`INSERT INTO automation_ai_action_policies`).
		WithArgs(operation, primary.Provider, primary.Model, primary.ReasoningEnabled, primary.ReasoningEffort, mustAIActionPolicyRoutesJSONForTest([]models.AutomationAIActionRoute{primary}), actor, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func aiActionPolicyRows(id uuid.UUID, operation string, revision int64, provider, model, routesJSON, actor string) *sqlmock.Rows {
	now := time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)
	return sqlmock.NewRows([]string{"id", "operation", "provider", "model", "reasoning_enabled", "reasoning_effort", "routes_json", "revision", "updated_by", "created_at", "updated_at"}).
		AddRow(id, operation, provider, model, false, "", routesJSON, revision, actor, now, now)
}

func mustAIActionPolicyRoutesJSON(t *testing.T, routes []models.AutomationAIActionRoute) string {
	t.Helper()
	return mustAIActionPolicyRoutesJSONForTest(routes)
}

func mustAIActionPolicyRoutesJSONForTest(routes []models.AutomationAIActionRoute) string {
	encoded, err := json.Marshal(routes)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
