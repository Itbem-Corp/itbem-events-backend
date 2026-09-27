//go:build integration

package integration_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"events-stocks/configuration"
	automation "events-stocks/controllers/automation"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/models"
	"events-stocks/services/deliveryplansteps"
	"events-stocks/services/deliveryworkflow"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type planFanoutClaimRequest struct {
	TaskID       string `json:"task_id"`
	PlanID       string `json:"plan_id"`
	StepID       string `json:"step_id,omitempty"`
	RunID        string `json:"run_id"`
	WorkerID     string `json:"worker_id"`
	AgentKey     string `json:"agent_key"`
	MachineID    string `json:"machine_id"`
	LeaseSeconds int    `json:"lease_seconds"`
}

type planFanoutLeaseTuple struct {
	TaskID       string `json:"task_id"`
	RunID        string `json:"run_id"`
	WorkerID     string `json:"worker_id"`
	AgentKey     string `json:"agent_key"`
	MachineID    string `json:"machine_id"`
	FencingToken string `json:"fencing_token"`
	Status       string `json:"status,omitempty"`
}

type planFanoutActivityRequest struct {
	EventID      string          `json:"event_id"`
	TaskID       string          `json:"task_id"`
	RunID        string          `json:"run_id"`
	WorkerID     string          `json:"worker_id"`
	AgentKey     string          `json:"agent_key"`
	MachineID    string          `json:"machine_id"`
	FencingToken string          `json:"fencing_token"`
	Sequence     int64           `json:"sequence"`
	Action       string          `json:"action"`
	Phase        string          `json:"phase"`
	ToolName     string          `json:"tool_name,omitempty"`
	Details      json.RawMessage `json:"details,omitempty"`
}

type planFanoutAcceptanceCheck struct {
	CriterionSHA256 string `json:"criterion_sha256"`
	Passed          bool   `json:"passed"`
}

type planFanoutEvidenceDetails struct {
	AcceptanceChecks []planFanoutAcceptanceCheck `json:"acceptance_checks"`
	ReviewDiffSHA256 string                      `json:"review_diff_sha256"`
}

type planFanoutClaimEnvelope struct {
	Data struct {
		Available bool `json:"available"`
		Step      *struct {
			ID string `json:"id"`
		} `json:"step"`
		FencingToken string `json:"fencing_token"`
	} `json:"data"`
}

type planFanoutWorker struct {
	profile    models.AutomationAgentProfile
	heartbeat  models.AutomationAgentHeartbeat
	instance   models.AutomationAgentInstance
	privateKey ed25519.PrivateKey
	task       models.AutomationTask
	runID      uuid.UUID
}

// TestDeliveryPlanParallelRoundTrip exercises the production database-backed
// plan assignment and callback boundaries against the disposable PostgreSQL
// database created by integration TestMain. It deliberately uses two distinct
// agent profiles/machines for independent roots and a third worker for their
// dependent step, so a passing result cannot be explained by one worker taking
// multiple steps sequentially.
func TestDeliveryPlanParallelRoundTrip(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "")

	now := time.Now().UTC()
	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	subject := "integration-plan-fanout-" + suffix
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Fan-out fixture " + suffix, Code: "FANOUT_" + suffix, Level: 10, IsActive: true}
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Fan-out fixture " + suffix, Code: "fanout-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Plan fan-out " + suffix, Slug: "plan-fanout-" + suffix, Summary: "Disposable database lease test", Status: "active", CreatedBy: subject}
	item := models.DeliveryWorkItem{
		ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject,
		Title: "Parallel roots with fan-in", Description: "Integration fixture for independent steps and their dependent step",
		ExpectedOutcome: "Each worker claims only its reserved ready step", State: deliveryworkflow.StateImplementation,
		IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`, AcceptanceJSON: `["alpha complete","alpha output reviewed","beta complete","both roots complete"]`,
		ClientContextJSON: `{}`, PlanJSON: `{}`,
	}
	snapshot := models.DeliveryContextSnapshot{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, SourceID: uuid.Must(uuid.NewV4()),
		Kind: "repository", Name: "Disposable fan-out fixture", Reference: "workspace://fanoutfixture",
		MetadataJSON: `{"repository_role":"primary"}`, CapturedAt: now, CreatedAt: now,
	}
	gate := models.DeliveryGate{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Kind: deliveryworkflow.GatePlan,
		Decision: deliveryworkflow.DecisionApproved, DecidedBy: "integration-reviewer",
		EvidenceChecklist: `[]`, DecidedAt: now, CreatedAt: now,
	}
	plan := models.DeliveryPlan{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Version: 1, Status: "approved",
		Summary: "Two independent roots, one dependent implementation step and one final integrator", StructuredJSON: `{}`, ContextDigest: "integration-fanout-v1",
		ProposedBy: "integration-agent", ApprovedGateID: &gate.ID, CreatedAt: now,
	}
	rootAlpha := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "root-alpha", IdempotencyKey: "fanout-alpha-" + suffix,
		Role:         models.DeliveryPlanStepRoleImplementation,
		DisplayOrder: 1, Title: "Independent root alpha", Objective: "Run on the alpha worker",
		AcceptanceCriteriaJSON: `["alpha complete","alpha output reviewed"]`, Status: models.DeliveryPlanStepPlanned, AgentKey: "fanoutalpha",
		CreatedBy: subject, CreatedAt: now, UpdatedAt: now,
	}
	rootBeta := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "root-beta", IdempotencyKey: "fanout-beta-" + suffix,
		Role:         models.DeliveryPlanStepRoleImplementation,
		DisplayOrder: 2, Title: "Independent root beta", Objective: "Run on the beta worker",
		AcceptanceCriteriaJSON: `["beta complete"]`, Status: models.DeliveryPlanStepPlanned, AgentKey: "fanoutbeta",
		CreatedBy: subject, CreatedAt: now, UpdatedAt: now,
	}
	dependent := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "dependent", IdempotencyKey: "fanout-dependent-" + suffix,
		Role:         models.DeliveryPlanStepRoleImplementation,
		DisplayOrder: 3, Title: "Fan-in dependent", Objective: "Wait until both independent roots complete",
		AcceptanceCriteriaJSON: `["both roots complete"]`, Status: models.DeliveryPlanStepPlanned, AgentKey: "fanoutgamma",
		CreatedBy: subject, CreatedAt: now, UpdatedAt: now,
	}
	integration := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "integrate", IdempotencyKey: "fanout-integrate-" + suffix,
		Role:         models.DeliveryPlanStepRoleIntegration,
		DisplayOrder: 4, Title: "Final integration", Objective: "Merge the terminal implementation branch and recheck all criteria",
		AcceptanceCriteriaJSON: `["alpha complete","alpha output reviewed","beta complete","both roots complete"]`, Status: models.DeliveryPlanStepPlanned, AgentKey: "fanoutdelta",
		CreatedBy: subject, CreatedAt: now, UpdatedAt: now,
	}
	dependencies := []models.DeliveryPlanStepDependency{
		{ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: dependent.ID, DependsOnStepID: rootAlpha.ID, CreatedAt: now},
		{ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: dependent.ID, DependsOnStepID: rootBeta.ID, CreatedAt: now},
		{ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: integration.ID, DependsOnStepID: dependent.ID, CreatedAt: now},
	}
	parentTask := models.AutomationTask{
		ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), RequestedBy: subject,
		DeliveryWorkItemID: &item.ID, CorrelationID: item.ID.String(), Operation: "delivery.implementation",
		InputRef: "private://fanout/parent", Status: "queued", CreatedAt: now, UpdatedAt: now,
	}
	alpha := makePlanFanoutWorker(t, "fanoutalpha", "fanout-alpha", item.ID, subject, now)
	beta := makePlanFanoutWorker(t, "fanoutbeta", "fanout-beta", item.ID, subject, now)
	gamma := makePlanFanoutWorker(t, "fanoutgamma", "fanout-gamma", item.ID, subject, now)
	alpha.task.MachineID = alpha.heartbeat.MachineID
	beta.task.MachineID = beta.heartbeat.MachineID
	gamma.task.MachineID = gamma.heartbeat.MachineID
	alpha.task.WorkerID, beta.task.WorkerID, gamma.task.WorkerID = alpha.heartbeat.WorkerID, beta.heartbeat.WorkerID, gamma.heartbeat.WorkerID
	alpha.task.AgentKey, beta.task.AgentKey, gamma.task.AgentKey = alpha.profile.AgentKey, beta.profile.AgentKey, gamma.profile.AgentKey
	alpha.task.RunID, beta.task.RunID, gamma.task.RunID = alpha.runID.String(), beta.runID.String(), gamma.runID.String()
	alpha.task.ID, beta.task.ID, gamma.task.ID = uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	alpha.task.JobID, beta.task.JobID, gamma.task.JobID = uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	alpha.task.InputRef, beta.task.InputRef, gamma.task.InputRef = "private://fanout/alpha", "private://fanout/beta", "private://fanout/gamma"
	alphaTwin := clonePlanFanoutInstance(t, &alpha, now)
	// Tear down only rows created for this test, in FK-safe reverse order. The
	// cleanup is registered before inserts so a partial fixture failure cannot
	// leave synthetic heartbeats or assignments visible to later tests.
	t.Cleanup(func() {
		_ = db.Unscoped().Where("instance_id IN ?", []uuid.UUID{alpha.instance.ID, alphaTwin.instance.ID, beta.instance.ID, gamma.instance.ID}).Delete(&models.AutomationAgentCallbackNonce{}).Error
		_ = db.Unscoped().Where("id IN ?", []uuid.UUID{alpha.instance.ID, alphaTwin.instance.ID, beta.instance.ID, gamma.instance.ID}).Delete(&models.AutomationAgentInstance{}).Error
		_ = db.Unscoped().Where("step_id IN ?", []uuid.UUID{rootAlpha.ID, rootBeta.ID, dependent.ID, integration.ID}).Delete(&models.DeliveryPlanStepActivityEvent{}).Error
		_ = db.Unscoped().Where("execution_id IN (SELECT id FROM delivery_plan_executions WHERE automation_task_id = ?)", parentTask.ID).Delete(&models.DeliveryPlanStepAssignment{}).Error
		_ = db.Unscoped().Where("child_automation_task_id IN ?", []uuid.UUID{alpha.task.ID, beta.task.ID, gamma.task.ID}).Delete(&models.DeliveryPlanStepAssignment{}).Error
		_ = db.Unscoped().Where("step_id IN ?", []uuid.UUID{rootAlpha.ID, rootBeta.ID, dependent.ID, integration.ID}).Delete(&models.DeliveryPlanStepDependency{}).Error
		_ = db.Unscoped().Where("plan_id = ?", plan.ID).Delete(&models.DeliveryPlanStepEvent{}).Error
		_ = db.Unscoped().Where("plan_id = ?", plan.ID).Delete(&models.DeliveryPlanExecution{}).Error
		_ = db.Unscoped().Where("plan_id = ?", plan.ID).Delete(&models.DeliveryPlanStep{}).Error
		_ = db.Unscoped().Where("id IN ?", []uuid.UUID{alpha.heartbeat.ID, beta.heartbeat.ID, gamma.heartbeat.ID}).Delete(&models.AutomationAgentHeartbeat{}).Error
		_ = db.Unscoped().Where("agent_key IN ?", []string{alpha.profile.AgentKey, beta.profile.AgentKey, gamma.profile.AgentKey}).Delete(&models.AutomationAgentProfile{}).Error
		_ = db.Unscoped().Where("id IN ?", []uuid.UUID{parentTask.ID, alpha.task.ID, beta.task.ID, gamma.task.ID}).Delete(&models.AutomationTask{}).Error
		_ = db.Unscoped().Where("id = ?", plan.ID).Delete(&models.DeliveryPlan{}).Error
		_ = db.Unscoped().Where("id = ?", gate.ID).Delete(&models.DeliveryGate{}).Error
		_ = db.Unscoped().Where("id = ?", snapshot.ID).Delete(&models.DeliveryContextSnapshot{}).Error
		_ = db.Unscoped().Where("id = ?", item.ID).Delete(&models.DeliveryWorkItem{}).Error
		_ = db.Unscoped().Where("id = ?", project.ID).Delete(&models.DeliveryProject{}).Error
		_ = db.Unscoped().Where("id = ?", client.ID).Delete(&models.Client{}).Error
		_ = db.Unscoped().Where("id = ?", clientType.ID).Delete(&models.ClientType{}).Error
	})

	for _, value := range []any{&clientType, &client, &project, &item, &snapshot, &gate, &plan, &rootAlpha, &rootBeta, &dependent, &integration} {
		require.NoError(t, db.Create(value).Error)
	}
	require.NoError(t, db.Model(&models.DeliveryPlan{}).Where("id = ?", plan.ID).Update("approved_gate_id", gate.ID).Error)
	for _, edge := range dependencies {
		require.NoError(t, db.Create(&edge).Error)
	}
	for _, worker := range []*planFanoutWorker{&alpha, &beta, &gamma} {
		require.NoError(t, db.Create(&worker.profile).Error)
		require.NoError(t, db.Create(&worker.heartbeat).Error)
		require.NoError(t, db.Create(&worker.instance).Error)
	}
	require.NoError(t, db.Create(&alphaTwin.instance).Error)
	for _, task := range []*models.AutomationTask{&parentTask, &alpha.task, &beta.task, &gamma.task} {
		require.NoError(t, db.Create(task).Error)
	}
	var execution models.DeliveryPlanExecution
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		created, _, err := deliveryplansteps.CreateExecutionInTransaction(tx, parentTask.ID, plan.ID, 2, "parallel-roundtrip-"+suffix, now)
		if err != nil {
			return err
		}
		execution = created
		assignments, err := deliveryplansteps.ReserveReadyAssignmentsInTransaction(tx, created.ID, 2,
			map[uuid.UUID]uuid.UUID{rootAlpha.ID: alpha.task.ID, rootBeta.ID: beta.task.ID}, now)
		if err != nil {
			return err
		}
		require.Len(t, assignments, 2)
		return nil
	}))
	var assignments []models.DeliveryPlanStepAssignment
	require.NoError(t, db.Where("execution_id = ?", execution.ID).Order("delivery_plan_step_id ASC").Find(&assignments).Error)
	require.Len(t, assignments, 2)
	require.Equal(t, 2, execution.MaxConcurrency)
	require.NotEqual(t, assignments[0].TargetMachineID, assignments[1].TargetMachineID, "independent steps must be assigned to distinct local machines")
	require.NotEqual(t, assignments[0].TargetAgentKey, assignments[1].TargetAgentKey, "independent steps must be assigned to distinct eligible agent profiles")
	assignmentByStep := map[uuid.UUID]models.DeliveryPlanStepAssignment{}
	for _, assignment := range assignments {
		assignmentByStep[assignment.DeliveryPlanStepID] = assignment
	}
	var persistedParent models.AutomationTask
	require.NoError(t, db.Select("id", "operation", "delivery_work_item_id", "status").First(&persistedParent, "id = ?", parentTask.ID).Error)
	require.Equal(t, "queued", persistedParent.Status)
	var persistedPlan models.DeliveryPlan
	require.NoError(t, db.Select("id", "work_item_id", "version", "status", "approved_gate_id").First(&persistedPlan, "id = ?", plan.ID).Error)
	var persistedExecution models.DeliveryPlanExecution
	require.NoError(t, db.Select("id", "status").First(&persistedExecution, "id = ?", execution.ID).Error)
	require.Equal(t, models.DeliveryPlanExecutionPending, persistedExecution.Status, "the concurrent claims must exercise the first pending-to-running transition")
	var persistedSteps []models.DeliveryPlanStep
	require.NoError(t, db.Where("plan_id = ?", plan.ID).Order("display_order ASC, id ASC").Find(&persistedSteps).Error)
	var persistedDependencies []models.DeliveryPlanStepDependency
	require.NoError(t, db.Where("plan_id = ?", plan.ID).Order("step_id ASC, depends_on_step_id ASC").Find(&persistedDependencies).Error)
	persistedHash, err := deliveryplansteps.ApprovedPlanContentHash(persistedPlan, persistedSteps, persistedDependencies)
	require.NoError(t, err)
	require.Equal(t, execution.PlanHash, persistedHash, "reserved assignments must freeze the exact approved plan graph")
	for _, worker := range []*planFanoutWorker{&alpha, &beta} {
		var persistedTask models.AutomationTask
		require.NoError(t, db.Select("id", "operation", "delivery_work_item_id", "worker_id", "agent_key", "machine_id", "status", "run_id", "lease_expires_at").First(&persistedTask, "id = ?", worker.task.ID).Error)
		require.Equal(t, "running", persistedTask.Status)
		require.Equal(t, worker.runID.String(), persistedTask.RunID)
		require.True(t, persistedTask.LeaseExpiresAt.After(time.Now().UTC()))
	}
	require.Equal(t, alpha.heartbeat.MachineID, assignmentByStep[rootAlpha.ID].TargetMachineID)
	require.Equal(t, alpha.profile.AgentKey, assignmentByStep[rootAlpha.ID].TargetAgentKey)
	require.Equal(t, beta.heartbeat.MachineID, assignmentByStep[rootBeta.ID].TargetMachineID)
	require.Equal(t, beta.profile.AgentKey, assignmentByStep[rootBeta.ID].TargetAgentKey)
	var activeAssignmentCount int64
	require.NoError(t, db.Model(&models.DeliveryPlanStepAssignment{}).Where("execution_id = ? AND status IN ?", execution.ID, []string{
		models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued,
		models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning,
	}).Count(&activeAssignmentCount).Error)
	require.EqualValues(t, execution.MaxConcurrency, activeAssignmentCount)
	callbackRouter := echo.New()
	workerCallbacks := callbackRouter.Group("/api/internal/automation")
	workerCallbacks.Use(automation.AgentCallbackAuthentication)
	workerCallbacks.POST("/steps/claim", automation.ClaimDeliveryPlanStep)
	workerCallbacks.PUT("/steps/:id/lease", automation.RenewDeliveryPlanStepLease)
	workerCallbacks.PUT("/steps/:id", automation.TransitionDeliveryPlanStep)
	workerCallbacks.POST("/steps/:id/activity", automation.RecordDeliveryPlanStepActivity)
	callbackServer := httptest.NewServer(callbackRouter)
	defer callbackServer.Close()
	clientHTTP := callbackServer.Client()

	// Start the two independently assigned claims at one barrier. Each call is
	// authenticated and goes through the production Echo callback handler and
	// its real PostgreSQL transaction.
	type claimOutcome struct {
		stepID   uuid.UUID
		worker   *planFanoutWorker
		response planFanoutClaimResult
		err      error
	}
	start := make(chan struct{})
	outcomes := make(chan claimOutcome, 2)
	var workers sync.WaitGroup
	for _, input := range []struct {
		stepID uuid.UUID
		worker *planFanoutWorker
	}{{rootAlpha.ID, &alpha}, {rootBeta.ID, &beta}} {
		workers.Add(1)
		go func(stepID uuid.UUID, worker *planFanoutWorker) {
			defer workers.Done()
			<-start
			response, err := callPlanFanoutClaim(clientHTTP, callbackServer.URL, plan, stepID, worker)
			outcomes <- claimOutcome{stepID: stepID, worker: worker, response: response, err: err}
		}(input.stepID, input.worker)
	}
	close(start)
	workers.Wait()
	close(outcomes)
	fences := map[uuid.UUID]int64{}
	for outcome := range outcomes {
		require.NoError(t, outcome.err)
		require.Equalf(t, http.StatusOK, outcome.response.Status, "step=%s task=%s agent=%s machine=%s body=%s", outcome.stepID, outcome.worker.task.ID, outcome.worker.profile.AgentKey, outcome.worker.heartbeat.MachineID, outcome.response.Body)
		require.True(t, outcome.response.Claim.Data.Available, outcome.response.Body)
		require.NotNil(t, outcome.response.Claim.Data.Step, outcome.response.Body)
		claimedStepID, err := uuid.FromString(outcome.response.Claim.Data.Step.ID)
		require.NoError(t, err)
		require.Equal(t, outcome.stepID, claimedStepID, "worker must receive exactly its persisted assignment")
		fence, err := parsePlanFanoutFence(outcome.response.Claim.Data.FencingToken)
		require.NoError(t, err)
		fences[outcome.stepID] = fence
		require.Equal(t, outcome.worker.task.ID, assignmentByStep[outcome.stepID].ChildAutomationTaskID)
	}
	require.Len(t, fences, 2)
	firstFence := fences[rootAlpha.ID]
	secondFence := fences[rootBeta.ID]
	require.EqualValues(t, 1, firstFence)
	require.EqualValues(t, 1, secondFence)

	// An exact duplicate from the current owner is an idempotent replay, while
	// that same task cannot name another worker's step and claim it instead.
	duplicate, err := callPlanFanoutClaim(clientHTTP, callbackServer.URL, plan, rootAlpha.ID, &alpha)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, duplicate.Status, duplicate.Body)
	require.True(t, duplicate.Claim.Data.Available, duplicate.Body)
	require.Equal(t, firstFence, mustPlanFanoutFence(t, duplicate.Claim.Data.FencingToken))
	stolen, err := callPlanFanoutClaim(clientHTTP, callbackServer.URL, plan, rootBeta.ID, &alpha)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, stolen.Status, "an assigned child task must not claim a sibling step: %s", stolen.Body)
	var alphaClaimEvents int64
	require.NoError(t, db.Model(&models.DeliveryPlanStepEvent{}).Where("step_id = ? AND event_type = ?", rootAlpha.ID, models.DeliveryPlanStepEventClaimed).Count(&alphaClaimEvents).Error)
	require.EqualValues(t, 1, alphaClaimEvents, "a retried active claim must not create a duplicate claim event")

	// Exercise both additional worker mutation routes through the same signed
	// per-instance identity boundary while the original claim is still live.
	activityID := uuid.Must(uuid.NewV4())
	activityBody, err := json.Marshal(planFanoutActivityRequest{
		EventID: activityID.String(), TaskID: alpha.task.ID.String(), RunID: alpha.runID.String(),
		WorkerID: alpha.heartbeat.WorkerID, AgentKey: alpha.profile.AgentKey, MachineID: alpha.heartbeat.MachineID,
		FencingToken: fmt.Sprintf("%d", firstFence), Sequence: 1, Action: models.DeliveryPlanStepActivityTool,
		Phase: models.DeliveryPlanStepActivityStarted, ToolName: "integration_test",
	})
	require.NoError(t, err)
	activityResponse := callPlanFanoutEndpoint(clientHTTP, callbackServer.URL, http.MethodPost,
		"/api/internal/automation/steps/"+rootAlpha.ID.String()+"/activity", activityBody, &alpha)
	require.NoError(t, activityResponse.Err)
	require.Equal(t, http.StatusOK, activityResponse.Status, activityResponse.Body)
	var persistedActivity models.DeliveryPlanStepActivityEvent
	require.NoError(t, db.First(&persistedActivity, "id = ?", activityID).Error)
	require.NotNil(t, persistedActivity.AgentInstanceID)
	require.Equal(t, alpha.instance.ID, *persistedActivity.AgentInstanceID)

	leaseBody, err := json.Marshal(planFanoutLeaseTuple{
		TaskID: alpha.task.ID.String(), RunID: alpha.runID.String(), WorkerID: alpha.heartbeat.WorkerID,
		AgentKey: alpha.profile.AgentKey, MachineID: alpha.heartbeat.MachineID,
		FencingToken: fmt.Sprintf("%d", firstFence),
	})
	require.NoError(t, err)
	leaseResponse := callPlanFanoutEndpoint(clientHTTP, callbackServer.URL, http.MethodPut,
		"/api/internal/automation/steps/"+rootAlpha.ID.String()+"/lease", leaseBody, &alpha)
	require.NoError(t, leaseResponse.Err)
	require.Equal(t, http.StatusOK, leaseResponse.Status, leaseResponse.Body)

	// The dependent worker is valid and online, but has no assignment yet. A
	// broad legacy claim must fail closed while this plan has an active fan-out
	// execution instead of letting that worker bypass the assignment queue.
	dependentBefore, err := callPlanFanoutClaim(clientHTTP, callbackServer.URL, plan, uuid.Nil, &gamma)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, dependentBefore.Status, dependentBefore.Body)
	require.Contains(t, strings.ToLower(dependentBefore.Body), "active execution already owns assignments", "unassigned worker must not bypass the active fan-out")

	// Simulate an expired *step* lease while its automation task lease remains
	// live, then prove the original fencing token is rejected and recovery
	// increments the step fence before granting the same assigned worker again.
	expiredAt := time.Now().UTC().Add(-time.Second)
	require.NoError(t, db.Model(&models.DeliveryPlanStep{}).Where("id = ?", rootAlpha.ID).Update("lease_expires_at", expiredAt).Error)
	staleCompletion := callPlanFanoutTransition(clientHTTP, callbackServer.URL, rootAlpha.ID, &alpha, firstFence, models.DeliveryPlanStepCompleted)
	require.Equal(t, http.StatusConflict, staleCompletion.Status, "expired lease callback must be fenced: %s", staleCompletion.Body)
	reclaimed, err := callPlanFanoutClaim(clientHTTP, callbackServer.URL, plan, rootAlpha.ID, &alpha)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, reclaimed.Status, reclaimed.Body)
	require.True(t, reclaimed.Claim.Data.Available, reclaimed.Body)
	newFence := mustPlanFanoutFence(t, reclaimed.Claim.Data.FencingToken)
	require.Equal(t, firstFence+1, newFence, "expired-lease recovery must advance the fencing token")
	staleAfterRecovery := callPlanFanoutTransition(clientHTTP, callbackServer.URL, rootAlpha.ID, &alpha, firstFence, models.DeliveryPlanStepCompleted)
	require.Equal(t, http.StatusConflict, staleAfterRecovery.Status, "a previous fence must not mutate a reclaimed step: %s", staleAfterRecovery.Body)

	// Completion is gated by append-only evidence bound to this exact step,
	// task, run, worker, machine, and current fencing token. The approved
	// criterion hashes are SHA-256 over the exact UTF-8 criterion strings.
	missingEvidence := callPlanFanoutTransition(clientHTTP, callbackServer.URL, rootAlpha.ID, &alpha, newFence, models.DeliveryPlanStepCompleted)
	require.Equal(t, http.StatusConflict, missingEvidence.Status, "completion without evidence must fail closed: %s", missingEvidence.Body)
	alphaCriteria := []string{"alpha complete", "alpha output reviewed"}
	staleEvidence := callPlanFanoutEvidence(clientHTTP, callbackServer.URL, rootAlpha.ID, &alpha, firstFence, alpha.runID.String(), 2, uuid.Must(uuid.NewV4()), alphaCriteria)
	require.Equal(t, http.StatusConflict, staleEvidence.Status, "evidence from a stale fencing token must be rejected: %s", staleEvidence.Body)
	wrongRun := uuid.Must(uuid.NewV4()).String()
	wrongRunEvidence := callPlanFanoutEvidence(clientHTTP, callbackServer.URL, rootAlpha.ID, &alpha, newFence, wrongRun, 2, uuid.Must(uuid.NewV4()), alphaCriteria)
	require.Equal(t, http.StatusConflict, wrongRunEvidence.Status, "evidence from a different run must be rejected: %s", wrongRunEvidence.Body)
	// A second enrolled key for the same agent/machine can attest to the same
	// task tuple, but the server-attributed instance ID must still match the
	// claimant at completion time.
	require.NoError(t, db.Model(&models.AutomationAgentInstance{}).Where("id = ?", alpha.instance.ID).Update("status", "revoked").Error)
	require.NoError(t, db.Model(&models.AutomationAgentInstance{}).Where("id = ?", alphaTwin.instance.ID).Update("status", "active").Error)
	crossInstanceEvidence := callPlanFanoutEvidence(clientHTTP, callbackServer.URL, rootAlpha.ID, &alphaTwin, newFence, alpha.runID.String(), 2, uuid.Must(uuid.NewV4()), alphaCriteria)
	require.Equal(t, http.StatusOK, crossInstanceEvidence.Status, crossInstanceEvidence.Body)
	require.NoError(t, db.Model(&models.AutomationAgentInstance{}).Where("id = ?", alphaTwin.instance.ID).Update("status", "revoked").Error)
	require.NoError(t, db.Model(&models.AutomationAgentInstance{}).Where("id = ?", alpha.instance.ID).Update("status", "active").Error)
	crossInstanceCompletion := callPlanFanoutTransition(clientHTTP, callbackServer.URL, rootAlpha.ID, &alpha, newFence, models.DeliveryPlanStepCompleted)
	require.Equal(t, http.StatusConflict, crossInstanceCompletion.Status, "evidence from another enrolled instance must not satisfy the completion gate: %s", crossInstanceCompletion.Body)
	incorrectCriteria := []string{"alpha complete", "different approved criterion"}
	incorrectEvidence := callPlanFanoutEvidence(clientHTTP, callbackServer.URL, rootAlpha.ID, &alpha, newFence, alpha.runID.String(), 3, uuid.Must(uuid.NewV4()), incorrectCriteria)
	require.Equal(t, http.StatusBadRequest, incorrectEvidence.Status, "a criterion digest that does not match the approved plan must be rejected: %s", incorrectEvidence.Body)
	incorrectEvidenceCompletion := callPlanFanoutTransition(clientHTTP, callbackServer.URL, rootAlpha.ID, &alpha, newFence, models.DeliveryPlanStepCompleted)
	require.Equal(t, http.StatusConflict, incorrectEvidenceCompletion.Status, "evidence whose criterion hashes do not match the approved plan must not complete the step: %s", incorrectEvidenceCompletion.Body)
	validEvidenceID := uuid.Must(uuid.NewV4())
	validEvidence := callPlanFanoutEvidence(clientHTTP, callbackServer.URL, rootAlpha.ID, &alpha, newFence, alpha.runID.String(), 4, validEvidenceID, alphaCriteria)
	require.Equal(t, http.StatusOK, validEvidence.Status, validEvidence.Body)
	validEvidenceReplay := callPlanFanoutEvidence(clientHTTP, callbackServer.URL, rootAlpha.ID, &alpha, newFence, alpha.runID.String(), 4, validEvidenceID, alphaCriteria)
	require.Equal(t, http.StatusOK, validEvidenceReplay.Status, validEvidenceReplay.Body)
	var evidenceReplayEnvelope struct {
		Data struct {
			Idempotent bool `json:"idempotent"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(validEvidenceReplay.Body), &evidenceReplayEnvelope))
	require.True(t, evidenceReplayEnvelope.Data.Idempotent, "an exact evidence event replay must be acknowledged idempotently")
	var persistedAlphaEvidence int64
	require.NoError(t, db.Model(&models.DeliveryPlanStepActivityEvent{}).
		Where("step_id = ? AND automation_task_id = ? AND run_id = ? AND worker_id = ? AND agent_key = ? AND machine_id = ? AND agent_instance_id = ? AND fencing_token = ? AND action = ? AND phase = ?",
			rootAlpha.ID, alpha.task.ID, alpha.runID.String(), alpha.heartbeat.WorkerID, alpha.profile.AgentKey, alpha.heartbeat.MachineID, alpha.instance.ID, newFence, models.DeliveryPlanStepActivityEvidence, models.DeliveryPlanStepActivityCompleted).
		Count(&persistedAlphaEvidence).Error)
	require.EqualValues(t, 1, persistedAlphaEvidence, "only the valid evidence event for the completing instance should be persisted; exact replay is idempotent")
	var persistedTwinEvidence int64
	require.NoError(t, db.Model(&models.DeliveryPlanStepActivityEvent{}).
		Where("step_id = ? AND agent_instance_id = ? AND action = ? AND phase = ?", rootAlpha.ID, alphaTwin.instance.ID, models.DeliveryPlanStepActivityEvidence, models.DeliveryPlanStepActivityCompleted).
		Count(&persistedTwinEvidence).Error)
	require.EqualValues(t, 1, persistedTwinEvidence, "the cross-instance evidence row must retain the authenticated instance identity")
	var persistedAlphaEvent models.DeliveryPlanStepActivityEvent
	require.NoError(t, db.Where("step_id = ? AND agent_instance_id = ? AND action = ? AND phase = ?", rootAlpha.ID, alpha.instance.ID, models.DeliveryPlanStepActivityEvidence, models.DeliveryPlanStepActivityCompleted).Take(&persistedAlphaEvent).Error)
	var persistedAlphaDetails planFanoutEvidenceDetails
	require.NoError(t, json.Unmarshal([]byte(persistedAlphaEvent.DetailsJSON), &persistedAlphaDetails))
	require.Equal(t, []planFanoutAcceptanceCheck{
		{CriterionSHA256: planFanoutCriterionSHA256(alphaCriteria[0]), Passed: true},
		{CriterionSHA256: planFanoutCriterionSHA256(alphaCriteria[1]), Passed: true},
	}, persistedAlphaDetails.AcceptanceChecks, "the append-only event must retain every approved criterion digest")
	completedAlpha := callPlanFanoutTransition(clientHTTP, callbackServer.URL, rootAlpha.ID, &alpha, newFence, models.DeliveryPlanStepCompleted)
	require.Equal(t, http.StatusOK, completedAlpha.Status, completedAlpha.Body)
	replayedAlphaCompletion := callPlanFanoutTransition(clientHTTP, callbackServer.URL, rootAlpha.ID, &alpha, newFence, models.DeliveryPlanStepCompleted)
	require.Equal(t, http.StatusOK, replayedAlphaCompletion.Status, "an exact completion replay should remain idempotent: %s", replayedAlphaCompletion.Body)
	dependentAfterOne, err := callPlanFanoutClaim(clientHTTP, callbackServer.URL, plan, uuid.Nil, &gamma)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, dependentAfterOne.Status, dependentAfterOne.Body)
	require.Contains(t, strings.ToLower(dependentAfterOne.Body), "active execution already owns assignments", "unassigned worker must not bypass the active fan-out after only one prerequisite completes")

	betaEvidence := callPlanFanoutEvidence(clientHTTP, callbackServer.URL, rootBeta.ID, &beta, secondFence, beta.runID.String(), 1, uuid.Must(uuid.NewV4()), []string{"beta complete"})
	require.Equal(t, http.StatusOK, betaEvidence.Status, betaEvidence.Body)
	completedBeta := callPlanFanoutTransition(clientHTTP, callbackServer.URL, rootBeta.ID, &beta, secondFence, models.DeliveryPlanStepCompleted)
	require.Equal(t, http.StatusOK, completedBeta.Status, completedBeta.Body)

	// Fan-in has opened only now. Persist the third assignment using the same
	// reservation service and then claim through the targeted callback path.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := deliveryplansteps.ReserveReadyAssignmentsInTransaction(tx, execution.ID, 2, map[uuid.UUID]uuid.UUID{dependent.ID: gamma.task.ID}, time.Now().UTC())
		return err
	}))
	var dependentAssignment models.DeliveryPlanStepAssignment
	require.NoError(t, db.Where("execution_id = ? AND delivery_plan_step_id = ?", execution.ID, dependent.ID).Take(&dependentAssignment).Error)
	require.Equal(t, gamma.heartbeat.MachineID, dependentAssignment.TargetMachineID)
	require.Equal(t, gamma.profile.AgentKey, dependentAssignment.TargetAgentKey)
	dependentClaim, err := callPlanFanoutClaim(clientHTTP, callbackServer.URL, plan, dependent.ID, &gamma)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, dependentClaim.Status, dependentClaim.Body)
	require.True(t, dependentClaim.Claim.Data.Available, dependentClaim.Body)
	require.Equal(t, dependent.ID.String(), dependentClaim.Claim.Data.Step.ID)
	var finalActiveCount int64
	require.NoError(t, db.Model(&models.DeliveryPlanStepAssignment{}).Where("execution_id = ? AND status IN ?", execution.ID, []string{
		models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued,
		models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning,
	}).Count(&finalActiveCount).Error)
	require.EqualValues(t, 1, finalActiveCount, "completed roots release capacity before the dependent is dispatched")
}

type planFanoutClaimResult struct {
	Status int
	Body   string
	Claim  planFanoutClaimEnvelope
}

type planFanoutCallbackResult struct {
	Status int
	Body   string
	Err    error
}

func callPlanFanoutClaim(client *http.Client, endpoint string, plan models.DeliveryPlan, stepID uuid.UUID, worker *planFanoutWorker) (planFanoutClaimResult, error) {
	request := planFanoutClaimRequest{
		TaskID: worker.task.ID.String(), PlanID: plan.ID.String(), RunID: worker.runID.String(),
		WorkerID: worker.heartbeat.WorkerID, AgentKey: worker.profile.AgentKey, MachineID: worker.heartbeat.MachineID,
		LeaseSeconds: 30,
	}
	if stepID != uuid.Nil {
		request.StepID = stepID.String()
	}
	body, err := json.Marshal(request)
	if err != nil {
		return planFanoutClaimResult{}, err
	}
	response := callPlanFanoutEndpoint(client, endpoint, http.MethodPost, "/api/internal/automation/steps/claim", body, worker)
	if response.Err != nil {
		return planFanoutClaimResult{}, response.Err
	}
	result := planFanoutClaimResult{Status: response.Status, Body: response.Body}
	if response.Status >= http.StatusOK && response.Status < http.StatusMultipleChoices {
		if err := json.Unmarshal([]byte(response.Body), &result.Claim); err != nil {
			return planFanoutClaimResult{}, fmt.Errorf("decode successful claim response: %w", err)
		}
	}
	return result, nil
}

func callPlanFanoutTransition(client *http.Client, endpoint string, stepID uuid.UUID, worker *planFanoutWorker, fence int64, status string) planFanoutCallbackResult {
	payload := planFanoutLeaseTuple{
		TaskID: worker.task.ID.String(), RunID: worker.runID.String(), WorkerID: worker.heartbeat.WorkerID,
		AgentKey: worker.profile.AgentKey, MachineID: worker.heartbeat.MachineID,
		FencingToken: fmt.Sprintf("%d", fence), Status: status,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return planFanoutCallbackResult{Status: http.StatusInternalServerError, Body: "could not encode transition fixture"}
	}
	return callPlanFanoutEndpoint(client, endpoint, http.MethodPut, "/api/internal/automation/steps/"+stepID.String(), body, worker)
}

func callPlanFanoutEvidence(
	client *http.Client,
	endpoint string,
	stepID uuid.UUID,
	worker *planFanoutWorker,
	fence int64,
	runID string,
	sequence int64,
	eventID uuid.UUID,
	criteria []string,
) planFanoutCallbackResult {
	checks := make([]planFanoutAcceptanceCheck, 0, len(criteria))
	for _, criterion := range criteria {
		checks = append(checks, planFanoutAcceptanceCheck{CriterionSHA256: planFanoutCriterionSHA256(criterion), Passed: true})
	}
	reviewDigest := sha256.Sum256([]byte("integration-review-diff:" + strings.Join(criteria, "\x00")))
	details, err := json.Marshal(planFanoutEvidenceDetails{
		AcceptanceChecks: checks,
		ReviewDiffSHA256: hex.EncodeToString(reviewDigest[:]),
	})
	if err != nil {
		return planFanoutCallbackResult{Status: http.StatusInternalServerError, Err: err}
	}
	payload := planFanoutActivityRequest{
		EventID: eventID.String(), TaskID: worker.task.ID.String(), RunID: runID,
		WorkerID: worker.heartbeat.WorkerID, AgentKey: worker.profile.AgentKey, MachineID: worker.heartbeat.MachineID,
		FencingToken: fmt.Sprintf("%d", fence), Sequence: sequence,
		Action: models.DeliveryPlanStepActivityEvidence, Phase: models.DeliveryPlanStepActivityCompleted,
		Details: details,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return planFanoutCallbackResult{Status: http.StatusInternalServerError, Err: err}
	}
	return callPlanFanoutEndpoint(client, endpoint, http.MethodPost,
		"/api/internal/automation/steps/"+stepID.String()+"/activity", body, worker)
}

func planFanoutCriterionSHA256(criterion string) string {
	digest := sha256.Sum256([]byte(criterion))
	return hex.EncodeToString(digest[:])
}

func callPlanFanoutEndpoint(client *http.Client, endpoint, method, requestURI string, body []byte, worker *planFanoutWorker) planFanoutCallbackResult {
	request, err := http.NewRequest(method, endpoint+requestURI, bytes.NewReader(body))
	if err != nil {
		return planFanoutCallbackResult{Status: http.StatusInternalServerError, Err: err}
	}
	timestamp := time.Now().Unix()
	nonce := uuid.Must(uuid.NewV4()).String()
	signature, err := agentcallbackauth.SignRequest(worker.privateKey, worker.instance.ID.String(), method, request.URL.RequestURI(), timestamp, nonce, body)
	if err != nil {
		return planFanoutCallbackResult{Status: http.StatusInternalServerError, Err: err}
	}
	encodedSignature, err := agentcallbackauth.EncodeSignature(signature)
	if err != nil {
		return planFanoutCallbackResult{Status: http.StatusInternalServerError, Err: err}
	}
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	request.Header.Set(agentcallbackauth.InstanceIDHeader, worker.instance.ID.String())
	request.Header.Set(agentcallbackauth.TimestampHeader, strconv.FormatInt(timestamp, 10))
	request.Header.Set(agentcallbackauth.NonceHeader, nonce)
	request.Header.Set(agentcallbackauth.SignatureHeader, encodedSignature)
	response, err := client.Do(request)
	if err != nil {
		return planFanoutCallbackResult{Status: http.StatusInternalServerError, Err: err}
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return planFanoutCallbackResult{Status: http.StatusInternalServerError, Err: err}
	}
	return planFanoutCallbackResult{Status: response.StatusCode, Body: string(responseBody)}
}

func makePlanFanoutWorker(t *testing.T, agentKey, label string, workItemID uuid.UUID, requestedBy string, now time.Time) planFanoutWorker {
	t.Helper()
	workerID, machineID, runID, instanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	publicKeyText, err := agentcallbackauth.EncodePublicKey(publicKey)
	require.NoError(t, err)
	fingerprint, err := agentcallbackauth.PublicKeyFingerprint(publicKey)
	require.NoError(t, err)
	operations := `["delivery.implementation"]`
	profile := models.AutomationAgentProfile{
		ID: uuid.Must(uuid.NewV4()), AgentKey: agentKey, Name: "Integration " + label,
		Specialty: "Disposable fan-out test", Description: "Synthetic profile used only in integration PostgreSQL",
		OperationsJSON: operations, CapabilitiesJSON: `["delivery_orchestration"]`, Active: true,
		CreatedAt: now, UpdatedAt: now,
	}
	readiness := `[{"id":"fanoutfixture","ready":true,"sandbox_ready":true,"isolation_mode":"docker_container"}]`
	heartbeat := models.AutomationAgentHeartbeat{
		ID: uuid.Must(uuid.NewV4()), WorkerID: workerID.String(), AgentKey: agentKey, MachineID: machineID.String(), AgentInstanceID: &instanceID,
		Provider: "integration", Model: "synthetic", Concurrency: 1, CapabilitiesJSON: operations,
		ProtocolsJSON:      `["delivery.plan_steps.v1"]`,
		WorkspaceReadiness: readiness, StartedAt: now.Add(-time.Minute), LastSeenAt: now,
		CreatedAt: now, UpdatedAt: now,
	}
	instance := models.AutomationAgentInstance{
		ID: instanceID, AgentKey: agentKey, MachineID: machineID.String(),
		PublicKey: publicKeyText, PublicKeyFingerprint: fingerprint, Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}
	leaseExpiry := now.Add(30 * time.Minute)
	task := models.AutomationTask{
		ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), RequestedBy: requestedBy,
		DeliveryWorkItemID: &workItemID, WorkerID: workerID.String(), AgentKey: agentKey,
		MachineID: machineID.String(), CorrelationID: workItemID.String(), Operation: "delivery.implementation",
		InputRef: "private://integration/" + label, Status: "running", RunID: runID.String(),
		LeaseExpiresAt: &leaseExpiry, AttemptCount: 1, CreatedAt: now, UpdatedAt: now,
	}
	return planFanoutWorker{profile: profile, heartbeat: heartbeat, instance: instance, privateKey: privateKey, task: task, runID: runID}
}

func clonePlanFanoutInstance(t *testing.T, worker *planFanoutWorker, now time.Time) planFanoutWorker {
	t.Helper()
	clone := *worker
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	publicKeyText, err := agentcallbackauth.EncodePublicKey(publicKey)
	require.NoError(t, err)
	fingerprint, err := agentcallbackauth.PublicKeyFingerprint(publicKey)
	require.NoError(t, err)
	clone.privateKey = privateKey
	clone.instance = worker.instance
	clone.instance.ID = uuid.Must(uuid.NewV4())
	clone.instance.PublicKey = publicKeyText
	clone.instance.PublicKeyFingerprint = fingerprint
	clone.instance.Status = "revoked"
	clone.instance.CreatedAt = now
	clone.instance.UpdatedAt = now
	return clone
}

func parsePlanFanoutFence(raw string) (int64, error) {
	var fence int64
	if _, err := fmt.Sscan(raw, &fence); err != nil || fence < 1 {
		return 0, fmt.Errorf("invalid fencing token %q", raw)
	}
	return fence, nil
}

func mustPlanFanoutFence(t *testing.T, raw string) int64 {
	t.Helper()
	fence, err := parsePlanFanoutFence(raw)
	require.NoError(t, err)
	return fence
}
