//go:build integration

package automation

import (
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

	"events-stocks/internal/agentwork"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/releasegate"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Source and provider responses are synthetic, while worker execution, signed
// claims, object gateway, policy snapshots, inference admission and projections
// are the production components. No transport may reach an external provider.
func verifyQAWorkerGatewayRecovery(t *testing.T, db *gorm.DB, item uuid.UUID, machine automationagent.MachineIdentity, instance uuid.UUID, acquired qaSourceAcquisition, commit, childPath string, providerCalls *atomic.Int32) {
	t.Helper()
	ctx := context.Background()
	taskID := uuid.Must(uuid.NewV4())
	branch := "itbem-agent/" + taskID.String()
	preview := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer preview.Close()
	candidate := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: item.String(), Revisions: []releasegate.Revision{{Repository: "example/service", Branch: "main", SHA: commit}}, Policy: releasegate.Policy{RequiredTestKinds: []string{}}}
	matrix, err := releasegate.RevisionMatrixDigest(candidate.Revisions)
	require.NoError(t, err)
	delivery, err := json.Marshal(map[string]any{"work_item": map[string]string{"preview_url": preview.URL}, "gatekeeper": candidate, "change_sets": []any{map[string]any{"repository_ref": "workspace://repo", "branch": branch, "commit_sha": commit, "review_type": "pull_request", "ci_status": "passed", "metadata": map[string]string{"remote_repository": "example/service", "target_branch": "main"}}}, "approved_plan": map[string]any{"qa_execution_matrix": []any{map[string]any{"repository_ref": "workspace://repo", "run_validation": true, "run_qa": false, "run_stagehand": false, "collect_evidence": false}}}})
	require.NoError(t, err)
	input, err := json.Marshal(automationagent.TaskInput{Prompt: "Summarize observed synthetic QA", Delivery: delivery})
	require.NoError(t, err)
	objects := map[string][]byte{"/synthetic-inputs/automation/inputs/worker/input.json": input}
	var storageMu sync.Mutex
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storageMu.Lock()
		defer storageMu.Unlock()
		switch r.Method {
		case http.MethodGet:
			body, ok := objects[r.URL.Path]
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(404)
				_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)
				return
			}
			_, _ = w.Write(body)
		case http.MethodPut:
			if r.Header.Get("X-Amz-Server-Side-Encryption") != "AES256" || (!strings.HasPrefix(r.URL.Path, "/synthetic-outputs/automation/") && !strings.HasPrefix(r.URL.Path, "/synthetic-inputs/inference-observations/")) {
				t.Error("worker storage escaped encrypted task scope")
				w.WriteHeader(400)
				return
			}
			if _, exists := objects[r.URL.Path]; exists && r.Header.Get("If-None-Match") == "*" {
				w.WriteHeader(412)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, (10<<20)+1))
			if err != nil || len(body) > 10<<20 {
				w.WriteHeader(400)
				return
			}
			objects[r.URL.Path] = body
		default:
			w.WriteHeader(405)
		}
	}))
	defer storage.Close()
	cfg := &models.Config{AutomationInputBucket: "synthetic-inputs", AutomationOutputBucket: "synthetic-outputs", S3Endpoint: storage.URL, S3UsePathStyle: "true", AwsRegion: "us-east-1", S3ClientId: "test", S3ClientSecret: "test"}
	registry, err := json.Marshal(map[string]automationagent.WorkspaceConfig{"repo": {Path: t.TempDir(), RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main", QASourceDependencies: map[string]string{childPath: "example/contract"}, ValidationCommands: [][]string{{"go", "test", "./..."}}, ValidationCommandKinds: []string{"unit"}}})
	require.NoError(t, err)
	t.Setenv("ITBEM_AI_WORKSPACES_JSON", string(registry))
	profile := models.AutomationAgentProfile{ID: uuid.Must(uuid.NewV4()), AgentKey: "qa", Name: "Synthetic QA", OperationsJSON: `["delivery.qa"]`, CapabilitiesJSON: `[]`, Active: true}
	require.NoError(t, db.Where("agent_key = ?", "qa").FirstOrCreate(&profile).Error)
	require.NoError(t, db.Create(&models.AutomationAIActionPolicy{ID: uuid.Must(uuid.NewV4()), Operation: "delivery.qa", Provider: "deepseek", Model: "deepseek-flash", ReasoningEnabled: true, ReasoningEffort: "high", Revision: 1}).Error)
	inputRef := "s3://synthetic-inputs/automation/inputs/worker/input.json"
	require.NoError(t, db.Create(&models.AutomationTask{ID: taskID, JobID: uuid.Must(uuid.NewV4()), Operation: "delivery.qa", Status: "queued", InputRef: inputRef, DeliveryWorkItemID: &item, EvidenceSubjectDigest: matrix, QASourceReceiptRequired: true, MaxCompletionTokens: 1024}).Error)
	app := echo.New()
	app.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { c.Set("config", cfg); return next(c) }
	})
	app.POST("/api/internal/automation/gateway/objects/read", GatewayReadObject)
	app.PUT("/api/internal/automation/gateway/objects/write", GatewayWriteObject)
	app.POST("/api/internal/automation/inference", Infer)
	var sourceCalls, terminalCalls atomic.Int32
	app.POST("/api/internal/automation/gateway/qa-source", func(c echo.Context) error {
		return gatewayQASourceWithBundleAcquirer(c, func(_ context.Context, subject qaSourceSubject) (qaSourceAcquisition, error) {
			require.Equal(t, commit, subject.SHA)
			require.Equal(t, branch, subject.Branch)
			sourceCalls.Add(1)
			return acquired, nil
		})
	}, AgentCallbackAuthentication)
	app.PUT("/api/internal/automation/tasks/:id", func(c echo.Context) error {
		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		var update struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(body, &update) != nil {
			return c.NoContent(400)
		}
		c.Request().Body = io.NopCloser(strings.NewReader(string(body)))
		if update.Status == "completed" && terminalCalls.Add(1) == 1 {
			return c.NoContent(503)
		}
		return Complete(c)
	}, AgentCallbackAuthentication)
	server := httptest.NewServer(app)
	defer server.Close()
	callback, err := automationagent.NewHTTPCallback(server.URL, machine, instance.String(), server.Client())
	require.NoError(t, err)
	lane := gatewayIdentity{Role: agentwork.RoleQA, Lane: agentwork.LaneQA}
	gateway, err := automationagent.NewHTTPGateway(server.URL, deriveGatewayToken("synthetic-qa-source-auth-fixture", lane), lane.Role, lane.Lane, server.Client())
	require.NoError(t, err)
	providerHTTP := &http.Client{Transport: providerUsageRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == server.URL+"/api/internal/automation/inference" {
			return http.DefaultTransport.RoundTrip(r)
		}
		if r.URL.Host == "models.dev" && r.Method == http.MethodGet {
			return inferenceProviderHTTPClient.Transport.RoundTrip(r)
		}
		return nil, fmt.Errorf("worker fixture blocked external transport")
	})}
	provider := automationagent.NewGatewayProviderClient(automationagent.GatewayProviderConfig{Provider: automationagent.ProviderDeepSeek, Model: "deepseek-flash", Endpoint: server.URL + "/api/internal/automation/inference"}, providerHTTP)
	worker, err := automationagent.NewWorker(automationagent.WorkerConfig{InputBucket: cfg.AutomationInputBucket, OutputBucket: cfg.AutomationOutputBucket, WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "qa", MachineID: machine.MachineID(), Role: lane.Role, Lane: lane.Lane, RequireProviderCapabilities: true}, gateway, callback, provider)
	require.NoError(t, err)
	lease, err := sealGatewayLease(gatewayLease{Version: 1, Role: string(lane.Role), Lane: string(lane.Lane), TaskID: taskID.String(), InputRef: inputRef, ReceiptHandle: "synthetic", ExpiresAt: time.Now().UTC().Add(5 * time.Minute).Unix()})
	require.NoError(t, err)
	message := automationagent.TaskMessage{SchemaVersion: 1, JobID: uuid.Must(uuid.NewV4()).String(), TenantCode: "itbem", Type: "ai.local.process", CorrelationID: uuid.Must(uuid.NewV4()).String()}
	message.Payload.TaskID, message.Payload.Operation, message.Payload.InputRef = taskID.String(), "delivery.qa", inputRef
	message.Payload.Attempt = 1
	message.Payload.MaxCompletionTokens = 1024
	ctx = gateway.BindMessageContext(ctx, automationagent.QueueMessage{ReceiptHandle: lease})
	before := providerCalls.Load()
	require.ErrorContains(t, worker.Process(ctx, message), "automation callback rejected (503)")
	var running models.AutomationTask
	require.NoError(t, db.First(&running, taskID).Error)
	require.Equal(t, "running", running.Status)
	originalRun := running.RunID
	storageMu.Lock()
	result := append([]byte(nil), objects["/synthetic-outputs/automation/"+taskID.String()+"/runs/"+originalRun+"/result.json"]...)
	storageMu.Unlock()
	require.True(t, json.Valid(result), "worker must preserve its private result before terminal callback")
	require.NoError(t, db.Model(&models.AutomationTask{}).Where("id = ?", taskID).Update("lease_expires_at", time.Now().UTC().Add(-time.Second)).Error)
	require.NoError(t, worker.Process(ctx, message))
	var completed models.AutomationTask
	require.NoError(t, db.First(&completed, taskID).Error)
	require.Equal(t, "completed", completed.Status)
	require.NotEqual(t, originalRun, completed.RunID)
	require.Equal(t, int32(1), sourceCalls.Load())
	require.Equal(t, before+1, providerCalls.Load())
	for _, model := range []any{&models.AutomationQASourceReceipt{}, &models.AutomationInferenceReceipt{}, &models.AutomationExecution{}} {
		var count int64
		column := "automation_task_id"
		if _, ok := model.(*models.AutomationQASourceReceipt); ok {
			column = "task_id"
		}
		require.NoError(t, db.Model(model).Where(column+" = ?", taskID).Count(&count).Error)
		require.Equal(t, int64(1), count, "recovery must not append another receipt or execution")
	}
	var sourceReceipt models.AutomationQASourceReceipt
	require.NoError(t, db.Where("task_id = ?", taskID).First(&sourceReceipt).Error)
	require.Equal(t, originalRun, sourceReceipt.RunID)
	require.Equal(t, commit, sourceReceipt.CommitSHA)
	require.Equal(t, acquired.bundleDigest, sourceReceipt.BundleSHA256)
	require.Equal(t, acquired.dependencies, sourceReceipt.DependencyManifest)
	storageMu.Lock()
	recoveredResult := append([]byte(nil), objects["/synthetic-outputs/automation/"+taskID.String()+"/runs/"+originalRun+"/result.json"]...)
	storageMu.Unlock()
	require.Equal(t, result, recoveredResult, "recovery changed the immutable original result")
	var accounting models.AutomationExecution
	require.NoError(t, db.Where("automation_task_id = ?", taskID).First(&accounting).Error)
	require.Equal(t, originalRun, accounting.RunID)
	var event models.DeliveryEvent
	require.NoError(t, db.Where("work_item_id = ? AND event_type = ?", item, deliveryledger.EventTypeQAObserved).Order("sequence DESC").First(&event).Error)
	projected, err := deliveryledger.ProjectQAObservation(event)
	require.NoError(t, err)
	require.Equal(t, taskID.String(), projected.Observation.TaskID)
	require.True(t, projected.Observation.Repositories[0].Commands[0].Passed)
}
