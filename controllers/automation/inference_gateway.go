package automation

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/internal/aicredentials"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/utils"
	"io"
	"net/http"
	"strings"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

const maxInferenceGatewayRequestBytes = 512 << 10

// The adapter below avoids making the controller depend on AWS. It is assigned
// once from the composition root and stays nil when this environment has not
// opted into the central credential bundle.
type credentialResolver interface {
	APIKey(context.Context, string) (string, error)
	ReplaceAPIKey(context.Context, string, string) error
}

var inferenceCredentials credentialResolver

func ConfigureInferenceCredentials(resolver credentialResolver) { inferenceCredentials = resolver }

type inferenceRequest struct {
	Provider            string                    `json:"provider"`
	Model               string                    `json:"model"`
	Messages            []automationagent.Message `json:"messages"`
	MaxCompletionTokens int                       `json:"max_completion_tokens"`
	TaskID              string                    `json:"task_id"`
	RunID               string                    `json:"run_id"`
	Operation           string                    `json:"operation"`
}

// Infer is an internal worker-only proxy. It accepts model input over the
// existing TLS callback channel, obtains the provider key in cloud memory, and
// returns only the normalized completion. It never returns a credential.
func Infer(c echo.Context) error {
	if !validCallbackSecret(c.Request().Header.Get("X-Automation-Secret")) {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	resolver := inferenceCredentials
	cfg, _ := c.Get("config").(*models.Config)
	if resolver == nil || cfg == nil || strings.TrimSpace(cfg.AIProviderCredentialsSecretID) == "" {
		return utils.Error(c, http.StatusServiceUnavailable, "AI gateway unavailable", "")
	}
	var request inferenceRequest
	decoder := json.NewDecoder(io.LimitReader(c.Request().Body, maxInferenceGatewayRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return utils.Error(c, http.StatusBadRequest, "Invalid inference request", "")
	}
	provider := automationagent.Provider(strings.ToLower(strings.TrimSpace(request.Provider)))
	model := strings.TrimSpace(request.Model)
	if provider == "" || model == "" || len(request.Messages) == 0 || len(request.Messages) > 32 || request.MaxCompletionTokens < 1 || request.MaxCompletionTokens > 8192 || !gatewayRequestMatchesPolicy(cfg, provider, model) || !validInferenceMessages(request.Messages) || !validGatewayLease(request) {
		return utils.Error(c, http.StatusBadRequest, "Invalid inference request", "")
	}
	endpoint, ok := automationagent.DefaultProviderEndpoint(provider)
	if !ok {
		return utils.Error(c, http.StatusForbidden, "Inference route is not permitted", "")
	}
	apiKey, err := resolver.APIKey(c.Request().Context(), string(provider))
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "AI gateway unavailable", "")
	}
	providerConfig, err := automationagent.NewProviderConfig(provider, model, endpoint, apiKey)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "AI gateway unavailable", "")
	}
	completion, err := automationagent.NewProviderClient(providerConfig, nil).Complete(c.Request().Context(), request.Messages, request.MaxCompletionTokens)
	if err != nil {
		var retryable *automationagent.RetryableError
		if errors.As(err, &retryable) {
			return utils.Error(c, http.StatusServiceUnavailable, "AI provider temporarily unavailable", "")
		}
		return utils.Error(c, http.StatusBadGateway, "AI provider rejected the request", "")
	}
	return c.JSON(http.StatusOK, completion)
}

func gatewayRequestMatchesPolicy(cfg *models.Config, provider automationagent.Provider, model string) bool {
	return strings.EqualFold(strings.TrimSpace(cfg.AutomationBudgetProvider), string(provider)) && strings.TrimSpace(cfg.AutomationBudgetModel) == model
}

func validGatewayLease(request inferenceRequest) bool {
	if configuration.DB == nil {
		return false
	}
	taskID, err := uuid.FromString(strings.TrimSpace(request.TaskID))
	if err != nil || taskID == uuid.Nil || strings.TrimSpace(request.RunID) == "" || len(request.RunID) > 64 || strings.TrimSpace(request.Operation) == "" {
		return false
	}
	var task models.AutomationTask
	if err := configuration.DB.Select("operation", "status", "run_id", "max_completion_tokens").First(&task, taskID).Error; err != nil {
		return false
	}
	return task.Status == "running" && task.Operation == request.Operation && task.RunID == request.RunID && request.MaxCompletionTokens <= task.MaxCompletionTokens
}

func validInferenceMessages(messages []automationagent.Message) bool {
	total := 0
	for _, message := range messages {
		if message.Role != "system" && message.Role != "user" && message.Role != "assistant" {
			return false
		}
		if content := strings.TrimSpace(message.Content); content == "" || len(content) > 128<<10 {
			return false
		} else {
			total += len(content)
		}
	}
	return total <= maxInferenceGatewayRequestBytes
}

// Keep the compiler honest that the cloud resolver is the sole concrete
// credential implementation configured in production.
var _ credentialResolver = (*aicredentials.Resolver)(nil)
