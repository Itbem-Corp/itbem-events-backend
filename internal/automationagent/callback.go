package automationagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/inferencecapability"
	"github.com/gofrs/uuid"
)

const retryReservationHeader = "X-ITBEM-Automation-Retry-Reservation"

// Only a rejected claim for a currently owned execution carries this signal.
// It must retain the queue message: the owner may still fail before producing
// a durable result. Terminal/cancelled task conflicts remain safe to ACK.
const runBusyHeader = "X-ITBEM-Automation-Run-Busy"

type inferenceCapabilityKey struct {
	taskID, runID string
}

type inferenceCapabilityEntry struct {
	token     string
	expiresAt time.Time
}

var runInferenceCapabilities = struct {
	sync.Mutex
	values map[inferenceCapabilityKey]inferenceCapabilityEntry
}{values: make(map[inferenceCapabilityKey]inferenceCapabilityEntry)}

func storeInferenceCapability(taskID, runID, token string, now time.Time) error {
	key := inferenceCapabilityKey{strings.TrimSpace(taskID), strings.TrimSpace(runID)}
	token = strings.TrimSpace(token)
	if key.taskID == "" || key.runID == "" || len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, "\r\n\t ") {
		return fmt.Errorf("claim response did not include a valid inference capability")
	}
	now = now.UTC()
	runInferenceCapabilities.Lock()
	defer runInferenceCapabilities.Unlock()
	for existingKey, entry := range runInferenceCapabilities.values {
		if !now.Before(entry.expiresAt) {
			delete(runInferenceCapabilities.values, existingKey)
		}
	}
	runInferenceCapabilities.values[key] = inferenceCapabilityEntry{token: token, expiresAt: now.Add(inferencecapability.MaxTTL)}
	return nil
}

func inferenceCapabilityForRun(taskID, runID string, now time.Time) (string, bool) {
	key := inferenceCapabilityKey{strings.TrimSpace(taskID), strings.TrimSpace(runID)}
	now = now.UTC()
	runInferenceCapabilities.Lock()
	defer runInferenceCapabilities.Unlock()
	entry, exists := runInferenceCapabilities.values[key]
	if !exists {
		return "", false
	}
	if !now.Before(entry.expiresAt) {
		delete(runInferenceCapabilities.values, key)
		return "", false
	}
	return entry.token, true
}

func clearInferenceCapabilitiesForRun(taskID, runID string) {
	taskID, runID = strings.TrimSpace(taskID), strings.TrimSpace(runID)
	runInferenceCapabilities.Lock()
	defer runInferenceCapabilities.Unlock()
	for key := range runInferenceCapabilities.values {
		if key.taskID == taskID && key.runID == runID {
			delete(runInferenceCapabilities.values, key)
		}
	}
}

type HTTPCallback struct {
	baseURL    string
	identity   MachineIdentity
	instanceID string
	client     *http.Client
}

// HeartbeatRejectionError reports only the HTTP status returned for a signed
// liveness request. Response bodies are deliberately not read or retained so
// they cannot leak credentials or server diagnostics into worker logs.
type HeartbeatRejectionError struct {
	StatusCode int
}

func (err *HeartbeatRejectionError) Error() string {
	if err == nil {
		return "automation heartbeat rejected"
	}
	return fmt.Sprintf("automation heartbeat rejected (%d)", err.StatusCode)
}

// AgentHeartbeat is intentionally metadata-only. Worker liveness must not
// reveal host names, queue addresses, prompts, outputs or credentials. Its
// workspace readiness projection contains only safe booleans and counts, so
// operators can see whether a live worker can actually run Delivery work.
type AgentHeartbeat struct {
	WorkerID           string               `json:"worker_id"`
	AgentKey           string               `json:"agent_key"`
	MachineID          string               `json:"machine_id,omitempty"`
	Provider           string               `json:"provider"`
	Model              string               `json:"model"`
	Concurrency        int                  `json:"concurrency"`
	Capabilities       []string             `json:"capabilities,omitempty"`
	Protocols          []string             `json:"protocols,omitempty"`
	StartedAt          string               `json:"started_at"`
	Draining           bool                 `json:"draining,omitempty"`
	WorkspaceReadiness []WorkspaceReadiness `json:"workspace_readiness,omitempty"`
}

func NewHTTPCallback(baseURL string, identity MachineIdentity, instanceID string, client *http.Client) (*HTTPCallback, error) {
	if err := validateAPIBaseURL(baseURL); err != nil {
		return nil, err
	}
	parsedInstanceID, err := uuid.FromString(strings.TrimSpace(instanceID))
	parsedMachineID, machineIDErr := uuid.FromString(strings.TrimSpace(identity.machineID))
	if err != nil || parsedInstanceID == uuid.Nil || machineIDErr != nil || parsedMachineID == uuid.Nil || len(identity.privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("registered machine callback identity is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	// Never follow a redirect: the destination must not receive a request signed
	// for a different authority or path.
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client = &clientCopy
	identity.machineID = parsedMachineID.String()
	return &HTTPCallback{baseURL: strings.TrimRight(baseURL, "/"), identity: identity, instanceID: parsedInstanceID.String(), client: client}, nil
}

func (c *HTTPCallback) newSignedRequest(ctx context.Context, method, target string, body []byte) (*http.Request, error) {
	if c == nil || c.client == nil || len(c.identity.privateKey) != ed25519.PrivateKeySize || c.instanceID == "" {
		return nil, fmt.Errorf("registered machine callback identity is unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("automation callback request could not be created")
	}
	timestamp := time.Now().UTC().Unix()
	nonce, err := uuid.NewV4()
	if err != nil {
		return nil, fmt.Errorf("automation callback request identity could not be created")
	}
	signature, err := agentcallbackauth.SignRequest(c.identity.privateKey, c.instanceID, req.Method, req.URL.RequestURI(), timestamp, nonce.String(), body)
	if err != nil {
		return nil, fmt.Errorf("automation callback request could not be signed")
	}
	encodedSignature, err := agentcallbackauth.EncodeSignature(signature)
	if err != nil {
		return nil, fmt.Errorf("automation callback request could not be signed")
	}
	req.Header.Set(agentcallbackauth.InstanceIDHeader, c.instanceID)
	req.Header.Set(agentcallbackauth.TimestampHeader, fmt.Sprintf("%d", timestamp))
	req.Header.Set(agentcallbackauth.NonceHeader, nonce.String())
	req.Header.Set(agentcallbackauth.SignatureHeader, encodedSignature)
	return req, nil
}

func (c *HTTPCallback) Update(ctx context.Context, taskID string, update TaskUpdate) (bool, error) {
	if strings.TrimSpace(taskID) == "" {
		return false, fmt.Errorf("automation task ID is required")
	}
	body, err := json.Marshal(update)
	if err != nil {
		return false, fmt.Errorf("automation callback body could not be encoded")
	}
	req, err := c.newSignedRequest(ctx, http.MethodPut, c.baseURL+"/api/internal/automation/tasks/"+taskID, body)
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("automation callback request failed")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		if response.Header.Get(retryReservationHeader) == "1" {
			return false, &RetryableError{Message: "automation budget reservation expired; awaiting a renewed lease"}
		}
		if update.Status == "running" && response.Header.Get(runBusyHeader) == "1" {
			return false, &RetryableError{Message: "automation run is owned by another worker; retaining delivery for recovery", RetryAfter: providerRetryMaxDelay}
		}
		return false, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, fmt.Errorf("automation callback rejected (%d)", response.StatusCode)
	}
	if update.Status == "running" {
		if err := storeInferenceCapability(taskID, update.RunID, response.Header.Get(inferencecapability.HeaderName), time.Now().UTC()); err != nil {
			// The callback must not be treated as a usable inference lease unless
			// the server returned its independently signed, run-scoped capability.
			return false, err
		}
	} else if update.RunID != "" {
		clearInferenceCapabilitiesForRun(taskID, update.RunID)
	}
	return true, nil
}

func (c *HTTPCallback) Heartbeat(ctx context.Context, heartbeat AgentHeartbeat) error {
	body, err := json.Marshal(heartbeat)
	if err != nil {
		return fmt.Errorf("automation heartbeat body could not be encoded")
	}
	req, err := c.newSignedRequest(ctx, http.MethodPut, c.baseURL+"/api/internal/automation/agents/heartbeat", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("automation heartbeat request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HeartbeatRejectionError{StatusCode: response.StatusCode}
	}
	return nil
}
