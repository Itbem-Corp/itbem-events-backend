package automationagent

import (
	"context"
	"encoding/json"
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
)

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
	queueURLResponse, err := runtime.SQS.GetQueueUrl(context.Background(), &sqs.GetQueueUrlInput{QueueName: aws.String("itbem-ai-local")})
	if err != nil || queueURLResponse.QueueUrl == nil {
		t.Fatalf("find LocalStack queue: %v", err)
	}

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
	worker, err := NewWorker(config.WorkerConfig, NewAWSObjectStore(runtime.S3), callback, fakeProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M2.7", Content: "LocalStack transport confirmed.", Usage: map[string]any{"total_tokens": 3}, ResponseID: "local-e2e"}})
	if err != nil {
		t.Fatal(err)
	}

	taskID := "d4a4b837-2e18-43af-9f58-6d59629db2bb"
	inputKey, outputKey := "automation/inputs/"+taskID+"/input.json", "automation/"+taskID+"/result.json"
	t.Cleanup(func() {
		_, _ = runtime.S3.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(config.InputBucket), Key: aws.String(inputKey)})
		_, _ = runtime.S3.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(config.OutputBucket), Key: aws.String(outputKey)})
	})
	input, _ := json.Marshal(TaskInput{Prompt: "Return an integration confirmation."})
	if _, err := runtime.S3.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(config.InputBucket), Key: aws.String(inputKey), Body: strings.NewReader(string(input)), ContentType: aws.String("application/json"), ServerSideEncryption: s3types.ServerSideEncryptionAes256}); err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.JobID, message.Payload.Operation, message.Payload.TaskID = "local-e2e-job", "ai.chat", taskID
	message.Payload.InputRef = "s3://" + config.InputBucket + "/" + inputKey
	encoded, _ := json.Marshal(message)
	if _, err := runtime.SQS.SendMessage(context.Background(), &sqs.SendMessageInput{QueueUrl: queueURLResponse.QueueUrl, MessageBody: aws.String(string(encoded))}); err != nil {
		t.Fatal(err)
	}
	queue, err := NewAWSQueue(runtime.SQS, *queueURLResponse.QueueUrl)
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
	if len(callbacks) != 2 || callbacks[0].Status != "running" || callbacks[1].Status != "completed" {
		callbackMu.Unlock()
		t.Fatalf("unexpected callbacks: %#v", callbacks)
	}
	callbackMu.Unlock()
	output, err := runtime.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(config.OutputBucket), Key: aws.String(outputKey)})
	if err != nil || output.ServerSideEncryption != s3types.ServerSideEncryptionAes256 {
		t.Fatalf("expected encrypted output: %v / %#v", err, output)
	}
	remaining, err := runtime.SQS.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: queueURLResponse.QueueUrl, MaxNumberOfMessages: 1, WaitTimeSeconds: 0})
	if err != nil || len(remaining.Messages) != 0 {
		t.Fatalf("message was not deleted: %#v / %v", remaining.Messages, err)
	}
}
