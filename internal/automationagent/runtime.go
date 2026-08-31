package automationagent

import (
	"context"
	"events-stocks/internal/agentwork"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	defaultAgentConcurrency = 1
	maxAgentConcurrency     = 8
)

type RuntimeConfig struct {
	WorkerConfig
	QueueURL       string
	AWSRegion      string
	APIBaseURL     string
	CallbackSecret string
	Concurrency    int
	SQSEndpoint    string
	S3Endpoint     string
}

func LoadRuntimeConfig(lookup func(string) string) (RuntimeConfig, error) {
	value := func(name string) string { return strings.TrimSpace(lookup(name)) }
	concurrency := defaultAgentConcurrency
	if raw := value("ITBEM_AI_CONCURRENCY"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxAgentConcurrency {
			return RuntimeConfig{}, fmt.Errorf("ITBEM_AI_CONCURRENCY must be between 1 and %d", maxAgentConcurrency)
		}
		concurrency = parsed
	}
	config := RuntimeConfig{
		WorkerConfig: WorkerConfig{
			InputBucket: value("ITBEM_AI_INPUT_BUCKET"), OutputBucket: value("ITBEM_AI_OUTPUT_BUCKET"),
			Role: agentwork.Role(value("ITBEM_AI_ROLE")), Lane: agentwork.Lane(value("ITBEM_AI_QUEUE_LANE")),
		},
		QueueURL:       value("ITBEM_AI_QUEUE_URL"),
		AWSRegion:      value("AWS_REGION"),
		APIBaseURL:     strings.TrimRight(value("ITBEM_API_BASE_URL"), "/"),
		CallbackSecret: value("AUTOMATION_CALLBACK_SECRET"),
		Concurrency:    concurrency,
		SQSEndpoint:    value("ITBEM_AI_SQS_ENDPOINT"),
		S3Endpoint:     value("ITBEM_AI_S3_ENDPOINT"),
	}
	for name, value := range map[string]string{
		"ITBEM_AI_QUEUE_URL":         config.QueueURL,
		"AWS_REGION":                 config.AWSRegion,
		"ITBEM_API_BASE_URL":         config.APIBaseURL,
		"AUTOMATION_CALLBACK_SECRET": config.CallbackSecret,
		"ITBEM_AI_INPUT_BUCKET":      config.InputBucket,
		"ITBEM_AI_OUTPUT_BUCKET":     config.OutputBucket,
	} {
		if value == "" {
			return RuntimeConfig{}, fmt.Errorf("%s is required", name)
		}
	}
	if _, err := NewWorker(config.WorkerConfig, discardStore{}, discardCallback{}, discardProvider{}); err != nil {
		return RuntimeConfig{}, err
	}
	if err := validateQueueURL(config.QueueURL); err != nil {
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

func validateQueueURL(raw string) error {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Hostname() == "" || strings.Trim(endpoint.Path, "/") == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return fmt.Errorf("ITBEM_AI_QUEUE_URL must be an absolute queue URL")
	}
	if endpoint.Scheme == "https" || (endpoint.Scheme == "http" && isLoopbackHost(endpoint.Hostname())) {
		return nil
	}
	return fmt.Errorf("ITBEM_AI_QUEUE_URL must use HTTPS or loopback HTTP")
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
