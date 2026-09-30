package automationagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/inferencecapability"
	"github.com/gofrs/uuid"
)

const retryReservationHeader = "X-ITBEM-Automation-Retry-Reservation"
const retryLeaseHeader = "X-ITBEM-Automation-Retry-Lease"

// Only a rejected claim for a currently owned execution carries this signal.
// It must retain the queue message: the owner may still fail before producing
// a durable result. Terminal/cancelled task conflicts remain safe to ACK.
const runBusyHeader = "X-ITBEM-Automation-Run-Busy"

const maxCallbackRejectionBodyBytes = 4096

func callbackBusyRetryAfter(header http.Header) time.Duration {
	seconds, err := strconv.ParseInt(strings.TrimSpace(header.Get(retryLeaseHeader)), 10, 64)
	maximumSeconds := int64(providerRetryMaxDelay / time.Second)
	if err != nil || seconds < 1 || seconds > maximumSeconds {
		return providerRetryMaxDelay
	}
	delay := time.Duration(seconds) * time.Second
	if delay < providerRetryMinDelay {
		return providerRetryMinDelay
	}
	return delay
}

func safeCallbackRejectionReason(reader io.Reader) string {
	if reader == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxCallbackRejectionBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxCallbackRejectionBodyBytes {
		return ""
	}
	var response struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil {
		return ""
	}
	switch strings.TrimSpace(response.Error) {
	case "running tasks require a valid run lease ID":
		return "run lease ID is invalid"
	case "a worker identity is required":
		return "worker identity is missing"
	case "worker identity or profile is invalid":
		return "worker identity or profile is invalid"
	case "progress label is invalid":
		return "progress label is invalid"
	case "recovery_run_id must identify a prior immutable run":
		return "recovery run ID is invalid"
	case "a recovery run may only complete a stored result":
		return "recovery cannot report a running status"
	case "recovery evidence must match the original private run":
		return "recovery evidence does not match the original run"
	case "worker identity is invalid for this run":
		return "worker identity is invalid for this run"
	case "output_ref must be an ITBEM private object reference":
		return "output reference is invalid"
	case "request_ref must be this execution's private request object":
		return "request reference does not match this run"
	case "status must be completed or failed":
		return "terminal callback status is invalid"
	case "deterministic tasks cannot report an inference receipt":
		return "deterministic callback includes an inference receipt"
	case "only onboarding probes, delivery publication, or release Gatekeeper may be deterministic":
		return "deterministic callback is not allowed for this operation"
	case "remote code review completion requires exact GitHub publication evidence":
		return "code review publication evidence is missing"
	case "only a bounded completed review or delivery execution may register execution metadata":
		return "execution metadata is invalid for this status"
	}
	switch strings.TrimSpace(response.Message) {
	case "Invalid automation claim":
		return "automation claim is invalid"
	case "Invalid automation result":
		return "automation result payload is invalid"
	default:
		return ""
	}
}

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
			return false, &RetryableError{Message: "automation run is owned by another worker; retaining delivery for recovery", RetryAfter: callbackBusyRetryAfter(response.Header)}
		}
		return false, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if reason := safeCallbackRejectionReason(response.Body); reason != "" {
			return false, fmt.Errorf("automation callback rejected (%d: %s)", response.StatusCode, reason)
		}
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
