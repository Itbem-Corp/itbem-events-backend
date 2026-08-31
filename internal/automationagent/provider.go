// Package automationagent contains the isolated, local ITBEM AI worker.
// It deliberately has no dependency on HTTP handlers, GORM or product data.
package automationagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultCompletionTokens   = 4096
	MinCompletionTokens       = 1
	MaxCompletionTokens       = 131072
	miniMaxM2CompletionLimit  = 2048
	miniMaxM3CompletionLimit  = 32768
	maxProviderResponseSize   = 8 << 20
	providerRequestMinTimeout = 30 * time.Second
	providerRequestTimeout    = 10 * time.Minute
	providerRequestMaxTimeout = 15 * time.Minute
	providerRetryMinDelay     = 30 * time.Second
	providerRetryDefaultDelay = 2 * time.Minute
	providerRetryMaxDelay     = 15 * time.Minute
	providerAuthProbeTimeout  = 8 * time.Second
	maxProviderProbeBodySize  = 64 << 10
)

type Provider string

const (
	ProviderMiniMax   Provider = "minimax"
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Completion struct {
	Provider   Provider       `json:"provider"`
	Content    string         `json:"content"`
	ResponseID string         `json:"response_id"`
	Usage      map[string]any `json:"usage"`
	Model      string         `json:"model"`
}

// ProviderResponseError retains the billable, provider-authenticated response
// metadata when the transport succeeded but the response cannot be used as an
// assistant answer. Callers must persist this through the private execution
// ledger instead of silently discarding usage or response identity.
type ProviderResponseError struct {
	Completion Completion
	Message    string
}

func (e *ProviderResponseError) Error() string { return e.Message }

// RetryableError tells the SQS loop to retain a message for another lease.
// It never contains provider response bodies, prompts or credentials.
type RetryableError struct {
	Message    string
	RetryAfter time.Duration
}

func (e *RetryableError) Error() string { return e.Message }

type ProviderConfig struct {
	Provider       Provider
	Model          string
	Endpoint       string
	secret         string
	requestTimeout time.Duration
}

// ProviderAuthProbe is credential-free evidence about one read-only provider
// authentication check. It deliberately excludes response bodies, account
// balances, quota values, request headers and credential material.
type ProviderAuthProbe struct {
	Ready               bool     `json:"ready"`
	Status              string   `json:"status"`
	Provider            Provider `json:"provider"`
	ConfiguredRegion    string   `json:"configured_region,omitempty"`
	DetectedRegion      string   `json:"detected_region,omitempty"`
	RecommendedEndpoint string   `json:"recommended_endpoint,omitempty"`
	NetworkChecksMade   bool     `json:"network_checks_made"`
	Billable            bool     `json:"billable"`
}

func (c ProviderConfig) SecretConfigured() bool { return c.secret != "" }

func LoadProviderConfig(lookup func(string) string) (ProviderConfig, error) {
	if lookup == nil {
		lookup = os.Getenv
	}
	provider := Provider(strings.ToLower(strings.TrimSpace(lookup("ITBEM_AI_PROVIDER"))))
	if provider == "" {
		provider = ProviderMiniMax
	}
	defaults := map[Provider]struct{ secret, modelName, model, endpointName, endpoint string }{
		ProviderMiniMax:   {"MINIMAX_API_KEY", "MINIMAX_MODEL", "MiniMax-M3", "MINIMAX_API_BASE_URL", "https://api.minimax.io/v1/chat/completions"},
		ProviderOpenAI:    {"OPENAI_API_KEY", "OPENAI_MODEL", "gpt-4.1-mini", "OPENAI_API_BASE_URL", "https://api.openai.com/v1/chat/completions"},
		ProviderAnthropic: {"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "claude-sonnet-4-20250514", "ANTHROPIC_API_BASE_URL", "https://api.anthropic.com/v1/messages"},
	}
	value, ok := defaults[provider]
	if !ok {
		return ProviderConfig{}, fmt.Errorf("ITBEM_AI_PROVIDER must be minimax, openai, or anthropic")
	}
	requestTimeout, err := configuredProviderRequestTimeout(lookup("ITBEM_AI_PROVIDER_TIMEOUT_SECONDS"))
	if err != nil {
		return ProviderConfig{}, err
	}
	config := ProviderConfig{
		Provider:       provider,
		Model:          firstNonEmpty(lookup(value.modelName), value.model),
		Endpoint:       firstNonEmpty(lookup(value.endpointName), value.endpoint),
		secret:         strings.TrimSpace(lookup(value.secret)),
		requestTimeout: requestTimeout,
	}
	if config.secret == "" {
		return ProviderConfig{}, fmt.Errorf("%s is required for the local %s provider", value.secret, provider)
	}
	if len(config.Model) > 200 {
		return ProviderConfig{}, fmt.Errorf("%s must be a bounded model identifier", value.modelName)
	}
	if err := validateProviderEndpoint(config.Endpoint); err != nil {
		return ProviderConfig{}, err
	}
	return config, nil
}

func configuredProviderRequestTimeout(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return providerRequestTimeout, nil
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("ITBEM_AI_PROVIDER_TIMEOUT_SECONDS must be an integer")
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout < providerRequestMinTimeout || timeout > providerRequestMaxTimeout {
		return 0, fmt.Errorf("ITBEM_AI_PROVIDER_TIMEOUT_SECONDS must be between %d and %d", int(providerRequestMinTimeout/time.Second), int(providerRequestMaxTimeout/time.Second))
	}
	return timeout, nil
}

func firstNonEmpty(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func validateProviderEndpoint(raw string) error {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || endpoint.Hostname() == "" {
		return fmt.Errorf("provider endpoint must be an absolute HTTPS URL or loopback HTTP test endpoint")
	}
	if endpoint.Scheme == "https" {
		return nil
	}
	if endpoint.Scheme == "http" {
		host := endpoint.Hostname()
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
	}
	return fmt.Errorf("provider endpoint must use HTTPS or loopback HTTP")
}

type ProviderClient interface {
	Complete(context.Context, []Message, int) (Completion, error)
}

// ProviderRequestAuditor is optionally implemented by a provider adapter that
// can serialize the exact credential-free request body it will send. The
// execution ledger uses it for private audit evidence; it must never return
// authorization headers, API keys or an endpoint containing credentials.
type ProviderRequestAuditor interface {
	AuditRequest([]Message, int) (json.RawMessage, error)
}

type httpProviderClient struct {
	config ProviderConfig
	client *http.Client
}

func NewProviderClient(config ProviderConfig, client *http.Client) ProviderClient {
	if client == nil {
		timeout := config.requestTimeout
		if timeout < providerRequestMinTimeout || timeout > providerRequestMaxTimeout {
			timeout = providerRequestTimeout
		}
		client = &http.Client{Timeout: timeout}
	}
	return &httpProviderClient{config: config, client: client}
}

type providerProbeAttempt struct {
	authorized   bool
	unauthorized bool
	unreachable  bool
}

// ProbeProviderAuth verifies that the configured credential is accepted by a
// provider's read-only metadata/quota endpoint. It never sends a prompt or
// creates a completion. MiniMax keys are checked using the same regional,
// dual-header contract as the official CLI so a valid CN key is not mistaken
// for an invalid Global key.
func ProbeProviderAuth(ctx context.Context, config ProviderConfig, client *http.Client) (ProviderAuthProbe, error) {
	result := ProviderAuthProbe{Provider: config.Provider, Status: "inconclusive", NetworkChecksMade: true, Billable: false}
	if !config.SecretConfigured() {
		result.Status = "not_configured"
		return result, nil
	}
	if client == nil {
		client = &http.Client{Timeout: providerAuthProbeTimeout}
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Hostname() == "" {
		return result, fmt.Errorf("provider authentication probe requires a valid configured endpoint")
	}
	baseURL := endpoint.Scheme + "://" + endpoint.Host

	switch config.Provider {
	case ProviderMiniMax:
		regions := []struct {
			name string
			base string
		}{}
		configuredRegion := "custom"
		switch strings.ToLower(endpoint.Hostname()) {
		case "api.minimax.io":
			configuredRegion = "global"
			regions = append(regions, struct{ name, base string }{"global", "https://api.minimax.io"}, struct{ name, base string }{"cn", "https://api.minimaxi.com"})
		case "api.minimaxi.com":
			configuredRegion = "cn"
			regions = append(regions, struct{ name, base string }{"cn", "https://api.minimaxi.com"}, struct{ name, base string }{"global", "https://api.minimax.io"})
		default:
			regions = append(regions, struct{ name, base string }{"custom", baseURL})
		}
		result.ConfiguredRegion = configuredRegion
		sawUnauthorized, sawUnreachable := false, false
		for _, region := range regions {
			attempt := probeMiniMaxAuth(ctx, client, region.base, config.secret)
			sawUnauthorized = sawUnauthorized || attempt.unauthorized
			sawUnreachable = sawUnreachable || attempt.unreachable
			if !attempt.authorized {
				continue
			}
			result.DetectedRegion = region.name
			if region.name == configuredRegion || region.name == "custom" {
				result.Ready, result.Status = true, "authenticated"
				return result, nil
			}
			result.Status = "region_mismatch"
			result.RecommendedEndpoint = region.base + "/v1/chat/completions"
			return result, nil
		}
		if sawUnauthorized {
			result.Status = "rejected"
		} else if sawUnreachable {
			result.Status = "unreachable"
		}
		return result, nil
	case ProviderOpenAI:
		attempt := probeSimpleProviderAuth(ctx, client, providerMetadataEndpoint(endpoint, "/chat/completions"), "Authorization", "Bearer "+config.secret, "")
		return finishSimpleProviderProbe(result, attempt), nil
	case ProviderAnthropic:
		attempt := probeSimpleProviderAuth(ctx, client, providerMetadataEndpoint(endpoint, "/messages"), "x-api-key", config.secret, "2023-06-01")
		return finishSimpleProviderProbe(result, attempt), nil
	default:
		return result, fmt.Errorf("provider authentication probe is unsupported")
	}
}

func providerMetadataEndpoint(configured *url.URL, completionSuffix string) string {
	probe := *configured
	probe.RawQuery, probe.Fragment = "", ""
	path := strings.TrimSuffix(strings.TrimRight(probe.Path, "/"), completionSuffix)
	probe.Path = strings.TrimRight(path, "/") + "/models"
	return probe.String()
}

func finishSimpleProviderProbe(result ProviderAuthProbe, attempt providerProbeAttempt) ProviderAuthProbe {
	switch {
	case attempt.authorized:
		result.Ready, result.Status = true, "authenticated"
	case attempt.unauthorized:
		result.Status = "rejected"
	case attempt.unreachable:
		result.Status = "unreachable"
	}
	return result
}

func probeMiniMaxAuth(ctx context.Context, client *http.Client, baseURL, secret string) providerProbeAttempt {
	path := "/v1/token_plan/remains"
	if strings.HasPrefix(secret, "sk-api-") {
		path = "/account/query_balance"
	}
	combined := providerProbeAttempt{}
	for _, auth := range []struct{ name, value string }{{"Authorization", "Bearer " + secret}, {"x-api-key", secret}} {
		attempt := probeSimpleProviderAuth(ctx, client, baseURL+path, auth.name, auth.value, "")
		combined.unauthorized = combined.unauthorized || attempt.unauthorized
		combined.unreachable = combined.unreachable || attempt.unreachable
		if attempt.authorized {
			return attempt
		}
		if !attempt.unauthorized && !attempt.unreachable {
			combined.unauthorized = false
		}
	}
	return combined
}

func probeSimpleProviderAuth(ctx context.Context, client *http.Client, endpoint, header, value, anthropicVersion string) providerProbeAttempt {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return providerProbeAttempt{}
	}
	req.Header.Set(header, value)
	req.Header.Set("Accept", "application/json")
	if anthropicVersion != "" {
		req.Header.Set("anthropic-version", anthropicVersion)
	}
	response, err := client.Do(req)
	if err != nil {
		return providerProbeAttempt{unreachable: true}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return providerProbeAttempt{unauthorized: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxProviderProbeBodySize))
		return providerProbeAttempt{}
	}
	if strings.Contains(endpoint, "minimax") || strings.Contains(endpoint, "minimaxi") || strings.Contains(endpoint, "/token_plan/") || strings.Contains(endpoint, "/account/query_balance") {
		var payload struct {
			BaseResponse struct {
				StatusCode int `json:"status_code"`
			} `json:"base_resp"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, maxProviderProbeBodySize)).Decode(&payload); err != nil || payload.BaseResponse.StatusCode != 0 {
			return providerProbeAttempt{unauthorized: payload.BaseResponse.StatusCode == 1004 || payload.BaseResponse.StatusCode == 2049}
		}
		return providerProbeAttempt{authorized: true}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxProviderProbeBodySize))
	return providerProbeAttempt{authorized: true}
}

func boundedCompletionTokens(provider Provider, model string, value int) (int, error) {
	if value == 0 {
		return DefaultCompletionTokens, nil
	}
	if value < 0 {
		return 0, fmt.Errorf("max completion tokens must not be negative")
	}
	if value < MinCompletionTokens {
		return MinCompletionTokens, nil
	}
	maximum := MaxCompletionTokens
	if provider == ProviderMiniMax {
		maximum = miniMaxM3CompletionLimit
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "minimax-m2") {
			maximum = miniMaxM2CompletionLimit
		}
	}
	if value > maximum {
		return maximum, nil
	}
	return value, nil
}

func (p *httpProviderClient) Complete(ctx context.Context, messages []Message, maxTokens int) (Completion, error) {
	if len(messages) == 0 {
		return Completion{}, fmt.Errorf("at least one provider message is required")
	}
	maxTokens, err := boundedCompletionTokens(p.config.Provider, p.config.Model, maxTokens)
	if err != nil {
		return Completion{}, err
	}
	payload, headers := p.payload(messages, maxTokens)
	raw, err := json.Marshal(payload)
	if err != nil {
		return Completion{}, fmt.Errorf("provider request could not be encoded")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return Completion{}, fmt.Errorf("provider request could not be created")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response, err := p.client.Do(req)
	if err != nil {
		if isNetworkError(err) {
			return Completion{}, &RetryableError{Message: "provider network request failed", RetryAfter: providerRetryDefaultDelay}
		}
		return Completion{}, fmt.Errorf("provider request failed")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		return Completion{}, &RetryableError{Message: fmt.Sprintf("provider temporarily unavailable (%d)", response.StatusCode), RetryAfter: providerRetryAfter(response.Header, time.Now().UTC())}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Completion{}, fmt.Errorf("provider request rejected (%d)", response.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, maxProviderResponseSize)).Decode(&body); err != nil {
		return Completion{}, fmt.Errorf("provider returned invalid JSON")
	}
	completion, err := parseCompletion(p.config, body)
	if err != nil {
		// Keep the billable response metadata for callers that need to record a
		// private terminal execution (for example, an empty safety-filtered
		// answer). Successful callers still receive the same completion value.
		return completion, err
	}
	return completion, nil
}

// providerRetryAfter treats the provider's retry hint as an upper-level
// scheduling input, never as a command. Malformed, stale, tiny or excessive
// values fall inside a bounded SQS visibility window so a transient provider
// failure cannot become a hot retry loop or a silently abandoned task.
func providerRetryAfter(headers http.Header, now time.Time) time.Duration {
	delay := providerRetryDefaultDelay
	value := strings.TrimSpace(headers.Get("Retry-After"))
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		delay = time.Duration(seconds) * time.Second
	} else if retryAt, err := http.ParseTime(value); err == nil {
		delay = retryAt.Sub(now.UTC())
	}
	if delay < providerRetryMinDelay {
		return providerRetryMinDelay
	}
	if delay > providerRetryMaxDelay {
		return providerRetryMaxDelay
	}
	return delay
}

// AuditRequest returns the same provider request body Complete will use after
// applying the same token clamp. It is deliberately separate from transport:
// the encrypted execution evidence contains no headers or secrets.
func (p *httpProviderClient) AuditRequest(messages []Message, maxTokens int) (json.RawMessage, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("at least one provider message is required")
	}
	maxTokens, err := boundedCompletionTokens(p.config.Provider, p.config.Model, maxTokens)
	if err != nil {
		return nil, err
	}
	payload, _ := p.payload(messages, maxTokens)
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("provider request could not be encoded")
	}
	return raw, nil
}

func (p *httpProviderClient) payload(messages []Message, maxTokens int) (map[string]any, map[string]string) {
	if p.config.Provider == ProviderAnthropic {
		system, conversation := make([]string, 0), make([]Message, 0, len(messages))
		for _, message := range messages {
			switch message.Role {
			case "system":
				system = append(system, message.Content)
			case "user", "assistant":
				conversation = append(conversation, message)
			}
		}
		return map[string]any{"model": p.config.Model, "system": strings.Join(system, "\n\n"), "messages": conversation, "max_tokens": maxTokens, "temperature": 0.2}, map[string]string{"x-api-key": p.config.secret, "anthropic-version": "2023-06-01", "content-type": "application/json"}
	}
	payload := map[string]any{"model": p.config.Model, "messages": messages, "max_completion_tokens": maxTokens, "temperature": 0.2}
	if p.config.Provider == ProviderMiniMax {
		payload["reasoning_split"] = true
	}
	return payload, map[string]string{"Authorization": "Bearer " + p.config.secret, "Content-Type": "application/json"}
}

func parseCompletion(config ProviderConfig, body map[string]any) (Completion, error) {
	completion := Completion{Provider: config.Provider, Model: stringValue(body["model"], config.Model), ResponseID: stringValue(body["id"], ""), Usage: providerUsageWithOutcome(body)}
	if config.Provider == ProviderAnthropic {
		for _, block := range sliceValue(body["content"]) {
			if blockMap := mapValue(block); stringValue(blockMap["type"], "") == "text" {
				completion.Content += stringValue(blockMap["text"], "")
			}
		}
	} else if choices := sliceValue(body["choices"]); len(choices) > 0 {
		message := mapValue(mapValue(choices[0])["message"])
		completion.Content = stringValue(message["content"], "")
	}
	if strings.TrimSpace(completion.Content) == "" {
		return completion, &ProviderResponseError{Completion: completion, Message: emptyCompletionMessage(config.Provider, body)}
	}
	return completion, nil
}

// providerUsageWithOutcome keeps usage provider-native while attaching a
// small non-sensitive outcome envelope. It allows operators to distinguish a
// normal completion from a filtered or truncated one without persisting model
// reasoning, headers, prompts, or provider error prose.
func providerUsageWithOutcome(body map[string]any) map[string]any {
	usage := make(map[string]any, len(mapValue(body["usage"]))+1)
	for key, value := range mapValue(body["usage"]) {
		usage[key] = value
	}
	metadata := map[string]any{}
	if choices := sliceValue(body["choices"]); len(choices) > 0 {
		if finishReason := strings.TrimSpace(stringAny(mapValue(choices[0])["finish_reason"])); finishReason != "" && len(finishReason) <= 64 {
			metadata["finish_reason"] = finishReason
		}
	}
	if inputSensitive, ok := body["input_sensitive"].(bool); ok {
		metadata["input_sensitive"] = inputSensitive
	}
	if outputSensitive, ok := body["output_sensitive"].(bool); ok {
		metadata["output_sensitive"] = outputSensitive
	}
	if statusCode := boundedProviderStatusCode(mapValue(body["base_resp"])["status_code"]); statusCode != nil {
		metadata["status_code"] = *statusCode
	}
	if len(metadata) > 0 {
		usage["_itbem_provider"] = metadata
	}
	return usage
}

func boundedProviderStatusCode(value any) *int64 {
	switch code := value.(type) {
	case float64:
		if code >= 0 && code <= 999999 && code == float64(int64(code)) {
			result := int64(code)
			return &result
		}
	case int:
		if code >= 0 && code <= 999999 {
			result := int64(code)
			return &result
		}
	case int64:
		if code >= 0 && code <= 999999 {
			return &code
		}
	}
	return nil
}

// emptyCompletionMessage intentionally exposes only bounded provider state.
// It helps an operator distinguish a policy/sensitivity rejection from an
// otherwise malformed empty answer without leaking prompts or response bodies.
func emptyCompletionMessage(provider Provider, body map[string]any) string {
	if provider != ProviderMiniMax {
		return fmt.Sprintf("%s returned no assistant content", provider)
	}
	inputSensitive, _ := body["input_sensitive"].(bool)
	outputSensitive, _ := body["output_sensitive"].(bool)
	statusCode := strings.TrimSpace(fmt.Sprint(mapValue(body["base_resp"])["status_code"]))
	if inputSensitive || outputSensitive {
		return "minimax returned an empty response because its safety filter was triggered"
	}
	if statusCode != "" && statusCode != "0" && statusCode != "<nil>" {
		return "minimax returned an empty response with a provider status"
	}
	return "minimax returned no assistant content"
}

func stringValue(value any, fallback string) string {
	if result, ok := value.(string); ok && result != "" {
		return result
	}
	return fallback
}

func mapValue(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return map[string]any{}
}

func sliceValue(value any) []any {
	if result, ok := value.([]any); ok {
		return result
	}
	return nil
}

func isNetworkError(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) || errors.Is(err, context.DeadlineExceeded)
}
