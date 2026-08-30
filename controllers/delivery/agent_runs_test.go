package delivery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/releasegate"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"github.com/gofrs/uuid"
)

func TestAgentRunSpecsAreBoundToDeliveryStates(t *testing.T) {
	checks := []struct {
		phase     string
		operation string
		state     string
	}{
		{"plan", "delivery.plan", deliveryworkflow.StatePlanning},
		{"implementation", "delivery.implementation", deliveryworkflow.StateImplementation},
		{"publish", "delivery.publish", deliveryworkflow.StatePreviewPending},
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

func TestStoredReleaseGateCandidateUsesEveryExactPublishedRepositoryHead(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	item := models.DeliveryWorkItem{
		ID:       workItemID,
		PlanJSON: `{"repository_impact":[{"reference":"workspace://api","impact":"changes"},{"reference":"workspace://web","impact":"changes"}]}`,
	}
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
		"implementation": "before a publication grant",
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
