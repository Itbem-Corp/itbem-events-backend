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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"events-stocks/configuration"
	automationCtrl "events-stocks/controllers/automation"
	delivery "events-stocks/controllers/delivery"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	automationqueue "events-stocks/repositories/automationqueuerepository"
	"events-stocks/services/deliveryworkflow"
	"events-stocks/services/outbox"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
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
	const callbackSecret = "integration-control-plane-callback-secret"
	t.Setenv("AUTOMATION_CALLBACK_SECRET", callbackSecret)

	endpoint := strings.TrimSpace(os.Getenv("ITBEM_LOCALSTACK_ENDPOINT"))
	if endpoint == "" {
		endpoint = "http://localhost:4566"
	}
	// LocalStack plus the durable outbox can legitimately take a few long-poll
	// turns when the developer machine is under load. Keep the integration
	// budget bounded, but do not let the implementation phase fail merely
	// because its queue visibility window is shorter than the dispatcher lag.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	const inputBucket = "itbem-ai-inputs-local"
	const outputBucket = "itbem-ai-outputs-local"
	workspaceRoot := t.TempDir()
	require.NoError(t, os.WriteFile(workspaceRoot+string(os.PathSeparator)+"README.md", []byte("local fixture\n"), 0o644))
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "integration@example.test"}, {"config", "user.name", "Integration"}, {"add", "README.md"}, {"commit", "-qm", "fixture"}} {
		command := exec.Command("git", append([]string{"-C", workspaceRoot}, args...)...)
		require.NoError(t, command.Run(), "git %v", args)
	}
	revisionBytes, err := exec.Command("git", "-C", workspaceRoot, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	fixtureRevision := strings.TrimSpace(string(revisionBytes))
	workspaceRegistry, err := json.Marshal(map[string]any{"fixture": map[string]any{
		"path":                workspaceRoot,
		"capabilities":        []string{automationagent.WorkspaceCapabilityReadRepository, automationagent.WorkspaceCapabilityCreateWorktree, automationagent.WorkspaceCapabilityApplyPatch},
		"validation_commands": [][]string{{"go", "version"}},
		"qa_commands":         [][]string{{"go", "version"}},
		"acceptance_checks":   []map[string]any{{"criterion": "fixture validation", "command": []string{"go", "version"}}},
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
		AwsRegion:                "us-east-1",
		S3Region:                 "us-east-1",
		S3ClientId:               "test",
		S3ClientSecret:           "test",
		AwsBucketName:            inputBucket,
		S3Endpoint:               endpoint,
		S3UsePathStyle:           "true",
		AutomationInputBucket:    inputBucket,
		AutomationOutputBucket:   outputBucket,
		AutomationCallbackSecret: callbackSecret,
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
	callbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := echo.New()
		c := e.NewContext(r, w)
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) == 0 {
			http.Error(w, "missing task", http.StatusNotFound)
			return
		}
		c.SetPath("/api/internal/automation/tasks/:id")
		c.SetParamNames("id")
		c.SetParamValues(parts[len(parts)-1])
		c.Set("config", cfg)
		if err := automationCtrl.Complete(c); err != nil {
			e.HTTPErrorHandler(err, c)
		}
	}))
	defer callbackServer.Close()
	callback, err := automationagent.NewHTTPCallback(callbackServer.URL, callbackSecret, callbackServer.Client())
	require.NoError(t, err)

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

	fake := &roundtripProvider{content: fmt.Sprintf(`{"summary":"Bounded local plan","goal_interpretation":"Validate the local control-plane handoff","autonomy_boundary":"Stop at the human plan gate.","confidence":0.95,"context_reviewed":["workspace://fixture"],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":["update the fixture README"],"risks":[],"qa_plan":["go version"],"evidence_plan":["durable callback and private result"],"acceptance_criteria":["fixture validation"],"files_impacted":["README.md"],"rollback_plan":["discard the local plan"],"questions":[],"estimate":"1 minute","repository_impact":[{"name":"Local fixture","reference":"workspace://fixture","revision":"%s","role":"primary","impact":"changes","notes":"Only README.md is in scope."}]}`, fixtureRevision), responseID: "roundtrip-plan"}
	worker, err := automationagent.NewWorker(automationagent.WorkerConfig{InputBucket: inputBucket, OutputBucket: outputBucket, AllowedOperations: []string{"delivery.plan"}}, automationagent.NewAWSObjectStore(runtime.S3), callback, fake)
	require.NoError(t, err)
	require.NoError(t, automationagent.ProcessQueueMessage(ctx, worker, queue, messages[0]))

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
	parallelRunCtx, cancelParallelRuns := context.WithCancel(ctx)
	parallelErrors := make(chan error, 2)
	for index := 0; index < 2; index++ {
		parallelWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{
			InputBucket: inputBucket, OutputBucket: outputBucket, AllowedOperations: []string{"delivery.plan"},
		}, automationagent.NewAWSObjectStore(runtime.S3), callback, parallelProvider)
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
	partialRunCtx, cancelPartialRuns := context.WithCancel(ctx)
	partialErrors := make(chan error, 2)
	for index := 0; index < 2; index++ {
		partialWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{
			InputBucket: inputBucket, OutputBucket: outputBucket, AllowedOperations: []string{"delivery.plan"},
		}, automationagent.NewAWSObjectStore(runtime.S3), callback, partialProvider)
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
	implementationContext.Set("config", cfg)
	require.NoError(t, delivery.StartAgentRun(implementationContext))
	require.Equal(t, http.StatusAccepted, implementationRecorder.Code, implementationRecorder.Body.String())
	var implementationTask models.AutomationTask
	require.NoError(t, db.Where("delivery_work_item_id = ? AND operation = ?", item.ID, "delivery.implementation").First(&implementationTask).Error)
	var implementationMessages []automationagent.QueueMessage
	implementationReceiveCtx, implementationReceiveCancel := context.WithTimeout(ctx, 35*time.Second)
	defer implementationReceiveCancel()
	implementationMessages, err = receiveIntegrationMessages(implementationReceiveCtx, queue, 1)
	require.NoError(t, err)
	require.Len(t, implementationMessages, 1)
	var implementationQueued automationagent.TaskMessage
	require.NoError(t, json.Unmarshal([]byte(implementationMessages[0].Body), &implementationQueued))
	require.Equal(t, implementationTask.ID.String(), implementationQueued.Payload.TaskID)
	implementationWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{InputBucket: inputBucket, OutputBucket: outputBucket, AllowedOperations: []string{"delivery.implementation"}}, automationagent.NewAWSObjectStore(runtime.S3), callback, &roundtripImplementationProvider{})
	require.NoError(t, workerErr)
	require.NoError(t, automationagent.ProcessQueueMessage(ctx, implementationWorker, queue, implementationMessages[0]))
	var completedImplementation models.AutomationTask
	require.NoError(t, db.First(&completedImplementation, implementationTask.ID).Error)
	require.Equalf(t, "completed", completedImplementation.Status, "implementation callback error=%q", completedImplementation.ErrorMessage)
	var changeSet models.DeliveryChangeSet
	require.NoError(t, db.Where("work_item_id = ?", item.ID).Order("created_at DESC").First(&changeSet).Error)
	require.Equal(t, "workspace://fixture", changeSet.RepositoryRef)
	require.Equal(t, "local_worktree", changeSet.ReviewType)
	require.Equal(t, "passed", changeSet.CIStatus)
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
	qaPlanJSON := fmt.Sprintf(`{"repository_impact":[{"name":"Local fixture","reference":"workspace://fixture","revision":"%s","role":"primary","impact":"changes","notes":"README fixture"}],"qa_execution_matrix":[{"repository_ref":"workspace://fixture","run_validation":false,"run_qa":true,"run_stagehand":false,"collect_evidence":false}]}`, fixtureRevision)
	qaItem := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject, Title: "Local QA roundtrip", Description: "Run bounded QA against the reviewed worktree", ExpectedOutcome: "A durable QA report", State: deliveryworkflow.StateQARunning, IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`, AcceptanceJSON: `["fixture validation"]`, ClientContextJSON: `{}`, PlanJSON: qaPlanJSON, PreviewURL: previewServer.URL}
	qaSnapshot := models.DeliveryContextSnapshot{ID: uuid.Must(uuid.NewV4()), WorkItemID: qaItem.ID, SourceID: uuid.Must(uuid.NewV4()), Kind: "repository", Name: "Local fixture", Reference: "workspace://fixture", Revision: fixtureRevision, MetadataJSON: `{"repository_role":"primary","repository_kind":"backend_api","repository_responsibility":"Local QA fixture"}`, CapturedAt: time.Now().UTC()}
	qaChangeSet := models.DeliveryChangeSet{ID: uuid.Must(uuid.NewV4()), WorkItemID: qaItem.ID, RepositoryRef: "workspace://fixture", Branch: changeSet.Branch, ReviewType: "local_worktree", CIStatus: "passed", PreviewURL: previewServer.URL, Environment: "local", MetadataJSON: `{}`, CreatedBy: subject}
	for _, value := range []any{&qaItem, &qaSnapshot, &qaChangeSet} {
		require.NoError(t, db.Create(value).Error)
	}
	qaCleanupIDs := []uuid.UUID{qaItem.ID}
	t.Cleanup(func() {
		_ = db.Unscoped().Where("delivery_work_item_id IN ?", qaCleanupIDs).Delete(&models.AutomationExecution{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", qaCleanupIDs).Delete(&models.DeliveryEvidence{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", qaCleanupIDs).Delete(&models.DeliveryGate{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", qaCleanupIDs).Delete(&models.DeliveryContinuation{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", qaCleanupIDs).Delete(&models.DeliveryChangeSet{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", qaCleanupIDs).Delete(&models.DeliveryPlan{}).Error
		_ = db.Unscoped().Where("delivery_work_item_id IN ?", qaCleanupIDs).Delete(&models.AutomationTask{}).Error
		for _, id := range qaCleanupIDs {
			_ = db.Unscoped().Where("correlation_id = ?", id.String()).Delete(&models.OutboxEvent{}).Error
		}
		_ = db.Unscoped().Where("work_item_id IN ?", qaCleanupIDs).Delete(&models.DeliveryContextSnapshot{}).Error
		_ = db.Unscoped().Where("id IN ?", qaCleanupIDs).Delete(&models.DeliveryWorkItem{}).Error
	})

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
	qaContext.Set("config", cfg)
	require.NoError(t, delivery.StartAgentRun(qaContext))
	require.Equal(t, http.StatusAccepted, qaRecorder.Code, qaRecorder.Body.String())
	var qaTask models.AutomationTask
	require.NoError(t, db.Where("delivery_work_item_id = ? AND operation = ?", qaItem.ID, "delivery.qa").First(&qaTask).Error)
	qaMessage := receiveTaskMessage(qaTask.ID)
	qaProvider := &roundtripProvider{content: `{"summary":"QA observed","verdict":"passed","checks":[{"name":"go version","status":"passed","detail":"The approved QA command exited successfully."}],"defects":[],"coverage_gaps":[],"recommended_actions":[]}`, responseID: "roundtrip-qa"}
	qaWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{InputBucket: inputBucket, OutputBucket: outputBucket, AllowedOperations: []string{"delivery.qa"}}, automationagent.NewAWSObjectStore(runtime.S3), callback, qaProvider)
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
	continuationSummaryProvider := &roundtripProvider{content: fmt.Sprintf(`{"executive":{"what_changed":"Bounded local delivery","why":"Prepare a grounded handoff","how_to_test":"go version and observed preview","risks":["No remote publication was attempted"]},"technical":{"decisions":["%s %s"],"evidence":["%s — %s"]}}`, qaGate.Kind, qaGate.Decision, qaEvidence.ID.String(), qaEvidence.Title), responseID: "roundtrip-continuation-summary"}
	continuationSummaryWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{InputBucket: inputBucket, OutputBucket: outputBucket, AllowedOperations: []string{"delivery.summary"}}, automationagent.NewAWSObjectStore(runtime.S3), callback, continuationSummaryProvider)
	require.NoError(t, workerErr)
	require.NoError(t, automationagent.ProcessQueueMessage(ctx, continuationSummaryWorker, queue, continuationSummaryMessage))
	var completedContinuationSummaryTask models.AutomationTask
	require.NoError(t, db.First(&completedContinuationSummaryTask, continuationSummaryTask.ID).Error)
	require.Equal(t, "completed", completedContinuationSummaryTask.Status)
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
	summarySnapshot := models.DeliveryContextSnapshot{ID: uuid.Must(uuid.NewV4()), WorkItemID: summaryItem.ID, SourceID: uuid.Must(uuid.NewV4()), Kind: "repository", Name: "Local fixture", Reference: "workspace://fixture", Revision: fixtureRevision, MetadataJSON: `{"repository_role":"primary","repository_kind":"backend_api","repository_responsibility":"Local summary fixture"}`, CapturedAt: time.Now().UTC()}
	evidenceID := uuid.Must(uuid.NewV4())
	capturedAt := time.Now().UTC()
	summaryEvidence := models.DeliveryEvidence{ID: evidenceID, WorkItemID: summaryItem.ID, Kind: "report", Phase: "qa", Title: "QA evidence", Reference: "s3://" + outputBucket + "/qa-evidence.json", CapturedBy: "integration", CapturedAt: &capturedAt, MetadataJSON: `{}`}
	summaryGate := models.DeliveryGate{ID: uuid.Must(uuid.NewV4()), WorkItemID: summaryItem.ID, Kind: "qa_review", Decision: "approved", DecidedBy: subject, Comment: "QA evidence approved for release review", EvidenceChecklist: `["preview observed","go version passed"]`, DecidedAt: capturedAt}
	for _, value := range []any{&summaryItem, &summarySnapshot, &summaryEvidence, &summaryGate} {
		require.NoError(t, db.Create(value).Error)
	}
	summaryCleanupIDs := []uuid.UUID{summaryItem.ID}
	t.Cleanup(func() {
		_ = db.Unscoped().Where("delivery_work_item_id IN ?", summaryCleanupIDs).Delete(&models.AutomationExecution{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", summaryCleanupIDs).Delete(&models.DeliveryEvidence{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", summaryCleanupIDs).Delete(&models.DeliveryGate{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", summaryCleanupIDs).Delete(&models.DeliveryContinuation{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", summaryCleanupIDs).Delete(&models.DeliveryChangeSet{}).Error
		_ = db.Unscoped().Where("work_item_id IN ?", summaryCleanupIDs).Delete(&models.DeliveryPlan{}).Error
		_ = db.Unscoped().Where("delivery_work_item_id IN ?", summaryCleanupIDs).Delete(&models.AutomationTask{}).Error
		for _, id := range summaryCleanupIDs {
			_ = db.Unscoped().Where("correlation_id = ?", id.String()).Delete(&models.OutboxEvent{}).Error
		}
		_ = db.Unscoped().Where("work_item_id IN ?", summaryCleanupIDs).Delete(&models.DeliveryContextSnapshot{}).Error
		_ = db.Unscoped().Where("id IN ?", summaryCleanupIDs).Delete(&models.DeliveryWorkItem{}).Error
	})
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
	summaryContext.Set("config", cfg)
	require.NoError(t, delivery.StartAgentRun(summaryContext))
	require.Equal(t, http.StatusAccepted, summaryRecorder.Code, summaryRecorder.Body.String())
	var summaryTask models.AutomationTask
	require.NoError(t, db.Where("delivery_work_item_id = ? AND operation = ?", summaryItem.ID, "delivery.summary").First(&summaryTask).Error)
	summaryMessage := receiveTaskMessage(summaryTask.ID)
	summaryProvider := &roundtripProvider{content: fmt.Sprintf(`{"executive":{"what_changed":"Bounded local delivery","why":"Validate the durable handoff","how_to_test":"go version and observed preview","risks":["No remote publication was attempted"]},"technical":{"decisions":["qa_review approved"],"evidence":["%s — QA evidence"]}}`, evidenceID.String()), responseID: "roundtrip-summary"}
	summaryWorker, workerErr := automationagent.NewWorker(automationagent.WorkerConfig{InputBucket: inputBucket, OutputBucket: outputBucket, AllowedOperations: []string{"delivery.summary"}}, automationagent.NewAWSObjectStore(runtime.S3), callback, summaryProvider)
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

type roundtripProvider struct {
	content    string
	responseID string
	calls      int
}

func (p *roundtripProvider) Complete(context.Context, []automationagent.Message, int) (automationagent.Completion, error) {
	p.calls++
	return automationagent.Completion{Provider: automationagent.ProviderMiniMax, Model: "MiniMax-M3", Content: p.content, ResponseID: p.responseID, Usage: map[string]any{"input_tokens": 8, "output_tokens": 12, "total_tokens": 20}}, nil
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
