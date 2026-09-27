package automation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/services/deliveryplansteps"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxPlanStepCallbackBody = 16 * 1024
	defaultPlanStepLease    = 120 * time.Second
	maxPlanStepLease        = 5 * time.Minute
)

var errPlanStepAcceptanceEvidenceRequired = errors.New("plan-step acceptance evidence is required")
var errPlanStepActiveExecutionOwnsPlan = errors.New("an active plan execution owns this plan")

type planStepClaimRequest struct {
	TaskID       string `json:"task_id"`
	PlanID       string `json:"plan_id"`
	StepID       string `json:"step_id,omitempty"`
	RunID        string `json:"run_id"`
	WorkerID     string `json:"worker_id"`
	AgentKey     string `json:"agent_key"`
	MachineID    string `json:"machine_id"`
	LeaseSeconds int    `json:"lease_seconds"`
}

type planStepLeaseRequest struct {
	TaskID       string `json:"task_id"`
	RunID        string `json:"run_id"`
	WorkerID     string `json:"worker_id"`
	AgentKey     string `json:"agent_key,omitempty"`
	MachineID    string `json:"machine_id,omitempty"`
	LeaseSeconds int    `json:"lease_seconds,omitempty"`
	FencingToken string `json:"fencing_token"`
}

type planStepTransitionRequest struct {
	TaskID       string `json:"task_id"`
	RunID        string `json:"run_id"`
	WorkerID     string `json:"worker_id"`
	AgentKey     string `json:"agent_key,omitempty"`
	MachineID    string `json:"machine_id,omitempty"`
	FencingToken string `json:"fencing_token"`
	Status       string `json:"status"`
}

type planStepClaimResponse struct {
	Available      bool                       `json:"available"`
	Step           *deliveryplansteps.StepDTO `json:"step,omitempty"`
	FencingToken   string                     `json:"fencing_token,omitempty"`
	LeaseExpiresAt *time.Time                 `json:"lease_expires_at,omitempty"`
}

type planStepLeaseResponse struct {
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

type planStepRuntimeTuple struct {
	TaskID    uuid.UUID
	RunID     string
	WorkerID  string
	AgentKey  string
	MachineID string
}

// ClaimDeliveryPlanStep atomically gives an already-running, authenticated
// agent one dependency-ready step from its approved plan. Work-item ownership
// and provider credentials are derived server-side, never trusted from the
// callback body.
func ClaimDeliveryPlanStep(c echo.Context) error {
	var request planStepClaimRequest
	if err := bindPlanStepRuntimePayload(c, &request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan-step claim", "The request body is invalid")
	}
	callbackIdentity, ok := bindCallbackProfileIdentity(c, &request.AgentKey, &request.MachineID)
	if !ok {
		return nil
	}
	taskID, runID, identity, leaseTTL, err := parsePlanStepRuntimeTuple(request.TaskID, request.RunID, request.WorkerID, request.AgentKey, request.MachineID, request.LeaseSeconds)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan-step claim", "The task, run, worker, or lease parameters are invalid")
	}
	if identity.AgentKey == "" {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan-step claim", "An active agent profile is required")
	}
	planID, err := uuid.FromString(strings.TrimSpace(request.PlanID))
	if err != nil || planID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan-step claim", "The plan identifier is invalid")
	}
	var targetStepID *uuid.UUID
	if strings.TrimSpace(request.StepID) != "" {
		parsedStepID, parseErr := uuid.FromString(strings.TrimSpace(request.StepID))
		if parseErr != nil || parsedStepID == uuid.Nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid plan-step claim", "The step identifier is invalid")
		}
		targetStepID = &parsedStepID
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Database is unavailable")
	}
	now := time.Now().UTC()
	var result deliveryplansteps.ClaimResult
	var dto *deliveryplansteps.StepDTO
	err = configuration.DB.WithContext(planStepCallbackContext(c, callbackIdentity.InstanceID)).Transaction(func(tx *gorm.DB) error {
		now = time.Now().UTC()
		if err := deliveryplansteps.ValidateStepProtocolWorker(tx, identity.WorkerID, identity.AgentKey, identity.MachineID, callbackIdentity.InstanceID, now); err != nil {
			return err
		}
		task, taskErr := validatePlanStepRuntimeTask(tx, planStepRuntimeTuple{TaskID: taskID, RunID: runID, WorkerID: identity.WorkerID, AgentKey: identity.AgentKey, MachineID: identity.MachineID}, now)
		if taskErr != nil {
			return taskErr
		}
		if task.Operation != "delivery.implementation" || task.DeliveryWorkItemID == nil {
			return deliveryplansteps.ErrStepInputInvalid
		}
		plan, err := lockDeliveryPlanForLegacyClaim(tx, planID, *task.DeliveryWorkItemID)
		if err != nil {
			return err
		}
		if plan.Status != "approved" || plan.ApprovedGateID == nil {
			return deliveryplansteps.ErrStepInputInvalid
		}
		// The plan lock can itself wait. Refresh the clock after both the task
		// and plan locks are held, and do not derive a child lease from an
		// automation-task lease that expired while waiting.
		now = time.Now().UTC()
		if task.LeaseExpiresAt == nil || !task.LeaseExpiresAt.After(now) {
			return deliveryplansteps.ErrStepLeaseConflict
		}
		stepLeaseTTL := leaseTTL
		if task.LeaseExpiresAt != nil {
			remaining := task.LeaseExpiresAt.Sub(now)
			if remaining < stepLeaseTTL {
				stepLeaseTTL = remaining
			}
		}
		if stepLeaseTTL <= 0 {
			return deliveryplansteps.ErrStepLeaseConflict
		}
		claimInput := deliveryplansteps.ClaimInput{
			PlanID: planID, AutomationTaskID: task.ID, RunID: runID,
			WorkerID: identity.WorkerID, AgentKey: identity.AgentKey, MachineID: identity.MachineID,
			LeaseTTL: stepLeaseTTL,
		}
		var assignment models.DeliveryPlanStepAssignment
		assignmentErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("child_automation_task_id = ?", task.ID).First(&assignment).Error
		if assignmentErr == nil {
			if targetStepID == nil || assignment.ChildAutomationTaskID != task.ID || *targetStepID != assignment.DeliveryPlanStepID {
				return deliveryplansteps.ErrStepLeaseConflict
			}
			var execution models.DeliveryPlanExecution
			if err := tx.First(&execution, "id = ?", assignment.ExecutionID).Error; err != nil {
				return err
			}
			var parentTask models.AutomationTask
			if err := tx.Select("id", "operation", "delivery_work_item_id", "status").First(&parentTask, "id = ?", execution.AutomationTaskID).Error; err != nil {
				return err
			}
			var planSteps []models.DeliveryPlanStep
			if err := tx.Where("plan_id = ?", plan.ID).Order("display_order ASC, id ASC").Find(&planSteps).Error; err != nil {
				return err
			}
			var dependencies []models.DeliveryPlanStepDependency
			if err := tx.Where("plan_id = ?", plan.ID).Order("step_id ASC, depends_on_step_id ASC").Find(&dependencies).Error; err != nil {
				return err
			}
			planHash, hashErr := deliveryplansteps.ApprovedPlanContentHash(plan, planSteps, dependencies)
			if hashErr != nil || !planStepAssignmentAllowsClaim(assignment, execution, parentTask, *task, plan, *targetStepID, planHash) {
				return deliveryplansteps.ErrStepLeaseConflict
			}
			result, err = deliveryplansteps.ClaimSpecificReadyInTransaction(tx, claimInput, assignment.DeliveryPlanStepID)
			if err != nil || !result.Available {
				return err
			}
			assignmentUpdates := map[string]any{"status": models.DeliveryPlanStepAssignmentRunning, "updated_at": now}
			if assignment.StartedAt == nil {
				assignmentUpdates["started_at"] = now
			}
			if err := tx.Model(&models.DeliveryPlanStepAssignment{}).Where("id = ? AND status IN ?", assignment.ID, []string{
				models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued,
				models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning,
			}).Updates(assignmentUpdates).Error; err != nil {
				return err
			}
			if execution.Status == models.DeliveryPlanExecutionPending || execution.Status == models.DeliveryPlanExecutionDispatching {
				result := tx.Model(&models.DeliveryPlanExecution{}).Where("id = ? AND status IN ?", execution.ID, []string{
					models.DeliveryPlanExecutionPending, models.DeliveryPlanExecutionDispatching, models.DeliveryPlanExecutionRunning,
				}).Updates(map[string]any{
					"status":     models.DeliveryPlanExecutionRunning,
					"started_at": gorm.Expr("COALESCE(started_at, ?)", now),
					"updated_at": now,
				})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return deliveryplansteps.ErrStepLeaseConflict
				}
			}
			dto, err = loadDeliveryPlanStepDTO(tx, plan, result.Step.ID)
			if err != nil {
				return err
			}
			return nil
		}
		if !errors.Is(assignmentErr, gorm.ErrRecordNotFound) {
			return assignmentErr
		}
		if targetStepID != nil {
			return deliveryplansteps.ErrStepLeaseConflict
		}
		var parentExecutionCount int64
		if err := tx.Model(&models.DeliveryPlanExecution{}).Where("automation_task_id = ?", task.ID).Count(&parentExecutionCount).Error; err != nil {
			return err
		}
		if parentExecutionCount > 0 {
			return deliveryplansteps.ErrStepLeaseConflict
		}
		if err := rejectLegacyPlanClaimDuringActiveExecution(tx, plan.ID); err != nil {
			return err
		}
		result, err = deliveryplansteps.ClaimNextReadyInTransaction(tx, claimInput)
		if err != nil || !result.Available {
			return err
		}
		dto, err = loadDeliveryPlanStepDTO(tx, plan, result.Step.ID)
		return err
	})
	if err != nil {
		return planStepRuntimeError(c, "Plan-step claim failed", err)
	}
	response, err := projectPlanStepClaimResponse(result, dto)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Plan-step claim failed", "The claimed step could not be projected")
	}
	return utils.Success(c, http.StatusOK, "Plan-step claim resolved", response)
}

func projectPlanStepClaimResponse(result deliveryplansteps.ClaimResult, dto *deliveryplansteps.StepDTO) (planStepClaimResponse, error) {
	response := planStepClaimResponse{Available: result.Available}
	if !result.Available {
		return response, nil
	}
	if dto == nil || result.Fence < 1 || result.LeaseExpiresAt == nil || result.LeaseExpiresAt.IsZero() {
		return planStepClaimResponse{}, errors.New("available plan-step claim is missing its safe projection or lease")
	}
	response.Step = dto
	response.FencingToken = strconv.FormatInt(result.Fence, 10)
	response.LeaseExpiresAt = result.LeaseExpiresAt
	return response, nil
}

// RenewDeliveryPlanStepLease rejects callbacks after the owning task lease or
// step fencing token is stale. It never accepts a new owner from the worker.
func RenewDeliveryPlanStepLease(c echo.Context) error {
	stepID, err := uuid.FromString(strings.TrimSpace(c.Param("id")))
	if err != nil || stepID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan step", "The step identifier is invalid")
	}
	var request planStepLeaseRequest
	if err := bindPlanStepRuntimePayload(c, &request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan-step lease", "The request body is invalid")
	}
	callbackIdentity, ok := bindCallbackProfileIdentity(c, &request.AgentKey, &request.MachineID)
	if !ok {
		return nil
	}
	taskID, runID, identity, leaseTTL, err := parsePlanStepRuntimeTuple(request.TaskID, request.RunID, request.WorkerID, request.AgentKey, request.MachineID, request.LeaseSeconds)
	fence, fenceErr := parsePlanStepFence(request.FencingToken)
	if err != nil || fenceErr != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan-step lease", "The task, run, worker, or fencing parameters are invalid")
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Database is unavailable")
	}
	now := time.Now().UTC()
	input := deliveryplansteps.LeaseInput{
		StepID: stepID, AutomationTaskID: taskID, RunID: runID,
		WorkerID: identity.WorkerID, AgentKey: identity.AgentKey, MachineID: identity.MachineID,
		Fence: fence, LeaseTTL: leaseTTL,
	}
	var expiresAt time.Time
	err = configuration.DB.WithContext(models.WithDeliveryPlanStepAgentInstanceID(c.Request().Context(), callbackIdentity.InstanceID)).Transaction(func(tx *gorm.DB) error {
		task, taskErr := validatePlanStepRuntimeTask(tx, planStepRuntimeTuple{TaskID: taskID, RunID: runID, WorkerID: identity.WorkerID, AgentKey: identity.AgentKey, MachineID: identity.MachineID}, now)
		if taskErr != nil {
			return taskErr
		}
		if task.Operation != "delivery.implementation" || task.DeliveryWorkItemID == nil {
			return deliveryplansteps.ErrStepInputInvalid
		}
		if _, err := validateAssignedPlanStepCallback(tx, *task, stepID); err != nil {
			return err
		}
		now = time.Now().UTC()
		if task.LeaseExpiresAt == nil || !task.LeaseExpiresAt.After(now) {
			return deliveryplansteps.ErrStepLeaseConflict
		}
		input.AgentKey, input.MachineID = task.AgentKey, task.MachineID
		if task.LeaseExpiresAt != nil && task.LeaseExpiresAt.Sub(now) < input.LeaseTTL {
			input.LeaseTTL = task.LeaseExpiresAt.Sub(now)
		}
		if input.LeaseTTL <= 0 {
			return deliveryplansteps.ErrStepLeaseConflict
		}
		if err := deliveryplansteps.RenewStepLeaseInTransaction(tx, input); err != nil {
			return err
		}
		var persistedLease struct {
			LeaseExpiresAt time.Time `gorm:"column:lease_expires_at"`
		}
		if err := tx.Model(&models.DeliveryPlanStep{}).Select("lease_expires_at").Take(&persistedLease, "id = ?", stepID).Error; err != nil {
			return err
		}
		expiresAt = persistedLease.LeaseExpiresAt.UTC()
		return nil
	})
	if err != nil {
		return planStepRuntimeError(c, "Plan-step lease rejected", err)
	}
	return utils.Success(c, http.StatusOK, "Plan-step lease renewed", planStepLeaseResponse{LeaseExpiresAt: expiresAt})
}

// TransitionDeliveryPlanStep accepts only lifecycle state, not arbitrary
// agent prose or raw tool output. Detailed evidence belongs in the existing
// private artifact and execution-ledger flows.
func TransitionDeliveryPlanStep(c echo.Context) error {
	stepID, err := uuid.FromString(strings.TrimSpace(c.Param("id")))
	if err != nil || stepID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan step", "The step identifier is invalid")
	}
	var request planStepTransitionRequest
	if err := bindPlanStepRuntimePayload(c, &request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan-step transition", "The request body is invalid")
	}
	callbackIdentity, ok := bindCallbackProfileIdentity(c, &request.AgentKey, &request.MachineID)
	if !ok {
		return nil
	}
	taskID, runID, identity, _, err := parsePlanStepRuntimeTuple(request.TaskID, request.RunID, request.WorkerID, request.AgentKey, request.MachineID, 0)
	fence, fenceErr := parsePlanStepFence(request.FencingToken)
	if err != nil || fenceErr != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan-step transition", "The task, run, worker, or fencing parameters are invalid")
	}
	status := strings.ToLower(strings.TrimSpace(request.Status))
	if status != models.DeliveryPlanStepRunning && status != models.DeliveryPlanStepCompleted && status != models.DeliveryPlanStepFailed && status != models.DeliveryPlanStepBlocked {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan-step transition", "status must be running, completed, failed, or blocked")
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Database is unavailable")
	}
	now := time.Now().UTC()
	input := deliveryplansteps.TransitionInput{
		StepID: stepID, AutomationTaskID: taskID, RunID: runID,
		WorkerID: identity.WorkerID, AgentKey: identity.AgentKey, MachineID: identity.MachineID,
		Fence: fence, Status: status,
	}
	var plan models.DeliveryPlan
	err = configuration.DB.WithContext(planStepCallbackContext(c, callbackIdentity.InstanceID)).Transaction(func(tx *gorm.DB) error {
		var replayPlan models.DeliveryPlan
		isReplay, replayErr := replayTerminalPlanStepTransition(tx, taskID, stepID, runID, identity, fence, callbackIdentity.InstanceID, status, &replayPlan)
		if replayErr != nil {
			return replayErr
		}
		if isReplay {
			plan = replayPlan
			return nil
		}
		task, taskErr := validatePlanStepRuntimeTask(tx, planStepRuntimeTuple{TaskID: taskID, RunID: runID, WorkerID: identity.WorkerID, AgentKey: identity.AgentKey, MachineID: identity.MachineID}, now)
		if taskErr != nil {
			return taskErr
		}
		if task.Operation != "delivery.implementation" || task.DeliveryWorkItemID == nil {
			return deliveryplansteps.ErrStepInputInvalid
		}
		var step models.DeliveryPlanStep
		if err := tx.Select("id", "plan_id", "acceptance_criteria_json", "evidence_requirements_json").First(&step, "id = ?", stepID).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&plan, "id = ?", step.PlanID).Error; err != nil {
			return err
		}
		if plan.WorkItemID != *task.DeliveryWorkItemID || plan.Status != "approved" || plan.ApprovedGateID == nil {
			return deliveryplansteps.ErrStepLeaseConflict
		}
		if status == models.DeliveryPlanStepCompleted {
			if err := requirePlanStepAcceptanceEvidence(tx, step, taskID, runID, identity, fence, callbackIdentity.InstanceID); err != nil {
				return err
			}
			if err := requirePlanStepEvidenceRequirements(tx, step, plan.Version, *task, runID, identity, fence, callbackIdentity.InstanceID); err != nil {
				return err
			}
		}
		assignment, err := validateAssignedPlanStepCallback(tx, *task, stepID)
		if err != nil {
			return err
		}
		input.AgentKey, input.MachineID = task.AgentKey, task.MachineID
		if err := deliveryplansteps.TransitionStepInTransaction(tx, input); err != nil {
			return err
		}
		if assignment == nil {
			return nil
		}
		assignmentStatus, terminal := assignmentStatusForStepStatus(status)
		updates := map[string]any{"status": assignmentStatus, "updated_at": now}
		if terminal {
			updates["completed_at"] = now
		}
		result := tx.Model(&models.DeliveryPlanStepAssignment{}).
			Where("id = ? AND child_automation_task_id = ? AND delivery_plan_step_id = ? AND status IN ?", assignment.ID, task.ID, stepID, []string{
				models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued,
				models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning,
			}).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return deliveryplansteps.ErrStepLeaseConflict
		}
		return nil
	})
	if err != nil {
		return planStepRuntimeError(c, "Plan-step transition rejected", err)
	}
	dto, err := loadDeliveryPlanStepDTO(configuration.DB, plan, stepID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Plan-step transition failed", "")
	}
	return utils.Success(c, http.StatusOK, "Plan-step state recorded", dto)
}

// requirePlanStepAcceptanceEvidence makes an authenticated, append-only
// acceptance event a prerequisite for the worker's completed transition. The
// event is a worker attestation, not proof that the checks ran remotely; later
// artifact integration must independently verify patch bytes and validation.
func requirePlanStepAcceptanceEvidence(
	tx *gorm.DB,
	step models.DeliveryPlanStep,
	taskID uuid.UUID,
	runID string,
	identity automationagent.AgentIdentity,
	fence int64,
	instanceID uuid.UUID,
) error {
	if tx == nil || step.ID == uuid.Nil || taskID == uuid.Nil || strings.TrimSpace(runID) == "" || fence < 1 || instanceID == uuid.Nil {
		return errPlanStepAcceptanceEvidenceRequired
	}
	var event models.DeliveryPlanStepActivityEvent
	err := tx.Where(`step_id = ? AND automation_task_id = ? AND run_id = ? AND worker_id = ?
		AND agent_key = ? AND machine_id = ? AND agent_instance_id = ? AND fencing_token = ?
		AND action = ? AND phase = ?`,
		step.ID, taskID, runID, identity.WorkerID, identity.AgentKey, identity.MachineID, instanceID,
		fence, models.DeliveryPlanStepActivityEvidence, models.DeliveryPlanStepActivityCompleted).
		Order("sequence DESC").First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errPlanStepAcceptanceEvidenceRequired
	}
	if err != nil {
		return err
	}
	var latestSequence int64
	if err := tx.Model(&models.DeliveryPlanStepActivityEvent{}).
		Where("step_id = ? AND run_id = ? AND fencing_token = ?", step.ID, runID, fence).
		Select("COALESCE(MAX(sequence), 0)").Scan(&latestSequence).Error; err != nil {
		return err
	}
	if event.Sequence != latestSequence {
		return errPlanStepAcceptanceEvidenceRequired
	}

	decoder := json.NewDecoder(strings.NewReader(event.DetailsJSON))
	decoder.DisallowUnknownFields()
	var details models.DeliveryPlanStepActivityDetails
	if decoder.Decode(&details) != nil || models.ValidateDeliveryPlanStepActivityDetails(event.Action, event.Phase, &details) != nil {
		return errPlanStepAcceptanceEvidenceRequired
	}
	var criteria []string
	if json.Unmarshal([]byte(step.AcceptanceCriteriaJSON), &criteria) != nil || len(criteria) == 0 || len(details.AcceptanceChecks) != len(criteria) {
		return errPlanStepAcceptanceEvidenceRequired
	}
	expected := make(map[string]struct{}, len(criteria))
	for _, criterion := range criteria {
		digest := sha256.Sum256([]byte(criterion))
		expected[hex.EncodeToString(digest[:])] = struct{}{}
	}
	seen := make(map[string]struct{}, len(details.AcceptanceChecks))
	for _, check := range details.AcceptanceChecks {
		if !check.Passed {
			return errPlanStepAcceptanceEvidenceRequired
		}
		if _, ok := expected[check.CriterionSHA256]; !ok {
			return errPlanStepAcceptanceEvidenceRequired
		}
		if _, duplicate := seen[check.CriterionSHA256]; duplicate {
			return errPlanStepAcceptanceEvidenceRequired
		}
		seen[check.CriterionSHA256] = struct{}{}
	}
	if len(seen) != len(expected) {
		return errPlanStepAcceptanceEvidenceRequired
	}
	var task models.AutomationTask
	if err := tx.Select("id", "operation", "delivery_work_item_id").First(&task, "id = ?", taskID).Error; err != nil {
		return err
	}
	if task.Operation != "delivery.implementation" || task.DeliveryWorkItemID == nil {
		return errPlanStepAcceptanceEvidenceRequired
	}
	if err := validateAppliedPlanStepDependencyPatchManifest(tx, step, task, identity, instanceID, fence, event); err != nil {
		if errors.Is(err, errPlanStepPatchObjectUnavailable) {
			return err
		}
		if errors.Is(err, errPlanStepAcceptanceEvidenceRequired) || errors.Is(err, deliveryplansteps.ErrStepLeaseConflict) ||
			errors.Is(err, deliveryplansteps.ErrStepInputInvalid) || errors.Is(err, gorm.ErrRecordNotFound) {
			return errPlanStepAcceptanceEvidenceRequired
		}
		return err
	}
	return nil
}

// replayTerminalPlanStepTransition acknowledges only an already-persisted
// terminal result with the exact task/run/worker/fence tuple. This is a
// read-only idempotent replay path: it lets a worker recover if the database
// committed a completion but the HTTP response was lost, without allowing an
// expired worker to mutate state or steal a newer lease. It intentionally does
// not lock the step before the caller acquires the plan lock; mutating paths
// keep a consistent plan-before-step lock order with fan-out creation.
func replayTerminalPlanStepTransition(
	tx *gorm.DB,
	taskID, stepID uuid.UUID,
	runID string,
	identity automationagent.AgentIdentity,
	fence int64,
	instanceID uuid.UUID,
	status string,
	planOut *models.DeliveryPlan,
) (bool, error) {
	if tx == nil || taskID == uuid.Nil || stepID == uuid.Nil || fence < 1 || planOut == nil {
		return false, deliveryplansteps.ErrStepInputInvalid
	}
	if status != models.DeliveryPlanStepCompleted && status != models.DeliveryPlanStepFailed && status != models.DeliveryPlanStepBlocked {
		return false, nil
	}
	var step models.DeliveryPlanStep
	if err := tx.First(&step, "id = ?", stepID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if step.Status != status || step.AutomationTaskID == nil || *step.AutomationTaskID != taskID ||
		step.RunID != runID || step.WorkerID != identity.WorkerID || step.LeaseFence != fence ||
		(identity.AgentKey != "" && step.AgentKey != identity.AgentKey) ||
		(identity.MachineID != "" && step.MachineID != identity.MachineID) {
		return false, nil
	}
	var task models.AutomationTask
	if err := tx.Select("id", "operation", "delivery_work_item_id").First(&task, "id = ?", taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if task.Operation != "delivery.implementation" || task.DeliveryWorkItemID == nil {
		return false, nil
	}
	var plan models.DeliveryPlan
	if err := tx.Select("id", "work_item_id", "version", "status", "approved_gate_id").First(&plan, "id = ?", step.PlanID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if plan.WorkItemID != *task.DeliveryWorkItemID {
		return false, nil
	}
	var assignment models.DeliveryPlanStepAssignment
	var execution *models.DeliveryPlanExecution
	var parentTask *models.AutomationTask
	err := tx.Select("id", "execution_id", "delivery_plan_step_id", "child_automation_task_id", "status").
		Where("child_automation_task_id = ?", taskID).First(&assignment).Error
	if err == nil {
		var loaded models.DeliveryPlanExecution
		if err := tx.Select("id", "automation_task_id", "plan_id", "plan_version").First(&loaded, "id = ?", assignment.ExecutionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		execution = &loaded
		var loadedParentTask models.AutomationTask
		if err := tx.Select("id", "operation", "delivery_work_item_id").First(&loadedParentTask, "id = ?", loaded.AutomationTaskID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		parentTask = &loadedParentTask
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	if !terminalPlanStepReplayMatches(step, task, parentTask, plan, &assignment, execution, taskID, runID, identity, fence, status) {
		return false, nil
	}
	if status == models.DeliveryPlanStepCompleted {
		if err := requirePlanStepAcceptanceEvidence(tx, step, taskID, runID, identity, fence, instanceID); err != nil {
			if errors.Is(err, errPlanStepAcceptanceEvidenceRequired) {
				return false, nil
			}
			return false, err
		}
		if err := requirePlanStepEvidenceRequirements(tx, step, plan.Version, task, runID, identity, fence, instanceID); err != nil {
			if errors.Is(err, errPlanStepRequiredEvidenceMissing) {
				return false, nil
			}
			return false, err
		}
	}
	*planOut = plan
	return true, nil
}

func terminalPlanStepReplayMatches(
	step models.DeliveryPlanStep,
	task models.AutomationTask,
	parentTask *models.AutomationTask,
	plan models.DeliveryPlan,
	assignment *models.DeliveryPlanStepAssignment,
	execution *models.DeliveryPlanExecution,
	taskID uuid.UUID,
	runID string,
	identity automationagent.AgentIdentity,
	fence int64,
	status string,
) bool {
	if taskID == uuid.Nil || fence < 1 || status != models.DeliveryPlanStepCompleted && status != models.DeliveryPlanStepFailed && status != models.DeliveryPlanStepBlocked {
		return false
	}
	if step.Status != status || step.AutomationTaskID == nil || *step.AutomationTaskID != taskID || step.PlanID != plan.ID ||
		step.RunID != runID || step.WorkerID != identity.WorkerID || step.LeaseFence != fence ||
		(identity.AgentKey != "" && step.AgentKey != identity.AgentKey) ||
		(identity.MachineID != "" && step.MachineID != identity.MachineID) ||
		task.ID != taskID || task.Operation != "delivery.implementation" || task.DeliveryWorkItemID == nil || *task.DeliveryWorkItemID != plan.WorkItemID {
		return false
	}
	if assignment == nil || assignment.ID == uuid.Nil {
		return execution == nil && parentTask == nil
	}
	expectedAssignmentStatus, _ := assignmentStatusForStepStatus(status)
	return execution != nil && parentTask != nil && assignment.ExecutionID == execution.ID && assignment.DeliveryPlanStepID == step.ID &&
		assignment.ChildAutomationTaskID == taskID && assignment.Status == expectedAssignmentStatus && execution.PlanID == plan.ID &&
		execution.PlanVersion == plan.Version && parentTask.ID != uuid.Nil && parentTask.ID != taskID && parentTask.ID == execution.AutomationTaskID &&
		parentTask.Operation == "delivery.implementation" && parentTask.DeliveryWorkItemID != nil && *parentTask.DeliveryWorkItemID == plan.WorkItemID
}

// validateAssignedPlanStepCallback enforces the persisted child-task binding
// on every assigned-worker mutation, not only the initial claim. Legacy
// sequential tasks have no assignment row and keep their existing callback
// path. New child tasks and their assignment rows are created atomically, so a
// missing row cannot be a valid child execution.
func validateAssignedPlanStepCallback(tx *gorm.DB, childTask models.AutomationTask, stepID uuid.UUID) (*models.DeliveryPlanStepAssignment, error) {
	if tx == nil || childTask.ID == uuid.Nil || stepID == uuid.Nil || childTask.DeliveryWorkItemID == nil {
		return nil, deliveryplansteps.ErrStepInputInvalid
	}
	var assignment models.DeliveryPlanStepAssignment
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("child_automation_task_id = ?", childTask.ID).First(&assignment).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if assignment.ChildAutomationTaskID != childTask.ID || assignment.DeliveryPlanStepID != stepID {
		return nil, deliveryplansteps.ErrStepLeaseConflict
	}
	var execution models.DeliveryPlanExecution
	if err := tx.First(&execution, "id = ?", assignment.ExecutionID).Error; err != nil {
		return nil, err
	}
	var assignedStep models.DeliveryPlanStep
	if err := tx.Select("id", "plan_id").First(&assignedStep, "id = ?", assignment.DeliveryPlanStepID).Error; err != nil {
		return nil, err
	}
	if assignedStep.PlanID != execution.PlanID {
		return nil, deliveryplansteps.ErrStepLeaseConflict
	}
	var plan models.DeliveryPlan
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND work_item_id = ?", execution.PlanID, *childTask.DeliveryWorkItemID).First(&plan).Error; err != nil {
		return nil, err
	}
	var parentTask models.AutomationTask
	if err := tx.Select("id", "operation", "delivery_work_item_id", "status").First(&parentTask, "id = ?", execution.AutomationTaskID).Error; err != nil {
		return nil, err
	}
	var planSteps []models.DeliveryPlanStep
	if err := tx.Where("plan_id = ?", plan.ID).Order("display_order ASC, id ASC").Find(&planSteps).Error; err != nil {
		return nil, err
	}
	var dependencies []models.DeliveryPlanStepDependency
	if err := tx.Where("plan_id = ?", plan.ID).Order("step_id ASC, depends_on_step_id ASC").Find(&dependencies).Error; err != nil {
		return nil, err
	}
	planHash, err := deliveryplansteps.ApprovedPlanContentHash(plan, planSteps, dependencies)
	if err != nil || !planStepAssignmentAllowsClaim(assignment, execution, parentTask, childTask, plan, stepID, planHash) {
		return nil, deliveryplansteps.ErrStepLeaseConflict
	}
	return &assignment, nil
}

func assignmentStatusForStepStatus(status string) (string, bool) {
	switch status {
	case models.DeliveryPlanStepRunning:
		return models.DeliveryPlanStepAssignmentRunning, false
	case models.DeliveryPlanStepCompleted:
		return models.DeliveryPlanStepAssignmentCompleted, true
	case models.DeliveryPlanStepFailed:
		return models.DeliveryPlanStepAssignmentFailed, true
	case models.DeliveryPlanStepBlocked:
		return models.DeliveryPlanStepAssignmentBlocked, false
	default:
		return "", false
	}
}

func bindPlanStepRuntimePayload(c echo.Context, target any) error {
	request := c.Request()
	request.Body = http.MaxBytesReader(c.Response(), request.Body, maxPlanStepCallbackBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

func parsePlanStepRuntimeTuple(taskIDRaw, runIDRaw, workerID, agentKey, machineID string, leaseSeconds int) (uuid.UUID, string, automationagent.AgentIdentity, time.Duration, error) {
	taskID, err := uuid.FromString(strings.TrimSpace(taskIDRaw))
	if err != nil || taskID == uuid.Nil {
		return uuid.Nil, "", automationagent.AgentIdentity{}, 0, deliveryplansteps.ErrStepInputInvalid
	}
	runID := strings.TrimSpace(runIDRaw)
	if runUUID, parseErr := uuid.FromString(runID); parseErr != nil || runUUID == uuid.Nil {
		return uuid.Nil, "", automationagent.AgentIdentity{}, 0, deliveryplansteps.ErrStepInputInvalid
	}
	identity := automationagent.AgentIdentity{WorkerID: strings.TrimSpace(workerID), AgentKey: strings.TrimSpace(agentKey), MachineID: strings.TrimSpace(machineID)}
	if identity.WorkerID == "" || validateAgentIdentity(identity, true) != nil {
		return uuid.Nil, "", automationagent.AgentIdentity{}, 0, deliveryplansteps.ErrStepInputInvalid
	}
	leaseTTL := defaultPlanStepLease
	if leaseSeconds > 0 {
		leaseTTL = time.Duration(leaseSeconds) * time.Second
	}
	if leaseTTL < 10*time.Second || leaseTTL > maxPlanStepLease {
		return uuid.Nil, "", automationagent.AgentIdentity{}, 0, deliveryplansteps.ErrStepInputInvalid
	}
	return taskID, runID, identity, leaseTTL, nil
}

func parsePlanStepFence(raw string) (int64, error) {
	fence, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || fence < 1 {
		return 0, deliveryplansteps.ErrStepInputInvalid
	}
	return fence, nil
}

func planStepAssignmentAllowsClaim(assignment models.DeliveryPlanStepAssignment, execution models.DeliveryPlanExecution, parentTask, childTask models.AutomationTask, plan models.DeliveryPlan, targetStepID uuid.UUID, computedPlanHash string) bool {
	if assignment.ID == uuid.Nil || execution.ID == uuid.Nil || assignment.ExecutionID != execution.ID ||
		assignment.DeliveryPlanStepID == uuid.Nil || assignment.DeliveryPlanStepID != targetStepID ||
		assignment.ChildAutomationTaskID == uuid.Nil || assignment.ChildAutomationTaskID != childTask.ID ||
		assignment.TargetMachineID == "" || assignment.TargetAgentKey == "" || childTask.MachineID != assignment.TargetMachineID || childTask.AgentKey != assignment.TargetAgentKey ||
		(execution.Status != models.DeliveryPlanExecutionPending && execution.Status != models.DeliveryPlanExecutionDispatching && execution.Status != models.DeliveryPlanExecutionRunning) ||
		(execution.PlanID != plan.ID || execution.PlanVersion != plan.Version || computedPlanHash == "" || execution.PlanHash != computedPlanHash) ||
		plan.Status != "approved" || plan.ApprovedGateID == nil || execution.ApprovedGateID != *plan.ApprovedGateID ||
		parentTask.ID == uuid.Nil || parentTask.ID != execution.AutomationTaskID || parentTask.ID == childTask.ID ||
		parentTask.Operation != "delivery.implementation" || (parentTask.Status != "queued" && parentTask.Status != "running") ||
		parentTask.DeliveryWorkItemID == nil || childTask.DeliveryWorkItemID == nil || *parentTask.DeliveryWorkItemID != *childTask.DeliveryWorkItemID || childTask.Operation != "delivery.implementation" {
		return false
	}
	switch assignment.Status {
	case models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued, models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning:
		return true
	default:
		return false
	}
}

// planHasActiveExecution prevents the legacy unassigned claim path from
// bypassing the persisted assignment's capacity and workspace affinity while
// a plan is being fanned out. The caller holds a shared lock on the plan row;
// execution creation takes an update lock on that same row, so the decision is
// serialized with a concurrent transition into fan-out.
func planHasActiveExecution(tx *gorm.DB, planID uuid.UUID) (bool, error) {
	if tx == nil || planID == uuid.Nil {
		return false, deliveryplansteps.ErrStepInputInvalid
	}
	var count int64
	if err := tx.Model(&models.DeliveryPlanExecution{}).
		Where("plan_id = ? AND status IN ?", planID, []string{
			models.DeliveryPlanExecutionPending,
			models.DeliveryPlanExecutionDispatching,
			models.DeliveryPlanExecutionRunning,
		}).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func lockDeliveryPlanForLegacyClaim(tx *gorm.DB, planID, workItemID uuid.UUID) (models.DeliveryPlan, error) {
	if tx == nil || planID == uuid.Nil || workItemID == uuid.Nil {
		return models.DeliveryPlan{}, deliveryplansteps.ErrStepInputInvalid
	}
	var plan models.DeliveryPlan
	err := tx.Select("id", "work_item_id", "version", "status", "approved_gate_id").
		Clauses(clause.Locking{Strength: "SHARE"}).
		Where("id = ? AND work_item_id = ?", planID, workItemID).First(&plan).Error
	return plan, err
}

func rejectLegacyPlanClaimDuringActiveExecution(tx *gorm.DB, planID uuid.UUID) error {
	active, err := planHasActiveExecution(tx, planID)
	if err != nil {
		return err
	}
	if active {
		// The unassigned sequential path has no persisted machine reservation
		// or fan-out slot. Do not let it take a ready node away from an active
		// execution whose assignments were capacity- and workspace-routed.
		return errPlanStepActiveExecutionOwnsPlan
	}
	return nil
}

func validatePlanStepRuntimeTask(tx *gorm.DB, tuple planStepRuntimeTuple, now time.Time) (*models.AutomationTask, error) {
	if tx == nil {
		return nil, gorm.ErrInvalidDB
	}
	var task models.AutomationTask
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, "id = ?", tuple.TaskID).Error; err != nil {
		return nil, err
	}
	// The SELECT above may wait on another callback's task lock. Never compare
	// an expiring lease with a timestamp captured before that wait.
	now = time.Now().UTC()
	if task.Status != "running" || task.RunID != tuple.RunID || task.LeaseExpiresAt == nil || !task.LeaseExpiresAt.After(now) {
		return nil, deliveryplansteps.ErrStepLeaseConflict
	}
	if task.WorkerID != tuple.WorkerID || (tuple.AgentKey != "" && task.AgentKey != tuple.AgentKey) || (tuple.MachineID != "" && task.MachineID != tuple.MachineID) || task.DeliveryWorkItemID == nil {
		return nil, deliveryplansteps.ErrStepLeaseConflict
	}
	identity := automationagent.AgentIdentity{WorkerID: task.WorkerID, AgentKey: task.AgentKey, MachineID: task.MachineID}
	if _, err := validateAgentClaimIdentity(tx, task.ID, callbackRequest{
		WorkerID: identity.WorkerID, AgentKey: identity.AgentKey, MachineID: identity.MachineID,
	}); err != nil {
		return nil, deliveryplansteps.ErrStepLeaseConflict
	}
	return &task, nil
}

func loadDeliveryPlanStepDTO(db *gorm.DB, plan models.DeliveryPlan, stepID uuid.UUID) (*deliveryplansteps.StepDTO, error) {
	if db == nil {
		return nil, gorm.ErrInvalidDB
	}
	var steps []models.DeliveryPlanStep
	if err := db.Where("plan_id = ?", plan.ID).Order("display_order ASC").Find(&steps).Error; err != nil {
		return nil, err
	}
	var dependencies []models.DeliveryPlanStepDependency
	if len(steps) > 0 {
		if err := db.Where("plan_id = ?", plan.ID).Order("step_id ASC, depends_on_step_id ASC").Find(&dependencies).Error; err != nil {
			return nil, err
		}
	}
	dtos, err := deliveryplansteps.DTOs(steps, dependencies, plan.Version)
	if err != nil {
		return nil, err
	}
	for index := range dtos {
		if dtos[index].ID == stepID.String() {
			return &dtos[index], nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func planStepRuntimeError(c echo.Context, message string, err error) error {
	switch {
	case errors.Is(err, errPlanStepRequiredEvidenceMissing):
		return utils.Error(c, http.StatusConflict, message, "The plan step cannot be completed without matching required evidence for this run and lease")
	case errors.Is(err, errPlanStepAcceptanceEvidenceRequired):
		return utils.Error(c, http.StatusConflict, message, "The plan step cannot be completed without matching accepted-criteria evidence for this run and lease")
	case errors.Is(err, gorm.ErrRecordNotFound):
		return utils.Error(c, http.StatusNotFound, message, "The task, approved plan, or plan step was not found")
	case errors.Is(err, deliveryplansteps.ErrStepLeaseConflict):
		return utils.Error(c, http.StatusConflict, message, "The task or step lease is no longer owned by this run")
	case errors.Is(err, deliveryplansteps.ErrNoEligibleAgentMachine):
		return utils.Error(c, http.StatusConflict, message, "This runtime instance is not eligible to claim a plan step")
	case errors.Is(err, errPlanStepActiveExecutionOwnsPlan):
		return utils.Error(c, http.StatusConflict, message, "An active execution already owns assignments for this plan")
	case errors.Is(err, deliveryplansteps.ErrStepInputInvalid):
		return utils.Error(c, http.StatusBadRequest, message, "The plan-step request is invalid")
	default:
		return utils.Error(c, http.StatusInternalServerError, message, "The plan-step operation could not be persisted")
	}
}
