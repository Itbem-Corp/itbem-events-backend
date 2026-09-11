package automationagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"events-stocks/internal/agentwork"
)

type gatewayLeaseContextKey struct{}

const gatewayMaxResponseBytes = ((maxInputBytes + 2) / 3 * 4) + (64 << 10)

const (
	gatewayRetryMinimumDelay = time.Second
	gatewayRetryDefaultDelay = 5 * time.Second
	gatewayRetryMaximumDelay = time.Minute
)

// gatewayRequestError preserves the status boundary between the local worker
// and its control plane. Only transport failures and explicitly transient HTTP
// responses return a positive RetryDelay; authentication, authorization and
// request validation errors intentionally remain terminal to the worker
// process.
type gatewayRequestError struct {
	statusCode int
	cause      error
	retryAfter time.Duration
}

func (e *gatewayRequestError) Error() string {
	if e.statusCode != 0 {
		return fmt.Sprintf("agent gateway rejected request (%d)", e.statusCode)
	}
	return fmt.Sprintf("agent gateway request failed: %v", e.cause)
}

func (e *gatewayRequestError) Unwrap() error { return e.cause }

// gatewayRejectedError preserves a non-retryable HTTP status for callers that
// must make a narrowly scoped decision about a denied capability. It is kept
// separate from gatewayRequestError so an authorization or validation failure
// can never enter the queue retry path.
type gatewayRejectedError struct{ statusCode int }

func (e *gatewayRejectedError) Error() string {
	return fmt.Sprintf("agent gateway rejected request (%d)", e.statusCode)
}

type gatewayStatusError interface{ GatewayStatusCode() int }

func (e *gatewayRequestError) GatewayStatusCode() int { return e.statusCode }

func (e *gatewayRejectedError) GatewayStatusCode() int { return e.statusCode }

func gatewayResponseStatus(err error) (int, bool) {
	var statusError gatewayStatusError
	if !errors.As(err, &statusError) || statusError.GatewayStatusCode() < 100 {
		return 0, false
	}
	return statusError.GatewayStatusCode(), true
}

// RetryDelay is deliberately absent from permanent gateway errors. RunQueue
// uses this small interface instead of treating every receive error as safe to
// retry, so a revoked token or an invalid lane cannot spin silently forever.
func (e *gatewayRequestError) RetryDelay() time.Duration {
	if !gatewayResponseIsTransient(e.statusCode) {
		return 0
	}
	if e.retryAfter < gatewayRetryMinimumDelay {
		return gatewayRetryMinimumDelay
	}
	if e.retryAfter > gatewayRetryMaximumDelay {
		return gatewayRetryMaximumDelay
	}
	return e.retryAfter
}

func gatewayResponseIsTransient(statusCode int) bool {
	return statusCode == 0 || statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooManyRequests || statusCode >= 500
}

func gatewayRetryAfter(headers http.Header, now time.Time) time.Duration {
	delay := gatewayRetryDefaultDelay
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return delay
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(raw); err == nil {
		return deadline.Sub(now)
	}
	return delay
}

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
		if ctx.Err() == nil {
			return &gatewayRequestError{cause: err, retryAfter: gatewayRetryDefaultDelay}
		}
		return fmt.Errorf("agent gateway request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if gatewayResponseIsTransient(response.StatusCode) || response.StatusCode == http.StatusNotFound {
			return &gatewayRequestError{statusCode: response.StatusCode, retryAfter: gatewayRetryAfter(response.Header, time.Now().UTC())}
		}
		return &gatewayRejectedError{statusCode: response.StatusCode}
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
		var gatewayErr *gatewayRequestError
		if errors.As(err, &gatewayErr) && gatewayErr.statusCode == http.StatusNotFound {
			return nil, ErrObjectNotFound
		}
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
