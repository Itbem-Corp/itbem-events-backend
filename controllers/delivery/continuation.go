package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"sort"
	"strings"
	"time"

	"events-stocks/internal/automationagent"
	"events-stocks/models"
	awsrepository "events-stocks/repositories/awsrepository"
	"events-stocks/services/deliveryworkflow"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func continuationAfterAction(action deliveryworkflow.Action) string {
	switch action {
	case deliveryworkflow.ActionApprovePlan, deliveryworkflow.ActionRequestCodeChanges, deliveryworkflow.ActionRequestQAChanges:
		return "implementation"
	case deliveryworkflow.ActionRequestPlanChanges:
		return "plan"
	case deliveryworkflow.ActionApproveCodeReview:
		return "preview"
	case deliveryworkflow.ActionPreviewReady:
		return "qa"
	case deliveryworkflow.ActionApproveQA:
		return "summary"
	}
	return ""
}

func scheduleContinuation(tx *gorm.DB, item models.DeliveryWorkItem, phase, actor, grant string) error {
	return tx.Create(&models.DeliveryContinuation{WorkItemID: item.ID, Epoch: item.AutomationEpoch, Phase: phase,
		RequestedBy: actor, PublicationGrantID: grant, Status: "pending", AvailableAt: time.Now().UTC()}).Error
}

// StartContinuationDispatcher is API-owned. A claim expires so another API
// replica can resume it; task.ContinuationID prevents a second admission after
// a crash between the task/outbox commit and marking the instruction dispatched.
func StartContinuationDispatcher(ctx context.Context, db *gorm.DB, cfg *models.Config) {
	if db == nil || cfg == nil || cfg.AutomationInputBucket == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			if err := dispatchContinuation(ctx, db, cfg); err != nil && ctx.Err() == nil {
				slog.Warn("delivery continuation unavailable", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// DispatchContinuationsOnce runs one synchronous continuation reconciliation
// tick. The normal API supervisor uses StartContinuationDispatcher, while a
// deterministic operator probe or integration harness can call this boundary
// without waiting for the five-second ticker. It does not bypass any state,
// lease, budget or human-gate validation; it only performs the same durable
// claim/reconcile operation once.
func DispatchContinuationsOnce(ctx context.Context, db *gorm.DB, cfg *models.Config) error {
	return dispatchContinuation(ctx, db, cfg)
}

// ScheduleReadyDecompositionPlansOnce materialises at most one durable plan
// instruction per ready decomposition child. It is intentionally separate
// from provider admission so a scheduler tick can be tested without creating
// a queue message or calling a model. A child is eligible only when its
// request is planned, it is still in planning, every dependency is released,
// and neither a plan task nor an active plan continuation already exists.
func ScheduleReadyDecompositionPlansOnce(db *gorm.DB) error {
	return scheduleReadyDecompositionPlans(db)
}

func scheduleReadyDecompositionPlans(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := markDecompositionDependencyBlocks(tx); err != nil {
			return err
		}
		if err := markDecompositionPlanConflicts(tx); err != nil {
			return err
		}
		var candidates []models.DeliveryWorkItem
		if err := tx.Where(`
			request_id IS NOT NULL AND state = ?
			AND NOT (agent_progress = 'blocked' AND blocked_reason LIKE 'Conflicto de cambios en %')
			AND NOT EXISTS (
				SELECT 1 FROM delivery_work_item_dependencies dependency
				JOIN delivery_work_items dependency_item ON dependency_item.id = dependency.depends_on_work_item_id
				WHERE dependency.work_item_id = delivery_work_items.id AND dependency_item.state <> ?
			)
			AND NOT EXISTS (
				SELECT 1 FROM automation_tasks plan_task
				WHERE plan_task.delivery_work_item_id = delivery_work_items.id AND plan_task.operation = ?
			)
			AND NOT EXISTS (
				SELECT 1 FROM delivery_continuations plan_intent
				WHERE plan_intent.work_item_id = delivery_work_items.id AND plan_intent.epoch = delivery_work_items.automation_epoch
				AND plan_intent.phase = ? AND plan_intent.status IN ?
			)
		`, deliveryworkflow.StatePlanning, deliveryworkflow.StateReleased, "delivery.plan", "plan", []string{"pending", "claimed", "dispatched"}).
			Order("created_at ASC").Limit(48).Find(&candidates).Error; err != nil {
			return err
		}
		for _, candidate := range candidates {
			var item models.DeliveryWorkItem
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&item, candidate.ID).Error; err != nil {
				return err
			}
			if item.RequestID == nil || item.State != deliveryworkflow.StatePlanning || (item.AgentProgress == "blocked" && strings.HasPrefix(item.BlockedReason, "Conflicto de cambios en ")) {
				continue
			}
			var request models.DeliveryRequest
			if err := tx.Select("id", "status").First(&request, *item.RequestID).Error; err != nil {
				return err
			}
			if request.Status != "planned" {
				continue
			}
			var pendingDependencies int64
			if err := tx.Model(&models.DeliveryWorkItemDependency{}).
				Joins("JOIN delivery_work_items AS dependency_items ON dependency_items.id = delivery_work_item_dependencies.depends_on_work_item_id").
				Where("delivery_work_item_dependencies.work_item_id = ? AND dependency_items.state <> ?", item.ID, deliveryworkflow.StateReleased).
				Count(&pendingDependencies).Error; err != nil {
				return err
			}
			if pendingDependencies > 0 {
				continue
			}
			var existingTask int64
			if err := tx.Model(&models.AutomationTask{}).Where("delivery_work_item_id = ? AND operation = ?", item.ID, "delivery.plan").Count(&existingTask).Error; err != nil {
				return err
			}
			if existingTask > 0 {
				continue
			}
			var existingIntent int64
			if err := tx.Model(&models.DeliveryContinuation{}).
				Where("work_item_id = ? AND epoch = ? AND phase = ? AND status IN ?", item.ID, item.AutomationEpoch, "plan", []string{"pending", "claimed", "dispatched"}).
				Count(&existingIntent).Error; err != nil {
				return err
			}
			if existingIntent > 0 {
				continue
			}
			if err := tx.Create(&models.DeliveryContinuation{WorkItemID: item.ID, Epoch: item.AutomationEpoch, Phase: "plan", RequestedBy: item.RequestedBy, Status: "pending", AvailableAt: time.Now().UTC()}).Error; err != nil {
				return err
			}
			if err := tx.Model(&item).Updates(map[string]any{"agent_progress": "queued", "blocked_reason": ""}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

type planConflictKey struct {
	RequestID     uuid.UUID
	RepositoryRef string
	File          string
}

type planConflictEntry struct {
	WorkItemID uuid.UUID
	Key        planConflictKey
}

// planChangedFiles returns only the conservative subset that can be safely
// compared across independent DAG roots. A plan with exactly one changed
// repository and explicit repository-relative files is comparable; ambiguous
// multi-repository plans are left for the repository/PR integration to resolve
// rather than being blocked by a guess.
func planChangedFiles(item models.DeliveryWorkItem) []planConflictEntry {
	if item.RequestID == nil || strings.TrimSpace(item.PlanJSON) == "" || strings.TrimSpace(item.PlanJSON) == "{}" {
		return nil
	}
	var value struct {
		FilesImpacted    []string `json:"files_impacted"`
		RepositoryImpact []struct {
			Reference string `json:"reference"`
			Impact    string `json:"impact"`
		} `json:"repository_impact"`
	}
	if err := json.Unmarshal([]byte(item.PlanJSON), &value); err != nil {
		return nil
	}
	changed := make([]string, 0, len(value.RepositoryImpact))
	for _, repository := range value.RepositoryImpact {
		if strings.EqualFold(strings.TrimSpace(repository.Impact), "changes") && strings.TrimSpace(repository.Reference) != "" {
			changed = append(changed, strings.TrimSpace(repository.Reference))
		}
	}
	if len(changed) != 1 || len(value.FilesImpacted) == 0 {
		return nil
	}
	files := make([]planConflictEntry, 0, len(value.FilesImpacted))
	seen := make(map[string]struct{}, len(value.FilesImpacted))
	for _, rawFile := range value.FilesImpacted {
		file := strings.ReplaceAll(strings.TrimSpace(rawFile), "\\", "/")
		file = strings.TrimPrefix(file, "./")
		unsafe := file == "" || strings.HasPrefix(file, "/") || strings.Contains(file, "\x00")
		for _, segment := range strings.Split(file, "/") {
			if segment == ".." || segment == ".git" || strings.HasPrefix(segment, ".env") {
				unsafe = true
			}
		}
		if unsafe {
			continue
		}
		clean := path.Clean(file)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			continue
		}
		if _, duplicate := seen[clean]; duplicate {
			continue
		}
		seen[clean] = struct{}{}
		files = append(files, planConflictEntry{WorkItemID: item.ID, Key: planConflictKey{RequestID: *item.RequestID, RepositoryRef: changed[0], File: clean}})
	}
	return files
}

// decompositionPlanConflicts finds overlap between already-proposed plans in
// one decomposition. It deliberately does not merge, rewrite or choose a
// winner: both owners must reconcile the branches before implementation can
// proceed.
func decompositionPlanConflicts(items []models.DeliveryWorkItem) map[uuid.UUID]string {
	entries := make([]planConflictEntry, 0)
	for _, item := range items {
		entries = append(entries, planChangedFiles(item)...)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Key.RequestID != entries[j].Key.RequestID {
			return entries[i].Key.RequestID.String() < entries[j].Key.RequestID.String()
		}
		if entries[i].Key.RepositoryRef != entries[j].Key.RepositoryRef {
			return entries[i].Key.RepositoryRef < entries[j].Key.RepositoryRef
		}
		if entries[i].Key.File != entries[j].Key.File {
			return entries[i].Key.File < entries[j].Key.File
		}
		return entries[i].WorkItemID.String() < entries[j].WorkItemID.String()
	})
	firstByKey := make(map[planConflictKey]uuid.UUID)
	conflicts := make(map[uuid.UUID]string)
	for _, entry := range entries {
		first, exists := firstByKey[entry.Key]
		if !exists {
			firstByKey[entry.Key] = entry.WorkItemID
			continue
		}
		if first == entry.WorkItemID {
			continue
		}
		reason := fmt.Sprintf("Conflicto de cambios en %s (%s) con otra rama del mismo DAG; reconcilia las ramas antes de continuar.", entry.Key.RepositoryRef, entry.Key.File)
		conflicts[first] = reason
		conflicts[entry.WorkItemID] = reason
	}
	return conflicts
}

func markDecompositionPlanConflicts(tx *gorm.DB) error {
	var items []models.DeliveryWorkItem
	if err := tx.Model(&models.DeliveryWorkItem{}).
		Joins("JOIN delivery_requests request ON request.id = delivery_work_items.request_id").
		Where("delivery_work_items.request_id IS NOT NULL AND delivery_work_items.state = ? AND request.status = ? AND delivery_work_items.plan_json <> ?", deliveryworkflow.StatePlanning, "planned", "{}").
		Order("delivery_work_items.created_at ASC").Find(&items).Error; err != nil {
		return err
	}
	conflicts := decompositionPlanConflicts(items)
	for _, item := range items {
		reason, hasConflict := conflicts[item.ID]
		if hasConflict {
			if err := tx.Model(&models.DeliveryWorkItem{}).Where("id = ?", item.ID).Updates(map[string]any{"agent_progress": "blocked", "blocked_reason": reason}).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.DeliveryContinuation{}).Where("work_item_id = ? AND phase = ? AND status = ?", item.ID, "plan", "pending").Update("status", "superseded").Error; err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(item.BlockedReason, "Conflicto de cambios en ") {
			if err := tx.Model(&models.DeliveryWorkItem{}).Where("id = ? AND agent_progress = ?", item.ID, "blocked").Updates(map[string]any{"agent_progress": "", "blocked_reason": ""}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// markDecompositionDependencyBlocks makes an unsatisfied terminal
// prerequisite visible without changing the work item state to a terminal
// value. A later reconciliation can clear the reason when the prerequisite is
// repaired/released, so this remains an operator-recoverable condition rather
// than a hidden deadlock or an irreversible automatic decision.
func markDecompositionDependencyBlocks(tx *gorm.DB) error {
	var blocked []models.DeliveryWorkItem
	if err := tx.Model(&models.DeliveryWorkItem{}).
		Joins("JOIN delivery_requests request ON request.id = delivery_work_items.request_id").
		Joins("JOIN delivery_work_item_dependencies dependency ON dependency.work_item_id = delivery_work_items.id").
		Joins("JOIN delivery_work_items dependency_item ON dependency_item.id = dependency.depends_on_work_item_id").
		Where("delivery_work_items.request_id IS NOT NULL AND delivery_work_items.state = ? AND request.status = ? AND dependency_item.state IN ?", deliveryworkflow.StatePlanning, "planned", []string{deliveryworkflow.StateBlocked, deliveryworkflow.StateCancelled}).
		Distinct().Order("delivery_work_items.created_at ASC").Limit(96).Find(&blocked).Error; err != nil {
		return err
	}
	for _, candidate := range blocked {
		result := tx.Model(&models.DeliveryWorkItem{}).
			Where("id = ? AND state = ? AND agent_progress IN ?", candidate.ID, deliveryworkflow.StatePlanning, []string{"", "blocked"}).
			Updates(map[string]any{"agent_progress": "blocked", "blocked_reason": "Una dependencia está bloqueada o cancelada; resuélvela antes de continuar."})
		if result.Error != nil {
			return result.Error
		}
	}
	return nil
}

func dispatchContinuation(ctx context.Context, db *gorm.DB, cfg *models.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	db = db.WithContext(ctx)
	if err := scheduleReadyDecompositionPlans(db); err != nil {
		return err
	}
	var intent models.DeliveryContinuation
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Human transitions advance the work-item epoch and intentionally leave
		// older durable intents visible for audit. Reconcile those stale intents
		// in one bounded pass before selecting new work; otherwise an old intent
		// can consume one scheduler tick at a time and starve the current phase
		// after a rapid sequence of gates or a recovered API replica.
		var stale []models.DeliveryContinuation
		if err := tx.Joins("JOIN delivery_work_items ON delivery_work_items.id = delivery_continuations.work_item_id").
			Where("delivery_continuations.status IN ? AND delivery_continuations.epoch <> delivery_work_items.automation_epoch", []string{"pending", "claimed", "dispatched"}).
			Select("delivery_continuations.*").Limit(256).Find(&stale).Error; err != nil {
			return err
		}
		for _, candidate := range stale {
			if err := tx.Model(&models.DeliveryContinuation{}).
				Where("id = ? AND status IN ?", candidate.ID, []string{"pending", "claimed", "dispatched"}).
				Update("status", "superseded").Error; err != nil {
				return err
			}
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status IN ? AND available_at <= ?", []string{"pending", "claimed", "dispatched"}, time.Now().UTC()).Order("available_at ASC").First(&intent).Error; err != nil {
			return err
		}
		return tx.Model(&intent).Updates(map[string]any{"status": "claimed", "available_at": time.Now().UTC().Add(2 * time.Minute)}).Error
	})
	if err == gorm.ErrRecordNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	var item models.DeliveryWorkItem
	if err := db.First(&item, intent.WorkItemID).Error; err != nil {
		return err
	}
	if item.AutomationEpoch != intent.Epoch {
		return db.Model(&intent).Update("status", "superseded").Error
	}
	if intent.Phase == "preview" {
		return advanceAvailablePreview(db, intent)
	}
	var task models.AutomationTask
	err = db.Where("continuation_id = ?", intent.ID).First(&task).Error
	if err == gorm.ErrRecordNotFound {
		// Ordinary queue contention is a wait, not a failed attempt. In
		// particular, multi-repository publication grants share one phase slot.
		var active int64
		if err := db.Model(&models.AutomationTask{}).Where("delivery_work_item_id = ? AND operation = ? AND status IN ?", item.ID, agentRunSpecs[intent.Phase].operation, []string{"queued", "running", "cancel_requested"}).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return db.Model(&intent).Where("status <> ?", "superseded").Updates(map[string]any{"status": "pending", "available_at": time.Now().UTC().Add(time.Minute)}).Error
		}
		// Share the same admission, state checks, private input construction and
		// budget reservations as explicit runs. No synthetic user authentication.
		recorder := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/internal/continuation", nil).WithContext(ctx), recorder)
		c.Set("config", cfg)
		err = enqueueAgentRun(c, item.ID, intent.RequestedBy, agentRunRequest{Phase: intent.Phase, PublicationGrantID: intent.PublicationGrantID}, &intent)
		if err != nil || recorder.Code >= 300 {
			if reason := continuationBudgetBlock(recorder.Code, recorder.Body.Bytes()); reason != "" {
				return finishContinuation(db, intent, "blocked", reason)
			}
			intent.Attempts++
			status := "pending"
			if intent.Attempts >= 3 {
				status = "blocked"
			}
			if saveErr := db.Model(&intent).Where("status <> ?", "superseded").Updates(map[string]any{"status": status, "attempts": intent.Attempts, "available_at": time.Now().UTC().Add(time.Minute)}).Error; saveErr != nil {
				return saveErr
			}
			if status == "blocked" {
				return finishContinuation(db, intent, "blocked", "No se pudo iniciar el siguiente paso: revisa configuración, contexto, presupuesto y ejecuciones activas.")
			}
			return nil
		}
		return db.Model(&intent).Where("status <> ?", "superseded").Updates(map[string]any{"status": "dispatched", "available_at": time.Now().UTC().Add(5 * time.Second)}).Error
	}
	if err != nil {
		return err
	}
	if task.Status == "queued" || task.Status == "running" || task.Status == "cancel_requested" {
		return db.Model(&intent).Where("status <> ?", "superseded").Updates(map[string]any{"status": "dispatched", "available_at": time.Now().UTC().Add(5 * time.Second)}).Error
	}
	if task.Status != "completed" {
		reason := "La ejecución necesita atención. Revisa la evidencia privada antes de reintentar."
		if strings.HasPrefix(task.ErrorMessage, "Agent requested assistance:") {
			reason = "El agente necesita una aclaración: " + strings.TrimPrefix(task.ErrorMessage, "Agent requested assistance:")
		}
		return finishContinuation(db, intent, "blocked", reason)
	}
	output, err := readContinuationResult(ctx, cfg, task)
	if err != nil {
		return err
	} // retry a storage outage, never repeat inference
	if err := completeContinuation(db, intent, task, output); err != nil {
		if errors.Is(err, errPendingDependencies) {
			return db.Model(&intent).Where("status <> ?", "superseded").Updates(map[string]any{"status": "dispatched", "available_at": time.Now().UTC().Add(time.Minute)}).Error
		}
		if intent.Attempts >= 2 {
			return finishContinuation(db, intent, "blocked", "El resultado no cumple el contrato de la fase. Revisa la evidencia privada; no se repetirá la llamada al modelo.")
		}
		return db.Model(&intent).Where("status <> ?", "superseded").Updates(map[string]any{"status": "dispatched", "attempts": intent.Attempts + 1, "available_at": time.Now().UTC().Add(time.Minute)}).Error
	}
	return nil
}

// Only expose known admission codes, never arbitrary internal response details.
func continuationBudgetBlock(status int, body []byte) string {
	if status != http.StatusConflict {
		return ""
	}
	var response struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil {
		return ""
	}
	switch response.Error {
	case "task_budget_insufficient":
		return "El presupuesto de esta tarea no alcanza para reservar la ejecución completa. Revisa Costos, ajusta el límite o espera a que finalicen las ejecuciones activas; después solicita continuar. No se ha llamado al modelo."
	case "project_budget_insufficient":
		return "El presupuesto mensual del proyecto no alcanza para reservar esta ejecución. Revisa el límite del proyecto o espera a que se liberen reservas; después solicita continuar. No se ha llamado al modelo."
	}
	return ""
}

func readContinuationResult(ctx context.Context, cfg *models.Config, task models.AutomationTask) (map[string]any, error) {
	bucket, key, err := automationagent.ParsePrivateReference(task.OutputRef)
	if err != nil || bucket != cfg.AutomationOutputBucket || !strings.HasPrefix(key, "automation/"+task.ID.String()+"/") {
		return nil, fmt.Errorf("invalid continuation evidence reference")
	}
	body, err := awsrepository.GetS3Object(ctx, key, bucket)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, 10<<20+1))
	if err != nil || len(raw) > 10<<20 {
		return nil, fmt.Errorf("invalid continuation result size")
	}
	var output map[string]any
	if json.Unmarshal(raw, &output) != nil || output["task_id"] != task.ID.String() || output["operation"] != task.Operation {
		return nil, fmt.Errorf("continuation result identity mismatch")
	}
	return output, nil
}

func finishContinuation(db *gorm.DB, intent models.DeliveryContinuation, progress, reason string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var item models.DeliveryWorkItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&item, intent.WorkItemID).Error; err != nil {
			return err
		}
		var current models.DeliveryContinuation
		if err := tx.First(&current, intent.ID).Error; err != nil {
			return err
		}
		if current.Status == "superseded" || current.Status == "done" || item.AutomationEpoch != intent.Epoch {
			return nil
		}
		if intent.Phase == "chat" {
			// A failed informational answer is visible and retryable from the
			// conversation, but it never turns the delivery itself into a blocked
			// run or asks for an unrelated approval. Keep the technical failure on
			// its task while closing this one-shot chat continuation.
			message := "No pude preparar una respuesta informativa con el formato requerido. La pregunta no cambió el plan ni inició trabajo; puedes reformularla o revisar el intento técnico si persiste."
			if err := recordContinuationMessage(tx, intent, "answered", message); err != nil {
				return err
			}
			return tx.Model(&intent).Update("status", "done").Error
		}
		if intent.Phase != "chat" {
			if err := tx.Model(&models.DeliveryWorkItem{}).Where("id = ? AND automation_epoch = ?", intent.WorkItemID, intent.Epoch).Updates(map[string]any{"agent_progress": progress, "blocked_reason": reason}).Error; err != nil {
				return err
			}
		}
		if err := recordContinuationMessage(tx, intent, progress, reason); err != nil {
			return err
		}
		return tx.Model(&intent).Update("status", progress).Error
	})
}

func completeContinuation(db *gorm.DB, intent models.DeliveryContinuation, task models.AutomationTask, output map[string]any) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var item models.DeliveryWorkItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&item, intent.WorkItemID).Error; err != nil {
			return err
		}
		if item.AutomationEpoch != intent.Epoch {
			return tx.Model(&intent).Update("status", "superseded").Error
		}
		var current models.DeliveryContinuation
		if err := tx.First(&current, intent.ID).Error; err != nil {
			return err
		}
		if current.Status == "superseded" || current.Status == "done" {
			return nil
		}
		var action deliveryworkflow.Action
		switch intent.Phase {
		case "plan":
			structured, ok := output["structured_result"].(map[string]any)
			if !ok {
				return fmt.Errorf("missing plan evidence")
			}
			if err := validatePlanStructure(structured); err != nil {
				return err
			}
			if err := validatePlanMatchesFrozenRepositoryTopology(tx, item.ID, structured); err != nil {
				return err
			}
			if err := requireReleasedDependencies(tx, item.ID); err != nil {
				return err
			}
			encoded, _ := json.Marshal(structured)
			var count int64
			if err := tx.Model(&models.DeliveryPlan{}).Where("work_item_id = ?", item.ID).Count(&count).Error; err != nil {
				return err
			}
			plan := models.DeliveryPlan{WorkItemID: item.ID, Version: int(count) + 1, Status: "proposed", Summary: fmt.Sprint(structured["summary"]), StructuredJSON: string(encoded), ContextDigest: contextDigest(tx, item.ID), ProposedBy: "agent:" + task.ID.String()}
			if err := tx.Create(&plan).Error; err != nil {
				return err
			}
			item.PlanJSON = string(encoded)
			action = deliveryworkflow.ActionSubmitPlan
		case "implementation":
			var changes []models.DeliveryChangeSet
			if err := tx.Where("work_item_id = ? AND created_by = ?", item.ID, "itbem-local-agent").Find(&changes).Error; err != nil {
				return err
			}
			required, err := codeReviewRequiredRepositories(item.PlanJSON)
			if err != nil {
				return err
			}
			for ref := range required {
				found := false
				for _, change := range changes {
					var metadata map[string]any
					_ = json.Unmarshal([]byte(change.MetadataJSON), &metadata)
					if change.RepositoryRef == ref && trustedImplementationChangeSet(change) && metadata["automation_task_id"] == task.ID.String() {
						found = true
					}
				}
				if !found {
					return fmt.Errorf("implementation has not verified every changed repository")
				}
			}
			if len(required) == 0 {
				return fmt.Errorf("implementation requires explicit repository coverage")
			}
			action = deliveryworkflow.ActionSubmitCodeReview
		case "qa":
			structured, _ := output["structured_result"].(map[string]any)
			if structured["verdict"] != "passed" {
				item.AgentProgress = "blocked"
				item.BlockedReason = "QA no verificó el resultado; revisa fallos y cobertura antes de continuar."
				if err := recordContinuationMessage(tx, intent, "blocked", item.BlockedReason); err != nil {
					return err
				}
				if err := tx.Save(&item).Error; err != nil {
					return err
				}
				return tx.Model(&intent).Update("status", "blocked").Error
			}
			action = deliveryworkflow.ActionSubmitQA
		case "summary":
			structured, _ := output["structured_result"].(map[string]any)
			encoded, _ := json.Marshal(structured)
			verified, err := automationagent.ParseDeliverySummary(string(encoded))
			if err != nil {
				return err
			}
			executive, _ := json.Marshal(verified["executive"])
			technical, _ := json.Marshal(verified["technical"])
			release := models.DeliveryRelease{ProjectID: item.ProjectID, WorkItemID: item.ID, Status: "ready", ExecutiveJSON: string(executive), TechnicalJSON: string(technical), ReportRef: task.OutputRef}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "work_item_id"}}, DoUpdates: clause.AssignmentColumns([]string{"status", "executive_json", "technical_json", "report_ref", "updated_at"})}).Create(&release).Error; err != nil {
				return err
			}
		case "chat":
			// Chat is a read-only continuation. Its answer is durable and visible
			// in the work-item timeline, but it must never advance state, satisfy a
			// gate, alter the plan, or overwrite progress from another phase.
			structured, ok := output["structured_result"].(map[string]any)
			if !ok {
				return fmt.Errorf("missing delivery chat evidence")
			}
			answer := humanizeInformationalChatAnswer(fmt.Sprint(structured["answer"]))
			if answer == "" || answer == "<nil>" {
				return fmt.Errorf("delivery chat answer is empty")
			}
			if err := recordContinuationMessage(tx, intent, "answered", answer, structured); err != nil {
				return err
			}
			return tx.Model(&intent).Update("status", "done").Error
		case "publish":
			// Publication remains gated by its exact, short-lived human grant.
			item.AgentProgress = "waiting_for_preview"
			if err := tx.Save(&item).Error; err != nil {
				return err
			}
			return tx.Model(&intent).Update("status", "done").Error
		default:
			return fmt.Errorf("unsupported continuation phase")
		}
		if action != "" {
			if err := requireCompletedAgentArtifact(tx, &item, action); err != nil {
				return err
			}
		}
		if action != "" {
			if err := deliveryworkflow.Advance(&item, action, nil, time.Now().UTC()); err != nil {
				return err
			}
			item.AutomationEpoch++
		}
		item.AgentProgress = "waiting_for_user"
		item.BlockedReason = ""
		if err := recordContinuationMessage(tx, intent, "ready", "El resultado de esta fase está disponible con su evidencia. Revisa la decisión correspondiente para continuar; no he aprobado ni publicado una entrega."); err != nil {
			return err
		}
		if err := tx.Save(&item).Error; err != nil {
			return err
		}
		return tx.Model(&intent).Update("status", "done").Error
	})
}

// humanizeInformationalChatAnswer prevents an otherwise valid but raw model
// token from leaking a control-plane identifier into the operator experience.
// It only rewrites an exact known token and never performs the corresponding
// transition; the normal human gate remains the sole authority for that action.
func humanizeInformationalChatAnswer(answer string) string {
	trimmed := strings.TrimSpace(answer)
	if label, ok := map[string]string{
		"approve_plan":         "La decisión pendiente es aprobar el plan. Debe tomarla una persona autorizada; el agente no la ha aplicado.",
		"request_plan_changes": "La alternativa es solicitar cambios al plan. Debe decidirla una persona autorizada; el agente no la ha aplicado.",
		"approve_code_review":  "La decisión pendiente es aprobar la revisión de código. Debe tomarla una persona autorizada; el agente no la ha aplicado.",
		"request_code_changes": "La alternativa es solicitar cambios al código. Debe decidirla una persona autorizada; el agente no la ha aplicado.",
		"approve_qa_review":    "La decisión pendiente es aprobar la validación. Debe tomarla una persona autorizada; el agente no la ha aplicado.",
		"request_qa_changes":   "La alternativa es solicitar cambios a la validación. Debe decidirla una persona autorizada; el agente no la ha aplicado.",
		"approve_release":      "La decisión pendiente es autorizar la entrega. Debe tomarla una persona autorizada; el agente no la ha aplicado.",
	}[trimmed]; ok {
		return label
	}
	return trimmed
}

func recordContinuationMessage(tx *gorm.DB, intent models.DeliveryContinuation, status, body string, details ...map[string]any) error {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	intentName := "agent_update"
	effect := "workflow_observation"
	if status == "answered" {
		intentName = "agent_answer"
		effect = "informational"
	}
	receipt := map[string]any{"version": 1, "status": status, "intent": intentName, "effect": effect}
	if len(details) > 0 && details[0] != nil {
		for _, key := range []string{"next_steps", "questions"} {
			if value, present := details[0][key]; present {
				receipt[key] = value
			}
		}
		if repairs, present := details[0]["_harness_repairs"]; present {
			receipt["repairs"] = repairs
		}
	}
	receiptJSON, _ := json.Marshal(receipt)
	message := models.DeliveryMessage{ID: uuid.NewV5(uuid.NamespaceURL, "delivery-continuation/"+intent.ID.String()+"/"+status), WorkItemID: intent.WorkItemID, Phase: intent.Phase, AuthorType: "agent", AuthorID: "itbem-runtime", Body: body, Intent: intentName, Effect: effect, ReceiptJSON: string(receiptJSON)}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&message).Error
}

// Preview readiness is observed, never invented by a model. Existing trusted
// CI/deployment integrations must record the preview of the reviewed branch.
// This does not grant deployment credentials or authorize production changes.
func advanceAvailablePreview(db *gorm.DB, intent models.DeliveryContinuation) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var item models.DeliveryWorkItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&item, intent.WorkItemID).Error; err != nil {
			return err
		}
		if item.AutomationEpoch != intent.Epoch || item.State != deliveryworkflow.StatePreviewPending {
			return tx.Model(&intent).Update("status", "superseded").Error
		}
		var changes []models.DeliveryChangeSet
		if err := tx.Where("work_item_id = ?", item.ID).Order("created_at DESC").Find(&changes).Error; err != nil {
			return err
		}
		required, err := codeReviewRequiredRepositories(item.PlanJSON)
		if err != nil {
			return err
		}
		preview := currentReviewedPreview(required, changes)
		if preview == "" {
			item.AgentProgress = "waiting_for_preview"
			item.BlockedReason = "Esperando publicación autorizada y preview trazable de CI; no se han concedido permisos de despliegue adicionales."
			if err := tx.Save(&item).Error; err != nil {
				return err
			}
			return tx.Model(&intent).Updates(map[string]any{"status": "pending", "available_at": time.Now().UTC().Add(time.Minute)}).Error
		}
		item.PreviewURL = preview
		if err := deliveryworkflow.Advance(&item, deliveryworkflow.ActionPreviewReady, nil, time.Now().UTC()); err != nil {
			return err
		}
		item.AutomationEpoch++
		item.AgentProgress = "queued"
		item.BlockedReason = ""
		if err := scheduleContinuation(tx, item, "qa", intent.RequestedBy, ""); err != nil {
			return err
		}
		if err := tx.Save(&item).Error; err != nil {
			return err
		}
		return tx.Model(&intent).Update("status", "done").Error
	})
}

// Changes arrive newest first. Old publications/previews must not satisfy a
// later human rework iteration for the same repository.
func currentReviewedPreview(required map[string]struct{}, changes []models.DeliveryChangeSet) string {
	if len(required) == 0 {
		return ""
	}
	preview := ""
	for reference := range required {
		var reviewed *models.DeliveryChangeSet
		for i := range changes {
			if changes[i].RepositoryRef == reference && changes[i].ReviewType == "local_worktree" {
				reviewed = &changes[i]
				break
			}
		}
		if reviewed == nil || !trustedImplementationChangeSet(*reviewed) {
			return ""
		}
		matchedPreview := ""
		for _, change := range changes {
			if change.RepositoryRef == reference && change.Branch == reviewed.Branch && change.CIStatus == "passed" && validPublishedChangeRecord(change) && publishedPreviewMatchesReview(*reviewed, change) {
				if validPreviewURL(change.PreviewURL) {
					matchedPreview = change.PreviewURL
					break
				}
			}
		}
		if matchedPreview == "" {
			return ""
		}
		if preview != "" && preview != matchedPreview {
			// A work item exposes one canonical preview URL. Never let map
			// iteration order select one repository's preview and present it as
			// proof for the whole multirepo delivery; the control plane must wait
			// for an integrated preview (or a future per-repository projection).
			return ""
		}
		preview = matchedPreview
	}
	return preview
}

// publishedPreviewMatchesReview binds the preview to the exact diff that a
// human reviewed. Branch and CI provenance alone are insufficient: a later
// publication on the same branch must never satisfy an older approval.
func publishedPreviewMatchesReview(reviewed, published models.DeliveryChangeSet) bool {
	if reviewed.RepositoryRef != published.RepositoryRef || reviewed.Branch != published.Branch {
		return false
	}
	reviewDigest, err := reviewedChangeSetDigest(reviewed)
	if err != nil {
		return false
	}
	metadata := map[string]any{}
	if err := json.Unmarshal([]byte(published.MetadataJSON), &metadata); err != nil {
		return false
	}
	publishedDigest, _ := metadata["review_diff_sha256"].(string)
	return strings.EqualFold(reviewDigest, strings.TrimSpace(publishedDigest))
}
