package automationagent

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"events-stocks/internal/agentwork"
	"github.com/gofrs/uuid"
)

const (
	defaultAgentConcurrency = 1
	maxAgentConcurrency     = 8
)

var runtimeAgentKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

type RuntimeConfig struct {
	WorkerConfig
	Transport        string
	GatewayToken     string
	QueueURL         string
	AWSRegion        string
	APIBaseURL       string
	AgentInstanceID  string
	CallbackIdentity MachineIdentity
	Concurrency      int
	SQSEndpoint      string
	S3Endpoint       string
}

func LoadRuntimeConfig(lookup func(string) string) (RuntimeConfig, error) {
	value := func(name string) string { return strings.TrimSpace(lookup(name)) }
	var selectedTaskIDs []string
	if raw := value("ITBEM_AI_TASK_IDS"); raw != "" {
		selectedTaskIDs = strings.Split(raw, ",")
		if err := validateSelectedTaskIDs(selectedTaskIDs); err != nil {
			return RuntimeConfig{}, err
		}
	}
	concurrency := defaultAgentConcurrency
	if raw := value("ITBEM_AI_CONCURRENCY"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxAgentConcurrency {
			return RuntimeConfig{}, fmt.Errorf("ITBEM_AI_CONCURRENCY must be between 1 and %d", maxAgentConcurrency)
		}
		concurrency = parsed
	}
	capabilities, err := parseWorkerCapabilities(value("ITBEM_AI_CAPABILITIES"))
	if err != nil {
		return RuntimeConfig{}, err
	}
	agentKey := value("ITBEM_AI_AGENT_KEY")
	if agentKey == "" {
		agentKey = "generalist"
	}
	if !runtimeAgentKeyPattern.MatchString(agentKey) {
		return RuntimeConfig{}, fmt.Errorf("ITBEM_AI_AGENT_KEY must be a lowercase stable key")
	}
	identity, err := LoadLocalMachineIdentity(value("ITBEM_AI_MACHINE_ID"), value("ITBEM_AI_STATE_DIR"))
	if err != nil {
		return RuntimeConfig{}, err
	}
	instanceIDValue := value("ITBEM_AGENT_INSTANCE_ID")
	if instanceIDValue == "" {
		instanceIDValue, err = identity.RegisteredAgentInstanceID(agentKey)
		if err != nil {
			return RuntimeConfig{}, err
		}
	}
	instanceID, err := uuid.FromString(instanceIDValue)
	if err != nil || instanceID == uuid.Nil || instanceID.String() != instanceIDValue {
		return RuntimeConfig{}, fmt.Errorf("registered agent instance ID is missing or invalid; run --ensure-registered using the lane-bound gateway token")
	}
	config := RuntimeConfig{
		WorkerConfig:     WorkerConfig{InputBucket: value("ITBEM_AI_INPUT_BUCKET"), OutputBucket: value("ITBEM_AI_OUTPUT_BUCKET"), AllowedOperations: capabilities, RequireProviderCapabilities: true, AgentKey: agentKey, MachineID: identity.MachineID(), Role: agentwork.Role(value("ITBEM_AI_ROLE")), Lane: agentwork.Lane(value("ITBEM_AI_QUEUE_LANE"))},
		Transport:        strings.ToLower(value("ITBEM_AI_TRANSPORT")),
		GatewayToken:     value("ITBEM_AI_GATEWAY_TOKEN"),
		QueueURL:         value("ITBEM_AI_QUEUE_URL"),
		AWSRegion:        value("AWS_REGION"),
		APIBaseURL:       strings.TrimRight(value("ITBEM_API_BASE_URL"), "/"),
		AgentInstanceID:  instanceID.String(),
		CallbackIdentity: identity,
		Concurrency:      concurrency,
		SQSEndpoint:      value("ITBEM_AI_SQS_ENDPOINT"),
		S3Endpoint:       value("ITBEM_AI_S3_ENDPOINT"),
	}
	config.AllowedTaskIDs = selectedTaskIDs
	if config.Transport == "" {
		if config.QueueURL != "" {
			config.Transport = "aws"
		} else {
			config.Transport = "gateway"
		}
	}
	if config.Transport == "aws" {
		normalizedQueueURL, err := normalizeLocalQueueURL(config.QueueURL, config.SQSEndpoint)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("ITBEM_AI_QUEUE_URL: %w", err)
		}
		config.QueueURL = normalizedQueueURL
	}
	for name, value := range map[string]string{
		"ITBEM_API_BASE_URL":      config.APIBaseURL,
		"ITBEM_AGENT_INSTANCE_ID": config.AgentInstanceID,
		"ITBEM_AI_INPUT_BUCKET":   config.InputBucket,
		"ITBEM_AI_OUTPUT_BUCKET":  config.OutputBucket,
	} {
		if value == "" {
			return RuntimeConfig{}, fmt.Errorf("%s is required", name)
		}
	}
	switch config.Transport {
	case "aws":
		if config.QueueURL == "" || config.AWSRegion == "" {
			return RuntimeConfig{}, fmt.Errorf("ITBEM_AI_QUEUE_URL and AWS_REGION are required for aws transport")
		}
	case "gateway":
		if config.GatewayToken == "" {
			return RuntimeConfig{}, fmt.Errorf("ITBEM_AI_GATEWAY_TOKEN is required for gateway transport")
		}
		if !agentwork.IsKnownRoleLane(config.Role, config.Lane) {
			return RuntimeConfig{}, fmt.Errorf("gateway transport requires an exact registered ITBEM_AI_ROLE and ITBEM_AI_QUEUE_LANE")
		}
	default:
		return RuntimeConfig{}, fmt.Errorf("ITBEM_AI_TRANSPORT must be gateway or aws")
	}
	if _, err := NewWorker(config.WorkerConfig, discardStore{}, discardCallback{}, discardProvider{}); err != nil {
		return RuntimeConfig{}, err
	}
	if err := validateAPIBaseURL(config.APIBaseURL); err != nil {
		return RuntimeConfig{}, err
	}
	if err := validateLocalEndpoint(config.SQSEndpoint); err != nil {
		return RuntimeConfig{}, fmt.Errorf("ITBEM_AI_SQS_ENDPOINT: %w", err)
	}
	if err := validateLocalEndpoint(config.S3Endpoint); err != nil {
		return RuntimeConfig{}, fmt.Errorf("ITBEM_AI_S3_ENDPOINT: %w", err)
	}
	return config, nil
}

func validateSelectedTaskIDs(taskIDs []string) error {
	if len(taskIDs) > 60 {
		return fmt.Errorf("ITBEM_AI_TASK_IDS must contain at most 60 unique canonical UUIDs")
	}
	seen := make(map[string]bool, len(taskIDs))
	for _, taskID := range taskIDs {
		parsed, err := uuid.FromString(taskID)
		if err != nil || parsed == uuid.Nil || parsed.String() != taskID || seen[taskID] {
			return fmt.Errorf("ITBEM_AI_TASK_IDS must contain unique canonical nonzero UUIDs")
		}
		seen[taskID] = true
	}
	return nil
}

// EnsureGatewayAgentInstance registers a new machine on first start by using
// the same role/lane-bound gateway token the worker already needs. AWS-direct
// migration workers retain their existing administrator-enrollment path.
func EnsureGatewayAgentInstance(ctx context.Context, lookup func(string) string) (string, error) {
	value := func(name string) string {
		if lookup == nil {
			return ""
		}
		return strings.TrimSpace(lookup(name))
	}
	transport := strings.ToLower(value("ITBEM_AI_TRANSPORT"))
	if transport == "" {
		if value("ITBEM_AI_QUEUE_URL") != "" {
			transport = "aws"
		} else {
			transport = "gateway"
		}
	}
	if transport == "aws" {
		return "", nil
	}
	if transport != "gateway" {
		return "", fmt.Errorf("ITBEM_AI_TRANSPORT must be gateway or aws")
	}
	agentKey := value("ITBEM_AI_AGENT_KEY")
	if agentKey == "" {
		agentKey = "generalist"
	}
	if !runtimeAgentKeyPattern.MatchString(agentKey) {
		return "", fmt.Errorf("ITBEM_AI_AGENT_KEY must be a lowercase stable key")
	}
	role := agentwork.Role(value("ITBEM_AI_ROLE"))
	lane := agentwork.Lane(value("ITBEM_AI_QUEUE_LANE"))
	apiBaseURL := strings.TrimRight(value("ITBEM_API_BASE_URL"), "/")
	if err := validateAPIBaseURL(apiBaseURL); err != nil {
		return "", err
	}
	if !agentwork.IsKnownRoleLane(role, lane) {
		return "", fmt.Errorf("gateway auto-enrollment requires an exact registered ITBEM_AI_ROLE and ITBEM_AI_QUEUE_LANE")
	}
	identity, err := LoadLocalMachineIdentity(value("ITBEM_AI_MACHINE_ID"), value("ITBEM_AI_STATE_DIR"))
	if err != nil {
		return "", err
	}
	gateway, err := NewHTTPGateway(apiBaseURL, value("ITBEM_AI_GATEWAY_TOKEN"), role, lane, nil)
	if err != nil {
		return "", err
	}
	instanceID, err := gateway.EnrollAgentInstance(ctx, agentKey, identity)
	if err != nil {
		return "", err
	}
	configuredID := value("ITBEM_AGENT_INSTANCE_ID")
	if configuredID != "" && configuredID != instanceID {
		return "", fmt.Errorf("configured instance ID does not match the authenticated control-plane enrollment")
	}
	if err := identity.StoreRegisteredAgentInstanceID(agentKey, instanceID); err != nil {
		return "", err
	}
	return instanceID, nil
}

func parseWorkerCapabilities(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	capabilities := make([]string, 0, len(parts))
	for _, part := range parts {
		capabilities = append(capabilities, strings.TrimSpace(part))
	}
	if err := validateWorkerCapabilities(capabilities); err != nil {
		return nil, fmt.Errorf("ITBEM_AI_CAPABILITIES: %w", err)
	}
	return capabilities, nil
}

func validateAPIBaseURL(raw string) error {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Hostname() == "" {
		return fmt.Errorf("ITBEM_API_BASE_URL must be an absolute URL")
	}
	if endpoint.Scheme == "https" || (endpoint.Scheme == "http" && isLoopbackHost(endpoint.Hostname())) {
		return nil
	}
	return fmt.Errorf("ITBEM_API_BASE_URL must use HTTPS or loopback HTTP")
}

func validateLocalEndpoint(raw string) error {
	if raw == "" {
		return nil
	}
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "http" || !isLoopbackHost(endpoint.Hostname()) {
		return fmt.Errorf("must be an HTTP loopback endpoint")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

type discardStore struct{}

func (discardStore) Get(context.Context, string, string) ([]byte, error)            { return nil, nil }
func (discardStore) PutEncryptedJSON(context.Context, string, string, []byte) error { return nil }

type discardCallback struct{}

func (discardCallback) Update(context.Context, string, TaskUpdate) (bool, error) { return true, nil }

type discardProvider struct{}

func (discardProvider) Complete(context.Context, []Message, int) (Completion, error) {
	return Completion{}, nil
}
