package automation

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/modelevaluation"
	"events-stocks/models"
	automationqueue "events-stocks/repositories/automationqueuerepository"
	"events-stocks/repositories/awsrepository"
	"events-stocks/services/automationcost"
	outboxService "events-stocks/services/outbox"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateModelEvaluation admits a versioned platform corpus. The client has no
// route, prompt, credential, signature, worker or budget override fields.
func CreateModelEvaluation(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	cfg, _ := c.Get("config").(*models.Config)
	actor, _ := c.Get("cognito_sub").(string)
	if cfg == nil || configuration.DB == nil || actor == "" || !automationqueue.IsConfigured() || strings.TrimSpace(cfg.AutomationInputBucket) == "" {
		return utils.Error(c, http.StatusServiceUnavailable, "Evaluation unavailable", "")
	}
	var request struct {
		ID            string `json:"id"`
		CorpusVersion string `json:"corpus_version"`
	}
	decoder := json.NewDecoder(io.LimitReader(c.Request().Body, 2049))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !modelevaluation.SupportedCorpus(request.CorpusVersion) {
		return utils.Error(c, http.StatusBadRequest, "Invalid evaluation request", "Only the published synthetic corpus is allowed")
	}
	id, err := uuid.FromString(request.ID)
	if err != nil || id == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid evaluation ID", "")
	}
	var existing models.AutomationModelEvaluation
	if err := configuration.DB.First(&existing, "id = ?", id).Error; err == nil {
		if existing.RequestedBy != actor || existing.CorpusVersion != request.CorpusVersion {
			return utils.Error(c, http.StatusConflict, "Evaluation ID already used", "")
		}
		return utils.Success(c, http.StatusOK, "Evaluation already admitted", existing)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return utils.Error(c, http.StatusServiceUnavailable, "Evaluation unavailable", "")
	}
	cases, instruction, corpusHash, err := modelevaluation.CorpusForVersion(request.CorpusVersion)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Evaluation corpus unavailable", "")
	}
	base, err := automationagent.SyntheticChatMessages("overhead")
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Worker contract unavailable", "")
	}
	overhead := 0
	for _, message := range base {
		overhead += len(message.Role) + len(message.Content) + 64
	}
	// Keep byte framing conservative, including the small placeholder prompt.
	plan, err := modelevaluation.Compile(cases, instruction, overhead, pricingCatalog(cfg))
	if err != nil {
		return utils.Error(c, http.StatusConflict, "Evaluation budget admission rejected", "The existing price catalog must reserve all sixty calls within USD 1")
	}
	batch := models.AutomationModelEvaluation{ID: id, RequestedBy: actor, CorpusVersion: request.CorpusVersion, CorpusHash: corpusHash, BudgetMicros: modelevaluation.MaxBudgetMicros, ReservationMicros: plan.ReservationMicros, PricingJSON: pricingCatalog(cfg), Status: "active", CreatedAt: time.Now().UTC()}
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "itbem:model-evaluation:admission").Error; err != nil {
			return err
		}
		var count int64
		// A completed or failed corpus cannot silently be billed again using a
		// new UUID. A new screening requires a new reviewed corpus version.
		if err := tx.Model(&models.AutomationModelEvaluation{}).Where("status = ? OR (requested_by = ? AND corpus_version = ?)", "active", actor, request.CorpusVersion).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return errEvaluationAdmission
		}
		if err := tx.Create(&batch).Error; err != nil {
			return err
		}
		for index, planned := range plan.Calls {
			taskID := uuid.NewV5(uuid.NamespaceURL, "itbem:model-evaluation:"+id.String()+":"+planned.CaseID+":"+string(planned.Candidate))
			key := "automation/inputs/" + taskID.String() + "/input.json"
			body, err := json.Marshal(automationagent.TaskInput{Prompt: planned.Prompt})
			if err != nil {
				return err
			}
			if err := awsrepository.UploadEncryptedJSON(c.Request().Context(), body, key, cfg.AutomationInputBucket); err != nil {
				return err
			}
			messages, err := automationagent.SyntheticChatMessages(planned.Prompt)
			if err != nil {
				return err
			}
			messageHash, err := modelevaluation.MessageDigest(messages)
			if err != nil {
				return err
			}
			_, routeHash, err := canonicalInferenceRoutes([]models.AutomationAIActionRoute{planned.Route})
			if err != nil {
				return err
			}
			task := models.AutomationTask{ID: taskID, JobID: uuid.NewV5(uuid.NamespaceURL, "itbem:model-evaluation-job:"+taskID.String()), RequestedBy: actor, ModelEvaluationID: &id, CorrelationID: id.String(), Operation: "ai.chat", MaxCompletionTokens: modelevaluation.MaxCompletionTokens, BudgetReservationMicros: planned.ReservationMicros, InputRef: "s3://" + cfg.AutomationInputBucket + "/" + key, Status: "pending"}
			binding := models.AutomationModelEvaluationCall{AutomationTaskID: taskID, EvaluationID: id, Sequence: index + 1, CaseID: planned.CaseID, Candidate: string(planned.Candidate), PromptHash: planned.PromptSHA256, MessagesHash: messageHash, RouteHash: routeHash, ReservationMicros: planned.ReservationMicros, CreatedAt: batch.CreatedAt}
			if err := tx.Create(&task).Error; err != nil {
				return err
			}
			if err := tx.Create(&binding).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return utils.Error(c, http.StatusConflict, "Evaluation admission failed", "No provider call was dispatched")
	}
	return utils.Success(c, http.StatusCreated, "Evaluation admitted; dispatch is explicit and sequential", batch)
}

// DispatchNextModelEvaluation queues exactly one task. Existing queued/running
// work is returned as busy; a failed or ambiguous outcome halts the batch.
func DispatchNextModelEvaluation(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	id, err := uuid.FromString(c.Param("id"))
	if err != nil || id == uuid.Nil || configuration.DB == nil || !automationqueue.IsConfigured() {
		return utils.Error(c, http.StatusBadRequest, "Evaluation unavailable", "")
	}
	var queued models.AutomationTask
	halted, busy, complete := false, false, false
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		var batch models.AutomationModelEvaluation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&batch, "id = ?", id).Error; err != nil {
			return err
		}
		if batch.Status != "active" {
			return errEvaluationAdmission
		}
		var calls []models.AutomationModelEvaluationCall
		if err := tx.Where("evaluation_id = ?", id).Order("sequence ASC").Limit(modelevaluation.MaxCalls + 1).Find(&calls).Error; err != nil {
			return err
		}
		if len(calls) != modelevaluation.MaxCalls {
			return errEvaluationAdmission
		}
		for _, call := range calls {
			var task models.AutomationTask
			if err := tx.First(&task, "id = ?", call.AutomationTaskID).Error; err != nil {
				return err
			}
			switch task.Status {
			case "completed":
				var receipts []models.AutomationInferenceReceipt
				if err := tx.Where("automation_task_id = ?", task.ID).Limit(2).Find(&receipts).Error; err != nil {
					return err
				}
				if len(receipts) != 1 || receipts[0].Status != "accepted" || receipts[0].TotalCostMicros > call.ReservationMicros || receipts[0].PricingBasis == "unpriced" {
					halted = true
				}
			case "queued", "running", "cancel_requested":
				busy = true
				return nil
			case "pending":
				queued = task
			default:
				halted = true
			}
			if halted {
				return tx.Model(&batch).Update("status", "halted").Error
			}
			if queued.ID != uuid.Nil {
				break
			}
		}
		if queued.ID == uuid.Nil {
			complete = true
			return tx.Model(&batch).Update("status", "completed").Error
		}
		message := automationqueue.Message{SchemaVersion: 1, JobID: queued.JobID.String(), TenantCode: "itbem", CorrelationID: queued.CorrelationID, Type: "ai.local.process"}
		message.Payload.TaskID, message.Payload.Operation, message.Payload.InputRef, message.Payload.MaxCompletionTokens, message.Payload.Attempt = queued.ID.String(), queued.Operation, queued.InputRef, queued.MaxCompletionTokens, 1
		result := tx.Model(&models.AutomationTask{}).Where("id = ? AND status = ?", queued.ID, "pending").Update("status", "queued")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errEvaluationAdmission
		}
		ok, err := outboxService.EnqueueAutomationProcess(c.Request().Context(), tx, message)
		if err != nil {
			return err
		}
		if !ok {
			return errEvaluationAdmission
		}
		queued.Status = "queued"
		return nil
	})
	if err != nil || halted {
		return utils.Error(c, http.StatusConflict, "Evaluation dispatch stopped", "Resolve the batch state; no automatic billable retry is permitted")
	}
	if busy {
		return utils.Error(c, http.StatusConflict, "Evaluation already in flight", "")
	}
	if complete {
		return utils.Success(c, http.StatusOK, "Evaluation completed", map[string]string{"id": id.String(), "status": "completed"})
	}
	return utils.Success(c, http.StatusAccepted, "One evaluation task queued", queued)
}

// GetModelEvaluation exposes root-scoped provenance and accounting, never
// credentials, prompts, responses or private reasoning. Final answers remain
// available through the existing task-scoped result inspector.
func GetModelEvaluation(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	id, err := uuid.FromString(c.Param("id"))
	if err != nil || id == uuid.Nil || configuration.DB == nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid evaluation", "")
	}
	var batch models.AutomationModelEvaluation
	if err := configuration.DB.First(&batch, "id = ?", id).Error; err != nil {
		return utils.Error(c, http.StatusNotFound, "Evaluation not found", "")
	}
	type callView struct {
		models.AutomationModelEvaluationCall
		Status              string     `json:"status"`
		RunID               string     `json:"run_id"`
		ReceiptRunID        string     `json:"receipt_run_id"`
		ReceiptID           *uuid.UUID `json:"receipt_id"`
		ReceiptStatus       string     `json:"receipt_status"`
		PolicyHash          string     `json:"policy_hash"`
		PolicyRevision      int64      `json:"policy_revision"`
		SealedRoutesJSON    string     `json:"sealed_routes_json"`
		WorkerID            string     `json:"worker_id"`
		AgentKey            string     `json:"agent_key"`
		MachineID           string     `json:"machine_id"`
		ErrorMessage        string     `json:"error_message"`
		FinishReason        string     `json:"finish_reason"`
		ActualProvider      string     `json:"actual_provider"`
		ActualModel         string     `json:"actual_model"`
		InputTokens         int64      `json:"input_tokens"`
		OutputTokens        int64      `json:"output_tokens"`
		CachedInputTokens   *int64     `json:"cached_input_tokens"`
		CacheWriteTokens    *int64     `json:"cache_write_tokens"`
		ReasoningTokens     *int64     `json:"reasoning_tokens"`
		UsageJSON           string     `json:"-"`
		TotalCostMicros     int64      `json:"total_cost_microusd"`
		PricingBasis        string     `json:"pricing_basis"`
		PricingSnapshotJSON string     `json:"pricing_snapshot_json"`
		LatencyMS           int64      `json:"gateway_latency_ms"`
		ResultAvailable     bool       `json:"result_available"`
	}
	var calls []callView
	err = configuration.DB.Table("automation_model_evaluation_calls AS evaluation_call").Select(`evaluation_call.*,
		task.status, task.run_id, COALESCE(receipt.run_id, '') AS receipt_run_id, receipt.id AS receipt_id, COALESCE(receipt.status, '') AS receipt_status,
		COALESCE(receipt.policy_snapshot_hash, '') AS policy_hash, COALESCE(policy.policy_revision, 0) AS policy_revision,
		COALESCE(policy.routes_json::text, '[]') AS sealed_routes_json,
		COALESCE(receipt.worker_id, '') AS worker_id, COALESCE(receipt.agent_key, '') AS agent_key, COALESCE(receipt.machine_id, '') AS machine_id,
		COALESCE(task.error_message, '') AS error_message,
		COALESCE(receipt.usage_json::jsonb #>> '{_itbem_provider,finish_reason}', '') AS finish_reason,
		COALESCE(receipt.provider, '') AS actual_provider, COALESCE(receipt.model, '') AS actual_model,
		COALESCE(receipt.input_tokens, 0) AS input_tokens, COALESCE(receipt.output_tokens, 0) AS output_tokens,
		COALESCE(receipt.usage_json::text, '{}') AS usage_json,
		COALESCE(receipt.total_cost_micros, 0) AS total_cost_micros,
		COALESCE(receipt.pricing_basis, '') AS pricing_basis, COALESCE(receipt.pricing_snapshot_json::text, '{}') AS pricing_snapshot_json,
		COALESCE((EXTRACT(EPOCH FROM (receipt.resolved_at - receipt.created_at))*1000)::bigint, 0) AS latency_ms,
		(task.output_ref <> '') AS result_available`).
		Joins("JOIN automation_tasks AS task ON task.id = evaluation_call.automation_task_id").
		Joins("LEFT JOIN automation_inference_receipts AS receipt ON receipt.automation_task_id = task.id").
		Joins("LEFT JOIN automation_inference_attempt_policies AS policy ON policy.automation_task_id = receipt.automation_task_id AND policy.run_id = receipt.run_id").
		Where("evaluation_call.evaluation_id = ?", id).Order("evaluation_call.sequence ASC").Limit(modelevaluation.MaxCalls + 1).Scan(&calls).Error
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Evaluation provenance unavailable", "")
	}
	// One extra row detects join expansion instead of silently dropping a task
	// at the export boundary. Partial batches remain inspectable.
	if len(calls) > modelevaluation.MaxCalls {
		return utils.Error(c, http.StatusServiceUnavailable, "Evaluation provenance ambiguous", "")
	}
	seenTasks := make(map[uuid.UUID]bool, len(calls))
	for _, call := range calls {
		if call.AutomationTaskID == uuid.Nil || seenTasks[call.AutomationTaskID] {
			return utils.Error(c, http.StatusServiceUnavailable, "Evaluation provenance ambiguous", "")
		}
		seenTasks[call.AutomationTaskID] = true
	}
	for i := range calls {
		calls[i].CachedInputTokens, calls[i].ReasoningTokens, calls[i].CacheWriteTokens = evaluationOptionalUsage(calls[i].ActualProvider, calls[i].ActualModel, calls[i].UsageJSON)
	}
	return utils.Success(c, http.StatusOK, "Evaluation provenance", map[string]any{"batch": batch, "calls": calls})
}

// Optional counters are evidence only when the provider actually reported them.
// Receipt columns default to zero, which cannot distinguish absence from zero.
func evaluationOptionalUsage(provider, model, raw string) (*int64, *int64, *int64) {
	var usage map[string]any
	if json.Unmarshal([]byte(raw), &usage) != nil {
		return nil, nil, nil
	}
	profile, err := automationcost.Profile(provider, model, usage, 0)
	if err != nil {
		return nil, nil, nil
	}
	return profile.CachedInputTokens, profile.ReasoningTokens, profile.CacheWriteTokens
}
