package automation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/inferencecapability"
	"events-stocks/models"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testCredentialResolver struct {
	reads        int
	projectReads int
}

func (r *testCredentialResolver) APIKey(context.Context, string) (string, error) {
	r.reads++
	return "must-not-be-read", nil
}

func TestFallbackEligibleRequiresAnExplicitTemporaryProviderResponse(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		if !fallbackEligible(&automationagent.RetryableError{StatusCode: status}) {
			t.Fatalf("status %d should permit a fallback", status)
		}
	}
	for _, status := range []int{0, http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity} {
		if fallbackEligible(&automationagent.RetryableError{StatusCode: status}) {
			t.Fatalf("status %d must not permit a fallback", status)
		}
	}
}

func (*testCredentialResolver) ReplaceAPIKey(context.Context, string, string) error { return nil }

func (r *testCredentialResolver) APIKeyForProject(_ context.Context, projectID, provider string) (string, error) {
	r.projectReads++
	if projectID == "" || provider == "" {
		return "", context.Canceled
	}
	return "project-scoped-test-key", nil
}

func (*testCredentialResolver) HasAPIKeyForProject(context.Context, string, string) (bool, error) {
	return false, nil
}

func (*testCredentialResolver) ReplaceProjectAPIKey(context.Context, string, string, string) error {
	return nil
}

func (*testCredentialResolver) DeleteProjectAPIKey(context.Context, string, string) error {
	return nil
}

func TestInferenceProjectScopeNeverFallsBackToGlobalCredential(t *testing.T) {
	resolver := &testCredentialResolver{}
	key, err := inferenceAPIKey(context.Background(), resolver, "18a34b7d-3b57-4d61-b525-1394355f8601", "minimax")
	if err != nil || key != "project-scoped-test-key" || resolver.projectReads != 1 || resolver.reads != 0 {
		t.Fatalf("project credential resolution key=%q err=%v scoped_reads=%d global_reads=%d", key, err, resolver.projectReads, resolver.reads)
	}
	globalOnly := &globalCredentialOnlyResolver{}
	if key, err := inferenceAPIKey(context.Background(), globalOnly, "18a34b7d-3b57-4d61-b525-1394355f8601", "minimax"); err == nil || key != "" {
		t.Fatalf("project credential fell back to global namespace: key=%q err=%v", key, err)
	}
}

type failingCredentialResolver struct{ message string }

func (r failingCredentialResolver) APIKey(context.Context, string) (string, error) {
	return "", errors.New(r.message)
}

func (failingCredentialResolver) ReplaceAPIKey(context.Context, string, string) error { return nil }

func TestInferenceGatewaySuppressesCredentialResolverErrors(t *testing.T) {
	const sentinel = "secret-manager-error-containing-provider-key"
	_, err := completeWithFallbackForProject(context.Background(), failingCredentialResolver{message: sentinel}, inferenceRequest{}, []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3"}}, "")
	if err == nil || err.Error() != "AI gateway unavailable" || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("credential resolver details must not reach inference response handling: %v", err)
	}
}

type globalCredentialOnlyResolver struct{}

func (*globalCredentialOnlyResolver) APIKey(context.Context, string) (string, error) {
	return "global-must-not-be-used", nil
}

func (*globalCredentialOnlyResolver) ReplaceAPIKey(context.Context, string, string) error { return nil }

var _ credentialResolver = (*globalCredentialOnlyResolver)(nil)

func TestGatewayInferenceScopeUsesPersistedProjectAndFrozenActionRoutes(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previousDB := configuration.DB
	configuration.DB = db
	defer func() { configuration.DB = previousDB }()

	taskID, workItemID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	leaseExpires := time.Now().UTC().Add(5 * time.Minute)
	routes := []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3", ReasoningEnabled: true, ReasoningEffort: "medium"}}
	_, routesHash, err := canonicalInferenceRoutes(routes)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "automation_tasks" WHERE .*"id" = \$1`).
		WithArgs(taskID.String(), 1).
		WillReturnRows(sqlmock.NewRows([]string{"operation", "status", "run_id", "lease_expires_at", "max_completion_tokens", "delivery_work_item_id"}).
			AddRow("delivery.implementation", "running", "run-1", leaseExpires, 100, workItemID.String()))
	mock.ExpectQuery(`SELECT .* FROM "delivery_work_items" WHERE .*"id" = \$1`).
		WithArgs(workItemID.String(), 1).
		WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(projectID.String()))
	expectGatewayAttemptPolicyLookup(mock, newGatewayAttemptPolicySnapshot(t, taskID, "run-1", "delivery.implementation", &projectID, 100, 7, routes))
	mock.ExpectCommit()
	request := inferenceRequest{TaskID: taskID.String(), RunID: "run-1", Operation: "delivery.implementation", MaxCompletionTokens: 40}
	scope, policyConfigured, err := gatewayInferenceScopeForRequest(request)
	if err != nil || !policyConfigured || scope.ProjectID != projectID.String() || scope.PolicyRevision != 7 || scope.RoutesHash != routesHash || len(scope.Routes) != 1 || scope.Routes[0].Model != "MiniMax-M3" {
		t.Fatalf("inference scope=%#v err=%v policy_configured=%v; want persisted project and frozen policy", scope, err, policyConfigured)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayInferenceScopeDoesNotFreezeCurrentPolicyOnFirstUse(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previousDB := configuration.DB
	configuration.DB = db
	defer func() { configuration.DB = previousDB }()

	taskID := uuid.Must(uuid.NewV4())
	leaseExpires := time.Now().UTC().Add(5 * time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "automation_tasks" WHERE .*"id" = \$1`).
		WithArgs(taskID.String(), 1).
		WillReturnRows(sqlmock.NewRows([]string{"operation", "status", "run_id", "lease_expires_at", "max_completion_tokens", "delivery_work_item_id"}).
			AddRow("delivery.plan", "running", "run-2", leaseExpires, 100, nil))
	mock.ExpectQuery(`SELECT \* FROM "automation_inference_attempt_policies" WHERE automation_task_id = \$1 AND run_id = \$2 LIMIT \$3`).
		WithArgs(taskID.String(), "run-2", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id", "run_id", "operation", "project_id", "policy_revision", "routes_json", "routes_hash", "max_completion_tokens", "max_inference_calls", "snapshot_hash", "signature_key_id", "snapshot_signature", "created_at"}))
	mock.ExpectRollback()

	request := inferenceRequest{TaskID: taskID.String(), RunID: "run-2", Operation: "delivery.plan", MaxCompletionTokens: 90}
	scope, policyConfigured, err := gatewayInferenceScopeForRequest(request)
	if err == nil || policyConfigured || len(scope.Routes) != 0 {
		t.Fatalf("missing attempt recipe must reject first-use lookup: scope=%#v err=%v policy_configured=%v", scope, err, policyConfigured)
	}
	if expectErr := mock.ExpectationsWereMet(); expectErr != nil {
		t.Fatalf("first-use SQL expectations: %v; helper error: %v", expectErr, err)
	}
}

func TestGatewayInferenceScopeFailsClosedForCorruptFrozenRoutes(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previousDB := configuration.DB
	configuration.DB = db
	defer func() { configuration.DB = previousDB }()

	taskID := uuid.Must(uuid.NewV4())
	leaseExpires := time.Now().UTC().Add(5 * time.Minute)
	routes := []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3"}}
	snapshot := newGatewayAttemptPolicySnapshot(t, taskID, "run-3", "delivery.plan", nil, 100, 4, routes)
	snapshot.RoutesHash = strings.Repeat("0", 64)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "automation_tasks" WHERE .*"id" = \$1`).
		WithArgs(taskID.String(), 1).
		WillReturnRows(sqlmock.NewRows([]string{"operation", "status", "run_id", "lease_expires_at", "max_completion_tokens", "delivery_work_item_id"}).
			AddRow("delivery.plan", "running", "run-3", leaseExpires, 100, nil))
	expectGatewayAttemptPolicyLookup(mock, snapshot)
	mock.ExpectRollback()

	request := inferenceRequest{TaskID: taskID.String(), RunID: "run-3", Operation: "delivery.plan", MaxCompletionTokens: 90}
	scope, policyConfigured, err := gatewayInferenceScopeForRequest(request)
	if err == nil || policyConfigured || len(scope.Routes) != 0 {
		t.Fatalf("corrupt attempt snapshot must fail closed: scope=%#v configured=%v err=%v", scope, policyConfigured, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInferenceRequestCannotSupplyItsOwnProjectCredentialScope(t *testing.T) {
	var request inferenceRequest
	decoder := json.NewDecoder(strings.NewReader(`{"provider":"minimax","project_id":"attacker-selected"}`))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err == nil {
		t.Fatal("worker-controlled project_id was accepted by the inference contract")
	}
}

func TestInferRejectsUnboundGatewayRequestBeforeReadingCredentials(t *testing.T) {
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "callback-secret")
	previousResolver, previousDB := inferenceCredentials, configuration.DB
	defer func() { inferenceCredentials, configuration.DB = previousResolver, previousDB }()
	resolver := &testCredentialResolver{}
	ConfigureInferenceCredentials(resolver)
	configuration.DB = nil

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/internal/automation/inference", strings.NewReader(`{"provider":"minimax","model":"MiniMax-M3","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":10,"task_id":"not-a-uuid","run_id":"run","operation":"delivery.plan"}`))
	req.Header.Set("X-Automation-Secret", "callback-secret")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("config", &models.Config{AIProviderCredentialsSecretID: "bundle", AutomationBudgetProvider: "minimax", AutomationBudgetModel: "MiniMax-M3"})
	if err := Infer(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnauthorized || resolver.reads != 0 {
		t.Fatalf("callback-master-only request status=%d credential_reads=%d; want unauthorized before database/credentials", rec.Code, resolver.reads)
	}
}

func TestInferAcceptsOnlyMatchingScopedCapabilityWithoutCallbackMaster(t *testing.T) {
	serverSigningKey := strings.Repeat("s", 48)
	t.Setenv(attemptPolicySigningKeyEnv, serverSigningKey)
	t.Setenv(attemptPolicyPreviousSigningKeyEnv, "")
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "test-callback-master-secret")
	previousResolver, previousDB := inferenceCredentials, configuration.DB
	defer func() { inferenceCredentials, configuration.DB = previousResolver, previousDB }()
	resolver := &testCredentialResolver{}
	ConfigureInferenceCredentials(resolver)
	configuration.DB = nil

	requestBody := `{"call_id":"0df7d927-61d9-436d-bc8c-2c54f2941f20","provider":"minimax","model":"gateway-managed","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":10,"task_id":"not-a-uuid","run_id":"run-1","operation":"delivery.qa"}`
	capabilityScope := inferencecapability.Scope{
		TaskID: "not-a-uuid", RunID: "run-1", Operation: "delivery.qa",
		WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String(),
	}
	token, err := inferencecapability.Mint(serverSigningKey, capabilityScope, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	call := func(body, capability string) *httptest.ResponseRecorder {
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/api/internal/automation/inference", strings.NewReader(body))
		req.Header.Set(inferencecapability.HeaderName, capability)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Set("config", &models.Config{AIProviderCredentialsSecretID: "bundle"})
		if err := Infer(c); err != nil {
			t.Errorf("Infer returned an unexpected handler error: %v", err)
		}
		return rec
	}

	// Authentication succeeds and the request advances to the independent
	// database lease check (which deliberately fails because this test has no DB).
	if rec := call(requestBody, token); rec.Code != http.StatusConflict || resolver.reads != 0 {
		t.Fatalf("matching capability status=%d credential_reads=%d; want current DB lease rejection before credentials", rec.Code, resolver.reads)
	}
	var mismatched map[string]any
	if err := json.Unmarshal([]byte(requestBody), &mismatched); err != nil {
		t.Fatal(err)
	}
	mismatched["operation"] = "delivery.plan"
	wrongScopeBody, err := json.Marshal(mismatched)
	if err != nil {
		t.Fatal(err)
	}
	if rec := call(string(wrongScopeBody), token); rec.Code != http.StatusUnauthorized || resolver.reads != 0 {
		t.Fatalf("mismatched capability status=%d credential_reads=%d; want unauthorized", rec.Code, resolver.reads)
	}
}

func TestInferRejectsCallbackMasterAndCapabilityLocallySignedByWorker(t *testing.T) {
	callbackMaster := strings.Repeat("c", 48)
	serverSigningKey := strings.Repeat("s", 48)
	t.Setenv("AUTOMATION_CALLBACK_SECRET", callbackMaster)
	t.Setenv(attemptPolicySigningKeyEnv, serverSigningKey)
	t.Setenv(attemptPolicyPreviousSigningKeyEnv, "")
	previousResolver, previousDB := inferenceCredentials, configuration.DB
	defer func() { inferenceCredentials, configuration.DB = previousResolver, previousDB }()
	resolver := &testCredentialResolver{}
	ConfigureInferenceCredentials(resolver)
	configuration.DB = nil
	requestBody := `{"provider":"minimax","model":"gateway-managed","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":10,"task_id":"task-master-test","run_id":"run-master-test","operation":"delivery.qa"}`
	workerScope := inferencecapability.Scope{
		TaskID: "task-master-test", RunID: "run-master-test", Operation: "delivery.qa",
		WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String(),
	}
	locallySigned, err := inferencecapability.Mint(callbackMaster, workerScope, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	call := func(capability, callback string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/internal/automation/inference", strings.NewReader(requestBody))
		if capability != "" {
			req.Header.Set(inferencecapability.HeaderName, capability)
		}
		if callback != "" {
			req.Header.Set("X-Automation-Secret", callback)
		}
		rec := httptest.NewRecorder()
		ctx := echo.New().NewContext(req, rec)
		ctx.Set("config", &models.Config{AIProviderCredentialsSecretID: "bundle"})
		if err := Infer(ctx); err != nil {
			t.Errorf("Infer returned handler error: %v", err)
		}
		return rec
	}
	if rec := call("", callbackMaster); rec.Code != http.StatusUnauthorized || resolver.reads != 0 {
		t.Fatalf("master-only inference status=%d credential_reads=%d; want unauthorized", rec.Code, resolver.reads)
	}
	if rec := call(locallySigned, callbackMaster); rec.Code != http.StatusUnauthorized || resolver.reads != 0 {
		t.Fatalf("worker-signed inference status=%d credential_reads=%d; want unauthorized", rec.Code, resolver.reads)
	}
}
