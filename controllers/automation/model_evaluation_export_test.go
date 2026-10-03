package automation

import (
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
	for _, scenario := range []string{"partial", "full", "duplicate-task", "overflow"} {
		t.Run(scenario, func(t *testing.T) {
			configureAIActionPolicyTestRoot(t, models.RootLevelPrimary)
			mockPtr, restore := configureAIActionPolicyRevisionTestDB(t)
			t.Cleanup(restore)
			mock := *mockPtr
			batchID := uuid.Must(uuid.NewV4())
			mock.ExpectQuery(`SELECT \* FROM "automation_model_evaluations"`).WithArgs(batchID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "status", "corpus_version"}).AddRow(batchID, "active", modelevaluation.CorpusVersion))
			count := 3
			if scenario == "duplicate-task" || scenario == "full" {
				count = modelevaluation.MaxCalls
			}
			if scenario == "overflow" {
				count = modelevaluation.MaxCalls + 1
			}
			rows := sqlmock.NewRows([]string{"automation_task_id", "evaluation_id", "sequence", "status"})
			for index := 0; index < count; index++ {
				identity := index
				if scenario == "duplicate-task" && index == count-1 {
					identity = 0
				}
				taskID := uuid.NewV5(uuid.NamespaceURL, fmt.Sprintf("synthetic-export-task-%d", identity))
				rows.AddRow(taskID, batchID, index+1, "pending")
			}
			mock.ExpectQuery(`SELECT evaluation_call\.\*,`).WithArgs(batchID, modelevaluation.MaxCalls+1).WillReturnRows(rows)
			response := httptest.NewRecorder()
			ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/evaluation", nil), response)
			ctx.Set("cognito_sub", "synthetic")
			ctx.SetParamNames("id")
			ctx.SetParamValues(batchID.String())
			if err := GetModelEvaluation(ctx); err != nil {
				t.Fatal(err)
			}
			expected := http.StatusServiceUnavailable
			if scenario == "partial" || scenario == "full" {
				expected = http.StatusOK
			}
			if response.Code != expected {
				t.Fatalf("status=%d expected=%d", response.Code, expected)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
