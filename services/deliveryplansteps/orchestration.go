package deliveryplansteps

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"events-stocks/internal/agentprotocol"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrPlanExecutionConflict       = errors.New("delivery plan execution conflicts with the existing parent-task execution")
	ErrPlanExecutionParentMismatch = errors.New("parent automation task does not belong to the delivery plan work item")
	ErrPlanExecutionApproval       = errors.New("delivery plan execution requires the currently approved plan gate")
	ErrPlanExecutionCapacity       = errors.New("delivery plan execution has no available assignment capacity")
	ErrNoEligibleAgentMachine      = errors.New("no eligible local agent machine is ready for this plan step")
)

const assignmentHeartbeatMaxAge = 90 * time.Second

var frozenWorkspaceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$`)

type assignmentTarget struct {
	MachineID string
	AgentKey  string
}

type assignmentHeartbeatRow struct {
	WorkerID              string    `gorm:"column:worker_id"`
	AgentKey              string    `gorm:"column:agent_key"`
	MachineID             string    `gorm:"column:machine_id"`
	Concurrency           int       `gorm:"column:concurrency"`
	Draining              bool      `gorm:"column:draining"`
	CapabilitiesJSON      string    `gorm:"column:capabilities_json"`
	ProtocolsJSON         string    `gorm:"column:protocols_json"`
	WorkspaceReadiness    string    `gorm:"column:workspace_readiness"`
	LastSeenAt            time.Time `gorm:"column:last_seen_at"`
	ProfileOperationsJSON string    `gorm:"column:profile_operations_json"`
}

type assignmentWorkspaceReadiness struct {
	ID            string `json:"id"`
	Ready         bool   `json:"ready"`
	SandboxReady  bool   `json:"sandbox_ready"`
	IsolationMode string `json:"isolation_mode"`
}

type assignmentTargetKey struct {
	MachineID string
	AgentKey  string
}

type assignmentTargetCandidate struct {
	Key      assignmentTargetKey
	Capacity int
	Reserved int
}

// CreateExecutionInTransaction freezes the currently approved plan snapshot
// for one parent task. The parent AutomationTask is the idempotency boundary:
// retries with the same input return the existing row; reusing that task with
// a different plan, key, or concurrency limit fails with
// ErrPlanExecutionConflict. The caller owns the surrounding transaction.
func CreateExecutionInTransaction(
	tx *gorm.DB,
	parentTaskID uuid.UUID,
	planID uuid.UUID,
	maxConcurrency int,
	idempotencyKey string,
	now time.Time,
) (models.DeliveryPlanExecution, bool, error) {
	if tx == nil {
		return models.DeliveryPlanExecution{}, false, invalidStepInput("transaction is required")
	}
	if parentTaskID == uuid.Nil || planID == uuid.Nil {
		return models.DeliveryPlanExecution{}, false, invalidStepInput("parent_task_id and plan_id are required")
	}
	if maxConcurrency < 1 || maxConcurrency > models.DeliveryPlanExecutionMaxConcurrency {
		return models.DeliveryPlanExecution{}, false, invalidStepInput(fmt.Sprintf("max_concurrency must be between 1 and %d", models.DeliveryPlanExecutionMaxConcurrency))
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return models.DeliveryPlanExecution{}, false, invalidStepInput("idempotency_key must contain 1 to 128 characters")
	}
	now = normalizeNow(now)

	// Serialize creation and replay on the parent task. A retried queue message
	// cannot produce a second plan execution for the same task.
	var parent models.AutomationTask
	if err := tx.Select("id", "delivery_work_item_id").
		Clauses(clause.Locking{Strength: "UPDATE"}).First(&parent, "id = ?", parentTaskID).Error; err != nil {
		return models.DeliveryPlanExecution{}, false, err
	}
	if parent.DeliveryWorkItemID == nil {
		return models.DeliveryPlanExecution{}, false, ErrPlanExecutionParentMismatch
	}

	var existing models.DeliveryPlanExecution
	err := tx.Select("id", "automation_task_id", "idempotency_key", "plan_id", "plan_version", "approved_gate_id", "plan_hash", "max_concurrency", "status", "dispatched_at", "started_at", "completed_at", "created_at", "updated_at").
		Clauses(clause.Locking{Strength: "UPDATE"}).Take(&existing, "automation_task_id = ?", parentTaskID).Error
	if err == nil {
		if existing.PlanID != planID || existing.IdempotencyKey != idempotencyKey || existing.MaxConcurrency != maxConcurrency {
			return models.DeliveryPlanExecution{}, false, ErrPlanExecutionConflict
		}
		var plan models.DeliveryPlan
		if err := tx.Select("id", "work_item_id").Take(&plan, "id = ?", existing.PlanID).Error; err != nil {
			return models.DeliveryPlanExecution{}, false, err
		}
		if plan.WorkItemID != *parent.DeliveryWorkItemID {
			return models.DeliveryPlanExecution{}, false, ErrPlanExecutionParentMismatch
		}
		return existing, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return models.DeliveryPlanExecution{}, false, err
	}

	parent, plan, steps, dependencies, err := loadApprovedExecutionSnapshot(tx, parent, planID)
	if err != nil {
		return models.DeliveryPlanExecution{}, false, err
	}
	// loadApprovedExecutionSnapshot holds an UPDATE lock on the plan and its
	// steps. A legacy claimant holds a SHARE lock on that same plan row. Once
	// this lock is acquired, reject fan-out if that older path already owns a
	// live step without a persisted machine/profile assignment.
	liveLegacyLease, err := hasLiveUnassignedPlanStepLease(tx, plan.ID, time.Now().UTC())
	if err != nil {
		return models.DeliveryPlanExecution{}, false, err
	}
	if liveLegacyLease {
		return models.DeliveryPlanExecution{}, false, fmt.Errorf("%w: the approved plan has a live legacy step lease without a machine assignment", ErrPlanExecutionConflict)
	}
	hash, err := ApprovedPlanContentHash(plan, steps, dependencies)
	if err != nil {
		return models.DeliveryPlanExecution{}, false, err
	}
	id, err := uuid.NewV4()
	if err != nil {
		return models.DeliveryPlanExecution{}, false, fmt.Errorf("generate delivery plan execution identity: %w", err)
	}
	execution := models.DeliveryPlanExecution{
		ID: id, AutomationTaskID: parent.ID, IdempotencyKey: idempotencyKey,
		PlanID: plan.ID, PlanVersion: plan.Version, ApprovedGateID: *plan.ApprovedGateID,
		PlanHash: hash, MaxConcurrency: maxConcurrency, Status: models.DeliveryPlanExecutionPending,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&execution).Error; err != nil {
		return models.DeliveryPlanExecution{}, false, err
	}
	return execution, true, nil
}

// hasLiveUnassignedPlanStepLease is called only after loadApprovedExecutionSnapshot
// locks the plan row FOR UPDATE. Legacy worker claims take FOR SHARE on the
// same row, so this check and the reciprocal claim guard serialize both race
// orders without introducing an independent lock protocol.
func hasLiveUnassignedPlanStepLease(tx *gorm.DB, planID uuid.UUID, now time.Time) (bool, error) {
	if tx == nil || planID == uuid.Nil {
		return false, invalidStepInput("transaction and plan_id are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	var count int64
	if err := tx.Model(&models.DeliveryPlanStep{}).
		Where("plan_id = ? AND status = ? AND lease_expires_at > ?", planID, models.DeliveryPlanStepRunning, now).
		Where(`NOT EXISTS (
			SELECT 1 FROM delivery_plan_step_assignments AS assignment
			WHERE assignment.delivery_plan_step_id = delivery_plan_steps.id
		)`).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ReserveReadyAssignmentsInTransaction assigns already-provisioned child
// AutomationTasks to eligible steps in the same caller-owned transaction.
// childTaskIDs maps DeliveryPlanStep ID to child AutomationTask ID. This
// service never creates child tasks or outbox rows: the caller must create
// each child task first in this transaction, with the same parent work item
// and organization. Repeating the same mapping returns its existing rows;
// conflicting mappings fail closed.
func ReserveReadyAssignmentsInTransaction(
	tx *gorm.DB,
	executionID uuid.UUID,
	limit int,
	childTaskIDs map[uuid.UUID]uuid.UUID,
	now time.Time,
) ([]models.DeliveryPlanStepAssignment, error) {
	if tx == nil {
		return nil, invalidStepInput("transaction is required")
	}
	if executionID == uuid.Nil {
		return nil, invalidStepInput("execution_id is required")
	}
	if limit < 1 || limit > models.DeliveryPlanExecutionMaxConcurrency {
		return nil, invalidStepInput(fmt.Sprintf("limit must be between 1 and %d", models.DeliveryPlanExecutionMaxConcurrency))
	}
	if len(childTaskIDs) == 0 {
		return []models.DeliveryPlanStepAssignment{}, nil
	}
	if len(childTaskIDs) > limit || len(childTaskIDs) > models.DeliveryPlanExecutionMaxConcurrency {
		return nil, invalidStepInput("child task mapping exceeds the requested reservation limit")
	}
	now = normalizeNow(now)

	// Read the parent identity without a lock, then acquire locks in the same
	// parent-before-execution order used by CreateExecutionInTransaction.
	var executionIdentity models.DeliveryPlanExecution
	if err := tx.Select("id", "automation_task_id").Take(&executionIdentity, "id = ?", executionID).Error; err != nil {
		return nil, err
	}
	var parent models.AutomationTask
	if err := tx.Select("id", "delivery_work_item_id").
		Clauses(clause.Locking{Strength: "UPDATE"}).First(&parent, "id = ?", executionIdentity.AutomationTaskID).Error; err != nil {
		return nil, err
	}
	if parent.DeliveryWorkItemID == nil {
		return nil, ErrPlanExecutionParentMismatch
	}

	var execution models.DeliveryPlanExecution
	if err := tx.Select("id", "automation_task_id", "idempotency_key", "plan_id", "plan_version", "approved_gate_id", "plan_hash", "max_concurrency", "status", "dispatched_at", "started_at", "completed_at", "created_at", "updated_at").
		Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "id = ?", executionID).Error; err != nil {
		return nil, err
	}
	if execution.AutomationTaskID != parent.ID || execution.ID != executionIdentity.ID {
		return nil, ErrPlanExecutionConflict
	}
	if execution.MaxConcurrency < 1 || execution.MaxConcurrency > models.DeliveryPlanExecutionMaxConcurrency {
		return nil, invalidStepInput("persisted execution max_concurrency is invalid")
	}
	switch execution.Status {
	case models.DeliveryPlanExecutionPending, models.DeliveryPlanExecutionDispatching, models.DeliveryPlanExecutionRunning:
	default:
		return nil, ErrPlanExecutionConflict
	}

	parentScope, err := loadTaskOrganizationScope(tx, parent.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPlanExecutionParentMismatch
		}
		return nil, err
	}
	if parentScope.WorkItemID != *parent.DeliveryWorkItemID {
		return nil, ErrPlanExecutionParentMismatch
	}
	parent, plan, steps, dependencies, err := loadApprovedExecutionSnapshot(tx, parent, execution.PlanID)
	if err != nil {
		return nil, err
	}
	if plan.Version != execution.PlanVersion || plan.ApprovedGateID == nil || *plan.ApprovedGateID != execution.ApprovedGateID {
		return nil, ErrPlanExecutionApproval
	}
	hash, err := ApprovedPlanContentHash(plan, steps, dependencies)
	if err != nil {
		return nil, err
	}
	if hash != execution.PlanHash {
		return nil, fmt.Errorf("%w: approved plan content changed after execution creation", ErrPlanExecutionConflict)
	}

	stepIDs, err := normalizeStepTaskMapping(childTaskIDs)
	if err != nil {
		return nil, err
	}
	childTaskIDList := make([]uuid.UUID, 0, len(stepIDs))
	for _, stepID := range stepIDs {
		childTaskIDList = append(childTaskIDList, childTaskIDs[stepID])
	}
	if err := validateChildTaskScopes(tx, childTaskIDList, parentScope); err != nil {
		return nil, err
	}

	// Matching rows make retries idempotent. A different child task for the
	// same execution/step is a conflict, not an implicit reassignment.
	var existing []models.DeliveryPlanStepAssignment
	if err := tx.Where("execution_id = ? AND delivery_plan_step_id IN ?", execution.ID, stepIDs).
		Order("delivery_plan_step_id ASC, id ASC").Find(&existing).Error; err != nil {
		return nil, err
	}
	result := make([]models.DeliveryPlanStepAssignment, 0, len(stepIDs))
	existingByStep := make(map[uuid.UUID]models.DeliveryPlanStepAssignment, len(existing))
	for _, assignment := range existing {
		existingByStep[assignment.DeliveryPlanStepID] = assignment
		if childTaskIDs[assignment.DeliveryPlanStepID] != assignment.ChildAutomationTaskID {
			return nil, ErrPlanExecutionConflict
		}
		if strings.TrimSpace(assignment.TargetMachineID) == "" || strings.TrimSpace(assignment.TargetAgentKey) == "" {
			return nil, fmt.Errorf("%w: an existing plan-step assignment has no persisted machine/profile target", ErrNoEligibleAgentMachine)
		}
		result = append(result, assignment)
	}
	newStepIDs := make([]uuid.UUID, 0, len(stepIDs)-len(existing))
	for _, stepID := range stepIDs {
		if _, found := existingByStep[stepID]; !found {
			newStepIDs = append(newStepIDs, stepID)
		}
	}
	if len(newStepIDs) == 0 {
		sort.Slice(result, func(i, j int) bool {
			return result[i].DeliveryPlanStepID.String() < result[j].DeliveryPlanStepID.String()
		})
		return result, nil
	}

	var activeCount int64
	if err := tx.Model(&models.DeliveryPlanStepAssignment{}).
		Where("execution_id = ? AND status IN ?", execution.ID, activePlanStepAssignmentStatuses()).Count(&activeCount).Error; err != nil {
		return nil, err
	}
	available := execution.MaxConcurrency - int(activeCount)
	newLimit := limit - len(existing)
	if newLimit > available {
		newLimit = available
	}
	if newLimit < len(newStepIDs) {
		return nil, ErrPlanExecutionCapacity
	}

	var readySteps []models.DeliveryPlanStep
	if err := tx.Model(&models.DeliveryPlanStep{}).
		Where("plan_id = ? AND id IN ? AND status IN ?", execution.PlanID, newStepIDs, []string{models.DeliveryPlanStepPlanned, models.DeliveryPlanStepReady}).
		Where(`NOT EXISTS (
			SELECT 1 FROM delivery_plan_step_dependencies AS edge
			JOIN delivery_plan_steps AS dependency ON dependency.id = edge.depends_on_step_id
			WHERE edge.step_id = delivery_plan_steps.id
			AND (edge.plan_id <> delivery_plan_steps.plan_id OR dependency.plan_id <> delivery_plan_steps.plan_id OR dependency.status <> ?)
		)`, models.DeliveryPlanStepCompleted).
		Where(`NOT EXISTS (
			SELECT 1 FROM delivery_plan_step_assignments AS assignment
			WHERE assignment.execution_id = ?
			AND assignment.delivery_plan_step_id = delivery_plan_steps.id
		)`, execution.ID).
		Order("display_order ASC, id ASC").Limit(newLimit).
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Find(&readySteps).Error; err != nil {
		return nil, err
	}
	if len(readySteps) != len(newStepIDs) {
		return nil, fmt.Errorf("%w: one or more mapped steps are not currently ready", ErrPlanExecutionConflict)
	}
	targets, err := resolveAssignmentTargets(tx, parentScope.WorkItemID, readySteps, now)
	if err != nil {
		return nil, err
	}

	for _, step := range readySteps {
		assignmentID, err := uuid.NewV4()
		if err != nil {
			return nil, fmt.Errorf("generate delivery plan step assignment identity: %w", err)
		}
		assignment := models.DeliveryPlanStepAssignment{
			ID: assignmentID, ExecutionID: execution.ID, DeliveryPlanStepID: step.ID,
			ChildAutomationTaskID: childTaskIDs[step.ID], TargetMachineID: targets[step.ID].MachineID,
			TargetAgentKey: targets[step.ID].AgentKey, Status: models.DeliveryPlanStepAssignmentPending,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&assignment).Error; err != nil {
			return nil, err
		}
		result = append(result, assignment)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].DeliveryPlanStepID.String() < result[j].DeliveryPlanStepID.String()
	})
	return result, nil
}

// resolveAssignmentTargets selects an immutable machine/profile pair for each
// newly reserved step. The context is read from the work-item snapshots (not
// client payloads or mutable project settings), and a worker is eligible only
// when one recent, active process reports every frozen local repository as
// ready inside the supported Docker sandbox. Heartbeats are locked in a stable
// order before slot counts are read so parallel reservations cannot oversubscribe
// the same machine/profile capacity.
func resolveAssignmentTargets(tx *gorm.DB, workItemID uuid.UUID, steps []models.DeliveryPlanStep, now time.Time) (map[uuid.UUID]assignmentTarget, error) {
	if tx == nil || workItemID == uuid.Nil || len(steps) == 0 {
		return nil, invalidStepInput("work item and ready plan steps are required to select a machine target")
	}
	workspaceIDs, err := frozenLocalWorkspaceIDs(tx, workItemID)
	if err != nil {
		return nil, err
	}
	if len(workspaceIDs) == 0 {
		return nil, fmt.Errorf("%w: approved work item has no frozen local workspace repository", ErrNoEligibleAgentMachine)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = normalizeNow(now)
	}

	var heartbeats []assignmentHeartbeatRow
	if err := tx.Table("automation_agent_heartbeats AS heartbeat").
		Select("heartbeat.worker_id, heartbeat.agent_key, heartbeat.machine_id, heartbeat.concurrency, heartbeat.draining, heartbeat.capabilities_json, heartbeat.protocols_json, heartbeat.workspace_readiness, heartbeat.last_seen_at, profile.operations_json AS profile_operations_json").
		Joins("JOIN automation_agent_profiles AS profile ON profile.agent_key = heartbeat.agent_key AND profile.active = ?", true).
		Where("heartbeat.last_seen_at >= ? AND heartbeat.draining = ? AND heartbeat.machine_id <> ''", now.Add(-assignmentHeartbeatMaxAge), false).
		Order("heartbeat.machine_id ASC, heartbeat.agent_key ASC, heartbeat.worker_id ASC").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Scan(&heartbeats).Error; err != nil {
		return nil, err
	}

	candidatesByKey := make(map[assignmentTargetKey]*assignmentTargetCandidate)
	for _, heartbeat := range heartbeats {
		if !assignmentHeartbeatEligible(heartbeat, workspaceIDs, "", now) {
			continue
		}
		key := assignmentTargetKey{MachineID: heartbeat.MachineID, AgentKey: heartbeat.AgentKey}
		candidate := candidatesByKey[key]
		if candidate == nil {
			candidate = &assignmentTargetCandidate{Key: key}
			candidatesByKey[key] = candidate
		}
		candidate.Capacity += heartbeat.Concurrency
	}
	if len(candidatesByKey) == 0 {
		return nil, fmt.Errorf("%w: no recent, active implementation profile reports every frozen local workspace ready in Docker", ErrNoEligibleAgentMachine)
	}

	var reservations []struct {
		MachineID string `gorm:"column:target_machine_id"`
		AgentKey  string `gorm:"column:target_agent_key"`
		Count     int    `gorm:"column:reservation_count"`
	}
	// Assignment rows reserve machine/profile slots globally, across parent
	// tasks and plan executions. Keep the target pair stable through lease
	// expiry and failover: recovery moves this same row, while terminal status
	// releases its slot. The local queue still decides which worker claims work.
	if err := tx.Model(&models.DeliveryPlanStepAssignment{}).
		Select("target_machine_id, target_agent_key, COUNT(*) AS reservation_count").
		Where("target_machine_id <> '' AND target_agent_key <> '' AND status IN ?", activePlanStepAssignmentStatuses()).
		Group("target_machine_id, target_agent_key").Scan(&reservations).Error; err != nil {
		return nil, err
	}
	for _, reservation := range reservations {
		if candidate := candidatesByKey[assignmentTargetKey{MachineID: reservation.MachineID, AgentKey: reservation.AgentKey}]; candidate != nil {
			candidate.Reserved = reservation.Count
		}
	}

	candidates := make([]*assignmentTargetCandidate, 0, len(candidatesByKey))
	for _, candidate := range candidatesByKey {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Key.MachineID != candidates[j].Key.MachineID {
			return candidates[i].Key.MachineID < candidates[j].Key.MachineID
		}
		return candidates[i].Key.AgentKey < candidates[j].Key.AgentKey
	})

	return allocateAssignmentTargets(steps, candidates)
}

func allocateAssignmentTargets(steps []models.DeliveryPlanStep, candidates []*assignmentTargetCandidate) (map[uuid.UUID]assignmentTarget, error) {
	if len(steps) == 0 {
		return map[uuid.UUID]assignmentTarget{}, nil
	}
	// Allocate constrained steps first. A flexible step (AgentKey empty) can
	// run on any eligible profile, while a constrained step can use only one.
	// Preserving display order for ties keeps this deterministic without letting
	// an early flexible step consume the only slot for a later required profile.
	orderedSteps := append([]models.DeliveryPlanStep(nil), steps...)
	sort.Slice(orderedSteps, func(i, j int) bool {
		leftAgent := strings.TrimSpace(orderedSteps[i].AgentKey)
		rightAgent := strings.TrimSpace(orderedSteps[j].AgentKey)
		if (leftAgent == "") != (rightAgent == "") {
			return leftAgent != ""
		}
		if leftAgent != rightAgent {
			return leftAgent < rightAgent
		}
		if orderedSteps[i].DisplayOrder != orderedSteps[j].DisplayOrder {
			return orderedSteps[i].DisplayOrder < orderedSteps[j].DisplayOrder
		}
		return orderedSteps[i].ID.String() < orderedSteps[j].ID.String()
	})

	cursors := make(map[string]int)
	targets := make(map[uuid.UUID]assignmentTarget, len(steps))
	for _, step := range orderedSteps {
		requiredAgent := strings.TrimSpace(step.AgentKey)
		matching := make([]*assignmentTargetCandidate, 0, len(candidates))
		for _, candidate := range candidates {
			if requiredAgent != "" && candidate.Key.AgentKey != requiredAgent {
				continue
			}
			if candidate.Capacity-candidate.Reserved > 0 {
				matching = append(matching, candidate)
			}
		}
		if len(matching) == 0 {
			return nil, fmt.Errorf("%w: compatible local machines have insufficient free concurrency for all ready steps", ErrNoEligibleAgentMachine)
		}
		cursor := cursors[requiredAgent] % len(matching)
		chosen := matching[cursor]
		chosen.Reserved++
		cursors[requiredAgent] = cursor + 1
		targets[step.ID] = assignmentTarget{MachineID: chosen.Key.MachineID, AgentKey: chosen.Key.AgentKey}
	}
	return targets, nil
}

func frozenLocalWorkspaceIDs(tx *gorm.DB, workItemID uuid.UUID) ([]string, error) {
	var snapshots []models.DeliveryContextSnapshot
	if err := tx.Select("kind", "reference").Where("work_item_id = ?", workItemID).Order("reference ASC").Find(&snapshots).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(snapshots))
	ids := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if !strings.EqualFold(strings.TrimSpace(snapshot.Kind), "repository") || !strings.HasPrefix(strings.TrimSpace(snapshot.Reference), "workspace://") {
			continue
		}
		id := strings.TrimPrefix(strings.TrimSpace(snapshot.Reference), "workspace://")
		if !frozenWorkspaceIDPattern.MatchString(id) {
			return nil, fmt.Errorf("%w: a frozen local workspace reference is not canonical", ErrNoEligibleAgentMachine)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func assignmentHeartbeatEligible(heartbeat assignmentHeartbeatRow, workspaceIDs []string, requiredAgentKey string, now time.Time) bool {
	if heartbeat.WorkerID == "" || heartbeat.AgentKey == "" || heartbeat.Concurrency < 1 || heartbeat.Concurrency > 8 || heartbeat.Draining || heartbeat.LastSeenAt.Before(now.Add(-assignmentHeartbeatMaxAge)) || heartbeat.LastSeenAt.After(now.Add(5*time.Minute)) {
		return false
	}
	if _, err := uuid.FromString(heartbeat.WorkerID); err != nil {
		return false
	}
	if machineID, err := uuid.FromString(strings.TrimSpace(heartbeat.MachineID)); err != nil || machineID == uuid.Nil {
		return false
	}
	if !planStepAgentKeyValid(heartbeat.AgentKey) {
		return false
	}
	if requiredAgentKey != "" && heartbeat.AgentKey != requiredAgentKey {
		return false
	}
	var profileOperations, capabilities, protocols []string
	if json.Unmarshal([]byte(heartbeat.ProfileOperationsJSON), &profileOperations) != nil || !containsAssignmentOperation(profileOperations) {
		return false
	}
	if json.Unmarshal([]byte(heartbeat.CapabilitiesJSON), &capabilities) != nil || (len(capabilities) > 0 && !containsAssignmentOperation(capabilities)) {
		return false
	}
	if json.Unmarshal([]byte(heartbeat.ProtocolsJSON), &protocols) != nil || !supportsDeliveryPlanStepsProtocol(protocols) {
		return false
	}
	var readiness []assignmentWorkspaceReadiness
	if json.Unmarshal([]byte(heartbeat.WorkspaceReadiness), &readiness) != nil {
		return false
	}
	readyByID := make(map[string]bool, len(readiness))
	for _, workspace := range readiness {
		if !frozenWorkspaceIDPattern.MatchString(workspace.ID) {
			return false
		}
		if _, duplicate := readyByID[workspace.ID]; duplicate {
			return false
		}
		readyByID[workspace.ID] = workspace.Ready && workspace.SandboxReady && strings.EqualFold(strings.TrimSpace(workspace.IsolationMode), "docker_container")
	}
	for _, requiredID := range workspaceIDs {
		if !readyByID[requiredID] {
			return false
		}
	}
	return true
}

// supportsDeliveryPlanStepsProtocol deliberately fails closed for legacy,
// malformed, duplicate, or not-yet-understood protocol advertisements. The
// profile operation and capabilities answer what work is allowed; this
// separate runtime contract proves the process can claim/fence/report a step.
func supportsDeliveryPlanStepsProtocol(protocols []string) bool {
	return agentprotocol.Supports(protocols, agentprotocol.ProtocolDeliveryPlanStepsV1)
}

// ValidateStepProtocolWorker is the final admission check at the step-claim
// boundary. It also protects the legacy sequential-claim path, which has no
// persisted machine assignment for ValidateAssignmentWorker to inspect.
func ValidateStepProtocolWorker(tx *gorm.DB, workerID, agentKey, machineID string, instanceID uuid.UUID, now time.Time) error {
	if tx == nil || strings.TrimSpace(workerID) == "" || strings.TrimSpace(agentKey) == "" || strings.TrimSpace(machineID) == "" || instanceID == uuid.Nil {
		return fmt.Errorf("%w: authenticated worker identity is incomplete", ErrNoEligibleAgentMachine)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	type protocolHeartbeat struct {
		ProtocolsJSON string    `gorm:"column:protocols_json"`
		Draining      bool      `gorm:"column:draining"`
		LastSeenAt    time.Time `gorm:"column:last_seen_at"`
	}
	var heartbeat protocolHeartbeat
	if err := tx.Table("automation_agent_heartbeats").
		Select("protocols_json, draining, last_seen_at").
		Where("worker_id = ? AND agent_key = ? AND machine_id = ? AND agent_instance_id = ? AND last_seen_at >= ? AND last_seen_at <= ?", workerID, agentKey, machineID, instanceID, now.Add(-assignmentHeartbeatMaxAge), now.Add(5*time.Minute)).
		Take(&heartbeat).Error; err != nil {
		return fmt.Errorf("%w: no fresh heartbeat is bound to the authenticated runtime instance", ErrNoEligibleAgentMachine)
	}
	if heartbeat.Draining || heartbeat.LastSeenAt.Before(now.Add(-assignmentHeartbeatMaxAge)) || heartbeat.LastSeenAt.After(now.Add(5*time.Minute)) {
		return fmt.Errorf("%w: runtime instance is stale or draining", ErrNoEligibleAgentMachine)
	}
	var protocols []string
	if json.Unmarshal([]byte(heartbeat.ProtocolsJSON), &protocols) != nil || !supportsDeliveryPlanStepsProtocol(protocols) {
		return fmt.Errorf("%w: runtime instance does not advertise the required plan-step protocol", ErrNoEligibleAgentMachine)
	}
	return nil
}

func containsAssignmentOperation(operations []string) bool {
	for _, operation := range operations {
		if strings.TrimSpace(operation) == "delivery.implementation" {
			return true
		}
	}
	return false
}

func planStepAgentKeyValid(agentKey string) bool {
	return agentKey == strings.TrimSpace(agentKey) && stepAgentKeyPattern.MatchString(agentKey)
}

// ValidateAssignmentWorker verifies the worker and, only after both the old
// task/step leases and the old machine heartbeat have expired, may move the
// existing logical assignment to another compatible machine. The caller's
// transaction must be the task-claim transaction: this function locks the
// child task first, then the one assignment row, so competing claimers cannot
// split ownership. It returns the clock captured after those locks are held;
// callers must use it for the ensuing task lease.
//
// The queue's target_machine_id is a routing hint, not an authorization
// boundary. The persisted assignment and this server-side check remain
// authoritative. A successful move updates that same assignment row; it does
// not create a second assignment or a new queue/outbox record.
func ValidateAssignmentWorker(tx *gorm.DB, assignment *models.DeliveryPlanStepAssignment, workItemID uuid.UUID, workerID, agentKey, machineID string, _ time.Time) (time.Time, error) {
	if tx == nil || assignment == nil || assignment.ID == uuid.Nil || assignment.ChildAutomationTaskID == uuid.Nil || assignment.DeliveryPlanStepID == uuid.Nil || workItemID == uuid.Nil || strings.TrimSpace(assignment.TargetMachineID) == "" || strings.TrimSpace(assignment.TargetAgentKey) == "" || strings.TrimSpace(workerID) == "" || strings.TrimSpace(agentKey) != strings.TrimSpace(assignment.TargetAgentKey) || strings.TrimSpace(machineID) == "" {
		return time.Time{}, fmt.Errorf("%w: current worker does not match the persisted plan-step profile target", ErrNoEligibleAgentMachine)
	}
	var task models.AutomationTask
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "delivery_work_item_id", "status", "run_id", "worker_id", "agent_key", "machine_id", "lease_expires_at").
		First(&task, "id = ?", assignment.ChildAutomationTaskID).Error; err != nil {
		return time.Time{}, err
	}
	// A claim may have waited behind a callback or another scheduler. Never use
	// its pre-lock timestamp to decide whether either lease is still live.
	now := time.Now().UTC()
	if task.DeliveryWorkItemID == nil || *task.DeliveryWorkItemID != workItemID {
		return time.Time{}, fmt.Errorf("%w: assigned child task no longer belongs to the frozen work item", ErrNoEligibleAgentMachine)
	}
	var current models.DeliveryPlanStepAssignment
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&current, "id = ? AND child_automation_task_id = ?", assignment.ID, task.ID).Error; err != nil {
		return time.Time{}, err
	}
	if current.ExecutionID != assignment.ExecutionID || current.DeliveryPlanStepID != assignment.DeliveryPlanStepID || current.TargetAgentKey != assignment.TargetAgentKey {
		return time.Time{}, fmt.Errorf("%w: the persisted plan-step assignment changed during claim", ErrNoEligibleAgentMachine)
	}
	if !isActiveAssignmentStatus(current.Status) {
		return time.Time{}, fmt.Errorf("%w: the persisted plan-step assignment is terminal", ErrNoEligibleAgentMachine)
	}
	var step models.DeliveryPlanStep
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "agent_key", "status", "lease_expires_at").
		First(&step, "id = ?", current.DeliveryPlanStepID).Error; err != nil {
		return time.Time{}, err
	}
	workspaceIDs, err := frozenLocalWorkspaceIDs(tx, workItemID)
	if err != nil {
		return time.Time{}, err
	}
	if len(workspaceIDs) == 0 {
		return time.Time{}, fmt.Errorf("%w: work item no longer has a frozen local workspace repository", ErrNoEligibleAgentMachine)
	}

	if current.TargetMachineID != machineID {
		if err := validateAssignmentFailoverLease(task, step, now); err != nil {
			return time.Time{}, err
		}
		if err := requireAssignmentTargetHeartbeatExpired(tx, current.TargetMachineID, current.TargetAgentKey, now); err != nil {
			return time.Time{}, err
		}
		capacity, candidateReady, err := lockAssignmentFailoverTarget(tx, workerID, machineID, current.TargetAgentKey, workspaceIDs, step.AgentKey, now)
		if err != nil {
			return time.Time{}, err
		}
		if !candidateReady {
			return time.Time{}, fmt.Errorf("%w: replacement worker is not recent, active, and ready for every frozen workspace", ErrNoEligibleAgentMachine)
		}
		var reserved int64
		if err := tx.Model(&models.DeliveryPlanStepAssignment{}).
			Where("target_machine_id = ? AND target_agent_key = ? AND status IN ?", machineID, current.TargetAgentKey, activePlanStepAssignmentStatuses()).
			Count(&reserved).Error; err != nil {
			return time.Time{}, err
		}
		if int64(capacity) <= reserved {
			return time.Time{}, fmt.Errorf("%w: replacement machine has no free assignment capacity", ErrNoEligibleAgentMachine)
		}
		result := tx.Model(&models.DeliveryPlanStepAssignment{}).
			Where("id = ? AND child_automation_task_id = ? AND target_machine_id = ? AND target_agent_key = ? AND status IN ?", current.ID, task.ID, current.TargetMachineID, current.TargetAgentKey, activePlanStepAssignmentStatuses()).
			Updates(map[string]any{
				"target_machine_id": machineID,
				"status":            models.DeliveryPlanStepAssignmentQueued,
				"updated_at":        now,
			})
		if result.Error != nil {
			return time.Time{}, result.Error
		}
		if result.RowsAffected != 1 {
			return time.Time{}, fmt.Errorf("%w: plan-step assignment was concurrently claimed", ErrNoEligibleAgentMachine)
		}
		current.TargetMachineID = machineID
		current.Status = models.DeliveryPlanStepAssignmentQueued
		current.UpdatedAt = now
	}
	if current.TargetAgentKey != agentKey || current.TargetMachineID != machineID {
		return time.Time{}, fmt.Errorf("%w: current worker does not match the persisted plan-step target", ErrNoEligibleAgentMachine)
	}
	if err := validateCurrentAssignmentHeartbeat(tx, workerID, machineID, current.TargetAgentKey, workspaceIDs, step.AgentKey, now); err != nil {
		return time.Time{}, err
	}
	*assignment = current
	return now, nil
}

func activePlanStepAssignmentStatuses() []string {
	return []string{models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued, models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning}
}

func isActiveAssignmentStatus(status string) bool {
	for _, active := range activePlanStepAssignmentStatuses() {
		if status == active {
			return true
		}
	}
	return false
}

func validateAssignmentFailoverLease(task models.AutomationTask, step models.DeliveryPlanStep, now time.Time) error {
	if err := validateAssignmentClaimLeaseState(task, step, now); err != nil {
		return err
	}
	if step.Status == models.DeliveryPlanStepRunning && step.LeaseExpiresAt == nil {
		return fmt.Errorf("%w: running plan step has no durable lease expiry", ErrNoEligibleAgentMachine)
	}
	return nil
}

func validateAssignmentClaimLeaseState(task models.AutomationTask, step models.DeliveryPlanStep, now time.Time) error {
	if task.LeaseExpiresAt != nil && task.LeaseExpiresAt.After(now) {
		return fmt.Errorf("%w: child task lease is still active", ErrNoEligibleAgentMachine)
	}
	if step.LeaseExpiresAt != nil && step.LeaseExpiresAt.After(now) {
		return fmt.Errorf("%w: plan-step lease is still active", ErrNoEligibleAgentMachine)
	}
	switch task.Status {
	case "pending", "queued":
	case "running":
		if task.LeaseExpiresAt == nil {
			return fmt.Errorf("%w: running child task has no durable lease expiry", ErrNoEligibleAgentMachine)
		}
	default:
		return fmt.Errorf("%w: child task is not recoverable", ErrNoEligibleAgentMachine)
	}
	switch step.Status {
	case models.DeliveryPlanStepPlanned, models.DeliveryPlanStepReady:
	case models.DeliveryPlanStepRunning:
		if step.LeaseExpiresAt == nil {
			return fmt.Errorf("%w: running plan step has no durable lease expiry", ErrNoEligibleAgentMachine)
		}
	default:
		return fmt.Errorf("%w: plan step is not recoverable", ErrNoEligibleAgentMachine)
	}
	return nil
}

func requireAssignmentTargetHeartbeatExpired(tx *gorm.DB, machineID, agentKey string, now time.Time) error {
	var heartbeats []struct {
		WorkerID   string    `gorm:"column:worker_id"`
		LastSeenAt time.Time `gorm:"column:last_seen_at"`
	}
	if err := tx.Table("automation_agent_heartbeats").Select("worker_id, last_seen_at").
		Where("machine_id = ? AND agent_key = ?", machineID, agentKey).
		Order("worker_id ASC").Clauses(clause.Locking{Strength: "UPDATE"}).Scan(&heartbeats).Error; err != nil {
		return err
	}
	for _, heartbeat := range heartbeats {
		if !heartbeat.LastSeenAt.Before(now.Add(-assignmentHeartbeatMaxAge)) {
			return fmt.Errorf("%w: current target machine heartbeat has not expired", ErrNoEligibleAgentMachine)
		}
	}
	return nil
}

func lockAssignmentFailoverTarget(tx *gorm.DB, workerID, machineID, agentKey string, workspaceIDs []string, requiredAgentKey string, now time.Time) (int, bool, error) {
	var heartbeats []assignmentHeartbeatRow
	if err := tx.Table("automation_agent_heartbeats AS heartbeat").
		Select("heartbeat.worker_id, heartbeat.agent_key, heartbeat.machine_id, heartbeat.concurrency, heartbeat.draining, heartbeat.capabilities_json, heartbeat.protocols_json, heartbeat.workspace_readiness, heartbeat.last_seen_at, profile.operations_json AS profile_operations_json").
		Joins("JOIN automation_agent_profiles AS profile ON profile.agent_key = heartbeat.agent_key AND profile.active = ?", true).
		Where("heartbeat.machine_id = ? AND heartbeat.agent_key = ? AND heartbeat.last_seen_at >= ? AND heartbeat.draining = ?", machineID, agentKey, now.Add(-assignmentHeartbeatMaxAge), false).
		Order("heartbeat.worker_id ASC").
		Clauses(clause.Locking{Strength: "UPDATE"}).Scan(&heartbeats).Error; err != nil {
		return 0, false, err
	}
	capacity := 0
	workerReady := false
	for _, heartbeat := range heartbeats {
		if !assignmentHeartbeatEligible(heartbeat, workspaceIDs, requiredAgentKey, now) {
			continue
		}
		capacity += heartbeat.Concurrency
		if heartbeat.WorkerID == workerID {
			workerReady = true
		}
	}
	return capacity, workerReady, nil
}

func validateCurrentAssignmentHeartbeat(tx *gorm.DB, workerID, machineID, agentKey string, workspaceIDs []string, requiredAgentKey string, now time.Time) error {
	capacity, workerReady, err := lockAssignmentFailoverTarget(tx, workerID, machineID, agentKey, workspaceIDs, requiredAgentKey, now)
	if err != nil {
		return err
	}
	if !workerReady || capacity < 1 {
		return fmt.Errorf("%w: current worker heartbeat no longer matches the assigned project workspaces", ErrNoEligibleAgentMachine)
	}
	return nil
}

type taskOrganizationScope struct {
	TaskID     uuid.UUID `gorm:"column:task_id"`
	WorkItemID uuid.UUID `gorm:"column:work_item_id"`
	ProjectID  uuid.UUID `gorm:"column:project_id"`
	ClientID   uuid.UUID `gorm:"column:client_id"`
}

func loadTaskOrganizationScope(tx *gorm.DB, taskID uuid.UUID) (taskOrganizationScope, error) {
	var scope taskOrganizationScope
	err := tx.Table("automation_tasks AS task").
		Select("task.id AS task_id, delivery_work_items.id AS work_item_id, delivery_work_items.project_id AS project_id, delivery_projects.client_id AS client_id").
		Joins("JOIN delivery_work_items ON delivery_work_items.id = task.delivery_work_item_id").
		Joins("JOIN delivery_projects ON delivery_projects.id = delivery_work_items.project_id").
		Where("task.id = ?", taskID).Take(&scope).Error
	return scope, err
}

func validateChildTaskScopes(tx *gorm.DB, childTaskIDs []uuid.UUID, parent taskOrganizationScope) error {
	for _, childTaskID := range childTaskIDs {
		child, err := loadTaskOrganizationScope(tx, childTaskID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: provisioned child automation task %s was not found with project scope", ErrPlanExecutionParentMismatch, childTaskID)
			}
			return err
		}
		if child.WorkItemID != parent.WorkItemID || child.ProjectID != parent.ProjectID || child.ClientID != parent.ClientID {
			return fmt.Errorf("%w: child automation task %s has a different work item or organization", ErrPlanExecutionParentMismatch, childTaskID)
		}
	}
	return nil
}

func normalizeStepTaskMapping(mapping map[uuid.UUID]uuid.UUID) ([]uuid.UUID, error) {
	stepIDs := make([]uuid.UUID, 0, len(mapping))
	seenChildren := make(map[uuid.UUID]struct{}, len(mapping))
	for stepID, childTaskID := range mapping {
		if stepID == uuid.Nil || childTaskID == uuid.Nil {
			return nil, invalidStepInput("child task mapping contains an empty step or task id")
		}
		if _, seen := seenChildren[childTaskID]; seen {
			return nil, invalidStepInput("one child automation task cannot be assigned to multiple steps")
		}
		seenChildren[childTaskID] = struct{}{}
		stepIDs = append(stepIDs, stepID)
	}
	sort.Slice(stepIDs, func(i, j int) bool { return stepIDs[i].String() < stepIDs[j].String() })
	return stepIDs, nil
}

// loadApprovedExecutionSnapshot locks and validates the current parent/plan/
// approval graph, then returns the complete plan graph used by the stable
// content hash. Locking prevents an approval or graph edit from racing the
// snapshot operation.
func loadApprovedExecutionSnapshot(
	tx *gorm.DB,
	parent models.AutomationTask,
	planID uuid.UUID,
) (models.AutomationTask, models.DeliveryPlan, []models.DeliveryPlanStep, []models.DeliveryPlanStepDependency, error) {
	var plan models.DeliveryPlan
	if err := tx.Select("id", "work_item_id", "version", "status", "approved_gate_id").
		Clauses(clause.Locking{Strength: "UPDATE"}).Take(&plan, "id = ?", planID).Error; err != nil {
		return parent, models.DeliveryPlan{}, nil, nil, err
	}
	if parent.DeliveryWorkItemID == nil || plan.WorkItemID != *parent.DeliveryWorkItemID {
		return parent, models.DeliveryPlan{}, nil, nil, ErrPlanExecutionParentMismatch
	}
	if !strings.EqualFold(strings.TrimSpace(plan.Status), "approved") || plan.Version < 1 || plan.ApprovedGateID == nil || *plan.ApprovedGateID == uuid.Nil {
		return parent, models.DeliveryPlan{}, nil, nil, ErrPlanExecutionApproval
	}

	var gate models.DeliveryGate
	if err := tx.Select("id", "work_item_id", "kind", "decision").
		Clauses(clause.Locking{Strength: "UPDATE"}).Take(&gate, "id = ?", *plan.ApprovedGateID).Error; err != nil {
		return parent, models.DeliveryPlan{}, nil, nil, err
	}
	if gate.WorkItemID != plan.WorkItemID || gate.Kind != deliveryworkflow.GatePlan || gate.Decision != deliveryworkflow.DecisionApproved {
		return parent, models.DeliveryPlan{}, nil, nil, ErrPlanExecutionApproval
	}

	var steps []models.DeliveryPlanStep
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("plan_id = ?", plan.ID).Order("display_order ASC, id ASC").Find(&steps).Error; err != nil {
		return parent, models.DeliveryPlan{}, nil, nil, err
	}
	var dependencies []models.DeliveryPlanStepDependency
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("plan_id = ?", plan.ID).Order("step_id ASC, depends_on_step_id ASC").Find(&dependencies).Error; err != nil {
		return parent, models.DeliveryPlan{}, nil, nil, err
	}
	return parent, plan, steps, dependencies, nil
}
