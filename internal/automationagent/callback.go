package automationagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"events-stocks/internal/agentwork"
)

const retryReservationHeader = "X-ITBEM-Automation-Retry-Reservation"

type HTTPCallback struct {
	baseURL string
	secret  string
	client  *http.Client
	role    string
	lane    string
}

func (c *HTTPCallback) BindIdentity(role agentwork.Role, lane agentwork.Lane) {
	c.role, c.lane = string(role), string(lane)
}

// AgentHeartbeat is intentionally metadata-only. Worker liveness must not
// reveal host names, queue addresses, prompts, outputs or credentials. Its
// workspace readiness projection contains only safe booleans and counts, so
// operators can see whether a live worker can actually run Delivery work.
type AgentHeartbeat struct {
	WorkerID           string               `json:"worker_id"`
	Provider           string               `json:"provider"`
	Model              string               `json:"model"`
	Role               string               `json:"role,omitempty"`
	Lane               string               `json:"lane,omitempty"`
	Concurrency        int                  `json:"concurrency"`
	StartedAt          string               `json:"started_at"`
	WorkspaceReadiness []WorkspaceReadiness `json:"workspace_readiness,omitempty"`
}

func NewHTTPCallback(baseURL, secret string, client *http.Client) (*HTTPCallback, error) {
	if err := validateAPIBaseURL(baseURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("automation callback secret is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &HTTPCallback{baseURL: strings.TrimRight(baseURL, "/"), secret: secret, client: client}, nil
}

func (c *HTTPCallback) Update(ctx context.Context, taskID string, update TaskUpdate) (bool, error) {
	if strings.TrimSpace(taskID) == "" {
		return false, fmt.Errorf("automation task ID is required")
	}
	body, err := json.Marshal(update)
	if err != nil {
		return false, fmt.Errorf("automation callback body could not be encoded")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/api/internal/automation/tasks/"+taskID, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("automation callback request could not be created")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Automation-Secret", c.secret)
	if c.role != "" && c.lane != "" {
		req.Header.Set("X-Agent-Role", c.role)
		req.Header.Set("X-Agent-Lane", c.lane)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("automation callback request failed")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		if response.Header.Get(retryReservationHeader) == "1" {
			return false, &RetryableError{Message: "automation budget reservation expired; awaiting a renewed lease"}
		}
		return false, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, fmt.Errorf("automation callback rejected (%d)", response.StatusCode)
	}
	return true, nil
}

func (c *HTTPCallback) Heartbeat(ctx context.Context, heartbeat AgentHeartbeat) error {
	body, err := json.Marshal(heartbeat)
	if err != nil {
		return fmt.Errorf("automation heartbeat body could not be encoded")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/api/internal/automation/agents/heartbeat", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("automation heartbeat request could not be created")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Automation-Secret", c.secret)
	if c.role != "" && c.lane != "" {
		req.Header.Set("X-Agent-Role", c.role)
		req.Header.Set("X-Agent-Lane", c.lane)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("automation heartbeat request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("automation heartbeat rejected (%d)", response.StatusCode)
	}
	return nil
}
