package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/internal/agentwork"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/gofrs/uuid"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLocalStackRoleAdmissionKeepsCrossLaneDeliveriesUnclaimed(t *testing.T) {
	if os.Getenv("ITBEM_LOCALSTACK_E2E") != "1" || testing.Short() {
		t.Skip("requires disposable loopback AWS emulator")
	}
	endpoint := os.Getenv("ITBEM_LOCALSTACK_ENDPOINT")
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
		t.Fatal("role transport qualification requires an explicit loopback emulator")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	config := RuntimeConfig{WorkerConfig: WorkerConfig{InputBucket: "fixture-input", OutputBucket: "fixture-output"}, AWSRegion: "us-east-1", SQSEndpoint: endpoint, S3Endpoint: endpoint}
	runtime, err := NewAWSRuntime(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	operations := []string{"ai.chat", "delivery.plan", "code.review", "delivery.onboarding_probe", "delivery.publish"}
	for _, operation := range operations {
		owner, _ := agentwork.AssignmentForOperation(operation)
		t.Run(string(owner.Lane), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			created, err := runtime.SQS.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("role-fixture-" + strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", ""))})
			if err != nil || created.QueueUrl == nil {
				t.Fatalf("create isolated lane queue: %v", err)
			}
			queueURL, err := normalizeLocalQueueURL(*created.QueueUrl, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cleanupCancel()
				if _, err := runtime.SQS.DeleteQueue(cleanupCtx, &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)}); err != nil {
					t.Errorf("delete fixture queue: %v", err)
				}
			})
			message := validMessage()
			message.Payload.Operation = operation
			message.Payload.InputRef = "s3://fixture-input/automation/inputs/task/input.json"
			body, err := json.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.SQS.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(body))}); err != nil {
				t.Fatal(err)
			}
			queue, err := NewAWSQueueWithWaitTime(runtime.SQS, queueURL, 0)
			if err != nil {
				t.Fatal(err)
			}
			deliveries, err := queue.Receive(ctx, 1)
			if err != nil || len(deliveries) != 1 {
				t.Fatalf("receive lane fixture: %v", err)
			}
			for _, otherOperation := range operations {
				other, _ := agentwork.AssignmentForOperation(otherOperation)
				if other == owner {
					continue
				}
				callback := &fakeCallback{}
				provider := &integrationCountingProvider{}
				workerConfig := config.WorkerConfig
				workerConfig.Role, workerConfig.Lane = other.Role, other.Lane
				worker, err := NewWorker(workerConfig, NewAWSObjectStore(runtime.S3), callback, provider)
				if err != nil {
					t.Fatal(err)
				}
				var retryable *RetryableError
				if err := ProcessQueueMessage(ctx, worker, queue, deliveries[0]); !errors.As(err, &retryable) {
					t.Fatalf("cross-lane delivery must remain retryable: %v", err)
				}
				if len(callback.updates) != 0 || provider.calls != 0 {
					t.Fatal("cross-lane delivery claimed or inferred before rejection")
				}
			}
			if _, err := runtime.SQS.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(deliveries[0].ReceiptHandle), VisibilityTimeout: 0}); err != nil {
				t.Fatal(err)
			}
			retained, err := queue.Receive(ctx, 1)
			if err != nil || len(retained) != 1 || retained[0].Body != string(body) {
				t.Fatalf("wrong-role deliveries deleted or changed the owner's task: %v", err)
			}
			ownerConfig := config.WorkerConfig
			ownerConfig.Role, ownerConfig.Lane = owner.Role, owner.Lane
			worker, err := NewWorker(ownerConfig, NewAWSObjectStore(runtime.S3), &fakeCallback{}, &integrationCountingProvider{})
			if err != nil || !worker.canProcessMessage(retained[0]) {
				t.Fatalf("retained task is not admissible to its owner: %v", err)
			}
		})
	}
}
