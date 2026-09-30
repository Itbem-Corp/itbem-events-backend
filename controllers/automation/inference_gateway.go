package automation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"events-stocks/configuration"
	"events-stocks/internal/aicredentials"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/utils"
	"io"
	"net/http"
	"strings"
	"time"

	"events-stocks/internal/inferencecapability"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// Exact-SHA code reviews preserve complete file diffs. A single changed
	// file may approach the 512 KiB segment boundary before the worker adds its
	// annotated evidence and bounded source context. Keep the gateway large
	// enough for that sealed request while retaining a strict body ceiling.
	maxInferenceGatewayRequestBytes = 1 << 20
	maxInferenceGatewayMessageBytes = 768 << 10
)

// The adapter below avoids making the controller depend on AWS. It is assigned
// once from the composition root and stays nil when this environment has not
// opted into the central credential bundle.
type credentialResolver interface {
	APIKey(context.Context, string) (string, error)
	ReplaceAPIKey(context.Context, string, string) error
}

var inferenceCredentials credentialResolver
var inferenceProviderHTTPClient *http.Client

func ConfigureInferenceCredentials(resolver credentialResolver) { inferenceCredentials = resolver }

// ConfigureInferenceProviderHTTPClient installs the outbound provider client
// used by the gateway. Production leaves it nil to use the default transport;
// the integration harness injects a strict local-only transport so tests can
// exercise the real provider adapter without contacting a billable API.
func ConfigureInferenceProviderHTTPClient(client *http.Client) {
	inferenceProviderHTTPClient = client
}

type inferenceRequest struct {
	CallID              string                    `json:"call_id"`
	PlanStepID          string                    `json:"plan_step_id,omitempty"`
	Provider            string                    `json:"provider"`
	Model               string                    `json:"model"`
	Messages            []automationagent.Message `json:"messages"`
	MaxCompletionTokens int                       `json:"max_completion_tokens"`
	TaskID              string                    `json:"task_id"`
	RunID               string                    `json:"run_id"`
	Operation           string                    `json:"operation"`
}

type gatewayInferenceScope struct {
	EvaluationID                *uuid.UUID
	EvaluationPricingJSON       string
	EvaluationReservationMicros int64
	ProjectID                   string
	PlanStepID                  *uuid.UUID
	Routes                      []models.AutomationAIActionRoute
	PolicyRevision              int64
	RoutesHash                  string
	PolicyHash                  string
	MaxCalls                    int
	CallID                      uuid.UUID
	ReceiptID                   uuid.UUID
	WorkerID                    string
	AgentKey                    string
	MachineID                   string
}

// Infer is an internal worker-only proxy. It accepts model input over the
// existing TLS callback channel, obtains the provider key in cloud memory, and
// returns only the normalized completion. It never returns a credential.
func Infer(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	resolver := inferenceCredentials
	cfg, _ := c.Get("config").(*models.Config)
	if resolver == nil || cfg == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "AI gateway unavailable", "")
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, maxInferenceGatewayRequestBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxInferenceGatewayRequestBytes {
		return utils.Error(c, http.StatusBadRequest, "Invalid inference request", "")
	}
	var request inferenceRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return utils.Error(c, http.StatusBadRequest, "Invalid inference request", "")
	}
	capabilityScope, authenticated := authenticatedInferenceCapability(request, c.Request().Header.Get(inferencecapability.HeaderName))
	if !authenticated {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	requestedProvider := automationagent.Provider(strings.ToLower(strings.TrimSpace(request.Provider)))
	if !validInferenceGatewayRequest(request, requestedProvider) {
		return utils.Error(c, http.StatusBadRequest, "Invalid inference request", "")
	}
	inferenceScope, policyConfigured, scopeErr := gatewayInferenceScopeForRequest(request, capabilityScope)
	if scopeErr != nil {
		c.Response().Header().Set(automationagent.InferenceConflictHeader, inferenceConflictCode(scopeErr))
		return utils.Error(c, http.StatusConflict, "Inference lease is no longer active", "")
	}
	if !policyConfigured {
		return utils.Error(c, http.StatusForbidden, "Inference route is not permitted", "")
	}
	// The worker's provider/model are only a capability bootstrap for the
	// transport. The task's persisted route snapshot is the sole authorization
	// for every inference call, including calls delegated to approved tools.
	completion, err := completeWithFallbackForProject(c.Request().Context(), resolver, request, inferenceScope.Routes, inferenceScope.ProjectID)
	if err != nil {
		var billable *automationagent.ProviderResponseError
		if errors.As(err, &billable) {
			completion = billable.Completion
			receipt, receiptErr := acceptInferenceReceipt(c.Request().Context(), cfg, inferenceScope, completion, "rejected")
			if receiptErr == nil {
				completion.CallID, completion.ReceiptID = request.CallID, receipt.ID.String()
				return c.JSON(http.StatusUnprocessableEntity, completion)
			}
		}
		_ = markInferenceReceiptAmbiguous(c.Request().Context(), inferenceScope.ReceiptID)
		c.Response().Header().Set(automationagent.InferenceFailureHeader, automationagent.InferenceFailureCode(err))
		var retryable *automationagent.RetryableError
		if errors.As(err, &retryable) {
			return utils.Error(c, http.StatusServiceUnavailable, "AI provider temporarily unavailable", "")
		}
		return utils.Error(c, http.StatusBadGateway, "AI provider rejected the request", "")
	}
	receipt, err := acceptInferenceReceipt(c.Request().Context(), cfg, inferenceScope, completion, "accepted")
	if err != nil {
		_ = markInferenceReceiptAmbiguous(c.Request().Context(), inferenceScope.ReceiptID)
		c.Response().Header().Set(automationagent.InferenceFailureHeader, inferenceAccountingFailureCode(err))
		return utils.Error(c, http.StatusBadGateway, "AI provider accounting unavailable", "")
	}
	if completion.Usage == nil {
		completion.Usage = map[string]any{}
	}
	routing, _ := completion.Usage["_itbem_routing"].(map[string]any)
	if routing == nil {
		routing = map[string]any{}
		completion.Usage["_itbem_routing"] = routing
	}
	routing["policy_revision"] = inferenceScope.PolicyRevision
	routing["policy_routes_hash"] = inferenceScope.RoutesHash
	completion.CallID = request.CallID
	completion.ReceiptID = receipt.ID.String()
	return c.JSON(http.StatusOK, completion)
}

func inferenceConflictCode(err error) string {
	if errors.Is(err, errInferenceCallAlreadyReserved) {
		return automationagent.InferenceConflictCallReused
	}
	if errors.Is(err, errInferenceRunQuotaExceeded) {
		return automationagent.InferenceConflictQuotaExhausted
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	switch {
	case strings.Contains(message, "worker identity"), strings.Contains(message, "capability scope"):
		return automationagent.InferenceConflictIdentityStale
	case strings.Contains(message, "attempt policy is unavailable"):
		return automationagent.InferenceConflictPolicyUnavailable
	case strings.Contains(message, "attempt policy scope"):
		return automationagent.InferenceConflictPolicyScopeInvalid
	case strings.Contains(message, "attempt policy signature"):
		return automationagent.InferenceConflictPolicySignatureInvalid
	case strings.Contains(message, "attempt policy has no routes"):
		return automationagent.InferenceConflictPolicyRoutesMissing
	case strings.Contains(message, "attempt policy routes"):
		return automationagent.InferenceConflictPolicyRoutesInvalid
	case strings.Contains(message, "attempt policy"):
		return automationagent.InferenceConflictPolicyInvalid
	case strings.Contains(message, "project scope"):
		return automationagent.InferenceConflictProjectInvalid
	case strings.Contains(message, "lease"):
		return automationagent.InferenceConflictLeaseInactive
	default:
		return automationagent.InferenceConflictStateUnavailable
	}
}

// authenticatedInferenceCapability accepts only a capability signed by the
// server-only attempt-policy key. The callback master is intentionally not a
// valid inference credential, even when it is present on the worker.
func authenticatedInferenceCapability(request inferenceRequest, token string) (inferencecapability.Scope, bool) {
	keys := make([][]byte, 0, 2)
	if active, _, ok := activeAttemptPolicySigningKey(); ok {
		keys = append(keys, active)
	}
	if previous, _, ok := previousAttemptPolicySigningKey(); ok {
		keys = append(keys, previous)
	}
	for _, key := range keys {
		if scope, err := inferencecapability.Verify(string(key), token, request.TaskID, request.RunID, request.Operation, time.Now().UTC()); err == nil {
			return scope, true
		}
	}
	return inferencecapability.Scope{}, false
}

// completeWithFallback attempts the selected primary, fallback, then final
// fallback route. Only explicit provider 408/429/5xx responses may advance
// the chain; network failures are deliberately retried on the same route to
// avoid duplicating a request whose billable outcome is unknown.
func completeWithFallback(ctx context.Context, resolver credentialResolver, request inferenceRequest, routes []models.AutomationAIActionRoute) (automationagent.Completion, error) {
	return completeWithFallbackForProject(ctx, resolver, request, routes, "")
}

func completeWithFallbackForProject(ctx context.Context, resolver credentialResolver, request inferenceRequest, routes []models.AutomationAIActionRoute, projectID string) (automationagent.Completion, error) {
	var lastRetryable error
	for index, route := range routes {
		provider := automationagent.Provider(route.Provider)
		endpoint, ok := automationagent.DefaultProviderEndpoint(provider)
		if !ok {
			return automationagent.Completion{}, errors.New("AI routing configuration is invalid")
		}
		apiKey, keyErr := inferenceAPIKey(ctx, resolver, projectID, route.Provider)
		if keyErr != nil {
			// Credential lookup failure is a configuration/policy failure, not
			// a provider response that permits failover. Moving to another key
			// here would silently change the project's billing route.
			return automationagent.Completion{}, errors.New("AI gateway unavailable")
		}
		providerConfig, configErr := automationagent.NewProviderConfig(provider, route.Model, endpoint, apiKey)
		if configErr != nil {
			return automationagent.Completion{}, errors.New("AI routing configuration is invalid")
		}
		if provider == automationagent.ProviderOpenCodeGo {
			providerConfig.SessionID = request.TaskID
		}
		providerConfig.ReasoningEnabled = route.ReasoningEnabled
		providerConfig.ReasoningEffort = route.ReasoningEffort
		completion, completionErr := automationagent.NewProviderClient(providerConfig, inferenceProviderHTTPClient).Complete(ctx, request.Messages, request.MaxCompletionTokens)
		if completionErr == nil {
			if completion.Usage == nil {
				completion.Usage = map[string]any{}
			}
			completion.Usage["_itbem_routing"] = map[string]any{"route_index": index, "attempts": index + 1, "fallback_used": index > 0}
			return completion, nil
		}
		var retryable *automationagent.RetryableError
		if errors.As(completionErr, &retryable) && fallbackEligible(retryable) && index+1 < len(routes) {
			lastRetryable = completionErr
			continue
		}
		return completion, completionErr
	}
	if lastRetryable != nil {
		return automationagent.Completion{}, lastRetryable
	}
	return automationagent.Completion{}, errors.New("AI gateway unavailable")
}

func inferenceAPIKey(ctx context.Context, resolver credentialResolver, projectID, provider string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return resolver.APIKey(ctx, provider)
	}
	projectResolver, ok := resolver.(projectAPIKeyResolver)
	if !ok {
		return "", errors.New("AI project provider credential is not configured")
	}
	return projectResolver.APIKeyForProject(ctx, projectID, provider)
}

func fallbackEligible(err *automationagent.RetryableError) bool {
	return err != nil && (err.StatusCode == http.StatusRequestTimeout || err.StatusCode == http.StatusTooManyRequests || err.StatusCode >= http.StatusInternalServerError)
}

func validInferenceGatewayRequest(request inferenceRequest, provider automationagent.Provider) bool {
	callID, callErr := uuid.FromString(strings.TrimSpace(request.CallID))
	_, supportedProvider := automationagent.DefaultProviderEndpoint(provider)
	model := strings.TrimSpace(request.Model)
	// Use the same per-operation ceiling as task creation and the worker.
	// A stale blanket 8192 limit rejected every full-output exact-SHA review
	// before its lease or provider could be checked. The signed attempt and
	// task limits below still independently enforce any tighter allowance.
	maxOutput := automationagent.CompletionTokensForOperation(request.Operation)
	if request.PlanStepID != "" && strings.TrimSpace(request.PlanStepID) == "" {
		return false
	}
	if strings.TrimSpace(request.PlanStepID) != "" {
		stepID, stepErr := uuid.FromString(strings.TrimSpace(request.PlanStepID))
		if stepErr != nil || stepID == uuid.Nil || request.PlanStepID != stepID.String() || request.Operation != "delivery.implementation" {
			return false
		}
	}
	return callErr == nil && callID != uuid.Nil && supportedProvider && model != "" && len(model) <= 200 &&
		len(request.Messages) > 0 && len(request.Messages) <= 32 && request.MaxCompletionTokens >= 1 && request.MaxCompletionTokens <= maxOutput &&
		validInferenceMessages(request.Messages)
}

func validGatewayLease(request inferenceRequest) bool {
	_, _, err := gatewayInferenceScopeForRequest(request)
	return err == nil
}

// gatewayInferenceScopeForRequest validates the active durable lease, derives
// credential authorization from task -> work item -> project, and loads the
// immutable route recipe captured for this exact (task, run) attempt. Workers
// cannot supply their own project scope or switch providers/models by changing
// local configuration, and policy edits during a lease cannot change its route.
func gatewayInferenceScopeForRequest(request inferenceRequest, authenticated ...inferencecapability.Scope) (gatewayInferenceScope, bool, error) {
	var scope gatewayInferenceScope
	if configuration.DB == nil {
		return scope, false, errors.New("automation database is unavailable")
	}
	taskID, err := uuid.FromString(strings.TrimSpace(request.TaskID))
	if err != nil || taskID == uuid.Nil || strings.TrimSpace(request.RunID) == "" || len(request.RunID) > 64 || strings.TrimSpace(request.Operation) == "" {
		return scope, false, errors.New("automation inference lease is invalid")
	}
	if request.PlanStepID != "" && strings.TrimSpace(request.PlanStepID) == "" {
		return scope, false, errors.New("automation inference step lease is invalid")
	}
	var requestedStepID *uuid.UUID
	if strings.TrimSpace(request.PlanStepID) != "" {
		parsedStepID, parseErr := uuid.FromString(strings.TrimSpace(request.PlanStepID))
		if parseErr != nil || parsedStepID == uuid.Nil || parsedStepID.String() != request.PlanStepID || request.Operation != "delivery.implementation" || len(authenticated) == 0 {
			return scope, false, errors.New("automation inference step lease is invalid")
		}
		requestedStepID = &parsedStepID
	}
	var task models.AutomationTask
	leaseValid := false
	policyConfigured := false
	var capabilityScope inferencecapability.Scope
	if len(authenticated) > 0 {
		capabilityScope = authenticated[0]
		if capabilityScope.TaskID != taskID.String() || capabilityScope.RunID != request.RunID || capabilityScope.Operation != request.Operation ||
			capabilityScope.WorkerID == "" || capabilityScope.AgentKey == "" || capabilityScope.MachineID == "" {
			return scope, false, errors.New("automation inference capability scope is invalid")
		}
	}
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(
			"id", "operation", "status", "run_id", "lease_expires_at", "max_completion_tokens", "delivery_work_item_id", "worker_id", "agent_key", "machine_id", "model_evaluation_id", "delivery_onboarding_id", "budget_reservation_expires_at",
		).First(&task, taskID).Error; err != nil {
			return err
		}
		if task.Status != "running" || task.LeaseExpiresAt == nil || !time.Now().UTC().Before(task.LeaseExpiresAt.UTC()) || task.Operation != request.Operation || task.RunID != request.RunID || request.MaxCompletionTokens > task.MaxCompletionTokens {
			return errors.New("automation inference lease is invalid")
		}
		if len(authenticated) > 0 && (task.WorkerID != capabilityScope.WorkerID || task.AgentKey != capabilityScope.AgentKey || task.MachineID != capabilityScope.MachineID) {
			return errors.New("automation inference worker identity is no longer current")
		}
		if requestedStepID != nil {
			if err := validateGatewayInferencePlanStep(tx, task, request, capabilityScope, *requestedStepID); err != nil {
				return err
			}
			scope.PlanStepID = requestedStepID
		}
		leaseValid = true
		var derivedProjectID *uuid.UUID
		if task.DeliveryWorkItemID != nil {
			var item models.DeliveryWorkItem
			if err := tx.Select("project_id").First(&item, *task.DeliveryWorkItemID).Error; err != nil || item.ProjectID == uuid.Nil {
				return errors.New("automation inference project scope is invalid")
			}
			projectID := item.ProjectID
			derivedProjectID = &projectID
			scope.ProjectID = projectID.String()
		}

		snapshot, err := readAutomationInferenceAttemptPolicy(tx, taskID, request.RunID)
		if err != nil {
			return errors.New("automation inference attempt policy is unavailable")
		}
		if snapshot.AutomationTaskID != taskID || snapshot.RunID != request.RunID || snapshot.Operation != task.Operation || snapshot.Operation != request.Operation || snapshot.MaxCompletionTokens != task.MaxCompletionTokens || request.MaxCompletionTokens > snapshot.MaxCompletionTokens || !sameInferenceProject(snapshot.ProjectID, derivedProjectID) {
			return errors.New("automation inference attempt policy scope is invalid")
		}
		routes, hash, valid := validateAutomationInferenceAttemptPolicy(snapshot)
		if !valid {
			return errors.New("automation inference attempt policy is invalid")
		}
		if !verifyAutomationInferenceAttemptPolicySignature(snapshot) {
			return errors.New("automation inference attempt policy signature is invalid")
		}
		if len(routes) == 0 {
			return errors.New("automation inference attempt policy has no routes")
		}
		if len(routes) > models.MaxAutomationAIActionRoutes {
			return errors.New("automation inference attempt policy routes are invalid")
		}
		_, canonicalHash, err := canonicalInferenceRoutes(routes)
		if err != nil || !strings.EqualFold(canonicalHash, hash) {
			return errors.New("automation inference attempt policy routes are invalid")
		}
		scope.Routes, scope.RoutesHash, scope.PolicyRevision = routes, hash, snapshot.PolicyRevision
		scope.PolicyHash, scope.MaxCalls = snapshot.SnapshotHash, snapshot.MaxInferenceCalls
		scope.WorkerID, scope.AgentKey, scope.MachineID = task.WorkerID, task.AgentKey, task.MachineID
		if strings.TrimSpace(request.CallID) != "" {
			callID, parseErr := uuid.FromString(strings.TrimSpace(request.CallID))
			if parseErr != nil || callID == uuid.Nil {
				return errors.New("automation inference call id is invalid")
			}
			if task.ModelEvaluationID != nil {
				if err := validateEvaluationInference(tx, task, request, snapshot, &scope); err != nil {
					return err
				}
			}
			receipt, reserveErr := reserveAutomationInferenceReceipt(tx, request, task, snapshot, callID, scope.PlanStepID)
			if reserveErr != nil {
				return reserveErr
			}
			scope.CallID, scope.ReceiptID = callID, receipt.ID
		}
		policyConfigured = true
		return nil
	})
	if err != nil {
		return gatewayInferenceScope{}, false, err
	}
	if !leaseValid {
		return gatewayInferenceScope{}, false, errors.New("automation inference lease is invalid")
	}
	return scope, policyConfigured, nil
}

// validateGatewayInferencePlanStep binds a worker-supplied opaque step UUID
// to the same live, approved plan-step lease that the callback path enforces.
// A step is never inferred from timing, event order, or task proximity.
func validateGatewayInferencePlanStep(tx *gorm.DB, task models.AutomationTask, request inferenceRequest, capability inferencecapability.Scope, stepID uuid.UUID) error {
	if tx == nil || stepID == uuid.Nil || task.ID == uuid.Nil || task.Operation != "delivery.implementation" || request.Operation != task.Operation ||
		task.DeliveryWorkItemID == nil || capability.TaskID != task.ID.String() || capability.RunID != request.RunID || capability.Operation != task.Operation ||
		capability.WorkerID != task.WorkerID || capability.AgentKey != task.AgentKey || capability.MachineID != task.MachineID {
		return errors.New("automation inference step lease is invalid")
	}
	var step models.DeliveryPlanStep
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&step, "id = ?", stepID).Error; err != nil {
		return err
	}
	var plan models.DeliveryPlan
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id", "work_item_id", "status", "approved_gate_id").First(&plan, "id = ?", step.PlanID).Error; err != nil {
		return err
	}
	if plan.WorkItemID != *task.DeliveryWorkItemID || plan.Status != "approved" || plan.ApprovedGateID == nil {
		return errors.New("automation inference step plan is not approved for this task")
	}
	// This validates the persisted fan-out child-task -> execution -> step
	// binding and approved plan hash. A nil assignment remains valid only for
	// legacy sequential claims, which are independently bound by the live tuple
	// and lease checks below.
	if _, err := validateAssignedPlanStepCallback(tx, task, stepID); err != nil {
		return err
	}
	// The assignment/hash queries can block behind concurrent dispatch writes.
	// Refresh time only after those locks are held, then ensure both the task
	// lease and step lease are still live at the instant of receipt reservation.
	now := time.Now().UTC()
	if !planStepActivityLeaseMatches(step, task, request.RunID, capability.WorkerID, capability.AgentKey, capability.MachineID, step.LeaseFence, now) {
		return errors.New("automation inference step lease is stale")
	}
	return nil
}

func sameInferenceProject(snapshotProjectID, derivedProjectID *uuid.UUID) bool {
	if snapshotProjectID == nil || *snapshotProjectID == uuid.Nil {
		return derivedProjectID == nil
	}
	return derivedProjectID != nil && *snapshotProjectID == *derivedProjectID
}

func canonicalInferenceRoutes(routes []models.AutomationAIActionRoute) (string, string, error) {
	if len(routes) == 0 || len(routes) > models.MaxAutomationAIActionRoutes {
		return "", "", errors.New("inference routes are invalid")
	}
	seen := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		provider := automationagent.Provider(strings.ToLower(strings.TrimSpace(route.Provider)))
		model := strings.TrimSpace(route.Model)
		effort := strings.ToLower(strings.TrimSpace(route.ReasoningEffort))
		_, supportedProvider := automationagent.DefaultProviderEndpoint(provider)
		if !supportedProvider || model == "" || len(model) > 200 ||
			(!automationagent.IsAllowedReasoningEffort(effort) && effort != "") ||
			(!route.ReasoningEnabled && effort != "") {
			return "", "", errors.New("inference routes are invalid")
		}
		identity := string(provider) + "\x00" + model
		if _, duplicate := seen[identity]; duplicate {
			return "", "", errors.New("inference routes are invalid")
		}
		seen[identity] = struct{}{}
	}
	encoded, err := json.Marshal(routes)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(encoded)
	return string(encoded), hex.EncodeToString(digest[:]), nil
}

func validInferenceMessages(messages []automationagent.Message) bool {
	total := 0
	for _, message := range messages {
		if message.Role != "system" && message.Role != "user" && message.Role != "assistant" {
			return false
		}
		if content := strings.TrimSpace(message.Content); content == "" || len(content) > maxInferenceGatewayMessageBytes {
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
