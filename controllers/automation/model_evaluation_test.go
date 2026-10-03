package automation

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/automationagent"
	"events-stocks/internal/modelevaluation"
	"events-stocks/models"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

const evaluationTestPricing = `{"version":"synthetic","basis":"api_equivalent","models":{"minimax:minimax-m3":{"input_microusd_per_million":600000,"output_microusd_per_million":2400000}}}`

func TestEvaluationRequiresPrimaryRootBeforeAdmissionOrDispatch(t *testing.T) {
	configureAIActionPolicyTestRoot(t, 2)
	for _, handler := range []func(echo.Context) error{CreateModelEvaluation, DispatchNextModelEvaluation, GetModelEvaluation} {
		response := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/evaluation", nil), response)
		ctx.Set("cognito_sub", "synthetic-operator")
		if err := handler(ctx); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusForbidden {
			t.Fatalf("non-primary root admitted: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestEvaluationGatewayBindsMessagesBudgetRouteAndCrossRunQuota(t *testing.T) {
	for _, scenario := range []string{"valid", "modified-prompt", "wrong-route", "wrong-tokens", "expired-reservation", "over-budget", "reused-across-runs", "ambiguous-batch", "pilot-valid", "pilot-excess-sequence", "pilot-wrong-case", "pilot-swapped-candidate", "unknown-corpus"} {
		t.Run(scenario, func(t *testing.T) {
			db, mock, cleanup := attemptPolicyClaimDB(t)
			defer cleanup()
			batchID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			expires := time.Now().UTC().Add(time.Hour)
			task := models.AutomationTask{ID: taskID, ModelEvaluationID: &batchID, Operation: "ai.chat", MaxCompletionTokens: 4096, BudgetReservationExpiresAt: &expires}
			route, _ := modelevaluation.Route(modelevaluation.MiniMax)
			messages, _ := automationagent.SyntheticChatMessages("synthetic case")
			hash, _ := modelevaluation.MessageDigest(messages)
			_, routeHash, _ := canonicalInferenceRoutes([]models.AutomationAIActionRoute{route})
			snapshot := newGatewayAttemptPolicySnapshot(t, taskID, "new-run", "ai.chat", nil, 4096, 1, []models.AutomationAIActionRoute{route})
			request := inferenceRequest{TaskID: taskID.String(), RunID: "new-run", Operation: "ai.chat", MaxCompletionTokens: 4096, Messages: messages}
			budget := int64(1000000)
			version, caseID, sequence := modelevaluation.CorpusVersion, "synthetic", 1
			if strings.HasPrefix(scenario, "pilot-") {
				version, caseID = modelevaluation.ImplementationPilotVersion, "pagination-v1"
			}
			switch scenario {
			case "modified-prompt":
				request.Messages = append([]automationagent.Message(nil), messages...)
				request.Messages[1].Content = "different input"
			case "wrong-route":
				snapshot.RoutesHash = "different-route"
			case "wrong-tokens":
				request.MaxCompletionTokens = 4097
			case "expired-reservation":
				past := time.Now().UTC().Add(-time.Second)
				task.BudgetReservationExpiresAt = &past
			case "over-budget":
				budget = 1000001
			case "pilot-excess-sequence":
				sequence = 4
			case "pilot-wrong-case":
				caseID = "foreign-case"
			case "pilot-swapped-candidate":
				sequence = 2
			case "unknown-corpus":
				version = "foreign-corpus"
			}
			mock.ExpectQuery(`SELECT \* FROM "automation_model_evaluation_calls" WHERE automation_task_id = \$1 AND evaluation_id = \$2 LIMIT \$3`).WithArgs(taskID, batchID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"automation_task_id", "evaluation_id", "sequence", "case_id", "candidate", "messages_hash", "route_hash", "reservation_micros"}).AddRow(taskID, batchID, sequence, caseID, string(modelevaluation.MiniMax), hash, routeHash, 20000))
			mock.ExpectQuery(`SELECT \* FROM "automation_model_evaluations" WHERE id = \$1 ORDER BY .* FOR UPDATE`).WithArgs(batchID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "status", "budget_micros", "reservation_micros", "pricing_json", "corpus_version"}).AddRow(batchID, "active", budget, 500000, evaluationTestPricing, version))
			if scenario == "valid" || scenario == "pilot-valid" || scenario == "reused-across-runs" || scenario == "ambiguous-batch" {
				used := 0
				if scenario == "reused-across-runs" {
					used = 1
				}
				mock.ExpectQuery(`SELECT count\(\*\) FROM "automation_inference_receipts" WHERE automation_task_id = \$1`).WithArgs(taskID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(used))
				if used == 0 {
					inFlight := 0
					if scenario == "ambiguous-batch" {
						inFlight = 1
					}
					mock.ExpectQuery(`SELECT count\(\*\) FROM "automation_inference_receipts" JOIN automation_model_evaluation_calls`).WithArgs(batchID, "reserved", "ambiguous", "rejected").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(inFlight))
				}
			}
			scope := gatewayInferenceScope{}
			err := validateEvaluationInference(db, task, request, snapshot, &scope)
			if scenario == "valid" || scenario == "pilot-valid" {
				if err != nil || scope.EvaluationID == nil || scope.EvaluationPricingJSON != evaluationTestPricing {
					t.Fatalf("valid scoped admission failed: %v", err)
				}
			} else if err == nil {
				t.Fatalf("unsafe evaluation admitted: %s", scenario)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEvaluationScopeRejectsProjectAndNonEvaluationTasks(t *testing.T) {
	batchID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	for _, task := range []models.AutomationTask{
		{Operation: "ai.chat", MaxCompletionTokens: 4096},
		{Operation: "code.review", MaxCompletionTokens: 4096, ModelEvaluationID: &batchID},
		{Operation: "ai.chat", MaxCompletionTokens: 4096, ModelEvaluationID: &batchID, DeliveryWorkItemID: &projectID},
		{Operation: "ai.chat", MaxCompletionTokens: 8192, ModelEvaluationID: &batchID},
	} {
		if _, _, err := evaluationCallForTask(nil, task); err == nil {
			t.Fatal("invalid evaluation scope accepted")
		}
	}
}

func TestEvaluationFreezesCandidateWithoutReadingGlobalPolicy(t *testing.T) {
	for _, candidate := range []modelevaluation.Candidate{modelevaluation.MiniMax, modelevaluation.DeepSeek, modelevaluation.Luna} {
		t.Run(string(candidate), func(t *testing.T) {
			db, mock, cleanup := attemptPolicyClaimDB(t)
			defer cleanup()
			batchID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			task := models.AutomationTask{ID: taskID, ModelEvaluationID: &batchID, Operation: "ai.chat", MaxCompletionTokens: 4096}
			route, _ := modelevaluation.Route(candidate)
			routesJSON, routesHash, _ := canonicalInferenceRoutes([]models.AutomationAIActionRoute{route})
			now := time.Now().UTC()
			snapshot := models.AutomationInferenceAttemptPolicy{ID: uuid.Must(uuid.NewV4()), AutomationTaskID: taskID, RunID: "evaluation-run", Operation: "ai.chat", PolicyRevision: 1, RoutesJSON: routesJSON, RoutesHash: routesHash, MaxCompletionTokens: 4096, MaxInferenceCalls: 1, CreatedAt: now}
			snapshot.SnapshotHash = inferenceAttemptSnapshotHash(snapshot)
			if err := signAutomationInferenceAttemptPolicy(&snapshot); err != nil {
				t.Fatal(err)
			}
			expectAttemptPolicyMissing(mock, taskID, snapshot.RunID)
			mock.ExpectQuery(`SELECT \* FROM "automation_model_evaluation_calls" WHERE automation_task_id = \$1 AND evaluation_id = \$2 LIMIT \$3`).WithArgs(taskID, batchID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"automation_task_id", "evaluation_id", "sequence", "candidate", "route_hash", "reservation_micros"}).AddRow(taskID, batchID, 1, string(candidate), routesHash, 20000))
			mock.ExpectBegin()
			mock.ExpectQuery(`INSERT INTO "automation_inference_attempt_policies".*RETURNING "id"`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(snapshot.ID.String()))
			mock.ExpectCommit()
			expectAttemptPolicyRow(mock, snapshot)
			frozen, err := freezeAutomationInferenceAttemptPolicy(db, task, snapshot.RunID, now)
			if err != nil || frozen.RoutesJSON != routesJSON || frozen.MaxInferenceCalls != 1 || !verifyAutomationInferenceAttemptPolicySignature(frozen) {
				t.Fatalf("candidate was not sealed: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
