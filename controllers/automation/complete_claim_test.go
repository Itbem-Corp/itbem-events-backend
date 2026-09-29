package automation

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestCompleteForwardsAuthenticatedWorkerIdentityToRunningClaim(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	originalDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = originalDB })

	taskID := uuid.Must(uuid.NewV4())
	runID := uuid.Must(uuid.NewV4()).String()
	identity := testClaimIdentity()
	mock.ExpectQuery(`SELECT \* FROM "automation_tasks" WHERE .*"id" = \$1.*LIMIT \$2`).
		WithArgs(taskID.String(), 1).
		WillReturnRows(automationTaskRows(taskID, "delivery.plan", "queued", "", 0, nil, nil))
	expectClaimWorkerIdentity(mock, taskID, "delivery.plan")

	body, err := json.Marshal(callbackRequest{
		Status:       "running",
		RunID:        runID,
		WorkerID:     identity.WorkerID,
		AgentKey:     identity.AgentKey,
		MachineID:    identity.MachineID,
		ProgressCall: automationagent.AgentMaxCalls + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/internal/automation/tasks/"+taskID.String(), bytes.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	ctx := echo.New().NewContext(request, recorder)
	ctx.SetPath("/api/internal/automation/tasks/:id")
	ctx.SetParamNames("id")
	ctx.SetParamValues(taskID.String())
	ctx.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{
		InstanceID: uuid.Must(uuid.NewV4()),
		AgentKey:   identity.AgentKey,
		MachineID:  identity.MachineID,
	})

	if err := Complete(ctx); err != nil {
		t.Fatalf("complete handler error: %v", err)
	}
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "progress label is invalid") {
		t.Fatalf("running callback identity was not forwarded to claim validation: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
