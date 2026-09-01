package automationagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"events-stocks/internal/agentwork"
)

type gatewayLeaseContextKey struct{}

const gatewayMaxResponseBytes = ((maxInputBytes + 2) / 3 * 4) + (64 << 10)

type HTTPGateway struct {
	baseURL string
	token   string
	role    agentwork.Role
	lane    agentwork.Lane
	client  *http.Client
}

func NewHTTPGateway(baseURL, token string, role agentwork.Role, lane agentwork.Lane, client *http.Client) (*HTTPGateway, error) {
	if err := validateAPIBaseURL(baseURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" || !agentwork.IsKnownRoleLane(role, lane) {
		return nil, fmt.Errorf("agent gateway requires a token and exact role-lane identity")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPGateway{baseURL: strings.TrimRight(baseURL, "/"), token: strings.TrimSpace(token), role: role, lane: lane, client: client}, nil
}

func (g *HTTPGateway) request(ctx context.Context, method, path string, input any, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode agent gateway request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create agent gateway request: %w", err)
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Agent-Gateway-Token", g.token)
	req.Header.Set("X-Agent-Role", string(g.role))
	req.Header.Set("X-Agent-Lane", string(g.lane))
	response, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("agent gateway request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("agent gateway rejected request (%d)", response.StatusCode)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, gatewayMaxResponseBytes)).Decode(output); err != nil {
		return fmt.Errorf("decode agent gateway response: %w", err)
	}
	return nil
}

func (g *HTTPGateway) Probe(ctx context.Context) error {
	return g.request(ctx, http.MethodGet, "/api/internal/automation/gateway/probe", nil, &struct {
		Status int `json:"status"`
	}{})
}

func (g *HTTPGateway) Receive(ctx context.Context, limit int) ([]QueueMessage, error) {
	var response struct {
		Data struct {
			Messages []struct {
				Body       string `json:"body"`
				LeaseToken string `json:"lease_token"`
			} `json:"messages"`
		} `json:"data"`
	}
	if err := g.request(ctx, http.MethodPost, "/api/internal/automation/gateway/leases", map[string]any{"limit": limit}, &response); err != nil {
		return nil, err
	}
	messages := make([]QueueMessage, 0, len(response.Data.Messages))
	for _, message := range response.Data.Messages {
		if strings.TrimSpace(message.Body) != "" && strings.TrimSpace(message.LeaseToken) != "" {
			messages = append(messages, QueueMessage{Body: message.Body, ReceiptHandle: message.LeaseToken})
		}
	}
	return messages, nil
}

func (g *HTTPGateway) Delete(ctx context.Context, message QueueMessage) error {
	return g.request(ctx, http.MethodDelete, "/api/internal/automation/gateway/leases", map[string]any{"lease_token": message.ReceiptHandle}, nil)
}

func (g *HTTPGateway) ExtendVisibility(ctx context.Context, message QueueMessage, seconds int32) error {
	return g.changeVisibility(ctx, message, seconds)
}

func (g *HTTPGateway) Defer(ctx context.Context, message QueueMessage, seconds int32) error {
	return g.changeVisibility(ctx, message, seconds)
}

func (g *HTTPGateway) changeVisibility(ctx context.Context, message QueueMessage, seconds int32) error {
	return g.request(ctx, http.MethodPut, "/api/internal/automation/gateway/leases/visibility", map[string]any{"lease_token": message.ReceiptHandle, "seconds": seconds}, nil)
}

func gatewayLeaseFromContext(ctx context.Context) (string, error) {
	token, _ := ctx.Value(gatewayLeaseContextKey{}).(string)
	if strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("agent gateway object operation requires an active task lease")
	}
	return token, nil
}

func (g *HTTPGateway) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	lease, err := gatewayLeaseFromContext(ctx)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			Body string `json:"body"`
		} `json:"data"`
	}
	if err := g.request(ctx, http.MethodPost, "/api/internal/automation/gateway/objects/read", map[string]any{"lease_token": lease, "reference": "s3://" + bucket + "/" + key}, &response); err != nil {
		return nil, err
	}
	body, err := base64.StdEncoding.DecodeString(response.Data.Body)
	if err != nil || len(body) > maxInputBytes {
		return nil, fmt.Errorf("agent gateway returned an invalid object")
	}
	return body, nil
}

func (g *HTTPGateway) PutEncryptedJSON(ctx context.Context, bucket, key string, body []byte) error {
	return g.PutEncryptedObject(ctx, bucket, key, body, "application/json")
}

func (g *HTTPGateway) PutEncryptedObject(ctx context.Context, bucket, key string, body []byte, contentType string) error {
	lease, err := gatewayLeaseFromContext(ctx)
	if err != nil {
		return err
	}
	return g.request(ctx, http.MethodPut, "/api/internal/automation/gateway/objects/write", map[string]any{"lease_token": lease, "reference": "s3://" + bucket + "/" + key, "body": base64.StdEncoding.EncodeToString(body), "content_type": contentType}, nil)
}
