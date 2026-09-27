package automation

import (
	"encoding/json"
	"strings"
	"testing"

	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
)

func TestInferenceReceiptCallbackBindingRejectsForeignRunOrIdentity(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		callID     string
		runID      string
		identity   automationagent.AgentIdentity
		wantAccept bool
	}{
		{name: "bound receipt", wantAccept: true},
		{name: "wrong call id", callID: uuid.Must(uuid.NewV4()).String()},
		{name: "wrong run", runID: "different-run"},
		{name: "wrong worker", identity: automationagent.AgentIdentity{WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String()}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			mock := setupGatewayAttemptPolicyDB(t)
			taskID, receiptID, callID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			identity := automationagent.AgentIdentity{WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String()}
			lookupCallID, lookupRunID, lookupIdentity := callID.String(), "run-receipt", identity
			if scenario.callID != "" {
				lookupCallID = scenario.callID
			}
			if scenario.runID != "" {
				lookupRunID = scenario.runID
			}
			if scenario.identity.WorkerID != "" {
				lookupIdentity = scenario.identity
			}
			mock.ExpectQuery(`SELECT \* FROM "automation_inference_receipts" WHERE id = \$1 LIMIT \$2`).
				WithArgs(receiptID.String(), 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id", "run_id", "call_id", "worker_id", "agent_key", "machine_id", "status", "provider", "model", "input_tokens", "output_tokens", "usage_json"}).
					AddRow(receiptID.String(), taskID.String(), "run-receipt", callID.String(), identity.WorkerID, identity.AgentKey, identity.MachineID, "accepted", "minimax", "MiniMax-M3", 10, 4, `{"input_tokens":10,"output_tokens":4,"total_tokens":14}`))

			_, err := resolveAutomationInferenceReceipt(configuration.DB, receiptID.String(), lookupCallID, taskID, lookupRunID, lookupIdentity)
			if scenario.wantAccept && err != nil {
				t.Fatalf("matching receipt was rejected: %v", err)
			}
			if !scenario.wantAccept && err == nil {
				t.Fatal("foreign callback reference was accepted")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInferenceReceiptStatusMustMatchCallbackOutcome(t *testing.T) {
	for _, test := range []struct {
		callbackStatus string
		receiptStatus  string
		want           bool
	}{
		{callbackStatus: "completed", receiptStatus: "accepted", want: true},
		{callbackStatus: "completed", receiptStatus: "rejected"},
		{callbackStatus: "failed", receiptStatus: "accepted", want: true},
		{callbackStatus: "failed", receiptStatus: "rejected", want: true},
		{callbackStatus: "running", receiptStatus: "accepted"},
	} {
		if got := inferenceReceiptStatusAllowsCallback(models.AutomationInferenceReceipt{Status: test.receiptStatus}, test.callbackStatus); got != test.want {
			t.Errorf("receipt %q with callback %q accepted=%v, want %v", test.receiptStatus, test.callbackStatus, got, test.want)
		}
	}
}

func TestVerifiedToolLedgerIgnoresCallbackProviderUsageAndCost(t *testing.T) {
	mock := setupGatewayAttemptPolicyDB(t)
	taskID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	receiptID, callID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	identity := automationagent.AgentIdentity{WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "implementation_specialist", MachineID: uuid.Must(uuid.NewV4()).String()}
	runID := "run-authoritative-receipt"
	config := &models.Config{AutomationOutputBucket: "itbem-ai-outputs-test"}
	task := &models.AutomationTask{ID: taskID, DeliveryWorkItemID: &workItemID, Operation: "delivery.implementation"}
	prefix := "s3://" + config.AutomationOutputBucket + "/automation/" + taskID.String() + "/runs/" + runID + "/steps/call-2"
	usageJSON := `{"input_tokens":10,"output_tokens":4,"total_tokens":14}`
	mock.ExpectQuery(`SELECT \* FROM "automation_inference_receipts" WHERE id = \$1 LIMIT \$2`).
		WithArgs(receiptID.String(), 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "automation_task_id", "run_id", "call_id", "worker_id", "agent_key", "machine_id", "status", "provider", "model",
			"input_tokens", "output_tokens", "cached_input_tokens", "cache_write_tokens", "reasoning_tokens", "total_tokens",
			"input_cost_micros", "output_cost_micros", "cached_cost_micros", "cache_write_cost_micros", "total_cost_micros",
			"currency", "pricing_basis", "pricing_snapshot_json", "usage_json",
		}).AddRow(
			receiptID.String(), taskID.String(), runID, callID.String(), identity.WorkerID, identity.AgentKey, identity.MachineID,
			"accepted", "minimax", "MiniMax-M3", 10, 4, 2, 0, 1, 14, 3, 7, 1, 0, 11, "USD", "test-catalog", `{"version":"immutable"}`, usageJSON,
		))

	rows, err := buildVerifiedToolExecutionLedger(configuration.DB, config, task, runID, "failed", []callbackToolExecution{{
		Tool: "agent_loop", CallKey: "call-2", CallID: callID.String(), ReceiptID: receiptID.String(), CallStatus: "completed",
		StepKey: "implementation.agent", Provider: "openai", Model: "forged-model", Usage: json.RawMessage(`{"input_tokens":999999,"output_tokens":999999}`),
		RequestRef: prefix + "/request.json", ResponseRef: prefix + "/response.json",
	}}, nil, identity, uuid.Nil, time.Now().UTC())
	if err != nil || len(rows) != 1 {
		t.Fatalf("receipt-backed tool accounting failed: rows=%#v err=%v", rows, err)
	}
	row := rows[0]
	if row.Provider != "minimax" || row.Model != "MiniMax-M3" || row.InputTokens != 10 || row.OutputTokens != 4 || row.TotalCostMicros != 11 || row.PricingBasis != "test-catalog" || row.InferenceReceiptID == nil || *row.InferenceReceiptID != receiptID || row.UsageJSON != usageJSON {
		t.Fatalf("callback-supplied accounting was trusted instead of the gateway receipt: %#v", row)
	}
	if strings.Contains(row.UsageJSON, "999999") || strings.Contains(row.PricingSnapshotJSON, "forged-model") {
		t.Fatal("forged callback usage/model reached the immutable tool ledger")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReceiptUsageAllowlistDropsArbitraryProviderText(t *testing.T) {
	private := "private prompt / completion / provider extension"
	usage := sanitizeProviderUsage(map[string]any{
		"input_tokens": float64(12), "output_tokens": float64(7), "total_tokens": float64(19),
		"prompt": private, "response": private, "api_key": private, "extension": map[string]any{"content": private},
		"prompt_tokens_details": map[string]any{"cached_tokens": float64(3), "secret": private},
		"_itbem_provider":       map[string]any{"finish_reason": "stop", "input_sensitive": true, "arbitrary_text": private},
	})
	encoded, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), private) || strings.Contains(string(encoded), "api_key") || strings.Contains(string(encoded), "extension") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("arbitrary provider data reached the receipt usage ledger: %s", encoded)
	}
	if usage["input_tokens"] != float64(12) || usage["output_tokens"] != float64(7) || usage["total_tokens"] != float64(19) {
		t.Fatalf("known accounting fields were lost: %#v", usage)
	}
}
