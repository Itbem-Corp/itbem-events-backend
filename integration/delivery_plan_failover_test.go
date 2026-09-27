//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"events-stocks/configuration"
	automation "events-stocks/controllers/automation"
	"events-stocks/models"
	"events-stocks/services/deliveryplansteps"
	"events-stocks/services/deliveryworkflow"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestDeliveryPlanAssignmentFailsOverOnlyAfterExpiredLeases exercises the
// actual task-claim transaction and plan-step fencing path. A stale queue
// message still names machine A, but two compatible replacement machines race
// to claim the same child. Exactly one can atomically retarget the existing
// assignment and acquire the new task/step fences; machine A is then rejected.
func TestDeliveryPlanAssignmentFailsOverOnlyAfterExpiredLeases(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")
	t.Setenv("AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY", strings.Repeat("f", 48))
	t.Setenv("AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY_PREVIOUS", "")

	now := time.Now().UTC()
	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	subject := "integration-plan-failover-" + suffix
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Failover fixture " + suffix, Code: "FAILOVER_" + suffix, Level: 10, IsActive: true}
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Failover fixture " + suffix, Code: "failover-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Plan failover " + suffix, Slug: "plan-failover-" + suffix, Summary: "Disposable lease recovery fixture", Status: "active", CreatedBy: subject}
	item := models.DeliveryWorkItem{
		ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject,
		Title: "Recover a lost local machine", Description: "Fail over only after durable leases expire",
		ExpectedOutcome: "One compatible replacement claims the same logical step", State: deliveryworkflow.StateImplementation,
		IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`, AcceptanceJSON: `["the old fence is rejected"]`,
		ClientContextJSON: `{}`, PlanJSON: `{}`,
	}
	snapshot := models.DeliveryContextSnapshot{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, SourceID: uuid.Must(uuid.NewV4()),
		Kind: "repository", Name: "Disposable failover fixture", Reference: "workspace://failoverfixture",
		MetadataJSON: `{"repository_role":"primary"}`, CapturedAt: now, CreatedAt: now,
	}
	gate := models.DeliveryGate{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Kind: deliveryworkflow.GatePlan,
		Decision: deliveryworkflow.DecisionApproved, DecidedBy: "integration-reviewer",
		EvidenceChecklist: `[]`, DecidedAt: now, CreatedAt: now,
	}
	plan := models.DeliveryPlan{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Version: 1, Status: "approved",
		Summary: "One recoverable step", StructuredJSON: `{}`, ContextDigest: "integration-plan-failover-v1",
		ProposedBy: "integration-agent", ApprovedGateID: &gate.ID, CreatedAt: now,
	}
	step := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "recoverable", IdempotencyKey: "failover-" + suffix,
		Role:         models.DeliveryPlanStepRoleImplementation,
		DisplayOrder: 1, Title: "Recoverable step", Objective: "Execute once on a compatible local workspace",
		AcceptanceCriteriaJSON: `["the old fence is rejected"]`, Status: models.DeliveryPlanStepReady,
		AgentKey: "failoverworker", CreatedBy: subject, CreatedAt: now, UpdatedAt: now,
	}
	integration := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "integrate", IdempotencyKey: "failover-integration-" + suffix,
		Role:         models.DeliveryPlanStepRoleIntegration,
		DisplayOrder: 2, Title: "Final integration", Objective: "Verify the frozen implementation result",
		AcceptanceCriteriaJSON: `["the old fence is rejected"]`, Status: models.DeliveryPlanStepPlanned,
		AgentKey: "failoverworker", CreatedBy: subject, CreatedAt: now, UpdatedAt: now,
	}
	integrationDependency := models.DeliveryPlanStepDependency{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: integration.ID, DependsOnStepID: step.ID, CreatedAt: now,
	}
	parent := models.AutomationTask{
		ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), RequestedBy: subject,
		DeliveryWorkItemID: &item.ID, CorrelationID: item.ID.String(), Operation: "delivery.implementation",
		InputRef: "private://failover/parent", Status: "queued", CreatedAt: now, UpdatedAt: now,
	}
	workerA := makePlanFanoutWorker(t, "failoverworker", "failover-A", item.ID, subject, now)
	workerA.heartbeat.WorkspaceReadiness = `[{"id":"failoverfixture","ready":true,"sandbox_ready":true,"isolation_mode":"docker_container"}]`
	workerA.task.Status = "queued"
	workerA.task.WorkerID, workerA.task.AgentKey, workerA.task.MachineID, workerA.task.RunID = "", "", "", ""
	workerA.task.LeaseExpiresAt = nil
	workerA.task.AttemptCount = 0
	workerB := makePlanFanoutWorker(t, workerA.profile.AgentKey, "failover-B", item.ID, subject, now)
	workerC := makePlanFanoutWorker(t, workerA.profile.AgentKey, "failover-C", item.ID, subject, now)
	workerB.profile, workerC.profile = workerA.profile, workerA.profile
	var execution models.DeliveryPlanExecution
	childID := uuid.Nil
	t.Cleanup(func() {
		instanceIDs := []uuid.UUID{workerA.instance.ID, workerB.instance.ID, workerC.instance.ID}
		workerIDs := []string{workerA.heartbeat.WorkerID, workerB.heartbeat.WorkerID, workerC.heartbeat.WorkerID}
		_ = db.Unscoped().Where("instance_id IN ?", instanceIDs).Delete(&models.AutomationAgentCallbackNonce{}).Error
		_ = db.Unscoped().Where("id IN ?", instanceIDs).Delete(&models.AutomationAgentInstance{}).Error
		_ = db.Unscoped().Where("worker_id IN ?", workerIDs).Delete(&models.AutomationAgentHeartbeat{}).Error
		if workerA.profile.AgentKey != "" {
			_ = db.Unscoped().Where("agent_key = ?", workerA.profile.AgentKey).Delete(&models.AutomationAgentProfile{}).Error
		}
		// Lifecycle/assignment ledgers are append-only by design; this disposable
		// integration database is torn down by TestMain after the full test run.
	})

	for _, value := range []any{&clientType, &client, &project, &item, &snapshot, &gate, &plan, &step, &integration, &integrationDependency, &parent, &workerA.profile, &workerA.heartbeat, &workerA.instance} {
		require.NoError(t, db.Create(value).Error)
	}
	require.NoError(t, db.Model(&models.DeliveryPlan{}).Where("id = ?", plan.ID).Update("approved_gate_id", gate.ID).Error)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		created, _, err := deliveryplansteps.CreateExecutionInTransaction(tx, parent.ID, plan.ID, 1, "failover-execution-"+suffix, now)
		if err != nil {
			return err
		}
		execution = created
		childID = deliveryplansteps.ChildAutomationTaskID(execution.ID, step.ID)
		workerA.task.ID = childID
		workerA.task.JobID = deliveryplansteps.ChildAutomationJobID(execution.ID, step.ID)
		workerA.task.CorrelationID = parent.CorrelationID
		workerA.task.Operation = parent.Operation
		workerA.task.InputRef = parent.InputRef
		if err := tx.Create(&workerA.task).Error; err != nil {
			return err
		}
		assignments, err := deliveryplansteps.ReserveReadyAssignmentsInTransaction(tx, execution.ID, 1, map[uuid.UUID]uuid.UUID{step.ID: childID}, now)
		if err != nil {
			return err
		}
		if len(assignments) != 1 || assignments[0].TargetMachineID != workerA.heartbeat.MachineID || assignments[0].TargetAgentKey != workerA.profile.AgentKey {
			return deliveryplansteps.ErrPlanExecutionConflict
		}
		return tx.Model(&models.DeliveryPlanStepAssignment{}).Where("id = ?", assignments[0].ID).Update("status", models.DeliveryPlanStepAssignmentQueued).Error
	}))

	for _, worker := range []*planFanoutWorker{&workerB, &workerC} {
		worker.profile = workerA.profile // one persisted specialist profile, multiple compatible machines
		worker.heartbeat.WorkspaceReadiness = `[{"id":"failoverfixture","ready":true,"sandbox_ready":true,"isolation_mode":"docker_container"}]`
		worker.task = workerA.task
		worker.task.WorkerID, worker.task.AgentKey, worker.task.MachineID, worker.task.RunID = "", "", "", ""
		worker.task.LeaseExpiresAt = nil
		worker.task.Status = "queued"
		worker.task.AttemptCount = 0
		require.NoError(t, db.Create(&worker.heartbeat).Error)
		require.NoError(t, db.Create(&worker.instance).Error)
	}
	// The new workers must not affect initial scheduling; the assignment has
	// already durably selected machine A before their heartbeats were inserted.

	router := echo.New()
	callbacks := router.Group("/api/internal/automation")
	callbacks.Use(automation.AgentCallbackAuthentication)
	callbacks.PUT("/tasks/:id", automation.Complete)
	callbacks.POST("/steps/claim", automation.ClaimDeliveryPlanStep)
	callbacks.PUT("/steps/:id", automation.TransitionDeliveryPlanStep)
	server := httptest.NewServer(router)
	defer server.Close()
	clientHTTP := server.Client()

	claimTask := func(worker *planFanoutWorker) planFanoutCallbackResult {
		body, err := json.Marshal(map[string]any{
			"status": "running", "run_id": worker.runID.String(),
			"worker_id": worker.heartbeat.WorkerID, "agent_key": worker.profile.AgentKey,
			"machine_id": worker.heartbeat.MachineID,
		})
		require.NoError(t, err)
		return callPlanFanoutEndpoint(clientHTTP, server.URL, http.MethodPut, "/api/internal/automation/tasks/"+childID.String(), body, worker)
	}

	initialClaim := claimTask(&workerA)
	require.NoError(t, initialClaim.Err)
	require.Equal(t, http.StatusNoContent, initialClaim.Status, initialClaim.Body)
	firstStepClaim, err := callPlanFanoutClaim(clientHTTP, server.URL, plan, step.ID, &workerA)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, firstStepClaim.Status, firstStepClaim.Body)
	require.True(t, firstStepClaim.Claim.Data.Available)
	oldFence := mustPlanFanoutFence(t, firstStepClaim.Claim.Data.FencingToken)
	require.EqualValues(t, 1, oldFence)

	// A live task or step lease is an absolute no-reassignment boundary, even
	// when another healthy machine is ready. The target heartbeat must also be
	// older than the scheduler's expiry threshold before recovery is possible.
	require.NoError(t, db.Model(&models.AutomationAgentHeartbeat{}).Where("worker_id = ?", workerA.heartbeat.WorkerID).
		Update("last_seen_at", time.Now().UTC()).Error)
	busyReplacement := claimTask(&workerB)
	require.NoError(t, busyReplacement.Err)
	require.Equal(t, http.StatusConflict, busyReplacement.Status, "a live task lease must block target changes: %s", busyReplacement.Body)
	require.NoError(t, db.Model(&models.AutomationTask{}).Where("id = ?", childID).
		Update("lease_expires_at", time.Now().UTC().Add(-time.Second)).Error)
	busyStepLease := claimTask(&workerB)
	require.NoError(t, busyStepLease.Err)
	require.Equal(t, http.StatusConflict, busyStepLease.Status, "an expired task lease must not override a live plan-step lease: %s", busyStepLease.Body)
	var stillAssigned models.DeliveryPlanStepAssignment
	require.NoError(t, db.Where("execution_id = ? AND delivery_plan_step_id = ?", execution.ID, step.ID).Take(&stillAssigned).Error)
	require.Equal(t, workerA.heartbeat.MachineID, stillAssigned.TargetMachineID)

	expiredAt := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, db.Model(&models.AutomationAgentHeartbeat{}).Where("worker_id = ?", workerA.heartbeat.WorkerID).
		Update("last_seen_at", expiredAt.Add(-3*time.Minute)).Error)
	require.NoError(t, db.Model(&models.AutomationTask{}).Where("id = ?", childID).Update("lease_expires_at", expiredAt).Error)
	require.NoError(t, db.Model(&models.DeliveryPlanStep{}).Where("id = ?", step.ID).Update("lease_expires_at", expiredAt).Error)

	// Simulate two independent scheduler deliveries racing the same expired
	// assignment. Both candidates are compatible and have capacity, but the
	// child task/assignment locks permit only one durable owner.
	start := make(chan struct{})
	type claimResult struct {
		worker   *planFanoutWorker
		response planFanoutCallbackResult
	}
	results := make(chan claimResult, 2)
	var group sync.WaitGroup
	for _, candidate := range []*planFanoutWorker{&workerB, &workerC} {
		group.Add(1)
		go func(worker *planFanoutWorker) {
			defer group.Done()
			<-start
			results <- claimResult{worker: worker, response: claimTask(worker)}
		}(candidate)
	}
	close(start)
	group.Wait()
	close(results)
	var winner *planFanoutWorker
	accepted, rejected := 0, 0
	for result := range results {
		require.NoError(t, result.response.Err)
		t.Logf("replacement machine=%s claim status=%d body=%s", result.worker.heartbeat.MachineID, result.response.Status, result.response.Body)
		switch result.response.Status {
		case http.StatusNoContent:
			accepted++
			winner = result.worker
		case http.StatusConflict:
			rejected++
		default:
			t.Fatalf("concurrent replacement claim status=%d body=%s", result.response.Status, result.response.Body)
		}
	}
	require.Equal(t, 1, accepted, "one replacement may own the task lease")
	require.Equal(t, 1, rejected, "the losing scheduler must retain its message")
	require.NotNil(t, winner)

	var reassigned models.DeliveryPlanStepAssignment
	require.NoError(t, db.Where("execution_id = ? AND delivery_plan_step_id = ?", execution.ID, step.ID).Take(&reassigned).Error)
	require.Equal(t, winner.heartbeat.MachineID, reassigned.TargetMachineID)
	require.Equal(t, models.DeliveryPlanStepAssignmentQueued, reassigned.Status)
	var assignmentCount int64
	require.NoError(t, db.Model(&models.DeliveryPlanStepAssignment{}).Where("execution_id = ? AND delivery_plan_step_id = ?", execution.ID, step.ID).Count(&assignmentCount).Error)
	require.EqualValues(t, 1, assignmentCount, "failover must preserve one logical assignment")

	var claimedTask models.AutomationTask
	require.NoError(t, db.First(&claimedTask, "id = ?", childID).Error)
	require.Equal(t, winner.runID.String(), claimedTask.RunID)
	require.Equal(t, winner.heartbeat.WorkerID, claimedTask.WorkerID)
	require.Equal(t, winner.heartbeat.MachineID, claimedTask.MachineID)
	require.Equal(t, 2, claimedTask.AttemptCount, "the replacement claim must advance the attempt")
	require.True(t, claimedTask.LeaseExpiresAt.After(time.Now().UTC()))

	winner.task = claimedTask
	newStepClaim, err := callPlanFanoutClaim(clientHTTP, server.URL, plan, step.ID, winner)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, newStepClaim.Status, newStepClaim.Body)
	newFence := mustPlanFanoutFence(t, newStepClaim.Claim.Data.FencingToken)
	require.Equal(t, oldFence+1, newFence, "replacement must advance the plan-step fence")

	oldWorkerTransition := callPlanFanoutTransition(clientHTTP, server.URL, step.ID, &workerA, oldFence, models.DeliveryPlanStepCompleted)
	require.Equal(t, http.StatusConflict, oldWorkerTransition.Status, "worker A's prior run/fence must not publish after failover: %s", oldWorkerTransition.Body)
	oldWorkerClaim := claimTask(&workerA)
	require.NoError(t, oldWorkerClaim.Err)
	require.Equal(t, http.StatusConflict, oldWorkerClaim.Status, "old machine must not reclaim the live replacement lease")

}
