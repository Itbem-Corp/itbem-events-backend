package deliveryplansteps

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	MinStepLeaseTTL = 15 * time.Second
	MaxStepLeaseTTL = 15 * time.Minute
)

var (
	ErrStepLeaseConflict = errors.New("delivery plan step lease conflict")
	ErrStepInputInvalid  = errors.New("delivery plan step input invalid")
	stepAgentKeyPattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
)

// ClaimInput identifies a currently leased automation task that may claim one
// ready step from its approved delivery plan. WorkItemID is intentionally
// derived from AutomationTask in the database rather than accepted here.
type ClaimInput struct {
	PlanID           uuid.UUID
	AutomationTaskID uuid.UUID
	RunID            string
	WorkerID         string
	AgentKey         string
	MachineID        string
	LeaseTTL         time.Duration `json:"-"`
	// Now is an optional deterministic clock override for service tests. Runtime
	// callers leave it zero so lease checks use the clock after row locks land.
	Now time.Time `json:"-"`
}

// ClaimResult is the internal claim contract. Step is an explicitly protected
// model; HTTP handlers should project its approved content through a DTO.
type ClaimResult struct {
	Available      bool                    `json:"available"`
	Step           models.DeliveryPlanStep `json:"-"`
	Fence          int64                   `json:"fence,omitempty"`
	LeaseExpiresAt *time.Time              `json:"lease_expires_at,omitempty"`
}

// LeaseInput identifies the lease holder for renewal or status transitions.
type LeaseInput struct {
	StepID           uuid.UUID
	AutomationTaskID uuid.UUID
	RunID            string
	WorkerID         string
	AgentKey         string
	MachineID        string
	Fence            int64
	LeaseTTL         time.Duration `json:"-"`
	// Now is an optional deterministic clock override for service tests. Runtime
	// callers leave it zero so lease checks use the clock after row locks land.
	Now time.Time `json:"-"`
}

// TransitionInput applies one bounded lifecycle status change using the same
// fenced identity tuple acquired by ClaimNextReadyInTransaction.
type TransitionInput struct {
	StepID           uuid.UUID
	AutomationTaskID uuid.UUID
	RunID            string
	WorkerID         string
	AgentKey         string
	MachineID        string
	Fence            int64
	Status           string
	// Now is an optional deterministic clock override for service tests. Runtime
	// callers leave it zero so lease checks use the clock after row locks land.
	Now time.Time `json:"-"`
}

type leaseIdentity struct {
	AutomationTaskID uuid.UUID
	RunID            string
	WorkerID         string
	AgentKey         string
	MachineID        string
}

type leasedTask struct {
	models.AutomationTask
}

// ClaimNextReadyInTransaction atomically claims the earliest eligible step in
// an approved plan. Parallel claimers may take separate rows; SKIP LOCKED and
// a monotonically increasing fence prevent a live lease from being stolen.
func ClaimNextReadyInTransaction(tx *gorm.DB, input ClaimInput) (ClaimResult, error) {
	return claimReadyStepInTransaction(tx, input, nil)
}

// ClaimSpecificReadyInTransaction claims only the requested plan step. This
// is used by a persisted child-task assignment; it must never fall through to
// another ready node when the assigned step is unavailable.
func ClaimSpecificReadyInTransaction(tx *gorm.DB, input ClaimInput, stepID uuid.UUID) (ClaimResult, error) {
	if stepID == uuid.Nil {
		return ClaimResult{}, invalidStepInput("step_id is required for a targeted claim")
	}
	return claimReadyStepInTransaction(tx, input, &stepID)
}

func claimReadyStepInTransaction(tx *gorm.DB, input ClaimInput, targetStepID *uuid.UUID) (ClaimResult, error) {
	if tx == nil {
		return ClaimResult{}, invalidStepInput("transaction is required")
	}
	claim, ttl, err := normalizeClaimInput(input)
	if err != nil {
		return ClaimResult{}, err
	}
	var plan models.DeliveryPlan
	if err := tx.Select("id", "work_item_id", "version", "status", "approved_gate_id").
		Clauses(clause.Locking{Strength: "SHARE"}).First(&plan, "id = ?", claim.PlanID).Error; err != nil {
		return ClaimResult{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(plan.Status), "approved") || plan.ApprovedGateID == nil || *plan.ApprovedGateID == uuid.Nil {
		return ClaimResult{}, invalidStepInput("delivery plan must have a human-approved version")
	}

	task, now, err := loadLeasedTask(tx, claim.Identity, input.Now)
	if err != nil {
		return ClaimResult{}, err
	}
	if task.DeliveryWorkItemID == nil || *task.DeliveryWorkItemID != plan.WorkItemID {
		return ClaimResult{}, ErrStepLeaseConflict
	}

	// A retried claim from the same task/run returns its existing live step
	// instead of consuming another DAG node after an uncertain HTTP response.
	var active models.DeliveryPlanStep
	activeErr := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("plan_id = ? AND automation_task_id = ? AND run_id = ? AND worker_id = ? AND agent_key = ? AND machine_id = ? AND status = ? AND lease_expires_at > ?",
			plan.ID, task.ID, claim.Identity.RunID, claim.Identity.WorkerID, claim.Identity.AgentKey, claim.Identity.MachineID, models.DeliveryPlanStepRunning, now).
		Order("display_order ASC, id ASC").Take(&active).Error
	if activeErr == nil {
		if targetStepID != nil && active.ID != *targetStepID {
			return ClaimResult{}, ErrStepLeaseConflict
		}
		return resultFor(active), nil
	}
	if !errors.Is(activeErr, gorm.ErrRecordNotFound) {
		return ClaimResult{}, activeErr
	}

	var step models.DeliveryPlanStep
	eligibleStatus := []string{models.DeliveryPlanStepPlanned, models.DeliveryPlanStepReady}
	query := tx.Model(&models.DeliveryPlanStep{}).
		Where("plan_id = ?", plan.ID).
		Where("(agent_key = '' OR agent_key = ?)", claim.Identity.AgentKey).
		Where("((status IN ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)) OR (status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?))",
			eligibleStatus, now, models.DeliveryPlanStepRunning, now).
		Where(`NOT EXISTS (
			SELECT 1 FROM delivery_plan_step_dependencies AS edge
			JOIN delivery_plan_steps AS dependency ON dependency.id = edge.depends_on_step_id
			WHERE edge.step_id = delivery_plan_steps.id
			AND (edge.plan_id <> delivery_plan_steps.plan_id OR dependency.plan_id <> delivery_plan_steps.plan_id OR dependency.status <> ?)
		)`, models.DeliveryPlanStepCompleted).
		Order("display_order ASC, id ASC").
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
	if targetStepID != nil {
		query = query.Where("id = ?", *targetStepID)
	}
	if err := query.Take(&step).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ClaimResult{Available: false}, nil
		}
		return ClaimResult{}, err
	}
	if step.LeaseFence < 0 || step.LeaseFence == math.MaxInt64 {
		return ClaimResult{}, invalidStepInput("step lease fencing counter is exhausted")
	}
	newFence := step.LeaseFence + 1
	leaseExpiresAt := now.Add(ttl)
	if task.LeaseExpiresAt.Before(leaseExpiresAt) {
		leaseExpiresAt = task.LeaseExpiresAt.UTC()
	}
	if !leaseExpiresAt.After(now) {
		return ClaimResult{}, ErrStepLeaseConflict
	}

	previousStatus := step.Status
	if previousStatus == models.DeliveryPlanStepPlanned {
		if err := updatePlanStep(tx, step.ID, map[string]any{"status": models.DeliveryPlanStepReady, "updated_at": now}); err != nil {
			return ClaimResult{}, err
		}
		if err := createStepEvent(tx, step, models.DeliveryPlanStepEventReady, previousStatus, models.DeliveryPlanStepReady, "Step ready", task, newFence, now); err != nil {
			return ClaimResult{}, err
		}
		previousStatus = models.DeliveryPlanStepReady
	}
	step.Status = models.DeliveryPlanStepRunning
	step.AutomationTaskID = uuidPointer(task.ID)
	step.AutomationExecutionID = nil
	step.RunID = task.RunID
	step.WorkerID = task.WorkerID
	step.AgentKey = task.AgentKey
	step.MachineID = task.MachineID
	step.LeaseFence = newFence
	step.LeaseExpiresAt = &leaseExpiresAt
	step.UpdatedAt = now
	if step.StartedAt == nil {
		startedAt := now
		step.StartedAt = &startedAt
	}
	if err := updatePlanStep(tx, step.ID, map[string]any{
		"status": step.Status, "automation_task_id": task.ID, "automation_execution_id": nil,
		"run_id": step.RunID, "worker_id": step.WorkerID, "agent_key": step.AgentKey, "machine_id": step.MachineID,
		"lease_fence": step.LeaseFence, "lease_expires_at": leaseExpiresAt, "started_at": step.StartedAt, "updated_at": now,
	}); err != nil {
		return ClaimResult{}, err
	}
	eventType, summary := models.DeliveryPlanStepEventClaimed, "Step claimed"
	if previousStatus == models.DeliveryPlanStepRunning {
		eventType, summary = models.DeliveryPlanStepEventLeaseReclaimed, "Expired step lease reclaimed"
	}
	if err := createStepEvent(tx, step, eventType, previousStatus, step.Status, summary, task, newFence, now); err != nil {
		return ClaimResult{}, err
	}
	return resultFor(step), nil
}

// RenewStepLeaseInTransaction extends a live step lease only while both the
// task lease and step lease are current and the identity/fence tuple matches.
func RenewStepLeaseInTransaction(tx *gorm.DB, input LeaseInput) error {
	if tx == nil {
		return invalidStepInput("transaction is required")
	}
	identity, ttl, err := normalizeLeaseInput(input)
	if err != nil {
		return err
	}
	task, _, err := loadLeasedTask(tx, identity, input.Now)
	if err != nil {
		return err
	}
	step, now, err := loadFencedStep(tx, input.StepID, identity, input.Fence, task, input.Now, true)
	if err != nil {
		return err
	}
	if task.LeaseExpiresAt == nil || !task.LeaseExpiresAt.After(now) {
		return ErrStepLeaseConflict
	}
	newExpiry := now.Add(ttl)
	if task.LeaseExpiresAt.Before(newExpiry) {
		newExpiry = task.LeaseExpiresAt.UTC()
	}
	if !newExpiry.After(*step.LeaseExpiresAt) {
		return ErrStepLeaseConflict
	}
	if err := updatePlanStep(tx, step.ID, map[string]any{"lease_expires_at": newExpiry, "updated_at": now}); err != nil {
		return err
	}
	return createStepEvent(tx, step, models.DeliveryPlanStepEventLeaseRenewed, step.Status, step.Status, "Step lease renewed", task, step.LeaseFence, now)
}

// TransitionStepInTransaction changes a step state only for its live owner.
// Repeating the current state with the exact tuple is idempotent and emits no
// duplicate event; lifecycle summaries are fixed strings, never caller text.
func TransitionStepInTransaction(tx *gorm.DB, input TransitionInput) error {
	if tx == nil {
		return invalidStepInput("transaction is required")
	}
	identity, nextStatus, err := normalizeTransitionInput(input)
	if err != nil {
		return err
	}
	task, _, err := loadLeasedTask(tx, identity, input.Now)
	if err != nil {
		return err
	}
	step, now, err := loadFencedStep(tx, input.StepID, identity, input.Fence, task, input.Now, false)
	if err != nil {
		return err
	}
	if task.LeaseExpiresAt == nil || !task.LeaseExpiresAt.After(now) {
		return ErrStepLeaseConflict
	}
	if step.Status == nextStatus {
		return nil
	}
	if !CanTransitionStatus(step.Status, nextStatus) {
		return fmt.Errorf("%w: status transition %q to %q is not allowed", ErrStepInputInvalid, step.Status, nextStatus)
	}
	if !strings.EqualFold(step.Status, models.DeliveryPlanStepRunning) {
		return ErrStepLeaseConflict
	}
	updates := map[string]any{"status": nextStatus, "updated_at": now}
	if nextStatus == models.DeliveryPlanStepCompleted || nextStatus == models.DeliveryPlanStepSkipped {
		updates["completed_at"] = now
	}
	if err := updatePlanStep(tx, step.ID, updates); err != nil {
		return err
	}
	previousStatus := step.Status
	step.Status = nextStatus
	step.UpdatedAt = now
	if nextStatus == models.DeliveryPlanStepCompleted || nextStatus == models.DeliveryPlanStepSkipped {
		step.CompletedAt = &now
	}
	return createStepEvent(tx, step, models.DeliveryPlanStepEventTransitioned, previousStatus, nextStatus, summaryForStatus(nextStatus), task, step.LeaseFence, now)
}

func normalizeClaimInput(input ClaimInput) (normalizedClaim, time.Duration, error) {
	if input.PlanID == uuid.Nil || input.AutomationTaskID == uuid.Nil {
		return normalizedClaim{}, 0, invalidStepInput("plan_id and automation_task_id are required")
	}
	runID, err := canonicalUUID(input.RunID, "run_id")
	if err != nil {
		return normalizedClaim{}, 0, err
	}
	workerID, err := canonicalUUID(input.WorkerID, "worker_id")
	if err != nil {
		return normalizedClaim{}, 0, err
	}
	agentKey := strings.TrimSpace(input.AgentKey)
	if agentKey == "" {
		agentKey = "generalist"
	}
	if !stepAgentKeyPattern.MatchString(agentKey) {
		return normalizedClaim{}, 0, invalidStepInput("agent_key has an invalid format")
	}
	machineID, err := optionalCanonicalUUID(input.MachineID, "machine_id")
	if err != nil {
		return normalizedClaim{}, 0, err
	}
	ttl, err := validateStepLeaseTTL(input.LeaseTTL)
	if err != nil {
		return normalizedClaim{}, 0, err
	}
	return normalizedClaim{PlanID: input.PlanID, Identity: leaseIdentity{AutomationTaskID: input.AutomationTaskID, RunID: runID, WorkerID: workerID, AgentKey: agentKey, MachineID: machineID}}, ttl, nil
}

func normalizeLeaseInput(input LeaseInput) (leaseIdentity, time.Duration, error) {
	if input.StepID == uuid.Nil || input.AutomationTaskID == uuid.Nil || input.Fence < 1 {
		return leaseIdentity{}, 0, invalidStepInput("step_id, automation_task_id, and positive fence are required")
	}
	identity, err := normalizeLeaseIdentity(input.AutomationTaskID, input.RunID, input.WorkerID, input.AgentKey, input.MachineID)
	if err != nil {
		return leaseIdentity{}, 0, err
	}
	ttl, err := validateStepLeaseTTL(input.LeaseTTL)
	if err != nil {
		return leaseIdentity{}, 0, err
	}
	return identity, ttl, nil
}

func normalizeTransitionInput(input TransitionInput) (leaseIdentity, string, error) {
	if input.StepID == uuid.Nil || input.AutomationTaskID == uuid.Nil || input.Fence < 1 {
		return leaseIdentity{}, "", invalidStepInput("step_id, automation_task_id, and positive fence are required")
	}
	identity, err := normalizeLeaseIdentity(input.AutomationTaskID, input.RunID, input.WorkerID, input.AgentKey, input.MachineID)
	if err != nil {
		return leaseIdentity{}, "", err
	}
	status := strings.ToLower(strings.TrimSpace(input.Status))
	if !validStepStatus(status) {
		return leaseIdentity{}, "", invalidStepInput("status is not a supported delivery plan step state")
	}
	return identity, status, nil
}

func normalizeLeaseIdentity(taskID uuid.UUID, runID, workerID, agentKey, machineID string) (leaseIdentity, error) {
	canonicalRunID, err := canonicalUUID(runID, "run_id")
	if err != nil {
		return leaseIdentity{}, err
	}
	canonicalWorkerID, err := canonicalUUID(workerID, "worker_id")
	if err != nil {
		return leaseIdentity{}, err
	}
	agentKey = strings.TrimSpace(agentKey)
	if !stepAgentKeyPattern.MatchString(agentKey) {
		return leaseIdentity{}, invalidStepInput("agent_key has an invalid format")
	}
	canonicalMachineID, err := optionalCanonicalUUID(machineID, "machine_id")
	if err != nil {
		return leaseIdentity{}, err
	}
	return leaseIdentity{AutomationTaskID: taskID, RunID: canonicalRunID, WorkerID: canonicalWorkerID, AgentKey: agentKey, MachineID: canonicalMachineID}, nil
}

type normalizedClaim struct {
	PlanID   uuid.UUID
	Identity leaseIdentity
}

func loadLeasedTask(tx *gorm.DB, identity leaseIdentity, clockOverride time.Time) (leasedTask, time.Time, error) {
	var task models.AutomationTask
	if err := tx.Select("id", "delivery_work_item_id", "status", "run_id", "worker_id", "agent_key", "machine_id", "lease_expires_at").
		Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, "id = ?", identity.AutomationTaskID).Error; err != nil {
		return leasedTask{}, time.Time{}, err
	}
	// The task SELECT may have waited for another transaction. Capture time only
	// after its lock is held, so a callback cannot validate against a stale lease.
	now := leaseValidationTime(clockOverride)
	if strings.ToLower(strings.TrimSpace(task.Status)) != "running" || strings.TrimSpace(task.RunID) != identity.RunID ||
		strings.TrimSpace(task.WorkerID) != identity.WorkerID || strings.TrimSpace(task.AgentKey) != identity.AgentKey ||
		strings.TrimSpace(task.MachineID) != identity.MachineID || task.LeaseExpiresAt == nil || !task.LeaseExpiresAt.After(now) {
		return leasedTask{}, time.Time{}, ErrStepLeaseConflict
	}
	return leasedTask{AutomationTask: task}, now, nil
}

func loadFencedStep(tx *gorm.DB, stepID uuid.UUID, identity leaseIdentity, fence int64, task leasedTask, clockOverride time.Time, requireRunning bool) (models.DeliveryPlanStep, time.Time, error) {
	var step models.DeliveryPlanStep
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&step, "id = ?", stepID).Error; err != nil {
		return models.DeliveryPlanStep{}, time.Time{}, err
	}
	// A task lock does not prevent its lease from expiring while this step row
	// lock waits. Recheck both task and step against a clock captured after the
	// step lock is acquired.
	now := leaseValidationTime(clockOverride)
	if step.AutomationTaskID == nil || *step.AutomationTaskID != identity.AutomationTaskID || step.RunID != identity.RunID ||
		step.WorkerID != identity.WorkerID || step.AgentKey != identity.AgentKey || step.MachineID != identity.MachineID ||
		step.LeaseFence != fence || step.LeaseExpiresAt == nil || !step.LeaseExpiresAt.After(now) ||
		(requireRunning && step.Status != models.DeliveryPlanStepRunning) || task.LeaseExpiresAt == nil || !task.LeaseExpiresAt.After(now) {
		return models.DeliveryPlanStep{}, time.Time{}, ErrStepLeaseConflict
	}
	if task.DeliveryWorkItemID == nil {
		return models.DeliveryPlanStep{}, time.Time{}, ErrStepLeaseConflict
	}
	var plan models.DeliveryPlan
	if err := tx.Select("id", "work_item_id", "status", "approved_gate_id").First(&plan, "id = ?", step.PlanID).Error; err != nil {
		return models.DeliveryPlanStep{}, time.Time{}, err
	}
	if plan.WorkItemID != *task.DeliveryWorkItemID || plan.Status != "approved" || plan.ApprovedGateID == nil || *plan.ApprovedGateID == uuid.Nil {
		return models.DeliveryPlanStep{}, time.Time{}, ErrStepLeaseConflict
	}
	return step, now, nil
}

func leaseValidationTime(clockOverride time.Time) time.Time {
	if !clockOverride.IsZero() {
		return clockOverride.UTC()
	}
	return time.Now().UTC()
}

func updatePlanStep(tx *gorm.DB, stepID uuid.UUID, updates map[string]any) error {
	result := tx.Model(&models.DeliveryPlanStep{}).Where("id = ?", stepID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func createStepEvent(tx *gorm.DB, step models.DeliveryPlanStep, eventType, fromStatus, toStatus, summary string, task leasedTask, fence int64, now time.Time) error {
	id, err := uuid.NewV4()
	if err != nil {
		return fmt.Errorf("generate delivery plan step event identity: %w", err)
	}
	if len(summary) > 160 {
		return invalidStepInput("step event summary exceeds 160 characters")
	}
	event := models.DeliveryPlanStepEvent{
		ID: id, PlanID: step.PlanID, StepID: step.ID, EventType: eventType,
		FromStatus: fromStatus, ToStatus: toStatus, AutomationTaskID: task.ID,
		RunID: task.RunID, WorkerID: task.WorkerID, AgentKey: task.AgentKey, MachineID: task.MachineID,
		LeaseFence: fence, Summary: summary, OccurredAt: now, CreatedAt: now,
	}
	return tx.Create(&event).Error
}

func resultFor(step models.DeliveryPlanStep) ClaimResult {
	var expiresAt *time.Time
	if step.LeaseExpiresAt != nil {
		value := step.LeaseExpiresAt.UTC()
		expiresAt = &value
	}
	return ClaimResult{Available: true, Step: step, Fence: step.LeaseFence, LeaseExpiresAt: expiresAt}
}

func validateStepLeaseTTL(ttl time.Duration) (time.Duration, error) {
	if ttl < MinStepLeaseTTL || ttl > MaxStepLeaseTTL {
		return 0, invalidStepInput(fmt.Sprintf("lease_ttl must be between %s and %s", MinStepLeaseTTL, MaxStepLeaseTTL))
	}
	return ttl, nil
}

func canonicalUUID(raw, field string) (string, error) {
	parsed, err := uuid.FromString(strings.TrimSpace(raw))
	if err != nil || parsed == uuid.Nil {
		return "", invalidStepInput(field + " must be a non-zero UUID")
	}
	return parsed.String(), nil
}

func optionalCanonicalUUID(raw, field string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	return canonicalUUID(raw, field)
}

func validStepStatus(status string) bool {
	switch status {
	case models.DeliveryPlanStepPlanned, models.DeliveryPlanStepReady, models.DeliveryPlanStepRunning,
		models.DeliveryPlanStepBlocked, models.DeliveryPlanStepCompleted, models.DeliveryPlanStepFailed, models.DeliveryPlanStepSkipped:
		return true
	default:
		return false
	}
}

func summaryForStatus(status string) string {
	switch status {
	case models.DeliveryPlanStepReady:
		return "Step made ready"
	case models.DeliveryPlanStepRunning:
		return "Step started"
	case models.DeliveryPlanStepBlocked:
		return "Step blocked"
	case models.DeliveryPlanStepCompleted:
		return "Step completed"
	case models.DeliveryPlanStepFailed:
		return "Step failed"
	case models.DeliveryPlanStepSkipped:
		return "Step skipped"
	default:
		return "Step status changed"
	}
}

func normalizeNow(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

func invalidStepInput(message string) error {
	return fmt.Errorf("%w: %s", ErrStepInputInvalid, message)
}

func uuidPointer(value uuid.UUID) *uuid.UUID {
	return &value
}
