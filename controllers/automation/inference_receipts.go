package automation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/evidencejson"
	"events-stocks/models"
	"events-stocks/services/automationcost"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errInferenceCallAlreadyReserved = errors.New("inference call id has already been used")
	errInferenceRunQuotaExceeded    = errors.New("inference run quota is exhausted")
)

// reserveAutomationInferenceReceipt is called inside the transaction which
// already holds the automation task row FOR UPDATE. That row lock serializes
// all call reservations for the run, including requests using different call
// IDs. Every state (including ambiguous) counts against the frozen quota.
func reserveAutomationInferenceReceipt(tx *gorm.DB, request inferenceRequest, task models.AutomationTask, snapshot models.AutomationInferenceAttemptPolicy, callID uuid.UUID, stepID *uuid.UUID) (models.AutomationInferenceReceipt, error) {
	var receipt models.AutomationInferenceReceipt
	if tx == nil || callID == uuid.Nil || snapshot.MaxInferenceCalls < 0 || snapshot.MaxInferenceCalls > maxAutomationInferenceCallsPerRun {
		return receipt, errors.New("inference receipt reservation is invalid")
	}
	if (stepID == nil && request.PlanStepID != "") ||
		(stepID != nil && (stepID.String() != request.PlanStepID || *stepID == uuid.Nil || task.Operation != "delivery.implementation")) {
		return receipt, errors.New("inference receipt step binding is invalid")
	}
	err := tx.Where("automation_task_id = ? AND run_id = ? AND call_id = ?", task.ID, request.RunID, callID).Take(&receipt).Error
	if err == nil {
		return models.AutomationInferenceReceipt{}, errInferenceCallAlreadyReserved
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return models.AutomationInferenceReceipt{}, err
	}
	var reserved int64
	if err := tx.Model(&models.AutomationInferenceReceipt{}).
		Where("automation_task_id = ? AND run_id = ?", task.ID, request.RunID).Count(&reserved).Error; err != nil {
		return models.AutomationInferenceReceipt{}, err
	}
	if reserved >= int64(snapshot.MaxInferenceCalls) {
		return models.AutomationInferenceReceipt{}, errInferenceRunQuotaExceeded
	}
	receiptID, err := uuid.NewV4()
	if err != nil {
		return models.AutomationInferenceReceipt{}, err
	}
	receipt = models.AutomationInferenceReceipt{
		ID: receiptID, AutomationTaskID: task.ID, RunID: request.RunID, CallID: callID,
		PlanStepID: stepID,
		Operation:  task.Operation, WorkerID: task.WorkerID, AgentKey: task.AgentKey, MachineID: task.MachineID,
		PolicySnapshotHash: snapshot.SnapshotHash, QuotaLimit: snapshot.MaxInferenceCalls, Status: "reserved", CreatedAt: time.Now().UTC(),
	}
	if err := tx.Create(&receipt).Error; err != nil {
		return models.AutomationInferenceReceipt{}, err
	}
	return receipt, nil
}

// acceptInferenceReceipt persists only accounting metadata derived from the
// normalized response received by the cloud gateway. Prompt and completion
// text is never stored in this table; only its exact-byte digest is retained.
func acceptInferenceReceipt(ctx context.Context, cfg *models.Config, scope gatewayInferenceScope, completion automationagent.Completion, status string) (models.AutomationInferenceReceipt, error) {
	var receipt models.AutomationInferenceReceipt
	if configuration.DB == nil || cfg == nil || scope.ReceiptID == uuid.Nil || scope.CallID == uuid.Nil || (status != "accepted" && status != "rejected") || completion.Usage == nil ||
		!providerAllowed(strings.ToLower(strings.TrimSpace(string(completion.Provider)))) || strings.TrimSpace(completion.Model) == "" {
		return receipt, errors.New("accepted inference response is missing accounting identity")
	}
	if err := automationcost.VerifyTokenUsage(completion.Usage); err != nil {
		return receipt, errors.New("provider usage could not be verified")
	}
	usage := sanitizeProviderUsage(completion.Usage)
	prices := pricingCatalog(cfg)
	if scope.EvaluationID != nil {
		prices = scope.EvaluationPricingJSON
	}
	ledger, err := automationcost.Build(string(completion.Provider), completion.Model, usage, prices)
	if err != nil || ledger.InputTokens+ledger.OutputTokens == 0 {
		return receipt, errors.New("provider usage could not be verified")
	}
	// Provider-supplied response bindings were discarded by sanitization. This
	// binding identifies the exact final-answer bytes observed by the gateway.
	usage["_itbem_response"] = inferenceResponseBinding(completion.Content)
	usageJSON, err := json.Marshal(usage)
	if err != nil {
		return receipt, errors.New("provider usage could not be recorded")
	}
	now := time.Now().UTC()
	err = configuration.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reserved models.AutomationInferenceReceipt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&reserved, "id = ?", scope.ReceiptID).Error; err != nil {
			return err
		}
		if reserved.Status != "reserved" || reserved.CallID != scope.CallID || reserved.AutomationTaskID == uuid.Nil || reserved.PolicySnapshotHash != scope.PolicyHash ||
			reserved.WorkerID != scope.WorkerID || reserved.AgentKey != scope.AgentKey || reserved.MachineID != scope.MachineID ||
			!sameInferenceReceiptStepID(reserved.PlanStepID, scope.PlanStepID) {
			return errors.New("inference receipt reservation does not match the gateway call")
		}
		updates := map[string]any{
			"status": status, "provider": strings.ToLower(strings.TrimSpace(string(completion.Provider))), "model": strings.TrimSpace(completion.Model),
			"provider_response_id": strings.TrimSpace(completion.ResponseID),
			"input_tokens":         ledger.InputTokens, "output_tokens": ledger.OutputTokens, "cached_input_tokens": ledger.CachedInputTokens,
			"cache_write_tokens": ledger.CacheWriteTokens, "reasoning_tokens": ledger.ReasoningTokens, "total_tokens": ledger.TotalTokens,
			"input_cost_micros": ledger.InputCostMicros, "output_cost_micros": ledger.OutputCostMicros,
			"cached_cost_micros": ledger.CachedCostMicros, "cache_write_cost_micros": ledger.CacheWriteCostMicros,
			"total_cost_micros": ledger.TotalCostMicros, "pricing_basis": ledger.PricingBasis,
			"pricing_snapshot_json": ledger.PricingSnapshot, "usage_json": string(usageJSON), "resolved_at": now,
		}
		result := tx.Model(&models.AutomationInferenceReceipt{}).Where("id = ? AND status = ?", reserved.ID, "reserved").Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("inference receipt was already resolved")
		}
		if scope.EvaluationID != nil && (status != "accepted" || ledger.TotalCostMicros > scope.EvaluationReservationMicros || ledger.PricingBasis == "unpriced" || len(scope.Routes) != 1 || !strings.EqualFold(scope.Routes[0].Provider, string(completion.Provider)) || !strings.EqualFold(scope.Routes[0].Model, completion.Model)) {
			if err := tx.Model(&models.AutomationModelEvaluation{}).Where("id = ?", *scope.EvaluationID).Update("status", "halted").Error; err != nil {
				return err
			}
		}
		return tx.First(&receipt, "id = ?", reserved.ID).Error
	})
	return receipt, err
}

func inferenceResponseBinding(content string) map[string]any {
	digest := sha256.Sum256([]byte(content))
	return map[string]any{"version": "utf8-final-answer-v1", "sha256": hex.EncodeToString(digest[:]), "bytes": len([]byte(content))}
}

func recordedInferenceResponseBinding(raw string) (*string, *int64) {
	var usage map[string]json.RawMessage
	if evidencejson.Validate([]byte(raw)) != nil || json.Unmarshal([]byte(raw), &usage) != nil {
		return nil, nil
	}
	var binding struct {
		Version string `json:"version"`
		SHA256  string `json:"sha256"`
		Bytes   *int64 `json:"bytes"`
	}
	if json.Unmarshal(usage["_itbem_response"], &binding) != nil || binding.Version != "utf8-final-answer-v1" || binding.Bytes == nil || *binding.Bytes < 0 || len(binding.SHA256) != 64 || binding.SHA256 != strings.ToLower(binding.SHA256) {
		return nil, nil
	}
	if _, err := hex.DecodeString(binding.SHA256); err != nil {
		return nil, nil
	}
	return &binding.SHA256, binding.Bytes
}

func sameInferenceReceiptStepID(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// sanitizeProviderUsage deliberately persists only known numeric token-count
// dimensions. Provider extensions, arbitrary strings and nested response data
// cannot smuggle prompt/answer content into the accounting ledger.
func sanitizeProviderUsage(raw map[string]any) map[string]any {
	clean := automationcost.TokenUsage(raw)
	if metadata, ok := raw["_itbem_provider"].(map[string]any); ok {
		filtered := make(map[string]any, 3)
		if reason, ok := metadata["finish_reason"].(string); ok && len(reason) <= 64 && safeProviderFinishReason(reason) {
			filtered["finish_reason"] = reason
		}
		for _, key := range []string{"input_sensitive", "output_sensitive"} {
			if value, ok := metadata[key].(bool); ok {
				filtered[key] = value
			}
		}
		if value, ok := metadata["status_code"]; ok && isNonNegativeUsageNumber(value) {
			filtered["status_code"] = value
		}
		if len(filtered) > 0 {
			clean["_itbem_provider"] = filtered
		}
	}
	return clean
}

func safeProviderFinishReason(value string) bool {
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '_', char == '-':
		default:
			return false
		}
	}
	return value != ""
}

func isNonNegativeUsageNumber(value any) bool {
	switch number := value.(type) {
	case float64:
		return number >= 0
	case int:
		return number >= 0
	case int64:
		return number >= 0
	case json.Number:
		parsed, err := number.Int64()
		return err == nil && parsed >= 0
	default:
		return false
	}
}

// markInferenceReceiptAmbiguous is intentionally terminal: when the provider
// outcome is unknown, retrying the same call ID must never invoke the provider
// a second time or release the reserved quota slot.
func markInferenceReceiptAmbiguous(ctx context.Context, receiptID uuid.UUID) error {
	if configuration.DB == nil || receiptID == uuid.Nil {
		return errors.New("inference receipt is unavailable")
	}
	resolvedAt := time.Now().UTC()
	return configuration.DB.WithContext(ctx).Model(&models.AutomationInferenceReceipt{}).
		Where("id = ? AND status = ?", receiptID, "reserved").
		Updates(map[string]any{"status": "ambiguous", "resolved_at": resolvedAt}).Error
}

// resolveAutomationInferenceReceipt is the only source for callback accounting.
// It binds a callback claim to the durable gateway reservation, accepted
// provider result, task/run/call, and server-authenticated worker identity.
func resolveAutomationInferenceReceipt(db *gorm.DB, receiptID, callID string, taskID uuid.UUID, runID string, identity automationagent.AgentIdentity) (models.AutomationInferenceReceipt, error) {
	var receipt models.AutomationInferenceReceipt
	parsedReceiptID, receiptErr := uuid.FromString(strings.TrimSpace(receiptID))
	parsedCallID, callErr := uuid.FromString(strings.TrimSpace(callID))
	if db == nil || receiptErr != nil || callErr != nil || parsedReceiptID == uuid.Nil || parsedCallID == uuid.Nil || taskID == uuid.Nil || strings.TrimSpace(runID) == "" {
		return receipt, errors.New("inference receipt identity is invalid")
	}
	if err := db.Where("id = ?", parsedReceiptID).Take(&receipt).Error; err != nil {
		return models.AutomationInferenceReceipt{}, err
	}
	if receipt.AutomationTaskID != taskID || receipt.RunID != runID || receipt.CallID != parsedCallID ||
		receipt.WorkerID != identity.WorkerID || receipt.AgentKey != identity.AgentKey || receipt.MachineID != identity.MachineID ||
		(receipt.Status != "accepted" && receipt.Status != "rejected") || receipt.Provider == "" || receipt.Model == "" ||
		receipt.InputTokens+receipt.OutputTokens == 0 || len(receipt.UsageJSON) == 0 || !json.Valid([]byte(receipt.UsageJSON)) {
		return models.AutomationInferenceReceipt{}, errors.New("inference receipt does not belong to this callback")
	}
	return receipt, nil
}

func hasCallbackProviderAccounting(provider, model string, usage json.RawMessage, responseID string) bool {
	return strings.TrimSpace(provider) != "" || strings.TrimSpace(model) != "" || len(usage) > 0 || strings.TrimSpace(responseID) != ""
}

func inferenceReceiptStatusAllowsCallback(receipt models.AutomationInferenceReceipt, callbackStatus string) bool {
	switch strings.ToLower(strings.TrimSpace(callbackStatus)) {
	case "completed":
		return receipt.Status == "accepted"
	case "failed":
		return receipt.Status == "accepted" || receipt.Status == "rejected"
	default:
		return false
	}
}

func buildVerifiedToolExecutionLedger(db *gorm.DB, cfg *models.Config, task *models.AutomationTask, runID, status string, reported []callbackToolExecution, artifacts []callbackArtifact, identity automationagent.AgentIdentity, primaryReceiptID uuid.UUID, completedAt time.Time) ([]models.AutomationToolExecution, error) {
	if len(reported) == 0 {
		return nil, nil
	}
	agentLoop := task != nil && task.Operation == "delivery.implementation" && (status == "completed" || status == "failed")
	if task == nil || (!agentLoop && ((status != "completed" && status != "failed") || task.Operation != "delivery.qa")) || task.DeliveryWorkItemID == nil || len(reported) > maxAutomationInferenceCallsPerRun {
		return nil, errors.New("only bounded completed delivery tool calls are allowed")
	}
	callCount := len(reported)
	if primaryReceiptID != uuid.Nil {
		callCount++
	}
	if callCount > inferenceAttemptCallQuota(task.Operation) {
		return nil, errInferenceRunQuotaExceeded
	}
	artifactReferences := make(map[string]callbackArtifact, len(artifacts))
	for _, artifact := range artifacts {
		artifactReferences[strings.TrimSpace(artifact.Reference)] = artifact
	}
	rows := make([]models.AutomationToolExecution, 0, len(reported))
	seenReceipts := make(map[uuid.UUID]struct{}, len(reported))
	seenCallKeys := make(map[string]struct{}, len(reported))
	if primaryReceiptID != uuid.Nil {
		seenReceipts[primaryReceiptID] = struct{}{}
	}
	for _, item := range reported {
		tool := strings.ToLower(strings.TrimSpace(item.Tool))
		stepKey := strings.TrimSpace(item.StepKey)
		if (!agentLoop && (tool != "stagehand" || stepKey != "qa.semantic_browser")) || (agentLoop && (tool != "agent_loop" || stepKey != "implementation.agent")) {
			return nil, errors.New("unapproved automation tool execution")
		}
		callKey := strings.ToLower(strings.TrimSpace(item.CallKey))
		if !toolCallKeyPattern.MatchString(callKey) {
			return nil, errors.New("tool call key is invalid")
		}
		if _, duplicate := seenCallKeys[callKey]; duplicate {
			return nil, errors.New("tool call key is duplicated")
		}
		seenCallKeys[callKey] = struct{}{}
		callStatus := strings.ToLower(strings.TrimSpace(item.CallStatus))
		if callStatus == "" {
			callStatus = "completed"
		}
		if callStatus != "completed" && callStatus != "failed" {
			return nil, errors.New("tool call status is invalid")
		}
		requestRef, responseRef := strings.TrimSpace(item.RequestRef), strings.TrimSpace(item.ResponseRef)
		artifact, exists := artifactReferences[responseRef]
		if agentLoop {
			if cfg == nil {
				return nil, errors.New("agent tool execution configuration is unavailable")
			}
			prefix := "s3://" + cfg.AutomationOutputBucket + "/automation/" + task.ID.String() + "/runs/" + runID + "/steps/" + callKey
			if requestRef != prefix+"/request.json" || responseRef != prefix+"/response.json" {
				return nil, errors.New("agent evidence must match the exact task, run and call")
			}
		} else if !exists || requestRef != responseRef || !strings.HasSuffix(strings.ToLower(strings.TrimSpace(artifact.Name)), "semantic-qa.json") || !strings.EqualFold(strings.TrimSpace(artifact.ContentType), "application/json") {
			return nil, errors.New("tool request and response must use the uploaded Stagehand report")
		}
		receipt, err := resolveAutomationInferenceReceipt(db, item.ReceiptID, item.CallID, task.ID, runID, identity)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seenReceipts[receipt.ID]; duplicate {
			return nil, errors.New("inference receipt was reused in one callback")
		}
		seenReceipts[receipt.ID] = struct{}{}
		receiptID := receipt.ID
		rowStatus := callStatus
		if receipt.Status == "rejected" {
			rowStatus = "failed"
		}
		rows = append(rows, models.AutomationToolExecution{
			AutomationTaskID: task.ID, DeliveryWorkItemID: task.DeliveryWorkItemID, RunID: runID,
			WorkerID: identity.WorkerID, AgentKey: identity.AgentKey, MachineID: identity.MachineID, InferenceReceiptID: &receiptID,
			Tool: tool, CallKey: callKey, CallStatus: rowStatus, StepKey: stepKey, Provider: receipt.Provider, Model: receipt.Model,
			InputTokens: receipt.InputTokens, OutputTokens: receipt.OutputTokens, CachedInputTokens: receipt.CachedInputTokens,
			CacheWriteTokens: receipt.CacheWriteTokens, ReasoningTokens: receipt.ReasoningTokens, TotalTokens: receipt.TotalTokens,
			InputCostMicros: receipt.InputCostMicros, OutputCostMicros: receipt.OutputCostMicros, CachedCostMicros: receipt.CachedCostMicros,
			CacheWriteCostMicros: receipt.CacheWriteCostMicros, TotalCostMicros: receipt.TotalCostMicros,
			Currency: receipt.Currency, PricingBasis: receipt.PricingBasis, PricingSnapshotJSON: receipt.PricingSnapshotJSON,
			UsageJSON: receipt.UsageJSON, RequestRef: requestRef, ResponseRef: responseRef, CompletedAt: completedAt,
		})
	}
	return rows, nil
}
