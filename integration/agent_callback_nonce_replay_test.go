//go:build integration

package integration_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	automationCtrl "events-stocks/controllers/automation"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestAgentCallbackNoncePersistsBeforeDispatchAcrossReplicas(t *testing.T) {
	if configuration.DB == nil {
		t.Fatal("integration TestMain did not provide its disposable PostgreSQL database")
	}

	newInstance := func(agentKey string) (models.AutomationAgentInstance, ed25519.PrivateKey) {
		t.Helper()
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		encodedPublicKey, err := agentcallbackauth.EncodePublicKey(publicKey)
		require.NoError(t, err)
		fingerprint, err := agentcallbackauth.PublicKeyFingerprint(publicKey)
		require.NoError(t, err)
		now := time.Now().UTC()
		instance := models.AutomationAgentInstance{
			ID: uuid.Must(uuid.NewV4()), AgentKey: agentKey,
			MachineID: uuid.Must(uuid.NewV4()).String(), PublicKey: encodedPublicKey,
			PublicKeyFingerprint: fingerprint, Status: "active", CreatedAt: now, UpdatedAt: now,
		}
		require.NoError(t, configuration.DB.Create(&instance).Error)
		return instance, privateKey
	}

	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	instanceA, privateKeyA := newInstance("nonce-replay-a-" + suffix)
	instanceB, privateKeyB := newInstance("nonce-replay-b-" + suffix)
	t.Cleanup(func() {
		_ = configuration.DB.Unscoped().Where("instance_id IN ?", []uuid.UUID{instanceA.ID, instanceB.ID}).Delete(&models.AutomationAgentCallbackNonce{}).Error
		_ = configuration.DB.Unscoped().Where("id IN ?", []uuid.UUID{instanceA.ID, instanceB.ID}).Delete(&models.AutomationAgentInstance{}).Error
	})

	nonce := uuid.Must(uuid.NewV4())
	oldNonce := uuid.Must(uuid.NewV4())
	require.NoError(t, configuration.DB.Create(&models.AutomationAgentCallbackNonce{
		InstanceID: instanceA.ID, Nonce: oldNonce.String(), CreatedAt: time.Now().UTC().Add(-6 * time.Minute),
	}).Error)

	dispatched := make([]uuid.UUID, 0, 2)
	handler := func(c echo.Context) error {
		instanceID, err := uuid.FromString(c.Request().Header.Get(agentcallbackauth.InstanceIDHeader))
		if err != nil {
			return c.NoContent(http.StatusInternalServerError)
		}
		var count int64
		if err := configuration.DB.Model(&models.AutomationAgentCallbackNonce{}).
			Where("instance_id = ? AND nonce = ?", instanceID, c.Request().Header.Get(agentcallbackauth.NonceHeader)).
			Count(&count).Error; err != nil || count != 1 {
			return c.NoContent(http.StatusInternalServerError)
		}
		dispatched = append(dispatched, instanceID)
		return c.NoContent(http.StatusNoContent)
	}
	newReplica := func() *httptest.Server {
		t.Helper()
		server := echo.New()
		server.PUT(agentHeartbeatPath, handler, automationCtrl.AgentCallbackAuthentication)
		return httptest.NewServer(server)
	}
	replicaA := newReplica()
	defer replicaA.Close()
	replicaB := newReplica()
	defer replicaB.Close()

	body := []byte(`{"worker_id":"nonce-replay-test"}`)
	timestamp := time.Now().UTC().Unix()
	requestA := makeSignedHeartbeat(t, privateKeyA, instanceA.ID.String(), agentHeartbeatPath, agentHeartbeatPath, body, timestamp, nonce.String())
	first := performSignedHeartbeat(replicaA.Client(), replicaA.URL, requestA)
	require.NoError(t, first.err)
	require.Equal(t, http.StatusNoContent, first.status, "nonce must be durably stored before the protected handler runs")
	require.Equal(t, []uuid.UUID{instanceA.ID}, dispatched)

	var activeNonceCount, expiredNonceCount int64
	require.NoError(t, configuration.DB.Model(&models.AutomationAgentCallbackNonce{}).
		Where("instance_id = ? AND nonce = ?", instanceA.ID, nonce.String()).Count(&activeNonceCount).Error)
	require.NoError(t, configuration.DB.Model(&models.AutomationAgentCallbackNonce{}).
		Where("instance_id = ? AND nonce = ?", instanceA.ID, oldNonce.String()).Count(&expiredNonceCount).Error)
	require.EqualValues(t, 1, activeNonceCount)
	require.Zero(t, expiredNonceCount, "authenticated traffic should clean nonce records past the retention window")

	// A distinct HTTP server instance represents another API replica. It has no
	// shared in-memory replay cache; the durable database key must reject replay.
	replay := performSignedHeartbeat(replicaB.Client(), replicaB.URL, requestA)
	require.NoError(t, replay.err)
	require.Equal(t, http.StatusUnauthorized, replay.status)
	require.Equal(t, []uuid.UUID{instanceA.ID}, dispatched, "replayed request must be rejected before dispatch on the other replica")

	// The uniqueness scope is the pair (instance_id, nonce), not a global nonce
	// namespace: an independent enrolled instance can use the same nonce value.
	requestB := makeSignedHeartbeat(t, privateKeyB, instanceB.ID.String(), agentHeartbeatPath, agentHeartbeatPath, body, timestamp, nonce.String())
	independent := performSignedHeartbeat(replicaB.Client(), replicaB.URL, requestB)
	require.NoError(t, independent.err)
	require.Equal(t, http.StatusNoContent, independent.status)
	require.Equal(t, []uuid.UUID{instanceA.ID, instanceB.ID}, dispatched)
}
