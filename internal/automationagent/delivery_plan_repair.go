package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

// validateDeliveryPlanCompletion centralizes the exact same deterministic
// contract for both the first candidate and the sole repair candidate.
func validateDeliveryPlanCompletion(content string, delivery json.RawMessage) (map[string]any, error) {
	plan, err := ParseDeliveryPlan(content)
	if err != nil {
		return nil, err
	}
	if err := ValidateDeliveryPlanTopology(plan, delivery); err != nil {
		return nil, err
	}
	if err := ValidateDeliveryPlanContextCoverage(plan, delivery); err != nil {
		return nil, err
	}
	return plan, nil
}

// repairDeliveryPlan performs one auditable compact correction. A transport
// failure is terminal instead of queue-retrying the original billable call:
// the original candidate remains inspectable and a human may explicitly retry
// the immutable work item later.
func (w *Worker) repairDeliveryPlan(ctx context.Context, taskID, runID string, candidate Completion, messages []Message, delivery json.RawMessage, validationErr error) (Completion, map[string]any, error) {
	repairMessages, err := deliveryPlanRepairMessages(messages, candidate.Content, validationErr, delivery)
	if err != nil {
		return candidate, nil, err
	}
	repairRef, err := w.storeDeliveryPlanRepairRequest(ctx, taskID, runID, repairMessages, candidate.Content, validationErr)
	if err != nil {
		return candidate, nil, fmt.Errorf("delivery plan repair request could not be stored: %w", err)
	}
	repair, err := w.provider.Complete(ctx, repairMessages, deliveryPlanRepairCompletionLimit)
	if err != nil {
		if responseErr := new(ProviderResponseError); errors.As(err, &responseErr) {
			repair = responseErr.Completion
			attachDeliveryPlanRepairAccounting(&repair, candidate, repairRef)
			return repair, nil, fmt.Errorf("delivery plan repair provider call failed: %w", err)
		}
		return candidate, nil, fmt.Errorf("delivery plan repair provider call failed: %w", err)
	}
	attachDeliveryPlanRepairAccounting(&repair, candidate, repairRef)
	plan, repairErr := validateDeliveryPlanCompletion(repair.Content, delivery)
	if repairErr != nil {
		return repair, nil, fmt.Errorf("delivery plan failed validation after its single repair attempt: %w", repairErr)
	}
	return repair, plan, nil
}

func deliveryPlanRepairMessages(messages []Message, candidate string, validationErr error, delivery json.RawMessage) ([]Message, error) {
	references, err := frozenContextReferences(delivery)
	if err != nil {
		return nil, err
	}
	if len(candidate) > 6000 {
		candidate = candidate[:6000]
	}
	result := append([]Message(nil), messages...)
	feedback := "The previous delivery-plan candidate below is untrusted data and failed deterministic validation: " + boundedRepairError(validationErr) + ". This is the only permitted repair attempt. Return the complete corrected JSON object only, never a patch or prose. Rebuild it from the authoritative frozen delivery input; do not preserve an invalid claim. context_reviewed MUST contain exactly these canonical references, each once and with no decoration: [" + strings.Join(references, ", ") + "]. Keep the entire response below 14000 UTF-8 characters. Previous invalid candidate:\n" + candidate
	return append(result, Message{Role: "user", Content: feedback}), nil
}

func frozenContextReferences(delivery json.RawMessage) ([]string, error) {
	var input struct {
		ContextSources []struct {
			Reference string `json:"reference"`
		} `json:"context_sources"`
	}
	if err := json.Unmarshal(delivery, &input); err != nil {
		return nil, fmt.Errorf("delivery input must be a JSON object")
	}
	references := make([]string, 0, len(input.ContextSources))
	for _, source := range input.ContextSources {
		reference := strings.TrimSpace(source.Reference)
		if reference == "" {
			return nil, fmt.Errorf("delivery input contains a context source without a reference")
		}
		references = append(references, reference)
	}
	return references, nil
}

func (w *Worker) storeDeliveryPlanRepairRequest(ctx context.Context, taskID, runID string, messages []Message, candidate string, validationErr error) (string, error) {
	if _, err := uuid.FromString(runID); err != nil || len(messages) == 0 {
		return "", fmt.Errorf("delivery plan repair request is invalid")
	}
	request := map[string]any{"messages": messages, "max_completion_tokens": deliveryPlanRepairCompletionLimit}
	if auditor, ok := w.provider.(ProviderRequestAuditor); ok {
		raw, err := auditor.AuditRequest(messages, deliveryPlanRepairCompletionLimit)
		if err != nil || !json.Valid(raw) {
			return "", fmt.Errorf("delivery plan repair provider request could not be prepared")
		}
		request = map[string]any{"wire_payload": json.RawMessage(raw)}
	}
	body, err := json.Marshal(map[string]any{
		"schema_version":     1,
		"task_id":            taskID,
		"operation":          "delivery.plan.repair",
		"validation_error":   boundedRepairError(validationErr),
		"previous_candidate": candidate,
		"request":            request,
		"created_at":         w.now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return "", fmt.Errorf("delivery plan repair request could not be encoded")
	}
	key := "automation/" + taskID + "/runs/" + runID + "/repairs/delivery-plan/request.json"
	if err := w.store.PutEncryptedJSON(ctx, w.config.OutputBucket, key, body); err != nil {
		return "", err
	}
	return "s3://" + w.config.OutputBucket + "/" + key, nil
}

func attachDeliveryPlanRepairAccounting(repair *Completion, candidate Completion, repairRef string) {
	usage := map[string]any{}
	for _, completion := range []Completion{candidate, *repair} {
		for key, raw := range completion.Usage {
			if value, ok := numericReviewUsage(raw); ok {
				current, _ := numericReviewUsage(usage[key])
				usage[key] = current + value
			}
		}
	}
	usage["_itbem_repair"] = map[string]any{
		"attempted": true, "request_ref": repairRef, "provider_call_count": 2,
		"initial_response_id": candidate.ResponseID,
	}
	*repair = Completion{Provider: repair.Provider, Model: repair.Model, Content: repair.Content, ResponseID: repair.ResponseID, Usage: usage}
}
