//go:build integration

package automation

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"events-stocks/internal/agentcallbackauth"
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

type releaseFlowProvider struct{ calls atomic.Int64 }

func (p *releaseFlowProvider) Complete(context.Context, []automationagent.Message, int) (automationagent.Completion, error) {
	p.calls.Add(1)
	return automationagent.Completion{}, fmt.Errorf("release must never reach inference")
}

// Uses the caller's independently approved policy/publication/QA fixtures.
// The actual worker, gateway object handlers, signed callbacks and PostgreSQL
// projections run together; only external GitHub and S3 are loopback fixtures.
func verifyReleaseWorkerControlPlaneRecovery(t *testing.T, db *gorm.DB, item uuid.UUID, candidate releasegate.Input, digest string) {
	t.Helper()
	head := candidate.Revisions[0].SHA
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var value any
		switch r.URL.Path {
		case "/repos/example/service/installation":
			value = map[string]any{"id": 22}
		case "/app/installations/22/access_tokens":
			if r.Method != http.MethodPost {
				w.WriteHeader(405)
				return
			}
			w.WriteHeader(201)
			value = map[string]any{"token": "synthetic-repository-token", "expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}
		case "/repos/example/service/pulls/42":
			value = map[string]any{"state": "open", "draft": false, "merged": false, "mergeable": true, "mergeable_state": "clean", "head": map[string]string{"sha": head}, "base": map[string]string{"ref": "main"}, "user": map[string]string{"login": "author"}}
		case "/repos/example/service/pulls/42/reviews":
			value = []map[string]any{{"id": 1, "state": "APPROVED", "commit_id": head, "user": map[string]string{"login": "independent-reviewer"}}}
		case "/repos/example/service/branches/main":
			value = map[string]any{"name": "main", "protected": true, "protection": map[string]any{"required_status_checks": map[string]any{"contexts": []string{"ci"}, "checks": []map[string]any{{"context": "ci", "app_id": 99}}}}}
		case "/repos/example/service/rules/branches/main":
			value = []any{}
		case "/repos/example/service/commits/" + head + "/check-runs":
			value = map[string]any{"total_count": 1, "check_runs": []map[string]any{{"id": 1, "name": "ci", "head_sha": head, "status": "completed", "conclusion": "success", "app": map[string]int64{"id": 99}}}}
		case "/repos/example/service/commits/" + head + "/status":
			value = map[string]any{"sha": head, "statuses": []any{}}
		case "/repos/example/service/contents/.github/workflows/deploy.yml":
			if r.URL.Query().Get("ref") != head {
				t.Error("workflow escaped frozen SHA")
				w.WriteHeader(400)
				return
			}
			value = map[string]any{"type": "file", "path": ".github/workflows/deploy.yml", "sha": strings.Repeat("b", 40)}
		case "/repos/example/service/environments/production":
			value = map[string]any{"name": "production"}
		default:
			t.Errorf("unexpected external request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(value)
	}))
	defer github.Close()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	t.Setenv("ITBEM_GITHUB_APP_ID", "12345")
	t.Setenv("ITBEM_GITHUB_INSTALLATION_IDS", "22")
	t.Setenv("ITBEM_GITHUB_APP_PRIVATE_KEY", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})))
	t.Setenv("ITBEM_GITHUB_API_BASE_URL", github.URL)
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "synthetic-release-flow-callback-secret")
	t.Setenv("AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY", strings.Repeat("s", 64))
	delivery, err := json.Marshal(map[string]any{"gatekeeper": candidate, "change_sets": []map[string]any{{"commit_sha": head, "review_type": "pull_request", "pull_request_url": "https://github.com/example/service/pull/42"}}, "release_environment": []automationagent.GitHubEnvironmentRequirement{{Repository: "example/service", HeadSHA: head, Workflow: ".github/workflows/deploy.yml", Environment: "production", RequiredSecretReferences: []string{}, RequiredVariableReferences: []string{}}}})
	require.NoError(t, err)
	input, err := json.Marshal(automationagent.TaskInput{Delivery: delivery})
	require.NoError(t, err)
	objects := map[string][]byte{"/synthetic-inputs/automation/inputs/flow/input.json": input}
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
			if !strings.HasPrefix(r.URL.Path, "/synthetic-outputs/automation/") || r.Header.Get("X-Amz-Server-Side-Encryption") != "AES256" {
				t.Error("storage write escaped encrypted task outputs")
				w.WriteHeader(400)
				return
			}
			body, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				t.Error(readErr)
				w.WriteHeader(500)
				return
			}
			objects[r.URL.Path] = body
		default:
			t.Errorf("unexpected storage method %s", r.Method)
			w.WriteHeader(405)
		}
	}))
	defer storage.Close()
	cfg := &models.Config{AutomationInputBucket: "synthetic-inputs", AutomationOutputBucket: "synthetic-outputs", S3Endpoint: storage.URL, S3UsePathStyle: "true", AwsRegion: "us-east-1", S3ClientId: "test", S3ClientSecret: "test"}
	identity, err := automationagent.LoadLocalMachineIdentity("", t.TempDir())
	require.NoError(t, err)
	public, err := agentcallbackauth.EncodePublicKey(identity.PublicKey())
	require.NoError(t, err)
	instance, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	require.NoError(t, db.Create(&models.AutomationAgentInstance{ID: instance, AgentKey: "release-flow", MachineID: identity.MachineID(), PublicKey: public, Status: "active"}).Error)
	require.NoError(t, db.Create(&models.AutomationAgentProfile{ID: uuid.Must(uuid.NewV4()), AgentKey: "release-flow", Name: "synthetic", OperationsJSON: `["delivery.release_gate"]`, CapabilitiesJSON: `[]`, Active: true}).Error)
	inputRef := "s3://synthetic-inputs/automation/inputs/flow/input.json"
	require.NoError(t, db.Create(&models.AutomationTask{ID: taskID, JobID: uuid.Must(uuid.NewV4()), Operation: "delivery.release_gate", Status: "queued", InputRef: inputRef, DeliveryWorkItemID: &item, EvidenceSubjectDigest: digest, RequestedBy: "synthetic-human", MaxCompletionTokens: 0}).Error)
	app := echo.New()
	app.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { c.Set("config", cfg); return next(c) }
	})
	app.POST("/api/internal/automation/gateway/objects/read", GatewayReadObject)
	app.PUT("/api/internal/automation/gateway/objects/write", GatewayWriteObject)
	app.POST("/api/internal/automation/gateway/release-observation", GatewayReleaseObservation, AgentCallbackAuthentication)
	var terminalCalls atomic.Int64
	app.PUT("/api/internal/automation/tasks/:id", func(c echo.Context) error {
		body, readErr := io.ReadAll(c.Request().Body)
		if readErr != nil {
			return readErr
		}
		var status struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(body, &status) != nil {
			return c.NoContent(400)
		}
		c.Request().Body = io.NopCloser(strings.NewReader(string(body)))
		if status.Status == "completed" && terminalCalls.Add(1) == 1 {
			return c.NoContent(503)
		}
		return Complete(c)
	}, AgentCallbackAuthentication)
	server := httptest.NewServer(app)
	defer server.Close()
	callback, err := automationagent.NewHTTPCallback(server.URL, identity, instance.String(), server.Client())
	require.NoError(t, err)
	lane := gatewayIdentity{Role: agentwork.RoleReleaseManager, Lane: agentwork.LaneRelease}
	gateway, err := automationagent.NewHTTPGateway(server.URL, deriveGatewayToken("synthetic-release-flow-callback-secret", lane), lane.Role, lane.Lane, server.Client())
	require.NoError(t, err)
	provider := &releaseFlowProvider{}
	worker, err := automationagent.NewWorker(automationagent.WorkerConfig{InputBucket: cfg.AutomationInputBucket, OutputBucket: cfg.AutomationOutputBucket, AgentKey: "release-flow", MachineID: identity.MachineID(), Role: lane.Role, Lane: lane.Lane, AllowedOperations: []string{"delivery.release_gate"}}, gateway, callback, provider)
	require.NoError(t, err)
	lease, err := sealGatewayLease(gatewayLease{Version: 1, Role: string(lane.Role), Lane: string(lane.Lane), TaskID: taskID.String(), InputRef: inputRef, ReceiptHandle: "synthetic", ExpiresAt: time.Now().UTC().Add(5 * time.Minute).Unix()})
	require.NoError(t, err)
	message := automationagent.TaskMessage{SchemaVersion: 1, JobID: uuid.Must(uuid.NewV4()).String(), Type: "ai.local.process", TenantCode: "itbem", CorrelationID: uuid.Must(uuid.NewV4()).String()}
	message.Payload.TaskID, message.Payload.Operation, message.Payload.InputRef = taskID.String(), "delivery.release_gate", inputRef
	message.Payload.Attempt = 1
	ctx := gateway.BindMessageContext(context.Background(), automationagent.QueueMessage{ReceiptHandle: lease})
	var eventsBefore int64
	require.NoError(t, db.Model(&models.DeliveryEvent{}).Where("work_item_id = ?", item).Count(&eventsBefore).Error)
	firstErr := worker.Process(ctx, message)
	require.ErrorContains(t, firstErr, "automation callback rejected (503)", "first terminal transport failure must retain durable result")
	var running models.AutomationTask
	require.NoError(t, db.First(&running, taskID).Error)
	require.Equal(t, "running", running.Status)
	originalRun := running.RunID
	storageMu.Lock()
	requestBody := append([]byte(nil), objects["/synthetic-outputs/automation/"+taskID.String()+"/runs/"+originalRun+"/request.json"]...)
	resultBody := append([]byte(nil), objects["/synthetic-outputs/automation/"+taskID.String()+"/runs/"+originalRun+"/result.json"]...)
	storageMu.Unlock()
	require.True(t, json.Valid(requestBody), "original immutable request was not stored")
	require.True(t, json.Valid(resultBody), "original immutable result was not stored")
	var storedResult struct {
		RequestRef string `json:"request_ref"`
	}
	require.NoError(t, json.Unmarshal(resultBody, &storedResult))
	require.Equal(t, "s3://synthetic-outputs/automation/"+taskID.String()+"/runs/"+originalRun+"/request.json", storedResult.RequestRef)
	require.NoError(t, db.Model(&models.AutomationTask{}).Where("id = ?", taskID).Update("lease_expires_at", time.Now().UTC().Add(-time.Second)).Error)
	require.NoError(t, worker.Process(ctx, message))
	var completed models.AutomationTask
	require.NoError(t, db.First(&completed, taskID).Error)
	require.Equal(t, "completed", completed.Status)
	require.NotEqual(t, originalRun, completed.RunID)
	require.Equal(t, "s3://synthetic-outputs/automation/"+taskID.String()+"/runs/"+originalRun+"/result.json", completed.OutputRef)
	require.Zero(t, provider.calls.Load())
	var observations int64
	require.NoError(t, db.Model(&models.AutomationReleaseObservation{}).Where("task_id = ?", taskID).Count(&observations).Error)
	require.Equal(t, int64(1), observations)
	var attempts []models.AutomationInferenceAttemptPolicy
	require.NoError(t, db.Where("automation_task_id = ?", taskID).Find(&attempts).Error)
	require.Len(t, attempts, 2)
	for _, attempt := range attempts {
		require.Zero(t, attempt.MaxInferenceCalls)
		require.Zero(t, attempt.MaxCompletionTokens)
	}
	var receipts, executions, eventsAfter int64
	require.NoError(t, db.Model(&models.AutomationInferenceReceipt{}).Where("automation_task_id = ?", taskID).Count(&receipts).Error)
	require.NoError(t, db.Model(&models.AutomationExecution{}).Where("automation_task_id = ?", taskID).Count(&executions).Error)
	require.Zero(t, receipts)
	require.Zero(t, executions)
	require.NoError(t, db.Model(&models.DeliveryEvent{}).Where("work_item_id = ?", item).Count(&eventsAfter).Error)
	require.Equal(t, eventsBefore+2, eventsAfter, "recovery must append only environment and Gatekeeper events")
	require.NoError(t, worker.Process(ctx, message), "terminal redelivery must be acknowledged")
	var afterRedelivery int64
	require.NoError(t, db.Model(&models.DeliveryEvent{}).Where("work_item_id = ?", item).Count(&afterRedelivery).Error)
	require.Equal(t, eventsAfter, afterRedelivery)
	require.Equal(t, int64(2), terminalCalls.Load())
	var event models.DeliveryEvent
	require.NoError(t, db.Where("work_item_id = ? AND event_type = ?", item, deliveryledger.EventTypeReleaseGateEvaluated).Order("sequence DESC").First(&event).Error)
	projection, err := deliveryledger.ProjectGateEvaluation(event)
	require.NoError(t, err)
	require.Equal(t, "blocked", projection.State, "full transport recovery must preserve the missing Vault gate")
}
