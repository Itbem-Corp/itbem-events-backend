package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/gofrs/uuid"
)

const codeReviewSegmentCompletionLimit = 8192
const codeReviewSegmentCompletionFloor = 2048

type codeReviewProviderCall struct {
	Index       int
	Boundary    CodeReviewInput
	Messages    []Message
	MaxTokens   int
	PatchDigest string
}

func (w *Worker) processSegmentedCodeReview(ctx context.Context, message TaskMessage, runID string, input TaskInput, boundary CodeReviewInput) error {
	segments, err := SegmentCodeReviewInput(boundary)
	if err != nil {
		return w.fail(ctx, message.Payload.TaskID, runID, err)
	}
	needsCoverageGap := reviewNeedsCoverageGap(boundary)
	totalCompletionTokens := messageCompletionTokens(message.Payload.Operation, message.Payload.MaxCompletionTokens)
	calls := make([]codeReviewProviderCall, 0, len(segments))
	for index, segment := range segments {
		delivery, err := json.Marshal(segment)
		if err != nil {
			return w.fail(ctx, message.Payload.TaskID, runID, fmt.Errorf("code review segment could not be encoded"))
		}
		segmentInput := input
		segmentInput.Delivery = delivery
		segmentInput.Prompt = fmt.Sprintf("%s\n\nReview segment %d of %d. This segment contains %d complete file diffs from one immutable pull request. Judge only this segment; a deterministic aggregator will combine every segment and will fail if any segment is missing or invalid.", strings.TrimSpace(input.Prompt), index+1, len(segments), len(segment.ChangedFiles))
		messages, err := buildTaskMessagesWithReviewCoverage("code.review", segmentInput, os.Getenv, &needsCoverageGap)
		if err != nil {
			return w.fail(ctx, message.Payload.TaskID, runID, err)
		}
		calls = append(calls, codeReviewProviderCall{Index: index + 1, Boundary: segment, Messages: messages, PatchDigest: segment.PatchSHA256})
	}
	allocations, err := allocateCodeReviewCompletionTokens(calls, totalCompletionTokens)
	if err != nil {
		return w.fail(ctx, message.Payload.TaskID, runID, err)
	}
	for index := range calls {
		calls[index].MaxTokens = allocations[index]
	}
	requestRef, err := w.storeCodeReviewExecutionRequest(ctx, message.Payload.TaskID, runID, calls, boundary)
	if err != nil {
		return err
	}
	completions := make([]Completion, 0, len(calls))
	reviews := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		completion, callErr := w.provider.Complete(ctx, call.Messages, call.MaxTokens)
		if callErr != nil {
			var retryable *RetryableError
			if errors.As(callErr, &retryable) && len(completions) == 0 {
				return retryable
			}
			var providerResponse *ProviderResponseError
			if errors.As(callErr, &providerResponse) {
				completion = providerResponse.Completion
				completions = append(completions, completion)
			}
			if len(completions) == 0 {
				return w.fail(ctx, message.Payload.TaskID, runID, callErr)
			}
			audit, auditErr := aggregateCodeReviewCompletions(completions, nil)
			if auditErr != nil {
				return w.fail(ctx, message.Payload.TaskID, runID, auditErr)
			}
			return w.failWithProviderResult(ctx, message.Payload.TaskID, runID, requestRef, message.Payload.Operation, audit, fmt.Errorf("code review segment %d provider call failed: %w", call.Index, callErr))
		}
		completions = append(completions, completion)
		review, parseErr := ParseCodeReview(completion.Content)
		if parseErr == nil {
			parseErr = ValidateCodeReviewBoundary(review, call.Boundary)
		}
		if parseErr != nil {
			audit, auditErr := aggregateCodeReviewCompletions(completions, nil)
			if auditErr != nil {
				return w.fail(ctx, message.Payload.TaskID, runID, auditErr)
			}
			return w.failWithProviderResult(ctx, message.Payload.TaskID, runID, requestRef, message.Payload.Operation, audit, fmt.Errorf("code review segment %d failed validation: %w", call.Index, parseErr))
		}
		reviews = append(reviews, review)
	}
	aggregate, err := AggregateCodeReviewSegments(boundary, segments, reviews)
	if err != nil {
		audit, auditErr := aggregateCodeReviewCompletions(completions, nil)
		if auditErr != nil {
			return w.fail(ctx, message.Payload.TaskID, runID, auditErr)
		}
		return w.failWithProviderResult(ctx, message.Payload.TaskID, runID, requestRef, message.Payload.Operation, audit, err)
	}
	execution := map[string]any(nil)
	if boundary.Remote != nil {
		publication, publishErr := PublishGitHubCodeReview(ctx, boundary, aggregate, os.Getenv)
		if publishErr != nil {
			audit, auditErr := aggregateCodeReviewCompletions(completions, aggregate)
			if auditErr != nil {
				return w.fail(ctx, message.Payload.TaskID, runID, auditErr)
			}
			return w.failWithProviderResult(ctx, message.Payload.TaskID, runID, requestRef, message.Payload.Operation, audit, publishErr)
		}
		execution = CodeReviewPublicationHandoff(publication)
	}
	completion, err := aggregateCodeReviewCompletions(completions, aggregate)
	if err != nil {
		return w.fail(ctx, message.Payload.TaskID, runID, err)
	}
	output := map[string]any{
		"schema_version": 1, "task_id": message.Payload.TaskID, "run_id": runID, "operation": message.Payload.Operation,
		"request_ref": requestRef, "provider": completion.Provider, "model": completion.Model,
		"response_id": completion.ResponseID, "usage": completion.Usage, "content": completion.Content,
		"structured_result": aggregate, "artifacts": map[string]any{"artifacts": []any{}}, "execution": execution,
		"review_segments": codeReviewCompletionAudit(completions, segments), "created_at": w.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return w.failWithProviderResult(ctx, message.Payload.TaskID, runID, requestRef, message.Payload.Operation, completion, fmt.Errorf("automation result could not be encoded"))
	}
	outputRef, err := w.storeExecutionResult(ctx, message.Payload.TaskID, runID, encoded)
	if err != nil {
		_, callbackErr := w.callback.Update(ctx, message.Payload.TaskID, TaskUpdate{Status: "failed", RunID: runID, ErrorMessage: "provider response storage unavailable; response cannot be inspected", RequestRef: requestRef, Provider: completion.Provider, Model: completion.Model, Usage: completion.Usage, ResponseID: completion.ResponseID})
		return callbackErr
	}
	_, err = w.callback.Update(ctx, message.Payload.TaskID, TaskUpdate{Status: "completed", RunID: runID, RequestRef: requestRef, OutputRef: outputRef, Provider: completion.Provider, Model: completion.Model, Usage: completion.Usage, ResponseID: completion.ResponseID, Execution: execution})
	return err
}

func allocateCodeReviewCompletionTokens(calls []codeReviewProviderCall, total int) ([]int, error) {
	if len(calls) == 0 || total < len(calls)*MinCompletionTokens {
		return nil, fmt.Errorf("code review completion budget cannot cover every segment")
	}
	floor := min(codeReviewSegmentCompletionFloor, total/len(calls))
	allocations := make([]int, len(calls))
	sizes := make([]int, len(calls))
	totalSize := 0
	for index, call := range calls {
		allocations[index] = floor
		for _, message := range call.Messages {
			sizes[index] += len(message.Content)
		}
		if sizes[index] < 1 {
			sizes[index] = 1
		}
		totalSize += sizes[index]
	}
	remaining := total - floor*len(calls)
	for index := range calls {
		share := remaining * sizes[index] / totalSize
		share = min(share, codeReviewSegmentCompletionLimit-allocations[index])
		allocations[index] += share
	}
	used := 0
	for _, allocation := range allocations {
		used += allocation
	}
	for index := 0; used < total; index = (index + 1) % len(allocations) {
		if allocations[index] >= codeReviewSegmentCompletionLimit {
			allCapped := true
			for _, allocation := range allocations {
				allCapped = allCapped && allocation >= codeReviewSegmentCompletionLimit
			}
			if allCapped {
				break
			}
			continue
		}
		allocations[index]++
		used++
	}
	return allocations, nil
}

func (w *Worker) storeCodeReviewExecutionRequest(ctx context.Context, taskID, runID string, calls []codeReviewProviderCall, boundary CodeReviewInput) (string, error) {
	if _, err := uuid.FromString(runID); err != nil {
		return "", fmt.Errorf("code review request run ID is invalid")
	}
	requests := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		request := map[string]any{"messages": call.Messages, "max_completion_tokens": call.MaxTokens}
		if auditor, ok := w.provider.(ProviderRequestAuditor); ok {
			raw, err := auditor.AuditRequest(call.Messages, call.MaxTokens)
			if err != nil || !json.Valid(raw) {
				return "", fmt.Errorf("code review segment provider request could not be prepared")
			}
			request = map[string]any{"wire_payload": json.RawMessage(raw)}
		}
		requests = append(requests, map[string]any{"segment": call.Index, "patch_sha256": call.PatchDigest, "request": request})
	}
	body, err := json.Marshal(map[string]any{
		"schema_version": 1, "task_id": taskID, "operation": "code.review", "base_sha": boundary.BaseSHA,
		"head_sha": boundary.HeadSHA, "patch_sha256": boundary.PatchSHA256, "segments": requests,
		"created_at": w.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	})
	if err != nil {
		return "", fmt.Errorf("code review request manifest could not be encoded")
	}
	runKey := "automation/" + taskID + "/runs/" + runID + "/request.json"
	if err := w.store.PutEncryptedJSON(ctx, w.config.OutputBucket, runKey, body); err != nil {
		return "", err
	}
	return "s3://" + w.config.OutputBucket + "/" + runKey, nil
}

func aggregateCodeReviewCompletions(completions []Completion, aggregate map[string]any) (Completion, error) {
	if len(completions) == 0 {
		return Completion{}, fmt.Errorf("code review has no provider completions")
	}
	provider, model := completions[0].Provider, completions[0].Model
	usage := map[string]any{}
	for _, completion := range completions {
		if completion.Provider != provider || completion.Model != model {
			return Completion{}, fmt.Errorf("code review segments changed provider identity")
		}
		for key, raw := range completion.Usage {
			if value, ok := numericReviewUsage(raw); ok {
				current, _ := numericReviewUsage(usage[key])
				usage[key] = current + value
			}
		}
	}
	inputSensitive, outputSensitive, statusCode := false, false, 0
	for _, completion := range completions {
		providerMeta, _ := completion.Usage["_itbem_provider"].(map[string]any)
		if value, _ := providerMeta["input_sensitive"].(bool); value {
			inputSensitive = true
		}
		if value, _ := providerMeta["output_sensitive"].(bool); value {
			outputSensitive = true
		}
		if value, ok := numericReviewUsage(providerMeta["status_code"]); ok && int(value) > statusCode {
			statusCode = int(value)
		}
	}
	usage["_itbem_provider"] = map[string]any{"finish_reason": "segmented", "input_sensitive": inputSensitive, "output_sensitive": outputSensitive, "status_code": statusCode, "segment_count": len(completions)}
	segmentAudit := make([]map[string]any, 0, len(completions))
	for index, completion := range completions {
		segmentAudit = append(segmentAudit, map[string]any{"segment": index + 1, "response_id": completion.ResponseID, "usage": completion.Usage, "content": completion.Content})
	}
	audit := map[string]any{"segment_count": len(completions), "segments": segmentAudit}
	if aggregate != nil {
		audit["aggregate"] = aggregate
	}
	content, err := json.Marshal(audit)
	if err != nil {
		return Completion{}, fmt.Errorf("code review completion audit could not be encoded")
	}
	return Completion{Provider: provider, Model: model, ResponseID: completions[len(completions)-1].ResponseID, Usage: usage, Content: string(content)}, nil
}

func numericReviewUsage(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, typed >= 0
	case float32:
		return float64(typed), typed >= 0
	case int:
		return float64(typed), typed >= 0
	case int64:
		return float64(typed), typed >= 0
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil && parsed >= 0
	default:
		return 0, false
	}
}

func codeReviewCompletionAudit(completions []Completion, segments []CodeReviewInput) []map[string]any {
	result := make([]map[string]any, 0, len(completions))
	for index, completion := range completions {
		result = append(result, map[string]any{"segment": index + 1, "patch_sha256": segments[index].PatchSHA256, "response_id": completion.ResponseID, "usage": completion.Usage, "content": completion.Content})
	}
	return result
}
