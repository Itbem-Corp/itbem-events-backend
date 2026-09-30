package automation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/utils"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// Explicit projection: no headers, capability, provider body or private reasoning.
type inferenceDiagnostics struct {
	SchemaVersion       int                        `json:"schema_version"`
	Stage               string                     `json:"stage"`
	RequestHash         string                     `json:"request_hash"`
	RequestBytes        int                        `json:"request_bytes"`
	MessageCount        int                        `json:"message_count"`
	MaxCompletionTokens int                        `json:"max_completion_tokens"`
	PolicyHash          string                     `json:"policy_hash"`
	DurationMillis      int64                      `json:"duration_ms"`
	ValidationMillis    int64                      `json:"validation_ms"`
	GatewayStatus       int                        `json:"gateway_status"`
	FailureCode         string                     `json:"failure_code,omitempty"`
	RequestCapture      string                     `json:"request_capture"`
	ResponseCapture     string                     `json:"response_capture"`
	Attempts            []inferenceRouteDiagnostic `json:"attempts"`
}
type inferenceRouteDiagnostic struct {
	Index            int    `json:"index"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	ReasoningEnabled bool   `json:"reasoning_enabled"`
	ReasoningEffort  string `json:"reasoning_effort"`
	DurationMillis   int64  `json:"duration_ms"`
	TimeoutMillis    int64  `json:"timeout_ms"`
	FailureCode      string `json:"failure_code,omitempty"`
	FinishReason     string `json:"finish_reason,omitempty"`
}

func diagnosticFinishReason(completion automationagent.Completion) string {
	outcome, _ := completion.Usage["_itbem_provider"].(map[string]any)
	reason, _ := outcome["finish_reason"].(string)
	switch reason {
	case "stop", "length", "tool_calls", "content_filter", "end_turn", "max_tokens", "safety", "pause_turn":
		return reason
	}
	if reason != "" {
		return "other"
	}
	return ""
}

func safeProviderDiagnostic(err error) string {
	if err == nil {
		return ""
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return "provider_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "request_canceled"
	}
	return automationagent.InferenceFailureCode(err)
}
func startInferenceDiagnostics(request inferenceRequest, scope gatewayInferenceScope) *inferenceDiagnostics {
	body, _ := json.Marshal(request.Messages)
	hash := sha256.Sum256(body)
	return &inferenceDiagnostics{SchemaVersion: 1, RequestHash: hex.EncodeToString(hash[:]), RequestBytes: len(body), MessageCount: len(request.Messages), MaxCompletionTokens: request.MaxCompletionTokens, PolicyHash: scope.PolicyHash, Attempts: []inferenceRouteDiagnostic{}}
}
func persistInferenceDiagnostics(id uuid.UUID, diagnostic *inferenceDiagnostics) {
	if configuration.DB == nil || id == uuid.Nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body, err := json.Marshal(diagnostic)
	if err != nil {
		return
	}
	// Only a known, server-reserved receipt can receive diagnostic metadata.
	// UpdateColumn deliberately avoids GORM's updated_at mutation: diagnostic
	// writes may not modify any accounting or receipt lifecycle column.
	result := configuration.DB.WithContext(ctx).Model(&models.AutomationInferenceReceipt{}).Where("id = ?", id).UpdateColumn("diagnostics_json", string(body))
	if result.Error != nil || result.RowsAffected != 1 {
		slog.Warn("inference diagnostic persistence failed", "receipt_id", id.String(), "stage", diagnostic.Stage)
	}
}

type privateInferenceContent struct {
	Messages    []automationagent.Message `json:"messages,omitempty"`
	FinalAnswer string                    `json:"final_answer,omitempty"`
	Redactions  int                       `json:"redactions"`
}

func inferenceContentKey(taskID, receiptID uuid.UUID, kind string) string {
	return "inference-observations/" + taskID.String() + "/" + receiptID.String() + "/" + kind + ".json"
}
func captureInferenceContent(cfg *models.Config, taskID, receiptID uuid.UUID, kind string, content privateInferenceContent) string {
	if cfg == nil || strings.TrimSpace(cfg.AutomationInputBucket) == "" {
		return "unavailable"
	}
	content.Messages = append([]automationagent.Message(nil), content.Messages...)
	for i := range content.Messages {
		cleaned, count := automationagent.InspectionContent(content.Messages[i].Content, false)
		content.Messages[i].Content = cleaned
		content.Redactions += count
	}
	if content.FinalAnswer != "" {
		cleaned, count := automationagent.InspectionContent(content.FinalAnswer, true)
		content.FinalAnswer = cleaned
		content.Redactions += count
	}
	body, err := json.Marshal(content)
	if err != nil || len(body) > 1<<20 {
		return "too_large"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := gatewayObjectClient(ctx, cfg, cfg.AutomationInputBucket)
	if err != nil {
		return "storage_failed"
	}
	_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cfg.AutomationInputBucket), Key: aws.String(inferenceContentKey(taskID, receiptID, kind)), Body: bytes.NewReader(body), ContentLength: aws.Int64(int64(len(body))), ContentType: aws.String("application/json"), ServerSideEncryption: s3types.ServerSideEncryptionAes256, IfNoneMatch: aws.String("*")})
	if err != nil {
		return "storage_failed"
	}
	return "available"
}

// Content stays in the existing encrypted task stores. These handlers accept
// no object key/URL and disclose content only after a durable body-free audit.
func GetInferenceDiagnostics(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	c.Response().Header().Set("Cache-Control", "private, no-store")
	id, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, 400, "Invalid task", "")
	}
	if configuration.DB == nil {
		return utils.Error(c, 503, "Diagnostics unavailable", "")
	}
	var receipts []models.AutomationInferenceReceipt
	query := configuration.DB.Where("automation_task_id = ?", id)
	if runID := c.QueryParam("run_id"); runID != "" {
		if len(runID) > 64 || safeAgentHistoryRunID(runID) != runID {
			return utils.Error(c, 400, "Invalid run", "")
		}
		query = query.Where("run_id = ?", runID)
	}
	if err := query.Order("created_at DESC").Limit(100).Find(&receipts).Error; err != nil {
		return utils.Error(c, 503, "Diagnostics unavailable", "")
	}
	type item struct {
		ID              uuid.UUID             `json:"receipt_id"`
		CallID          uuid.UUID             `json:"call_id"`
		RunID           string                `json:"run_id"`
		Status          string                `json:"status"`
		Provider        string                `json:"provider"`
		Model           string                `json:"model"`
		CreatedAt       time.Time             `json:"created_at"`
		ResolvedAt      *time.Time            `json:"resolved_at"`
		InputTokens     *int64                `json:"input_tokens"`
		OutputTokens    *int64                `json:"output_tokens"`
		TotalCostMicros *int64                `json:"total_cost_microusd"`
		PricingBasis    string                `json:"pricing_basis"`
		Diagnostics     *inferenceDiagnostics `json:"diagnostics"`
	}
	items := make([]item, 0, len(receipts))
	for _, r := range receipts {
		var d inferenceDiagnostics
		var available *inferenceDiagnostics
		if json.Unmarshal([]byte(r.DiagnosticsJSON), &d) == nil && d.SchemaVersion == 1 {
			available = &d
		}
		row := item{ID: r.ID, CallID: r.CallID, RunID: r.RunID, Status: r.Status, Provider: r.Provider, Model: r.Model, CreatedAt: r.CreatedAt, ResolvedAt: r.ResolvedAt, PricingBasis: r.PricingBasis, Diagnostics: available}
		if r.ResolvedAt != nil && (r.Status == "accepted" || r.Status == "rejected") {
			row.InputTokens, row.OutputTokens = &r.InputTokens, &r.OutputTokens
			if r.PricingBasis != "" && r.PricingBasis != "unpriced" {
				row.TotalCostMicros = &r.TotalCostMicros
			}
		}
		items = append(items, row)
	}
	return utils.Success(c, 200, "Inference diagnostics", items)
}
func InspectInferenceTaskContent(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	c.Response().Header().Set("Cache-Control", "private, no-store")
	id, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, 400, "Invalid task", "")
	}
	cfg, _ := c.Get("config").(*models.Config)
	if cfg == nil || configuration.DB == nil {
		return utils.Error(c, 503, "Content unavailable", "")
	}
	var task models.AutomationTask
	if configuration.DB.First(&task, "id = ?", id).Error != nil {
		return utils.Error(c, 404, "Task not found", "")
	}
	var receipt models.AutomationInferenceReceipt
	receiptID, err := uuid.FromString(c.Param("receipt"))
	if err != nil {
		return utils.Error(c, 400, "Invalid receipt", "")
	}
	if configuration.DB.Where("id = ? AND automation_task_id = ?", receiptID, id).Take(&receipt).Error != nil {
		return utils.Error(c, 404, "Receipt not found", "")
	}
	actor, _ := c.Get("cognito_sub").(string)
	tenant, _ := c.Get("tenant_code").(string)
	entry := models.AuditLog{ActorCognitoSub: actor, TenantCode: tenant, Method: http.MethodPost, Route: c.Path(), ResourceType: "automation_private_content", ResourceID: receiptID.String(), Status: 200, Succeeded: true, OccurredAt: time.Now().UTC()}
	if configuration.DB.Create(&entry).Error != nil {
		return utils.Error(c, 503, "Content audit unavailable", "")
	}
	result := map[string]any{"task_id": id, "receipt_id": receiptID, "request_available": false, "response_available": false}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 15*time.Second)
	defer cancel()
	for _, kind := range []string{"request", "response"} {
		client, e := gatewayObjectClient(ctx, cfg, cfg.AutomationInputBucket)
		if e != nil {
			continue
		}
		object, e := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(cfg.AutomationInputBucket), Key: aws.String(inferenceContentKey(id, receiptID, kind))})
		if e != nil {
			continue
		}
		body := object.Body
		raw, e := io.ReadAll(io.LimitReader(body, (1<<20)+1))
		body.Close()
		if e != nil || len(raw) > 1<<20 {
			continue
		}
		var content privateInferenceContent
		if json.Unmarshal(raw, &content) != nil {
			continue
		}
		if kind == "request" {
			content.FinalAnswer = ""
		} else {
			content.Messages = nil
		}
		result[kind] = content
		result[kind+"_available"] = true
	}
	return utils.Success(c, 200, "Private inference content inspected", result)
}
