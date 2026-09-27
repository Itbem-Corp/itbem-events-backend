//go:build integration

package integration_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"events-stocks/configuration"
	automationCtrl "events-stocks/controllers/automation"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

const agentHeartbeatPath = "/api/internal/automation/agents/heartbeat"

type callbackIdentityRegistration struct {
	ID        string `json:"id"`
	AgentKey  string `json:"agent_key"`
	MachineID string `json:"machine_id"`
	Status    string `json:"status"`
}

type callbackIdentityResponse struct {
	Data struct {
		Instance callbackIdentityRegistration `json:"instance"`
	} `json:"data"`
}

type signedHeartbeat struct {
	method     string
	actualURI  string
	body       []byte
	instanceID string
	timestamp  int64
	nonce      string
	signature  string
}

func TestAgentCallbackIdentityRoundTrip(t *testing.T) {
	if configuration.DB == nil {
		t.Fatal("integration TestMain did not provide its disposable PostgreSQL database")
	}
	// Ensure this test never silently falls back to the retired shared-secret
	// callback path. No legacy secret is sent on any request.
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "")

	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	agentKey := "callback-qa-" + suffix
	profile := models.AutomationAgentProfile{
		AgentKey:         agentKey,
		Name:             "Callback identity integration test",
		Specialty:        "integration test only",
		OperationsJSON:   `[]`,
		CapabilitiesJSON: `[]`,
		Active:           true,
	}
	require.NoError(t, configuration.DB.Create(&profile).Error)
	var registeredIDs []string
	var workerIDs []string
	t.Cleanup(func() {
		if len(registeredIDs) > 0 {
			_ = configuration.DB.Unscoped().Where("instance_id IN ?", registeredIDs).Delete(&models.AutomationAgentCallbackNonce{}).Error
		}
		if len(workerIDs) > 0 {
			_ = configuration.DB.Unscoped().Where("worker_id IN ?", workerIDs).Delete(&models.AutomationAgentHeartbeat{}).Error
		}
		_ = configuration.DB.Unscoped().Where("agent_key = ?", agentKey).Delete(&models.AutomationAgentHeartbeat{}).Error
		_ = configuration.DB.Unscoped().Where("agent_key = ?", agentKey).Delete(&models.AutomationAgentInstance{}).Error
		_ = configuration.DB.Unscoped().Where("id = ?", profile.ID).Delete(&models.AutomationAgentProfile{}).Error
	})

	rootSubject := "callback-identity-integration-root-" + suffix
	restoreHooks := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(subject string) (*models.User, error) {
		if subject != rootSubject {
			return nil, io.ErrUnexpectedEOF
		}
		return &models.User{CognitoSub: rootSubject, IsRoot: true, RootLevel: models.RootLevelPrimary, IsActive: true}, nil
	}})
	t.Cleanup(restoreHooks)

	e := echo.New()
	withRootPrincipal := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("cognito_sub", rootSubject)
			c.Set("tenant_code", "itbem")
			c.Set("workspace_mode", "platform")
			return next(c)
		}
	}
	e.POST("/api/automation/agent-instances", automationCtrl.RegisterAgentInstance, withRootPrincipal)
	e.GET("/api/automation/agent-instances", automationCtrl.ListAgentInstances, withRootPrincipal)
	e.DELETE("/api/automation/agent-instances/:id", automationCtrl.RevokeAgentInstance, withRootPrincipal)
	e.PUT(agentHeartbeatPath, automationCtrl.AgentHeartbeat, automationCtrl.AgentCallbackAuthentication)
	server := httptest.NewServer(e)
	t.Cleanup(server.Close)
	client := server.Client()
	newHeartbeatBody := func(bodyAgentKey, bodyMachineID string) []byte {
		workerID := uuid.Must(uuid.NewV4()).String()
		workerIDs = append(workerIDs, workerID)
		return makeHeartbeatBody(t, workerID, bodyAgentKey, bodyMachineID)
	}

	publicKeyA, privateKeyA, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	publicKeyB, privateKeyB, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	publicKeyTextA, err := agentcallbackauth.EncodePublicKey(publicKeyA)
	require.NoError(t, err)
	publicKeyTextB, err := agentcallbackauth.EncodePublicKey(publicKeyB)
	require.NoError(t, err)
	machineIDA := uuid.Must(uuid.NewV4()).String()
	machineIDB := uuid.Must(uuid.NewV4()).String()
	instanceA := registerCallbackIdentity(t, client, server.URL, agentKey, machineIDA, publicKeyTextA)
	registeredIDs = append(registeredIDs, instanceA.ID)
	instanceB := registerCallbackIdentity(t, client, server.URL, agentKey, machineIDB, publicKeyTextB)
	registeredIDs = append(registeredIDs, instanceB.ID)
	require.NotEqual(t, instanceA.ID, instanceB.ID)
	require.Equal(t, "active", instanceA.Status)
	require.Equal(t, machineIDA, instanceA.MachineID)

	// The enrollment/list APIs return only the public identity projection, not
	// the enrolled public key or any private key material.
	listResponse := sendJSON(t, client, http.MethodGet, server.URL+"/api/automation/agent-instances?agent_key="+agentKey, nil, nil)
	require.Equal(t, http.StatusOK, listResponse.StatusCode)
	listBody, err := io.ReadAll(listResponse.Body)
	require.NoError(t, err)
	require.NoError(t, listResponse.Body.Close())
	require.NotContains(t, string(listBody), publicKeyTextA)
	require.NotContains(t, string(listBody), publicKeyTextB)

	validBody := newHeartbeatBody("", "")
	valid := makeSignedHeartbeat(t, privateKeyA, instanceA.ID, agentHeartbeatPath, agentHeartbeatPath, validBody, time.Now().Unix(), uuid.Must(uuid.NewV4()).String())
	validResponse := sendSignedHeartbeat(t, client, server.URL, valid)
	assertSuccessfulCallback(t, validResponse)

	var stored models.AutomationAgentHeartbeat
	require.NoError(t, configuration.DB.Where("worker_id = ?", bodyWorkerID(t, validBody)).First(&stored).Error)
	require.Equal(t, agentKey, stored.AgentKey, "heartbeat identity must match the enrolled agent profile")
	require.Equal(t, machineIDA, stored.MachineID, "heartbeat identity must match the enrolled machine, not an arbitrary callback body")

	t.Run("signature and request binding", func(t *testing.T) {
		body := newHeartbeatBody(agentKey, machineIDA)
		t.Run("signature altered", func(t *testing.T) {
			request := makeSignedHeartbeat(t, privateKeyA, instanceA.ID, agentHeartbeatPath, agentHeartbeatPath, body, time.Now().Unix(), uuid.Must(uuid.NewV4()).String())
			signatureBytes, decodeErr := agentcallbackauth.DecodeSignature(request.signature)
			require.NoError(t, decodeErr)
			signatureBytes[0] ^= 0x01
			request.signature, err = agentcallbackauth.EncodeSignature(signatureBytes)
			require.NoError(t, err)
			assertRejectedCallback(t, sendSignedHeartbeat(t, client, server.URL, request))
		})
		t.Run("body altered after signing", func(t *testing.T) {
			request := makeSignedHeartbeat(t, privateKeyA, instanceA.ID, agentHeartbeatPath, agentHeartbeatPath, body, time.Now().Unix(), uuid.Must(uuid.NewV4()).String())
			request.body = append(append([]byte(nil), request.body...), ' ')
			assertRejectedCallback(t, sendSignedHeartbeat(t, client, server.URL, request))
		})
		t.Run("path differs from signed URI", func(t *testing.T) {
			request := makeSignedHeartbeat(t, privateKeyA, instanceA.ID, agentHeartbeatPath+"?signed=elsewhere", agentHeartbeatPath, body, time.Now().Unix(), uuid.Must(uuid.NewV4()).String())
			assertRejectedCallback(t, sendSignedHeartbeat(t, client, server.URL, request))
		})
		t.Run("timestamp outside window", func(t *testing.T) {
			request := makeSignedHeartbeat(t, privateKeyA, instanceA.ID, agentHeartbeatPath, agentHeartbeatPath, body, time.Now().Add(-2*time.Minute).Unix(), uuid.Must(uuid.NewV4()).String())
			assertRejectedCallback(t, sendSignedHeartbeat(t, client, server.URL, request))
		})
	})

	for _, mismatch := range []struct {
		name      string
		agentKey  string
		machineID string
	}{
		{name: "agent key mismatch", agentKey: "forged-agent", machineID: machineIDA},
		{name: "machine id mismatch", agentKey: agentKey, machineID: uuid.Must(uuid.NewV4()).String()},
	} {
		t.Run("identity mismatch: "+mismatch.name, func(t *testing.T) {
			body := newHeartbeatBody(mismatch.agentKey, mismatch.machineID)
			request := makeSignedHeartbeat(t, privateKeyA, instanceA.ID, agentHeartbeatPath, agentHeartbeatPath, body, time.Now().Unix(), uuid.Must(uuid.NewV4()).String())
			response := performSignedHeartbeat(client, server.URL, request)
			require.NoError(t, response.err)
			require.Equal(t, http.StatusForbidden, response.status, "a registered instance cannot claim a different profile or machine")
			var count int64
			mismatchWorkerID := bodyWorkerID(t, body)
			require.NoError(t, configuration.DB.Model(&models.AutomationAgentHeartbeat{}).Where("worker_id = ?", mismatchWorkerID).Count(&count).Error)
			require.Zero(t, count, "a rejected identity mismatch must have no heartbeat side effect")
			var errorEnvelope struct {
				Status int `json:"status"`
			}
			require.NoError(t, json.Unmarshal([]byte(response.body), &errorEnvelope), "identity rejection must produce one response object")
			require.Equal(t, http.StatusForbidden, errorEnvelope.Status)
		})
	}

	t.Run("nonce replay is rejected", func(t *testing.T) {
		body := newHeartbeatBody(agentKey, machineIDA)
		request := makeSignedHeartbeat(t, privateKeyA, instanceA.ID, agentHeartbeatPath, agentHeartbeatPath, body, time.Now().Unix(), uuid.Must(uuid.NewV4()).String())
		first := sendSignedHeartbeat(t, client, server.URL, request)
		assertSuccessfulCallback(t, first)
		second := sendSignedHeartbeat(t, client, server.URL, request)
		assertRejectedCallback(t, second)
	})

	t.Run("concurrent nonce replay has exactly one winner", func(t *testing.T) {
		body := newHeartbeatBody(agentKey, machineIDA)
		request := makeSignedHeartbeat(t, privateKeyA, instanceA.ID, agentHeartbeatPath, agentHeartbeatPath, body, time.Now().Unix(), uuid.Must(uuid.NewV4()).String())
		start := make(chan struct{})
		type result struct {
			status int
			err    error
		}
		statuses := make(chan result, 2)
		var wait sync.WaitGroup
		for range 2 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				response := performSignedHeartbeat(client, server.URL, request)
				statuses <- result{status: response.status, err: response.err}
			}()
		}
		close(start)
		wait.Wait()
		close(statuses)
		var successes, rejected int
		for response := range statuses {
			require.NoError(t, response.err)
			if response.status >= http.StatusOK && response.status < http.StatusMultipleChoices {
				successes++
			} else {
				rejected++
			}
		}
		require.Equal(t, 1, successes, "the unique nonce constraint must allow exactly one concurrent request")
		require.Equal(t, 1, rejected, "the duplicate nonce must be rejected across concurrent requests")
	})

	t.Run("instance id cannot be substituted", func(t *testing.T) {
		body := newHeartbeatBody(agentKey, machineIDA)
		request := makeSignedHeartbeat(t, privateKeyA, instanceA.ID, agentHeartbeatPath, agentHeartbeatPath, body, time.Now().Unix(), uuid.Must(uuid.NewV4()).String())
		request.instanceID = instanceB.ID
		assertRejectedCallback(t, sendSignedHeartbeat(t, client, server.URL, request))
	})

	revokeResponse := sendJSON(t, client, http.MethodDelete, server.URL+"/api/automation/agent-instances/"+instanceB.ID, nil, nil)
	require.Equal(t, http.StatusOK, revokeResponse.StatusCode)
	require.NoError(t, revokeResponse.Body.Close())
	revokedBody := newHeartbeatBody(agentKey, machineIDB)
	revokedRequest := makeSignedHeartbeat(t, privateKeyB, instanceB.ID, agentHeartbeatPath, agentHeartbeatPath, revokedBody, time.Now().Unix(), uuid.Must(uuid.NewV4()).String())
	assertRejectedCallback(t, sendSignedHeartbeat(t, client, server.URL, revokedRequest))
}

func registerCallbackIdentity(t *testing.T, client *http.Client, serverURL, agentKey, machineID, publicKey string) callbackIdentityRegistration {
	t.Helper()
	body, err := json.Marshal(map[string]string{"agent_key": agentKey, "machine_id": machineID, "public_key": publicKey})
	require.NoError(t, err)
	response := sendJSON(t, client, http.MethodPost, serverURL+"/api/automation/agent-instances", body, nil)
	responseBody, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equalf(t, http.StatusOK, response.StatusCode, "instance registration failed: %s", string(responseBody))
	var envelope callbackIdentityResponse
	require.NoError(t, json.Unmarshal(responseBody, &envelope))
	registered := envelope.Data.Instance
	parsed, err := uuid.FromString(registered.ID)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, parsed)
	require.Equal(t, agentKey, registered.AgentKey)
	require.Equal(t, machineID, registered.MachineID)
	return registered
}

func makeHeartbeatBody(t *testing.T, workerID, agentKey, machineID string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"worker_id":           workerID,
		"agent_key":           agentKey,
		"machine_id":          machineID,
		"provider":            "openrouter",
		"model":               "synthetic-callback-identity-test",
		"concurrency":         2,
		"draining":            false,
		"capabilities":        []string{},
		"started_at":          time.Now().UTC().Format(time.RFC3339),
		"workspace_readiness": []any{},
	})
	require.NoError(t, err)
	return body
}

func makeSignedHeartbeat(t *testing.T, privateKey ed25519.PrivateKey, instanceID, signedURI, actualURI string, body []byte, timestamp int64, nonce string) signedHeartbeat {
	t.Helper()
	signature, err := agentcallbackauth.SignRequest(privateKey, instanceID, http.MethodPut, signedURI, timestamp, nonce, body)
	require.NoError(t, err)
	encodedSignature, err := agentcallbackauth.EncodeSignature(signature)
	require.NoError(t, err)
	return signedHeartbeat{method: http.MethodPut, actualURI: actualURI, body: append([]byte(nil), body...), instanceID: instanceID, timestamp: timestamp, nonce: nonce, signature: encodedSignature}
}

func sendSignedHeartbeat(t *testing.T, client *http.Client, serverURL string, callback signedHeartbeat) int {
	t.Helper()
	response := performSignedHeartbeat(client, serverURL, callback)
	require.NoError(t, response.err)
	return response.status
}

type signedHeartbeatResponse struct {
	status int
	body   string
	err    error
}

func performSignedHeartbeat(client *http.Client, serverURL string, callback signedHeartbeat) signedHeartbeatResponse {
	request, err := http.NewRequest(callback.method, serverURL+callback.actualURI, bytes.NewReader(callback.body))
	if err != nil {
		return signedHeartbeatResponse{err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(agentcallbackauth.InstanceIDHeader, callback.instanceID)
	request.Header.Set(agentcallbackauth.TimestampHeader, strconv.FormatInt(callback.timestamp, 10))
	request.Header.Set(agentcallbackauth.NonceHeader, callback.nonce)
	request.Header.Set(agentcallbackauth.SignatureHeader, callback.signature)
	response, err := client.Do(request)
	if err != nil {
		return signedHeartbeatResponse{err: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return signedHeartbeatResponse{err: err}
	}
	return signedHeartbeatResponse{status: response.StatusCode, body: string(body)}
}

func sendJSON(t *testing.T, client *http.Client, method, url string, body []byte, headers http.Header) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	return response
}

func assertSuccessfulCallback(t *testing.T, status int) {
	t.Helper()
	require.GreaterOrEqual(t, status, http.StatusOK)
	require.Less(t, status, http.StatusMultipleChoices)
}

func assertRejectedCallback(t *testing.T, status int) {
	t.Helper()
	require.GreaterOrEqual(t, status, http.StatusMultipleChoices, "invalid callback must not be accepted")
}

func bodyWorkerID(t *testing.T, body []byte) string {
	t.Helper()
	var request struct {
		WorkerID string `json:"worker_id"`
	}
	require.NoError(t, json.Unmarshal(body, &request))
	return request.WorkerID
}
