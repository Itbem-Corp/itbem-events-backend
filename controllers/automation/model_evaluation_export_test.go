package automation

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"events-stocks/internal/modelevaluation"
	"events-stocks/models"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestEvaluationExportRejectsAmbiguousJoinedOutcomes(t *testing.T) {
	for _, scenario := range []string{"partial", "full", "recovery", "duplicate-task", "overflow", "pilot-full", "pilot-overflow"} {
		t.Run(scenario, func(t *testing.T) {
			configureAIActionPolicyTestRoot(t, models.RootLevelPrimary)
			mockPtr, restore := configureAIActionPolicyRevisionTestDB(t)
			t.Cleanup(restore)
			mock := *mockPtr
			batchID := uuid.Must(uuid.NewV4())
			version, expectedCalls := modelevaluation.CorpusVersion, modelevaluation.MaxCalls
			if scenario == "pilot-full" || scenario == "pilot-overflow" {
				version, expectedCalls = modelevaluation.ImplementationPilotVersion, 3
			}
			mock.ExpectQuery(`SELECT \* FROM "automation_model_evaluations"`).WithArgs(batchID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "status", "corpus_version"}).AddRow(batchID, "active", version))
			count := 3
			if scenario == "duplicate-task" || scenario == "full" {
				count = modelevaluation.MaxCalls
			}
			if scenario == "overflow" {
				count = modelevaluation.MaxCalls + 1
			}
			if scenario == "pilot-overflow" {
				count = 4
			}
			rows := sqlmock.NewRows([]string{"automation_task_id", "evaluation_id", "sequence", "status", "run_id", "receipt_run_id"})
			for index := 0; index < count; index++ {
				identity := index
				if scenario == "duplicate-task" && index == count-1 {
					identity = 0
				}
				taskID := uuid.NewV5(uuid.NamespaceURL, fmt.Sprintf("synthetic-export-task-%d", identity))
				rows.AddRow(taskID, batchID, index+1, "pending", "recovery-run", "original-provider-run")
			}
			mock.ExpectQuery(`SELECT evaluation_call\.\*,`).WithArgs(batchID, expectedCalls+1).WillReturnRows(rows)
			response := httptest.NewRecorder()
			ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/evaluation", nil), response)
			ctx.Set("cognito_sub", "synthetic")
			ctx.SetParamNames("id")
			ctx.SetParamValues(batchID.String())
			if err := GetModelEvaluation(ctx); err != nil {
				t.Fatal(err)
			}
			expected := http.StatusServiceUnavailable
			if scenario == "partial" || scenario == "full" || scenario == "recovery" || scenario == "pilot-full" {
				expected = http.StatusOK
			}
			if response.Code != expected {
				t.Fatalf("status=%d expected=%d", response.Code, expected)
			}
			if scenario == "recovery" {
				var result struct {
					Data struct {
						Calls []struct {
							RunID        string `json:"run_id"`
							ReceiptRunID string `json:"receipt_run_id"`
						} `json:"calls"`
					} `json:"data"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Data.Calls) != count || result.Data.Calls[0].RunID != "recovery-run" || result.Data.Calls[0].ReceiptRunID != "original-provider-run" {
					t.Fatal("export lost original provider-run identity")
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
