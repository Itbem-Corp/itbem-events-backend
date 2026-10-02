//go:build integration

package automation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/agentwork"
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
	require.NoError(t, db.AutoMigrate(&models.AutomationTask{}, &models.AutomationAgentInstance{}, &models.AutomationAgentCallbackNonce{}))
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
	app := echo.New()
	app.POST(path, GatewayReleaseObservation, AgentCallbackAuthentication)
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
		return response.Code
	}
	require.Equal(t, http.StatusUnauthorized, call(append(append([]byte{}, body...), byte(' '))), "body mutation must fail signature before admission")
	require.Equal(t, http.StatusForbidden, call(body), "authenticated cancellation must fail before storage or GitHub reads")
	require.Equal(t, http.StatusUnauthorized, call(body), "durable nonce must deny replay")
	var count int64
	require.NoError(t, db.Model(&models.AutomationAgentCallbackNonce{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}
