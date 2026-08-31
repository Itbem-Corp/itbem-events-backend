package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/internal/agentwork"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/smithy-go"
	"github.com/gofrs/uuid"
)

const (
	awsEmulatorInputBucket  = "itbem-ai-inputs-local"
	awsEmulatorOutputBucket = "itbem-ai-outputs-local"
)

type countingProvider struct {
	mu         sync.Mutex
	calls      int
	completion Completion
	err        error
}

func (p *countingProvider) Complete(context.Context, []Message, int) (Completion, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.completion, p.err
}

func (p *countingProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestAWSEmulatorTransportRoundTrip(t *testing.T) {
	config, runtime := requireAWSEmulator(t)
	queueURL := createAWSEmulatorQueue(t, runtime)

	var callbacks []TaskUpdate
	var callbackMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut || request.Header.Get("X-Automation-Secret") != "integration-callback-secret" {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		var update TaskUpdate
		if json.NewDecoder(request.Body).Decode(&update) != nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		callbackMu.Lock()
		callbacks = append(callbacks, update)
		callbackMu.Unlock()
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	callback, err := NewHTTPCallback(server.URL, "integration-callback-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(config.WorkerConfig, NewAWSObjectStore(runtime.S3), callback, fakeProvider{completion: emulatorCompletion("AWS emulator transport confirmed.", "aws-emulator-round-trip")})
	if err != nil {
		t.Fatal(err)
	}

	taskID := uuid.Must(uuid.NewV4()).String()
	inputKey, outputKey := putAWSEmulatorTask(t, runtime, config, queueURL, taskID, "aws-emulator-round-trip")
	cleanupAWSEmulatorObjects(t, runtime, config, taskID, inputKey)
	queue, err := NewAWSQueue(runtime.SQS, queueURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	messages, err := queue.Receive(ctx, 1)
	if err != nil || len(messages) != 1 {
		t.Fatalf("receive AWS emulator message: %#v / %v", messages, err)
	}
	if err := ProcessQueueMessage(ctx, worker, queue, messages[0]); err != nil {
		t.Fatal(err)
	}
	callbackMu.Lock()
	if len(callbacks) != 2 || callbacks[0].Status != "running" || callbacks[1].Status != "completed" {
		callbackMu.Unlock()
		t.Fatalf("unexpected callbacks: %#v", callbacks)
	}
	callbackMu.Unlock()
	output, err := runtime.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(config.OutputBucket), Key: aws.String(outputKey)})
	if err != nil || output.ServerSideEncryption != s3types.ServerSideEncryptionAes256 {
		t.Fatalf("expected encrypted output: %v / %#v", err, output)
	}
	defer output.Body.Close()
	remaining, err := runtime.SQS.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 0})
	if err != nil || len(remaining.Messages) != 0 {
		t.Fatalf("message was not deleted: %#v / %v", remaining.Messages, err)
	}
}

func TestAWSEmulatorRedeliveryReusesPersistedResult(t *testing.T) {
	config, runtime := requireAWSEmulator(t)
	queueURL := createAWSEmulatorQueue(t, runtime)
	provider := &countingProvider{completion: emulatorCompletion("Persist once and safely redeliver.", "aws-emulator-redelivery")}

	var callbackMu sync.Mutex
	var callbacks []TaskUpdate
	completedAttempts, acceptedCompletions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut || request.Header.Get("X-Automation-Secret") != "integration-callback-secret" {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		var update TaskUpdate
		if json.NewDecoder(request.Body).Decode(&update) != nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		callbackMu.Lock()
		callbacks = append(callbacks, update)
		if update.Status == "completed" {
			completedAttempts++
			if completedAttempts == 1 {
				callbackMu.Unlock()
				response.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			acceptedCompletions++
		}
		callbackMu.Unlock()
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	callback, err := NewHTTPCallback(server.URL, "integration-callback-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(config.WorkerConfig, NewAWSObjectStore(runtime.S3), callback, provider)
	if err != nil {
		t.Fatal(err)
	}

	taskID := uuid.Must(uuid.NewV4()).String()
	inputKey, _ := putAWSEmulatorTask(t, runtime, config, queueURL, taskID, "aws-emulator-redelivery")
	cleanupAWSEmulatorObjects(t, runtime, config, taskID, inputKey)
	queue, err := NewAWSQueue(runtime.SQS, queueURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	firstDelivery, err := queue.Receive(ctx, 1)
	if err != nil || len(firstDelivery) != 1 {
		t.Fatalf("receive first AWS emulator delivery: %#v / %v", firstDelivery, err)
	}
	if err := ProcessQueueMessage(ctx, worker, queue, firstDelivery[0]); err == nil {
		t.Fatal("expected the first completed callback to fail")
	}
	if provider.callCount() != 1 {
		t.Fatalf("expected one provider call after the first delivery, got %d", provider.callCount())
	}
	if _, err := runtime.SQS.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(firstDelivery[0].ReceiptHandle), VisibilityTimeout: 0}); err != nil {
		t.Fatalf("make retained message visible: %v", err)
	}
	restartedProvider := &countingProvider{err: errors.New("provider must not be called during persisted-result recovery")}
	restartedWorker, err := NewWorker(config.WorkerConfig, NewAWSObjectStore(runtime.S3), callback, restartedProvider)
	if err != nil {
		t.Fatal(err)
	}
	secondDelivery, err := queue.Receive(ctx, 1)
	if err != nil || len(secondDelivery) != 1 {
		t.Fatalf("receive redelivered AWS emulator message: %#v / %v", secondDelivery, err)
	}
	if err := ProcessQueueMessage(ctx, restartedWorker, queue, secondDelivery[0]); err != nil {
		t.Fatalf("complete redelivery from persisted result: %v", err)
	}
	if provider.callCount() != 1 || restartedProvider.callCount() != 0 {
		t.Fatalf("redelivery made a duplicate provider call: original=%d restarted=%d", provider.callCount(), restartedProvider.callCount())
	}

	callbackMu.Lock()
	callbackSnapshot := append([]TaskUpdate(nil), callbacks...)
	completedSnapshot, acceptedSnapshot := completedAttempts, acceptedCompletions
	callbackMu.Unlock()
	if completedSnapshot != 2 || acceptedSnapshot != 1 {
		t.Fatalf("expected one accepted terminal effect after one failed attempt, got attempts=%d accepted=%d", completedSnapshot, acceptedSnapshot)
	}
	if len(callbackSnapshot) != 4 || callbackSnapshot[0].Status != "running" || callbackSnapshot[1].Status != "completed" || callbackSnapshot[2].Status != "running" || callbackSnapshot[3].Status != "completed" {
		t.Fatalf("unexpected redelivery callbacks: %#v", callbackSnapshot)
	}
	if callbackSnapshot[1].RunID == "" || callbackSnapshot[3].RecoveryRunID != callbackSnapshot[1].RunID || callbackSnapshot[3].RunID == callbackSnapshot[1].RunID {
		t.Fatalf("redelivery did not bind the new lease to the original provider run: %#v", callbackSnapshot)
	}
	remaining, err := runtime.SQS.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 0})
	if err != nil || len(remaining.Messages) != 0 {
		t.Fatalf("redelivered message was not deleted: %#v / %v", remaining.Messages, err)
	}
}

func TestAWSEmulatorRoleLaneIsolation(t *testing.T) {
	config, runtime := requireAWSEmulator(t)
	_, revision, lookup := testOnboardingProbeWorkspace(t, false)
	t.Setenv("ITBEM_AI_WORKSPACES_JSON", lookup("ITBEM_AI_WORKSPACES_JSON"))
	probeDelivery, err := BuildOnboardingProbeDelivery("workspace://service", "github://acme/service", "trunk", revision, []string{"unit"})
	if err != nil {
		t.Fatal(err)
	}

	var callbackMu sync.Mutex
	callbacks := map[string][]TaskUpdate{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut || request.Header.Get("X-Automation-Secret") != "integration-callback-secret" {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		var update TaskUpdate
		if json.NewDecoder(request.Body).Decode(&update) != nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		taskID := strings.TrimPrefix(request.URL.Path, "/api/internal/automation/tasks/")
		callbackMu.Lock()
		callbacks[taskID] = append(callbacks[taskID], update)
		callbackMu.Unlock()
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	callback, err := NewHTTPCallback(server.URL, "integration-callback-secret", nil)
	if err != nil {
		t.Fatal(err)
	}

	type laneFixture struct {
		role       agentwork.Role
		lane       agentwork.Lane
		operation  string
		input      TaskInput
		completion string
		terminal   string
	}
	fixtures := []laneFixture{
		{role: agentwork.RoleOrchestrator, lane: agentwork.LaneOrchestration, operation: agentwork.OperationDocumentAnalyze, input: TaskInput{Prompt: "Summarize the bounded fixture."}, completion: "The bounded fixture is valid.", terminal: "completed"},
		{role: agentwork.RolePrincipalEngineer, lane: agentwork.LaneEngineering, operation: agentwork.OperationProductIdeate, input: TaskInput{Prompt: "Compare two bounded directions."}, completion: validProductBrief, terminal: "completed"},
		{role: agentwork.RoleReviewer, lane: agentwork.LaneReview, operation: agentwork.OperationCodeReview, input: TaskInput{Prompt: "Review only the frozen patch.", Delivery: validCodeReviewInput()}, completion: validCodeReview(), terminal: "completed"},
		{role: agentwork.RoleQA, lane: agentwork.LaneQA, operation: agentwork.OperationDeliveryOnboardingProbe, input: TaskInput{Delivery: probeDelivery}, terminal: "completed"},
		{role: agentwork.RoleReleaseManager, lane: agentwork.LaneRelease, operation: agentwork.OperationDeliveryReleaseGate, input: TaskInput{Delivery: json.RawMessage(`{"project":{"id":"fixture"}}`)}, terminal: "failed"},
	}

	queues := make(map[agentwork.Lane]*AWSQueue, len(fixtures))
	workers := make(map[agentwork.Lane]*Worker, len(fixtures))
	providers := make(map[agentwork.Lane]*countingProvider, len(fixtures))
	for _, fixture := range fixtures {
		queueURL := createAWSEmulatorQueue(t, runtime)
		queue, queueErr := NewAWSQueue(runtime.SQS, queueURL)
		if queueErr != nil {
			t.Fatal(queueErr)
		}
		provider := &countingProvider{completion: emulatorCompletion(fixture.completion, "lane-"+string(fixture.lane)), err: errors.New("provider must not be called by a deterministic lane")}
		if fixture.completion != "" {
			provider.err = nil
		}
		workerConfig := config.WorkerConfig
		workerConfig.Role, workerConfig.Lane = fixture.role, fixture.lane
		worker, workerErr := NewWorker(workerConfig, NewAWSObjectStore(runtime.S3), callback, provider)
		if workerErr != nil {
			t.Fatal(workerErr)
		}
		queues[fixture.lane], workers[fixture.lane], providers[fixture.lane] = queue, worker, provider
	}

	for index, fixture := range fixtures {
		taskID := uuid.Must(uuid.NewV4()).String()
		inputKey := putAWSEmulatorLaneTask(t, runtime, config, queues[fixture.lane].queueURL, taskID, fixture.operation, fixture.input)
		cleanupAWSEmulatorObjects(t, runtime, config, taskID, inputKey)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		messages, receiveErr := queues[fixture.lane].Receive(ctx, 1)
		if receiveErr != nil || len(messages) != 1 {
			cancel()
			t.Fatalf("receive %s lane message: %#v / %v", fixture.lane, messages, receiveErr)
		}
		decoded, decodeErr := DecodeTaskMessage(messages[0].Body)
		if decodeErr != nil {
			cancel()
			t.Fatal(decodeErr)
		}
		wrongLane := fixtures[(index+1)%len(fixtures)].lane
		if processErr := workers[wrongLane].Process(ctx, decoded); processErr == nil || !strings.Contains(processErr.Error(), "outside this worker role") {
			cancel()
			t.Fatalf("%s task crossed into %s: %v", fixture.lane, wrongLane, processErr)
		}
		callbackMu.Lock()
		wrongUpdates := len(callbacks[taskID])
		callbackMu.Unlock()
		if wrongUpdates != 0 {
			cancel()
			t.Fatalf("wrong-role %s worker emitted a callback for %s", wrongLane, fixture.lane)
		}
		if processErr := ProcessQueueMessage(ctx, workers[fixture.lane], queues[fixture.lane], messages[0]); processErr != nil {
			cancel()
			t.Fatalf("process %s lane: %v", fixture.lane, processErr)
		}
		cancel()
		callbackMu.Lock()
		updates := append([]TaskUpdate(nil), callbacks[taskID]...)
		callbackMu.Unlock()
		if len(updates) != 2 || updates[0].Status != "running" || updates[1].Status != fixture.terminal {
			t.Fatalf("unexpected %s lane callbacks: %#v", fixture.lane, updates)
		}
		if fixture.completion == "" && providers[fixture.lane].callCount() != 0 {
			t.Fatalf("deterministic %s lane invoked the model provider", fixture.lane)
		}
		if fixture.completion != "" && providers[fixture.lane].callCount() != 1 {
			t.Fatalf("%s lane made %d provider calls; expected exactly one", fixture.lane, providers[fixture.lane].callCount())
		}
	}

	for _, fixture := range fixtures {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		remaining, receiveErr := runtime.SQS.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queues[fixture.lane].queueURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 0})
		cancel()
		if receiveErr != nil || len(remaining.Messages) != 0 {
			t.Fatalf("%s lane retained terminal work: %#v / %v", fixture.lane, remaining.Messages, receiveErr)
		}
	}
}

func requireAWSEmulator(t *testing.T) (RuntimeConfig, AWSRuntime) {
	t.Helper()
	if testing.Short() || (os.Getenv("ITBEM_AWS_EMULATOR_E2E") != "1" && os.Getenv("ITBEM_LOCALSTACK_E2E") != "1") {
		t.Skip("set ITBEM_AWS_EMULATOR_E2E=1 to run against a loopback AWS emulator")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	endpoint := strings.TrimSpace(os.Getenv("ITBEM_AWS_EMULATOR_ENDPOINT"))
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("ITBEM_LOCALSTACK_ENDPOINT"))
	}
	if endpoint == "" {
		endpoint = "http://localhost:4566"
	}
	if err := validateLocalEndpoint(endpoint); err != nil {
		t.Fatalf("AWS emulator endpoint: %v", err)
	}
	config := RuntimeConfig{WorkerConfig: WorkerConfig{InputBucket: awsEmulatorInputBucket, OutputBucket: awsEmulatorOutputBucket}, AWSRegion: "us-east-1", SQSEndpoint: endpoint, S3Endpoint: endpoint}
	runtime, err := NewAWSRuntime(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	for _, bucket := range []string{config.InputBucket, config.OutputBucket} {
		_, err := runtime.S3.HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: aws.String(bucket)})
		if err == nil {
			continue
		}
		if !awsEmulatorBucketMissing(err) {
			t.Fatalf("inspect AWS emulator bucket %s: %v", bucket, err)
		}
		if _, err := runtime.S3.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
			t.Fatalf("create AWS emulator bucket %s: %v", bucket, err)
		}
	}
	return config, runtime
}

func awsEmulatorBucketMissing(err error) bool {
	var notFound *s3types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(apiError.ErrorCode()))
	return code == "notfound" || code == "nosuchbucket"
}

func TestAWSEmulatorBucketMissingClassification(t *testing.T) {
	if !awsEmulatorBucketMissing(&smithy.GenericAPIError{Code: "NoSuchBucket"}) {
		t.Fatal("a confirmed missing bucket must be creatable")
	}
	if awsEmulatorBucketMissing(&smithy.GenericAPIError{Code: "AccessDenied"}) || awsEmulatorBucketMissing(errors.New("network unavailable")) {
		t.Fatal("non-missing bucket failures must remain visible")
	}
}

func createAWSEmulatorQueue(t *testing.T, runtime AWSRuntime) string {
	t.Helper()
	name := "itbem-ai-e2e-" + strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")
	created, err := runtime.SQS.CreateQueue(context.Background(), &sqs.CreateQueueInput{QueueName: aws.String(name)})
	if err != nil || created.QueueUrl == nil {
		t.Fatalf("create AWS emulator queue: %v", err)
	}
	queueURL := *created.QueueUrl
	t.Cleanup(func() {
		_, _ = runtime.SQS.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
	})
	return queueURL
}

func putAWSEmulatorTask(t *testing.T, runtime AWSRuntime, config RuntimeConfig, queueURL, taskID, jobID string) (string, string) {
	t.Helper()
	inputKey, outputKey := "automation/inputs/"+taskID+"/input.json", "automation/"+taskID+"/result.json"
	input, err := json.Marshal(TaskInput{Prompt: "Return an integration confirmation."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.S3.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(config.InputBucket), Key: aws.String(inputKey), Body: strings.NewReader(string(input)), ContentType: aws.String("application/json"), ServerSideEncryption: s3types.ServerSideEncryptionAes256}); err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.JobID, message.Payload.Operation, message.Payload.TaskID = jobID, "ai.chat", taskID
	message.Payload.InputRef = "s3://" + config.InputBucket + "/" + inputKey
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SQS.SendMessage(context.Background(), &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(encoded))}); err != nil {
		t.Fatal(err)
	}
	return inputKey, outputKey
}

func putAWSEmulatorLaneTask(t *testing.T, runtime AWSRuntime, config RuntimeConfig, queueURL, taskID, operation string, input TaskInput) string {
	t.Helper()
	inputKey := "automation/inputs/" + taskID + "/input.json"
	encodedInput, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.S3.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(config.InputBucket), Key: aws.String(inputKey), Body: strings.NewReader(string(encodedInput)), ContentType: aws.String("application/json"), ServerSideEncryption: s3types.ServerSideEncryptionAes256}); err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.JobID, message.Payload.Operation, message.Payload.TaskID = "lane-"+taskID, operation, taskID
	message.Payload.InputRef = "s3://" + config.InputBucket + "/" + inputKey
	encodedMessage, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SQS.SendMessage(context.Background(), &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(encodedMessage))}); err != nil {
		t.Fatal(err)
	}
	return inputKey
}

func cleanupAWSEmulatorObjects(t *testing.T, runtime AWSRuntime, config RuntimeConfig, taskID, inputKey string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = runtime.S3.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(config.InputBucket), Key: aws.String(inputKey)})
		paginator := s3.NewListObjectsV2Paginator(runtime.S3, &s3.ListObjectsV2Input{Bucket: aws.String(config.OutputBucket), Prefix: aws.String("automation/" + taskID + "/")})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(context.Background())
			if err != nil {
				return
			}
			for _, object := range page.Contents {
				if object.Key != nil {
					_, _ = runtime.S3.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(config.OutputBucket), Key: object.Key})
				}
			}
		}
	})
}

func emulatorCompletion(content, responseID string) Completion {
	return Completion{Provider: ProviderMiniMax, Model: "MiniMax-M2.7", Content: content, Usage: map[string]any{"total_tokens": 3}, ResponseID: responseID}
}

var _ ProviderClient = (*countingProvider)(nil)
