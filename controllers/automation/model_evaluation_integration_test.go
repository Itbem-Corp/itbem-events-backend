//go:build integration

package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/inferencecapability"
	"events-stocks/internal/modelevaluation"
	"events-stocks/models"
	automationqueue "events-stocks/repositories/automationqueuerepository"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This test uses a disposable PostgreSQL database and the real gateway/provider
// adapters. Its transport returns synthetic completions and cannot reach a
// provider or read any production credentials.
func TestExpiredReviewRecoveryPreservesUnresolvedReceiptsAcrossRuns(t *testing.T) {
	ctx := context.Background()
	container, err := postgrescontainer.Run(ctx, "postgres:16-alpine", postgrescontainer.WithDatabase("testdb"), postgrescontainer.WithUsername("test"), postgrescontainer.WithPassword("test"), postgrescontainer.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, raw.Close()) })
	require.NoError(t, db.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error)
	require.NoError(t, configuration.MigrateModelsForTest(db))
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	for _, status := range []string{"reserved", "ambiguous"} {
		t.Run(status, func(t *testing.T) {
			task := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), Operation: "code.review", RequestedBy: "github-app-review", Status: "running", AttemptCount: 1, RunID: "current-run", InputRef: "s3://fixture/input.json", EvidenceSubjectDigest: strings.Repeat("a", 64), LeaseExpiresAt: &expired}
			require.NoError(t, db.Create(&task).Error)
			receipt := models.AutomationInferenceReceipt{ID: uuid.Must(uuid.NewV4()), AutomationTaskID: task.ID, CallID: uuid.Must(uuid.NewV4()), RunID: "previous-run", Operation: task.Operation, PolicySnapshotHash: strings.Repeat("b", 64), QuotaLimit: 1, Status: status}
			require.NoError(t, db.Create(&receipt).Error)
			blocked, err := unresolvedReviewInference(db, task.ID)
			require.NoError(t, err)
			require.True(t, blocked)
			var before int64
			require.NoError(t, db.Model(&models.AutomationTask{}).Count(&before).Error)
			next, err := recoverStrandedGitHubReview(ctx, &task, now)
			require.NoError(t, err)
			require.Nil(t, next)
			var saved models.AutomationTask
			require.NoError(t, db.First(&saved, task.ID).Error)
			require.Equal(t, "running", saved.Status)
			require.Equal(t, 1, saved.AttemptCount)
			var savedReceipt models.AutomationInferenceReceipt
			require.NoError(t, db.First(&savedReceipt, receipt.ID).Error)
			require.Equal(t, status, savedReceipt.Status)
			require.Equal(t, "previous-run", savedReceipt.RunID)
			var retries int64
			require.NoError(t, db.Model(&models.AutomationTask{}).Count(&retries).Error)
			require.Equal(t, before, retries)
		})
	}
}

func TestEvaluationRealGatewaySerializesAndAccounts(t *testing.T) {
	ctx := context.Background()
	container, err := postgrescontainer.Run(ctx, "postgres:16-alpine", postgrescontainer.WithDatabase("testdb"), postgrescontainer.WithUsername("test"), postgrescontainer.WithPassword("test"), postgrescontainer.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, raw.Close()) })
	require.NoError(t, db.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error)
	require.NoError(t, configuration.MigrateModelsForTest(db))
	previousDB, previousResolver, previousClient := configuration.DB, inferenceCredentials, inferenceProviderHTTPClient
	configuration.DB = db
	inferenceCredentials = &testCredentialResolver{}
	t.Cleanup(func() {
		configuration.DB, inferenceCredentials, inferenceProviderHTTPClient = previousDB, previousResolver, previousClient
	})
	key := strings.Repeat("s", 48)
	t.Setenv(attemptPolicySigningKeyEnv, key)
	t.Setenv(attemptPolicyPreviousSigningKeyEnv, "")
	batchID := uuid.Must(uuid.NewV4())
	batch := models.AutomationModelEvaluation{ID: batchID, RequestedBy: "synthetic", CorpusVersion: modelevaluation.CorpusVersion, CorpusHash: strings.Repeat("a", 64), BudgetMicros: 1000000, ReservationMicros: 900000, PricingJSON: evaluationIntegrationPricing, Status: "active", CreatedAt: time.Now().UTC()}
	require.NoError(t, db.Create(&batch).Error)
	messages, err := automationagent.SyntheticChatMessages("Return synthetic JSON only.")
	require.NoError(t, err)
	messageHash, err := modelevaluation.MessageDigest(messages)
	require.NoError(t, err)
	var tasks []models.AutomationTask
	for index, candidate := range []modelevaluation.Candidate{modelevaluation.MiniMax, modelevaluation.DeepSeek, modelevaluation.Luna} {
		route, err := modelevaluation.Route(candidate)
		require.NoError(t, err)
		_, routeHash, err := canonicalInferenceRoutes([]models.AutomationAIActionRoute{route})
		require.NoError(t, err)
		expires := time.Now().UTC().Add(time.Hour)
		task := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), ModelEvaluationID: &batchID, RequestedBy: "synthetic", Operation: "ai.chat", MaxCompletionTokens: 4096, Status: "running", RunID: "synthetic-run", WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String(), LeaseExpiresAt: &expires, BudgetReservationMicros: 15000, BudgetReservationExpiresAt: &expires, InputRef: "s3://synthetic/input.json"}
		require.NoError(t, db.Create(&task).Error)
		binding := models.AutomationModelEvaluationCall{AutomationTaskID: task.ID, EvaluationID: batchID, Sequence: index + 1, CaseID: "synthetic", Candidate: string(candidate), PromptHash: strings.Repeat("b", 64), MessagesHash: messageHash, RouteHash: routeHash, ReservationMicros: 15000, CreatedAt: batch.CreatedAt}
		require.NoError(t, db.Create(&binding).Error)
		_, err = freezeAutomationInferenceAttemptPolicy(db, task, task.RunID, time.Now().UTC())
		require.NoError(t, err)
		tasks = append(tasks, task)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var providerCalls atomic.Int32
	inferenceProviderHTTPClient = &http.Client{Transport: providerUsageRoundTripper(func(request *http.Request) (*http.Response, error) {
		respond := func(body string) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		}
		if request.Method == http.MethodGet {
			switch request.URL.Host + request.URL.Path {
			case "models.dev/api.json":
				return respond(`{"minimax":{"models":{"MiniMax-M3":{"id":"MiniMax-M3","modalities":{"input":["text"],"output":["text"]},"limit":{"context":1000000,"output":8192}}}},"deepseek":{"models":{"deepseek-flash":{"id":"deepseek-flash","modalities":{"input":["text"],"output":["text"]},"limit":{"context":1000000,"output":8192}}}}}`)
			case "www.minimax.io/v1/token_plan/remains":
				return respond(`{"model_remains":[{"model_name":"MiniMax-M3"}],"base_resp":{"status_code":0}}`)
			case "api.deepseek.com/models":
				return respond(`{"data":[{"id":"deepseek-flash"}]}`)
			case "openrouter.ai/api/v1/models":
				return respond(`{"data":[{"id":"openai/gpt-6-luna","context_length":1000000,"max_completion_tokens":8192,"top_provider":{"max_completion_tokens":8192},"architecture":{"input_modalities":["text"],"output_modalities":["text"]},"supported_parameters":["reasoning","max_tokens"]}]}`)
			default:
				return nil, fmt.Errorf("test blocked unexpected catalog request")
			}
		}
		if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/chat/completions") {
			return nil, fmt.Errorf("test blocked unexpected provider request")
		}
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		model, _ := body["model"].(string)
		if providerCalls.Add(1) == 1 {
			entered <- struct{}{}
			<-release
		}
		if model == "MiniMax-M3" {
			require.NotEqual(t, map[string]any{"type": "disabled"}, body["thinking"])
		}
		encoded, _ := json.Marshal(map[string]any{"id": "synthetic-response", "model": model, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"ok":true}`}}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(encoded))}, nil
	})}
	call := func(task models.AutomationTask, changed bool) *httptest.ResponseRecorder {
		request := inferenceRequest{CallID: uuid.Must(uuid.NewV4()).String(), Provider: "minimax", Model: "gateway-managed", TaskID: task.ID.String(), RunID: task.RunID, Operation: "ai.chat", MaxCompletionTokens: 4096, Messages: append([]automationagent.Message(nil), messages...)}
		if changed {
			request.Messages[1].Content = "unauthorized modified prompt"
		}
		body, _ := json.Marshal(request)
		token, mintErr := inferencecapability.Mint(key, inferencecapability.Scope{TaskID: task.ID.String(), RunID: task.RunID, Operation: task.Operation, WorkerID: task.WorkerID, AgentKey: task.AgentKey, MachineID: task.MachineID}, time.Minute)
		require.NoError(t, mintErr)
		httpRequest := httptest.NewRequest(http.MethodPost, "/inference", bytes.NewReader(body))
		httpRequest.Header.Set(inferencecapability.HeaderName, token)
		recorder := httptest.NewRecorder()
		c := echo.New().NewContext(httpRequest, recorder)
		c.Set("config", &models.Config{})
		require.NoError(t, Infer(c))
		return recorder
	}
	require.Equal(t, http.StatusConflict, call(tasks[0], true).Code)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- call(tasks[0], false) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first fake provider call did not start")
	}
	require.Equal(t, http.StatusConflict, call(tasks[1], false).Code)
	require.Equal(t, int32(1), providerCalls.Load(), "concurrent admission reached a provider")
	close(release)
	firstResult := <-first
	require.Equal(t, http.StatusOK, firstResult.Code, firstResult.Body.String())
	require.Equal(t, http.StatusConflict, call(tasks[0], false).Code, "second call for a task must be rejected")
	for _, task := range tasks[1:] {
		result := call(task, false)
		require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	}
	var receipts []models.AutomationInferenceReceipt
	require.NoError(t, db.Order("created_at").Find(&receipts).Error)
	require.Len(t, receipts, 3)
	for index, receipt := range receipts {
		route, _ := modelevaluation.Route([]modelevaluation.Candidate{modelevaluation.MiniMax, modelevaluation.DeepSeek, modelevaluation.Luna}[index])
		require.Equal(t, "accepted", receipt.Status)
		require.Equal(t, route.Provider, receipt.Provider)
		require.Equal(t, route.Model, receipt.Model)
		require.Equal(t, tasks[index].ID, receipt.AutomationTaskID)
		require.Positive(t, receipt.TotalCostMicros)
	}
	require.Error(t, db.Model(&batch).Update("budget_micros", 2000000).Error)
	require.Error(t, db.Model(&models.AutomationModelEvaluationCall{}).Where("automation_task_id = ?", tasks[0].ID).Update("candidate", "luna-medium").Error)
	// The normal root-only provenance query must work on real PostgreSQL.
	configureAIActionPolicyTestRoot(t, models.RootLevelPrimary)
	recorder := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/evaluation", nil), recorder)
	c.Set("cognito_sub", "synthetic")
	c.SetParamNames("id")
	c.SetParamValues(batchID.String())
	require.NoError(t, GetModelEvaluation(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NotContains(t, recorder.Body.String(), "Return synthetic")
	require.NotContains(t, recorder.Body.String(), "must-not-be-read")
	require.NoError(t, db.Model(&batch).Update("status", "halted").Error)
	for _, version := range []string{modelevaluation.CorpusVersion, modelevaluation.CacheCorpusVersion} {
		t.Run("normal admission and concurrent dispatch/"+version, func(t *testing.T) { testEvaluationAdmissionAndDispatch(t, db, version) })
	}
}

func testEvaluationAdmissionAndDispatch(t *testing.T, db *gorm.DB, version string) {
	var uploaded atomic.Int32
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.Header.Get("X-Amz-Server-Side-Encryption") != "AES256" {
			http.Error(w, "invalid encrypted upload", 400)
			return
		}
		var input automationagent.TaskInput
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Prompt == "" {
			http.Error(w, "invalid input", 400)
			return
		}
		uploaded.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(storage.Close)
	previousS3 := configuration.GetS3Client(nil)
	t.Cleanup(func() { configuration.SetS3Client(previousS3) })
	storageConfig := &models.Config{AwsRegion: "us-east-1", S3Region: "us-east-1", S3ClientId: "integration", S3ClientSecret: "integration", S3Endpoint: storage.URL, S3UsePathStyle: "true"}
	client, _, err := configuration.BuildS3Client(context.Background(), storageConfig)
	require.NoError(t, err)
	configuration.SetS3Client(client)
	require.NoError(t, automationqueue.Init("us-east-1", "integration", "integration", "http://127.0.0.1:1/queue/evaluation", "", "", "", "http://127.0.0.1:1"))
	cfg := &models.Config{AutomationInputBucket: "synthetic-evaluation", AutomationPricingJSON: evaluationIntegrationPricing}
	batchID := uuid.Must(uuid.NewV4())
	invoke := func(handler echo.HandlerFunc, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/evaluation", strings.NewReader(body)), recorder)
		c.Set("config", cfg)
		c.Set("cognito_sub", "synthetic-admission")
		c.SetParamNames("id")
		c.SetParamValues(batchID.String())
		require.NoError(t, handler(c))
		return recorder
	}
	request := `{"id":"` + batchID.String() + `","corpus_version":"` + version + `"}`
	require.Equal(t, http.StatusBadRequest, invoke(CreateModelEvaluation, strings.Replace(request, version, "arbitrary-client-corpus", 1)).Code)
	require.Equal(t, http.StatusBadRequest, invoke(CreateModelEvaluation, strings.TrimSuffix(request, "}")+`,"routes":[]}`).Code)
	created := invoke(CreateModelEvaluation, request)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	require.Equal(t, int32(60), uploaded.Load())
	require.Equal(t, http.StatusOK, invoke(CreateModelEvaluation, request).Code)
	require.Equal(t, int32(60), uploaded.Load(), "idempotent admission uploaded inputs again")
	var calls []models.AutomationModelEvaluationCall
	require.NoError(t, db.Where("evaluation_id = ?", batchID).Order("sequence").Find(&calls).Error)
	require.Len(t, calls, 60)
	for i := 0; i < 60; i += 3 {
		require.Equal(t, calls[i].MessagesHash, calls[i+1].MessagesHash)
		require.Equal(t, calls[i].MessagesHash, calls[i+2].MessagesHash)
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); statuses <- invoke(DispatchNextModelEvaluation, "").Code }()
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	require.Equal(t, 1, counts[http.StatusAccepted], "concurrent dispatch did not queue exactly one call")
	require.Equal(t, 1, counts[http.StatusConflict])
	var queued, outbox int64
	require.NoError(t, db.Model(&models.AutomationTask{}).Where("model_evaluation_id = ? AND status = ?", batchID, "queued").Count(&queued).Error)
	require.NoError(t, db.Model(&models.OutboxEvent{}).Where("correlation_id = ?", batchID.String()).Count(&outbox).Error)
	require.Equal(t, int64(1), queued)
	require.Equal(t, int64(1), outbox)
	require.NoError(t, db.Model(&models.AutomationTask{}).Where("id = ?", calls[0].AutomationTaskID).Update("status", "failed").Error)
	require.Equal(t, http.StatusConflict, invoke(DispatchNextModelEvaluation, "").Code)
	var batch models.AutomationModelEvaluation
	require.NoError(t, db.First(&batch, "id = ?", batchID).Error)
	require.Equal(t, "halted", batch.Status)
	require.Equal(t, http.StatusConflict, invoke(DispatchNextModelEvaluation, "").Code)
	require.Equal(t, int32(60), uploaded.Load())
	// Completed or halted history cannot be retried under a fresh ID.
	repeated := strings.Replace(request, batchID.String(), uuid.Must(uuid.NewV4()).String(), 1)
	require.Equal(t, http.StatusConflict, invoke(CreateModelEvaluation, repeated).Code)
	require.Equal(t, int32(60), uploaded.Load())
}

const evaluationIntegrationPricing = `{"version":"synthetic","basis":"api_equivalent","models":{"minimax:minimax-m3":{"input_microusd_per_million":600000,"output_microusd_per_million":2400000},"deepseek:deepseek-flash":{"input_microusd_per_million":300000,"output_microusd_per_million":1200000},"openrouter:openai/gpt-6-luna":{"input_microusd_per_million":200000,"output_microusd_per_million":750000}}}`
