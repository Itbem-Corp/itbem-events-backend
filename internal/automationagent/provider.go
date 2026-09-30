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

	"events-stocks/internal/inferencecapability"
	"github.com/gofrs/uuid"
)

const (
	DefaultCompletionTokens   = 4096
	MinCompletionTokens       = 1
	MaxCompletionTokens       = 131072
	miniMaxM2CompletionLimit  = 2048
	miniMaxM3CompletionLimit  = 32768
	maxProviderResponseSize   = 8 << 20
	providerRetryMinDelay     = 30 * time.Second
	providerRetryDefaultDelay = 2 * time.Minute
	providerRetryMaxDelay     = 15 * time.Minute
)

const (
	// InferenceConflictHeader carries only a bounded conflict class from the
	// authenticated gateway. It never contains database, provider or lease data.
	InferenceConflictHeader = "X-ITBEM-Inference-Conflict"

	InferenceConflictCallReused             = "call_reused"
	InferenceConflictIdentityStale          = "identity_stale"
	InferenceConflictLeaseInactive          = "lease_inactive"
	InferenceConflictPolicyUnavailable      = "attempt_policy_unavailable"
	InferenceConflictPolicyScopeInvalid     = "attempt_policy_scope_invalid"
	InferenceConflictPolicyInvalid          = "attempt_policy_invalid"
	InferenceConflictPolicySignatureInvalid = "attempt_policy_signature_invalid"
	InferenceConflictPolicyRoutesMissing    = "attempt_policy_routes_missing"
	InferenceConflictPolicyRoutesInvalid    = "attempt_policy_routes_invalid"
	InferenceConflictProjectInvalid         = "project_scope_invalid"
	InferenceConflictQuotaExhausted         = "quota_exhausted"
	InferenceConflictStateUnavailable       = "state_unavailable"
)

type inferenceLeaseContextKey struct{}
type inferenceCapabilityContextKey struct{}

// InferenceLease binds a cloud-gateway call to the opaque run lease already
// accepted by the control plane. It is request metadata, never a credential.
type InferenceLease struct {
	TaskID    string
	RunID     string
	Operation string
	StepID    string
}

func WithInferenceLease(ctx context.Context, taskID, runID, operation, stepID string) context.Context {
	taskID, runID, operation = strings.TrimSpace(taskID), strings.TrimSpace(runID), strings.TrimSpace(operation)
	stepID = strings.TrimSpace(stepID)
	ctx = context.WithValue(ctx, inferenceLeaseContextKey{}, InferenceLease{TaskID: taskID, RunID: runID, Operation: operation, StepID: stepID})
	if capability, ok := inferenceCapabilityForRun(taskID, runID, time.Now().UTC()); ok {
		ctx = context.WithValue(ctx, inferenceCapabilityContextKey{}, capability)
	}
	return ctx
}

func InferenceLeaseFromContext(ctx context.Context) (InferenceLease, bool) {
	lease, ok := ctx.Value(inferenceLeaseContextKey{}).(InferenceLease)
	if !ok || lease.TaskID == "" || lease.RunID == "" || lease.Operation == "" {
		return lease, false
	}
	if lease.StepID != "" {
		stepID, err := uuid.FromString(lease.StepID)
		if err != nil || stepID == uuid.Nil || stepID.String() != lease.StepID {
			return lease, false
		}
	}
	return lease, true
}

func InferenceCapabilityFromContext(ctx context.Context) (string, bool) {
	capability, ok := ctx.Value(inferenceCapabilityContextKey{}).(string)
	return capability, ok && strings.TrimSpace(capability) != ""
}

type Provider string

const (
	ProviderMiniMax    Provider = "minimax"
	ProviderOpenAI     Provider = "openai"
	ProviderDeepSeek   Provider = "deepseek"
	ProviderOpenRouter Provider = "openrouter"
	ProviderAnthropic  Provider = "anthropic"
	// ProviderOpenCodeGo uses the OpenCode Go subscription endpoint. It is
	// intentionally distinct from OpenAI: OpenCode Go routes models across
	// Chat Completions, Responses, and Messages APIs.
	ProviderOpenCodeGo Provider = "opencode-go"
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
	CallID     string         `json:"call_id,omitempty"`
	ReceiptID  string         `json:"receipt_id,omitempty"`
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
	// StatusCode is present only for an explicit provider HTTP response. A
	// network failure remains ambiguous and must not automatically fail over to
	// another billable provider.
	StatusCode int
}

func (e *RetryableError) Error() string { return e.Message }

type ProviderConfig struct {
	Provider Provider
	Model    string
	Endpoint string
	// SessionID is a non-secret, stable per-task identifier used only by
	// OpenCode Go for routing and prompt-cache affinity.
	SessionID        string
	ReasoningEnabled bool
	ReasoningEffort  string
	secret           string
}

// String deliberately omits the provider credential so accidental structured
// logging of the in-memory cloud configuration cannot print it.
func (c ProviderConfig) String() string {
	return fmt.Sprintf("ProviderConfig{Provider:%q Model:%q CredentialConfigured:%t}", c.Provider, c.Model, c.secret != "")
}

// GoString keeps %#v diagnostics safe as well as the ordinary %v/%+v forms.
func (c ProviderConfig) GoString() string { return c.String() }

// GatewayProviderConfig contains no provider or control-plane signing secret.
// A server-issued run capability is attached to each inference context; the
// gateway obtains the actual provider key from the server-side secret bundle.
type GatewayProviderConfig struct {
	Provider Provider
	Model    string
	Endpoint string
}

// DefaultProviderEndpoint returns an operator-owned endpoint. Callers must not
// accept an endpoint from a work item or a worker request.
func DefaultProviderEndpoint(provider Provider) (string, bool) {
	switch provider {
	case ProviderMiniMax:
		return "https://api.minimax.io/v1/chat/completions", true
	case ProviderOpenAI:
		return "https://api.openai.com/v1/chat/completions", true
	case ProviderDeepSeek:
		return "https://api.deepseek.com/chat/completions", true
	case ProviderOpenRouter:
		return "https://openrouter.ai/api/v1/chat/completions", true
	case ProviderAnthropic:
		return "https://api.anthropic.com/v1/messages", true
	case ProviderOpenCodeGo:
		return "https://opencode.ai/zen/go/v1", true
	default:
		return "", false
	}
}

// NewProviderConfig builds an in-memory provider configuration for the cloud
// gateway. The credential is deliberately unexported and never serializable.
func NewProviderConfig(provider Provider, model, endpoint, secret string) (ProviderConfig, error) {
	provider = Provider(strings.ToLower(strings.TrimSpace(string(provider))))
	if _, ok := DefaultProviderEndpoint(provider); !ok {
		return ProviderConfig{}, fmt.Errorf("provider is not supported")
	}
	config := ProviderConfig{Provider: provider, Model: strings.TrimSpace(model), Endpoint: strings.TrimSpace(endpoint), secret: strings.TrimSpace(secret)}
	if config.Model == "" || len(config.Model) > 200 {
		return ProviderConfig{}, fmt.Errorf("provider model is invalid")
	}
	if provider == ProviderOpenCodeGo {
		if _, ok := OpenCodeGoModelAPI(config.Model); !ok {
			return ProviderConfig{}, fmt.Errorf("opencode-go model is not supported by a known API family")
		}
	}
	if config.secret == "" {
		return ProviderConfig{}, fmt.Errorf("provider credential is unavailable")
	}
	if err := validateProviderEndpoint(config.Endpoint); err != nil {
		return ProviderConfig{}, err
	}
	return config, nil
}

// ProviderCapabilities is the adapter contract that keeps the harness rules
// independent from a model vendor.  It is intentionally small and describes
// guarantees the runtime can enforce, not marketing features advertised by a
// provider.  A provider that cannot make one of these guarantees must fail
// admission instead of silently changing the worker's semantics.
type ProviderCapabilities struct {
	ContractVersion      int      `json:"contract_version"`
	Provider             Provider `json:"provider"`
	Model                string   `json:"model"`
	SupportsJSONActions  bool     `json:"supports_json_actions"`
	SupportsUsageLedger  bool     `json:"supports_usage_ledger"`
	SupportsCancellation bool     `json:"supports_cancellation"`
	SupportsRequestAudit bool     `json:"supports_request_audit"`
	MaxCompletionTokens  int      `json:"max_completion_tokens"`
	MaxRequestBytes      int      `json:"max_request_bytes"`
}

// ProviderCapabilityReader is implemented by real provider adapters. It is a
// separate interface so deterministic unit-test doubles remain lightweight;
// production adapters created by NewProviderClient always implement it.
type ProviderCapabilityReader interface {
	Capabilities() ProviderCapabilities
}

const providerCapabilityContractVersion = 1

// ValidateProviderCapabilities rejects a provider before a billable call when
// it cannot uphold the worker contract for the requested operation. The
// operation is recorded in the error so the dashboard can show an actionable
// admission failure without exposing credentials or provider response text.
func ValidateProviderCapabilities(capabilities ProviderCapabilities, operation string, requestedCompletionTokens int) error {
	operation = strings.TrimSpace(operation)
	if capabilities.ContractVersion != providerCapabilityContractVersion {
		return fmt.Errorf("provider capability contract version %d is unsupported", capabilities.ContractVersion)
	}
	if capabilities.Provider == "" || strings.TrimSpace(capabilities.Model) == "" {
		return fmt.Errorf("provider capability contract must identify provider and model")
	}
	if !capabilities.SupportsJSONActions {
		return fmt.Errorf("provider %s/%s cannot guarantee JSON actions for %s", capabilities.Provider, capabilities.Model, operation)
	}
	if !capabilities.SupportsUsageLedger {
		return fmt.Errorf("provider %s/%s cannot provide usage ledger metadata", capabilities.Provider, capabilities.Model)
	}
	if !capabilities.SupportsCancellation {
		return fmt.Errorf("provider %s/%s cannot honor request cancellation", capabilities.Provider, capabilities.Model)
	}
	if !capabilities.SupportsRequestAudit {
		return fmt.Errorf("provider %s/%s cannot produce a credential-free request audit", capabilities.Provider, capabilities.Model)
	}
	if capabilities.MaxCompletionTokens < MinCompletionTokens {
		return fmt.Errorf("provider %s/%s exposes no usable completion-token capacity", capabilities.Provider, capabilities.Model)
	}
	if requestedCompletionTokens < MinCompletionTokens || requestedCompletionTokens > capabilities.MaxCompletionTokens {
		return fmt.Errorf("provider %s/%s cannot satisfy %d completion tokens for %s (maximum %d)", capabilities.Provider, capabilities.Model, requestedCompletionTokens, operation, capabilities.MaxCompletionTokens)
	}
	if capabilities.MaxRequestBytes < 1 || capabilities.MaxRequestBytes > AgentMaxRequestBytes {
		return fmt.Errorf("provider %s/%s exposes an invalid request-byte bound", capabilities.Provider, capabilities.Model)
	}
	return nil
}

func validateProviderContract(provider ProviderClient, operation string, requestedCompletionTokens int, requireCapabilities bool) error {
	reader, ok := provider.(ProviderCapabilityReader)
	if !ok {
		if requireCapabilities {
			return fmt.Errorf("provider capability contract is required for runtime workers")
		}
		// Deterministic test doubles and legacy adapters are allowed to keep the
		// narrow ProviderClient surface. The built-in HTTP adapter is capability
		// aware; unknown production adapters should implement the reader before
		// being admitted to a worker.
		return nil
	}
	return ValidateProviderCapabilities(reader.Capabilities(), operation, requestedCompletionTokens)
}

func providerCapabilitiesSnapshot(provider ProviderClient) any {
	reader, ok := provider.(ProviderCapabilityReader)
	if !ok {
		return nil
	}
	return reader.Capabilities()
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
		ProviderMiniMax:    {"MINIMAX_API_KEY", "MINIMAX_MODEL", "MiniMax-M3", "MINIMAX_API_BASE_URL", "https://api.minimax.io/v1/chat/completions"},
		ProviderOpenAI:     {"OPENAI_API_KEY", "OPENAI_MODEL", "gpt-4.1-mini", "OPENAI_API_BASE_URL", "https://api.openai.com/v1/chat/completions"},
		ProviderDeepSeek:   {"DEEPSEEK_API_KEY", "DEEPSEEK_MODEL", "deepseek-flash", "DEEPSEEK_API_BASE_URL", "https://api.deepseek.com/chat/completions"},
		ProviderOpenRouter: {"OPENROUTER_API_KEY", "OPENROUTER_MODEL", "openai/gpt-4.1-mini", "OPENROUTER_API_BASE_URL", "https://openrouter.ai/api/v1/chat/completions"},
		ProviderAnthropic:  {"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "claude-sonnet-4-20250514", "ANTHROPIC_API_BASE_URL", "https://api.anthropic.com/v1/messages"},
		ProviderOpenCodeGo: {"OPENCODE_GO_API_KEY", "OPENCODE_GO_MODEL", "glm-5.3-flash", "OPENCODE_GO_API_BASE_URL", "https://opencode.ai/zen/go/v1"},
	}
	value, ok := defaults[provider]
	if !ok {
		return ProviderConfig{}, fmt.Errorf("ITBEM_AI_PROVIDER must be minimax, openai, deepseek, openrouter, anthropic, or opencode-go")
	}
	config := ProviderConfig{
		Provider: provider,
		Model:    firstNonEmpty(lookup(value.modelName), value.model),
		Endpoint: firstNonEmpty(lookup(value.endpointName), value.endpoint),
		secret:   strings.TrimSpace(lookup(value.secret)),
	}
	if config.secret == "" {
		return ProviderConfig{}, fmt.Errorf("%s is required for the local %s provider", value.secret, provider)
	}
	if len(config.Model) > 200 {
		return ProviderConfig{}, fmt.Errorf("%s must be a bounded model identifier", value.modelName)
	}
	if config.Provider == ProviderOpenCodeGo {
		if _, ok := OpenCodeGoModelAPI(config.Model); !ok {
			return ProviderConfig{}, fmt.Errorf("OPENCODE_GO_MODEL is not supported by a known API family")
		}
	}
	if err := validateProviderEndpoint(config.Endpoint); err != nil {
		return ProviderConfig{}, err
	}
	return config, nil
}

func GatewayProviderEnabled(lookup func(string) string) bool {
	if lookup == nil {
		lookup = os.Getenv
	}
	return strings.TrimSpace(lookup("ITBEM_AI_GATEWAY_URL")) != ""
}

func LoadGatewayProviderConfig(lookup func(string) string) (GatewayProviderConfig, error) {
	if lookup == nil {
		lookup = os.Getenv
	}
	provider := Provider(strings.ToLower(strings.TrimSpace(lookup("ITBEM_AI_PROVIDER"))))
	if provider == "" {
		provider = ProviderMiniMax
	}
	defaults := map[Provider]struct{ modelName, model string }{
		ProviderMiniMax:    {"MINIMAX_MODEL", "MiniMax-M3"},
		ProviderOpenAI:     {"OPENAI_MODEL", "gpt-4.1-mini"},
		ProviderDeepSeek:   {"DEEPSEEK_MODEL", "deepseek-flash"},
		ProviderOpenRouter: {"OPENROUTER_MODEL", "openai/gpt-4.1-mini"},
		ProviderAnthropic:  {"ANTHROPIC_MODEL", "claude-sonnet-4-20250514"},
		ProviderOpenCodeGo: {"OPENCODE_GO_MODEL", "glm-5.3-flash"},
	}
	value, ok := defaults[provider]
	if !ok {
		return GatewayProviderConfig{}, fmt.Errorf("ITBEM_AI_PROVIDER must be minimax, openai, deepseek, openrouter, anthropic, or opencode-go")
	}
	config := GatewayProviderConfig{
		Provider: provider, Model: firstNonEmpty(lookup(value.modelName), value.model),
		Endpoint: strings.TrimSpace(lookup("ITBEM_AI_GATEWAY_URL")),
	}
	if config.Model == "" || len(config.Model) > 200 {
		return GatewayProviderConfig{}, fmt.Errorf("AI gateway configuration is incomplete")
	}
	if config.Provider == ProviderOpenCodeGo {
		if _, ok := OpenCodeGoModelAPI(config.Model); !ok {
			return GatewayProviderConfig{}, fmt.Errorf("OPENCODE_GO_MODEL is not supported by a known API family")
		}
	}
	if err := validateProviderEndpoint(config.Endpoint); err != nil {
		return GatewayProviderConfig{}, fmt.Errorf("ITBEM_AI_GATEWAY_URL: %w", err)
	}
	return config, nil
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
	// Credentials in URLs are routinely copied into access logs, traces and
	// transport errors. Reject them (and query/fragment data, which commonly
	// carries tokens) before either the worker gateway or provider adapter can
	// issue a request.
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return fmt.Errorf("provider endpoint must not contain credentials, query parameters, or fragments")
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
	config        ProviderConfig
	client        *http.Client
	resolveLimits inferenceModelLimitsResolver
}

const providerHTTPTimeout = 120 * time.Second

// The gateway also validates identity, fetches sealed input, and records the
// receipt. Its caller must outlive the provider deadline so cancellation does
// not hide the server's terminal response and accounting outcome.
const gatewayHTTPTimeout = providerHTTPTimeout + time.Minute

func NewProviderClient(config ProviderConfig, client *http.Client) ProviderClient {
	if client == nil {
		client = &http.Client{Timeout: providerHTTPTimeout}
	} else {
		// Do not mutate a shared caller client. Provider credentials are attached
		// to every inference request, so an upstream redirect must never be
		// allowed to carry them to another URL (including a provider subdomain).
		clientCopy := *client
		client = &clientCopy
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &httpProviderClient{config: config, client: client, resolveLimits: resolveProviderModelLimits}
}

type gatewayProviderClient struct {
	config        GatewayProviderConfig
	client        *http.Client
	resolveLimits inferenceModelLimitsResolver
}

func NewGatewayProviderClient(config GatewayProviderConfig, client *http.Client) ProviderClient {
	if client == nil {
		client = &http.Client{Timeout: gatewayHTTPTimeout}
	} else {
		// Do not mutate the caller's shared HTTP client, but enforce the
		// gateway-auth boundary even when a caller supplies a client with its
		// own redirect policy. A redirect must never carry X-Automation-Secret
		// to a different endpoint.
		clientCopy := *client
		client = &clientCopy
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &gatewayProviderClient{config: config, client: client, resolveLimits: resolvePublicProviderModelLimits}
}

func (p *gatewayProviderClient) Complete(ctx context.Context, messages []Message, maxTokens int) (Completion, error) {
	if len(messages) == 0 {
		return Completion{}, fmt.Errorf("at least one provider message is required")
	}
	lease, ok := InferenceLeaseFromContext(ctx)
	if !ok {
		return Completion{}, fmt.Errorf("AI gateway execution lease is required")
	}
	capability, ok := InferenceCapabilityFromContext(ctx)
	if !ok {
		return Completion{}, fmt.Errorf("server-issued AI gateway capability is required")
	}
	limits, err := p.resolveLimits(ctx, p.config.Provider, p.config.Model, "", p.client)
	if err != nil {
		return Completion{}, errors.New("provider model limits are unavailable")
	}
	maxTokens, err = completionTokensWithinModelLimits(p.config.Provider, p.config.Model, maxTokens, limits)
	if err != nil {
		return Completion{}, err
	}
	if err := validatePromptContext(messages, maxTokens, limits); err != nil {
		return Completion{}, err
	}
	callID, err := uuid.NewV4()
	if err != nil {
		return Completion{}, fmt.Errorf("AI gateway call identity could not be created")
	}
	payload, err := json.Marshal(struct {
		CallID              string    `json:"call_id"`
		Provider            Provider  `json:"provider"`
		Model               string    `json:"model"`
		Messages            []Message `json:"messages"`
		MaxCompletionTokens int       `json:"max_completion_tokens"`
		TaskID              string    `json:"task_id"`
		RunID               string    `json:"run_id"`
		Operation           string    `json:"operation"`
		PlanStepID          string    `json:"plan_step_id,omitempty"`
	}{callID.String(), p.config.Provider, p.config.Model, messages, maxTokens, lease.TaskID, lease.RunID, lease.Operation, lease.StepID})
	if err != nil {
		return Completion{}, fmt.Errorf("AI gateway request could not be encoded")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return Completion{}, fmt.Errorf("AI gateway request could not be created")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(inferencecapability.HeaderName, capability)
	response, err := p.client.Do(request)
	if err != nil {
		// A lost gateway response cannot prove that the provider was not billed.
		// Retrying with a fresh call ID requires an explicit operator decision.
		// Never expose the transport error, which may contain private URLs.
		return Completion{}, errors.New("AI gateway transport outcome is unknown; explicit retry required")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		message := fmt.Sprintf("AI gateway temporarily unavailable (%d)", response.StatusCode)
		if reason := SafeInferenceFailureCode(response.Header.Get(InferenceFailureHeader)); reason != "" {
			message += ": " + reason
			// Unresolved provider/accounting failures may follow a request already
			// billed. A new call ID must require an explicit retry decision.
			if reason != "provider_model_limits_unavailable" && reason != "credentials_unavailable" && reason != "routing_invalid" {
				return Completion{}, fmt.Errorf("%s", message)
			}
		}
		if SafeInferenceFailureCode(response.Header.Get(InferenceFailureHeader)) == "" && response.StatusCode != http.StatusTooManyRequests {
			return Completion{}, errors.New(message)
		}
		return Completion{}, &RetryableError{Message: message, RetryAfter: providerRetryAfter(response.Header, time.Now().UTC()), StatusCode: response.StatusCode}
	}
	if response.StatusCode == http.StatusUnprocessableEntity {
		var completion Completion
		if err := json.NewDecoder(io.LimitReader(response.Body, maxProviderResponseSize)).Decode(&completion); err != nil || !providerConfigured(completion.Provider) || strings.TrimSpace(completion.Model) == "" || completion.CallID != callID.String() || !validReceiptUUID(completion.ReceiptID) || completion.Usage == nil {
			return Completion{}, fmt.Errorf("AI gateway returned an invalid billable rejection")
		}
		return completion, &ProviderResponseError{Completion: completion, Message: "AI provider response was rejected by the gateway contract"}
	}
	if response.StatusCode == http.StatusConflict {
		reason := strings.TrimSpace(response.Header.Get(InferenceConflictHeader))
		switch reason {
		case InferenceConflictCallReused, InferenceConflictIdentityStale, InferenceConflictLeaseInactive,
			InferenceConflictPolicyUnavailable, InferenceConflictPolicyScopeInvalid, InferenceConflictPolicyInvalid,
			InferenceConflictPolicySignatureInvalid, InferenceConflictPolicyRoutesMissing, InferenceConflictPolicyRoutesInvalid,
			InferenceConflictProjectInvalid, InferenceConflictQuotaExhausted,
			InferenceConflictStateUnavailable:
			return Completion{}, fmt.Errorf("AI gateway request rejected (409: %s)", reason)
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Completion{}, fmt.Errorf("AI gateway request rejected (%d)", response.StatusCode)
	}
	var completion Completion
	if err := json.NewDecoder(io.LimitReader(response.Body, maxProviderResponseSize)).Decode(&completion); err != nil || !providerConfigured(completion.Provider) || strings.TrimSpace(completion.Model) == "" || strings.TrimSpace(completion.Content) == "" || completion.CallID != callID.String() || !validReceiptUUID(completion.ReceiptID) {
		return Completion{}, fmt.Errorf("AI gateway returned an invalid completion")
	}
	return completion, nil
}

func (p *gatewayProviderClient) Capabilities() ProviderCapabilities {
	maximum, _ := boundedCompletionTokens(p.config.Provider, p.config.Model, MaxCompletionTokens)
	return ProviderCapabilities{ContractVersion: providerCapabilityContractVersion, Provider: p.config.Provider, Model: p.config.Model, SupportsJSONActions: true, SupportsUsageLedger: true, SupportsCancellation: true, SupportsRequestAudit: true, MaxCompletionTokens: maximum, MaxRequestBytes: AgentMaxRequestBytes}
}

func (p *gatewayProviderClient) AuditRequest(messages []Message, maxTokens int) (json.RawMessage, error) {
	limits, err := p.resolveLimits(context.Background(), p.config.Provider, p.config.Model, "", p.client)
	if err != nil {
		return nil, errors.New("provider model limits are unavailable")
	}
	maxTokens, err = completionTokensWithinModelLimits(p.config.Provider, p.config.Model, maxTokens, limits)
	if err != nil {
		return nil, err
	}
	if err := validatePromptContext(messages, maxTokens, limits); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"provider": p.config.Provider, "model": p.config.Model, "messages": messages, "max_completion_tokens": maxTokens})
}

func boundedCompletionTokens(provider Provider, model string, value int) (int, error) {
	if value == 0 {
		value = DefaultCompletionTokens
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

// completionTokensWithinModelLimits rejects explicit requests above either
// the provider adapter's hard ceiling or the selected model's output ceiling.
// A zero request means "use the default" and may resolve to a lower default;
// an explicit value is never silently truncated.
func completionTokensWithinModelLimits(provider Provider, model string, requested int, limits inferenceModelLimits) (int, error) {
	if limits.ContextWindowTokens < 1 || limits.MaxOutputTokens < 1 {
		return 0, errors.New("provider model limits are unavailable")
	}
	if requested < 0 {
		return 0, errors.New("provider completion-token request is invalid")
	}
	defaulted := requested == 0
	if defaulted {
		requested = DefaultCompletionTokens
	}
	maximum := min(limits.MaxOutputTokens, MaxCompletionTokens)
	if provider == ProviderMiniMax {
		adapterMaximum := miniMaxM3CompletionLimit
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "minimax-m2") {
			adapterMaximum = miniMaxM2CompletionLimit
		}
		maximum = min(maximum, adapterMaximum)
	}
	if maximum < MinCompletionTokens {
		return 0, errors.New("provider model limits are unavailable")
	}
	if defaulted && requested > maximum {
		requested = maximum
	}
	if requested < MinCompletionTokens || requested > maximum {
		return 0, errors.New("requested completion tokens exceed the selected model limit")
	}
	return requested, nil
}

// validatePromptContext uses UTF-8 byte length as a conservative upper bound
// for text tokens, plus bounded chat framing overhead. It rejects rather than
// truncates; exact provider tokenization is not available in this dependency-
// light worker package.
func validatePromptContext(messages []Message, completionTokens int, limits inferenceModelLimits) error {
	if limits.ContextWindowTokens < 1 || completionTokens < 1 || len(messages) == 0 {
		return errors.New("provider model limits are unavailable")
	}
	const (
		conversationFramingUpperBound int64 = 256
		messageFramingUpperBound      int64 = 32
	)
	inputUpperBound := conversationFramingUpperBound
	for _, message := range messages {
		contentBytes := int64(len([]byte(message.Content)))
		roleBytes := int64(len([]byte(message.Role)))
		if contentBytes > int64(limits.ContextWindowTokens)-inputUpperBound || roleBytes+messageFramingUpperBound > int64(limits.ContextWindowTokens)-inputUpperBound-contentBytes {
			return errors.New("provider request exceeds the selected model context window")
		}
		inputUpperBound += contentBytes + roleBytes + messageFramingUpperBound
	}
	if inputUpperBound+int64(completionTokens) > int64(limits.ContextWindowTokens) {
		return errors.New("provider request exceeds the selected model context window")
	}
	return nil
}

const (
	openCodeGoChatAPI      = "chat_completions"
	openCodeGoResponsesAPI = "responses"
	openCodeGoMessagesAPI  = "messages"
)

// OpenCodeGoModelAPI maps the current Go model families to their documented
// wire API. The live catalogue intentionally supplies availability only; this
// explicit routing table fails closed for a newly introduced family rather
// than sending a model request to an incompatible endpoint.
func OpenCodeGoModelAPI(model string) (string, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if base, variant, hasVariant := strings.Cut(model, "#"); hasVariant {
		if base == "" || variant == "" || strings.ContainsAny(variant, "#\t\r\n") {
			return "", false
		}
		model = base
	}
	switch {
	case strings.HasPrefix(model, "glm-"), strings.HasPrefix(model, "kimi-"), strings.HasPrefix(model, "longcat-"), strings.HasPrefix(model, "deepseek-"), strings.HasPrefix(model, "mimo-"), strings.HasPrefix(model, "space-bunny-"), strings.HasPrefix(model, "hy3"), strings.HasPrefix(model, "hy4"):
		return openCodeGoChatAPI, true
	case strings.HasPrefix(model, "gpt-"), strings.HasPrefix(model, "grok-"), strings.HasPrefix(model, "muse-"):
		return openCodeGoResponsesAPI, true
	case strings.HasPrefix(model, "minimax-"), strings.HasPrefix(model, "qwen"):
		return openCodeGoMessagesAPI, true
	default:
		return "", false
	}
}

// OpenAIModelAPI keeps direct OpenAI routing fail-closed. The public model
// listing endpoint is an availability inventory, not an API-contract list;
// embeddings, image-only and unknown future models must not be sent through a
// text inference route merely because they appear in that inventory.
func OpenAIModelAPI(model string) (string, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(model, "gpt-6"), strings.HasPrefix(model, "gpt-5"), strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"), strings.HasPrefix(model, "o4"), strings.HasPrefix(model, "computer-use"):
		return openCodeGoResponsesAPI, true
	case strings.HasPrefix(model, "gpt-4"), strings.HasPrefix(model, "gpt-3.5"):
		return openCodeGoChatAPI, true
	default:
		return "", false
	}
}

func providerRequestEndpoint(config ProviderConfig) (string, error) {
	if config.Provider == ProviderOpenAI {
		api, supported := OpenAIModelAPI(config.Model)
		if !supported {
			return "", fmt.Errorf("openai model is not supported by a known API family")
		}
		if api == openCodeGoResponsesAPI {
			base := strings.TrimSuffix(strings.TrimRight(config.Endpoint, "/"), "/chat/completions")
			return base + "/responses", nil
		}
		return config.Endpoint, nil
	}
	if config.Provider != ProviderOpenCodeGo {
		return config.Endpoint, nil
	}
	api, ok := OpenCodeGoModelAPI(config.Model)
	if !ok {
		return "", fmt.Errorf("opencode-go model is not supported by a known API family")
	}
	base := strings.TrimRight(config.Endpoint, "/")
	switch api {
	case openCodeGoChatAPI:
		return base + "/chat/completions", nil
	case openCodeGoResponsesAPI:
		return base + "/responses", nil
	case openCodeGoMessagesAPI:
		return base + "/messages", nil
	default:
		return "", fmt.Errorf("opencode-go model API family is invalid")
	}
}

func (p *httpProviderClient) Complete(ctx context.Context, messages []Message, maxTokens int) (Completion, error) {
	if len(messages) == 0 {
		return Completion{}, fmt.Errorf("at least one provider message is required")
	}
	limits, err := p.resolveLimits(ctx, p.config.Provider, p.config.Model, p.config.secret, p.client)
	if err != nil {
		return Completion{}, errors.New("provider model limits are unavailable")
	}
	maxTokens, err = completionTokensWithinModelLimits(p.config.Provider, p.config.Model, maxTokens, limits)
	if err != nil {
		return Completion{}, err
	}
	if err := validatePromptContext(messages, maxTokens, limits); err != nil {
		return Completion{}, err
	}
	payload, headers := p.payload(messages, maxTokens)
	raw, err := json.Marshal(payload)
	if err != nil {
		return Completion{}, fmt.Errorf("provider request could not be encoded")
	}
	endpoint, err := providerRequestEndpoint(p.config)
	if err != nil {
		return Completion{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
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
		return Completion{}, &RetryableError{Message: fmt.Sprintf("provider temporarily unavailable (%d)", response.StatusCode), RetryAfter: providerRetryAfter(response.Header, time.Now().UTC()), StatusCode: response.StatusCode}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Completion{}, &ProviderHTTPError{StatusCode: response.StatusCode}
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

func (p *httpProviderClient) Capabilities() ProviderCapabilities {
	maximum := MaxCompletionTokens
	if p.config.Provider == ProviderMiniMax {
		maximum = miniMaxM3CompletionLimit
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.config.Model)), "minimax-m2") {
			maximum = miniMaxM2CompletionLimit
		}
	}
	return ProviderCapabilities{
		ContractVersion:      providerCapabilityContractVersion,
		Provider:             p.config.Provider,
		Model:                p.config.Model,
		SupportsJSONActions:  true,
		SupportsUsageLedger:  true,
		SupportsCancellation: true,
		SupportsRequestAudit: true,
		MaxCompletionTokens:  maximum,
		MaxRequestBytes:      AgentMaxRequestBytes,
	}
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
	limits, err := p.resolveLimits(context.Background(), p.config.Provider, p.config.Model, p.config.secret, p.client)
	if err != nil {
		return nil, errors.New("provider model limits are unavailable")
	}
	maxTokens, err = completionTokensWithinModelLimits(p.config.Provider, p.config.Model, maxTokens, limits)
	if err != nil {
		return nil, err
	}
	if err := validatePromptContext(messages, maxTokens, limits); err != nil {
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
	if p.config.Provider == ProviderOpenAI {
		api, supported := OpenAIModelAPI(p.config.Model)
		if !supported {
			return nil, nil
		}
		headers := map[string]string{"Authorization": "Bearer " + p.config.secret, "Content-Type": "application/json"}
		if api == openCodeGoResponsesAPI {
			payload := map[string]any{"model": p.config.Model, "input": messages, "max_output_tokens": maxTokens}
			if p.config.ReasoningEnabled && IsAllowedReasoningEffort(p.config.ReasoningEffort) {
				payload["reasoning"] = map[string]string{"effort": strings.ToLower(strings.TrimSpace(p.config.ReasoningEffort))}
			}
			return payload, headers
		}
		return map[string]any{"model": p.config.Model, "messages": messages, "max_completion_tokens": maxTokens, "temperature": 0.2}, headers
	}
	if p.config.Provider == ProviderOpenCodeGo {
		api, ok := OpenCodeGoModelAPI(p.config.Model)
		if !ok {
			return nil, nil
		}
		headers := map[string]string{"Authorization": "Bearer " + p.config.secret, "Content-Type": "application/json", "User-Agent": "itbem-ai-agent/1.0"}
		if p.config.SessionID != "" {
			headers["X-OpenCode-Session"] = p.config.SessionID
		}
		if api == openCodeGoMessagesAPI {
			system, conversation := make([]string, 0), make([]Message, 0, len(messages))
			for _, message := range messages {
				switch message.Role {
				case "system":
					system = append(system, message.Content)
				case "user", "assistant":
					conversation = append(conversation, message)
				}
			}
			return map[string]any{"model": p.config.Model, "system": strings.Join(system, "\n\n"), "messages": conversation, "max_tokens": maxTokens, "temperature": 0.2}, headers
		}
		if api == openCodeGoResponsesAPI {
			return map[string]any{"model": p.config.Model, "input": messages, "max_output_tokens": maxTokens}, headers
		}
		return map[string]any{"model": p.config.Model, "messages": messages, "max_completion_tokens": maxTokens, "temperature": 0.2}, headers
	}
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
	payload := map[string]any{"model": p.config.Model, "messages": messages, "max_completion_tokens": maxTokens}
	// OpenRouter's live catalogue contains models with different optional
	// generation controls. Omitting temperature keeps a text-chat request
	// compatible with more of that catalogue while retaining each model's
	// provider-side default. Direct providers keep the explicit deterministic
	// temperature used by the existing automation flow.
	if p.config.Provider != ProviderOpenRouter {
		payload["temperature"] = 0.2
	}
	if p.config.Provider == ProviderMiniMax {
		payload["reasoning_split"] = true
		if strings.EqualFold(strings.TrimSpace(p.config.Model), "MiniMax-M3") && !p.config.ReasoningEnabled {
			payload["thinking"] = map[string]string{"type": "disabled"}
		}
	}
	if p.config.Provider == ProviderDeepSeek && p.config.ReasoningEnabled {
		payload["thinking"] = map[string]string{"type": "enabled"}
		if effort := normalizeDeepSeekReasoningEffort(p.config.ReasoningEffort); effort != "" {
			payload["reasoning_effort"] = effort
		}
	}
	if p.config.Provider == ProviderOpenRouter && p.config.ReasoningEnabled {
		if effort := normalizeOpenRouterReasoningEffort(p.config.ReasoningEffort); effort != "" {
			payload["reasoning"] = map[string]string{"effort": effort}
		}
	}
	return payload, map[string]string{"Authorization": "Bearer " + p.config.secret, "Content-Type": "application/json"}
}

// normalizeDeepSeekReasoningEffort preserves old saved policies while using
// the current API vocabulary. "medium" was accepted by earlier UI versions;
// DeepSeek now documents low, high and max.
func normalizeDeepSeekReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "medium":
		return "high"
	case "low", "high", "max":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

func normalizeOpenRouterReasoningEffort(effort string) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if IsAllowedReasoningEffort(effort) {
		return effort
	}
	return ""
}

func parseCompletion(config ProviderConfig, body map[string]any) (Completion, error) {
	completion := Completion{Provider: config.Provider, Model: stringValue(body["model"], config.Model), ResponseID: stringValue(body["id"], ""), Usage: providerUsageWithOutcome(body)}
	api, _ := OpenCodeGoModelAPI(config.Model)
	if config.Provider == ProviderAnthropic || (config.Provider == ProviderOpenCodeGo && api == openCodeGoMessagesAPI) {
		for _, block := range sliceValue(body["content"]) {
			if blockMap := mapValue(block); stringValue(blockMap["type"], "") == "text" {
				completion.Content += stringValue(blockMap["text"], "")
			}
		}
	} else if (config.Provider == ProviderOpenCodeGo && api == openCodeGoResponsesAPI) || (config.Provider == ProviderOpenAI && func() bool {
		candidate, ok := OpenAIModelAPI(config.Model)
		return ok && candidate == openCodeGoResponsesAPI
	}()) {
		completion.Content = responseText(body)
	} else if choices := sliceValue(body["choices"]); len(choices) > 0 {
		message := mapValue(mapValue(choices[0])["message"])
		completion.Content = stringValue(message["content"], "")
	}
	completion = redactCompletionCredential(completion, config.secret)
	if strings.TrimSpace(completion.Content) == "" {
		return completion, &ProviderResponseError{Completion: completion, Message: emptyCompletionMessage(config.Provider, body)}
	}
	return completion, nil
}

// redactCompletionCredential is a final output boundary: even if an upstream
// provider accidentally echoes its Authorization value, that value must not
// flow back through the gateway to a local agent or into persisted usage data.
func redactCompletionCredential(completion Completion, secret string) Completion {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return completion
	}
	completion.Model = strings.ReplaceAll(completion.Model, secret, "[REDACTED]")
	completion.ResponseID = strings.ReplaceAll(completion.ResponseID, secret, "[REDACTED]")
	completion.Content = strings.ReplaceAll(completion.Content, secret, "[REDACTED]")
	if usage, ok := redactCredentialValue(completion.Usage, secret).(map[string]any); ok {
		completion.Usage = usage
	}
	return completion
}

func redactCredentialValue(value any, secret string) any {
	switch typed := value.(type) {
	case string:
		return strings.ReplaceAll(typed, secret, "[REDACTED]")
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, nested := range typed {
			redacted[strings.ReplaceAll(key, secret, "[REDACTED]")] = redactCredentialValue(nested, secret)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, nested := range typed {
			redacted[index] = redactCredentialValue(nested, secret)
		}
		return redacted
	default:
		return value
	}
}

func responseText(body map[string]any) string {
	if text := stringValue(body["output_text"], ""); text != "" {
		return text
	}
	var text strings.Builder
	for _, output := range sliceValue(body["output"]) {
		outputMap := mapValue(output)
		for _, block := range sliceValue(outputMap["content"]) {
			blockMap := mapValue(block)
			if stringValue(blockMap["type"], "") == "output_text" || stringValue(blockMap["type"], "") == "text" {
				text.WriteString(stringValue(blockMap["text"], ""))
			}
		}
	}
	return text.String()
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
