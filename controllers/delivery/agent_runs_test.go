package delivery

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/releasegate"
	"events-stocks/models"
	"events-stocks/services/deliveryplansteps"
	"events-stocks/services/deliveryworkflow"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestPlanExecutionConflictMapsToStableHTTP409(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/api/automation/work-items/id/agent-runs", nil), recorder)

	handled, err := handlePlanExecutionConflict(ctx, fmt.Errorf("create plan execution: %w", deliveryplansteps.ErrPlanExecutionConflict))
	if err != nil || !handled {
		t.Fatalf("handlePlanExecutionConflict() = handled %v, err %v", handled, err)
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	var response struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != http.StatusConflict || response.Message != "Conflicto en la ejecución del plan" || response.Error != planExecutionConflictCode {
		t.Fatalf("response = %#v, want stable plan execution conflict response", response)
	}
}

func TestPlanExecutionConflictMapperDoesNotHandleOtherErrors(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/api/automation/work-items/id/agent-runs", nil), recorder)

	handled, err := handlePlanExecutionConflict(ctx, fmt.Errorf("database unavailable"))
	if handled || err != nil {
		t.Fatalf("handlePlanExecutionConflict(other error) = handled %v, err %v; want false, nil", handled, err)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("non-conflict error wrote a response body: %s", recorder.Body.String())
	}
}

func TestAgentRunSpecsAreBoundToDeliveryStates(t *testing.T) {
	checks := []struct {
		phase     string
		operation string
		state     string
	}{
		{"plan", "delivery.plan", deliveryworkflow.StatePlanning},
		{"implementation", "delivery.implementation", deliveryworkflow.StateImplementation},
		{"assessment", "delivery.assessment", deliveryworkflow.StateImplementation},
		{"publish", "delivery.publish", deliveryworkflow.StateCodeReview},
		{"release_gate", "delivery.release_gate", deliveryworkflow.StateReleaseReview},
		{"qa", "delivery.qa", deliveryworkflow.StateQARunning},
		{"summary", "delivery.summary", deliveryworkflow.StateReleaseReview},
	}
	for _, check := range checks {
		spec, found := agentRunSpecs[check.phase]
		if !found || spec.operation != check.operation {
			t.Fatalf("invalid spec for %s", check.phase)
		}
		if _, allowed := spec.states[check.state]; !allowed {
			t.Fatalf("%s must be bound to %s", check.phase, check.state)
		}
	}
	if _, allowed := agentRunSpecs["plan"].states[deliveryworkflow.StateImplementation]; allowed {
		t.Fatal("a plan run must not be started during implementation")
	}
	if _, allowed := agentRunSpecs["summary"].states[deliveryworkflow.StateReleased]; allowed {
		t.Fatal("a completed delivery must not enqueue another summary run")
	}
}

func TestEnvironmentContextKeepsWorkflowWithoutLeakingExtraMetadata(t *testing.T) {
	raw := map[string]any{
		"branch": "staging", "deployment": "manual", "url": "https://staging.example.test",
		"promotion": "After QA, open a PR to main", "excerpt": "QA verifies the preview",
		"api_key": "must-stay-private", "github_code_context": "not-an-environment-field",
	}
	got := sanitizedDeliveryContextMetadata("environment", raw)
	for _, key := range []string{"branch", "deployment", "url", "promotion", "excerpt"} {
		if got[key] != raw[key] {
			t.Fatalf("environment field %s was not preserved: %#v", key, got)
		}
	}
	if _, found := got["api_key"]; found {
		t.Fatal("credentials must not leave with environment context")
	}
	if _, found := got["github_code_context"]; found {
		t.Fatal("repository metadata must not be accepted on an environment source")
	}
}

func TestRunbookContextKeepsProjectWorkflowWithoutLeakingSecrets(t *testing.T) {
	raw := map[string]any{
		"technologies": "Go and Next.js", "issue_workflow": "Plan in GitHub Issues",
		"branch_workflow": "Feature branches start from dev", "pull_request_workflow": "Review before merge",
		"release_workflow": "Promote through staging", "excerpt": "Project-specific workflow",
		"api_key": "must-stay-private", "github_code_context": "not-a-runbook-field",
	}
	got := sanitizedDeliveryContextMetadata("runbook", raw)
	for _, key := range []string{"technologies", "issue_workflow", "branch_workflow", "pull_request_workflow", "release_workflow", "excerpt"} {
		if got[key] != raw[key] {
			t.Fatalf("runbook field %s was not preserved: %#v", key, got)
		}
	}
	if _, found := got["api_key"]; found {
		t.Fatal("credentials must not leave with runbook context")
	}
	if _, found := got["github_code_context"]; found {
		t.Fatal("repository metadata must not be accepted on a runbook source")
	}
}

func TestChatAdmissionDoesNotRepresentInformationalWorkAsDeliveryProgress(t *testing.T) {
	// The persistence branch in StartAgentRun is intentionally phase-scoped:
	// delivery.chat may allocate a task and write a durable answer, but it must
	// leave the current delivery phase and recovery projection untouched.
	if agentRunUpdatesWorkflowProgress("chat") {
		t.Fatal("delivery.chat must not update workflow progress")
	}
	if !agentRunUpdatesWorkflowProgress("assessment") {
		t.Fatal("a read-only assessment is a terminal workflow result, not informational chat")
	}
}

func TestStoredReleaseGateCandidateUsesOnlyThePublishedExactRepositoryMatrix(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	item := models.DeliveryWorkItem{ID: workItemID, PlanJSON: `{"repository_impact":[{"reference":"workspace://web","impact":"changes"},{"reference":"workspace://api","impact":"changes"}]}`}
	change := func(reference, repository, branch, sha, pr string) models.DeliveryChangeSet {
		return models.DeliveryChangeSet{
			RepositoryRef: reference, Branch: branch, CommitSHA: sha, ReviewType: "pull_request", PullRequestURL: pr,
			MetadataJSON: `{"branch_published":true,"verification_source":"itbem-github-app","remote_repository":"` + repository + `","target_branch":"main"}`,
		}
	}
	changes := []models.DeliveryChangeSet{
		change("workspace://web", "Example/Web", "itbem-agent/22222222-2222-4222-8222-222222222222", strings.Repeat("b", 40), "https://github.com/Example/Web/pull/8"),
		change("workspace://api", "Example/API", "itbem-agent/11111111-1111-4111-8111-111111111111", strings.Repeat("a", 40), "https://github.com/Example/API/pull/7"),
	}
	candidate, err := storedReleaseGateCandidate(item, changes)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ChangeSetID != workItemID.String() || candidate.Action != "release" || candidate.Policy.Resolved || len(candidate.Revisions) != 2 {
		t.Fatalf("unexpected release candidate: %#v", candidate)
	}
	if candidate.Revisions[0].Repository != "example/api" || candidate.Revisions[1].Repository != "example/web" {
		t.Fatalf("revision matrix must be canonical and sorted: %#v", candidate.Revisions)
	}
	if candidate.Revisions[0].Branch != "main" || candidate.Revisions[1].Branch != "main" {
		t.Fatalf("revision matrix must use each persisted PR target branch: %#v", candidate.Revisions)
	}
	if decision := releasegate.Evaluate(candidate); decision.State != "blocked" {
		t.Fatalf("stored evidence alone must never authorize release: %#v", decision)
	}
}

func TestStoredReleaseGateCandidateFailsClosedForMissingOrUntrustedPublishedHead(t *testing.T) {
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), PlanJSON: `{"repository_impact":[{"reference":"workspace://api","impact":"changes"}]}`}
	for _, changes := range [][]models.DeliveryChangeSet{
		nil,
		{{RepositoryRef: "workspace://api", Branch: "itbem-agent/11111111-1111-4111-8111-111111111111", CommitSHA: strings.Repeat("a", 40), ReviewType: "pull_request", PullRequestURL: "https://github.com/Example/API/pull/7", MetadataJSON: `{"branch_published":true,"verification_source":"manual","remote_repository":"example/api","target_branch":"main"}`}},
		{{RepositoryRef: "workspace://api", Branch: "itbem-agent/11111111-1111-4111-8111-111111111111", CommitSHA: strings.Repeat("a", 39), ReviewType: "pull_request", PullRequestURL: "https://github.com/Example/API/pull/7", MetadataJSON: `{"branch_published":true,"verification_source":"itbem-github-app","remote_repository":"example/api","target_branch":"main"}`}},
		{{RepositoryRef: "workspace://api", Branch: "itbem-agent/11111111-1111-4111-8111-111111111111", CommitSHA: strings.Repeat("a", 40), ReviewType: "pull_request", PullRequestURL: "https://github.com/Example/API/pull/7", MetadataJSON: `{"branch_published":true,"verification_source":"itbem-github-app","remote_repository":"example/api"}`}},
	} {
		if _, err := storedReleaseGateCandidate(item, changes); err == nil {
			t.Fatal("missing or untrusted exact PR head must fail closed")
		}
	}
}

func TestApprovedPlanStepQueueMessageTargetsOneStepAndKeepsParentInputReference(t *testing.T) {
	workItemID, projectID, taskID, jobID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	inputRef := "s3://private-inputs/automation/inputs/shared-plan/input.json"
	item := models.DeliveryWorkItem{ID: workItemID, ProjectID: projectID}
	child := models.AutomationTask{ID: taskID, JobID: jobID, CorrelationID: "correlation-123", Operation: "delivery.implementation", InputRef: inputRef, MaxCompletionTokens: 512}
	step := deliveryplansteps.StepDTO{ID: stepID.String(), AgentKey: "backend_engineer"}
	message := approvedPlanStepQueueMessage(item, child, step)
	if message.JobID != jobID.String() || message.Payload.TaskID != taskID.String() || message.Payload.ProjectID != projectID.String() {
		t.Fatalf("plan-step queue message lost task scope: %#v", message)
	}
	if message.Payload.PlanStepID != stepID.String() || message.Payload.AgentKey != "backend_engineer" {
		t.Fatalf("plan-step message must target the authenticated worker profile and exactly one step: %#v", message.Payload)
	}
	if message.Payload.InputRef != inputRef || message.Payload.Operation != child.Operation || message.Payload.Attempt != 1 {
		t.Fatalf("child message must reuse the encrypted immutable parent input and operation: %#v", message.Payload)
	}
}

func TestImplementationFanoutRejectsStepDefinitionsThatDifferFromFrozenApproval(t *testing.T) {
	stepID, planID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
	expected := []deliveryplansteps.StepDTO{{
		ID: stepID, PlanID: planID, PlanVersion: 3, StepKey: "add-handler", Order: 1,
		Title: "Add handler", Objective: "Implement the endpoint", AcceptanceCriteria: []string{"Tests pass"}, AgentKey: "backend_engineer",
	}}
	frozen := append([]deliveryplansteps.StepDTO(nil), expected...)
	if !sameApprovedPlanStepDefinitions(expected, frozen) {
		t.Fatal("the exact approved plan definition should match")
	}
	frozen[0].AgentKey = "frontend_engineer"
	if sameApprovedPlanStepDefinitions(expected, frozen) {
		t.Fatal("fan-out must not route a stale input to a changed frozen step profile")
	}
}

func TestAggregatePlanStepBudgetReservationIsConservativeAndBounded(t *testing.T) {
	got, err := aggregatePlanStepBudgetReservation(125, 4)
	if err != nil || got != 500 {
		t.Fatalf("aggregate reservation = %d, err=%v; want 500", got, err)
	}
	if got, err := aggregatePlanStepBudgetReservation(0, 3); err != nil || got != 0 {
		t.Fatalf("unbounded parent budget must not invent a hold, got=%d err=%v", got, err)
	}
	for _, input := range []struct {
		perChild int64
		count    int
	}{{1, 0}, {-1, 1}, {1, models.DeliveryPlanExecutionMaxConcurrency + 1}, {int64(^uint64(0) >> 1), 2}} {
		if _, err := aggregatePlanStepBudgetReservation(input.perChild, input.count); err == nil {
			t.Fatalf("invalid or overflowing aggregate reservation was accepted: %#v", input)
		}
	}
}

func TestBuildDeliveryAgentInputUsesFrozenContextAndBoundedScope(t *testing.T) {
	projectID, workItemID, sourceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	item := models.DeliveryWorkItem{
		ID: workItemID, ProjectID: projectID, State: deliveryworkflow.StatePlanning,
		Title: "Plan delivery", Description: "Review the change", ExpectedOutcome: "Approved plan",
		IncludedScopeJSON: `["controllers/delivery"]`, ExcludedScopeJSON: `["production"]`, AcceptanceJSON: `["Human approves the plan"]`,
		PlanJSON:          `{"implementation_steps":["Add the bounded endpoint"],"qa_plan":["Run focused tests"]}`,
		ClientContextJSON: `{"health":"watch","rules":["No Friday deploy"],"conversation_summary":"Client wants explicit QA evidence.","profile_updated_at":"2026-08-06T00:00:00Z","contacts":["must not leak"]}`,
	}
	project := models.DeliveryProject{ID: projectID, Name: "Control plane", Summary: "ITBEM internal"}
	snapshotAt := time.Date(2026, 8, 9, 0, 30, 0, 0, time.UTC)
	snapshots := []models.DeliveryContextSnapshot{{WorkItemID: workItemID, SourceID: sourceID, Kind: "repository", Name: "Backend at review", Reference: "workspace://backend", Revision: "abc123", MetadataJSON: `{"excerpt":"Keep the migration backward compatible."}`, CapturedAt: snapshotAt}}
	messages := []models.DeliveryMessage{{Phase: "plan_review", AuthorType: "human", AuthorID: "must-not-leak", Body: "Add an explicit rollback path.", CreatedAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}}
	changeSets := []models.DeliveryChangeSet{{RepositoryRef: "workspace://backend", Branch: "itbem-agent/123", ReviewType: "local_worktree", CIStatus: "passed"}}
	evidenceID, gateID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	capturedAt := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC)
	evidence := []models.DeliveryEvidence{{ID: evidenceID, Kind: "screenshot", Phase: "qa", Title: "Mobile checkout", Reference: "s3://private/must-not-leak.png", MetadataJSON: `{"content_type":"image/png","size_bytes":1234,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, CapturedBy: "must-not-leak", CapturedAt: &capturedAt}}
	gates := []models.DeliveryGate{{ID: gateID, Kind: "qa", Decision: "approved", DecidedBy: "must-not-leak", Comment: "Reviewed the screenshot.", EvidenceChecklist: `["mobile screenshot reviewed"]`, DecidedAt: capturedAt}}
	input, err := buildDeliveryAgentInput(item, project, snapshots, changeSets, evidence, gates, messages, "Focus on error paths", "implementation")
	if err != nil {
		t.Fatal(err)
	}
	if input.Delivery.ContextSources[0].Revision != "abc123" || input.Delivery.ContextSources[0].Reference != "workspace://backend" {
		t.Fatal("agent input must use the immutable snapshot, not a changed source")
	}
	if input.Delivery.ContextSources[0].Name != "Backend at review" {
		t.Fatal("agent input must use the snapshotted context identity")
	}
	if input.Delivery.ContextSources[0].SnapshotAt != snapshotAt.Format(time.RFC3339) {
		t.Fatalf("agent input must disclose frozen context recency: %#v", input.Delivery.ContextSources[0])
	}
	if input.Delivery.ContextSources[0].Metadata["excerpt"] != "Keep the migration backward compatible." {
		t.Fatal("agent input must use frozen context metadata")
	}
	if input.Delivery.HumanRequest != "Focus on error paths" || input.Delivery.WorkItem.IncludedScope[0] != "controllers/delivery" {
		t.Fatal("agent input lost bounded human requirements")
	}
	if input.Delivery.ClientContext.Health != "watch" || input.Delivery.ClientContext.Rules[0] != "No Friday deploy" {
		t.Fatal("agent input must use the frozen minimum client context")
	}
	if input.Delivery.ApprovedPlan["implementation_steps"] == nil || input.Delivery.AutonomyPolicy.Phase != "implementation" {
		t.Fatal("agent input must include the persisted plan and explicit phase policy")
	}
	if len(input.Delivery.Conversation) != 1 || input.Delivery.Conversation[0].Body != "Add an explicit rollback path." || input.Delivery.Conversation[0].CreatedAt == "" {
		t.Fatalf("agent input must include the bounded task conversation: %#v", input.Delivery.Conversation)
	}
	if len(input.Delivery.ChangeSets) != 1 || input.Delivery.ChangeSets[0].RepositoryRef != "workspace://backend" || input.Delivery.ChangeSets[0].Branch != "itbem-agent/123" {
		t.Fatalf("QA must receive the immutable reviewed worktree map: %#v", input.Delivery.ChangeSets)
	}
	if len(input.Delivery.Evidence) != 1 || input.Delivery.Evidence[0].ID != evidenceID.String() || input.Delivery.Evidence[0].SHA256 == "" || input.Delivery.Evidence[0].ContentType != "image/png" {
		t.Fatalf("delivery summary context must cite recorded evidence safely: %#v", input.Delivery.Evidence)
	}
	if len(input.Delivery.Gates) != 1 || input.Delivery.Gates[0].ID != gateID.String() || input.Delivery.Gates[0].Decision != "approved" || input.Delivery.Gates[0].Comment != "Reviewed the screenshot." {
		t.Fatalf("delivery summary context must include human gate outcome: %#v", input.Delivery.Gates)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("agent input must remain serializable: %v", err)
	}
	if strings.Contains(string(encoded), "must not leak") || strings.Contains(string(encoded), "s3://private") {
		t.Fatal("contacts, author IDs and private evidence locations must never be included in agent input")
	}
	if input.Delivery.Mandate.Version != deliveryMandateVersion || input.Delivery.Mandate.MaxConcurrency != models.DefaultDeliveryMandateMaxConcurrency || input.Delivery.Mandate.Objective != "Approved plan" {
		t.Fatalf("agent input must include the durable bounded mandate: %#v", input.Delivery.Mandate)
	}
	if len(input.Delivery.Mandate.EffectiveAllowedTools) == 0 || !containsDeliveryString(input.Delivery.Mandate.EffectiveAllowedTools, "patch.apply") {
		t.Fatalf("implementation capabilities must be explicitly bounded: %#v", input.Delivery.Mandate.EffectiveAllowedTools)
	}
}

func TestDeliveryMandateDefaultsAreConservativeAndPhaseScoped(t *testing.T) {
	item := models.DeliveryWorkItem{Title: "Ship contract", ExpectedOutcome: "A reviewed contract", IncludedScopeJSON: `["packages/contracts"]`, ExcludedScopeJSON: `["production"]`, BudgetMicros: 120000}
	mandate := defaultDeliveryMandate(item, []string{"workspace://api", "workspace://api"})
	if mandate.Version != deliveryMandateVersion || mandate.MaxConcurrency != models.DefaultDeliveryMandateMaxConcurrency || mandate.BudgetMicros != 120000 || len(mandate.RepositoryRefs) != 1 {
		t.Fatalf("unexpected default mandate: %#v", mandate)
	}
	if _, err := validateDeliveryMandate(mandate); err != nil {
		t.Fatal(err)
	}
	chat := effectiveDeliveryMandate(mandate, "chat")
	if containsDeliveryString(chat.EffectiveAllowedTools, "patch.apply") || !containsDeliveryString(chat.EffectiveAllowedTools, "conversation.respond") {
		t.Fatalf("chat must remain informational and read-only: %#v", chat.EffectiveAllowedTools)
	}
	item.MandateJSON = "{}"
	resolved, err := resolveDeliveryMandate(item, []models.DeliveryContextSnapshot{
		{Kind: "repository", Reference: "workspace://api"},
		{Kind: "document", Reference: "https://docs.example.test/brief"},
	})
	if err != nil || len(resolved.RepositoryRefs) != 1 || resolved.RepositoryRefs[0] != "workspace://api" {
		t.Fatalf("only repository snapshots may enter repository_refs: %#v / %v", resolved.RepositoryRefs, err)
	}
}

func TestStoredDeliveryMandateFailsClosedOnUnknownCapabilityOrVersion(t *testing.T) {
	item := models.DeliveryWorkItem{Title: "Bounded task", ExpectedOutcome: "Evidence", MandateJSON: `{"version":1,"objective":"Evidence","included_scope":[],"excluded_scope":[],"repository_refs":[],"allowed_tools":["shell.exec"],"max_concurrency":1,"budget_microusd":0,"autonomy_policy":"bounded_autonomy","stop_conditions":["scope_exceeded"],"human_actions":["approve_plan"]}`}
	if _, err := resolveDeliveryMandate(item, nil); err == nil || !strings.Contains(err.Error(), "unallowlisted") {
		t.Fatalf("unknown capabilities must fail closed, got %v", err)
	}
	item.MandateJSON = `{"version":2,"objective":"Evidence","included_scope":[],"excluded_scope":[],"repository_refs":[],"allowed_tools":["context.read"],"max_concurrency":1,"budget_microusd":0,"autonomy_policy":"bounded_autonomy","stop_conditions":["scope_exceeded"],"human_actions":["approve_plan"]}`
	if _, err := resolveDeliveryMandate(item, nil); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unknown mandate versions must fail closed, got %v", err)
	}
}

func containsDeliveryString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestBuildDeliveryAgentInputSanitizesContextMetadataBeforeInference(t *testing.T) {
	projectID, workItemID, sourceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	item := models.DeliveryWorkItem{
		ID: workItemID, ProjectID: projectID, State: deliveryworkflow.StatePlanning,
		IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`, AcceptanceJSON: `[]`,
	}
	rawMetadata := `{
		"repository_role":"primary",
		"repository_kind":"backend_api",
		"workspace_capabilities":["repository:read","worktree:create"],
		"workspace_harness":{"semantic_qa_mode":"configured_command","qa_command_count":1},
		"workspace_architecture":{"runtime_hints":["go"],"entrypoint_paths":["cmd/api/main.go"],"nested_secret":"must-not-leak"},
		"github_code_map":{"file_count":2,"files":["README.md","cmd/api/main.go"],"api_key":"must-not-leak"},
		"github_code_context":{"revision":"abc123","excerpts":[{"path":"README.md","content":"# Safe architecture\nAPI_KEY=must-not-leak"},{"path":"cmd/api/main.go","content":"package main","api_key":"must-not-leak"}],"redacted_values":1},
		"local_remote_refs_fetched_by_user_id":"must-not-leak",
		"api_key":"must-not-leak",
		"contact_email":"must-not-leak",
		"unrecognized_notes":"must-not-leak"
	}`
	snapshots := []models.DeliveryContextSnapshot{{
		WorkItemID: workItemID, SourceID: sourceID, Kind: "repository", Name: "Backend", Reference: "workspace://backend", Revision: "abc123", MetadataJSON: rawMetadata,
	}}
	input, err := buildDeliveryAgentInput(item, models.DeliveryProject{ID: projectID}, snapshots, nil, nil, nil, nil, "", "plan")
	if err != nil {
		t.Fatal(err)
	}
	metadata := input.Delivery.ContextSources[0].Metadata
	if metadata["repository_role"] != "primary" || metadata["repository_kind"] != "backend_api" || metadata["workspace_harness"] == nil || metadata["workspace_architecture"] == nil || metadata["github_code_map"] == nil || metadata["github_code_context"] == nil {
		t.Fatalf("allowed repository topology and harness context was lost: %#v", metadata)
	}
	architecture, ok := metadata["workspace_architecture"].(map[string]any)
	if !ok || architecture["nested_secret"] != nil {
		t.Fatalf("workspace architecture signals must remain useful without nested secrets: %#v", metadata["workspace_architecture"])
	}
	codeMap, ok := metadata["github_code_map"].(map[string]any)
	if !ok || codeMap["file_count"] != float64(2) || codeMap["api_key"] != nil {
		t.Fatalf("repository inventory should remain useful and scrub nested secrets: %#v", metadata["github_code_map"])
	}
	codeContext, ok := metadata["github_code_context"].(map[string]any)
	if !ok || codeContext["revision"] != "abc123" || codeContext["redacted_values"] != 2 {
		t.Fatalf("bounded remote source orientation was lost: %#v", metadata["github_code_context"])
	}
	excerpts, ok := codeContext["excerpts"].([]any)
	if !ok || len(excerpts) != 2 || excerpts[1].(map[string]any)["api_key"] != nil {
		t.Fatalf("remote source context must retain safe excerpts but scrub nested secrets: %#v", codeContext)
	}
	encoded, marshalErr := json.Marshal(input)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), "must-not-leak") || metadata["unrecognized_notes"] != nil {
		t.Fatalf("private or uncontracted context metadata reached model input: %s", encoded)
	}
	if snapshots[0].MetadataJSON != rawMetadata {
		t.Fatal("sanitizing model context must not mutate the frozen timeline snapshot")
	}
}

func TestBuildDeliveryAgentInputProjectsFrozenEpicContextAllowlist(t *testing.T) {
	projectID, workItemID, epicID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	item := models.DeliveryWorkItem{
		ID: workItemID, ProjectID: projectID, State: deliveryworkflow.StatePlanning,
		IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`, AcceptanceJSON: `[]`,
	}
	rawMetadata := `{"epic_id":"` + epicID.String() + `","status":"active","summary":"Ship the audit-ready delivery workflow.","api_key":"must-not-reach-inference","created_by":"private-user","unreviewed_notes":"must-not-reach-inference"}`
	snapshots := []models.DeliveryContextSnapshot{{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: workItemID, SourceID: epicID, Kind: "epic", Name: "  Delivery platform  ",
		Reference: "  epic://" + epicID.String() + "  ", Revision: "epic-v3", MetadataJSON: rawMetadata,
		CapturedAt: time.Date(2026, 8, 9, 0, 30, 0, 0, time.UTC),
	}}
	original := snapshots[0]
	input, err := buildDeliveryAgentInput(item, models.DeliveryProject{ID: projectID}, snapshots, nil, nil, nil, nil, "", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Delivery.ContextSources) != 1 {
		t.Fatalf("expected one frozen epic context source, got %#v", input.Delivery.ContextSources)
	}
	projected := input.Delivery.ContextSources[0]
	if projected.Kind != "epic" || projected.Name != "Delivery platform" || projected.Reference != "epic://"+epicID.String() || projected.Revision != "epic-v3" {
		t.Fatalf("epic identity must be projected from the frozen snapshot: %#v", projected)
	}
	if len(projected.Metadata) != 3 || projected.Metadata["epic_id"] != epicID.String() || projected.Metadata["status"] != "active" || projected.Metadata["summary"] != "Ship the audit-ready delivery workflow." {
		t.Fatalf("epic metadata must contain only the shared safe projection: %#v", projected.Metadata)
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "must-not-reach-inference") || strings.Contains(string(encoded), "created_by") {
		t.Fatalf("unallowlisted epic metadata reached inference: %s", encoded)
	}
	if snapshots[0] != original {
		t.Fatal("building inference input must not mutate the immutable epic snapshot")
	}
}

func TestBuildDeliveryAgentInputRejectsCredentialShapedLegacyEpicSnapshotsWithoutEcho(t *testing.T) {
	projectID, workItemID, epicID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	item := models.DeliveryWorkItem{
		ID: workItemID, ProjectID: projectID, State: deliveryworkflow.StatePlanning,
		IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`, AcceptanceJSON: `[]`,
	}
	const fixedError = "frozen epic context snapshot is invalid"
	cases := []struct {
		name      string
		title     string
		reference string
		revision  string
		summary   string
	}{
		{name: "title", title: "Delivery api_key=legacy-secret", reference: "epic://" + epicID.String(), summary: "Safe summary"},
		{name: "reference", title: "Delivery", reference: "epic://" + epicID.String() + "?token=legacy-secret", summary: "Safe summary"},
		{name: "revision", title: "Delivery", reference: "epic://" + epicID.String(), revision: "api_key=legacy-secret", summary: "Safe summary"},
		{name: "oversized revision", title: "Delivery", reference: "epic://" + epicID.String(), revision: strings.Repeat("r", 81), summary: "Safe summary"},
		{name: "summary", title: "Delivery", reference: "epic://" + epicID.String(), summary: "Legacy password=legacy-secret"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			metadata, err := json.Marshal(map[string]string{"epic_id": epicID.String(), "status": "active", "summary": test.summary})
			if err != nil {
				t.Fatal(err)
			}
			snapshots := []models.DeliveryContextSnapshot{{
				WorkItemID: workItemID, SourceID: epicID, Kind: "epic", Name: test.title,
				Reference: test.reference, Revision: test.revision, MetadataJSON: string(metadata),
			}}
			_, buildErr := buildDeliveryAgentInput(item, models.DeliveryProject{ID: projectID}, snapshots, nil, nil, nil, nil, "", "plan")
			if buildErr == nil || buildErr.Error() != fixedError {
				t.Fatalf("credential-shaped legacy epic data must fail with one generic error, got %v", buildErr)
			}
			if strings.Contains(buildErr.Error(), "legacy-secret") || strings.Contains(buildErr.Error(), "api_key") || strings.Contains(buildErr.Error(), "password") {
				t.Fatalf("rejection error leaked legacy data: %v", buildErr)
			}
		})
	}
}

func TestFrozenRepositoryTopologyMakesCrossRepositoryImpactExplicit(t *testing.T) {
	workItemID, sourceID, supportingID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	snapshots := []models.DeliveryContextSnapshot{
		{WorkItemID: workItemID, SourceID: sourceID, Kind: "repository", Name: "API", Reference: "workspace://api", Revision: "a1", MetadataJSON: `{"repository_role":"primary","repository_kind":"backend_api","repository_responsibility":"Public API and delivery control plane","depends_on_repositories":["workspace://web"]}`},
		{WorkItemID: workItemID, SourceID: supportingID, Kind: "repository", Name: "Web", Reference: "workspace://web", Revision: "b2", MetadataJSON: `{"repository_role":"supporting","repository_kind":"frontend","repository_responsibility":"Dashboard contracts and visual verification"}`},
	}
	topology, err := frozenRepositoryTopology(snapshots)
	if err != nil || len(topology) != 2 {
		t.Fatalf("expected valid multi-repository topology: %#v / %v", topology, err)
	}
	if topology[0].Role != "primary" || topology[0].Kind != "backend_api" || topology[0].DependsOn[0] != "workspace://web" || topology[1].Role != "supporting" || topology[1].Kind != "frontend" {
		t.Fatalf("repository topology lost roles or dependencies: %#v", topology)
	}
	bad := append([]models.DeliveryContextSnapshot(nil), snapshots...)
	bad[1].MetadataJSON = `{"repository_role":"primary"}`
	if _, err := frozenRepositoryTopology(bad); err == nil {
		t.Fatal("multiple primaries must fail closed")
	}
}

func TestFrozenRepositoryTopologyMakesMissingOrInvalidKindsExplicit(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	legacy := []models.DeliveryContextSnapshot{{
		WorkItemID: workItemID, SourceID: uuid.Must(uuid.NewV4()), Kind: "repository", Name: "Legacy", Reference: "workspace://legacy", Revision: "a1", MetadataJSON: `{"repository_role":"primary"}`,
	}}
	topology, err := frozenRepositoryTopology(legacy)
	if err != nil || len(topology) != 1 || topology[0].Kind != "unclassified" {
		t.Fatalf("legacy repository should carry an explicit unclassified kind: %#v / %v", topology, err)
	}
	legacy[0].MetadataJSON = `{"repository_role":"primary","repository_kind":"unknown"}`
	if _, err := frozenRepositoryTopology(legacy); err == nil {
		t.Fatal("invalid repository kind must not reach the agent topology")
	}
}

func TestFrozenRepositoryTopologyRejectsIndirectDependencyCycles(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	snapshots := []models.DeliveryContextSnapshot{
		{WorkItemID: workItemID, SourceID: uuid.Must(uuid.NewV4()), Kind: "repository", Name: "API", Reference: "workspace://api", Revision: "a1", MetadataJSON: `{"repository_role":"primary","depends_on_repositories":["workspace://web"]}`},
		{WorkItemID: workItemID, SourceID: uuid.Must(uuid.NewV4()), Kind: "repository", Name: "Web", Reference: "workspace://web", Revision: "b2", MetadataJSON: `{"repository_role":"supporting","depends_on_repositories":["workspace://worker"]}`},
		{WorkItemID: workItemID, SourceID: uuid.Must(uuid.NewV4()), Kind: "repository", Name: "Worker", Reference: "workspace://worker", Revision: "c3", MetadataJSON: `{"repository_role":"supporting","depends_on_repositories":["workspace://api"]}`},
	}
	if _, err := frozenRepositoryTopology(snapshots); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("indirect repository dependency cycle must fail closed, got %v", err)
	}
}

func TestDeliveryAutonomyPolicyKeepsPublicationHumanControlled(t *testing.T) {
	policy := deliveryAutonomyPolicy("implementation")
	if policy.Phase != "implementation" || len(policy.Allowed) == 0 || len(policy.RequiredEvidence) == 0 {
		t.Fatalf("implementation policy is incomplete: %#v", policy)
	}
	for _, prohibited := range policy.Prohibited {
		if prohibited == "deploy, merge, push, or publish remotely" {
			return
		}
	}
	t.Fatal("implementation policy must forbid remote publication")
}

func TestDeliveryAutonomyPolicyNamesTheImmediateHumanGateForEveryPhase(t *testing.T) {
	checks := map[string]string{
		"plan":           "before implementation",
		"implementation": "publication grant",
		"publish":        "before QA can begin",
		"qa":             "before release review",
		"summary":        "before marking the delivery released",
	}
	for phase, expected := range checks {
		policy := deliveryAutonomyPolicy(phase)
		if len(policy.HumanGateRequiredFor) == 0 || !strings.Contains(strings.Join(policy.HumanGateRequiredFor, " "), expected) {
			t.Fatalf("%s policy must name its immediate human gate: %#v", phase, policy.HumanGateRequiredFor)
		}
	}
}

func TestApplyFrozenAutonomyPolicyKeepsModelUnprivileged(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	input := deliveryAgentInput{}
	input.Delivery.AutonomyPolicy = deliveryAutonomyPolicy("qa")
	originalProhibited := append([]string(nil), input.Delivery.AutonomyPolicy.Prohibited...)
	originalAllowed := append([]string(nil), input.Delivery.AutonomyPolicy.Allowed...)

	applyFrozenAutonomyPolicy(&input, deliveryledger.AutonomySnapshot{EventID: eventID, Delegated: true})
	policy := input.Delivery.AutonomyPolicy
	if policy.GateAuthority != "delegated" || policy.AuthoritySnapshotID != eventID.String() {
		t.Fatalf("delegated snapshot must be projected into the task policy: %#v", policy)
	}
	if len(policy.HumanGateRequiredFor) != 0 || len(policy.CoordinatorRequiredFor) == 0 {
		t.Fatalf("delegated policy must name the coordinator instead of a human gate: %#v", policy)
	}
	if !strings.Contains(strings.Join(policy.RequiredEvidence, " "), "independent role evidence") {
		t.Fatalf("delegated policy must require independently validated evidence: %#v", policy.RequiredEvidence)
	}
	if strings.Join(policy.Prohibited, "\n") != strings.Join(originalProhibited, "\n") || strings.Join(policy.Allowed, "\n") != strings.Join(originalAllowed, "\n") {
		t.Fatalf("a delegated policy must not give a model approval or remote authority: %#v", policy)
	}
}

func TestApplyFrozenAutonomyPolicyLeavesManualModeUntouched(t *testing.T) {
	input := deliveryAgentInput{}
	input.Delivery.AutonomyPolicy = deliveryAutonomyPolicy("plan")
	applyFrozenAutonomyPolicy(&input, deliveryledger.AutonomySnapshot{EventID: uuid.Must(uuid.NewV4())})
	if input.Delivery.AutonomyPolicy.GateAuthority != "human" || len(input.Delivery.AutonomyPolicy.HumanGateRequiredFor) == 0 || input.Delivery.AutonomyPolicy.AuthoritySnapshotID != "" {
		t.Fatalf("manual policy must remain the default: %#v", input.Delivery.AutonomyPolicy)
	}
}

func TestPostPlanAgentInputRequiresPersistedApprovedPlan(t *testing.T) {
	projectID, workItemID, sourceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	item := models.DeliveryWorkItem{
		ID: workItemID, ProjectID: projectID, State: deliveryworkflow.StateImplementation,
		IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`, AcceptanceJSON: `[]`, PlanJSON: `{}`,
	}
	project := models.DeliveryProject{ID: projectID}
	snapshots := []models.DeliveryContextSnapshot{{WorkItemID: workItemID, SourceID: sourceID, Kind: "repository", Name: "Backend", Reference: "workspace://backend", Revision: "abc123"}}
	if _, err := buildDeliveryAgentInput(item, project, snapshots, nil, nil, nil, nil, "", "implementation"); err == nil || !strings.Contains(err.Error(), "human-approved plan") {
		t.Fatalf("expected an implementation run without an approved plan to be rejected, got %v", err)
	}
}

func TestReadOnlyAssessmentRequiresAnExplicitZeroChangeMatrix(t *testing.T) {
	valid := map[string]any{"repository_impact": []any{map[string]any{"reference": "workspace://backend", "impact": "consulted"}}}
	if err := requireReadOnlyAssessmentPlan(valid); err != nil {
		t.Fatalf("expected bounded read-only assessment plan: %v", err)
	}
	githubSnapshot := map[string]any{"repository_impact": []any{map[string]any{"reference": "github://Itbem-Corp/itbem-events-backend", "impact": "consulted"}}}
	if err := requireReadOnlyAssessmentPlan(githubSnapshot); err != nil {
		t.Fatalf("expected immutable GitHub repository snapshot to be accepted: %v", err)
	}
	for _, plan := range []map[string]any{
		{},
		{"repository_impact": []any{}},
		{"repository_impact": []any{map[string]any{"reference": "workspace://backend", "impact": "changes"}}},
		{"repository_impact": []any{map[string]any{"reference": "workspace://backend", "impact": "unknown"}}},
	} {
		if err := requireReadOnlyAssessmentPlan(plan); err == nil {
			t.Fatalf("invalid read-only assessment plan was accepted: %#v", plan)
		}
	}
}

func TestSubmissionActionsRequireMatchingAgentOperation(t *testing.T) {
	checks := []struct {
		action    deliveryworkflow.Action
		operation string
		phase     string
	}{
		{deliveryworkflow.ActionSubmitPlan, "delivery.plan", "plan"},
		{deliveryworkflow.ActionSubmitCodeReview, "delivery.implementation", "implementation"},
		{deliveryworkflow.ActionSubmitQA, "delivery.qa", "qa"},
	}
	for _, check := range checks {
		operation, phase := agentOperationForSubmission(check.action)
		if operation != check.operation || phase != check.phase {
			t.Fatalf("unexpected submission mapping for %s", check.action)
		}
	}
	if operation, phase := agentOperationForSubmission(deliveryworkflow.ActionApprovePlan); operation != "" || phase != "" {
		t.Fatal("human approvals must not be treated as agent submissions")
	}
}

func TestValidPreviewURLRejectsNonWebAndMalformedValues(t *testing.T) {
	for _, value := range []string{"https://preview.example.com/task", "http://localhost:3000"} {
		if !validPreviewURL(value) {
			t.Fatalf("expected valid preview URL: %s", value)
		}
	}
	for _, value := range []string{"", "ftp://preview.example.com", "not a url"} {
		if validPreviewURL(value) {
			t.Fatalf("unexpected valid preview URL: %s", value)
		}
	}
}

func TestCodeReviewRecordSupportsRemotePRsOrIsolatedLocalWorktrees(t *testing.T) {
	for _, value := range []string{"https://github.com/itbem/repo/pull/1", "http://localhost:3000/pr/1"} {
		if !validWebURL(value) {
			t.Fatalf("expected valid code review URL: %s", value)
		}
	}
	for _, value := range []string{"", "git@github.com:itbem/repo.git", "ftp://example.com/pr"} {
		if validWebURL(value) {
			t.Fatalf("unexpected valid code review URL: %s", value)
		}
	}
	if !validCodeReviewRecord(models.DeliveryChangeSet{ReviewType: "local_worktree", RepositoryRef: "workspace://itbem-events-backend", Branch: "itbem-agent/123", CIStatus: "passed"}) {
		t.Fatal("expected isolated local worktree review to be valid")
	}
	if validCodeReviewRecord(models.DeliveryChangeSet{ReviewType: "local_worktree", RepositoryRef: "https://example.test/repo", Branch: "main", CIStatus: "passed"}) {
		t.Fatal("unexpected non-worktree local review")
	}
}

func TestPublicationGrantReviewBindingIsRepositorySpecificAndImmutable(t *testing.T) {
	baseSHA := strings.Repeat("a", 40)
	digest := strings.Repeat("b", 64)
	grant := models.DeliveryPublicationGrant{
		RepositoryRef: "workspace://backend", Branch: "itbem-agent/task", BaseSHA: baseSHA,
		GitHubRepository: "itbem-corp/backend", ReviewDiffSHA256: digest,
	}
	reviewed := models.DeliveryChangeSet{
		RepositoryRef: "workspace://backend", Branch: "itbem-agent/task", ReviewType: "local_worktree", CIStatus: "passed",
		MetadataJSON: `{"base_sha":"` + baseSHA + `","github_repository":"itbem-corp/backend","review_diff_sha256":"` + digest + `"}`,
	}
	if err := validatePublicationGrantReviewBinding(grant, reviewed); err != nil {
		t.Fatalf("exact reviewed grant should remain publishable: %v", err)
	}
	wrongRepository := grant
	wrongRepository.RepositoryRef = "workspace://dashboard"
	if err := validatePublicationGrantReviewBinding(wrongRepository, reviewed); err == nil {
		t.Fatal("a grant for another repository must never bind to this review")
	}
	wrongDigest := grant
	wrongDigest.ReviewDiffSHA256 = strings.Repeat("c", 64)
	if err := validatePublicationGrantReviewBinding(wrongDigest, reviewed); err == nil {
		t.Fatal("a grant with another reviewed diff must never publish")
	}
}
