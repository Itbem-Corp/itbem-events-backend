//go:build integration

package automation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/qaevidence"
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

func TestQASourceSignedGatewayBindsPostgresAuthorityAndIndependentCheckout(t *testing.T) {
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
	require.NoError(t, db.AutoMigrate(&models.AutomationTask{}, &models.AutomationAgentInstance{}, &models.AutomationAgentCallbackNonce{}, &models.AutomationQASourceReceipt{}))
	previous := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previous })
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "synthetic-qa-source-auth-fixture")
	machine, err := automationagent.LoadLocalMachineIdentity("", t.TempDir())
	require.NoError(t, err)
	public, err := agentcallbackauth.EncodePublicKey(machine.PublicKey())
	require.NoError(t, err)
	instance, taskID, item := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	run := uuid.Must(uuid.NewV4()).String()
	expires := time.Now().UTC().Add(time.Minute)
	require.NoError(t, db.Create(&models.AutomationAgentInstance{ID: instance, AgentKey: "qa", MachineID: machine.MachineID(), PublicKey: public, Status: "active"}).Error)
	source := t.TempDir()
	git := func(input []byte, args ...string) []byte {
		cmd := exec.Command("git", args...)
		cmd.Dir = source
		cmd.Stdin = bytes.NewReader(input)
		output, err := cmd.Output()
		require.NoError(t, err)
		return output
	}
	git(nil, "init", "--initial-branch=main")
	git(nil, "config", "user.name", "Synthetic Fixture")
	git(nil, "config", "user.email", "fixture@example.invalid")
	require.NoError(t, os.WriteFile(filepath.Join(source, "README.md"), []byte("exact synthetic source\n"), 0600))
	git(nil, "add", ".")
	git(nil, "commit", "-m", "synthetic published source")
	head := strings.TrimSpace(string(git(nil, "rev-parse", "HEAD")))
	objects := git(nil, "rev-list", "--objects", "--no-object-names", "--no-walk", head)
	pack := git(objects, "pack-objects", "--stdout")
	packDigest := fmt.Sprintf("%x", sha256.Sum256(pack))
	branch := "itbem-agent/" + uuid.Must(uuid.NewV4()).String()
	candidate := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: item.String(), Revisions: []releasegate.Revision{{Repository: "example/service", Branch: "main", SHA: head}}, Policy: releasegate.Policy{RequiredTestKinds: []string{}}}
	matrix, err := releasegate.RevisionMatrixDigest(candidate.Revisions)
	require.NoError(t, err)
	delivery, err := json.Marshal(map[string]any{"gatekeeper": candidate, "change_sets": []any{map[string]any{"repository_ref": "workspace://repo", "branch": branch, "commit_sha": head, "review_type": "pull_request", "ci_status": "passed", "metadata": map[string]string{"remote_repository": "example/service", "target_branch": "main"}}}})
	require.NoError(t, err)
	input, err := json.Marshal(automationagent.TaskInput{Delivery: delivery})
	require.NoError(t, err)
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/synthetic/automation/inputs/fixture/input.json", r.URL.Path)
		_, _ = w.Write(input)
	}))
	defer storage.Close()
	cfg := &models.Config{AutomationInputBucket: "synthetic", S3Endpoint: storage.URL, S3UsePathStyle: "true", AwsRegion: "us-east-1", S3ClientId: "test", S3ClientSecret: "test"}
	ref := "s3://synthetic/automation/inputs/fixture/input.json"
	require.NoError(t, db.Create(&models.AutomationTask{ID: taskID, JobID: uuid.Must(uuid.NewV4()), Operation: "delivery.qa", Status: "cancelled", RunID: run, LeaseExpiresAt: &expires, InputRef: ref, AgentInstanceID: &instance, AgentKey: "qa", MachineID: machine.MachineID(), DeliveryWorkItemID: &item, EvidenceSubjectDigest: matrix}).Error)
	identity := gatewayIdentity{Role: agentwork.RoleQA, Lane: agentwork.LaneQA}
	lease, err := sealGatewayLease(gatewayLease{Version: 1, Role: string(identity.Role), Lane: string(identity.Lane), TaskID: taskID.String(), InputRef: ref, ReceiptHandle: "synthetic", ExpiresAt: expires.Unix()})
	require.NoError(t, err)
	var acquisitions atomic.Int32
	var revoke atomic.Bool
	var captureMutex sync.Mutex
	var capturedBody []byte
	var capturedHeader http.Header
	app := echo.New()
	app.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			body, err := io.ReadAll(c.Request().Body)
			if err != nil {
				return err
			}
			c.Request().Body = io.NopCloser(bytes.NewReader(body))
			captureMutex.Lock()
			capturedBody = append([]byte(nil), body...)
			capturedHeader = c.Request().Header.Clone()
			captureMutex.Unlock()
			c.Set("config", cfg)
			return next(c)
		}
	})
	const path = "/api/internal/automation/gateway/qa-source"
	app.POST(path, func(c echo.Context) error {
		return gatewayQASourceWithAcquirer(c, func(_ context.Context, subject qaSourceSubject) ([]byte, string, error) {
			acquisitions.Add(1)
			require.Equal(t, qaSourceSubject{Reference: "workspace://repo", Repository: "example/service", Branch: branch, SHA: head}, subject)
			if revoke.Load() {
				require.NoError(t, db.Model(&models.AutomationAgentInstance{}).Where("id = ?", instance).Update("status", "revoked").Error)
			}
			return pack, packDigest, nil
		})
	}, AgentCallbackAuthentication)
	server := httptest.NewServer(app)
	defer server.Close()
	callback, err := automationagent.NewHTTPCallback(server.URL, machine, instance.String(), server.Client())
	require.NoError(t, err)
	gateway, err := automationagent.NewHTTPGateway(server.URL, deriveGatewayToken("synthetic-qa-source-auth-fixture", identity), identity.Role, identity.Lane, server.Client())
	require.NoError(t, err)
	clientCtx := gateway.BindMessageContext(ctx, automationagent.QueueMessage{ReceiptHandle: lease})
	qaRoot := t.TempDir()
	registry, err := json.Marshal(map[string]automationagent.WorkspaceConfig{"repo": {Path: qaRoot, RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main"}})
	require.NoError(t, err)
	lookup := func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return string(registry)
		}
		return ""
	}
	_, err = callback.AcquireQASource(clientCtx, gateway, taskID.String(), run, "workspace://repo", delivery, lookup)
	require.ErrorContains(t, err, "403")
	require.Equal(t, int32(0), acquisitions.Load())
	require.NoError(t, db.Model(&models.AutomationTask{}).Where("id = ?", taskID).Update("status", "running").Error)
	target, err := callback.AcquireQASource(clientCtx, gateway, taskID.String(), run, "workspace://repo", delivery, lookup)
	require.NoError(t, err)
	contents, err := os.ReadFile(filepath.Join(target, "README.md"))
	require.NoError(t, err)
	require.Equal(t, "exact synthetic source\n", string(contents))
	check := exec.Command("git", "-C", target, "rev-parse", "HEAD")
	actual, err := check.Output()
	require.NoError(t, err)
	require.Equal(t, head, strings.TrimSpace(string(actual)))
	require.Equal(t, int32(1), acquisitions.Load())
	var receipt models.AutomationQASourceReceipt
	require.NoError(t, db.Where("task_id = ? AND run_id = ?", taskID, run).First(&receipt).Error)
	require.Equal(t, instance, receipt.AgentInstanceID)
	require.Equal(t, matrix, receipt.MatrixDigest)
	require.Equal(t, head, receipt.CommitSHA)
	require.Equal(t, packDigest, receipt.PackSHA256)
	var currentTask models.AutomationTask
	require.NoError(t, db.First(&currentTask, taskID).Error)
	opened, err := openGatewayLease(lease, identity)
	require.NoError(t, err)
	actor := authenticatedAgentCallback{InstanceID: instance, AgentKey: "qa", MachineID: machine.MachineID()}
	subject := qaSourceSubject{Reference: "workspace://repo", Repository: "example/service", Branch: branch, SHA: head}
	require.NoError(t, recordServerQASourceReceipt(db, &currentTask, opened, identity, actor, subject, packDigest, int64(len(pack)), time.Now().UTC()), "same exact receipt should be idempotent")
	require.Error(t, recordServerQASourceReceipt(db, &currentTask, opened, identity, actor, subject, strings.Repeat("f", 64), int64(len(pack)), time.Now().UTC()), "a different pack must not replace provenance")
	observation := qaevidence.Observation{SchemaVersion: qaevidence.SchemaVersion, TaskID: taskID.String(), MatrixDigest: matrix, PreviewPassed: true, RepositoryExecutionOrder: []string{"workspace://repo"}, Repositories: []qaevidence.Repository{{Reference: "workspace://repo", Branch: branch, Commands: []qaevidence.Command{{Index: 0, Phase: "validation", Kind: "unit", Passed: true}}}}}
	observationRaw, err := json.Marshal(observation)
	require.NoError(t, err)
	require.NoError(t, verifyServerQASourceReceipts(db, &currentTask, run, instance, observationRaw))
	require.Error(t, verifyServerQASourceReceipts(db, &currentTask, uuid.Must(uuid.NewV4()).String(), instance, observationRaw), "another run cannot borrow acquisition provenance")
	require.Error(t, verifyServerQASourceReceipts(db, &currentTask, run, uuid.Must(uuid.NewV4()), observationRaw), "another instance cannot borrow acquisition provenance")
	observation.Repositories[0].Branch = "itbem-agent/" + uuid.Must(uuid.NewV4()).String()
	changedRaw, err := json.Marshal(observation)
	require.NoError(t, err)
	require.Error(t, verifyServerQASourceReceipts(db, &currentTask, run, instance, changedRaw), "callback branch must match source acquisition")
	captureMutex.Lock()
	replayBody := append([]byte(nil), capturedBody...)
	replayHeaders := capturedHeader.Clone()
	captureMutex.Unlock()
	replayed := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(replayBody))
	replayed.Header = replayHeaders
	replayResponse := httptest.NewRecorder()
	app.ServeHTTP(replayResponse, replayed)
	require.Equal(t, http.StatusUnauthorized, replayResponse.Code, "PostgreSQL nonce must reject the same signed request")
	require.Equal(t, int32(1), acquisitions.Load(), "replay must not reacquire source")
	revoke.Store(true)
	_, err = callback.AcquireQASource(clientCtx, gateway, taskID.String(), run, "workspace://repo", delivery, lookup)
	require.ErrorContains(t, err, "403")
	require.Equal(t, int32(2), acquisitions.Load())
	var receiptCount int64
	require.NoError(t, db.Model(&models.AutomationQASourceReceipt{}).Where("task_id = ?", taskID).Count(&receiptCount).Error)
	require.Equal(t, int64(1), receiptCount, "replay or revocation must not append source provenance")
	var unchangedReceipt models.AutomationQASourceReceipt
	require.NoError(t, db.First(&unchangedReceipt, receipt.ID).Error)
	require.Equal(t, receipt.PackSHA256, unchangedReceipt.PackSHA256)
	require.True(t, receipt.AcquiredAt.Equal(unchangedReceipt.AcquiredAt))
	// Source acquisition is injected only in this isolated fixture; real TLS
	// Git transport and resource limits are independently required tests.
	var nonceCount int64
	require.NoError(t, db.Model(&models.AutomationAgentCallbackNonce{}).Count(&nonceCount).Error)
	require.Equal(t, int64(3), nonceCount)
}
