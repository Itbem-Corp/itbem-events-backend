//go:build integration

package automation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/releasegate"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestReleaseObserverSignedAdmissionRejectsCancelledTaskAndReplayedNonce(t *testing.T) {
	ctx := context.Background()
	container, err := postgrescontainer.Run(ctx, "postgres:16-alpine", postgrescontainer.WithDatabase("testdb"), postgrescontainer.WithUsername("test"), postgrescontainer.WithPassword("test"), postgrescontainer.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error)
	require.NoError(t, db.AutoMigrate(&models.AutomationTask{}, &models.AutomationAgentInstance{}, &models.AutomationAgentCallbackNonce{}, &models.AutomationReleaseObservation{}))
	previous := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previous })
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "synthetic-release-auth-fixture")
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	encodedPublic, err := agentcallbackauth.EncodePublicKey(public)
	require.NoError(t, err)
	instance, taskID, item := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	run := uuid.Must(uuid.NewV4()).String()
	expires := time.Now().UTC().Add(time.Minute)
	require.NoError(t, db.Create(&models.AutomationAgentInstance{ID: instance, AgentKey: "release", MachineID: "synthetic", PublicKey: encodedPublic, Status: "active"}).Error)
	require.NoError(t, db.Create(&models.AutomationTask{ID: taskID, JobID: uuid.Must(uuid.NewV4()), Operation: "delivery.release_gate", Status: "cancelled", RunID: run, LeaseExpiresAt: &expires, InputRef: "s3://synthetic/task.json", AgentInstanceID: &instance, AgentKey: "release", MachineID: "synthetic", DeliveryWorkItemID: &item, EvidenceSubjectDigest: strings.Repeat("a", 64)}).Error)
	identity := gatewayIdentity{Role: agentwork.RoleReleaseManager, Lane: agentwork.LaneRelease}
	lease, err := sealGatewayLease(gatewayLease{Version: 1, Role: string(identity.Role), Lane: string(identity.Lane), TaskID: taskID.String(), InputRef: "s3://synthetic/task.json", ReceiptHandle: "synthetic", ExpiresAt: expires.Unix()})
	require.NoError(t, err)
	body, err := json.Marshal(map[string]string{"lease_token": lease, "run_id": run})
	require.NoError(t, err)
	path := "/api/internal/automation/gateway/release-observation"
	nonce := uuid.Must(uuid.NewV4()).String()
	timestamp := time.Now().UTC().Unix()
	signature, err := agentcallbackauth.SignRequest(private, instance.String(), http.MethodPost, path, timestamp, nonce, body)
	require.NoError(t, err)
	encodedSignature, err := agentcallbackauth.EncodeSignature(signature)
	require.NoError(t, err)
	var cfg *models.Config
	app := echo.New()
	app.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { c.Set("config", cfg); return next(c) }
	})
	app.POST(path, GatewayReleaseObservation, AgentCallbackAuthentication)
	var lastResponse *httptest.ResponseRecorder
	call := func(payload []byte) int {
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		request.Header.Set(agentcallbackauth.InstanceIDHeader, instance.String())
		request.Header.Set(agentcallbackauth.TimestampHeader, strconv.FormatInt(timestamp, 10))
		request.Header.Set(agentcallbackauth.NonceHeader, nonce)
		request.Header.Set(agentcallbackauth.SignatureHeader, encodedSignature)
		request.Header.Set("X-Agent-Gateway-Token", deriveGatewayToken("synthetic-release-auth-fixture", identity))
		request.Header.Set("X-Agent-Role", string(identity.Role))
		request.Header.Set("X-Agent-Lane", string(identity.Lane))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		lastResponse = response
		return response.Code
	}
	require.Equal(t, http.StatusUnauthorized, call(append(append([]byte{}, body...), byte(' '))), "body mutation must fail signature before admission")
	require.Equal(t, http.StatusForbidden, call(body), "authenticated cancellation must fail before storage or GitHub reads")
	require.Equal(t, http.StatusUnauthorized, call(body), "durable nonce must deny replay")
	var count int64
	require.NoError(t, db.Model(&models.AutomationAgentCallbackNonce{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
	var revokeDuringObservation atomic.Bool
	head := strings.Repeat("b", 40)
	github := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/installation":
			_ = json.NewEncoder(response).Encode(map[string]int64{"id": 22})
		case request.Method == http.MethodPost && request.URL.Path == "/app/installations/22/access_tokens":
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(map[string]string{"token": "repository-token", "expires_at": time.Now().UTC().Add(45 * time.Minute).Format(time.RFC3339)})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/pulls/42":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"state": "open", "draft": false, "merged": false, "mergeable": true, "mergeable_state": "clean",
				"head": map[string]string{"sha": head}, "base": map[string]string{"ref": "main"}, "user": map[string]string{"login": "author"},
			})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/pulls/42/reviews":
			_ = json.NewEncoder(response).Encode([]map[string]any{{"id": 1, "state": "APPROVED", "commit_id": head, "user": map[string]string{"login": "reviewer"}}})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/branches/main":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"name": "main", "protected": true,
				"protection": map[string]any{"required_status_checks": map[string]any{
					"contexts": []string{"ci"}, "checks": []map[string]any{{"context": "ci", "app_id": 99}},
				}},
			})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/rules/branches/main":
			_ = json.NewEncoder(response).Encode([]any{})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/commits/"+head+"/check-runs":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"total_count": 1,
				"check_runs":  []map[string]any{{"id": 1, "name": "ci", "head_sha": head, "status": "completed", "conclusion": "success", "app": map[string]int64{"id": 99}}},
			})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/commits/"+head+"/status":
			_ = json.NewEncoder(response).Encode(map[string]any{"sha": head, "statuses": []any{}})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/contents/.github/workflows/deploy.yml":
			if request.URL.Query().Get("ref") != head {
				t.Error("workflow not bound to frozen SHA")
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"type": "file", "path": ".github/workflows/deploy.yml", "sha": strings.Repeat("b", 40)})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/environments/production":
			if revokeDuringObservation.Load() {
				if err := db.Model(&models.AutomationAgentInstance{}).Where("id = ?", instance).Update("status", "revoked").Error; err != nil {
					t.Errorf("synthetic revocation failed: %v", err)
				}
			}
			_ = json.NewEncoder(response).Encode(map[string]string{"name": "production"})
		default:
			t.Fatalf("unexpected GitHub Gatekeeper request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer github.Close()
	appKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	syntheticPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(appKey)})
	t.Setenv("ITBEM_GITHUB_APP_ID", "12345")
	t.Setenv("ITBEM_GITHUB_INSTALLATION_IDS", "22")
	t.Setenv("ITBEM_GITHUB_APP_PRIVATE_KEY", string(syntheticPEM))
	t.Setenv("ITBEM_GITHUB_API_BASE_URL", github.URL)
	candidate := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: item.String(), Revisions: []releasegate.Revision{{Repository: "example/service", Branch: "main", SHA: head}}, Policy: releasegate.Policy{Resolved: false}}
	digest, err := releasegate.RevisionMatrixDigest(candidate.Revisions)
	require.NoError(t, err)
	delivery, err := json.Marshal(map[string]any{"gatekeeper": candidate, "change_sets": []map[string]any{{"commit_sha": head, "review_type": "pull_request", "pull_request_url": "https://github.com/example/service/pull/42"}}, "release_environment": []automationagent.GitHubEnvironmentRequirement{{Repository: "example/service", HeadSHA: head, Workflow: ".github/workflows/deploy.yml", Environment: "production", RequiredSecretReferences: []string{}, RequiredVariableReferences: []string{}}}})
	require.NoError(t, err)
	input, err := json.Marshal(automationagent.TaskInput{Delivery: delivery})
	require.NoError(t, err)
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/synthetic/automation/inputs/fixture/input.json" {
			t.Errorf("unexpected storage request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write(input)
	}))
	defer storage.Close()
	cfg = &models.Config{AutomationInputBucket: "synthetic", S3Endpoint: storage.URL, S3UsePathStyle: "true", AwsRegion: "us-east-1", S3ClientId: "test", S3ClientSecret: "test"}
	ref := "s3://synthetic/automation/inputs/fixture/input.json"
	require.NoError(t, db.Model(&models.AutomationTask{}).Where("id = ?", taskID).Updates(map[string]any{"status": "running", "input_ref": ref, "evidence_subject_digest": digest, "requested_by": "synthetic-human"}).Error)
	lease, err = sealGatewayLease(gatewayLease{Version: 1, Role: string(identity.Role), Lane: string(identity.Lane), TaskID: taskID.String(), InputRef: ref, ReceiptHandle: "synthetic", ExpiresAt: expires.Unix()})
	require.NoError(t, err)
	body, err = json.Marshal(map[string]string{"lease_token": lease, "run_id": run})
	require.NoError(t, err)
	nonce = uuid.Must(uuid.NewV4()).String()
	signature, err = agentcallbackauth.SignRequest(private, instance.String(), http.MethodPost, path, timestamp, nonce, body)
	require.NoError(t, err)
	encodedSignature, err = agentcallbackauth.EncodeSignature(signature)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, call(body), "positive signed observer: %s", lastResponse.Body.String())
	var result struct {
		Data struct {
			SchemaVersion int               `json:"schema_version"`
			Input         releasegate.Input `json:"gatekeeper_input"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(lastResponse.Body.Bytes(), &result))
	require.Equal(t, 2, result.Data.SchemaVersion)
	require.Nil(t, result.Data.Input.HumanApproval)
	require.Len(t, result.Data.Input.Checks, 1)
	got, err := releasegate.RevisionMatrixDigest(result.Data.Input.Revisions)
	require.NoError(t, err)
	require.Equal(t, digest, got)

	revokeDuringObservation.Store(true)
	nonce = uuid.Must(uuid.NewV4()).String()
	signature, err = agentcallbackauth.SignRequest(private, instance.String(), http.MethodPost, path, timestamp, nonce, body)
	require.NoError(t, err)
	encodedSignature, err = agentcallbackauth.EncodeSignature(signature)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, call(body), "revocation during observation must discard evidence")

	// Cross the actual HTTP client/server boundary with a fresh synthetic local
	// identity. No private-key constructor or unsigned transport is substituted.
	revokeDuringObservation.Store(false)
	clientIdentity, err := automationagent.LoadLocalMachineIdentity("", t.TempDir())
	require.NoError(t, err)
	clientPublic, err := agentcallbackauth.EncodePublicKey(clientIdentity.PublicKey())
	require.NoError(t, err)
	clientInstance := uuid.Must(uuid.NewV4())
	clientRun := uuid.Must(uuid.NewV4()).String()
	require.NoError(t, db.Create(&models.AutomationAgentInstance{ID: clientInstance, AgentKey: "release", MachineID: clientIdentity.MachineID(), PublicKey: clientPublic, Status: "active"}).Error)
	require.NoError(t, db.Model(&models.AutomationTask{}).Where("id = ?", taskID).Updates(map[string]any{"run_id": clientRun, "agent_instance_id": clientInstance, "machine_id": clientIdentity.MachineID()}).Error)
	server := httptest.NewServer(app)
	defer server.Close()
	callback, err := automationagent.NewHTTPCallback(server.URL, clientIdentity, clientInstance.String(), server.Client())
	require.NoError(t, err)
	gateway, err := automationagent.NewHTTPGateway(server.URL, deriveGatewayToken("synthetic-release-auth-fixture", identity), identity.Role, identity.Lane, server.Client())
	require.NoError(t, err)
	clientContext := gateway.BindMessageContext(ctx, automationagent.QueueMessage{ReceiptHandle: lease})
	clientObservation, err := callback.ObserveRelease(clientContext, gateway, taskID.String(), clientRun, delivery)
	require.NoError(t, err)
	clientRaw, err := json.Marshal(clientObservation)
	require.NoError(t, err)
	var current models.AutomationTask
	require.NoError(t, db.First(&current, taskID).Error)
	require.NoError(t, verifyServerReleaseObservation(db, &current, clientRun, clientInstance, clientRaw))
	var observationCount int64
	require.NoError(t, db.Model(&models.AutomationReleaseObservation{}).Where("task_id = ?", taskID).Count(&observationCount).Error)
	require.Equal(t, int64(2), observationCount, "revoked observation must not create a server receipt")

}
