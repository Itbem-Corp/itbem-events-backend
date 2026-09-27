//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"events-stocks/configuration"
	automationCtrl "events-stocks/controllers/automation"
	delivery "events-stocks/controllers/delivery"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/inferencecapability"
	"events-stocks/models"
	automationqueue "events-stocks/repositories/automationqueuerepository"
	"events-stocks/services/deliveryplansteps"
	"events-stocks/services/deliveryworkflow"
	"events-stocks/services/outbox"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// receiveIntegrationMessages isolates short SQS long-polls from the overall
// test deadline. LocalStack can occasionally hold one poll until its context
// expires while the outbox dispatcher is still publishing; that transient
// expiry is retryable, but the outer deadline remains a hard failure.
func receiveIntegrationMessages(ctx context.Context, queue *automationagent.AWSQueue, limit int) ([]automationagent.QueueMessage, error) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return queue.Receive(ctx, limit)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pollDeadline := time.Now().Add(2 * time.Second)
		if pollDeadline.After(deadline) {
			pollDeadline = deadline
		}
		pollCtx, cancel := context.WithDeadline(ctx, pollDeadline)
		messages, err := queue.Receive(pollCtx, limit)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				continue
			}
			return nil, err
		}
		if len(messages) > 0 {
			return messages, nil
		}
	}
}

// TestDeliveryControlPlaneQueueWorkerRoundTrip proves the local production
// handoff rather than a fixture-only worker loop: the API creates the task and
// encrypted input, the durable outbox publishes to an isolated LocalStack
// queue, the real worker processes it, and the real callback controller
// persists the lease, execution ledger and terminal result. The queue is
// unique to this test so a developer's long-running local worker cannot claim
// the synthetic message.
func TestDeliveryControlPlaneQueueWorkerRoundTrip(t *testing.T) {
	if testing.Short() || os.Getenv("ITBEM_LOCALSTACK_E2E") != "1" {
		t.Skip("set ITBEM_LOCALSTACK_E2E=1 to run against local loopback LocalStack")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	// Claims freeze a signed inference policy before the worker calls a model.
	// Keep this key synthetic and scoped to the ephemeral integration process.
	t.Setenv("AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY", "integration-only-attempt-policy-signing-key-48-bytes")

	endpoint := strings.TrimSpace(os.Getenv("ITBEM_LOCALSTACK_ENDPOINT"))
	if endpoint == "" {
		endpoint = "http://localhost:4566"
	}
	// LocalStack plus the durable outbox can legitimately take a few long-poll
	// turns when the developer machine is under load. Keep the integration
	// budget bounded, but do not let the implementation phase fail merely
	// because its queue visibility window is shorter than the dispatcher lag.
	ctx, cancel := context.WithTimeout(context.Background(), 360*time.Second)
	defer cancel()
	const inputBucket = "itbem-ai-inputs-local"
	const outputBucket = "itbem-ai-outputs-local"
	workspaceRoot := t.TempDir()
	require.NoError(t, os.WriteFile(workspaceRoot+string(os.PathSeparator)+"README.md", []byte("local fixture\n"), 0o644))
	for _, file := range []string{"alpha.txt", "beta.txt", "dependent.txt"} {
		require.NoError(t, os.WriteFile(workspaceRoot+string(os.PathSeparator)+file, []byte("base fixture\n"), 0o644))
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "integration@example.test"}, {"config", "user.name", "Integration"}, {"add", "."}, {"commit", "-qm", "fixture"}} {
		command := exec.Command("git", append([]string{"-C", workspaceRoot}, args...)...)
		require.NoError(t, command.Run(), "git %v", args)
	}
	revisionBytes, err := exec.Command("git", "-C", workspaceRoot, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	fixtureRevision := strings.TrimSpace(string(revisionBytes))
	workspaceRegistry, err := json.Marshal(map[string]any{"fixture": map[string]any{
		"path":                 workspaceRoot,
		"capabilities":         []string{automationagent.WorkspaceCapabilityReadRepository, automationagent.WorkspaceCapabilityCreateWorktree, automationagent.WorkspaceCapabilityApplyPatch},
		"sandbox_runtime":      automationagent.WorkspaceSandboxDocker,
		"sandbox_image":        "golang:1.25-bookworm",
		"sandbox_image_digest": "sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437",
		"sandbox_network":      "none",
		"require_sandbox":      true,
		"validation_commands":  [][]string{{"go", "version"}},
		"qa_commands":          [][]string{{"go", "version"}},
		"acceptance_checks":    []map[string]any{{"criterion": "fixture validation", "command": []string{"go", "version"}}},
	}})
	require.NoError(t, err)
	t.Setenv("ITBEM_AI_WORKSPACES_JSON", string(workspaceRegistry))
	runtime, err := automationagent.NewAWSRuntime(ctx, automationagent.RuntimeConfig{
		WorkerConfig: automationagent.WorkerConfig{InputBucket: inputBucket, OutputBucket: outputBucket},
		AWSRegion:    "us-east-1", SQSEndpoint: endpoint, S3Endpoint: endpoint,
	})
	require.NoError(t, err)

	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	queueName := "itbem-ai-integration-" + suffix
	createdQueue, err := runtime.SQS.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(queueName)})
	require.NoError(t, err)
	require.NotNil(t, createdQueue.QueueUrl)
	queueURL := aws.ToString(createdQueue.QueueUrl)
	t.Cleanup(func() {
		_, _ = runtime.SQS.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
	})
	// The package owns a sync.Once by design. This test is the only integration
	// caller that initializes it, and the unique queue keeps the handoff local.
	automationqueue.Init("us-east-1", "test", "test", queueURL, "", endpoint)
	require.True(t, automationqueue.IsConfigured())

	cfg := &models.Config{
		AwsRegion:              "us-east-1",
		S3Region:               "us-east-1",
		S3ClientId:             "test",
		S3ClientSecret:         "test",
		AwsBucketName:          inputBucket,
		S3Endpoint:             endpoint,
		S3UsePathStyle:         "true",
		AutomationInputBucket:  inputBucket,
		AutomationOutputBucket: outputBucket,
	}
	previousEndpoint, previousPathStyle := configuration.GetS3Endpoint()
	previousRegion := configuration.GetS3Region()
	t.Cleanup(func() {
		configuration.SetS3Endpoint(previousEndpoint, previousPathStyle)
		configuration.SetS3Region(previousRegion)
	})
	s3Client, region, err := configuration.BuildS3Client(ctx, cfg)
	require.NoError(t, err)
	configuration.SetS3Client(s3Client)
	configuration.SetS3Region(region)
	configuration.SetS3Endpoint(endpoint, true)

	db := configuration.DB
	require.NotNil(t, db)
	configureRoundTripAIActionPolicies(t, db, "delivery.plan", "delivery.implementation", "delivery.qa", "delivery.summary")
	profile := models.DefaultGeneralistAgentProfile()
	require.NoError(t, db.Where("agent_key = ?", profile.AgentKey).FirstOrCreate(&profile).Error)
	localIdentity, err := automationagent.LoadLocalMachineIdentity("", t.TempDir())
	require.NoError(t, err)
	publicKey, err := agentcallbackauth.EncodePublicKey(localIdentity.PublicKey())
	require.NoError(t, err)
	fingerprint, err := agentcallbackauth.PublicKeyFingerprint(localIdentity.PublicKey())
	require.NoError(t, err)
	registeredInstance := models.AutomationAgentInstance{
		ID: uuid.Must(uuid.NewV4()), AgentKey: profile.AgentKey, MachineID: localIdentity.MachineID(),
		PublicKey: publicKey, PublicKeyFingerprint: fingerprint, Status: "active",
	}
	require.NoError(t, db.Create(&registeredInstance).Error)
	t.Cleanup(func() {
		_ = db.Unscoped().Where("instance_id = ?", registeredInstance.ID).Delete(&models.AutomationAgentCallbackNonce{}).Error
		_ = db.Unscoped().Where("machine_id = ?", localIdentity.MachineID()).Delete(&models.AutomationAgentHeartbeat{}).Error
		_ = db.Unscoped().Where("id = ?", registeredInstance.ID).Delete(&models.AutomationAgentInstance{}).Error
	})
	subject := "integration-worker-roundtrip-" + suffix
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Roundtrip customer " + suffix, Code: "ROUNDTRIP_" + suffix, Level: 10, IsActive: true}
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Roundtrip customer " + suffix, Code: "roundtrip-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Local queue roundtrip " + suffix, Slug: "local-queue-roundtrip-" + suffix, Summary: "control plane to local worker", Status: "active", CreatedBy: subject}
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject, Title: "Local queue roundtrip", Description: "Create and process a bounded local plan", ExpectedOutcome: "A durable plan callback", State: deliveryworkflow.StatePlanning, IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`, AcceptanceJSON: `["fixture validation"]`, ClientContextJSON: `{}`, PlanJSON: `{}`}
	snapshot := models.DeliveryContextSnapshot{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, SourceID: uuid.Must(uuid.NewV4()), Kind: "repository", Name: "Local fixture", Reference: "workspace://fixture", Revision: fixtureRevision, MetadataJSON: `{"repository_role":"primary","repository_kind":"backend_api","repository_responsibility":"Local roundtrip fixture"}`, CapturedAt: time.Now().UTC()}
	for _, value := range []any{&clientType, &client, &project, &item, &snapshot} {
		require.NoError(t, db.Create(value).Error)
	}
	var taskID, jobID uuid.UUID
	t.Cleanup(func() {
		_ = db.Unscoped().Where("delivery_work_item_id = ?", item.ID).Delete(&models.AutomationExecution{}).Error
		_ = db.Unscoped().Where("work_item_id = ?", item.ID).Delete(&models.DeliveryEvidence{}).Error
		_ = db.Unscoped().Where("work_item_id = ?", item.ID).Delete(&models.DeliveryGate{}).Error
		_ = db.Unscoped().Where("work_item_id = ?", item.ID).Delete(&models.DeliveryContinuation{}).Error
		_ = db.Unscoped().Where("work_item_id = ?", item.ID).Delete(&models.DeliveryChangeSet{}).Error
		_ = db.Unscoped().Where("work_item_id = ?", item.ID).Delete(&models.DeliveryPlan{}).Error
		_ = db.Unscoped().Where("delivery_work_item_id = ?", item.ID).Delete(&models.AutomationTask{}).Error
		_ = db.Unscoped().Where("correlation_id = ?", item.ID.String()).Delete(&models.OutboxEvent{}).Error
		_ = db.Unscoped().Where("work_item_id = ?", item.ID).Delete(&models.DeliveryContextSnapshot{}).Error
		_ = db.Unscoped().Where("id = ?", item.ID).Delete(&models.DeliveryWorkItem{}).Error
		_ = db.Unscoped().Where("id = ?", project.ID).Delete(&models.DeliveryProject{}).Error
		_ = db.Unscoped().Where("id = ?", client.ID).Delete(&models.Client{}).Error
		_ = db.Unscoped().Where("id = ?", clientType.ID).Delete(&models.ClientType{}).Error
	})
	restoreHooks := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		if cognitoSub != subject {
			return nil, fmt.Errorf("unexpected subject %q", cognitoSub)
		}
		return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: subject, IsRoot: true, RootLevel: models.RootLevelOperational, IsActive: true}, nil
	}})
	t.Cleanup(restoreHooks)

	// Real callback route, without booting the HTTP server or auth middleware.
	type callbackRejection struct {
		Status  int
		Message string
		Error   string
	}
	var rejectedCompletionCallbackStatuses sync.Map
	callbackRouter := echo.New()
	callbackRouter.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("config", cfg)
			c.SetRequest(c.Request().WithContext(configuration.WithConfig(c.Request().Context(), cfg)))
			return next(c)
		}
	})
	callbackRouter.POST("/api/internal/automation/inference", func(c echo.Context) error {
		c.Set("config", cfg)
		requestBody, readErr := io.ReadAll(c.Request().Body)
		if readErr == nil {
			c.Request().Body = io.NopCloser(bytes.NewReader(requestBody))
			var scope struct {
				TaskID              string `json:"task_id"`
				RunID               string `json:"run_id"`
				Operation           string `json:"operation"`
				MaxCompletionTokens int    `json:"max_completion_tokens"`
			}
			if json.Unmarshal(requestBody, &scope) == nil {
				capabilityScope, verifyErr := inferencecapability.Verify(os.Getenv("AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY"), c.Request().Header.Get(inferencecapability.HeaderName), scope.TaskID, scope.RunID, scope.Operation, time.Now().UTC())
				if verifyErr != nil {
					t.Errorf("integration gateway request did not carry a valid lease capability: %v", verifyErr)
				}
				inferErr := automationCtrl.Infer(c)
				if c.Response().Status == http.StatusConflict && verifyErr == nil {
					var task models.AutomationTask
					var policy models.AutomationInferenceAttemptPolicy
					taskErr := configuration.DB.First(&task, "id = ?", scope.TaskID).Error
					policyErr := configuration.DB.Where("automation_task_id = ? AND run_id = ?", scope.TaskID, scope.RunID).First(&policy).Error
					t.Logf("gateway 409 scope diagnostic: task_error=%v task_status=%s task_run_match=%t task_operation_match=%t task_max=%d request_max=%d worker_match=%t agent_match=%t machine_match=%t policy_error=%v policy_operation_match=%t policy_max=%d policy_revision=%d routes=%d policy_project=%v", taskErr, task.Status, task.RunID == scope.RunID, task.Operation == scope.Operation, task.MaxCompletionTokens, scope.MaxCompletionTokens, task.WorkerID == capabilityScope.WorkerID, task.AgentKey == capabilityScope.AgentKey, task.MachineID == capabilityScope.MachineID, policyErr, policy.Operation == scope.Operation, policy.MaxCompletionTokens, policy.PolicyRevision, len(policy.RoutesJSON), policy.ProjectID)
				}
				return inferErr
			}
		}
		return automationCtrl.Infer(c)
	})
	callbackRouter.PUT("/api/internal/automation/tasks/:id", func(c echo.Context) error {
		c.Set("config", cfg)
		requestBody, _ := io.ReadAll(c.Request().Body)
		c.Request().Body = io.NopCloser(bytes.NewReader(requestBody))
		var requestMeta struct {
			Status    string `json:"status"`
			RunID     string `json:"run_id"`
			WorkerID  string `json:"worker_id"`
			AgentKey  string `json:"agent_key"`
			MachineID string `json:"machine_id"`
		}
		_ = json.Unmarshal(requestBody, &requestMeta)
		capture := &roundtripResponseWriter{ResponseWriter: c.Response().Writer}
		c.Response().Writer = capture
		err := automationCtrl.Complete(c)
		status := c.Response().Status
		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) {
			status = httpErr.Code
		}
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			// Keep diagnostics to the API's static category only. Never retain or
			// log error details or raw response bodies from an execution callback.
			var response struct {
				Message string `json:"message"`
				Error   string `json:"error"`
			}
			_ = json.Unmarshal(capture.body.Bytes(), &response)
			rejectedCompletionCallbackStatuses.Store(c.Param("id"), callbackRejection{Status: status, Message: response.Message, Error: response.Error})
			t.Logf("worker task callback returned non-success: task=%s status=%d category=%q", c.Param("id"), status, response.Message)
			if status == http.StatusConflict && requestMeta.Status == "running" {
				var current models.AutomationTask
				queryErr := configuration.DB.Select("status", "run_id", "worker_id", "agent_key", "machine_id").First(&current, "id = ?", c.Param("id")).Error
				t.Logf("task lease conflict diagnostic: query_error=%v request_run_matches=%t request_worker_matches=%t request_agent_matches=%t request_machine_matches=%t current_status=%s", queryErr, current.RunID == requestMeta.RunID, current.WorkerID == requestMeta.WorkerID, current.AgentKey == requestMeta.AgentKey, current.MachineID == requestMeta.MachineID, current.Status)
			}
		}
		return err
	}, automationCtrl.AgentCallbackAuthentication)
	callbackRouter.PUT("/api/internal/automation/agents/heartbeat", automationCtrl.AgentHeartbeat, automationCtrl.AgentCallbackAuthentication)
	callbackRouter.POST("/api/internal/automation/steps/claim", automationCtrl.ClaimDeliveryPlanStep, automationCtrl.AgentCallbackAuthentication)
	callbackRouter.PUT("/api/internal/automation/steps/:id/lease", func(c echo.Context) error {
		err := automationCtrl.RenewDeliveryPlanStepLease(c)
		status := c.Response().Status
		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) {
			status = httpErr.Code
		}
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			t.Errorf("plan-step lease renewal rejected: status=%d", status)
		} else {
			var step models.DeliveryPlanStep
			_ = configuration.DB.Select("lease_expires_at").First(&step, "id = ?", c.Param("id")).Error
			t.Logf("plan-step lease renewed: step=%s expiry=%s", c.Param("id"), step.LeaseExpiresAt.UTC().Format(time.RFC3339))
		}
		return err
	}, automationCtrl.AgentCallbackAuthentication)
	callbackRouter.PUT("/api/internal/automation/steps/:id", automationCtrl.TransitionDeliveryPlanStep, automationCtrl.AgentCallbackAuthentication)
	callbackRouter.POST("/api/internal/automation/steps/:id/activity", automationCtrl.RecordDeliveryPlanStepActivity, automationCtrl.AgentCallbackAuthentication)
	callbackRouter.POST("/api/internal/automation/steps/:id/dependency-patches/manifest", automationCtrl.GetDeliveryPlanStepDependencyPatchManifest, automationCtrl.AgentCallbackAuthentication)
	callbackRouter.POST("/api/internal/automation/steps/:id/dependency-patches/:sha256", automationCtrl.GetDeliveryPlanStepDependencyPatch, automationCtrl.AgentCallbackAuthentication)
	callbackServer := httptest.NewServer(callbackRouter)
	defer callbackServer.Close()
	callback, err := automationagent.NewHTTPCallback(callbackServer.URL, localIdentity, registeredInstance.ID.String(), callbackServer.Client())
	require.NoError(t, err)
	providerTransport := &roundtripMiniMaxTransport{}
	automationCtrl.ConfigureInferenceCredentials(roundtripCredentialResolver{})
	automationCtrl.ConfigureInferenceProviderHTTPClient(&http.Client{Transport: providerTransport})
	t.Cleanup(func() {
		automationCtrl.ConfigureInferenceProviderHTTPClient(nil)
		automationCtrl.ConfigureInferenceCredentials(nil)
	})
	inferenceURL := callbackServer.URL + "/api/internal/automation/inference"
	gatewayProvider := automationagent.NewGatewayProviderClient(
		automationagent.GatewayProviderConfig{Provider: automationagent.ProviderMiniMax, Model: "MiniMax-M3", Endpoint: inferenceURL},
		&http.Client{Transport: roundtripGatewayTransport{inferenceURL: inferenceURL, callbackClient: callbackServer.Client()}},
	)

	dispatchCtx, stopDispatcher := context.WithCancel(ctx)
	defer stopDispatcher()
	outbox.StartDispatcher(dispatchCtx, db)

	// This is the actual authenticated control-plane request that uploads the
	// private input and creates the durable task + outbox event.
	body, err := json.Marshal(map[string]string{"phase": "plan", "instructions": "Produce the bounded local plan."})
	require.NoError(t, err)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/automation/work-items/"+item.ID.String()+"/agent-runs", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	c := e.NewContext(req, recorder)
	c.SetPath("/api/automation/work-items/:id/agent-runs")
	c.SetParamNames("id")
	c.SetParamValues(item.ID.String())
	c.Set("cognito_sub", subject)
	c.Set("tenant_code", "itbem")
	setRoundTripOrganizationWorkspace(c, client.ID)
	c.Set("config", cfg)
	require.NoError(t, delivery.StartAgentRun(c))
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	require.NoError(t, db.Where("delivery_work_item_id = ? AND operation = ?", item.ID, "delivery.plan").First(&models.AutomationTask{}).Error)
	var createdTask models.AutomationTask
	require.NoError(t, db.Where("delivery_work_item_id = ? AND operation = ?", item.ID, "delivery.plan").First(&createdTask).Error)
	taskID, jobID = createdTask.ID, createdTask.JobID

	queue, err := automationagent.NewAWSQueueWithWaitTime(runtime.SQS, queueURL, 1)
	require.NoError(t, err)
	var messages []automationagent.QueueMessage
	receiveCtx, receiveCancel := context.WithTimeout(ctx, 20*time.Second)
	defer receiveCancel()
	messages, err = receiveIntegrationMessages(receiveCtx, queue, 1)
	require.NoError(t, err)
	require.Len(t, messages, 1, "the outbox dispatcher must publish the control-plane task")
	var queued automationagent.TaskMessage
	require.NoError(t, json.Unmarshal([]byte(messages[0].Body), &queued))
	require.Equal(t, taskID.String(), queued.Payload.TaskID)
	require.Equal(t, jobID.String(), queued.JobID)

	fake := &roundtripProvider{content: fmt.Sprintf(`{"summary":"Bounded local plan","goal_interpretation":"Validate the local control-plane handoff","autonomy_boundary":"Stop at the human plan gate.","confidence":0.95,"context_reviewed":["workspace://fixture"],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":["update the fixture README"],"execution_steps":[{"step_key":"update-readme","role":"implementation","order":0,"title":"Update fixture README","objective":"Make the bounded README change.","acceptance_criteria":["fixture validation"]},{"step_key":"integrate","role":"integration","order":1,"title":"Verify fixture","objective":"Integrate the implementation patch and validate the result.","acceptance_criteria":["fixture validation"],"depends_on":["update-readme"]}],"risks":[],"qa_plan":["go version"],"evidence_plan":["durable callback and private result"],"acceptance_criteria":["fixture validation"],"files_impacted":["README.md"],"rollback_plan":["discard the local plan"],"questions":[],"estimate":"1 minute","repository_impact":[{"name":"Local fixture","reference":"workspace://fixture","revision":"%s","role":"primary","impact":"changes","notes":"Only README.md is in scope."}]}`, fixtureRevision), responseID: "roundtrip-plan"}
	providerTransport.setProvider(fake)
	worker, err := automationagent.NewWorker(automationagent.WorkerConfig{
		InputBucket: inputBucket, OutputBucket: outputBucket, AgentKey: profile.AgentKey,
		MachineID: localIdentity.MachineID(), AllowedOperations: []string{"delivery.plan"},
	}, automationagent.NewAWSObjectStore(runtime.S3), callback, gatewayProvider)
	require.NoError(t, err)
	processErr := automationagent.ProcessQueueMessage(ctx, worker, queue, messages[0])
	if rejection, ok := rejectedCompletionCallbackStatuses.Load(taskID.String()); ok {
		observed := rejection.(callbackRejection)
		t.Fatalf("plan worker callback rejected: status=%d message=%q detail=%q worker_error=%v", observed.Status, observed.Message, observed.Error, processErr)
	}
	require.NoError(t, processErr)

	var completed models.AutomationTask
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := db.First(&completed, taskID).Error; err == nil && completed.Status == "completed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Equalf(t, "completed", completed.Status, "worker callback status=%s error=%q output=%q run=%q", completed.Status, completed.ErrorMessage, completed.OutputRef, completed.RunID)
	require.Equal(t, "minimax", completed.Provider)
	require.Equal(t, "MiniMax-M3", completed.Model)
	require.NotEmpty(t, completed.RunID)
	require.True(t, strings.HasPrefix(completed.OutputRef, "s3://"+outputBucket+"/automation/"+taskID.String()+"/runs/"))
	var execution models.AutomationExecution
	require.NoError(t, db.Where("automation_task_id = ?", taskID).First(&execution).Error)
	require.Equal(t, completed.RunID, execution.RunID)
	require.Equal(t, "plan", execution.StepKey)
	require.NotEmpty(t, execution.RequestRef)

	// The decomposition scheduler is covered by the database-level DAG test,
	// but that only proves durable intent. Exercise the next boundary here:
	// two independent plan intents are admitted through the real control plane,
	// published by the real outbox into LocalStack SQS, and consumed concurrently
	// by two independent worker loops. The provider barrier makes overlap an
	// assertion rather than an assumption about goroutine scheduling.
	parallelItems := make([]models.DeliveryWorkItem, 0, 2)
	parallelTaskIDs := make(map[uuid.UUID]struct{}, 2)
	for index := 0; index < 2; index++ {
		parallelItem := models.DeliveryWorkItem{
			ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject,
			Title:           fmt.Sprintf("Local parallel plan %d", index+1),
			Description:     "Independent DAG root consumed by a dedicated local worker",
			ExpectedOutcome: "A durable parallel plan callback", State: deliveryworkflow.StatePlanning,
			IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`, AcceptanceJSON: `["fixture validation"]`,
			ClientContextJSON: `{}`, PlanJSON: `{}`,
		}
		parallelSnapshot := models.DeliveryContextSnapshot{
			ID: uuid.Must(uuid.NewV4()), WorkItemID: parallelItem.ID, SourceID: uuid.Must(uuid.NewV4()),
			Kind: "repository", Name: "Local fixture", Reference: "workspace://fixture", Revision: fixtureRevision,
			MetadataJSON: `{"repository_role":"primary","repository_kind":"backend_api","repository_responsibility":"Local parallel fixture"}`,
			CapturedAt:   time.Now().UTC(),
		}
		require.NoError(t, db.Create(&parallelItem).Error)
		require.NoError(t, db.Create(&parallelSnapshot).Error)
		parallelItems = append(parallelItems, parallelItem)
		itemID := parallelItem.ID
		t.Cleanup(func() {
			_ = db.Unscoped().Where("delivery_work_item_id = ?", itemID).Delete(&models.AutomationExecution{}).Error
			_ = db.Unscoped().Where("work_item_id = ?", itemID).Delete(&models.DeliveryEvidence{}).Error
			_ = db.Unscoped().Where("work_item_id = ?", itemID).Delete(&models.DeliveryGate{}).Error
			_ = db.Unscoped().Where("work_item_id = ?", itemID).Delete(&models.DeliveryContinuation{}).Error
			_ = db.Unscoped().Where("work_item_id = ?", itemID).Delete(&models.DeliveryChangeSet{}).Error
			_ = db.Unscoped().Where("work_item_id = ?", itemID).Delete(&models.DeliveryPlan{}).Error
			_ = db.Unscoped().Where("delivery_work_item_id = ?", itemID).Delete(&models.AutomationTask{}).Error
			_ = db.Unscoped().Where("correlation_id = ?", itemID.String()).Delete(&models.OutboxEvent{}).Error
			_ = db.Unscoped().Where("work_item_id = ?", itemID).Delete(&models.DeliveryContextSnapshot{}).Error
			_ = db.Unscoped().Where("id = ?", itemID).Delete(&models.DeliveryWorkItem{}).Error
		})

		parallelBody, marshalErr := json.Marshal(map[string]string{"phase": "plan", "instructions": "Produce the bounded independent local plan."})
		require.NoError(t, marshalErr)
		parallelEcho := echo.New()
		parallelRequest := httptest.NewRequest(http.MethodPost, "/api/automation/work-items/"+itemID.String()+"/agent-runs", bytes.NewReader(parallelBody))
		parallelRequest.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		parallelRecorder := httptest.NewRecorder()
		parallelContext := parallelEcho.NewContext(parallelRequest, parallelRecorder)
		parallelContext.SetPath("/api/automation/work-items/:id/agent-runs")
		parallelContext.SetParamNames("id")
		parallelContext.SetParamValues(itemID.String())
		parallelContext.Set("cognito_sub", subject)
		parallelContext.Set("tenant_code", "itbem")
		setRoundTripOrganizationWorkspace(parallelContext, client.ID)
		parallelContext.Set("config", cfg)
		require.NoError(t, delivery.StartAgentRun(parallelContext))
		require.Equal(t, http.StatusAccepted, parallelRecorder.Code, parallelRecorder.Body.String())
		var parallelTask models.AutomationTask
		require.NoError(t, db.Where("delivery_work_item_id = ? AND operation = ?", itemID, "delivery.plan").First(&parallelTask).Error)
		parallelTaskIDs[parallelTask.ID] = struct{}{}
	}

	parallelMessages := make(map[uuid.UUID]automationagent.QueueMessage, 2)
	parallelReceiveCtx, parallelReceiveCancel := context.WithTimeout(ctx, 20*time.Second)
	for len(parallelMessages) < 2 && parallelReceiveCtx.Err() == nil {
		batch, receiveErr := receiveIntegrationMessages(parallelReceiveCtx, queue, 2-len(parallelMessages))
		require.NoError(t, receiveErr)
		for _, raw := range batch {
			var queuedParallel automationagent.TaskMessage
			require.NoError(t, json.Unmarshal([]byte(raw.Body), &queuedParallel))
			parsedTaskID, parseErr := uuid.FromString(queuedParallel.Payload.TaskID)
			require.NoError(t, parseErr)
			if _, expected := parallelTaskIDs[parsedTaskID]; expected {
				parallelMessages[parsedTaskID] = raw
			}
		}
	}
	parallelReceiveCancel()
	require.Len(t, parallelMessages, 2, "the outbox must publish both independent DAG roots")
	// ReceiveMessage starts the SQS visibility lease. We only inspected these
	// envelopes above; release them before starting the real queue workers so
	// the same messages can be claimed by the worker loops below. Without this
	// explicit handoff the messages remain invisible for the queue's long lease
	// and the concurrency assertion would time out while testing the fixture,
	// not the workers.
	for _, raw := range parallelMessages {
		require.NoError(t, queue.Defer(ctx, raw, 1))
	}

	parallelProvider := &parallelPlanProvider{content: fake.content, entered: make(chan struct{}, 2), release: make(chan struct{})}
	providerTransport.setProvider(parallelProvider)
	parallelRunCtx, cancelParallelRuns := context.WithCancel(ctx)
	parallelErrors := make(chan error, 2)
	for index := 0; index < 2; index++ {
		parallelWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{
			InputBucket: inputBucket, OutputBucket: outputBucket, AgentKey: profile.AgentKey,
			MachineID: localIdentity.MachineID(), AllowedOperations: []string{"delivery.plan"},
		}, automationagent.NewAWSObjectStore(runtime.S3), callback, gatewayProvider)
		require.NoError(t, workerErr)
		go func(worker *automationagent.Worker) {
			parallelErrors <- automationagent.RunQueue(parallelRunCtx, worker, queue, 1, nil)
		}(parallelWorker)
	}
	for index := 0; index < 2; index++ {
		select {
		case <-parallelProvider.entered:
		case <-time.After(15 * time.Second):
			t.Fatal("both independent plan workers did not enter the provider concurrently")
		}
	}
	require.GreaterOrEqual(t, parallelProvider.maxActive.Load(), int32(2), "independent DAG roots must overlap on separate workers")
	close(parallelProvider.release)
	for taskID := range parallelTaskIDs {
		deadline := time.Now().Add(15 * time.Second)
		var completedParallel models.AutomationTask
		for time.Now().Before(deadline) {
			if err := db.First(&completedParallel, taskID).Error; err == nil && completedParallel.Status == "completed" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		require.Equalf(t, "completed", completedParallel.Status, "parallel worker callback status=%s error=%q", completedParallel.Status, completedParallel.ErrorMessage)
	}
	cancelParallelRuns()
	for index := 0; index < 2; index++ {
		require.NoError(t, <-parallelErrors)
	}
	// LocalStack may finish the server side of a canceled long-poll just after
	// the client observes RunQueue's cancellation. Let that short poll drain so
	// it cannot claim the next phase's messages on behalf of an exited worker.
	time.Sleep(2 * time.Second)
	require.Len(t, parallelItems, 2)

	// Reuse the same two independent roots for an adversarial second attempt.
	// One provider response is intentionally malformed. The control plane must
	// persist that branch as failed while the sibling still completes; neither
	// branch may be promoted as a synthetic global success.
	partialTaskIDs := make(map[uuid.UUID]string, 2)
	for index, parallelItem := range parallelItems {
		marker := fmt.Sprintf("partial-isolation-root-%d", index)
		partialBody, marshalErr := json.Marshal(map[string]string{"phase": "plan", "instructions": "Produce the bounded independent local plan " + marker})
		require.NoError(t, marshalErr)
		partialEcho := echo.New()
		partialRequest := httptest.NewRequest(http.MethodPost, "/api/automation/work-items/"+parallelItem.ID.String()+"/agent-runs", bytes.NewReader(partialBody))
		partialRequest.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		partialRecorder := httptest.NewRecorder()
		partialContext := partialEcho.NewContext(partialRequest, partialRecorder)
		partialContext.SetPath("/api/automation/work-items/:id/agent-runs")
		partialContext.SetParamNames("id")
		partialContext.SetParamValues(parallelItem.ID.String())
		partialContext.Set("cognito_sub", subject)
		partialContext.Set("tenant_code", "itbem")
		setRoundTripOrganizationWorkspace(partialContext, client.ID)
		partialContext.Set("config", cfg)
		require.NoError(t, delivery.StartAgentRun(partialContext))
		require.Equal(t, http.StatusAccepted, partialRecorder.Code, partialRecorder.Body.String())
		var partialTask models.AutomationTask
		require.NoError(t, db.Where("delivery_work_item_id = ? AND operation = ? AND status = ?", parallelItem.ID, "delivery.plan", "queued").Order("created_at DESC").First(&partialTask).Error)
		partialTaskIDs[partialTask.ID] = marker
	}

	// Start the real queue loops immediately after creating the recovery
	// attempts. Unlike a read-only envelope assertion, this lets the workers
	// themselves prove that the outbox publication is visible and processable;
	// manually receiving here would acquire an SQS lease and hide the messages
	// from the workers we are trying to exercise.
	visibleDeadline := time.Now().Add(20 * time.Second)
	for {
		queueAttributes, attributesErr := runtime.SQS.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl: aws.String(queueURL), AttributeNames: []types.QueueAttributeName{
				types.QueueAttributeNameApproximateNumberOfMessages,
			},
		})
		require.NoError(t, attributesErr)
		visible, parseErr := strconv.Atoi(queueAttributes.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)])
		require.NoError(t, parseErr)
		if visible >= 2 {
			break
		}
		if time.Now().After(visibleDeadline) {
			t.Fatalf("outbox did not expose both recovery-attempt messages: visible=%d", visible)
		}
		time.Sleep(100 * time.Millisecond)
	}
	partialProvider := &partialFailureProvider{content: fake.content, failMarker: "partial-isolation-root-0", entered: make(chan struct{}, 2), release: make(chan struct{})}
	providerTransport.setProvider(partialProvider)
	partialRunCtx, cancelPartialRuns := context.WithCancel(ctx)
	partialErrors := make(chan error, 2)
	for index := 0; index < 2; index++ {
		partialWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{
			InputBucket: inputBucket, OutputBucket: outputBucket, AgentKey: profile.AgentKey,
			MachineID: localIdentity.MachineID(), AllowedOperations: []string{"delivery.plan"},
		}, automationagent.NewAWSObjectStore(runtime.S3), callback, gatewayProvider)
		require.NoError(t, workerErr)
		go func(worker *automationagent.Worker) {
			partialErrors <- automationagent.RunQueue(partialRunCtx, worker, queue, 1, nil)
		}(partialWorker)
	}
	for index := 0; index < 2; index++ {
		select {
		case <-partialProvider.entered:
		case <-time.After(15 * time.Second):
			t.Fatal("both partial-isolation workers did not enter the provider concurrently")
		}
	}
	require.GreaterOrEqual(t, partialProvider.maxActive.Load(), int32(2), "partial-isolation roots must overlap before one fails")
	close(partialProvider.release)
	for taskID, marker := range partialTaskIDs {
		wantStatus := "completed"
		if marker == partialProvider.failMarker {
			wantStatus = "failed"
		}
		deadline := time.Now().Add(15 * time.Second)
		var partialTask models.AutomationTask
		for time.Now().Before(deadline) {
			if err := db.First(&partialTask, taskID).Error; err == nil && partialTask.Status == wantStatus {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		require.Equalf(t, wantStatus, partialTask.Status, "partial branch %s status=%s error=%q", marker, partialTask.Status, partialTask.ErrorMessage)
		if wantStatus == "failed" {
			require.NotEmpty(t, partialTask.ErrorMessage, "failed branch must expose a durable diagnostic")
		} else {
			require.NotEmpty(t, partialTask.OutputRef, "successful sibling must retain its private result")
		}
	}
	cancelPartialRuns()
	for index := 0; index < 2; index++ {
		require.NoError(t, <-partialErrors)
	}

	// Promote through the real delivery artifact controller. This verifies that
	// the dashboard can consume the worker's immutable run-scoped result rather
	// than relying on a pre-seeded legacy task pointer.
	promoteEcho := echo.New()
	promoteRequest := httptest.NewRequest(http.MethodPost, "/api/automation/work-items/"+item.ID.String()+"/plans/promote-agent", nil)
	promoteRecorder := httptest.NewRecorder()
	promoteContext := promoteEcho.NewContext(promoteRequest, promoteRecorder)
	promoteContext.SetPath("/api/automation/work-items/:id/plans/promote-agent")
	promoteContext.SetParamNames("id")
	promoteContext.SetParamValues(item.ID.String())
	promoteContext.Set("cognito_sub", subject)
	promoteContext.Set("tenant_code", "itbem")
	setRoundTripOrganizationWorkspace(promoteContext, client.ID)
	promoteContext.Set("config", cfg)
	require.NoError(t, delivery.PromoteLatestAgentPlan(promoteContext))
	require.Equal(t, http.StatusCreated, promoteRecorder.Code, promoteRecorder.Body.String())
	var promotedPlan models.DeliveryPlan
	require.NoError(t, db.Where("work_item_id = ?", item.ID).First(&promotedPlan).Error)
	require.Contains(t, promotedPlan.StructuredJSON, "Bounded local plan")
	transitionWorkItem := func(workItemID uuid.UUID, action deliveryworkflow.Action, comment string, checklist []string) {
		t.Helper()
		payload, marshalErr := json.Marshal(map[string]any{"action": action, "comment": comment, "evidence_checklist": checklist})
		require.NoError(t, marshalErr)
		transitionEcho := echo.New()
		transitionRequest := httptest.NewRequest(http.MethodPost, "/api/automation/work-items/"+workItemID.String()+"/transitions", bytes.NewReader(payload))
		transitionRequest.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		transitionRecorder := httptest.NewRecorder()
		transitionContext := transitionEcho.NewContext(transitionRequest, transitionRecorder)
		transitionContext.SetPath("/api/automation/work-items/:id/transitions")
		transitionContext.SetParamNames("id")
		transitionContext.SetParamValues(workItemID.String())
		transitionContext.Set("cognito_sub", subject)
		transitionContext.Set("tenant_code", "itbem")
		setRoundTripOrganizationWorkspace(transitionContext, client.ID)
		transitionContext.Set("config", cfg)
		require.NoError(t, delivery.TransitionWorkItem(transitionContext))
		require.Equal(t, http.StatusOK, transitionRecorder.Code, transitionRecorder.Body.String())
	}
	transition := func(action deliveryworkflow.Action, comment string, checklist []string) {
		transitionWorkItem(item.ID, action, comment, checklist)
	}
	transition(deliveryworkflow.ActionSubmitPlan, "Plan listo para revisión", nil)
	transition(deliveryworkflow.ActionApprovePlan, "Alcance aprobado para implementación local", []string{"plan estructurado", "alcance confirmado"})

	// Run the next phase through the same real control-plane/outbox/queue path.
	implementationBody, marshalErr := json.Marshal(map[string]string{"phase": "implementation", "instructions": "Apply only the approved README change."})
	require.NoError(t, marshalErr)
	implementationWorkerID := uuid.Must(uuid.NewV4()).String()
	// The integration TestMain migrates a fresh schema but intentionally does
	// not run application catalog seeds. Mirror the production profile bootstrap
	// before registering the local implementation worker.
	implementationMachineID := localIdentity.MachineID()
	registerRoundTripImplementationWorker(t, ctx, callback, implementationWorkerID, profile.AgentKey, implementationMachineID)
	var implementationHeartbeat models.AutomationAgentHeartbeat
	require.NoError(t, db.Where("worker_id = ?", implementationWorkerID).First(&implementationHeartbeat).Error)
	require.Equal(t, profile.AgentKey, implementationHeartbeat.AgentKey)
	require.Equal(t, localIdentity.MachineID(), implementationHeartbeat.MachineID)
	implementationEcho := echo.New()
	implementationRequest := httptest.NewRequest(http.MethodPost, "/api/automation/work-items/"+item.ID.String()+"/agent-runs", bytes.NewReader(implementationBody))
	implementationRequest.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	implementationRecorder := httptest.NewRecorder()
	implementationContext := implementationEcho.NewContext(implementationRequest, implementationRecorder)
	implementationContext.SetPath("/api/automation/work-items/:id/agent-runs")
	implementationContext.SetParamNames("id")
	implementationContext.SetParamValues(item.ID.String())
	implementationContext.Set("cognito_sub", subject)
	implementationContext.Set("tenant_code", "itbem")
	setRoundTripOrganizationWorkspace(implementationContext, client.ID)
	implementationContext.Set("config", cfg)
	require.NoError(t, delivery.StartAgentRun(implementationContext))
	require.Equal(t, http.StatusAccepted, implementationRecorder.Code, implementationRecorder.Body.String())
	var implementationMessages []automationagent.QueueMessage
	implementationReceiveCtx, implementationReceiveCancel := context.WithTimeout(ctx, 35*time.Second)
	defer implementationReceiveCancel()
	implementationMessages, err = receiveIntegrationMessages(implementationReceiveCtx, queue, 1)
	require.NoError(t, err)
	require.Len(t, implementationMessages, 1)
	var implementationQueued automationagent.TaskMessage
	require.NoError(t, json.Unmarshal([]byte(implementationMessages[0].Body), &implementationQueued))
	implementationTaskID, parseImplementationTaskIDErr := uuid.FromString(implementationQueued.Payload.TaskID)
	require.NoError(t, parseImplementationTaskIDErr)
	var implementationTask models.AutomationTask
	require.NoError(t, db.Where("id = ? AND delivery_work_item_id = ? AND operation = ?", implementationTaskID, item.ID, "delivery.implementation").First(&implementationTask).Error)
	require.Equal(t, implementationTask.ID.String(), implementationQueued.Payload.TaskID)
	implementationWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{
		InputBucket: inputBucket, OutputBucket: outputBucket, WorkerID: implementationWorkerID,
		AgentKey: profile.AgentKey, MachineID: implementationMachineID, AllowedOperations: []string{"delivery.implementation"},
	}, automationagent.NewAWSObjectStore(runtime.S3), callback, gatewayProvider)
	providerTransport.setProvider(&roundtripImplementationProvider{})
	require.NoError(t, workerErr)
	require.NoError(t, automationagent.ProcessQueueMessage(ctx, implementationWorker, queue, implementationMessages[0]))
	var completedImplementation models.AutomationTask
	require.NoError(t, db.First(&completedImplementation, implementationTask.ID).Error)
	completionCallbackRejection := callbackRejection{}
	if rejection, ok := rejectedCompletionCallbackStatuses.Load(implementationTask.ID.String()); ok {
		completionCallbackRejection = rejection.(callbackRejection)
	}
	require.Equalf(t, "completed", completedImplementation.Status,
		"implementation callback error=%q; completion callback HTTP status=%d category=%q",
		completedImplementation.ErrorMessage, completionCallbackRejection.Status, completionCallbackRejection.Message)

	// Approved plans fan out one child task per step. The first implementation
	// child must persist its patch and publish only the dependency-ready
	// integration child; the parent change set exists only after verified fan-in.
	var implementationAssignment models.DeliveryPlanStepAssignment
	require.NoError(t, db.Where("child_automation_task_id = ?", implementationTask.ID).First(&implementationAssignment).Error)
	var planExecution models.DeliveryPlanExecution
	require.NoError(t, db.First(&planExecution, "id = ?", implementationAssignment.ExecutionID).Error)
	var parentTask models.AutomationTask
	require.NoError(t, db.First(&parentTask, planExecution.AutomationTaskID).Error)

	integrationReceiveCtx, integrationReceiveCancel := context.WithTimeout(ctx, 35*time.Second)
	integrationMessages, integrationReceiveErr := receiveIntegrationMessages(integrationReceiveCtx, queue, 1)
	integrationReceiveCancel()
	require.NoError(t, integrationReceiveErr)
	require.Len(t, integrationMessages, 1)
	var integrationQueued automationagent.TaskMessage
	require.NoError(t, json.Unmarshal([]byte(integrationMessages[0].Body), &integrationQueued))
	require.Equal(t, "delivery.implementation", integrationQueued.Payload.Operation)
	require.NotEmpty(t, integrationQueued.Payload.PlanStepID)
	require.NotEqual(t, implementationQueued.Payload.PlanStepID, integrationQueued.Payload.PlanStepID)
	var integrationStep models.DeliveryPlanStep
	require.NoError(t, db.First(&integrationStep, "id = ?", integrationQueued.Payload.PlanStepID).Error)
	require.Equal(t, models.DeliveryPlanStepRoleIntegration, integrationStep.Role)

	providerTransport.setProvider(&roundtripProvider{
		content:    `{"action":"verify_integration"}`,
		responseID: "roundtrip-integration",
	})
	integrationWorker, integrationWorkerErr := automationagent.NewWorker(automationagent.WorkerConfig{
		InputBucket: inputBucket, OutputBucket: outputBucket, WorkerID: implementationWorkerID,
		AgentKey: profile.AgentKey, MachineID: implementationMachineID, AllowedOperations: []string{"delivery.implementation"},
	}, automationagent.NewAWSObjectStore(runtime.S3), callback, gatewayProvider)
	require.NoError(t, integrationWorkerErr)
	require.NoError(t, automationagent.ProcessQueueMessage(ctx, integrationWorker, queue, integrationMessages[0]))
	require.NoError(t, db.First(&parentTask, planExecution.AutomationTaskID).Error)
	require.Equalf(t, "completed", parentTask.Status, "verified plan fan-in did not complete the parent: %q", parentTask.ErrorMessage)
	var completedPlanExecution models.DeliveryPlanExecution
	require.NoError(t, db.First(&completedPlanExecution, planExecution.ID).Error)
	require.Equal(t, models.DeliveryPlanExecutionCompleted, completedPlanExecution.Status)

	var changeSet models.DeliveryChangeSet
	require.NoError(t, db.Where("work_item_id = ?", item.ID).Order("created_at DESC").First(&changeSet).Error)
	require.Equal(t, "workspace://fixture", changeSet.RepositoryRef)
	require.Equal(t, "local_worktree", changeSet.ReviewType)
	require.Equal(t, "passed", changeSet.CIStatus)
	exerciseAutomaticPlanStepFanout(t, ctx, runtime, cfg, inputBucket, outputBucket, queue, callback, project, client.ID, subject, implementationWorkerID, profile.AgentKey, implementationMachineID, workspaceRoot, fixtureRevision, gatewayProvider, providerTransport)
	transition(deliveryworkflow.ActionSubmitCodeReview, "Cambio local listo para revisión", nil)
	transition(deliveryworkflow.ActionApproveCodeReview, "Cambio revisado y validación local aprobada", []string{"diff revisado", "CI local aprobado"})

	// Exercise QA with a separately persisted qa_running item. This preserves
	// the strict publication requirement on the implementation item while still
	// proving that the exact reviewed worktree reaches the real QA worker.
	previewServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>local preview</title><main>fixture preview</main>"))
	}))
	defer previewServer.Close()
	qaPlanJSON := fmt.Sprintf(`{"execution_steps":[{"step_key":"qa-implementation","role":"implementation","order":0,"title":"Prepare reviewed fixture","objective":"Keep the approved local worktree available for QA","acceptance_criteria":["The approved local worktree is available for review"]},{"step_key":"qa-integration","role":"integration","order":1,"title":"Confirm QA handoff","objective":"Verify the reviewed worktree before QA","acceptance_criteria":["The approved local worktree is available for review"],"depends_on":["qa-implementation"]}],"files_impacted":["README.md"],"acceptance_criteria":["The approved local worktree is available for review"],"repository_impact":[{"name":"Local fixture","reference":"workspace://fixture","revision":"%s","role":"primary","impact":"changes","notes":"README fixture"}],"qa_execution_matrix":[{"repository_ref":"workspace://fixture","run_validation":false,"run_qa":true,"run_stagehand":false,"collect_evidence":false}]}`, fixtureRevision)
	qaItem := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject, Title: "Local QA roundtrip", Description: "Run bounded QA against the reviewed worktree", ExpectedOutcome: "A durable QA report", State: deliveryworkflow.StateQARunning, IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`, AcceptanceJSON: `["fixture validation"]`, ClientContextJSON: `{}`, PlanJSON: qaPlanJSON, PreviewURL: previewServer.URL}
	qaPlanGate := models.DeliveryGate{ID: uuid.Must(uuid.NewV4()), WorkItemID: qaItem.ID, Kind: deliveryworkflow.GatePlan, Decision: deliveryworkflow.DecisionApproved, DecidedBy: subject, Comment: "Human-approved plan fixture for QA", EvidenceChecklist: `["fixture scope reviewed"]`, DecidedAt: time.Now().UTC()}
	qaApprovedPlan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: qaItem.ID, Version: 1, Status: "approved", Summary: "Approved bounded local QA plan", StructuredJSON: qaPlanJSON, ProposedBy: subject, ApprovedGateID: &qaPlanGate.ID}
	qaSnapshot := models.DeliveryContextSnapshot{ID: uuid.Must(uuid.NewV4()), WorkItemID: qaItem.ID, SourceID: uuid.Must(uuid.NewV4()), Kind: "repository", Name: "Local fixture", Reference: "workspace://fixture", Revision: fixtureRevision, MetadataJSON: `{"repository_role":"primary","repository_kind":"backend_api","repository_responsibility":"Local QA fixture"}`, CapturedAt: time.Now().UTC()}
	qaChangeSet := models.DeliveryChangeSet{ID: uuid.Must(uuid.NewV4()), WorkItemID: qaItem.ID, RepositoryRef: "workspace://fixture", Branch: changeSet.Branch, ReviewType: "local_worktree", CIStatus: "passed", PreviewURL: previewServer.URL, Environment: "local", MetadataJSON: `{}`, CreatedBy: subject}
	qaCleanupIDs := []uuid.UUID{qaItem.ID}
	t.Cleanup(func() {
		_ = db.Unscoped().Where("delivery_work_item_id IN ?", qaCleanupIDs).Delete(&models.AutomationExecution{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", qaCleanupIDs).Delete(&models.DeliveryEvidence{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", qaCleanupIDs).Delete(&models.DeliveryContinuation{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", qaCleanupIDs).Delete(&models.DeliveryChangeSet{}).Error
		_ = db.Unscoped().Where("plan_id = ?", qaApprovedPlan.ID).Delete(&models.DeliveryPlanStepDependency{}).Error
		_ = db.Unscoped().Where("plan_id = ?", qaApprovedPlan.ID).Delete(&models.DeliveryPlanStep{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", qaCleanupIDs).Delete(&models.DeliveryPlan{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", qaCleanupIDs).Delete(&models.DeliveryGate{}).Error
		_ = db.Unscoped().Where("delivery_work_item_id IN ?", qaCleanupIDs).Delete(&models.AutomationTask{}).Error
		for _, id := range qaCleanupIDs {
			_ = db.Unscoped().Where("correlation_id = ?", id.String()).Delete(&models.OutboxEvent{}).Error
		}
		_ = db.Unscoped().Where("work_item_id IN ?", qaCleanupIDs).Delete(&models.DeliveryContextSnapshot{}).Error
		_ = db.Unscoped().Where("id IN ?", qaCleanupIDs).Delete(&models.DeliveryWorkItem{}).Error
	})
	for _, value := range []any{&qaItem, &qaPlanGate, &qaApprovedPlan, &qaSnapshot, &qaChangeSet} {
		require.NoError(t, db.Create(value).Error)
	}

	receiveTaskMessage := func(expected uuid.UUID) automationagent.QueueMessage {
		t.Helper()
		var received []automationagent.QueueMessage
		receiveCtx, receiveCancel := context.WithTimeout(ctx, 20*time.Second)
		defer receiveCancel()
		received, err := receiveIntegrationMessages(receiveCtx, queue, 1)
		require.NoError(t, err)
		require.Len(t, received, 1, "the outbox dispatcher must publish the control-plane task")
		var queued automationagent.TaskMessage
		require.NoError(t, json.Unmarshal([]byte(received[0].Body), &queued))
		require.Equal(t, expected.String(), queued.Payload.TaskID)
		return received[0]
	}

	qaBody, marshalErr := json.Marshal(map[string]string{"phase": "qa", "instructions": "Run only the approved local QA matrix."})
	require.NoError(t, marshalErr)
	qaEcho := echo.New()
	qaRequest := httptest.NewRequest(http.MethodPost, "/api/automation/work-items/"+qaItem.ID.String()+"/agent-runs", bytes.NewReader(qaBody))
	qaRequest.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	qaRecorder := httptest.NewRecorder()
	qaContext := qaEcho.NewContext(qaRequest, qaRecorder)
	qaContext.SetPath("/api/automation/work-items/:id/agent-runs")
	qaContext.SetParamNames("id")
	qaContext.SetParamValues(qaItem.ID.String())
	qaContext.Set("cognito_sub", subject)
	qaContext.Set("tenant_code", "itbem")
	setRoundTripOrganizationWorkspace(qaContext, client.ID)
	qaContext.Set("config", cfg)
	require.NoError(t, delivery.StartAgentRun(qaContext))
	require.Equal(t, http.StatusAccepted, qaRecorder.Code, qaRecorder.Body.String())
	var qaTask models.AutomationTask
	require.NoError(t, db.Where("delivery_work_item_id = ? AND operation = ?", qaItem.ID, "delivery.qa").First(&qaTask).Error)
	qaMessage := receiveTaskMessage(qaTask.ID)
	qaProvider := &roundtripProvider{content: `{"summary":"QA observed","verdict":"passed","checks":[{"name":"go version","status":"passed","detail":"The approved QA command exited successfully."}],"defects":[],"coverage_gaps":[],"recommended_actions":[]}`, responseID: "roundtrip-qa"}
	providerTransport.setProvider(qaProvider)
	qaWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{
		InputBucket: inputBucket, OutputBucket: outputBucket, AgentKey: profile.AgentKey,
		MachineID: localIdentity.MachineID(), AllowedOperations: []string{"delivery.qa"},
	}, automationagent.NewAWSObjectStore(runtime.S3), callback, gatewayProvider)
	require.NoError(t, workerErr)
	require.NoError(t, automationagent.ProcessQueueMessage(ctx, qaWorker, queue, qaMessage))
	var completedQA models.AutomationTask
	require.NoError(t, db.First(&completedQA, qaTask.ID).Error)
	require.Equalf(t, "completed", completedQA.Status, "qa callback error=%q", completedQA.ErrorMessage)
	var qaExecution models.AutomationExecution
	require.NoError(t, db.Where("automation_task_id = ?", qaTask.ID).First(&qaExecution).Error)
	require.Equal(t, "qa", qaExecution.StepKey)
	require.NotEmpty(t, completedQA.OutputRef)
	qaResultBucket, qaResultKey, parseErr := automationagent.ParsePrivateReference(completedQA.OutputRef)
	require.NoError(t, parseErr)
	qaObject, getErr := runtime.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(qaResultBucket), Key: aws.String(qaResultKey)})
	require.NoError(t, getErr)
	qaOutputBody, readErr := io.ReadAll(qaObject.Body)
	require.NoError(t, readErr)
	_ = qaObject.Body.Close()
	var qaOutput map[string]any
	require.NoError(t, json.Unmarshal(qaOutputBody, &qaOutput))
	qaStructured, ok := qaOutput["structured_result"].(map[string]any)
	require.True(t, ok, "QA provider report should survive in the immutable result")
	require.Equal(t, "passed", qaStructured["verdict"])

	// Exercise the human QA gates and durable continuation dispatcher instead
	// of only starting a summary explicitly. The stale preview continuation on
	// the earlier item was intentionally superseded by the explicit local run;
	// keep this deterministic tick scoped to the QA fixture below.
	require.NoError(t, db.Model(&models.DeliveryContinuation{}).Where("work_item_id = ?", item.ID).Update("status", "superseded").Error)
	transitionWorkItem(qaItem.ID, deliveryworkflow.ActionSubmitQA, "QA observada y lista para revisión", nil)
	transitionWorkItem(qaItem.ID, deliveryworkflow.ActionApproveQA, "QA local aprobada para preparar el resumen", []string{"preview observado", "go version aprobado"})
	var qaAfterGate models.DeliveryWorkItem
	require.NoError(t, db.First(&qaAfterGate, qaItem.ID).Error)
	require.Equal(t, deliveryworkflow.StateReleaseReview, qaAfterGate.State)
	var qaEvidence models.DeliveryEvidence
	require.NoError(t, db.Where("work_item_id = ? AND phase = ?", qaItem.ID, "qa").Order("created_at DESC").First(&qaEvidence).Error)
	var qaGate models.DeliveryGate
	require.NoError(t, db.Where("work_item_id = ? AND kind = ?", qaItem.ID, "qa_review").Order("created_at DESC").First(&qaGate).Error)
	var summaryContinuation models.DeliveryContinuation
	require.NoError(t, db.Where("work_item_id = ? AND phase = ? AND status = ?", qaItem.ID, "summary", "pending").First(&summaryContinuation).Error)
	var continuationSummaryTask models.AutomationTask
	// A database can contain several durable intents from earlier phases. The
	// dispatcher reconciles them safely, but a synchronous probe should allow
	// more than one bounded tick before declaring the current continuation lost.
	for attempt := 0; attempt < 16; attempt++ {
		require.NoError(t, db.Model(&models.DeliveryContinuation{}).Where("id = ?", summaryContinuation.ID).Updates(map[string]any{"status": "pending", "available_at": time.Now().UTC()}).Error)
		require.NoError(t, delivery.DispatchContinuationsOnce(ctx, db, cfg))
		if continuationTaskErr := db.Where("continuation_id = ? AND operation = ?", summaryContinuation.ID, "delivery.summary").First(&continuationSummaryTask).Error; continuationTaskErr == nil {
			break
		}
	}
	if continuationTaskErr := db.Where("continuation_id = ? AND operation = ?", summaryContinuation.ID, "delivery.summary").First(&continuationSummaryTask).Error; continuationTaskErr != nil {
		var debugContinuation models.DeliveryContinuation
		_ = db.First(&debugContinuation, summaryContinuation.ID).Error
		t.Logf("summary continuation did not create task: status=%s attempts=%d available_at=%s error=%v", debugContinuation.Status, debugContinuation.Attempts, debugContinuation.AvailableAt.UTC().Format(time.RFC3339Nano), continuationTaskErr)
		var debugTasks []models.AutomationTask
		_ = db.Where("delivery_work_item_id = ?", qaItem.ID).Order("created_at ASC").Find(&debugTasks).Error
		for _, debugTask := range debugTasks {
			t.Logf("qa item task: id=%s operation=%s status=%s continuation=%v", debugTask.ID, debugTask.Operation, debugTask.Status, debugTask.ContinuationID)
		}
		t.Fatalf("find continuation summary task: %v", continuationTaskErr)
	}
	continuationSummaryMessage := receiveTaskMessage(continuationSummaryTask.ID)
	continuationSummaryProvider := &roundtripProvider{content: fmt.Sprintf(`{"executive":{"what_changed":"Bounded local delivery","why":"Prepare a grounded handoff","how_to_test":"go version and observed preview","risks":["No remote publication was attempted"]},"technical":{"decisions":["plan approved; %s %s"],"evidence":["%s — %s"]}}`, qaGate.Kind, qaGate.Decision, qaEvidence.ID.String(), qaEvidence.Title), responseID: "roundtrip-continuation-summary"}
	providerTransport.setProvider(continuationSummaryProvider)
	continuationSummaryWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{
		InputBucket: inputBucket, OutputBucket: outputBucket, AgentKey: profile.AgentKey,
		MachineID: localIdentity.MachineID(), AllowedOperations: []string{"delivery.summary"},
	}, automationagent.NewAWSObjectStore(runtime.S3), callback, gatewayProvider)
	require.NoError(t, workerErr)
	require.NoError(t, automationagent.ProcessQueueMessage(ctx, continuationSummaryWorker, queue, continuationSummaryMessage))
	var completedContinuationSummaryTask models.AutomationTask
	require.NoError(t, db.First(&completedContinuationSummaryTask, continuationSummaryTask.ID).Error)
	require.Equalf(t, "completed", completedContinuationSummaryTask.Status, "continuation summary failed: %s", completedContinuationSummaryTask.ErrorMessage)
	var preparedRelease models.DeliveryRelease
	for attempt := 0; attempt < 16; attempt++ {
		// The production dispatcher deliberately applies a short visibility delay
		// after enqueueing. Advance only this test continuation's availability so
		// the synchronous reconciliation does not sleep five seconds.
		require.NoError(t, db.Model(&models.DeliveryContinuation{}).Where("id = ?", summaryContinuation.ID).Updates(map[string]any{"available_at": time.Now().UTC()}).Error)
		require.NoError(t, delivery.DispatchContinuationsOnce(ctx, db, cfg))
		if dbErr := db.Where("work_item_id = ? AND status = ?", qaItem.ID, "ready").First(&preparedRelease).Error; dbErr == nil {
			break
		}
	}
	require.NoError(t, db.Where("work_item_id = ? AND status = ?", qaItem.ID, "ready").First(&preparedRelease).Error)
	require.Equal(t, completedContinuationSummaryTask.OutputRef, preparedRelease.ReportRef)
	transitionWorkItem(qaItem.ID, deliveryworkflow.ActionApproveRelease, "Resumen revisado y entrega local aprobada", []string{"summary grounded", "QA gate approved"})
	var releasedQAItem models.DeliveryWorkItem
	require.NoError(t, db.First(&releasedQAItem, qaItem.ID).Error)
	require.Equal(t, deliveryworkflow.StateReleased, releasedQAItem.State)

	// Summary is another real worker handoff. Its response must cite immutable
	// evidence and the recorded human decision or the harness fails closed.
	summaryItem := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject, Title: "Local summary roundtrip", Description: "Prepare a grounded release review draft", ExpectedOutcome: "A summary tied to QA evidence", State: deliveryworkflow.StateReleaseReview, IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`, AcceptanceJSON: `["fixture validation"]`, ClientContextJSON: `{}`, PlanJSON: qaPlanJSON, PreviewURL: previewServer.URL}
	summaryPlanGate := models.DeliveryGate{ID: uuid.Must(uuid.NewV4()), WorkItemID: summaryItem.ID, Kind: deliveryworkflow.GatePlan, Decision: deliveryworkflow.DecisionApproved, DecidedBy: subject, Comment: "Human-approved plan fixture for release summary", EvidenceChecklist: `["fixture scope reviewed"]`, DecidedAt: time.Now().UTC()}
	summaryApprovedPlan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: summaryItem.ID, Version: 1, Status: "approved", Summary: "Approved bounded local release-summary plan", StructuredJSON: qaPlanJSON, ProposedBy: subject, ApprovedGateID: &summaryPlanGate.ID}
	summarySnapshot := models.DeliveryContextSnapshot{ID: uuid.Must(uuid.NewV4()), WorkItemID: summaryItem.ID, SourceID: uuid.Must(uuid.NewV4()), Kind: "repository", Name: "Local fixture", Reference: "workspace://fixture", Revision: fixtureRevision, MetadataJSON: `{"repository_role":"primary","repository_kind":"backend_api","repository_responsibility":"Local summary fixture"}`, CapturedAt: time.Now().UTC()}
	evidenceID := uuid.Must(uuid.NewV4())
	capturedAt := time.Now().UTC()
	summaryEvidence := models.DeliveryEvidence{ID: evidenceID, WorkItemID: summaryItem.ID, Kind: "report", Phase: "qa", Title: "QA evidence", Reference: "s3://" + outputBucket + "/qa-evidence.json", CapturedBy: "integration", CapturedAt: &capturedAt, MetadataJSON: `{}`}
	summaryGate := models.DeliveryGate{ID: uuid.Must(uuid.NewV4()), WorkItemID: summaryItem.ID, Kind: "qa_review", Decision: "approved", DecidedBy: subject, Comment: "QA evidence approved for release review", EvidenceChecklist: `["preview observed","go version passed"]`, DecidedAt: capturedAt}
	summaryCleanupIDs := []uuid.UUID{summaryItem.ID}
	t.Cleanup(func() {
		_ = db.Unscoped().Where("delivery_work_item_id IN ?", summaryCleanupIDs).Delete(&models.AutomationExecution{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", summaryCleanupIDs).Delete(&models.DeliveryEvidence{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", summaryCleanupIDs).Delete(&models.DeliveryContinuation{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", summaryCleanupIDs).Delete(&models.DeliveryChangeSet{}).Error
		_ = db.Unscoped().Where("plan_id = ?", summaryApprovedPlan.ID).Delete(&models.DeliveryPlanStepDependency{}).Error
		_ = db.Unscoped().Where("plan_id = ?", summaryApprovedPlan.ID).Delete(&models.DeliveryPlanStep{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", summaryCleanupIDs).Delete(&models.DeliveryPlan{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", summaryCleanupIDs).Delete(&models.DeliveryGate{}).Error
		_ = db.Unscoped().Where("delivery_work_item_id IN ?", summaryCleanupIDs).Delete(&models.AutomationTask{}).Error
		for _, id := range summaryCleanupIDs {
			_ = db.Unscoped().Where("correlation_id = ?", id.String()).Delete(&models.OutboxEvent{}).Error
		}
		_ = db.Unscoped().Where("work_item_id IN ?", summaryCleanupIDs).Delete(&models.DeliveryContextSnapshot{}).Error
		_ = db.Unscoped().Where("id IN ?", summaryCleanupIDs).Delete(&models.DeliveryWorkItem{}).Error
	})
	for _, value := range []any{&summaryItem, &summaryPlanGate, &summaryApprovedPlan, &summarySnapshot, &summaryEvidence, &summaryGate} {
		require.NoError(t, db.Create(value).Error)
	}
	summaryBody, marshalErr := json.Marshal(map[string]string{"phase": "summary", "instructions": "Prepare the release review draft from recorded evidence."})
	require.NoError(t, marshalErr)
	summaryEcho := echo.New()
	summaryRequest := httptest.NewRequest(http.MethodPost, "/api/automation/work-items/"+summaryItem.ID.String()+"/agent-runs", bytes.NewReader(summaryBody))
	summaryRequest.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	summaryRecorder := httptest.NewRecorder()
	summaryContext := summaryEcho.NewContext(summaryRequest, summaryRecorder)
	summaryContext.SetPath("/api/automation/work-items/:id/agent-runs")
	summaryContext.SetParamNames("id")
	summaryContext.SetParamValues(summaryItem.ID.String())
	summaryContext.Set("cognito_sub", subject)
	summaryContext.Set("tenant_code", "itbem")
	setRoundTripOrganizationWorkspace(summaryContext, client.ID)
	summaryContext.Set("config", cfg)
	require.NoError(t, delivery.StartAgentRun(summaryContext))
	require.Equal(t, http.StatusAccepted, summaryRecorder.Code, summaryRecorder.Body.String())
	var summaryTask models.AutomationTask
	require.NoError(t, db.Where("delivery_work_item_id = ? AND operation = ?", summaryItem.ID, "delivery.summary").First(&summaryTask).Error)
	summaryMessage := receiveTaskMessage(summaryTask.ID)
	summaryProvider := &roundtripProvider{content: fmt.Sprintf(`{"executive":{"what_changed":"Bounded local delivery","why":"Validate the durable handoff","how_to_test":"go version and observed preview","risks":["No remote publication was attempted"]},"technical":{"decisions":["plan approved; qa_review approved"],"evidence":["%s — QA evidence"]}}`, evidenceID.String()), responseID: "roundtrip-summary"}
	providerTransport.setProvider(summaryProvider)
	summaryWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{
		InputBucket: inputBucket, OutputBucket: outputBucket, AgentKey: profile.AgentKey,
		MachineID: localIdentity.MachineID(), AllowedOperations: []string{"delivery.summary"},
	}, automationagent.NewAWSObjectStore(runtime.S3), callback, gatewayProvider)
	require.NoError(t, workerErr)
	require.NoError(t, automationagent.ProcessQueueMessage(ctx, summaryWorker, queue, summaryMessage))
	var completedSummary models.AutomationTask
	require.NoError(t, db.First(&completedSummary, summaryTask.ID).Error)
	require.Equalf(t, "completed", completedSummary.Status, "summary callback error=%q", completedSummary.ErrorMessage)
	var summaryExecution models.AutomationExecution
	require.NoError(t, db.Where("automation_task_id = ?", summaryTask.ID).First(&summaryExecution).Error)
	require.Equal(t, "summary", summaryExecution.StepKey)
	require.NotEmpty(t, completedSummary.OutputRef)
	summaryResultBucket, summaryResultKey, parseErr := automationagent.ParsePrivateReference(completedSummary.OutputRef)
	require.NoError(t, parseErr)
	summaryObject, getErr := runtime.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(summaryResultBucket), Key: aws.String(summaryResultKey)})
	require.NoError(t, getErr)
	summaryOutputBody, readErr := io.ReadAll(summaryObject.Body)
	require.NoError(t, readErr)
	_ = summaryObject.Body.Close()
	var summaryOutput map[string]any
	require.NoError(t, json.Unmarshal(summaryOutputBody, &summaryOutput))
	summaryStructured, ok := summaryOutput["structured_result"].(map[string]any)
	require.True(t, ok, "summary provider report should survive in the immutable result")
	summaryTechnical, ok := summaryStructured["technical"].(map[string]any)
	require.True(t, ok)
	summaryEvidenceRows, ok := summaryTechnical["evidence"].([]any)
	require.True(t, ok)
	require.Contains(t, fmt.Sprint(summaryEvidenceRows[0]), evidenceID.String())
	resultBucket, resultKey, err := automationagent.ParsePrivateReference(completed.OutputRef)
	require.NoError(t, err)
	result, err := runtime.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(resultBucket), Key: aws.String(resultKey)})
	require.NoError(t, err)
	require.Equal(t, "AES256", string(result.ServerSideEncryption))
	_ = result.Body.Close()
	require.Equal(t, 1, fake.calls)
}

// setRoundTripOrganizationWorkspace simulates the trusted workspace values
// inserted by application-access middleware on an authenticated customer
// request. The organization is the fixture client that owns the project.
func setRoundTripOrganizationWorkspace(c echo.Context, organizationID uuid.UUID) {
	c.Set("workspace_mode", "organization")
	c.Set("organization_id", organizationID)
}

func configureRoundTripAIActionPolicies(t *testing.T, db *gorm.DB, operations ...string) {
	t.Helper()
	if db == nil || len(operations) == 0 {
		t.Fatal("integration AI policies require a database and at least one operation")
	}
	routes := `[ {"provider":"minimax","model":"MiniMax-M3"} ]`
	original := make(map[string]*models.AutomationAIActionPolicy, len(operations))
	now := time.Now().UTC()
	for _, operation := range operations {
		var previous models.AutomationAIActionPolicy
		err := db.Where("operation = ?", operation).First(&previous).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("load integration AI policy %s: %v", operation, err)
		}
		if err == nil {
			copy := previous
			original[operation] = &copy
			previous.Provider = string(automationagent.ProviderMiniMax)
			previous.Model = "MiniMax-M3"
			previous.ReasoningEnabled = false
			previous.ReasoningEffort = ""
			previous.RoutesJSON = routes
			previous.Revision++
			previous.UpdatedBy = "integration-test"
			previous.UpdatedAt = now
			if saveErr := db.Save(&previous).Error; saveErr != nil {
				t.Fatalf("configure integration AI policy %s: %v", operation, saveErr)
			}
			continue
		}
		policy := models.AutomationAIActionPolicy{
			ID: uuid.Must(uuid.NewV4()), Operation: operation,
			Provider: string(automationagent.ProviderMiniMax), Model: "MiniMax-M3",
			RoutesJSON: routes, Revision: 1, UpdatedBy: "integration-test", CreatedAt: now, UpdatedAt: now,
		}
		if createErr := db.Create(&policy).Error; createErr != nil {
			t.Fatalf("create integration AI policy %s: %v", operation, createErr)
		}
	}
	t.Cleanup(func() {
		for _, operation := range operations {
			_ = db.Unscoped().Where("operation = ?", operation).Delete(&models.AutomationAIActionPolicy{}).Error
			if previous := original[operation]; previous != nil {
				_ = db.Create(previous).Error
			}
		}
	})
}

func exerciseAutomaticPlanStepFanout(
	t *testing.T,
	ctx context.Context,
	runtime automationagent.AWSRuntime,
	cfg *models.Config,
	inputBucket, outputBucket string,
	queue *automationagent.AWSQueue,
	callback *automationagent.HTTPCallback,
	project models.DeliveryProject,
	organizationID uuid.UUID,
	requestedBy, workerID, agentKey, machineID, workspaceRoot, baseRevision string,
	gatewayProvider automationagent.ProviderClient,
	providerTransport *roundtripMiniMaxTransport,
) {
	t.Helper()
	db := configuration.DB
	require.NotNil(t, db)

	criteria := []string{
		"Alpha root validation passes",
		"Beta root validation passes",
		"Dependent validation passes",
	}
	steps := []map[string]any{
		{"step_key": "root-alpha", "role": "implementation", "order": 0, "title": "Alpha root", "objective": "Write the isolated alpha fixture", "acceptance_criteria": []string{criteria[0]}},
		{"step_key": "root-beta", "role": "implementation", "order": 1, "title": "Beta root", "objective": "Write the isolated beta fixture", "acceptance_criteria": []string{criteria[1]}},
		{"step_key": "dependent", "role": "implementation", "order": 2, "title": "Dependent child", "objective": "Consume both root patches", "acceptance_criteria": []string{criteria[2]}, "depends_on": []string{"root-alpha", "root-beta"}},
		{"step_key": "integration", "role": "integration", "order": 3, "title": "Integrate verified roots", "objective": "Verify the merged repository state", "acceptance_criteria": []string{criteria[0], criteria[1], criteria[2]}, "depends_on": []string{"dependent"}},
	}
	structuredPlan, err := json.Marshal(map[string]any{
		"summary":              "Integration fixture for automatic DAG child dispatch",
		"implementation_steps": []string{"Write two independent roots, then their dependent child"},
		"files_impacted":       []string{"alpha.txt", "beta.txt", "dependent.txt"},
		"acceptance_criteria":  criteria,
		"execution_steps":      steps,
		"repository_impact": []map[string]any{{
			"name": "Disposable fanout fixture", "reference": "workspace://fixture", "revision": baseRevision,
			"role": "primary", "impact": "changes", "notes": "Only bounded integration-test files are in scope.",
		}},
	})
	require.NoError(t, err)
	criteriaJSON, err := json.Marshal(criteria)
	require.NoError(t, err)

	acceptanceChecks := []map[string]any{{"criterion": "fixture validation", "command": []string{"go", "version"}}}
	for _, criterion := range criteria {
		acceptanceChecks = append(acceptanceChecks, map[string]any{"criterion": criterion, "command": []string{"go", "version"}})
	}
	workspaceRegistry, err := json.Marshal(map[string]any{"fixture": map[string]any{
		"path":                 workspaceRoot,
		"capabilities":         []string{automationagent.WorkspaceCapabilityReadRepository, automationagent.WorkspaceCapabilityCreateWorktree, automationagent.WorkspaceCapabilityApplyPatch},
		"sandbox_runtime":      automationagent.WorkspaceSandboxDocker,
		"sandbox_image":        "golang:1.25-bookworm",
		"sandbox_image_digest": "sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437",
		"sandbox_network":      "none",
		"require_sandbox":      true,
		"validation_commands":  [][]string{{"go", "version"}},
		"qa_commands":          [][]string{{"go", "version"}},
		"acceptance_checks":    acceptanceChecks,
	}})
	require.NoError(t, err)
	t.Setenv("ITBEM_AI_WORKSPACES_JSON", string(workspaceRegistry))
	registerRoundTripImplementationWorker(t, ctx, callback, workerID, agentKey, machineID)

	now := time.Now().UTC()
	item := models.DeliveryWorkItem{
		ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: requestedBy,
		Title: "Automatic plan-step fanout", Description: "Exercise root and dependent delivery workers end to end",
		ExpectedOutcome:   "The dependent child is published only after both roots complete",
		IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`, AcceptanceJSON: string(criteriaJSON),
		ClientContextJSON: `{}`, PlanJSON: string(structuredPlan), State: deliveryworkflow.StateImplementation,
		CreatedAt: now, UpdatedAt: now,
	}
	snapshot := models.DeliveryContextSnapshot{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, SourceID: uuid.Must(uuid.NewV4()),
		Kind: "repository", Name: "Local fixture", Reference: "workspace://fixture", Revision: baseRevision,
		MetadataJSON: `{"repository_role":"primary","repository_kind":"backend_api","repository_responsibility":"Disposable fanout integration fixture"}`,
		CapturedAt:   now, CreatedAt: now,
	}
	gate := models.DeliveryGate{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Kind: deliveryworkflow.GatePlan,
		Decision: deliveryworkflow.DecisionApproved, DecidedBy: requestedBy,
		Comment: "Integration-only approved DAG fixture", EvidenceChecklist: `[]`, DecidedAt: now, CreatedAt: now,
	}
	plan := models.DeliveryPlan{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Version: 1, Status: "approved",
		Summary: "Two independent roots and one dependent child", StructuredJSON: string(structuredPlan),
		ContextDigest: "worker-roundtrip-plan-fanout-v1", ProposedBy: "integration", ApprovedGateID: &gate.ID,
		CreatedAt: now,
	}
	t.Cleanup(func() {
		_ = db.Unscoped().Where("plan_id = ?", plan.ID).Delete(&models.DeliveryPlanStepDependencyPatch{}).Error
		_ = db.Unscoped().Where("plan_id = ?", plan.ID).Delete(&models.DeliveryPlanStepPatchArtifact{}).Error
		_ = db.Unscoped().Where("plan_id = ?", plan.ID).Delete(&models.DeliveryPlanStepActivityEvent{}).Error
		_ = db.Unscoped().Where("plan_id = ?", plan.ID).Delete(&models.DeliveryPlanStepEvent{}).Error
		_ = db.Unscoped().Where("plan_id = ?", plan.ID).Delete(&models.DeliveryPlanStepDependency{}).Error
		_ = db.Unscoped().Where("execution_id IN (SELECT id FROM delivery_plan_executions WHERE automation_task_id IN (SELECT id FROM automation_tasks WHERE delivery_work_item_id = ?))", item.ID).Delete(&models.DeliveryPlanStepAssignment{}).Error
		_ = db.Unscoped().Where("delivery_work_item_id = ?", item.ID).Delete(&models.AutomationToolExecution{}).Error
		_ = db.Unscoped().Where("automation_task_id IN (SELECT id FROM automation_tasks WHERE delivery_work_item_id = ?)", item.ID).Delete(&models.AutomationInferenceAttemptPolicy{}).Error
		_ = db.Unscoped().Where("delivery_work_item_id = ?", item.ID).Delete(&models.AutomationExecution{}).Error
		_ = db.Unscoped().Where("automation_task_id IN (SELECT id FROM automation_tasks WHERE delivery_work_item_id = ?)", item.ID).Delete(&models.DeliveryPlanExecution{}).Error
		_ = db.Unscoped().Where("plan_id = ?", plan.ID).Delete(&models.DeliveryPlanStep{}).Error
		_ = db.Unscoped().Where("work_item_id = ?", item.ID).Delete(&models.DeliveryContinuation{}).Error
		_ = db.Unscoped().Where("work_item_id = ?", item.ID).Delete(&models.DeliveryMessage{}).Error
		_ = db.Unscoped().Where("work_item_id = ?", item.ID).Delete(&models.DeliveryPlan{}).Error
		_ = db.Unscoped().Where("work_item_id = ?", item.ID).Delete(&models.DeliveryGate{}).Error
		_ = db.Unscoped().Where("correlation_id = ?", item.ID.String()).Delete(&models.OutboxEvent{}).Error
		_ = db.Unscoped().Where("delivery_work_item_id = ?", item.ID).Delete(&models.AutomationTask{}).Error
		_ = db.Unscoped().Where("work_item_id = ?", item.ID).Delete(&models.DeliveryContextSnapshot{}).Error
		_ = db.Unscoped().Where("id = ?", item.ID).Delete(&models.DeliveryWorkItem{}).Error
	})
	for _, value := range []any{&item, &snapshot, &gate, &plan} {
		require.NoError(t, db.Create(value).Error)
	}

	startBody := []byte(`{"phase":"implementation","instructions":"Execute only the approved steps and return after the runtime checks pass."}`)
	startEcho := echo.New()
	startRequest := httptest.NewRequest(http.MethodPost, "/api/automation/work-items/"+item.ID.String()+"/agent-runs", bytes.NewReader(startBody)).WithContext(ctx)
	startRequest.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	startRecorder := httptest.NewRecorder()
	startContext := startEcho.NewContext(startRequest, startRecorder)
	startContext.SetPath("/api/automation/work-items/:id/agent-runs")
	startContext.SetParamNames("id")
	startContext.SetParamValues(item.ID.String())
	startContext.Set("cognito_sub", requestedBy)
	startContext.Set("tenant_code", "itbem")
	setRoundTripOrganizationWorkspace(startContext, organizationID)
	// Reuse the exact config/runtime already wired to this unique test queue and
	// disposable LocalStack buckets; do not derive an endpoint from ambient AWS.
	startContext.Set("config", cfg)
	require.NoError(t, delivery.StartAgentRun(startContext))
	require.Equal(t, http.StatusAccepted, startRecorder.Code, startRecorder.Body.String())

	var execution models.DeliveryPlanExecution
	require.NoError(t, db.Where("plan_id = ?", plan.ID).First(&execution).Error)
	var parent models.AutomationTask
	require.NoError(t, db.First(&parent, execution.AutomationTaskID).Error)
	var planSteps []models.DeliveryPlanStep
	require.NoError(t, db.Where("plan_id = ?", plan.ID).Order("display_order ASC").Find(&planSteps).Error)
	require.Len(t, planSteps, 4)
	stepByKey := make(map[string]models.DeliveryPlanStep, len(planSteps))
	for _, step := range planSteps {
		stepByKey[step.StepKey] = step
	}
	alpha, beta, dependent, integration := stepByKey["root-alpha"], stepByKey["root-beta"], stepByKey["dependent"], stepByKey["integration"]
	require.NotEqual(t, uuid.Nil, alpha.ID)
	require.NotEqual(t, uuid.Nil, beta.ID)
	require.NotEqual(t, uuid.Nil, dependent.ID)
	require.NotEqual(t, uuid.Nil, integration.ID)
	childIDs := map[string]uuid.UUID{
		"root-alpha":  deliveryplansteps.ChildAutomationTaskID(execution.ID, alpha.ID),
		"root-beta":   deliveryplansteps.ChildAutomationTaskID(execution.ID, beta.ID),
		"dependent":   deliveryplansteps.ChildAutomationTaskID(execution.ID, dependent.ID),
		"integration": deliveryplansteps.ChildAutomationTaskID(execution.ID, integration.ID),
	}
	var rootAssignments []models.DeliveryPlanStepAssignment
	require.NoError(t, db.Where("execution_id = ?", execution.ID).Find(&rootAssignments).Error)
	require.Len(t, rootAssignments, 2, "only dependency-free roots may be assigned at initial dispatch")
	for _, assignment := range rootAssignments {
		require.Equal(t, machineID, assignment.TargetMachineID)
		require.Equal(t, agentKey, assignment.TargetAgentKey)
	}
	var dependentTask models.AutomationTask
	require.NoError(t, db.First(&dependentTask, "id = ?", childIDs["dependent"]).Error)
	require.Equal(t, "pending", dependentTask.Status, "all child rows are precreated, but a dependency-blocked child must stay inert")
	var dependentAssignments int64
	require.NoError(t, db.Model(&models.DeliveryPlanStepAssignment{}).Where("execution_id = ? AND delivery_plan_step_id = ?", execution.ID, dependent.ID).Count(&dependentAssignments).Error)
	require.Zero(t, dependentAssignments)
	require.Zero(t, countOutboxMessagesForPlanStep(t, db, item.ID.String(), dependent.ID.String()), "dependent must not be published at initial dispatch")
	require.EqualValues(t, 1, countOutboxMessagesForPlanStep(t, db, item.ID.String(), alpha.ID.String()))
	require.EqualValues(t, 1, countOutboxMessagesForPlanStep(t, db, item.ID.String(), beta.ID.String()))

	expectedRoots := map[uuid.UUID]string{childIDs["root-alpha"]: alpha.ID.String(), childIDs["root-beta"]: beta.ID.String()}
	rootMessages := receiveExpectedPlanStepMessages(t, ctx, queue, expectedRoots)
	require.Len(t, rootMessages, 2, "the unique SQS queue must receive both root assignments")

	provider := newPlanFanoutBarrierProvider()
	providerTransport.setProvider(provider)
	store := automationagent.NewAWSObjectStore(runtime.S3)
	workers := make(map[string]*automationagent.Worker, 2)
	for _, key := range []string{"root-alpha", "root-beta"} {
		worker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{
			InputBucket: inputBucket, OutputBucket: outputBucket, WorkerID: workerID,
			AgentKey: agentKey, MachineID: machineID, AllowedOperations: []string{"delivery.implementation"},
		}, store, callback, gatewayProvider)
		require.NoError(t, workerErr)
		workers[key] = worker
	}
	outcomes := make(chan planFanoutWorkerOutcome, 2)
	for key := range workers {
		key := key
		go func() {
			outcomes <- planFanoutWorkerOutcome{stepKey: key, err: automationagent.ProcessQueueMessage(ctx, workers[key], queue, rootMessages[childIDs[key]])}
		}()
	}
	waitForPlanFanoutProviderEntries(t, provider, outcomes, 2)
	require.GreaterOrEqual(t, provider.maxActive.Load(), int32(2), "both root workers must be inside inference concurrently")
	for _, key := range []string{"root-alpha", "root-beta"} {
		require.Equal(t, "running", mustLoadPlanFanoutTask(t, db, childIDs[key]).Status)
	}
	require.NoError(t, db.First(&dependentTask, "id = ?", childIDs["dependent"]).Error)
	require.Equal(t, "pending", dependentTask.Status, "the dependent must stay inert while both roots are still running")
	require.NoError(t, db.Model(&models.DeliveryPlanStepAssignment{}).Where("execution_id = ? AND delivery_plan_step_id = ?", execution.ID, dependent.ID).Count(&dependentAssignments).Error)
	require.Zero(t, dependentAssignments)
	require.Zero(t, countOutboxMessagesForPlanStep(t, db, item.ID.String(), dependent.ID.String()))

	// The actual gateway and local validation make each worker more expensive
	// than the old direct-provider fixture. Release both roots after proving they
	// overlap and that their dependent is still gated; this test is about DAG
	// dispatch, not holding a peer across a long lease-renewal interval.
	provider.release("root-alpha")
	provider.release("root-beta")
	completedRoots := make(map[string]bool, 2)
	rootWait := time.NewTimer(120 * time.Second)
	defer rootWait.Stop()
	for len(completedRoots) < 2 {
		select {
		case outcome := <-outcomes:
			require.NoError(t, outcome.err, "worker %s failed", outcome.stepKey)
			completedRoots[outcome.stepKey] = true
		case <-rootWait.C:
			states := make(map[string]map[string]string, 2)
			activity := make([]struct {
				AutomationTaskID uuid.UUID `gorm:"column:automation_task_id"`
				Action           string    `gorm:"column:action"`
				Phase            string    `gorm:"column:phase"`
				ToolName         string    `gorm:"column:tool_name"`
				Summary          string    `gorm:"column:summary"`
				Count            int64     `gorm:"column:count"`
			}, 0)
			for _, key := range []string{"root-alpha", "root-beta"} {
				task := mustLoadPlanFanoutTask(t, db, childIDs[key])
				states[key] = map[string]string{"status": task.Status, "progress": task.ProgressStep, "error": task.ErrorMessage}
			}
			diagnosticErr := db.Model(&models.DeliveryPlanStepActivityEvent{}).
				Select("automation_task_id, action, phase, tool_name, summary, count(*) AS count").
				Where("automation_task_id IN ?", []uuid.UUID{childIDs["root-alpha"], childIDs["root-beta"]}).
				Group("automation_task_id, action, phase, tool_name, summary").
				Order("automation_task_id, action, phase, tool_name, summary").Scan(&activity).Error
			t.Fatalf("timed out waiting for both independent roots to finish: provider_calls=%d max_active=%d task_states=%v activity_error=%v activity_summary=%v feedback_classes=%v",
				provider.calls.Load(), provider.maxActive.Load(), states, diagnosticErr, activity, provider.feedbackSnapshot())
		}
	}
	var completedAlpha models.AutomationTask
	require.NoError(t, db.First(&completedAlpha, "id = ?", childIDs["root-alpha"]).Error)
	require.Equal(t, "completed", completedAlpha.Status)
	require.Eventually(t, func() bool {
		var assignment models.DeliveryPlanStepAssignment
		if err := db.Where("execution_id = ? AND delivery_plan_step_id = ?", execution.ID, dependent.ID).Take(&assignment).Error; err != nil || assignment.Status != models.DeliveryPlanStepAssignmentQueued {
			return false
		}
		return countOutboxMessagesForPlanStep(t, db, item.ID.String(), dependent.ID.String()) == 1
	}, 20*time.Second, 100*time.Millisecond, "the second terminal worker callback must reserve and enqueue the dependent exactly once")
	require.EqualValues(t, 1, countOutboxMessagesForPlanStep(t, db, item.ID.String(), dependent.ID.String()))
	// A duplicate delivery for an already-terminal root must be acknowledged
	// without inference and must not enqueue its dependent a second time.
	alphaMessage, err := automationagent.DecodeTaskMessage(rootMessages[childIDs["root-alpha"]].Body)
	require.NoError(t, err)
	callsBeforeReplay := provider.calls.Load()
	require.NoError(t, workers["root-alpha"].Process(ctx, alphaMessage))
	require.Equal(t, callsBeforeReplay, provider.calls.Load(), "terminal replay must not invoke inference again")
	require.NoError(t, db.First(&dependentTask, "id = ?", childIDs["dependent"]).Error)
	require.Equal(t, "queued", dependentTask.Status, "root replay must not alter the already-dispatched dependent")
	require.NoError(t, db.Model(&models.DeliveryPlanStepAssignment{}).Where("execution_id = ? AND delivery_plan_step_id = ?", execution.ID, dependent.ID).Count(&dependentAssignments).Error)
	require.EqualValues(t, 1, dependentAssignments)
	require.EqualValues(t, 1, countOutboxMessagesForPlanStep(t, db, item.ID.String(), dependent.ID.String()))
	expectedDependent := map[uuid.UUID]string{childIDs["dependent"]: dependent.ID.String()}
	dependentMessages := receiveExpectedPlanStepMessages(t, ctx, queue, expectedDependent)
	require.Len(t, dependentMessages, 1, "the callback-triggered outbox must publish the dependent child")
	dependentMessage := dependentMessages[childIDs["dependent"]]
	dependentWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{
		InputBucket: inputBucket, OutputBucket: outputBucket, WorkerID: workerID,
		AgentKey: agentKey, MachineID: machineID, AllowedOperations: []string{"delivery.implementation"},
	}, store, callback, gatewayProvider)
	require.NoError(t, workerErr)
	require.NoError(t, automationagent.ProcessQueueMessage(ctx, dependentWorker, queue, dependentMessage))
	require.Equal(t, "completed", mustLoadPlanFanoutTask(t, db, childIDs["dependent"]).Status)

	expectedIntegration := map[uuid.UUID]string{childIDs["integration"]: integration.ID.String()}
	integrationMessages := receiveExpectedPlanStepMessages(t, ctx, queue, expectedIntegration)
	require.Len(t, integrationMessages, 1, "the dependency-ready integration step must be dispatched after its implementation dependency")
	providerTransport.setProvider(&roundtripProvider{
		content:    `{"action":"verify_integration"}`,
		responseID: "plan-fanout-integration",
	})
	integrationWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{
		InputBucket: inputBucket, OutputBucket: outputBucket, WorkerID: workerID,
		AgentKey: agentKey, MachineID: machineID, AllowedOperations: []string{"delivery.implementation"},
	}, store, callback, gatewayProvider)
	require.NoError(t, workerErr)
	require.NoError(t, automationagent.ProcessQueueMessage(ctx, integrationWorker, queue, integrationMessages[childIDs["integration"]]))
	require.Equal(t, "completed", mustLoadPlanFanoutTask(t, db, childIDs["integration"]).Status)
	require.Eventually(t, func() bool {
		var currentParent models.AutomationTask
		if err := db.First(&currentParent, parent.ID).Error; err != nil || currentParent.Status != "completed" {
			return false
		}
		var currentExecution models.DeliveryPlanExecution
		return db.First(&currentExecution, execution.ID).Error == nil && currentExecution.Status == models.DeliveryPlanExecutionCompleted
	}, 5*time.Second, 100*time.Millisecond, "the explicit integration step must complete the parent after verified fan-in")
}

func countOutboxMessagesForPlanStep(t *testing.T, db *gorm.DB, correlationID, stepID string) int64 {
	t.Helper()
	var events []models.OutboxEvent
	require.NoError(t, db.Where("correlation_id = ?", correlationID).Find(&events).Error)
	var count int64
	for _, event := range events {
		var message automationqueue.Message
		if err := json.Unmarshal([]byte(event.Payload), &message); err != nil {
			continue
		}
		if message.Payload.PlanStepID == stepID {
			count++
		}
	}
	return count
}

func receiveExpectedPlanStepMessages(t *testing.T, ctx context.Context, queue *automationagent.AWSQueue, expected map[uuid.UUID]string) map[uuid.UUID]automationagent.QueueMessage {
	t.Helper()
	received := make(map[uuid.UUID]automationagent.QueueMessage, len(expected))
	deadlineCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for len(received) < len(expected) && deadlineCtx.Err() == nil {
		batch, err := receiveIntegrationMessages(deadlineCtx, queue, len(expected)-len(received))
		require.NoError(t, err)
		for _, raw := range batch {
			message, decodeErr := automationagent.DecodeTaskMessage(raw.Body)
			require.NoError(t, decodeErr)
			taskID, parseErr := uuid.FromString(message.Payload.TaskID)
			require.NoError(t, parseErr)
			stepID, wanted := expected[taskID]
			if wanted && message.Payload.PlanStepID == stepID {
				received[taskID] = raw
				continue
			}
			require.NoError(t, queue.Defer(deadlineCtx, raw, 1), "unrelated message is returned to the isolated test queue")
		}
	}
	require.Len(t, received, len(expected), "timed out waiting for the exact plan-step queue messages")
	return received
}

type planFanoutWorkerOutcome struct {
	stepKey string
	err     error
}

type planFanoutBarrierProvider struct {
	entered     chan string
	releases    map[string]chan struct{}
	releaseOnce map[string]*sync.Once
	mu          sync.Mutex
	feedback    map[string]string
	calls       atomic.Int32
	active      atomic.Int32
	maxActive   atomic.Int32
}

func newPlanFanoutBarrierProvider() *planFanoutBarrierProvider {
	return &planFanoutBarrierProvider{
		entered: make(chan string, 3),
		releases: map[string]chan struct{}{
			"root-alpha": make(chan struct{}),
			"root-beta":  make(chan struct{}),
		},
		releaseOnce: map[string]*sync.Once{
			"root-alpha": {},
			"root-beta":  {},
		},
		feedback: make(map[string]string),
	}
}

func (provider *planFanoutBarrierProvider) Complete(ctx context.Context, messages []automationagent.Message, _ int) (automationagent.Completion, error) {
	provider.calls.Add(1)
	stepKey := ""
	const stepPrefix = "Current claimed delivery plan step (JSON data): "
	for _, message := range messages {
		if !strings.HasPrefix(message.Content, stepPrefix) {
			continue
		}
		var step struct {
			StepKey string `json:"step_key"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(message.Content, stepPrefix)), &step); err != nil {
			return automationagent.Completion{}, fmt.Errorf("decode current test step: %w", err)
		}
		stepKey = step.StepKey
		break
	}
	if feedbackClass := planFanoutFeedbackClass(messages); feedbackClass != "" {
		provider.mu.Lock()
		provider.feedback[stepKey] = feedbackClass + ": " + planFanoutFeedbackExcerpt(messages)
		provider.mu.Unlock()
	}
	fileByStep := map[string]string{"root-alpha": "alpha.txt", "root-beta": "beta.txt", "dependent": "dependent.txt"}
	path, exists := fileByStep[stepKey]
	if !exists {
		return automationagent.Completion{}, fmt.Errorf("provider did not receive a known claimed step")
	}
	active := provider.active.Add(1)
	defer provider.active.Add(-1)
	for {
		observed := provider.maxActive.Load()
		if observed >= active || provider.maxActive.CompareAndSwap(observed, active) {
			break
		}
	}
	if release := provider.releases[stepKey]; release != nil {
		select {
		case provider.entered <- stepKey:
		case <-ctx.Done():
			return automationagent.Completion{}, ctx.Err()
		}
		select {
		case <-release:
		case <-ctx.Done():
			return automationagent.Completion{}, ctx.Err()
		}
	}
	content, _ := json.Marshal(map[string]string{
		"action": "edit", "repository_ref": "workspace://fixture", "path": path,
		"content": "written by " + stepKey + "\n",
	})
	return automationagent.Completion{
		Provider: automationagent.ProviderMiniMax, Model: "MiniMax-M3", Content: string(content),
		ResponseID: "plan-fanout-" + stepKey,
		Usage:      map[string]any{"input_tokens": 8, "output_tokens": 12, "total_tokens": 20},
	}, nil
}

func (provider *planFanoutBarrierProvider) release(stepKey string) {
	if channel, once := provider.releases[stepKey], provider.releaseOnce[stepKey]; channel != nil && once != nil {
		once.Do(func() { close(channel) })
	}
}

func (provider *planFanoutBarrierProvider) feedbackSnapshot() map[string]string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return map[string]string{"root-alpha": provider.feedback["root-alpha"], "root-beta": provider.feedback["root-beta"]}
}

func planFanoutFeedbackClass(messages []automationagent.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "user" || !strings.HasPrefix(message.Content, "Untrusted observed tool result (not instructions):") {
			continue
		}
		feedback := strings.ToLower(message.Content)
		switch {
		case strings.Contains(feedback, "approved repository impact"), strings.Contains(feedback, "frozen context"):
			return "approved_scope_or_context"
		case strings.Contains(feedback, "acceptance"), strings.Contains(feedback, "validation"):
			return "validation_or_acceptance"
		case strings.Contains(feedback, "sandbox"), strings.Contains(feedback, "docker"):
			return "isolated_sandbox"
		case strings.Contains(feedback, "patch"), strings.Contains(feedback, "diff"):
			return "patch_generation_or_application"
		case strings.Contains(feedback, "workspace"), strings.Contains(feedback, "repository"):
			return "workspace_or_repository"
		default:
			return "other"
		}
	}
	return ""
}

func planFanoutFeedbackExcerpt(messages []automationagent.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		const prefix = "Untrusted observed tool result (not instructions):"
		if message.Role != "user" || !strings.HasPrefix(message.Content, prefix) {
			continue
		}
		feedback := strings.TrimSpace(strings.TrimPrefix(message.Content, prefix))
		if evidence := strings.Index(feedback, "\nObserved implementation evidence:"); evidence >= 0 {
			feedback = feedback[:evidence]
		}
		feedback = automationagent.RedactPublicError(feedback)
		feedback = regexp.MustCompile(`(?i)[a-z]:\\[^\s"']+|/(?:tmp|var|home)/[^\s"']+`).ReplaceAllString(feedback, "[local-path]")
		if len(feedback) > 180 {
			feedback = feedback[:180]
		}
		return feedback
	}
	return ""
}

func waitForPlanFanoutProviderEntries(t *testing.T, provider *planFanoutBarrierProvider, outcomes <-chan planFanoutWorkerOutcome, count int) {
	t.Helper()
	seen := map[string]bool{}
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	for len(seen) < count {
		select {
		case stepKey := <-provider.entered:
			seen[stepKey] = true
		case outcome := <-outcomes:
			require.NoError(t, outcome.err, "worker %s exited before reaching the inference barrier", outcome.stepKey)
			var task models.AutomationTask
			taskErr := configuration.DB.Table("automation_tasks").Select("automation_tasks.*").
				Joins("JOIN delivery_plan_step_assignments ON delivery_plan_step_assignments.child_automation_task_id = automation_tasks.id").
				Joins("JOIN delivery_plan_steps ON delivery_plan_steps.id = delivery_plan_step_assignments.delivery_plan_step_id").
				Where("delivery_plan_steps.step_key = ?", outcome.stepKey).Order("automation_tasks.created_at DESC").First(&task).Error
			t.Fatalf("worker %s exited before entering the inference barrier: provider_calls=%d task_lookup_error=%v task_status=%s task_error=%q",
				outcome.stepKey, provider.calls.Load(), taskErr, task.Status, task.ErrorMessage)
		case <-timeout.C:
			t.Fatalf("only %d/%d independent workers reached inference; entered=%v", len(seen), count, seen)
		}
	}
}

func waitForPlanFanoutWorker(t *testing.T, outcomes <-chan planFanoutWorkerOutcome, wanted string) {
	t.Helper()
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case outcome := <-outcomes:
			require.NoError(t, outcome.err, "worker %s failed", outcome.stepKey)
			if outcome.stepKey == wanted {
				return
			}
			t.Fatalf("unexpected worker %s completed while waiting for %s", outcome.stepKey, wanted)
		case <-timeout.C:
			t.Fatalf("timed out waiting for worker %s terminal callback", wanted)
		}
	}
}

func mustLoadPlanFanoutTask(t *testing.T, db *gorm.DB, taskID uuid.UUID) models.AutomationTask {
	t.Helper()
	var task models.AutomationTask
	require.NoError(t, db.First(&task, "id = ?", taskID).Error)
	return task
}

func registerRoundTripImplementationWorker(t *testing.T, ctx context.Context, callback *automationagent.HTTPCallback, workerID, agentKey, machineID string) {
	t.Helper()
	readiness, err := automationagent.WorkspaceReadinessSnapshot(os.Getenv)
	require.NoError(t, err)
	heartbeat := automationagent.AgentHeartbeat{
		WorkerID: workerID, AgentKey: agentKey, MachineID: machineID,
		Provider: "minimax", Model: "MiniMax-M3", Concurrency: 2,
		Capabilities: []string{"delivery.implementation"}, StartedAt: time.Now().UTC().Format(time.RFC3339),
		WorkspaceReadiness: readiness,
	}
	require.NoError(t, callback.Heartbeat(ctx, heartbeat))
	// The production agent reports every 30 seconds while its queue workers run.
	// Keep this fixture's worker lease live across long real-worker executions as
	// well, otherwise assignment renewals correctly reject a stale heartbeat.
	heartbeatCtx, stopHeartbeats := context.WithCancel(ctx)
	t.Cleanup(stopHeartbeats)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				requestCtx, cancel := context.WithTimeout(heartbeatCtx, 10*time.Second)
				heartbeatErr := callback.Heartbeat(requestCtx, heartbeat)
				cancel()
				if heartbeatErr != nil && heartbeatCtx.Err() == nil && !t.Failed() {
					t.Errorf("integration worker heartbeat failed")
				}
			}
		}
	}()
}

type roundtripProvider struct {
	content    string
	responseID string
	calls      int
}

func (p *roundtripProvider) Complete(context.Context, []automationagent.Message, int) (automationagent.Completion, error) {
	p.calls++
	return automationagent.Completion{Provider: automationagent.ProviderMiniMax, Model: "MiniMax-M3", Content: p.content, ResponseID: p.responseID, Usage: map[string]any{"input_tokens": 8, "output_tokens": 12, "total_tokens": 20}}, nil
}

const roundtripSyntheticProviderAPIKey = "integration-only-provider-key"

type roundtripResponseWriter struct {
	http.ResponseWriter
	body bytes.Buffer
}

func (writer *roundtripResponseWriter) Write(body []byte) (int, error) {
	_, _ = writer.body.Write(body)
	return writer.ResponseWriter.Write(body)
}

type roundtripCredentialResolver struct{}

func (roundtripCredentialResolver) APIKey(_ context.Context, provider string) (string, error) {
	if provider != string(automationagent.ProviderMiniMax) {
		return "", fmt.Errorf("unexpected test provider")
	}
	return roundtripSyntheticProviderAPIKey, nil
}

func (resolver roundtripCredentialResolver) APIKeyForProject(ctx context.Context, _ string, provider string) (string, error) {
	return resolver.APIKey(ctx, provider)
}

func (roundtripCredentialResolver) ReplaceAPIKey(context.Context, string, string) error { return nil }

// roundtripMiniMaxTransport implements only the three read/inference endpoints
// needed by the production MiniMax adapter. Any attempt to reach the internet,
// a different host, or an unexpected path fails closed.
type roundtripMiniMaxTransport struct {
	mu       sync.RWMutex
	provider automationagent.ProviderClient
}

func (transport *roundtripMiniMaxTransport) setProvider(provider automationagent.ProviderClient) {
	transport.mu.Lock()
	transport.provider = provider
	transport.mu.Unlock()
}

func (transport *roundtripMiniMaxTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.Method + " " + request.URL.Host + request.URL.Path {
	case "GET models.dev/api.json":
		return roundtripHTTPResponse(request, http.StatusOK, roundtripModelsDevMiniMaxCatalog), nil
	case "GET www.minimax.io/v1/token_plan/remains":
		if request.Header.Get("Authorization") != "Bearer "+roundtripSyntheticProviderAPIKey {
			return nil, fmt.Errorf("synthetic MiniMax credential was not used")
		}
		return roundtripHTTPResponse(request, http.StatusOK, `{"model_remains":[{"model_name":"MiniMax-M3"}],"base_resp":{"status_code":0}}`), nil
	case "POST api.minimax.io/v1/chat/completions":
		if request.Header.Get("Authorization") != "Bearer "+roundtripSyntheticProviderAPIKey {
			return nil, fmt.Errorf("synthetic MiniMax credential was not used")
		}
		var payload struct {
			Model               string                    `json:"model"`
			Messages            []automationagent.Message `json:"messages"`
			MaxCompletionTokens int                       `json:"max_completion_tokens"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.Model != "MiniMax-M3" || len(payload.Messages) == 0 {
			return nil, fmt.Errorf("synthetic MiniMax request was invalid")
		}
		transport.mu.RLock()
		provider := transport.provider
		transport.mu.RUnlock()
		if provider == nil {
			return nil, fmt.Errorf("synthetic MiniMax completion provider is not configured")
		}
		completion, err := provider.Complete(request.Context(), payload.Messages, payload.MaxCompletionTokens)
		if err != nil {
			return nil, err
		}
		body, err := json.Marshal(map[string]any{
			"id": completion.ResponseID, "model": completion.Model, "usage": completion.Usage,
			"base_resp": map[string]any{"status_code": 0},
			"choices":   []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": completion.Content}}},
		})
		if err != nil {
			return nil, fmt.Errorf("synthetic MiniMax response was invalid")
		}
		return roundtripHTTPResponse(request, http.StatusOK, string(body)), nil
	default:
		return nil, fmt.Errorf("integration test blocked unexpected provider request")
	}
}

const roundtripModelsDevMiniMaxCatalog = `{"minimax":{"models":{"MiniMax-M3":{"id":"MiniMax-M3","name":"MiniMax M3","modalities":{"input":["text"],"output":["text"]},"limit":{"context":1000000,"output":8192}}}}}`

func roundtripHTTPResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(body)), Request: request,
	}
}

type roundtripGatewayTransport struct {
	inferenceURL   string
	callbackClient *http.Client
}

func (transport roundtripGatewayTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodGet && request.URL.String() == "https://models.dev/api.json" {
		return roundtripHTTPResponse(request, http.StatusOK, roundtripModelsDevMiniMaxCatalog), nil
	}
	if request.Method == http.MethodPost && request.URL.String() == transport.inferenceURL && transport.callbackClient != nil && transport.callbackClient.Transport != nil {
		return transport.callbackClient.Transport.RoundTrip(request)
	}
	return nil, fmt.Errorf("integration test blocked unexpected worker network request")
}

type parallelPlanProvider struct {
	content   string
	entered   chan struct{}
	release   chan struct{}
	active    atomic.Int32
	maxActive atomic.Int32
	calls     atomic.Int32
}

func (p *parallelPlanProvider) Complete(ctx context.Context, _ []automationagent.Message, _ int) (automationagent.Completion, error) {
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for {
		observed := p.maxActive.Load()
		if observed >= active || p.maxActive.CompareAndSwap(observed, active) {
			break
		}
	}
	select {
	case p.entered <- struct{}{}:
	case <-ctx.Done():
		return automationagent.Completion{}, ctx.Err()
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return automationagent.Completion{}, ctx.Err()
	}
	call := p.calls.Add(1)
	return automationagent.Completion{
		Provider:   automationagent.ProviderMiniMax,
		Model:      "MiniMax-M3",
		Content:    p.content,
		ResponseID: fmt.Sprintf("parallel-plan-%d", call),
		Usage:      map[string]any{"input_tokens": 8, "output_tokens": 12, "total_tokens": 20},
	}, nil
}

type partialFailureProvider struct {
	content    string
	failMarker string
	entered    chan struct{}
	release    chan struct{}
	active     atomic.Int32
	maxActive  atomic.Int32
	calls      atomic.Int32
}

func (p *partialFailureProvider) Complete(ctx context.Context, messages []automationagent.Message, _ int) (automationagent.Completion, error) {
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for {
		observed := p.maxActive.Load()
		if observed >= active || p.maxActive.CompareAndSwap(observed, active) {
			break
		}
	}
	select {
	case p.entered <- struct{}{}:
	case <-ctx.Done():
		return automationagent.Completion{}, ctx.Err()
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return automationagent.Completion{}, ctx.Err()
	}
	call := p.calls.Add(1)
	joined := strings.Builder{}
	for _, message := range messages {
		joined.WriteString(message.Content)
	}
	if strings.Contains(joined.String(), p.failMarker) {
		return automationagent.Completion{
			Provider:   automationagent.ProviderMiniMax,
			Model:      "MiniMax-M3",
			Content:    "{malformed provider response",
			ResponseID: fmt.Sprintf("partial-failure-%d", call),
			Usage:      map[string]any{"input_tokens": 8, "output_tokens": 5, "total_tokens": 13},
		}, nil
	}
	return automationagent.Completion{
		Provider:   automationagent.ProviderMiniMax,
		Model:      "MiniMax-M3",
		Content:    p.content,
		ResponseID: fmt.Sprintf("partial-success-%d", call),
		Usage:      map[string]any{"input_tokens": 8, "output_tokens": 12, "total_tokens": 20},
	}, nil
}

type roundtripImplementationProvider struct{}

func (*roundtripImplementationProvider) Complete(context.Context, []automationagent.Message, int) (automationagent.Completion, error) {
	return automationagent.Completion{
		Provider:   automationagent.ProviderMiniMax,
		Model:      "MiniMax-M3",
		Content:    `{"action":"edit","repository_ref":"workspace://fixture","path":"README.md","content":"local fixture\nupdated by the bounded agent\n"}`,
		ResponseID: "roundtrip-implementation",
		Usage:      map[string]any{"input_tokens": 10, "output_tokens": 16, "total_tokens": 26},
	}, nil
}
