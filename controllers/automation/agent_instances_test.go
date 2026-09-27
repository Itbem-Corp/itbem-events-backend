package automation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestAgentInstanceEndpointsRequirePlatformWorkspace(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	endpoints := []struct {
		name    string
		request func(echo.Context) error
	}{
		{name: "register", request: RegisterAgentInstance},
		{name: "list", request: ListAgentInstances},
		{name: "revoke", request: RevokeAgentInstance},
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			newContext := func() (echo.Context, *httptest.ResponseRecorder) {
				recorder := httptest.NewRecorder()
				ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/api/automation/agent-instances", nil), recorder)
				ctx.Set("cognito_sub", "agent-instance-workspace-test")
				return ctx, recorder
			}

			organizationContext, organizationRecorder := newContext()
			organizationContext.Set("workspace_mode", "organization")
			organizationContext.Set("organization_id", uuid.Must(uuid.NewV4()))
			if err := endpoint.request(organizationContext); err != nil {
				t.Fatal(err)
			}
			if organizationRecorder.Code != http.StatusNotFound {
				t.Fatalf("organization workspace status = %d, want %d: %s", organizationRecorder.Code, http.StatusNotFound, organizationRecorder.Body.String())
			}

			tenantContext, tenantRecorder := newContext()
			tenantContext.Set("workspace_mode", "platform")
			tenantContext.Set("tenant_code", "caffetton")
			if err := endpoint.request(tenantContext); err != nil {
				t.Fatal(err)
			}
			if tenantRecorder.Code != http.StatusForbidden {
				t.Fatalf("tenant surface status = %d, want %d: %s", tenantRecorder.Code, http.StatusForbidden, tenantRecorder.Body.String())
			}

			platformContext, platformRecorder := newContext()
			platformContext.Set("workspace_mode", "platform")
			if err := endpoint.request(platformContext); err != nil {
				t.Fatal(err)
			}
			if platformRecorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("platform root should reach DB readiness check; status = %d, want %d: %s", platformRecorder.Code, http.StatusServiceUnavailable, platformRecorder.Body.String())
			}
		})
	}
}

func TestGatewayAgentEnrollmentRequiresExactLaneGatewayToken(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "test-only-callback-root-secret")

	for _, test := range []struct {
		name   string
		role   string
		lane   string
		token  string
		status int
	}{
		{name: "missing credential", role: "reviewer", lane: "review", status: http.StatusUnauthorized},
		{name: "token for another lane", role: "release_manager", lane: "release", token: deriveGatewayToken("test-only-callback-root-secret", gatewayIdentity{Role: "reviewer", Lane: "review"}), status: http.StatusUnauthorized},
		{name: "valid reviewer lane token", role: "reviewer", lane: "review", token: deriveGatewayToken("test-only-callback-root-secret", gatewayIdentity{Role: "reviewer", Lane: "review"}), status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/internal/automation/agent-instances/enroll", strings.NewReader(`{}`))
			request.Header.Set("X-Agent-Role", test.role)
			request.Header.Set("X-Agent-Lane", test.lane)
			if test.token != "" {
				request.Header.Set("X-Agent-Gateway-Token", test.token)
			}
			ctx := echo.New().NewContext(request, recorder)
			if err := EnrollGatewayAgentInstance(ctx); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != test.status {
				t.Fatalf("enrollment status = %d, want %d: %s", recorder.Code, test.status, recorder.Body.String())
			}
		})
	}
}

func TestGatewayAgentEnrollmentCreatesOnlySignedMachineIdentity(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	callbackRoot := strings.Repeat("c", 48)
	t.Setenv("AUTOMATION_CALLBACK_SECRET", callbackRoot)

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := agentcallbackauth.EncodePublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	machineID := uuid.Must(uuid.NewV4()).String()
	agentKey := "generalist"
	message, err := agentcallbackauth.AgentInstanceEnrollmentMessage(agentKey, machineID, encodedKey)
	if err != nil {
		t.Fatal(err)
	}
	encodedSignature, err := agentcallbackauth.EncodeSignature(ed25519.Sign(privateKey, message))
	if err != nil {
		t.Fatal(err)
	}
	requestBody, err := json.Marshal(gatewayAgentInstanceEnrollmentRequest{AgentKey: agentKey, MachineID: machineID, PublicKey: encodedKey, Signature: encodedSignature})
	if err != nil {
		t.Fatal(err)
	}
	instanceID := uuid.Must(uuid.NewV4())
	now := time.Now().UTC()
	profileRows := sqlmock.NewRows([]string{"id", "agent_key", "name", "specialty", "description", "operations_json", "capabilities_json", "active", "created_at", "updated_at"}).
		AddRow(uuid.Must(uuid.NewV4()), agentKey, "Generalist", "general", "test", `["code.review"]`, `[]`, true, now, now)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "automation_agent_profiles" WHERE agent_key = $1 AND active = $2 ORDER BY "automation_agent_profiles"."id" LIMIT $3`)).
		WithArgs(agentKey, true, 1).WillReturnRows(profileRows)
	mock.ExpectBegin()
	instanceColumns := []string{"id", "agent_key", "machine_id", "public_key", "public_key_fingerprint", "status", "last_seen_at", "revoked_at", "created_at", "updated_at"}
	mock.ExpectQuery(`SELECT \* FROM "automation_agent_instances" WHERE agent_key = \$1 AND machine_id = \$2 AND status = \$3 ORDER BY created_at DESC, id DESC.*FOR UPDATE`).
		WithArgs(agentKey, machineID, "active", 1).WillReturnRows(sqlmock.NewRows(instanceColumns))
	mock.ExpectQuery(`SELECT \* FROM "automation_agent_instances" WHERE agent_key = \$1 AND machine_id = \$2 ORDER BY created_at DESC, id DESC.*FOR UPDATE`).
		WithArgs(agentKey, machineID, 1).WillReturnRows(sqlmock.NewRows(instanceColumns))
	mock.ExpectQuery(`INSERT INTO "automation_agent_instances" .*ON CONFLICT DO NOTHING RETURNING "id"`).
		WithArgs(agentKey, machineID, encodedKey, sqlmock.AnyArg(), "active", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(instanceID))
	mock.ExpectCommit()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/internal/automation/agent-instances/enroll", strings.NewReader(string(requestBody)))
	request.Header.Set("X-Agent-Role", "reviewer")
	request.Header.Set("X-Agent-Lane", "review")
	request.Header.Set("X-Agent-Gateway-Token", deriveGatewayToken(callbackRoot, gatewayIdentity{Role: "reviewer", Lane: "review"}))
	ctx := echo.New().NewContext(request, recorder)
	if err := EnrollGatewayAgentInstance(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("enrollment status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), encodedKey) || !strings.Contains(recorder.Body.String(), instanceID.String()) {
		t.Fatalf("enrollment response should expose only the issued ID/fingerprint: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations were not met: %v", err)
	}
}

func TestGatewayAgentEnrollmentIsIdempotentAndCannotUndoRevocation(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		status     string
		wantStatus int
	}{
		{name: "active identity is idempotent", status: "active", wantStatus: http.StatusOK},
		{name: "revoked identity requires primary root", status: "revoked", wantStatus: http.StatusConflict},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, mock := automationCostLedgerTestDB(t)
			previousDB := configuration.DB
			configuration.DB = db
			t.Cleanup(func() { configuration.DB = previousDB })
			callbackRoot := strings.Repeat("c", 48)
			t.Setenv("AUTOMATION_CALLBACK_SECRET", callbackRoot)

			publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			encodedKey, err := agentcallbackauth.EncodePublicKey(publicKey)
			if err != nil {
				t.Fatal(err)
			}
			agentKey := "generalist"
			machineID := uuid.Must(uuid.NewV4()).String()
			message, err := agentcallbackauth.AgentInstanceEnrollmentMessage(agentKey, machineID, encodedKey)
			if err != nil {
				t.Fatal(err)
			}
			signature, err := agentcallbackauth.EncodeSignature(ed25519.Sign(privateKey, message))
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(gatewayAgentInstanceEnrollmentRequest{AgentKey: agentKey, MachineID: machineID, PublicKey: encodedKey, Signature: signature})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			profileID, instanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			profileRows := sqlmock.NewRows([]string{"id", "agent_key", "name", "specialty", "description", "operations_json", "capabilities_json", "active", "created_at", "updated_at"}).
				AddRow(profileID, agentKey, "Generalist", "general", "test", `["code.review"]`, `[]`, true, now, now)
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "automation_agent_profiles" WHERE agent_key = $1 AND active = $2 ORDER BY "automation_agent_profiles"."id" LIMIT $3`)).
				WithArgs(agentKey, true, 1).WillReturnRows(profileRows)
			mock.ExpectBegin()
			instanceColumns := []string{"id", "agent_key", "machine_id", "public_key", "public_key_fingerprint", "status", "last_seen_at", "revoked_at", "created_at", "updated_at"}
			activeRows := sqlmock.NewRows(instanceColumns)
			if scenario.status == "active" {
				activeRows.AddRow(instanceID, agentKey, machineID, encodedKey, "sha256:test", "active", nil, nil, now, now)
			}
			mock.ExpectQuery(`SELECT \* FROM "automation_agent_instances" WHERE agent_key = \$1 AND machine_id = \$2 AND status = \$3 ORDER BY created_at DESC, id DESC.*FOR UPDATE`).
				WithArgs(agentKey, machineID, "active", 1).WillReturnRows(activeRows)
			if scenario.status == "revoked" {
				mock.ExpectQuery(`SELECT \* FROM "automation_agent_instances" WHERE agent_key = \$1 AND machine_id = \$2 ORDER BY created_at DESC, id DESC.*FOR UPDATE`).
					WithArgs(agentKey, machineID, 1).WillReturnRows(sqlmock.NewRows(instanceColumns).AddRow(instanceID, agentKey, machineID, encodedKey, "sha256:test", "revoked", nil, now, now, now))
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/internal/automation/agent-instances/enroll", strings.NewReader(string(body)))
			request.Header.Set("X-Agent-Role", "reviewer")
			request.Header.Set("X-Agent-Lane", "review")
			request.Header.Set("X-Agent-Gateway-Token", deriveGatewayToken(callbackRoot, gatewayIdentity{Role: "reviewer", Lane: "review"}))
			ctx := echo.New().NewContext(request, recorder)
			if err := EnrollGatewayAgentInstance(ctx); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != scenario.wantStatus {
				t.Fatalf("enrollment status = %d, want %d: %s", recorder.Code, scenario.wantStatus, recorder.Body.String())
			}
			if scenario.status == "active" && !strings.Contains(recorder.Body.String(), instanceID.String()) {
				t.Fatalf("idempotent enrollment did not return the existing ID: %s", recorder.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("database expectations were not met: %v", err)
			}
		})
	}
}

func TestAgentInstanceCursorScopeBindsAgentAndStatusFilters(t *testing.T) {
	createdAt := time.Date(2026, 9, 24, 12, 30, 0, 0, time.UTC)
	cursor := agentInstanceCursor{Version: 1, Scope: agentInstanceCursorScope("generalist", "active"), CreatedAt: createdAt, ID: uuid.Must(uuid.NewV4()).String()}
	encoded := encodeAgentInstanceCursor(cursor)
	if _, err := decodeAgentInstanceCursor(encoded, agentInstanceCursorScope("generalist", "active")); err != nil {
		t.Fatalf("cursor should decode under its original filters: %v", err)
	}
	for _, changedScope := range []string{
		agentInstanceCursorScope("reviewer", "active"),
		agentInstanceCursorScope("generalist", "revoked"),
		agentInstanceCursorScope("reviewer", "revoked"),
	} {
		if _, err := decodeAgentInstanceCursor(encoded, changedScope); err == nil {
			t.Fatalf("cursor was accepted after filters changed to scope %s", changedScope)
		}
	}
	if agentInstanceCursorScope(" generalist ", " active ") != agentInstanceCursorScope("generalist", "active") {
		t.Fatal("cursor scope should normalize surrounding filter whitespace")
	}
}

func TestProjectAgentInstanceOmitsLegacyMachineLabel(t *testing.T) {
	instance := models.AutomationAgentInstance{ID: uuid.Must(uuid.NewV4()), AgentKey: "generalist", MachineID: "legacy-workstation-hostname", Status: "revoked"}
	dto := projectAgentInstance(instance)
	if dto.MachineID != "" {
		t.Fatalf("invalid historical machine identity should be omitted, got %q", dto.MachineID)
	}
	encoded, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "legacy-workstation-hostname") || strings.Contains(string(encoded), `"machine_id"`) {
		t.Fatalf("historical machine label was serialized: %s", encoded)
	}
}
