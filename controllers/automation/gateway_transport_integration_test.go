package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	automationqueue "events-stocks/repositories/automationqueuerepository"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

// This opt-in test runs in its own Go process because queue initialization is
// deliberately immutable. It exercises actual gateway handlers, HTTPS worker
// clients and AWS SDK requests to an isolated emulator. It never runs a model,
// publishes a PR or synthesizes a completed engineering task.
func TestLocalGatewayFiveLaneTransportRoundTrip(t *testing.T) {
	if os.Getenv("ITBEM_GATEWAY_TRANSPORT_E2E") != "1" {
		t.Skip("set ITBEM_GATEWAY_TRANSPORT_E2E=1 for isolated gateway transport qualification")
	}
	endpoint := os.Getenv("ITBEM_LOCALSTACK_ENDPOINT")
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		t.Fatal("qualification requires explicit http://127.0.0.1:port emulator endpoint")
	}
	if automationqueue.IsConfigured() {
		t.Fatal("qualification must run in an isolated Go process with fresh queue configuration")
	}
	for name, value := range map[string]string{"ENV": "local", "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_SESSION_TOKEN": "", "AWS_PROFILE": "", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null", "AWS_CONFIG_FILE": "/dev/null", "AWS_EC2_METADATA_DISABLED": "true", "AUTOMATION_CALLBACK_SECRET": strings.Repeat("synthetic-fixture-", 4), "AUTOMATION_CALLBACK_SECRET_PREVIOUS": ""} {
		t.Setenv(name, value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	runtime, err := automationagent.NewAWSRuntime(ctx, automationagent.RuntimeConfig{AWSRegion: "us-east-1", SQSEndpoint: endpoint, S3Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")
	cfg := &models.Config{AwsRegion: "us-east-1", S3Region: "us-east-1", S3Endpoint: endpoint, S3UsePathStyle: "true", S3ClientId: "test", S3ClientSecret: "test", AutomationInputBucket: "gateway-input-" + suffix, AutomationOutputBucket: "gateway-output-" + suffix}
	for _, bucket := range []string{cfg.AutomationInputBucket, cfg.AutomationOutputBucket} {
		if _, err := runtime.S3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx, done := context.WithTimeout(context.Background(), 15*time.Second)
			defer done()
			pages := s3.NewListObjectsV2Paginator(runtime.S3, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
			for pages.HasMorePages() {
				page, err := pages.NextPage(cleanupCtx)
				if err != nil {
					t.Error("fixture bucket cleanup failed")
					return
				}
				for _, object := range page.Contents {
					if _, err := runtime.S3.DeleteObject(cleanupCtx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: object.Key}); err != nil {
						t.Error("fixture object cleanup failed")
						return
					}
				}
			}
			if _, err := runtime.S3.DeleteBucket(cleanupCtx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}); err != nil {
				t.Error("fixture bucket cleanup failed")
			}
		})
	}
	createQueue := func(name string) string {
		response, err := runtime.SQS.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("gateway-" + name + "-" + suffix)})
		if err != nil || response.QueueUrl == nil {
			t.Fatal("create isolated gateway queue failed")
		}
		queueURL, err := url.Parse(*response.QueueUrl)
		if err != nil {
			t.Fatal(err)
		}
		queueURL.Scheme, queueURL.Host = parsed.Scheme, parsed.Host
		value := queueURL.String()
		t.Cleanup(func() {
			cleanupCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
			defer done()
			if _, err := runtime.SQS.DeleteQueue(cleanupCtx, &sqs.DeleteQueueInput{QueueUrl: aws.String(value)}); err != nil {
				t.Error("fixture queue cleanup failed")
			}
		})
		return value
	}
	samples := []struct {
		identity  gatewayIdentity
		operation string
	}{
		{gatewayIdentity{agentwork.RoleOrchestrator, agentwork.LaneOrchestration}, "ai.chat"},
		{gatewayIdentity{agentwork.RolePrincipalEngineer, agentwork.LaneEngineering}, "delivery.implementation"},
		{gatewayIdentity{agentwork.RoleReviewer, agentwork.LaneReview}, "code.review"},
		{gatewayIdentity{agentwork.RoleQA, agentwork.LaneQA}, "delivery.qa"},
		{gatewayIdentity{agentwork.RoleReleaseManager, agentwork.LaneRelease}, "delivery.publish"},
	}
	lanes := map[string]string{}
	for _, sample := range samples {
		lanes[string(sample.identity.Lane)] = createQueue(string(sample.identity.Lane))
	}
	laneJSON, err := json.Marshal(lanes)
	if err != nil {
		t.Fatal(err)
	}
	if err := automationqueue.Init("us-east-1", "test", "test", "", "", string(laneJSON), createQueue("dlq"), endpoint); err != nil {
		t.Fatal(err)
	}
	previousClient := configuration.GetS3Client(nil)
	configuration.SetS3Client(runtime.S3)
	t.Cleanup(func() { configuration.SetS3Client(previousClient) })
	app := echo.New()
	app.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { c.Set("config", cfg); return next(c) }
	})
	app.GET("/api/internal/automation/gateway/probe", GatewayProbe)
	app.POST("/api/internal/automation/gateway/leases", GatewayLease)
	app.DELETE("/api/internal/automation/gateway/leases", GatewayAcknowledge)
	app.PUT("/api/internal/automation/gateway/leases/visibility", GatewayVisibility)
	app.POST("/api/internal/automation/gateway/objects/read", GatewayReadObject)
	app.PUT("/api/internal/automation/gateway/objects/write", GatewayWriteObject)
	server := httptest.NewTLSServer(app)
	defer server.Close()
	httpClient := server.Client()
	if transport, ok := httpClient.Transport.(*http.Transport); !ok || transport.TLSClientConfig == nil || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("qualification must verify the fixture TLS certificate")
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	clients := make([]*automationagent.HTTPGateway, len(samples))
	for index, sample := range samples {
		clients[index], err = automationagent.NewHTTPGateway(server.URL, deriveGatewayToken(os.Getenv("AUTOMATION_CALLBACK_SECRET"), sample.identity), sample.identity.Role, sample.identity.Lane, httpClient)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Only the trusted backend SDK clients retain these synthetic credentials.
	// The execution clients below have role tokens and a verified HTTPS client.
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	for index, sample := range samples {
		t.Run(string(sample.identity.Lane), func(t *testing.T) {
			client := clients[index]
			if err := client.Probe(ctx); err != nil {
				t.Fatal(err)
			}
			taskID := uuid.Must(uuid.NewV4()).String()
			inputKey := "automation/inputs/" + taskID + "/input.json"
			input := []byte(fmt.Sprintf(`{"synthetic_transport_fixture":true,"role":%q}`, sample.identity.Role))
			if _, err := runtime.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cfg.AutomationInputBucket), Key: aws.String(inputKey), Body: bytes.NewReader(input), ServerSideEncryption: s3types.ServerSideEncryptionAes256}); err != nil {
				t.Fatal(err)
			}
			message := automationqueue.Message{SchemaVersion: 1, JobID: uuid.Must(uuid.NewV4()).String(), TenantCode: "itbem", Type: "ai.local.process"}
			message.Payload.TaskID, message.Payload.Operation, message.Payload.InputRef, message.Payload.Attempt = taskID, sample.operation, "s3://"+cfg.AutomationInputBucket+"/"+inputKey, 1
			if err := automationqueue.Validate(message); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.SQS.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(lanes[string(sample.identity.Lane)]), MessageBody: aws.String(string(body))}); err != nil {
				t.Fatal(err)
			}
			// Readiness must not consume work, and a token from another role
			// cannot be used by changing the role/lane request headers.
			if err := client.Probe(ctx); err != nil {
				t.Fatal(err)
			}
			for foreignIndex, foreignSample := range samples {
				if foreignIndex == index {
					continue
				}
				forgedIdentity, err := automationagent.NewHTTPGateway(server.URL, deriveGatewayToken(os.Getenv("AUTOMATION_CALLBACK_SECRET"), foreignSample.identity), sample.identity.Role, sample.identity.Lane, httpClient)
				if err != nil {
					t.Fatal(err)
				}
				requireGatewayTransportStatus(t, forgedIdentity.Probe(ctx), http.StatusUnauthorized)
			}
			leased, err := client.Receive(ctx, 1)
			if err != nil || len(leased) != 1 {
				t.Fatalf("own lane did not lease exactly one message: %v", err)
			}
			if leased[0].Body != string(body) {
				t.Fatal("leased message changed")
			}
			bound := client.BindMessageContext(ctx, leased[0])
			observed, err := client.Get(bound, cfg.AutomationInputBucket, inputKey)
			if err != nil || !bytes.Equal(observed, input) {
				t.Fatalf("exact private input failed: %v", err)
			}
			for foreignIndex, foreign := range clients {
				if foreignIndex == index {
					continue
				}
				foreignCtx := foreign.BindMessageContext(ctx, leased[0])
				_, err := foreign.Get(foreignCtx, cfg.AutomationInputBucket, inputKey)
				requireGatewayTransportStatus(t, err, http.StatusUnauthorized)
				if err := foreign.Delete(ctx, leased[0]); err == nil {
					t.Fatal("foreign role acknowledged another lane lease")
				} else {
					requireGatewayTransportStatus(t, err, http.StatusUnauthorized)
				}
			}
			_, err = client.Get(bound, cfg.AutomationInputBucket, "automation/inputs/"+uuid.Must(uuid.NewV4()).String()+"/input.json")
			requireGatewayTransportStatus(t, err, http.StatusForbidden)
			requireGatewayTransportStatus(t, client.PutEncryptedJSON(bound, cfg.AutomationInputBucket, inputKey, []byte(`{}`)), http.StatusForbidden)
			outputKey := "automation/" + taskID + "/result.json"
			output := []byte(`{"synthetic_transport_result":true}`)
			if err := client.PutEncryptedJSON(bound, cfg.AutomationOutputBucket, outputKey, output); err != nil {
				t.Fatal(err)
			}
			// Release visibility to simulate an interrupted execution client. A
			// fresh client must acquire an actual SQS redelivery and a new sealed
			// lease before recovering its task-scoped durable evidence.
			if err := client.Defer(ctx, leased[0], 1); err != nil {
				t.Fatal(err)
			}
			select {
			case <-time.After(1200 * time.Millisecond):
			case <-ctx.Done():
				t.Fatal("qualification deadline exceeded while waiting for redelivery")
			}
			restarted, err := automationagent.NewHTTPGateway(server.URL, deriveGatewayToken(os.Getenv("AUTOMATION_CALLBACK_SECRET"), sample.identity), sample.identity.Role, sample.identity.Lane, httpClient)
			if err != nil {
				t.Fatal(err)
			}
			redelivered, err := restarted.Receive(ctx, 1)
			if err != nil || len(redelivered) != 1 || redelivered[0].Body != string(body) || redelivered[0].ReceiptHandle == leased[0].ReceiptHandle {
				t.Fatalf("restart did not acquire a fresh lease for the original message: %v", err)
			}
			resumed := restarted.BindMessageContext(ctx, redelivered[0])
			observed, err = restarted.Get(resumed, cfg.AutomationOutputBucket, outputKey)
			if err != nil || !bytes.Equal(observed, output) {
				t.Fatalf("durable checkpoint recovery failed: %v", err)
			}
			head, err := runtime.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(cfg.AutomationOutputBucket), Key: aws.String(outputKey)})
			if err != nil || head.ServerSideEncryption != s3types.ServerSideEncryptionAes256 {
				t.Fatal("gateway result lacks AES256 storage encryption")
			}
			if err := restarted.ExtendVisibility(ctx, redelivered[0], 30); err != nil {
				t.Fatal(err)
			}
			if err := restarted.Delete(ctx, redelivered[0]); err != nil {
				t.Fatal(err)
			}
			remaining, err := restarted.Receive(ctx, 1)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("acknowledged own lane is not empty: %v", err)
			}
		})
	}
}

func requireGatewayTransportStatus(t *testing.T, err error, expected int) {
	t.Helper()
	var status interface{ GatewayStatusCode() int }
	if err == nil || !errors.As(err, &status) || status.GatewayStatusCode() != expected {
		t.Fatalf("gateway did not return the required denial status %d", expected)
	}
}
