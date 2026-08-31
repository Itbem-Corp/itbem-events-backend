package automationagent

import (
	"context"
	"encoding/json"
	"errors"
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
		if _, err := runtime.S3.HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: aws.String(bucket)}); err == nil {
			continue
		}
		if _, err := runtime.S3.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
			t.Fatalf("create AWS emulator bucket %s: %v", bucket, err)
		}
	}
	return config, runtime
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
