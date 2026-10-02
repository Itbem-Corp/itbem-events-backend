package automationagent

import (
	"context"
	"encoding/json"
	"events-stocks/internal/inferencecapability"
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

func grantTransportFixtureCapability(t *testing.T, response http.ResponseWriter, request *http.Request, update TaskUpdate, operation string) {
	t.Helper()
	if update.Status != "running" {
		return
	}
	taskID := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
	if err := issueTestInferenceCapability(taskID, update, operation); err != nil {
		t.Errorf("issue fixture capability: %v", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	token, ok := inferenceCapabilityForRun(taskID, update.RunID, time.Now().UTC())
	if !ok {
		t.Error("fixture capability was not persisted")
		return
	}
	response.Header().Set(inferencecapability.HeaderName, token)
}

func prepareTransportFixtureBuckets(t *testing.T, client *s3.Client, config *RuntimeConfig) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")
	config.InputBucket = "itbem-e2e-input-" + suffix
	config.OutputBucket = "itbem-e2e-output-" + suffix
	for _, bucket := range []string{config.InputBucket, config.OutputBucket} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		cancel()
		if err != nil {
			t.Fatalf("create isolated transport bucket: %v", err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
			for paginator.HasMorePages() {
				page, err := paginator.NextPage(ctx)
				if err != nil {
					t.Errorf("list fixture bucket during cleanup: %v", err)
					return
				}
				for _, object := range page.Contents {
					if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: object.Key}); err != nil {
						t.Errorf("remove fixture object: %v", err)
						return
					}
				}
			}
			if _, err := client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}); err != nil {
				t.Errorf("remove fixture bucket: %v", err)
			}
		})
	}
}

func TestLocalStackTransportRoundTrip(t *testing.T) {
	if testing.Short() || os.Getenv("ITBEM_LOCALSTACK_E2E") != "1" {
		t.Skip("set ITBEM_LOCALSTACK_E2E=1 to run against local loopback LocalStack")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	endpoint := os.Getenv("ITBEM_LOCALSTACK_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:4566"
	}
	config := RuntimeConfig{WorkerConfig: WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, AWSRegion: "us-east-1", SQSEndpoint: endpoint, S3Endpoint: endpoint}
	runtime, err := NewAWSRuntime(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	prepareTransportFixtureBuckets(t, runtime.S3, &config)
	queueName := "itbem-ai-e2e-" + strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")
	queueURLResponse, err := runtime.SQS.CreateQueue(context.Background(), &sqs.CreateQueueInput{QueueName: aws.String(queueName)})
	if err != nil || queueURLResponse.QueueUrl == nil {
		t.Fatalf("create isolated LocalStack queue: %v", err)
	}
	queueURL, err := normalizeLocalQueueURL(*queueURLResponse.QueueUrl, endpoint)
	if err != nil {
		t.Fatalf("normalize LocalStack queue URL: %v", err)
	}
	t.Cleanup(func() {
		_, _ = runtime.SQS.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
	})

	var callbacks []TaskUpdate
	var callbackMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut || request.Header.Get("X-Automation-Secret") != "" || request.Header.Get("X-ITBEM-Agent-Signature") == "" {
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
		grantTransportFixtureCapability(t, response, request, update, "ai.chat")
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	callback := newTestHTTPCallback(t, server)
	worker, err := NewWorker(config.WorkerConfig, NewAWSObjectStore(runtime.S3), callback, fakeProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M2.7", Content: "LocalStack transport confirmed.", Usage: map[string]any{"total_tokens": 3}, ResponseID: "local-e2e", CallID: uuid.Must(uuid.NewV4()).String(), ReceiptID: uuid.Must(uuid.NewV4()).String()}})
	if err != nil {
		t.Fatal(err)
	}

	taskID := uuid.Must(uuid.NewV4()).String()
	inputKey, outputKey := "automation/inputs/"+taskID+"/input.json", "automation/"+taskID+"/result.json"
	t.Cleanup(func() {
		_, _ = runtime.S3.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(config.InputBucket), Key: aws.String(inputKey)})
		_, _ = runtime.S3.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(config.OutputBucket), Key: aws.String(outputKey)})
		_, _ = runtime.S3.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(config.OutputBucket), Key: aws.String("automation/" + taskID + "/provider-intent.json")})
	})
	input, _ := json.Marshal(TaskInput{Prompt: "Return an integration confirmation."})
	if _, err := runtime.S3.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(config.InputBucket), Key: aws.String(inputKey), Body: strings.NewReader(string(input)), ContentType: aws.String("application/json"), ServerSideEncryption: s3types.ServerSideEncryptionAes256}); err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.JobID, message.Payload.Operation, message.Payload.TaskID = "local-e2e-job", "ai.chat", taskID
	message.Payload.InputRef = "s3://" + config.InputBucket + "/" + inputKey
	encoded, _ := json.Marshal(message)
	if _, err := runtime.SQS.SendMessage(context.Background(), &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(encoded))}); err != nil {
		t.Fatal(err)
	}
	queue, err := NewAWSQueue(runtime.SQS, queueURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	messages, err := queue.Receive(ctx, 1)
	if err != nil || len(messages) != 1 {
		t.Fatalf("receive LocalStack message: %#v / %v", messages, err)
	}
	if err := ProcessQueueMessage(ctx, worker, queue, messages[0]); err != nil {
		t.Fatal(err)
	}
	callbackMu.Lock()
	if len(callbacks) != 3 || callbacks[0].Status != "running" || callbacks[1].Status != "running" || callbacks[1].ProgressStep != "thinking" || callbacks[1].ProgressCall != 1 || callbacks[2].Status != "completed" {
		callbackMu.Unlock()
		t.Fatalf("unexpected callbacks: %#v", callbacks)
	}
	completed := callbacks[2]
	callbackMu.Unlock()
	outputBucket, completedOutputKey, err := ParsePrivateReference(completed.OutputRef)
	if err != nil || outputBucket != config.OutputBucket || completed.RunID == "" || completedOutputKey != "automation/"+taskID+"/runs/"+completed.RunID+"/result.json" {
		t.Fatalf("completion did not bind the expected private run result: %v", err)
	}
	output, err := runtime.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(outputBucket), Key: aws.String(completedOutputKey)})
	if err != nil || output.ServerSideEncryption != s3types.ServerSideEncryptionAes256 {
		t.Fatalf("expected encrypted output: %v / %#v", err, output)
	}
	defer output.Body.Close()
	remaining, err := runtime.SQS.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 0})
	if err != nil || len(remaining.Messages) != 0 {
		t.Fatalf("message was not deleted: %#v / %v", remaining.Messages, err)
	}
}

type integrationCountingProvider struct {
	completion Completion
	calls      int
}

func (p *integrationCountingProvider) Complete(_ context.Context, _ []Message, _ int) (Completion, error) {
	p.calls++
	return p.completion, nil
}

func TestLocalStackRedeliveryReusesDurableResultWithoutProviderRepeat(t *testing.T) {
	if testing.Short() || os.Getenv("ITBEM_LOCALSTACK_E2E") != "1" {
		t.Skip("set ITBEM_LOCALSTACK_E2E=1 to run against local loopback LocalStack")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	endpoint := os.Getenv("ITBEM_LOCALSTACK_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:4566"
	}
	config := RuntimeConfig{WorkerConfig: WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, AWSRegion: "us-east-1", SQSEndpoint: endpoint, S3Endpoint: endpoint}
	runtime, err := NewAWSRuntime(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	prepareTransportFixtureBuckets(t, runtime.S3, &config)
	queueName := "itbem-ai-e2e-" + strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")
	queueURLResponse, err := runtime.SQS.CreateQueue(context.Background(), &sqs.CreateQueueInput{QueueName: aws.String(queueName)})
	if err != nil || queueURLResponse.QueueUrl == nil {
		t.Fatalf("create isolated LocalStack queue: %v", err)
	}
	queueURL, err := normalizeLocalQueueURL(*queueURLResponse.QueueUrl, endpoint)
	if err != nil {
		t.Fatalf("normalize LocalStack queue URL: %v", err)
	}
	t.Cleanup(func() {
		_, _ = runtime.SQS.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
	})

	taskID := uuid.Must(uuid.NewV4()).String()
	inputKey := "automation/inputs/" + taskID + "/input.json"
	t.Cleanup(func() {
		_, _ = runtime.S3.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(config.InputBucket), Key: aws.String(inputKey)})
		for _, bucket := range []string{config.InputBucket, config.OutputBucket} {
			paginator := s3.NewListObjectsV2Paginator(runtime.S3, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String("automation/" + taskID + "/")})
			for paginator.HasMorePages() {
				page, listErr := paginator.NextPage(context.Background())
				if listErr != nil {
					t.Errorf("list redelivery fixture objects: %v", listErr)
					break
				}
				if len(page.Contents) == 0 {
					continue
				}
				identifiers := make([]s3types.ObjectIdentifier, 0, len(page.Contents))
				for _, object := range page.Contents {
					if object.Key != nil {
						identifiers = append(identifiers, s3types.ObjectIdentifier{Key: object.Key})
					}
				}
				if len(identifiers) > 0 {
					_, _ = runtime.S3.DeleteObjects(context.Background(), &s3.DeleteObjectsInput{Bucket: aws.String(bucket), Delete: &s3types.Delete{Objects: identifiers, Quiet: aws.Bool(true)}})
				}
			}
		}
	})
	input, _ := json.Marshal(TaskInput{Prompt: "Return one durable integration answer.", Delivery: json.RawMessage(`{"work_item":{"id":"local-redelivery"}}`)})
	if _, err := runtime.S3.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(config.InputBucket), Key: aws.String(inputKey), Body: strings.NewReader(string(input)), ContentType: aws.String("application/json"), ServerSideEncryption: s3types.ServerSideEncryptionAes256}); err != nil {
		t.Fatal(err)
	}

	var callbacks []TaskUpdate
	var callbackMu sync.Mutex
	terminalFailures := 0
	successfulTerminals := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut || request.Header.Get("X-Automation-Secret") != "" || request.Header.Get("X-ITBEM-Agent-Signature") == "" {
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
		if update.Status == "completed" && terminalFailures == 0 {
			terminalFailures++
			callbackMu.Unlock()
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		callbackMu.Unlock()
		if update.Status == "completed" {
			callbackMu.Lock()
			successfulTerminals++
			callbackMu.Unlock()
		}
		grantTransportFixtureCapability(t, response, request, update, "delivery.chat")
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	callback := newTestHTTPCallback(t, server)
	provider := &integrationCountingProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: `{"answer":"durable","next_steps":[],"questions":[]}`, Usage: map[string]any{"total_tokens": 3}, ResponseID: "local-redelivery", CallID: uuid.Must(uuid.NewV4()).String(), ReceiptID: uuid.Must(uuid.NewV4()).String()}}
	worker, err := NewWorker(config.WorkerConfig, NewAWSObjectStore(runtime.S3), callback, provider)
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.JobID, message.Payload.Operation, message.Payload.TaskID = "local-redelivery-job", "delivery.chat", taskID
	message.Payload.InputRef = "s3://" + config.InputBucket + "/" + inputKey
	encoded, _ := json.Marshal(message)
	if _, err := runtime.SQS.SendMessage(context.Background(), &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(encoded))}); err != nil {
		t.Fatal(err)
	}
	queue, err := NewAWSQueue(runtime.SQS, queueURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first, err := queue.Receive(ctx, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("receive first LocalStack message: %#v / %v", first, err)
	}
	if err := ProcessQueueMessage(ctx, worker, queue, first[0]); err == nil {
		t.Fatal("terminal callback outage must retain the SQS delivery")
	}
	if provider.calls != 1 {
		t.Fatalf("first delivery provider calls=%d, want 1", provider.calls)
	}
	if _, err := runtime.SQS.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(first[0].ReceiptHandle), VisibilityTimeout: 0}); err != nil {
		t.Fatal(err)
	}
	second, err := queue.Receive(ctx, 1)
	if err != nil || len(second) != 1 {
		t.Fatalf("receive redelivery: %#v / %v", second, err)
	}
	if err := ProcessQueueMessage(ctx, worker, queue, second[0]); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("redelivery repeated provider inference: %d", provider.calls)
	}
	callbackMu.Lock()
	completed := 0
	uncertain := 0
	runIDs := map[string]struct{}{}
	for _, update := range callbacks {
		if update.RunID != "" {
			runIDs[update.RunID] = struct{}{}
		}
		if update.Status == "completed" {
			completed++
		}
		if strings.Contains(update.ErrorMessage, "uncertain") {
			uncertain++
		}
	}
	if completed != 2 || terminalFailures != 1 || successfulTerminals != 1 || uncertain != 0 {
		callbackMu.Unlock()
		t.Fatalf("unexpected callback lifecycle: %#v", callbacks)
	}
	callbackMu.Unlock()
	for _, key := range []string{inputKey, "automation/" + taskID + "/result.json", "automation/" + taskID + "/provider-intent.json"} {
		_, _ = runtime.S3.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(config.InputBucket), Key: aws.String(key)})
		_, _ = runtime.S3.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(config.OutputBucket), Key: aws.String(key)})
	}
	for runID := range runIDs {
		for _, name := range []string{"request.json", "result.json"} {
			_, _ = runtime.S3.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(config.OutputBucket), Key: aws.String("automation/" + taskID + "/runs/" + runID + "/" + name)})
		}
	}
}
