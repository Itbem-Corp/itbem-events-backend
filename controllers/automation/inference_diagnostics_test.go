package automation

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiagnosticPersistenceDoesNotMutateReceiptLifecycle(t *testing.T) {
	_, mock, cleanup := attemptPolicyClaimDB(t)
	defer cleanup()
	receiptID := uuid.Must(uuid.NewV4())
	mock.ExpectBegin()
	// Exact SET projection guards against an implicit updated_at mutation.
	mock.ExpectExec(`UPDATE "automation_inference_receipts" SET "diagnostics_json"=\$1 WHERE id = \$2`).WithArgs(sqlmock.AnyArg(), receiptID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	persistInferenceDiagnostics(receiptID, &inferenceDiagnostics{SchemaVersion: 1, Stage: "reserved"})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInferenceInspectionRequiresPrimaryRootBeforeStorage(t *testing.T) {
	configureAIActionPolicyTestRoot(t, 2)
	for _, handler := range []func(echo.Context) error{GetInferenceDiagnostics, InspectInferenceTaskContent} {
		response := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/inspection", nil), response)
		c.Set("cognito_sub", "synthetic-operator")
		if err := handler(c); err != nil {
			t.Fatal(err)
		}
		if response.Code != 403 {
			t.Fatalf("inspection accepted non-root: %d", response.Code)
		}
	}
}

func TestPrivateInferenceContentBindsReceiptAndFailsClosedWithoutAudit(t *testing.T) {
	for _, auditFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "wrong-task-receipt", true: "audit-unavailable"}[auditFailure], func(t *testing.T) {
			configureAIActionPolicyTestRoot(t, 1)
			_, mock, cleanup := attemptPolicyClaimDB(t)
			defer cleanup()
			taskID, receiptID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			mock.ExpectQuery(`SELECT .* FROM "automation_tasks"`).WithArgs(taskID, 1).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(taskID))
			query := mock.ExpectQuery(`SELECT .* FROM "automation_inference_receipts" WHERE id = \$1 AND automation_task_id = \$2`).WithArgs(receiptID, taskID, 1)
			want := 404
			if auditFailure {
				query.WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id"}).AddRow(receiptID, taskID))
				mock.ExpectBegin()
				mock.ExpectQuery(`INSERT INTO "audit_logs"`).WillReturnError(errors.New("synthetic audit failure"))
				mock.ExpectRollback()
				want = 503
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"id"}))
			}
			response := httptest.NewRecorder()
			c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/inspection", nil), response)
			c.Set("cognito_sub", "synthetic-operator")
			c.Set("config", &models.Config{AutomationInputBucket: "synthetic-private-bucket"})
			c.SetParamNames("id", "receipt")
			c.SetParamValues(taskID.String(), receiptID.String())
			if err := InspectInferenceTaskContent(c); err != nil {
				t.Fatal(err)
			}
			if response.Code != want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestInferenceDiagnosticHashesMessageContentWithoutExportingIt(t *testing.T) {
	request := inferenceRequest{Messages: []automationagent.Message{{Role: "user", Content: "synthetic private prompt"}}, MaxCompletionTokens: 4096}
	first := startInferenceDiagnostics(request, gatewayInferenceScope{PolicyHash: "sealed"})
	request.Messages[0].Content = "changed"
	second := startInferenceDiagnostics(request, gatewayInferenceScope{})
	if first.RequestHash == second.RequestHash || len(first.RequestHash) != 64 || first.MessageCount != 1 {
		t.Fatal("invalid request provenance")
	}
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), "synthetic private prompt") {
		t.Fatal("prompt entered metadata")
	}
	if safeProviderDiagnostic(&automationagent.RetryableError{Message: "safe", Cause: context.DeadlineExceeded}) != "provider_timeout" {
		t.Fatal("timeout lost its safe classification")
	}
}

func TestInferenceDiagnosticsDoNotReportAmbiguousUsageAsVerified(t *testing.T) {
	configureAIActionPolicyTestRoot(t, 1)
	_, mock, cleanup := attemptPolicyClaimDB(t)
	defer cleanup()
	taskID := uuid.Must(uuid.NewV4())
	mock.ExpectQuery(`SELECT .* FROM "automation_inference_receipts"`).WithArgs(taskID, 100).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status", "input_tokens", "total_cost_micros", "usage_json"}).
			AddRow(uuid.Must(uuid.NewV4()), "ambiguous", 99, 123, `{"private":"never expose"}`))
	response := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/diagnostics", nil), response)
	c.Set("cognito_sub", "synthetic-operator")
	c.SetParamNames("id")
	c.SetParamValues(taskID.String())
	if err := GetInferenceDiagnostics(c); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	if response.Code != 200 || !strings.Contains(body, `"input_tokens":null`) || !strings.Contains(body, `"total_cost_microusd":null`) || strings.Contains(body, "never expose") {
		t.Fatalf("unverified accounting or private metadata exported: %s", body)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerCannotReadOrOverwritePrivateInferenceObservation(t *testing.T) {
	taskID, receiptID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationInputBucket: "private-inputs", AutomationOutputBucket: "private-outputs"}
	lease := gatewayLease{TaskID: taskID.String(), InputRef: "s3://private-inputs/approved-input.json"}
	reference := "s3://private-inputs/" + inferenceContentKey(taskID, receiptID, "response")
	for _, write := range []bool{false, true} {
		if _, _, allowed := validateGatewayObject(lease, cfg, reference, write); allowed {
			t.Fatal("worker accessed server-only observation")
		}
	}
}
