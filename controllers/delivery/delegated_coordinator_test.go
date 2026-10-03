package delivery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/deliverypolicy"
	"events-stocks/internal/qaevidence"
	"events-stocks/internal/releasegate"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"github.com/gofrs/uuid"
)

type coordinatorFixture struct {
	now          time.Time
	item         models.DeliveryWorkItem
	intent       models.DeliveryContinuation
	task         models.AutomationTask
	authority    deliveryledger.AutonomySnapshotInput
	candidate    releasegate.Input
	observation  qaevidence.Observation
	repositories map[string]deliveryledger.AutonomyRepository
	changes      []models.DeliveryChangeSet
	receipts     []models.AutomationQASourceReceipt
	review       models.AutomationCodeReviewPublication
}

func newCoordinatorFixture(t *testing.T) coordinatorFixture {
	t.Helper()
	f := coordinatorFixture{now: time.Now().UTC().Truncate(time.Microsecond)}
	itemID, projectID, intentID, taskID, instanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	branch := "itbem-agent/" + uuid.Must(uuid.NewV4()).String()
	head := strings.Repeat("a", 40)
	f.item = models.DeliveryWorkItem{ID: itemID, ProjectID: projectID, RequestedBy: "synthetic-owner", Title: "Synthetic bounded delivery", State: deliveryworkflow.StateQARunning, AutomationEpoch: 3, PreviewURL: "https://preview.example.test", PlanJSON: `{"repository_impact":[{"reference":"workspace://api","impact":"changes"}],"implementation_steps":["Correct the approved handler"],"acceptance_criteria":["The approved handler passes its test"]}`}
	var plan map[string]any
	if err := json.Unmarshal([]byte(f.item.PlanJSON), &plan); err != nil {
		t.Fatal(err)
	}
	plan["execution_steps"] = []any{
		map[string]any{"step_key": "correct", "role": "implementation", "order": 0, "title": "Correct handler", "objective": "Correct the approved handler", "acceptance_criteria": []string{"The approved handler passes its test"}, "depends_on": []string{}},
		map[string]any{"step_key": "integrate", "role": "integration", "order": 1, "title": "Verify handler", "objective": "Verify the approved handler", "acceptance_criteria": []string{"The approved handler passes its test"}, "depends_on": []string{"correct"}},
	}
	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	f.item.PlanJSON = string(encodedPlan)
	f.intent = models.DeliveryContinuation{ID: intentID, WorkItemID: itemID, Epoch: 3, Phase: "qa", RequestedBy: "synthetic-owner", Status: "dispatched", AvailableAt: f.now, CreatedAt: f.now.Add(-3 * time.Minute)}
	completed := f.now.Add(-time.Second)
	f.task = models.AutomationTask{ID: taskID, JobID: uuid.Must(uuid.NewV4()), Operation: "delivery.qa", Status: "completed", DeliveryWorkItemID: &itemID, ContinuationID: &intentID, AgentInstanceID: &instanceID, QASourceReceiptRequired: true, CreatedAt: f.now.Add(-3 * time.Minute), CompletedAt: &completed, OutputRef: "s3://synthetic-private/result.json"}
	f.candidate = releasegate.Input{SchemaVersion: releasegate.SchemaVersion, ChangeSetID: itemID.String(), Revisions: []releasegate.Revision{{Repository: "example/service", Branch: "main", SHA: head}}}
	matrix, err := releasegate.RevisionMatrixDigest(f.candidate.Revisions)
	if err != nil {
		t.Fatal(err)
	}
	f.task.EvidenceSubjectDigest = matrix
	mode, delegated, merge := deliverypolicy.ModeMerge, deliverypolicy.GateApprovalDelegated, "squash"
	kinds, branches := []string{"unit"}, []string{"main"}
	layer := deliverypolicy.Layer{SchemaVersion: 1, RevisionID: "synthetic-approved-policy", Level: deliverypolicy.LevelProject, OrganizationID: "synthetic-org", ProjectID: projectID.String(), Approved: true, ApprovedBy: "synthetic-owner", ApprovedAt: f.now.Add(-time.Hour), Patch: deliverypolicy.Patch{Mode: &mode, GateApprovalMode: &delegated, MergeMethod: &merge, RequiredTestKinds: &kinds, AllowedTargetBranches: &branches}}
	layer.Digest, err = deliverypolicy.LayerDigest(layer)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := deliverypolicy.Resolve(deliverypolicy.Context{OrganizationID: layer.OrganizationID, ProjectID: projectID.String(), Repository: "github://example/service", ChangeSetID: itemID.String()}, []deliverypolicy.Layer{layer}, f.now)
	if err != nil || !policy.Resolved {
		t.Fatalf("fixture policy: %v / %#v", err, policy)
	}
	repository := deliveryledger.AutonomyRepository{Repository: "github://example/service", SourceReference: "workspace://api", SourceRevision: strings.Repeat("b", 40), VaultRevisionID: uuid.Must(uuid.NewV4()).String(), VaultVersion: 1, VaultRevision: strings.Repeat("b", 40), VaultDigest: strings.Repeat("c", 64), Policy: policy}
	f.authority = deliveryledger.AutonomySnapshotInput{ProjectID: projectID, ChangeSetID: itemID.String(), Repositories: []deliveryledger.AutonomyRepository{repository}}
	f.repositories = map[string]deliveryledger.AutonomyRepository{"workspace://api": repository}
	f.observation = qaevidence.Observation{SchemaVersion: 2, TaskID: taskID.String(), MatrixDigest: matrix, PreviewPassed: true, RepositoryExecutionOrder: []string{"workspace://api"}, Repositories: []qaevidence.Repository{{Reference: "workspace://api", Branch: branch, Commands: []qaevidence.Command{{Index: 0, Phase: "validation", Kind: "unit", Passed: true}, {Index: 1, Phase: "qa", Kind: "security:secrets", Passed: true}, {Index: 2, Phase: "qa", Kind: "security:high-critical", Passed: true}}}}}
	diff := strings.Repeat("d", 64)
	metadata, _ := json.Marshal(map[string]any{"branch_published": true, "verification_source": "itbem-github-app", "remote_repository": "example/service", "target_branch": "main", "review_diff_sha256": diff})
	f.changes = []models.DeliveryChangeSet{{ID: uuid.Must(uuid.NewV4()), WorkItemID: itemID, RepositoryRef: "workspace://api", Branch: branch, CommitSHA: head, ReviewType: "pull_request", PullRequestURL: "https://github.com/example/service/pull/42", CIStatus: "passed", PreviewURL: f.item.PreviewURL, MetadataJSON: string(metadata), CreatedBy: "itbem-github-app", CreatedAt: f.now.Add(-2 * time.Minute)}, {ID: uuid.Must(uuid.NewV4()), WorkItemID: itemID, RepositoryRef: "workspace://api", Branch: branch, ReviewType: "local_worktree", CIStatus: "passed", CreatedBy: "itbem-local-agent", CreatedAt: f.now.Add(-3 * time.Minute), MetadataJSON: `{"verification_source":"itbem-local-agent","automation_task_id":"` + taskID.String() + `","worktree":"workspace://api#` + branch + `","review_diff_sha256":"` + diff + `"}`}}
	f.receipts = []models.AutomationQASourceReceipt{{ID: uuid.Must(uuid.NewV4()), TaskID: taskID, RunID: uuid.Must(uuid.NewV4()).String(), Reference: "workspace://api", AgentInstanceID: instanceID, MatrixDigest: matrix, Repository: "example/service", Branch: branch, CommitSHA: head, PackSHA256: strings.Repeat("e", 64), PackBytes: 100, AcquiredAt: f.now.Add(-time.Minute)}}
	checkID, checkName, checkConclusion := int64(123), "Bema Review / exact-sha", "success"
	f.review = models.AutomationCodeReviewPublication{ID: uuid.Must(uuid.NewV4()), AutomationTaskID: uuid.Must(uuid.NewV4()), Repository: "example/service", PullRequest: 42, HeadSHA: head, PatchSHA256: diff, SubjectSHA256: strings.Repeat("f", 64), PayloadSHA256: strings.Repeat("f", 64), Verdict: "approve", Event: "APPROVE", ReviewGatePassed: true, ReviewID: 456, ReviewURL: "https://github.com/example/service/pull/42#pullrequestreview-456", ReviewerActor: "synthetic-reviewer", AuthorActor: "synthetic-author", CheckRunID: &checkID, CheckName: &checkName, CheckConclusion: &checkConclusion, PublishedAt: f.now.Add(-time.Minute)}
	return f
}

func TestDelegatedQADecisionUsesObservedChecksAndFrozenCoverage(t *testing.T) {
	f := newCoordinatorFixture(t)
	if d, err := evaluateDelegatedQA(f.task, f.observation, f.candidate, f.repositories); err != nil || len(d.Failures) != 0 {
		t.Fatalf("valid observed QA rejected: %#v / %v", d, err)
	}
	f.observation.Repositories[0].Commands[0].Passed = false
	d, err := evaluateDelegatedQA(f.task, f.observation, f.candidate, f.repositories)
	if err != nil || len(d.Failures) != 1 || d.Failures[0] != "workspace://api/unit" {
		t.Fatalf("observed failure was lost: %#v / %v", d, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*coordinatorFixture)
	}{
		{"different task", func(f *coordinatorFixture) { f.observation.TaskID = uuid.Must(uuid.NewV4()).String() }},
		{"changed commit", func(f *coordinatorFixture) { f.candidate.Revisions[0].SHA = strings.Repeat("b", 40) }},
		{"unbound task", func(f *coordinatorFixture) { f.task.EvidenceSubjectDigest = strings.Repeat("f", 64) }},
		{"failed preview", func(f *coordinatorFixture) { f.observation.PreviewPassed = false }},
		{"untyped failed command", func(f *coordinatorFixture) {
			f.observation.Repositories[0].Commands = append(f.observation.Repositories[0].Commands, qaevidence.Command{Index: 3, Phase: "qa", Passed: false})
		}},
		{"missing scanner", func(f *coordinatorFixture) {
			f.observation.Repositories[0].Commands = f.observation.Repositories[0].Commands[:2]
		}},
		{"security failure", func(f *coordinatorFixture) { f.observation.Repositories[0].Commands[1].Passed = false }},
		{"additional security failure", func(f *coordinatorFixture) {
			f.observation.Repositories[0].Commands = append(f.observation.Repositories[0].Commands, qaevidence.Command{Index: 3, Phase: "qa", Kind: "security:dependencies", Passed: false})
		}},
		{"missing unit", func(f *coordinatorFixture) {
			f.observation.Repositories[0].Commands = f.observation.Repositories[0].Commands[1:]
		}},
		{"human policy", func(f *coordinatorFixture) {
			r := f.repositories["workspace://api"]
			r.Policy.GateApprovalMode = deliverypolicy.GateApprovalHuman
			f.repositories["workspace://api"] = r
		}},
		{"force merge", func(f *coordinatorFixture) {
			r := f.repositories["workspace://api"]
			r.Policy.Safety.ForceMergeAllowed = true
			f.repositories["workspace://api"] = r
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCoordinatorFixture(t)
			test.mutate(&f)
			if _, err := evaluateDelegatedQA(f.task, f.observation, f.candidate, f.repositories); err == nil {
				t.Fatal("unqualified evidence advanced QA")
			}
		})
	}
}

func TestDelegatedQASourceReceiptRequiresExactCheckoutAndIndependentIdentity(t *testing.T) {
	f := newCoordinatorFixture(t)
	if err := validateDelegatedQASourceReceipts(f.task, f.observation, f.candidate, f.repositories, f.receipts, f.changes); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*coordinatorFixture)
	}{
		{"no receipt", func(f *coordinatorFixture) { f.receipts = nil }},
		{"other worker", func(f *coordinatorFixture) { f.receipts[0].AgentInstanceID = uuid.Must(uuid.NewV4()) }},
		{"old source", func(f *coordinatorFixture) { f.receipts[0].CommitSHA = strings.Repeat("b", 40) }},
		{"old task", func(f *coordinatorFixture) { f.receipts[0].TaskID = uuid.Must(uuid.NewV4()) }},
		{"old matrix", func(f *coordinatorFixture) { f.receipts[0].MatrixDigest = strings.Repeat("b", 64) }},
		{"wrong branch", func(f *coordinatorFixture) {
			f.observation.Repositories[0].Branch = "itbem-agent/" + uuid.Must(uuid.NewV4()).String()
		}},
		{"wrong origin", func(f *coordinatorFixture) { f.receipts[0].Repository = "other/service" }},
		{"late receipt", func(f *coordinatorFixture) { f.receipts[0].AcquiredAt = f.now.Add(time.Hour) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCoordinatorFixture(t)
			test.mutate(&f)
			if err := validateDelegatedQASourceReceipts(f.task, f.observation, f.candidate, f.repositories, f.receipts, f.changes); err == nil {
				t.Fatal("unbound source receipt accepted")
			}
		})
	}
}

func TestDelegatedQAIndependentReviewCannotComeFromAnotherPRSHAOrAuthor(t *testing.T) {
	f := newCoordinatorFixture(t)
	if !validDelegatedIndependentReview(f.review, f.candidate.Revisions[0], 42, f.now) {
		t.Fatal("valid independent review rejected")
	}
	for _, mutate := range []func(*models.AutomationCodeReviewPublication){
		func(r *models.AutomationCodeReviewPublication) { r.HeadSHA = strings.Repeat("b", 40) },
		func(r *models.AutomationCodeReviewPublication) { r.PullRequest = 43 },
		func(r *models.AutomationCodeReviewPublication) { r.AuthorActor = r.ReviewerActor },
		func(r *models.AutomationCodeReviewPublication) { r.ReviewGatePassed = false },
		func(r *models.AutomationCodeReviewPublication) { r.CheckName = nil },
		func(r *models.AutomationCodeReviewPublication) { r.CheckConclusion = nil },
	} {
		f := newCoordinatorFixture(t)
		mutate(&f.review)
		if validDelegatedIndependentReview(f.review, f.candidate.Revisions[0], 42, f.now) {
			t.Fatal("unqualified independent review accepted")
		}
	}
}

func TestDelegatedCorrectionBudgetStopsUnchangedFailureAndExhaustion(t *testing.T) {
	d := delegatedQADecision{SchemaVersion: 1, MatrixDigest: strings.Repeat("a", 64), Failures: []string{"workspace://api/unit"}}
	if err := validateDelegatedCorrectionBudget(d, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	gate := models.DeliveryGate{Kind: deliveryworkflow.GateQAReview, Decision: deliveryworkflow.DecisionChangesRequested, Comment: string(raw)}
	if err := validateDelegatedCorrectionBudget(d, []models.DeliveryGate{gate}); err == nil {
		t.Fatal("unchanged revision retried")
	}
	if err := validateDelegatedCorrectionBudget(d, []models.DeliveryGate{{Kind: deliveryworkflow.GateCodeReview}, gate}); err == nil {
		t.Fatal("intervening code gate hid the unchanged failed QA revision")
	}
	d.MatrixDigest = strings.Repeat("b", 64)
	if err := validateDelegatedCorrectionBudget(d, []models.DeliveryGate{gate}); err != nil {
		t.Fatal("changed code should permit a bounded correction", err)
	}
	if err := validateDelegatedCorrectionBudget(d, []models.DeliveryGate{gate, gate, gate}); err == nil {
		t.Fatal("fourth correction admitted")
	}
}

func TestDelegatedQARepositoryBoundariesCannotExpandFrozenScope(t *testing.T) {
	f := newCoordinatorFixture(t)
	if repositories, err := delegatedQARepositoryBoundaries(f.item.PlanJSON, f.authority, f.candidate, f.changes); err != nil || len(repositories) != 1 {
		t.Fatalf("valid frozen scope rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*coordinatorFixture)
	}{
		{"unapproved target", func(f *coordinatorFixture) { f.candidate.Revisions[0].Branch = "production" }},
		{"different repository", func(f *coordinatorFixture) { f.candidate.Revisions[0].Repository = "example/other" }},
		{"read-only policy", func(f *coordinatorFixture) { f.authority.Repositories[0].Policy.Mode = deliverypolicy.ModeReviewOnly }},
		{"missing publication", func(f *coordinatorFixture) { f.changes = nil }},
		{"unfrozen source", func(f *coordinatorFixture) { f.authority.Repositories[0].SourceReference = "workspace://other" }},
		{"expanded changed scope", func(f *coordinatorFixture) {
			f.item.PlanJSON = `{"repository_impact":[{"reference":"workspace://api","impact":"changes"},{"reference":"workspace://other","impact":"changes"}]}`
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCoordinatorFixture(t)
			test.mutate(&f)
			if _, err := delegatedQARepositoryBoundaries(f.item.PlanJSON, f.authority, f.candidate, f.changes); err == nil {
				t.Fatal("expanded authority was accepted")
			}
		})
	}
}
