package automation

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/agentprotocol"
	"events-stocks/models"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

func TestAgentHeartbeatAttributesRegisteredInstanceFromAuthenticatedContext(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })

	authenticatedInstanceID := uuid.Must(uuid.NewV4())
	spoofedInstanceID := uuid.Must(uuid.NewV4())
	machineID := uuid.Must(uuid.NewV4()).String()
	workerID := uuid.Must(uuid.NewV4()).String()
	now := time.Now().UTC().Truncate(time.Second)
	profileRows := sqlmock.NewRows([]string{"id", "agent_key", "name", "specialty", "description", "operations_json", "capabilities_json", "active", "created_at", "updated_at"}).
		AddRow(uuid.Must(uuid.NewV4()), "generalist", "Generalist", "general", "test profile", `[]`, `[]`, true, now, now)
	mock.ExpectQuery(`SELECT \* FROM "automation_agent_profiles" WHERE agent_key = \$1 AND active = \$2 ORDER BY "automation_agent_profiles"\."id" LIMIT \$3`).
		WithArgs("generalist", true, 1).WillReturnRows(profileRows)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "automation_agent_heartbeats".*RETURNING "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.Must(uuid.NewV4())))
	mock.ExpectCommit()

	var captured models.AutomationAgentHeartbeat
	var createError error
	if err := db.Callback().Create().Before("gorm:create").Register("test:capture_agent_heartbeat", func(tx *gorm.DB) {
		if heartbeat, ok := tx.Statement.Dest.(*models.AutomationAgentHeartbeat); ok {
			captured = *heartbeat
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Create().After("gorm:create").Register("test:capture_agent_heartbeat_error", func(tx *gorm.DB) {
		createError = tx.Error
	}); err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(map[string]any{
		"worker_id": workerID, "agent_key": "generalist", "machine_id": machineID,
		"agent_instance_id": spoofedInstanceID.String(), "provider": "minimax", "model": "MiniMax-M3",
		"concurrency": 1, "capabilities": []string{}, "started_at": now.Format(time.RFC3339),
		"workspace_readiness": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/internal/automation/agents/heartbeat", bytes.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	ctx := echo.New().NewContext(request, response)
	ctx.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{InstanceID: authenticatedInstanceID, AgentKey: "generalist", MachineID: machineID})
	if err := AgentHeartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("heartbeat status=%d, want 200: %s; create error: %v; SQL expectations: %v", response.Code, response.Body.String(), createError, mock.ExpectationsWereMet())
	}
	if captured.AgentInstanceID == nil || *captured.AgentInstanceID != authenticatedInstanceID {
		t.Fatalf("persisted instance attribution=%v, want authenticated instance %s", captured.AgentInstanceID, authenticatedInstanceID)
	}
	if *captured.AgentInstanceID == spoofedInstanceID {
		t.Fatal("heartbeat trusted agent_instance_id from the request body")
	}
	if captured.ProtocolsJSON != "[]" {
		t.Fatalf("legacy heartbeat protocols JSON = %q, want normalized empty list", captured.ProtocolsJSON)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentHeartbeatPersistsAllowlistedRuntimeProtocol(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })

	workerID := uuid.Must(uuid.NewV4()).String()
	machineID := uuid.Must(uuid.NewV4()).String()
	instanceID := uuid.Must(uuid.NewV4())
	now := time.Now().UTC().Truncate(time.Second)
	profileRows := sqlmock.NewRows([]string{"id", "agent_key", "name", "specialty", "description", "operations_json", "capabilities_json", "active", "created_at", "updated_at"}).
		AddRow(uuid.Must(uuid.NewV4()), "generalist", "Generalist", "general", "test profile", `[]`, `[]`, true, now, now)
	mock.ExpectQuery(`SELECT \* FROM "automation_agent_profiles" WHERE agent_key = \$1 AND active = \$2 ORDER BY "automation_agent_profiles"\."id" LIMIT \$3`).
		WithArgs("generalist", true, 1).WillReturnRows(profileRows)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)INSERT INTO "automation_agent_heartbeats".*protocols_json.*DO UPDATE SET.*protocols_json.*RETURNING "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.Must(uuid.NewV4())))
	mock.ExpectCommit()

	var captured models.AutomationAgentHeartbeat
	if err := db.Callback().Create().Before("gorm:create").Register("test:capture_protocol_agent_heartbeat", func(tx *gorm.DB) {
		if heartbeat, ok := tx.Statement.Dest.(*models.AutomationAgentHeartbeat); ok {
			captured = *heartbeat
		}
	}); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"worker_id": workerID, "agent_key": "generalist", "machine_id": machineID,
		"provider": "minimax", "model": "MiniMax-M3", "concurrency": 1,
		"protocols":  []string{agentprotocol.ProtocolDeliveryPlanStepsV1},
		"started_at": now.Format(time.RFC3339), "workspace_readiness": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/internal/automation/agents/heartbeat", bytes.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	ctx := echo.New().NewContext(request, response)
	ctx.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{InstanceID: instanceID, AgentKey: "generalist", MachineID: machineID})
	if err := AgentHeartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("heartbeat status=%d, want 200: %s; SQL expectations: %v", response.Code, response.Body.String(), mock.ExpectationsWereMet())
	}
	if captured.ProtocolsJSON != `["delivery.plan_steps.v1"]` {
		t.Fatalf("persisted protocols = %q", captured.ProtocolsJSON)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeWorkerProtocolsOnlyAcceptsAllowlistedVersion(t *testing.T) {
	protocols, err := normalizeWorkerProtocols([]string{" " + agentprotocol.ProtocolDeliveryPlanStepsV1 + " "})
	if err != nil {
		t.Fatalf("normalize allowlisted protocol: %v", err)
	}
	if len(protocols) != 1 || protocols[0] != agentprotocol.ProtocolDeliveryPlanStepsV1 {
		t.Fatalf("normalized protocols = %#v", protocols)
	}

	for _, input := range [][]string{{"unknown.protocol.v9"}, {agentprotocol.ProtocolDeliveryPlanStepsV1, agentprotocol.ProtocolDeliveryPlanStepsV1}, {""}} {
		if _, err := normalizeWorkerProtocols(input); err == nil {
			t.Fatalf("normalizeWorkerProtocols(%#v) unexpectedly succeeded", input)
		}
	}
	if protocols, err := normalizeWorkerProtocols(nil); err != nil || len(protocols) != 0 {
		t.Fatalf("legacy missing protocols = %#v, %v; want empty list without error", protocols, err)
	}
}

func TestSafeWorkerProtocolsJSONFiltersUnrecognizedPersistedValues(t *testing.T) {
	if got := safeWorkerProtocolsJSON(`["delivery.plan_steps.v1"]`); len(got) != 1 || got[0] != agentprotocol.ProtocolDeliveryPlanStepsV1 {
		t.Fatalf("safe known protocol projection = %#v", got)
	}
	for _, raw := range []string{"", `null`, `not-json`, `["delivery.plan_steps.v1", "private.protocol"]`} {
		if got := safeWorkerProtocolsJSON(raw); len(got) != 0 {
			t.Fatalf("unsafe protocol JSON %q projected as %#v", raw, got)
		}
	}
}
