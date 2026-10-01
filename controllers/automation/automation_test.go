package automation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/environmentevidence"
	"events-stocks/internal/projectvault"
	"events-stocks/internal/qaevidence"
	"events-stocks/internal/releasegate"
	"events-stocks/models"
	"events-stocks/services/deliveryplansteps"
	"events-stocks/services/deliveryworkflow"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	automationqueuerepository "events-stocks/repositories/automationqueuerepository"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOnboardingCapabilityProbeForTaskBindsTaskAndProposalSubject(t *testing.T) {
	taskID, onboardingID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	revision, evidenceDigest := strings.Repeat("a", 40), strings.Repeat("b", 64)
	probe := projectvault.CapabilityProbe{Name: "unit", State: "ready", Reason: "operator-owned capability command exited zero at the exact repository SHA", Revision: revision, EvidenceSHA256: evidenceDigest, ExecutorRole: "qa"}
	probe.SubjectSHA256, _ = projectvault.CapabilityProbeSubjectSHA256(projectvault.Repository{Reference: "github://acme/service", Revision: revision}, probe)
	execution := automationagent.OnboardingProbeExecution{SchemaVersion: 1, TaskID: taskID.String(), RepositoryReference: "github://acme/service", DefaultBranch: "main", Revision: revision, WorkspaceReference: "workspace://service", ExecutorRole: "qa", Probes: []projectvault.CapabilityProbe{probe}}
	raw, _ := json.Marshal(execution)
	task := models.AutomationTask{ID: taskID, Operation: "delivery.onboarding_probe", DeliveryOnboardingID: &onboardingID, EvidenceSubjectDigest: strings.Repeat("c", 64)}
	actual, subject, err := onboardingCapabilityProbeForTask(&task, raw)
	if err != nil || actual.TaskID != taskID.String() || subject != task.EvidenceSubjectDigest {
		t.Fatalf("exact onboarding probe task rejected: %#v / %q / %v", actual, subject, err)
	}
	for _, mutate := range []func(*models.AutomationTask, *automationagent.OnboardingProbeExecution){
		func(task *models.AutomationTask, _ *automationagent.OnboardingProbeExecution) {
			task.Operation = "delivery.qa"
		},
		func(task *models.AutomationTask, _ *automationagent.OnboardingProbeExecution) {
			task.EvidenceSubjectDigest = "invalid"
		},
		func(_ *models.AutomationTask, execution *automationagent.OnboardingProbeExecution) {
			execution.TaskID = uuid.Must(uuid.NewV4()).String()
		},
	} {
		candidateTask, candidateExecution := task, execution
		mutate(&candidateTask, &candidateExecution)
		candidateRaw, _ := json.Marshal(candidateExecution)
		if _, _, err := onboardingCapabilityProbeForTask(&candidateTask, candidateRaw); err == nil {
			t.Fatal("onboarding probe outside its queued subject was accepted")
		}
	}
}

func TestAutomationLeaseRetryAfterSecondsRoundsUpWithoutInventingDelay(t *testing.T) {
	now := time.Date(2026, time.September, 7, 3, 30, 0, 500_000_000, time.UTC)
	if got := automationLeaseRetryAfterSeconds(now.Add(119*time.Second+time.Nanosecond), now); got != 120 {
		t.Fatalf("retry seconds = %d, want 120", got)
	}
	if got := automationLeaseRetryAfterSeconds(now, now); got != 0 {
		t.Fatalf("expired lease retry seconds = %d, want 0", got)
	}
}

func TestReleaseGateCandidateForTaskRequiresAuthenticatedRequester(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	input := releasegate.Input{
		SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: workItemID.String(),
		Revisions: []releasegate.Revision{{Repository: "example/service", Branch: "feature/release", SHA: strings.Repeat("a", 40)}},
		Policy:    releasegate.Policy{Resolved: false, RequiredTestKinds: []string{}},
	}
	digest, _ := releasegate.RevisionMatrixDigest(input.Revisions)
	taskID := uuid.Must(uuid.NewV4())
	environment := environmentevidence.Observation{SchemaVersion: environmentevidence.SchemaVersion, TaskID: taskID.String(), MatrixDigest: digest, Repositories: []environmentevidence.Repository{{Repository: "example/service", HeadSHA: strings.Repeat("a", 40), Workflow: ".github/workflows/deploy.yml", Environment: "production", RequiredSecretReferences: []string{}, RequiredVariableReferences: []string{}, WorkflowExists: true, EnvironmentExists: true, MissingSecretReferences: []string{}, MissingVariableReferences: []string{}}}}
	raw, _ := json.Marshal(map[string]any{"schema_version": 2, "gatekeeper_input": input, "environment_observation": environment})
	task := &models.AutomationTask{ID: taskID, Operation: "delivery.release_gate", DeliveryWorkItemID: &workItemID, RequestedBy: "cognito-human-42", EvidenceSubjectDigest: digest}
	actualWorkItemID, actor, actual, actualEnvironment, err := releaseGateCandidateForTask(task, raw)
	if err != nil || actualWorkItemID != workItemID || actor != task.RequestedBy || actual.HumanApproval != nil {
		t.Fatalf("release Gatekeeper callback did not preserve its human requester boundary: %s / %s / %#v / %#v / %v", actualWorkItemID, actor, actual, actualEnvironment, err)
	}
	legacyRaw, _ := json.Marshal(map[string]any{"schema_version": 1, "gatekeeper_input": input})
	_, _, _, legacyEnvironment, err := releaseGateCandidateForTask(task, legacyRaw)
	if err != nil || legacyEnvironment != nil {
		t.Fatalf("legacy rolling-upgrade callback should stay environment-blocked: %#v / %v", legacyEnvironment, err)
	}

	for _, invalidActor := range []string{"", "github-app-review", "itbem-local-agent", "itbem-github-app"} {
		copy := *task
		copy.RequestedBy = invalidActor
		if _, _, _, _, err := releaseGateCandidateForTask(&copy, raw); err == nil {
			t.Fatalf("technical requester %q was accepted as a human", invalidActor)
		}
	}
}

func TestSecurityObservationPromotesOnlyCompleteOperatorNamedQACommands(t *testing.T) {
	observation := qaevidence.Observation{
		SchemaVersion: qaevidence.SchemaVersion, TaskID: "11111111-1111-4111-8111-111111111111", MatrixDigest: strings.Repeat("a", 64),
		RepositoryExecutionOrder: []string{"workspace://api", "workspace://web"},
		Repositories: []qaevidence.Repository{
			{Reference: "workspace://api", Branch: "itbem-agent/11111111-1111-4111-8111-111111111111", Commands: []qaevidence.Command{{Index: 0, Phase: "qa", Kind: securitySecretsTestKind, Passed: true}, {Index: 1, Phase: "qa", Kind: securityHighCriticalTestKind, Passed: true}}},
			{Reference: "workspace://web", Branch: "itbem-agent/22222222-2222-4222-8222-222222222222", Commands: []qaevidence.Command{{Index: 0, Phase: "qa", Kind: securitySecretsTestKind, Passed: false}, {Index: 1, Phase: "qa", Kind: securityHighCriticalTestKind, Passed: false}}},
		},
	}
	security, complete, err := securityObservationFromQA(observation)
	if err != nil || !complete || len(security.Repositories) != 2 || !security.Repositories[0].SecretScanPassed || security.Repositories[1].SecretScanPassed || security.Repositories[1].HighFindings != 1 {
		t.Fatalf("complete local security scans were not promoted: %#v / %t / %v", security, complete, err)
	}
	missing := observation
	missing.Repositories = append([]qaevidence.Repository(nil), observation.Repositories...)
	missing.Repositories[0].Commands = missing.Repositories[0].Commands[:1]
	if _, complete, err := securityObservationFromQA(missing); err != nil || complete {
		t.Fatalf("missing local security scanner became gate evidence: %t / %v", complete, err)
	}
}

func TestQAObservationForTaskRequiresExactQueuedMatrix(t *testing.T) {
	workItemID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	digest := strings.Repeat("a", 64)
	task := &models.AutomationTask{ID: taskID, Operation: "delivery.qa", DeliveryWorkItemID: &workItemID, EvidenceSubjectDigest: digest}
	observation := qaevidence.Observation{
		SchemaVersion: qaevidence.SchemaVersion, TaskID: taskID.String(), MatrixDigest: digest, PreviewPassed: true,
		RepositoryExecutionOrder: []string{"workspace://api"},
		Repositories:             []qaevidence.Repository{{Reference: "workspace://api", Branch: "itbem-agent/11111111-1111-4111-8111-111111111111", Commands: []qaevidence.Command{}}},
	}
	raw, _ := json.Marshal(observation)
	actualWorkItemID, actual, err := qaObservationForTask(task, raw)
	if err != nil || actualWorkItemID != workItemID || actual.MatrixDigest != digest {
		t.Fatalf("exact QA observation was rejected: %#v / %v", actual, err)
	}
	for _, mutate := range []func(*models.AutomationTask, *qaevidence.Observation){
		func(task *models.AutomationTask, _ *qaevidence.Observation) {
			task.EvidenceSubjectDigest = strings.Repeat("b", 64)
		},
		func(_ *models.AutomationTask, observation *qaevidence.Observation) {
			observation.TaskID = uuid.Must(uuid.NewV4()).String()
		},
		func(task *models.AutomationTask, _ *qaevidence.Observation) { task.Operation = "delivery.release_gate" },
	} {
		candidateTask, candidateObservation := *task, observation
		mutate(&candidateTask, &candidateObservation)
		candidateRaw, _ := json.Marshal(candidateObservation)
		if _, _, err := qaObservationForTask(&candidateTask, candidateRaw); err == nil {
			t.Fatal("QA observation outside its task subject was accepted")
		}
	}
}

func TestGitHubReviewWebhookAdmissionIsExplicitAndSignatureBound(t *testing.T) {
	cfg := &models.Config{GitHubReviewWebhookSecret: "webhook-secret", GitHubReviewRepositories: "itbem/backend, ITBEM/dashboard"}
	if !githubReviewWebhookConfigured(cfg) || !githubReviewRepositoryAllowed(cfg.GitHubReviewRepositories, "itbem/dashboard") || githubReviewRepositoryAllowed(cfg.GitHubReviewRepositories, "other/repo") {
		t.Fatal("review webhook must require an explicit normalized repository allow-list")
	}
	if !githubReviewRepositoryAllowed("itbem/*", "itbem/new-repository") || githubReviewRepositoryAllowed("itbem/*", "other/repository") {
		t.Fatal("organization wildcard must remain scoped to its explicitly allowed owner")
	}
	body := []byte(`{"action":"synchronize","number":42}`)
	mac := hmac.New(sha256.New, []byte(cfg.GitHubReviewWebhookSecret))
	_, _ = mac.Write(body)
	signature := "sha256=" + fmt.Sprintf("%x", mac.Sum(nil))
	if !validGitHubWebhookSignature(body, signature, cfg.GitHubReviewWebhookSecret) || validGitHubWebhookSignature(append(body, 'x'), signature, cfg.GitHubReviewWebhookSecret) {
		t.Fatal("GitHub webhook signature must bind the exact raw body")
	}
	if !githubReviewActionAllowed("synchronize") || githubReviewActionAllowed("closed") {
		t.Fatal("only safe active pull-request actions may enqueue a new review")
	}
	if got := githubReviewWebhookIgnoreReason(githubPullRequestWebhook{Action: "closed"}, cfg); got != "ineligible_pull_request" {
		t.Fatalf("closed pull request ignore status = %q", got)
	}
	eligible := githubPullRequestWebhook{Action: "synchronize", Number: 42}
	eligible.Installation.ID = 99
	eligible.Repository.FullName = "itbem/backend"
	eligible.PullRequest.Base.SHA = strings.Repeat("a", 40)
	eligible.PullRequest.Head.SHA = strings.Repeat("b", 40)
	if got := githubReviewWebhookIgnoreReason(eligible, cfg); got != "" {
		t.Fatalf("eligible pull request was ignored as %q", got)
	}
	if !githubReviewWebhookPing(" ping ", []byte(`{"zen":"Keep it logically awesome."}`)) {
		t.Fatal("a valid signed GitHub ping envelope must complete the webhook handshake without queueing work")
	}
	if githubReviewWebhookPing("pull_request", []byte(`{}`)) || githubReviewWebhookPing("ping", []byte(`{} {}`)) {
		t.Fatal("only one valid JSON ping envelope may complete the webhook handshake")
	}
	var decoded githubPullRequestWebhook
	decoder := json.NewDecoder(strings.NewReader(`{"action":"synchronize"} {}`))
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		// The handler must reject this condition; this assertion proves the
		// decoder sees the appended second JSON value rather than ignoring it.
	} else {
		t.Fatal("concatenated GitHub webhook JSON must remain detectable")
	}
}

func TestCodeReviewPublicationForTaskRequiresExactIndependentGitHubEvidence(t *testing.T) {
	taskID := uuid.Must(uuid.NewV4())
	subject := strings.Repeat("a", 64)
	correlationID, err := githubReviewCorrelationID("itbem/backend", 42, strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	task := &models.AutomationTask{
		ID: taskID, RequestedBy: "github-app-review", Operation: "code.review", EvidenceSubjectDigest: subject,
		CorrelationID: correlationID,
	}
	newExecution := func() automationagent.GitHubCodeReviewPublication {
		return automationagent.GitHubCodeReviewPublication{
			SchemaVersion: 2, Repository: "itbem/backend", PullRequest: 42, HeadSHA: strings.Repeat("b", 40),
			PatchSHA256: strings.Repeat("c", 64), SubjectSHA256: subject, PayloadSHA256: strings.Repeat("d", 64),
			Verdict: "approve", Event: "APPROVE", ReviewGatePassed: true, ReviewID: 77,
			ReviewURL:     "https://github.com/itbem/backend/pull/42#pullrequestreview-77",
			ReviewerActor: "reviewer-bot[bot]", AuthorActor: "engineer-bot[bot]", PublishedAt: time.Now().UTC(),
			CheckRunID: 88, CheckRunURL: "https://github.com/itbem/backend/runs/88", CheckName: "Bema Review / exact-sha", CheckConclusion: "success",
		}
	}
	execution := newExecution()
	raw, _ := json.Marshal(execution)
	publication, err := codeReviewPublicationForTask(task, raw)
	if err != nil || publication.AutomationTaskID != uuid.Nil || publication.ReviewerActor != "reviewer-bot[bot]" {
		t.Fatalf("valid review publication rejected: %#v / %v", publication, err)
	}
	nonBlockingComment := newExecution()
	nonBlockingComment.Verdict, nonBlockingComment.Event = "comment", "COMMENT"
	nonBlockingComment.ReviewGatePassed, nonBlockingComment.CheckConclusion = true, "success"
	raw, _ = json.Marshal(nonBlockingComment)
	commentPublication, err := codeReviewPublicationForTask(task, raw)
	if err != nil || !commentPublication.ReviewGatePassed || commentPublication.Verdict != "comment" || commentPublication.Event != "COMMENT" || commentPublication.CheckConclusion == nil || *commentPublication.CheckConclusion != "success" {
		t.Fatalf("independent non-blocking exact-SHA comment was not persisted explicitly: %#v / %v", commentPublication, err)
	}
	missingGateClassification := newExecution()
	missingGateClassification.Verdict, missingGateClassification.Event = "comment", "COMMENT"
	missingGateClassification.ReviewGatePassed, missingGateClassification.CheckConclusion = false, "success"
	raw, _ = json.Marshal(missingGateClassification)
	if _, err := codeReviewPublicationForTask(task, raw); err == nil {
		t.Fatal("a successful comment without the reviewer gate classification was accepted")
	}
	contradictoryNonBlockingComment := newExecution()
	contradictoryNonBlockingComment.Verdict, contradictoryNonBlockingComment.Event = "comment", "COMMENT"
	contradictoryNonBlockingComment.ReviewGatePassed, contradictoryNonBlockingComment.CheckConclusion = true, "failure"
	raw, _ = json.Marshal(contradictoryNonBlockingComment)
	if _, err := codeReviewPublicationForTask(task, raw); err == nil {
		t.Fatal("a non-blocking reviewer classification with a failed GitHub check was accepted")
	}
	requestedChanges := newExecution()
	requestedChanges.Verdict, requestedChanges.Event, requestedChanges.ReviewGatePassed, requestedChanges.CheckConclusion = "request_changes", "REQUEST_CHANGES", false, "failure"
	raw, _ = json.Marshal(requestedChanges)
	if _, err := codeReviewPublicationForTask(task, raw); err != nil {
		t.Fatalf("a failed exact-SHA request-changes review was rejected: %v", err)
	}
	requestedChanges.ReviewGatePassed = true
	raw, _ = json.Marshal(requestedChanges)
	if _, err := codeReviewPublicationForTask(task, raw); err == nil {
		t.Fatal("a request-changes review with a passing gate classification was accepted")
	}
	for name, mutate := range map[string]func(*models.AutomationTask, *automationagent.GitHubCodeReviewPublication){
		"stale subject": func(_ *models.AutomationTask, value *automationagent.GitHubCodeReviewPublication) {
			value.SubjectSHA256 = strings.Repeat("e", 64)
		},
		"self approval": func(_ *models.AutomationTask, value *automationagent.GitHubCodeReviewPublication) {
			value.AuthorActor = value.ReviewerActor
		},
		"approval missing gate classification": func(_ *models.AutomationTask, value *automationagent.GitHubCodeReviewPublication) {
			value.ReviewGatePassed = false
		},
		"wrong PR": func(_ *models.AutomationTask, value *automationagent.GitHubCodeReviewPublication) {
			value.PullRequest = 43
		},
		"wrong operation": func(value *models.AutomationTask, _ *automationagent.GitHubCodeReviewPublication) {
			value.Operation = "delivery.qa"
		},
		"contradictory check": func(_ *models.AutomationTask, value *automationagent.GitHubCodeReviewPublication) {
			value.CheckConclusion = "failure"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidateTask, candidateExecution := *task, newExecution()
			mutate(&candidateTask, &candidateExecution)
			candidateRaw, _ := json.Marshal(candidateExecution)
			if _, err := codeReviewPublicationForTask(&candidateTask, candidateRaw); err == nil {
				t.Fatal("invalid GitHub review publication evidence was accepted")
			}
		})
	}
}

func TestAutomationTaskListViewNeverSerializesPrivateTaskFields(t *testing.T) {
	view := automationTaskListView{
		ID: uuid.Must(uuid.NewV4()), Operation: "code.review", Status: "completed", Provider: "minimax", Model: "MiniMax-M3",
		AttemptCount: 1, ResultAvailable: true, HasError: false, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"input_ref", "output_ref", "error_message", "requested_by", "correlation_id", "provider_response_id", "evidence_subject_digest", "budget_reservation"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("broad task list leaked %q: %s", forbidden, raw)
		}
	}
	for _, required := range []string{`"result_available":true`, `"attempt_count":1`} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("safe task state lost %q: %s", required, raw)
		}
	}
}

func TestSupersedeQueuedGitHubReviewsTargetsOnlyOlderQueuedHeadsForTheSamePR(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	replacement := uuid.Must(uuid.NewV4())
	prefix, err := githubReviewCorrelationPrefix("itbem/backend", 42)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "automation_tasks"`).
		WithArgs(sqlmock.AnyArg(), 0, sqlmock.AnyArg(), "Superseded by a newer pull-request commit before review began", sqlmock.AnyArg(), "cancelled", sqlmock.AnyArg(), "code.review", "github-app-review", "queued", prefix+":%", "github-pr:itbem/backend:42:%", replacement).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := gormDB.Transaction(func(tx *gorm.DB) error {
		return supersedeQueuedGitHubReviews(tx, "itbem/backend", 42, replacement, time.Now().UTC())
	}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if err := supersedeQueuedGitHubReviews(nil, "itbem/backend", 42, replacement, time.Now().UTC()); err == nil {
		t.Fatal("supersession must reject an absent transaction")
	}
}

func TestPlanStepFanInWaitsForTerminalChildCallbackAfterStepCompletion(t *testing.T) {
	stepID := uuid.Must(uuid.NewV4())
	assignment := models.DeliveryPlanStepAssignment{
		ID: uuid.Must(uuid.NewV4()), DeliveryPlanStepID: stepID,
		ChildAutomationTaskID: uuid.Must(uuid.NewV4()), Status: models.DeliveryPlanStepAssignmentCompleted,
	}
	status, err := effectivePlanStepAssignmentStatus(assignment.Status, "running")
	if err != nil {
		t.Fatal(err)
	}
	assignment.Status = status
	decision := deliveryplansteps.AssessFanIn([]uuid.UUID{stepID}, []models.DeliveryPlanStepAssignment{assignment})
	if !decision.WaitingForChildren || decision.AggregationPending {
		t.Fatalf("fan-in must wait for the child's terminal output callback, got %#v", decision)
	}
	status, err = effectivePlanStepAssignmentStatus(models.DeliveryPlanStepAssignmentCompleted, "failed")
	if err != nil || status != models.DeliveryPlanStepAssignmentFailed {
		t.Fatalf("task-level failure must not be hidden by a prior step completion: status=%s err=%v", status, err)
	}
}

func TestPlanIntegrationOutputRequiresExactPassingFinalCriteria(t *testing.T) {
	criteria := []string{"first branch is integrated", "the complete change passes verification"}
	valid := []planIntegrationOutputCheck{{Criterion: criteria[0], Passed: true}, {Criterion: criteria[1], Passed: true}}
	if !validPlanIntegrationOutputChecks(criteria, valid) {
		t.Fatal("exact passing final-verification evidence was rejected")
	}
	tests := []struct {
		name   string
		checks []planIntegrationOutputCheck
	}{
		{name: "missing criterion", checks: valid[:1]},
		{name: "failed criterion", checks: []planIntegrationOutputCheck{{Criterion: criteria[0], Passed: true}, {Criterion: criteria[1], Passed: false}}},
		{name: "duplicate criterion", checks: []planIntegrationOutputCheck{{Criterion: criteria[0], Passed: true}, {Criterion: criteria[0], Passed: true}}},
		{name: "invented criterion", checks: []planIntegrationOutputCheck{{Criterion: criteria[0], Passed: true}, {Criterion: "not in the frozen plan", Passed: true}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if validPlanIntegrationOutputChecks(criteria, test.checks) {
				t.Fatalf("invalid final-verification evidence was accepted: %#v", test.checks)
			}
		})
	}
	if sameAutomationStrings(criteria, []string{criteria[1], criteria[0]}) {
		t.Fatal("reordered frozen criteria must not be treated as an exact result projection")
	}
}

func TestPlanFanInChangeSetReplayIsExactAndConflictsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name       string
		existingCI string
		wantError  bool
	}{
		{name: "exact replay", existingCI: "passed"},
		{name: "conflicting repository evidence", existingCI: "pending", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock := automationCostLedgerTestDB(t)
			change := models.DeliveryChangeSet{
				WorkItemID: uuid.Must(uuid.NewV4()), RepositoryRef: "workspace://backend", Branch: "agent/implementation",
				ReviewType: "local_worktree", CIStatus: "passed", Environment: "local",
				MetadataJSON: `{"review_diff_sha256":"` + strings.Repeat("a", 64) + `"}`, CreatedBy: "itbem-local-agent",
			}
			rows := sqlmock.NewRows([]string{"id", "work_item_id", "repository_ref", "branch", "review_type", "ci_status", "environment", "metadata_json", "created_by"}).
				AddRow(uuid.Must(uuid.NewV4()), change.WorkItemID, change.RepositoryRef, change.Branch, change.ReviewType, test.existingCI, change.Environment, change.MetadataJSON, change.CreatedBy)
			mock.ExpectQuery(`SELECT \* FROM "delivery_change_sets" WHERE work_item_id = \$1 AND repository_ref = \$2 AND branch = \$3 ORDER BY "delivery_change_sets"\."id" LIMIT \$4`).
				WithArgs(change.WorkItemID, change.RepositoryRef, change.Branch, 1).WillReturnRows(rows)
			err := persistVerifiedPlanFanInChangeSets(db, []models.DeliveryChangeSet{change}, time.Now().UTC())
			if (err != nil) != test.wantError {
				t.Fatalf("fan-in review change-set replay error = %v, wantError=%v", err, test.wantError)
			}
			if test.wantError && !strings.Contains(err.Error(), "conflicts") {
				t.Fatalf("conflicting immutable evidence returned an unclear error: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVerifiedPlanFanInCompletionReplayRequiresCommittedExactReceipt(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	childID, parentID, workItemID, executionID, planID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	runID, workerID, machineID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
	planHash := strings.Repeat("b", 64)
	identity := automationagent.AgentIdentity{WorkerID: workerID, AgentKey: "integrator", MachineID: machineID}
	child := models.AutomationTask{
		ID: childID, DeliveryWorkItemID: &workItemID, Operation: "delivery.implementation", Status: "completed", RunID: runID,
		WorkerID: workerID, AgentKey: identity.AgentKey, MachineID: machineID,
		OutputRef: "s3://outputs/automation/" + childID.String() + "/runs/" + runID + "/result.json",
	}
	cfg := &models.Config{AutomationOutputBucket: "outputs"}
	receipt := planIntegrationCallbackReceipt{
		ParentTaskID: parentID.String(), PlanID: planID.String(), PlanVersion: 4, PlanHash: planHash,
		StepID: stepID.String(), ChildTaskID: childID.String(), RunID: runID,
	}
	handoff, err := json.Marshal(map[string]any{"fan_in_receipt": receipt})
	if err != nil {
		t.Fatal(err)
	}
	request := callbackRequest{Status: "completed", RunID: runID, OutputRef: child.OutputRef, Execution: handoff}
	parentOutput := map[string]any{
		"task_id": parentID.String(), "integration_task_id": childID.String(), "run_id": runID,
		"integration_receipt": receipt, "execution": map[string]any{"fan_in_receipt": receipt},
		"fan_in": planIntegrationFanInProof{
			SchemaVersion: 1, ParentTaskID: parentID.String(), ExecutionID: executionID.String(), PlanID: planID.String(), PlanVersion: 4, PlanHash: planHash,
			IntegrationStepID: stepID.String(), IntegrationStepKey: "integrate", IntegrationTaskID: childID.String(), RunID: runID, FencingToken: 12,
			WorkerID: workerID, AgentKey: identity.AgentKey, MachineID: machineID,
		},
	}
	encodedParent, err := json.Marshal(parentOutput)
	if err != nil {
		t.Fatal(err)
	}
	previousObject := getPlanStepPatchObject
	getPlanStepPatchObject = func(_ context.Context, key, bucket string) (io.ReadCloser, error) {
		if key != "automation/"+parentID.String()+"/runs/"+runID+"/result.json" || bucket != cfg.AutomationOutputBucket {
			return nil, fmt.Errorf("unexpected parent result reference %s/%s", bucket, key)
		}
		return io.NopCloser(strings.NewReader(string(encodedParent))), nil
	}
	t.Cleanup(func() { getPlanStepPatchObject = previousObject })

	assignment := uuid.Must(uuid.NewV4())
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_assignments" WHERE child_automation_task_id = \$1 LIMIT \$2`).
		WithArgs(childID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "execution_id", "delivery_plan_step_id", "child_automation_task_id", "status"}).
		AddRow(assignment, executionID, stepID, childID, models.DeliveryPlanStepAssignmentCompleted))
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_executions" WHERE id = \$1 LIMIT \$2`).
		WithArgs(executionID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id", "plan_id", "plan_version", "plan_hash", "status"}).
		AddRow(executionID, parentID, planID, 4, planHash, models.DeliveryPlanExecutionCompleted))
	mock.ExpectQuery(`SELECT \* FROM "automation_tasks" WHERE id = \$1 LIMIT \$2`).
		WithArgs(parentID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "delivery_work_item_id", "operation", "status", "output_ref"}).
		AddRow(parentID, workItemID, "delivery.implementation", "completed", "s3://outputs/automation/"+parentID.String()+"/runs/"+runID+"/result.json"))
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE id = \$1 AND plan_id = \$2 LIMIT \$3`).
		WithArgs(stepID, planID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id", "step_key", "role", "automation_task_id", "run_id", "lease_fence", "worker_id", "agent_key", "machine_id"}).
		AddRow(stepID, planID, "integrate", models.DeliveryPlanStepRoleIntegration, childID, runID, 12, workerID, identity.AgentKey, machineID))

	got, err := verifiedPlanFanInCompletionReplay(db, cfg, child, request, runID, identity)
	if err != nil || !got {
		t.Fatalf("exact committed fan-in replay was not acknowledged: got=%v err=%v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChildStepQueueEnvelopeBindsStepProfileAndSharedPrivateInput(t *testing.T) {
	workItemID, projectID, taskID, jobID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	inputRef := "s3://private-inputs/automation/inputs/parent/input.json"
	item := models.DeliveryWorkItem{ID: workItemID, ProjectID: projectID}
	child := models.AutomationTask{ID: taskID, JobID: jobID, CorrelationID: "corr-fanout", Operation: "delivery.implementation", InputRef: inputRef, MaxCompletionTokens: 700}
	step := deliveryplansteps.StepDTO{ID: stepID.String(), AgentKey: "backend_engineer"}
	message := planStepChildQueueMessage(item, child, step)
	if message.JobID != jobID.String() || message.Payload.TaskID != taskID.String() || message.Payload.ProjectID != projectID.String() {
		t.Fatalf("child queue envelope lost its scoped identity: %#v", message)
	}
	if message.Payload.PlanStepID != stepID.String() || message.Payload.AgentKey != step.AgentKey || message.Payload.InputRef != inputRef {
		t.Fatalf("child queue envelope must target one step/profile and reuse only the private input reference: %#v", message.Payload)
	}
}

func TestPlanAggregationPendingMessageIsAuditableAndIdempotent(t *testing.T) {
	workItemID, executionID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	item := models.DeliveryWorkItem{ID: workItemID}
	execution := models.DeliveryPlanExecution{ID: executionID}
	first := planAggregationPendingMessage(item, execution, "aggregation_pending: merge evidence is absent", now)
	replay := planAggregationPendingMessage(item, execution, "aggregation_pending: merge evidence is absent", now.Add(time.Minute))
	if first.ID == uuid.Nil || first.ID != replay.ID || first.WorkItemID != workItemID || first.Phase != "implementation" {
		t.Fatalf("aggregation-pending audit event must be deterministic for one execution: %#v %#v", first, replay)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(first.ReceiptJSON), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["status"] != "aggregation_pending" || receipt["required_artifact"] != "verified_parent_workspace_merge_and_full_plan_acceptance_receipt" {
		t.Fatalf("audit receipt must name the missing verified artifact: %#v", receipt)
	}
	if first.Effect != "workflow_observation" || first.AuthorType != "agent" {
		t.Fatalf("aggregation pending is an audit observation, not a human decision: %#v", first)
	}
}

func TestChildClaimsRenewOnlyTheSingleParentAggregateBudgetHold(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	childID, parentID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expiresAt, now := time.Date(2026, 9, 23, 13, 0, 0, 0, time.UTC), time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .*parent_task_id.*delivery_plan_step_assignments.*`).
		WillReturnRows(sqlmock.NewRows([]string{"parent_task_id"}).AddRow(parentID))
	mock.ExpectExec(`UPDATE "automation_tasks"`).
		WithArgs(expiresAt, now, parentID, "queued").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := db.Transaction(func(tx *gorm.DB) error {
		return renewPlanExecutionParentReservationInTransaction(tx, childID, expiresAt, now)
	}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRetryCodeReviewIsNarrowAndPreservesTheFrozenInputBoundary(t *testing.T) {
	digest := strings.Repeat("a", 64)
	failed := &models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), RequestedBy: "github-app-review", CorrelationID: "github-pr:subject:head", Operation: "code.review", Status: "failed", EvidenceSubjectDigest: digest, MaxCompletionTokens: 4096, InputRef: "s3://itbem-ai-inputs-local/automation/inputs/original/input.json"}
	if !retryableCodeReviewTask(failed) {
		t.Fatal("a failed frozen code review must be retryable")
	}
	retry, err := newCodeReviewRetryTask(failed)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID == failed.ID || retry.JobID == failed.JobID || retry.Status != "queued" || retry.InputRef != failed.InputRef || retry.EvidenceSubjectDigest != digest || retry.CorrelationID != failed.CorrelationID || retry.RequestedBy != failed.RequestedBy || retry.MaxCompletionTokens != failed.MaxCompletionTokens {
		t.Fatalf("retry did not preserve the immutable review boundary: %#v", retry)
	}
	message := codeReviewRetryQueueMessage(failed, retry)
	if message.SchemaVersion != 1 || message.JobID != retry.JobID.String() || message.Payload.TaskID != retry.ID.String() || message.Payload.RetryOfTaskID != failed.ID.String() || message.Payload.InputRef != failed.InputRef || message.Payload.Operation != "code.review" || message.Payload.Attempt != 1 {
		t.Fatalf("retry queue message did not preserve and bind the failed review: %#v", message)
	}
	for _, task := range []*models.AutomationTask{
		{Operation: "code.review", Status: "completed", InputRef: failed.InputRef, EvidenceSubjectDigest: digest},
		{Operation: "ai.chat", Status: "failed", InputRef: failed.InputRef, EvidenceSubjectDigest: digest},
		{Operation: "code.review", Status: "failed", EvidenceSubjectDigest: digest},
		{Operation: "code.review", Status: "failed", InputRef: failed.InputRef, EvidenceSubjectDigest: "invalid"},
	} {
		if retryableCodeReviewTask(task) {
			t.Fatalf("unexpected retry eligibility: %#v", task)
		}
	}
}

func TestStrandedGitHubReviewRecoveryPreservesOnlyAnUnclaimedImmutableBoundary(t *testing.T) {
	now := time.Now().UTC()
	digest := strings.Repeat("a", 64)
	original := &models.AutomationTask{
		ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), RequestedBy: "github-app-review",
		CorrelationID: "github-pr:subject:head", Operation: "code.review", Status: "queued", EvidenceSubjectDigest: digest,
		MaxCompletionTokens: 4096, InputRef: "s3://itbem-ai-inputs-local/automation/inputs/original/input.json",
		CreatedAt: now.Add(-githubReviewRecoveryDelay),
	}
	if !recoverableQueuedGitHubReview(original, now) {
		t.Fatal("an aged, unclaimed GitHub review should be recoverable")
	}
	recovery, err := newStrandedGitHubReviewRecovery(original, now)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.ID == original.ID || recovery.JobID == original.JobID || recovery.Status != "queued" || recovery.InputRef != original.InputRef || recovery.EvidenceSubjectDigest != digest || recovery.CorrelationID != original.CorrelationID || recovery.RequestedBy != original.RequestedBy || recovery.MaxCompletionTokens != original.MaxCompletionTokens {
		t.Fatalf("recovery did not preserve the immutable review boundary: %#v", recovery)
	}
	for _, mutate := range []func(*models.AutomationTask){
		func(task *models.AutomationTask) { task.AttemptCount = 1 },
		func(task *models.AutomationTask) { task.Status = "running" },
		func(task *models.AutomationTask) {
			task.CreatedAt = now.Add(-githubReviewRecoveryDelay + time.Nanosecond)
		},
		func(task *models.AutomationTask) { task.RequestedBy = "operator" },
		func(task *models.AutomationTask) { task.EvidenceSubjectDigest = "invalid" },
	} {
		candidate := *original
		mutate(&candidate)
		if recoverableQueuedGitHubReview(&candidate, now) {
			t.Fatalf("unexpected recovery eligibility: %#v", candidate)
		}
		if _, err := newStrandedGitHubReviewRecovery(&candidate, now); err == nil {
			t.Fatalf("invalid recovery boundary was accepted: %#v", candidate)
		}
	}
}

func TestLatestGitHubReviewAttemptFollowsTheImmutableRecoveryBoundary(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previousDB := configuration.DB
	configuration.DB = db
	defer func() { configuration.DB = previousDB }()

	now := time.Now().UTC()
	digest := strings.Repeat("a", 64)
	original := &models.AutomationTask{
		ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), RequestedBy: "github-app-review",
		CorrelationID: "github-pr:subject:head", Operation: "code.review", Status: "cancelled",
		EvidenceSubjectDigest: digest, InputRef: "s3://itbem-ai-inputs-local/automation/inputs/original/input.json",
		CreatedAt: now.Add(-time.Minute),
	}
	recoveryID, recoveryJobID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	rows := sqlmock.NewRows([]string{"id", "job_id", "requested_by", "correlation_id", "operation", "evidence_subject_digest", "input_ref", "status", "attempt_count", "created_at"}).
		AddRow(recoveryID, recoveryJobID, original.RequestedBy, original.CorrelationID, original.Operation, digest, original.InputRef, "running", 1, now)
	mock.ExpectQuery(`SELECT \* FROM "automation_tasks".*operation = \$1 AND requested_by = \$2 AND correlation_id = \$3 AND input_ref = \$4 AND evidence_subject_digest = \$5.*ORDER BY created_at DESC, id DESC`).
		WithArgs(original.Operation, original.RequestedBy, original.CorrelationID, original.InputRef, original.EvidenceSubjectDigest, 1).
		WillReturnRows(rows)

	current, err := latestGitHubReviewAttempt(original)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != recoveryID || current.JobID != recoveryJobID || current.Status != "running" || current.AttemptCount != 1 {
		t.Fatalf("latest immutable recovery attempt was not selected: %#v", current)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredGitHubReviewLeaseRecoveryIsBoundedAndPreservesExactReviewSubject(t *testing.T) {
	now := time.Now().UTC()
	digest := strings.Repeat("a", 64)
	expiredLease := now.Add(-time.Second)
	original := &models.AutomationTask{
		ID:                    uuid.Must(uuid.NewV4()),
		JobID:                 uuid.Must(uuid.NewV4()),
		RequestedBy:           "github-app-review",
		CorrelationID:         "github-pr:subject:head",
		Operation:             "code.review",
		Status:                "running",
		AttemptCount:          1,
		EvidenceSubjectDigest: digest,
		MaxCompletionTokens:   4096,
		InputRef:              "s3://itbem-ai-inputs-local/automation/inputs/original/input.json",
		LeaseExpiresAt:        &expiredLease,
	}
	if !recoverableExpiredGitHubReviewLease(original, now) {
		t.Fatal("an expired, claimed GitHub review with no completion should be recoverable once")
	}
	recovery, err := newExpiredGitHubReviewLeaseRecovery(original, now)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.ID == original.ID || recovery.JobID == original.JobID || recovery.Status != "queued" || recovery.AttemptCount != original.AttemptCount || recovery.InputRef != original.InputRef || recovery.EvidenceSubjectDigest != digest || recovery.CorrelationID != original.CorrelationID || recovery.RequestedBy != original.RequestedBy || recovery.MaxCompletionTokens != original.MaxCompletionTokens {
		t.Fatalf("lease recovery did not preserve the immutable review boundary: %#v", recovery)
	}
	message := codeReviewRetryQueueMessage(original, recovery)
	if message.Payload.RetryOfTaskID != original.ID.String() || message.Payload.TaskID != recovery.ID.String() || message.Payload.InputRef != original.InputRef || message.Payload.Operation != "code.review" || message.Payload.Attempt != original.AttemptCount+1 {
		t.Fatalf("lease recovery queue message was not bounded to its original review: %#v", message)
	}
	for _, mutate := range []func(*models.AutomationTask){
		func(task *models.AutomationTask) { task.Status = "queued" },
		func(task *models.AutomationTask) { task.LeaseExpiresAt = nil },
		func(task *models.AutomationTask) { future := now.Add(time.Second); task.LeaseExpiresAt = &future },
		func(task *models.AutomationTask) { task.AttemptCount = githubReviewLeaseRecoveryMaximumAttempts },
		func(task *models.AutomationTask) { completed := now; task.CompletedAt = &completed },
		func(task *models.AutomationTask) { task.RequestedBy = "operator" },
		func(task *models.AutomationTask) { task.EvidenceSubjectDigest = "invalid" },
	} {
		candidate := *original
		mutate(&candidate)
		if recoverableExpiredGitHubReviewLease(&candidate, now) {
			t.Fatalf("unexpected expired-lease recovery eligibility: %#v", candidate)
		}
		if _, err := newExpiredGitHubReviewLeaseRecovery(&candidate, now); err == nil {
			t.Fatalf("invalid expired-lease recovery boundary was accepted: %#v", candidate)
		}
	}
	exhausted := *original
	exhausted.AttemptCount = githubReviewLeaseRecoveryMaximumAttempts
	if recoverableExpiredGitHubReviewLease(&exhausted, now) || !exhaustedExpiredGitHubReviewLease(&exhausted, now) {
		t.Fatalf("maximum-attempt reviewer lease must be terminal-only, got %#v", exhausted)
	}
	exhausted.Status = "failed"
	if exhaustedExpiredGitHubReviewLease(&exhausted, now) {
		t.Fatalf("terminal reviewer task must not be reconciled: %#v", exhausted)
	}
}

func TestExpiredReviewRecoveryStopsBeforeInputAndRechecksReceiptUnderLock(t *testing.T) {
	for _, path := range []string{"poll", "redelivery"} {
		t.Run(path, func(t *testing.T) {
			db, mock := automationCostLedgerTestDB(t)
			previousDB := configuration.DB
			configuration.DB = db
			defer func() { configuration.DB = previousDB }()
			now := time.Now().UTC()
			expired := now.Add(-time.Minute)
			task := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), Operation: "code.review", RequestedBy: "github-app-review", Status: "running", AttemptCount: 1, InputRef: "s3://fixture/input.json", EvidenceSubjectDigest: strings.Repeat("a", 64), LeaseExpiresAt: &expired}
			rows := sqlmock.NewRows([]string{"id", "job_id", "operation", "requested_by", "status", "attempt_count", "input_ref", "evidence_subject_digest", "lease_expires_at"}).AddRow(task.ID, task.JobID, task.Operation, task.RequestedBy, task.Status, task.AttemptCount, task.InputRef, task.EvidenceSubjectDigest, expired)
			if path == "redelivery" {
				mock.ExpectBegin()
			}
			mock.ExpectQuery(`SELECT \* FROM "automation_tasks"`).WillReturnRows(rows)
			mock.ExpectQuery(`SELECT count\(\*\) FROM "automation_inference_receipts" WHERE automation_task_id = \$1 AND status IN \(\$2,\$3\)`).WithArgs(task.ID, "reserved", "ambiguous").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			if path == "redelivery" {
				mock.ExpectCommit()
				next, err := recoverStrandedGitHubReview(context.Background(), &task, now)
				if err != nil || next != nil {
					t.Fatalf("ambiguous redelivery created work: next=%v err=%v", next, err)
				}
			} else {
				recovered, err := reconcileOneExpiredGitHubReviewLease(context.Background(), &models.Config{GitHubReviewWebhookSecret: "fixture", GitHubReviewRepositories: "itbem/dashboard"}, now)
				if err != nil || recovered {
					t.Fatalf("ambiguous poll created work: recovered=%t err=%v", recovered, err)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReconcileExpiredGitHubReviewLeaseFinalizesExhaustedTaskBeforeReadingInput(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previousDB := configuration.DB
	configuration.DB = db
	defer func() { configuration.DB = previousDB }()

	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	task := models.AutomationTask{
		ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), Operation: "code.review", RequestedBy: "github-app-review",
		Status: "running", AttemptCount: githubReviewLeaseRecoveryMaximumAttempts, InputRef: "s3://itbem-ai-inputs-test/automation/inputs/task/input.json",
		EvidenceSubjectDigest: strings.Repeat("a", 64), LeaseExpiresAt: &expired,
	}
	rows := sqlmock.NewRows([]string{"id", "job_id", "operation", "requested_by", "status", "attempt_count", "input_ref", "evidence_subject_digest", "lease_expires_at"}).
		AddRow(task.ID, task.JobID, task.Operation, task.RequestedBy, task.Status, task.AttemptCount, task.InputRef, task.EvidenceSubjectDigest, task.LeaseExpiresAt)
	mock.ExpectQuery(`SELECT \* FROM "automation_tasks"`).WillReturnRows(rows)
	mock.ExpectBegin()
	currentRows := sqlmock.NewRows([]string{"id", "job_id", "operation", "requested_by", "status", "attempt_count", "input_ref", "evidence_subject_digest", "lease_expires_at"}).
		AddRow(task.ID, task.JobID, task.Operation, task.RequestedBy, task.Status, task.AttemptCount, task.InputRef, task.EvidenceSubjectDigest, task.LeaseExpiresAt)
	mock.ExpectQuery(`SELECT \* FROM "automation_tasks"`).WillReturnRows(currentRows)
	mock.ExpectQuery(`SELECT count\(\*\) FROM "automation_code_review_publications"`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(`UPDATE "automation_tasks"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	reconciled, err := reconcileOneExpiredGitHubReviewLease(context.Background(), &models.Config{GitHubReviewWebhookSecret: "secret", GitHubReviewRepositories: "itbem/dashboard"}, now)
	if err != nil || !reconciled {
		t.Fatalf("exhausted lease was not finalized before input recovery: reconciled=%t err=%v", reconciled, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestParseGitHubReviewRecoverySubjectRequiresFrozenCurrentReviewIdentity(t *testing.T) {
	now := time.Now().UTC()
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	patch := "diff --git a/controllers/orders.go b/controllers/orders.go\nindex abc..def 100644\n--- a/controllers/orders.go\n+++ b/controllers/orders.go\n@@ -1 +1 @@\n-old\n+new\n"
	review, err := automationagent.NewCodeReviewInput("github://itbem/backend", base, head, patch)
	if err != nil {
		t.Fatal(err)
	}
	review, err = automationagent.BindCodeReviewRemoteTarget(review, 42, 67890)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := automationagent.CodeReviewPublicationSubjectSHA256(review)
	if err != nil {
		t.Fatal(err)
	}
	correlation, err := githubReviewCorrelationID("itbem/backend", 42, head)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(automationagent.TaskInput{Prompt: "Review only the frozen patch.", Delivery: mustJSON(review)})
	if err != nil {
		t.Fatal(err)
	}
	expired := now.Add(-time.Second)
	task := &models.AutomationTask{
		ID:                    uuid.Must(uuid.NewV4()),
		JobID:                 uuid.Must(uuid.NewV4()),
		RequestedBy:           "github-app-review",
		CorrelationID:         correlation,
		Operation:             "code.review",
		Status:                "running",
		AttemptCount:          1,
		EvidenceSubjectDigest: digest,
		InputRef:              "s3://itbem-ai-inputs-test/automation/inputs/task/input.json",
		LeaseExpiresAt:        &expired,
	}
	cfg := &models.Config{AutomationInputBucket: "itbem-ai-inputs-test"}
	subject, err := parseGitHubReviewRecoverySubject(task, cfg, raw, now)
	if err != nil || subject.Repository != "itbem/backend" || subject.PullRequest != 42 || subject.InstallationID != 67890 || subject.HeadSHA != head {
		t.Fatalf("frozen recovery subject rejected or changed: %#v / %v", subject, err)
	}
	for _, mutate := range []func(*models.AutomationTask){
		func(value *models.AutomationTask) { value.EvidenceSubjectDigest = strings.Repeat("c", 64) },
		func(value *models.AutomationTask) { value.CorrelationID = "github-pr:wrong" },
		func(value *models.AutomationTask) { value.Status = "failed" },
	} {
		candidate := *task
		mutate(&candidate)
		if _, err := parseGitHubReviewRecoverySubject(&candidate, cfg, raw, now); err == nil {
			t.Fatalf("mutated recovery task was accepted: %#v", candidate)
		}
	}
}

func TestAutomationReviewIngressStatusReportsOnlySafeReadiness(t *testing.T) {
	t.Setenv("ITBEM_GITHUB_APP_ID", "")
	t.Setenv("ITBEM_GITHUB_INSTALLATION_ID", "")
	t.Setenv("ITBEM_GITHUB_APP_PRIVATE_KEY", "")
	status := automationReviewIngressStatus(&models.Config{GitHubReviewWebhookSecret: "secret", GitHubReviewRepositories: "itbem/backend"}, 1)
	if !status.Enabled || status.GitHubAppConfigured || !status.WorkerAvailable || status.Ready || status.AllowedRepositoryCount != 1 {
		t.Fatalf("incomplete review ingress must be visible without claiming ready: %#v", status)
	}
	status = automationReviewIngressStatus(&models.Config{}, 0)
	if status.Enabled || status.WorkerAvailable || status.Ready || status.AllowedRepositoryCount != 0 {
		t.Fatalf("disabled ingress status leaked a readiness claim: %#v", status)
	}
}

func TestAutomationHealthExposesLaneTelemetryWithoutInventingIt(t *testing.T) {
	withoutTelemetry, err := json.Marshal(automationHealth{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(withoutTelemetry), `"queue_lanes"`) {
		t.Fatalf("absent lane telemetry must remain unknown, got %s", withoutTelemetry)
	}

	withTelemetry, err := json.Marshal(automationHealth{QueueLanes: map[string]automationqueuerepository.LaneHealth{
		"review": {Available: true, Visible: 2, InFlight: 1, Delayed: 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"queue_lanes"`, `"review"`, `"available":true`, `"visible":2`, `"in_flight":1`} {
		if !strings.Contains(string(withTelemetry), expected) {
			t.Fatalf("lane telemetry is missing %s: %s", expected, withTelemetry)
		}
	}
}

func TestPopulateAutomationOutboxHealthKeepsTheHandoffAggregateOnly(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	oldest := time.Date(2026, time.September, 11, 15, 4, 5, 0, time.UTC)
	mock.ExpectQuery(`SELECT.*COUNT.*FROM "outbox_events".*`).
		WithArgs("local-ai-agent").
		WillReturnRows(sqlmock.NewRows([]string{"state", "count", "retrying"}).
			AddRow("pending", 2, 1).
			AddRow("processing", 1, 3).
			AddRow("completed", 9, 0))
	mock.ExpectQuery(`SELECT MIN\(created_at\).*FROM "outbox_events".*`).
		WithArgs("local-ai-agent", "pending").
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(oldest))

	health := automationHealth{}
	populateAutomationOutboxHealth(db, &health)
	if !health.OutboxTelemetryAvailable || health.OutboxPending != 2 || health.OutboxProcessing != 1 || health.OutboxRetrying != 4 || health.OutboxOldestPendingAt == nil || !health.OutboxOldestPendingAt.Equal(oldest) {
		t.Fatalf("unexpected safe outbox health projection: %#v", health)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPopulateAutomationOutboxHealthKeepsUnknownTelemetryAbsent(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT.*COUNT.*FROM "outbox_events".*`).WithArgs("local-ai-agent").WillReturnError(errors.New("outbox migration pending"))
	health := automationHealth{}
	populateAutomationOutboxHealth(db, &health)
	if health.OutboxTelemetryAvailable || health.OutboxPending != 0 || health.OutboxProcessing != 0 || health.OutboxRetrying != 0 || health.OutboxOldestPendingAt != nil {
		t.Fatalf("unavailable telemetry must remain unknown: %#v", health)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestValidCallbackSecretSupportsRotation(t *testing.T) {
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "current")
	t.Setenv("AUTOMATION_CALLBACK_SECRET_PREVIOUS", "previous")
	if !validCallbackSecret("current") || !validCallbackSecret("previous") {
		t.Fatal("expected active and previous automation secrets to be valid")
	}
	if validCallbackSecret("wrong") || validCallbackSecret("") {
		t.Fatal("unexpected automation callback secret accepted")
	}
	if err := os.Unsetenv("AUTOMATION_CALLBACK_SECRET"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("AUTOMATION_CALLBACK_SECRET_PREVIOUS"); err != nil {
		t.Fatal(err)
	}
	if validCallbackSecret("current") {
		t.Fatal("empty configuration must fail closed")
	}
}

func TestValidPublicationPRURLBindsTheApprovedRepository(t *testing.T) {
	approved := "itbem-corp/itbem-events-backend"
	for _, value := range []string{
		"https://github.com/itbem-corp/itbem-events-backend/pull/34",
		"https://github.com/ITBEM-CORP/ITBEM-EVENTS-BACKEND/pull/34",
	} {
		if !validPublicationPRURL(value, approved) {
			t.Fatalf("expected approved repository PR accepted: %q", value)
		}
	}
	for _, value := range []string{
		"https://github.com/itbem-corp/other-repository/pull/34",
		"https://github.com/itbem-corp/itbem-events-backend/pulls/34",
		"https://github.com/itbem-corp/itbem-events-backend/pull/0",
		"https://github.com/itbem-corp/itbem-events-backend/pull/34?redirect=1",
	} {
		if validPublicationPRURL(value, approved) {
			t.Fatalf("expected unrelated or non-canonical PR rejected: %q", value)
		}
	}
}

func TestValidReleaseTargetBranchAcceptsGenericSafeBranches(t *testing.T) {
	for _, branch := range []string{"main", "trunk", "release/v2"} {
		if !validReleaseTargetBranch(branch) {
			t.Fatalf("expected safe target branch accepted: %q", branch)
		}
	}
	for _, branch := range []string{"", " main", "main\nother", "../main", strings.Repeat("a", 256)} {
		if validReleaseTargetBranch(branch) {
			t.Fatalf("expected unsafe target branch rejected: %q", branch)
		}
	}
}

func TestWorkerWorkspaceReadinessRejectsImpossibleOrUnboundedStates(t *testing.T) {
	valid := []automationWorkspaceHealth{{
		ID: "eventiapp-dashboard", Ready: true, QAReady: true, VisualQAReady: true,
		PublicationReady: true, ValidationCommandCount: 2, NamedValidationCommandCount: 2, QACommandCount: 1, NamedQACommandCount: 1,
	}}
	if err := validateWorkerWorkspaceReadiness(valid); err != nil {
		t.Fatalf("expected safe readiness accepted: %v", err)
	}
	if err := validateWorkerWorkspaceReadiness([]automationWorkspaceHealth{{ID: "stagehand-only", Ready: true, VisualQAReady: true}}); err != nil {
		t.Fatalf("a visual browser harness can be ready without traditional QA commands: %v", err)
	}
	for _, invalid := range [][]automationWorkspaceHealth{
		{{ID: "../unsafe", Ready: true}},
		{{ID: "same", Ready: true}, {ID: "same", Ready: true}},
		{{ID: "qa-without-workspace", QAReady: true}},
		{{ID: "visual-without-workspace", VisualQAReady: true}},
		{{ID: "too-many-commands", Ready: true, ValidationCommandCount: 65}},
		{{ID: "impossible-named-commands", Ready: true, ValidationCommandCount: 1, NamedValidationCommandCount: 2}},
	} {
		if err := validateWorkerWorkspaceReadiness(invalid); err == nil {
			t.Fatalf("expected invalid readiness rejected: %#v", invalid)
		}
	}
}

func TestWorkspaceAttestationValidationRejectsUnsafeOrUnverifiableState(t *testing.T) {
	valid := []workspaceAttestationStatement{{
		ID: "backend", Available: true, GitHubRepository: "itbem-corp/itbem-events-backend", HeadSHA: strings.Repeat("a", 40),
		Branch: "main", Clean: true, Capabilities: []string{automationagent.WorkspaceCapabilityReadRepository, automationagent.WorkspaceCapabilityCreateWorktree},
	}}
	if err := validateWorkspaceAttestations(valid); err != nil {
		t.Fatalf("valid workspace attestation rejected: %v", err)
	}
	for _, candidate := range [][]workspaceAttestationStatement{
		{{ID: "backend", Available: true, GitHubRepository: "itbem-corp/itbem-events-backend", HeadSHA: "short", Branch: "main", Clean: true, Capabilities: []string{automationagent.WorkspaceCapabilityReadRepository}}},
		{{ID: "backend", Available: true, GitHubRepository: "itbem-corp/itbem-events-backend", HeadSHA: strings.Repeat("a", 40), Branch: "../main", Clean: true, Capabilities: []string{automationagent.WorkspaceCapabilityReadRepository}}},
		{{ID: "backend", Available: true, GitHubRepository: "itbem-corp/itbem-events-backend", HeadSHA: strings.Repeat("a", 40), Branch: "main", Clean: true, ChangeCount: 1, Capabilities: []string{automationagent.WorkspaceCapabilityReadRepository}}},
		{{ID: "backend", Available: true, GitHubRepository: "itbem-corp/itbem-events-backend", HeadSHA: strings.Repeat("a", 40), Branch: "main", Clean: true, Capabilities: []string{automationagent.WorkspaceCapabilityApplyPatch}}},
		{{ID: "backend", Available: false, HeadSHA: strings.Repeat("a", 40)}},
	} {
		if err := validateWorkspaceAttestations(candidate); err == nil {
			t.Fatalf("unsafe workspace attestation accepted: %#v", candidate)
		}
	}
}

func TestNormalizeWorkerRoleLaneKeepsLegacyVisibleAndRejectsCrossRoleIdentity(t *testing.T) {
	if role, lane, err := normalizeWorkerRoleLane("", ""); err != nil || role != "" || lane != "" {
		t.Fatalf("legacy combined worker rejected: %q %q %v", role, lane, err)
	}
	if role, lane, err := normalizeWorkerRoleLane(" reviewer ", " review "); err != nil || role != "reviewer" || lane != "review" {
		t.Fatalf("review worker rejected: %q %q %v", role, lane, err)
	}
	for _, identity := range [][2]string{{"reviewer", "release"}, {"admin", "production"}, {"principal_engineer", ""}, {"", "qa"}} {
		if _, _, err := normalizeWorkerRoleLane(identity[0], identity[1]); err == nil {
			t.Fatalf("invalid worker identity accepted: %#v", identity)
		}
	}
}

func TestValidWorkerProviderAllowsProviderlessReleaseOnly(t *testing.T) {
	if !validWorkerProvider("release_manager", "release", "", "") {
		t.Fatal("providerless deterministic release worker was rejected")
	}
	if validWorkerProvider("release_manager", "release", "minimax", "MiniMax-M3") {
		t.Fatal("release worker retained an unnecessary model credential")
	}
	if validWorkerProvider("reviewer", "review", "", "") {
		t.Fatal("review worker was allowed without a model provider")
	}
	if !validWorkerProvider("reviewer", "review", "minimax", "MiniMax-M3") {
		t.Fatal("configured review provider was rejected")
	}
}

func TestRecentCostLedgerSelectionIncludesEveryBillableComponentWithoutPrivateReferences(t *testing.T) {
	for _, column := range []string{
		"execution.input_cost_micros",
		"execution.output_cost_micros",
		"execution.cached_cost_micros",
		"execution.cache_write_cost_micros",
		"execution.pricing_basis",
		"execution.agent_instance_id AS agent_instance_id",
	} {
		if !strings.Contains(automationCostRecentExecutionSelect, column) {
			t.Fatalf("recent cost selection omitted %s", column)
		}
	}
	for _, privateColumn := range []string{"execution.request_ref", "execution.response_ref", "task.input_ref"} {
		if strings.Contains(automationCostRecentExecutionSelect, privateColumn) {
			t.Fatalf("recent cost selection leaked private reference %s", privateColumn)
		}
	}
}

func TestCostExecutionScanIgnoresDerivedProviderOutcome(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT execution.id, execution.automation_task_id, execution.completed_at FROM automation_executions AS execution")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id", "completed_at"}))

	var executions []automationCostExecution
	if err := db.Table("automation_executions AS execution").
		Select("execution.id, execution.automation_task_id, execution.completed_at").
		Scan(&executions).Error; err != nil {
		t.Fatalf("cost summary scan must ignore the trace-only provider outcome: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAutomationWorkerLastSeenAllowsNoHeartbeats(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT MAX(last_seen_at) FROM \"automation_agent_heartbeats\"")).
		WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(nil))

	lastSeen, err := automationWorkerLastSeen(db.Table("automation_agent_heartbeats"))
	if err != nil {
		t.Fatalf("empty heartbeat table must be a valid health state: %v", err)
	}
	if lastSeen != nil {
		t.Fatalf("empty heartbeat table returned an unexpected timestamp: %v", lastSeen)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentAutomationWorkersUsesNewestHeartbeatForEachRoleLane(t *testing.T) {
	now := time.Date(2026, time.September, 8, 22, 0, 0, 0, time.UTC)
	workers := []automationWorkerHealth{
		{Role: "release_manager", Lane: "release", Concurrency: 1, StartedAt: now.Add(-2 * time.Minute), LastSeenAt: now.Add(-30 * time.Second), WorkspaceReadinessJSON: `[{"id":"backend","ready":false}]`},
		{Role: "release_manager", Lane: "release", Concurrency: 1, StartedAt: now.Add(-time.Minute), LastSeenAt: now, WorkspaceReadinessJSON: `[{"id":"backend","ready":true}]`},
		{Role: "reviewer", Lane: "review", Concurrency: 1, StartedAt: now.Add(-time.Minute), LastSeenAt: now.Add(-10 * time.Second)},
		{Concurrency: 1, StartedAt: now.Add(-time.Minute), LastSeenAt: now.Add(-20 * time.Second)},
		{Concurrency: 1, StartedAt: now.Add(-time.Minute), LastSeenAt: now.Add(-15 * time.Second)},
	}

	got := currentAutomationWorkers(workers)
	if len(got) != 4 {
		t.Fatalf("current workers = %d, want release, review, and two legacy entries", len(got))
	}
	var release *automationWorkerHealth
	legacy := 0
	for index := range got {
		worker := &got[index]
		if worker.Role == "release_manager" && worker.Lane == "release" {
			release = worker
		}
		if worker.Role == "" && worker.Lane == "" {
			legacy++
		}
	}
	if release == nil || !release.LastSeenAt.Equal(now) || !strings.Contains(release.WorkspaceReadinessJSON, `"ready":true`) {
		t.Fatalf("release worker did not retain its newest heartbeat: %#v", release)
	}
	if legacy != 2 {
		t.Fatalf("legacy workers must not be coalesced without a role/lane: %d", legacy)
	}
	if latest := newestAutomationWorkerLastSeen(got); latest == nil || !latest.Equal(now) {
		t.Fatalf("newest worker heartbeat = %v, want %v", latest, now)
	}
}

func TestToolExecutionLedgerCostsOnlyUploadedStagehandReport(t *testing.T) {
	taskID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	workerID, machineID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
	task := &models.AutomationTask{ID: taskID, DeliveryWorkItemID: &workItemID, Operation: "delivery.qa", WorkerID: workerID, AgentKey: "qa_specialist", MachineID: machineID}
	reference := "s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/01-dashboard-semantic-qa.json"
	artifacts := []callbackArtifact{{Name: "01-dashboard-semantic-qa.json", Reference: reference, ContentType: "application/json", SizeBytes: 120, SHA256: strings.Repeat("a", 64)}}
	usage := json.RawMessage(`{"input_tokens":120,"output_tokens":40,"cached_input_tokens":10,"total_tokens":160,"prompt":"private-provider-prompt"}`)
	rows, err := buildToolExecutionLedger(nil, task, uuid.Must(uuid.NewV4()).String(), "completed", []callbackToolExecution{{Tool: "stagehand", CallKey: "semantic-assessment", StepKey: "qa.semantic_browser", Provider: "minimax", Model: "MiniMax-M3", Usage: usage, RequestRef: reference, ResponseRef: reference}}, artifacts, time.Now().UTC())
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected one costed Stagehand row: %#v / %v", rows, err)
	}
	if rows[0].CallKey != "semantic-assessment" || rows[0].InputTokens != 120 || rows[0].OutputTokens != 40 || rows[0].CachedInputTokens != 10 || rows[0].TotalCostMicros <= 0 || rows[0].RequestRef != reference || rows[0].ResponseRef != reference {
		t.Fatalf("Stagehand row lost accounting or audit linkage: %#v", rows[0])
	}
	if rows[0].WorkerID != workerID || rows[0].AgentKey != "qa_specialist" || rows[0].MachineID != machineID {
		t.Fatalf("tool ledger row must inherit the persisted parent task attribution: got worker=%q agent=%q machine=%q", rows[0].WorkerID, rows[0].AgentKey, rows[0].MachineID)
	}
	if strings.Contains(rows[0].UsageJSON, "private-provider-prompt") {
		t.Fatal("private tool prompt reached accounting")
	}
	for _, incomplete := range []json.RawMessage{json.RawMessage(`{"input_tokens":120}`), json.RawMessage(`{"output_tokens":40}`), json.RawMessage(`{"total_tokens":160}`)} {
		_, err := buildToolExecutionLedger(nil, task, uuid.Must(uuid.NewV4()).String(), "completed", []callbackToolExecution{{Tool: "stagehand", CallKey: "semantic-assessment", StepKey: "qa.semantic_browser", Provider: "minimax", Model: "MiniMax-M3", Usage: incomplete, RequestRef: reference, ResponseRef: reference}}, artifacts, time.Now().UTC())
		if err == nil {
			t.Fatal("incomplete tool accounting was accepted")
		}
	}
	_, err = buildToolExecutionLedger(nil, task, uuid.Must(uuid.NewV4()).String(), "completed", []callbackToolExecution{{Tool: "stagehand", StepKey: "qa.semantic_browser", Provider: "minimax", Model: "MiniMax-M3", Usage: usage, RequestRef: reference, ResponseRef: "s3://arbitrary/report.json"}}, artifacts, time.Now().UTC())
	if err == nil {
		t.Fatal("expected arbitrary tool response reference to be rejected")
	}
}

func TestToolExecutionLedgerAcceptsDistinctCallsAndRejectsDuplicates(t *testing.T) {
	taskID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	task := &models.AutomationTask{ID: taskID, DeliveryWorkItemID: &workItemID, Operation: "delivery.qa"}
	reference := "s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/01-dashboard-semantic-qa.json"
	artifacts := []callbackArtifact{{Name: "01-dashboard-semantic-qa.json", Reference: reference, ContentType: "application/json", SizeBytes: 120, SHA256: strings.Repeat("a", 64)}}
	usage := json.RawMessage(`{"input_tokens":12,"output_tokens":4,"total_tokens":16}`)
	calls := []callbackToolExecution{
		{Tool: "stagehand", CallKey: "semantic-assessment", StepKey: "qa.semantic_browser", Provider: "minimax", Model: "MiniMax-M3", Usage: usage, RequestRef: reference, ResponseRef: reference},
		{Tool: "stagehand", CallKey: "semantic-retry", CallStatus: "failed", StepKey: "qa.semantic_browser", Provider: "minimax", Model: "MiniMax-M3", Usage: usage, RequestRef: reference, ResponseRef: reference},
	}
	rows, err := buildToolExecutionLedger(nil, task, uuid.Must(uuid.NewV4()).String(), "completed", calls, artifacts, time.Now().UTC())
	if err != nil || len(rows) != 2 || rows[0].CallKey == rows[1].CallKey || rows[1].CallStatus != "failed" {
		t.Fatalf("distinct tool calls must remain distinct: %#v / %v", rows, err)
	}
	calls[1].CallKey = "semantic-assessment"
	if _, err = buildToolExecutionLedger(nil, task, uuid.Must(uuid.NewV4()).String(), "completed", calls, artifacts, time.Now().UTC()); err == nil {
		t.Fatal("duplicate tool call keys must be rejected before persistence")
	}
}

func TestCostLedgerUnionIncludesPrimaryAndToolExecutions(t *testing.T) {
	for _, expected := range []string{"FROM automation_executions", "FROM automation_tool_executions", "'agent' AS execution_kind", "'tool' AS execution_kind", "agent_key", "agent_instance_id", "call_key"} {
		if !strings.Contains(automationCostLedgerUnion, expected) {
			t.Fatalf("ledger union omitted %q: %s", expected, automationCostLedgerUnion)
		}
	}
	for _, forbidden := range []string{"request_ref", "response_ref", "usage_json"} {
		if strings.Contains(automationCostLedgerUnion, forbidden) {
			t.Fatalf("ledger union must not expose private %q: %s", forbidden, automationCostLedgerUnion)
		}
	}
}

func costLedgerColumns(names ...string) map[string]struct{} {
	columns := make(map[string]struct{}, len(names))
	for _, name := range names {
		columns[name] = struct{}{}
	}
	return columns
}

func automationCostLedgerTestDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, mock
}

func TestCostLedgerSourceFallsBackToPersistedAgentLedgerWhenToolMigrationIsAbsent(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	rows := sqlmock.NewRows([]string{"table_name", "column_name"})
	for _, column := range []string{"id", "automation_task_id", "total_cost_micros", "completed_at"} {
		rows.AddRow(automationExecutionLedgerTable, column)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT table_name, column_name")+`\s+FROM information_schema\.columns`).
		WithArgs(automationExecutionLedgerTable, automationToolExecutionLedgerTable).
		WillReturnRows(rows)

	source, coverage, err := automationCostLedgerSource(db)
	if err != nil {
		t.Fatalf("agent ledger fallback should be available: %v", err)
	}
	if coverage.State != "partial" || !coverage.AgentLedger || coverage.ToolLedger {
		t.Fatalf("coverage should identify the missing optional tool migration: %#v", coverage)
	}
	if strings.Contains(source, automationToolExecutionLedgerTable) || !strings.Contains(source, automationExecutionLedgerTable+".total_cost_micros") {
		t.Fatalf("source must use the persisted agent ledger only: %s", source)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCostLedgerProjectionKeepsLegacyAgentTotalsWithoutInventingToolCalls(t *testing.T) {
	projection, missing, ok := automationCostLedgerProjection(automationExecutionLedgerTable, costLedgerColumns(
		"id", "automation_task_id", "total_cost_micros", "completed_at",
	), "agent")
	if !ok {
		t.Fatalf("legacy primary ledger should remain readable: missing=%#v", missing)
	}
	for _, expected := range []string{
		"automation_executions.total_cost_micros",
		"NULL::uuid AS agent_instance_id",
		"0::bigint AS cached_input_tokens",
		"''::text AS agent_key",
		"'legacy'::text AS pricing_basis",
		"'agent'::text AS execution_kind",
		"''::text AS tool",
	} {
		if !strings.Contains(projection, expected) {
			t.Fatalf("legacy primary projection omitted %q: %s", expected, projection)
		}
	}
	if strings.Contains(projection, automationToolExecutionLedgerTable) {
		t.Fatalf("agent-only projection must not imply a tool ledger: %s", projection)
	}
	if !strings.Contains(strings.Join(missing, ","), "cached_input_tokens") {
		t.Fatalf("legacy dimensions must be marked rather than silently claimed complete: %#v", missing)
	}
}

func TestCostLedgerProjectionPreservesImmutableAgentAttribution(t *testing.T) {
	projection, missing, ok := automationCostLedgerProjection(automationExecutionLedgerTable, costLedgerColumns(
		"id", "automation_task_id", "total_cost_micros", "completed_at", "agent_key", "agent_instance_id",
	), "agent")
	if !ok {
		t.Fatalf("agent ledger projection should be available: missing=%#v", missing)
	}
	if !strings.Contains(projection, "automation_executions.agent_key") {
		t.Fatalf("immutable agent attribution was not selected: %s", projection)
	}
	if !strings.Contains(projection, "automation_executions.agent_instance_id") {
		t.Fatalf("immutable instance attribution was not selected: %s", projection)
	}
	if strings.Contains(strings.Join(missing, ","), "agent_key") {
		t.Fatalf("present agent attribution was incorrectly marked missing: %#v", missing)
	}
	if strings.Contains(strings.Join(missing, ","), "agent_instance_id") {
		t.Fatalf("present instance attribution was incorrectly marked missing: %#v", missing)
	}
}

func TestCostLedgerProjectionSupportsStableCreationSnapshot(t *testing.T) {
	legacyProjection, legacyMissing, ok := automationCostLedgerProjection(automationExecutionLedgerTable, costLedgerColumns(
		"id", "automation_task_id", "total_cost_micros", "completed_at",
	), "agent")
	if !ok {
		t.Fatalf("legacy ledger should support a completion-time snapshot: missing=%#v", legacyMissing)
	}
	if !strings.Contains(legacyProjection, "completed_at AS created_at") || !strings.Contains(strings.Join(legacyMissing, ","), "created_at") {
		t.Fatalf("legacy fallback must be explicit and marked: %s missing=%#v", legacyProjection, legacyMissing)
	}
	currentProjection, currentMissing, ok := automationCostLedgerProjection(automationExecutionLedgerTable, costLedgerColumns(
		"id", "automation_task_id", "total_cost_micros", "completed_at", "created_at",
	), "agent")
	if !ok {
		t.Fatalf("current ledger should remain readable: missing=%#v", currentMissing)
	}
	if !strings.Contains(currentProjection, "automation_executions.created_at") || strings.Contains(strings.Join(currentMissing, ","), "created_at") {
		t.Fatalf("current ledger must use its immutable creation timestamp: %s missing=%#v", currentProjection, currentMissing)
	}
}

func TestCostLedgerProjectionRequiresAnAuthoritativePrimaryCost(t *testing.T) {
	projection, missing, ok := automationCostLedgerProjection(automationExecutionLedgerTable, costLedgerColumns(
		"id", "automation_task_id", "completed_at",
	), "agent")
	if ok || projection != "" {
		t.Fatalf("projection without persisted total cost must be unavailable, got %q", projection)
	}
	if !strings.Contains(strings.Join(missing, ","), "total_cost_micros") {
		t.Fatalf("missing authoritative cost was not reported: %#v", missing)
	}
}

func TestCostLedgerProjectionReadsLegacyToolRowsWithoutLosingCost(t *testing.T) {
	projection, missing, ok := automationCostLedgerProjection(automationToolExecutionLedgerTable, costLedgerColumns(
		"id", "automation_task_id", "total_cost_micros", "completed_at", "tool",
	), "tool")
	if !ok {
		t.Fatalf("legacy tool ledger should remain readable: missing=%#v", missing)
	}
	for _, expected := range []string{
		"automation_tool_executions.total_cost_micros",
		"automation_tool_executions.tool",
		"''::text AS call_key",
		"'completed'::text AS call_status",
		"'tool'::text AS execution_kind",
	} {
		if !strings.Contains(projection, expected) {
			t.Fatalf("legacy tool projection omitted %q: %s", expected, projection)
		}
	}
	for _, expectedMissing := range []string{"call_key", "call_status"} {
		if !strings.Contains(strings.Join(missing, ","), expectedMissing) {
			t.Fatalf("legacy tool metadata must be marked as unavailable: %#v", missing)
		}
	}
}

func TestCostBreakdownHasRuntimeIdentityForStepAccounting(t *testing.T) {
	breakdown := automationCostBreakdown{Key: "qa.semantic_browser", ExecutionKind: "tool", Tool: "stagehand"}
	if breakdown.ExecutionKind != "tool" || breakdown.Tool != "stagehand" {
		t.Fatalf("step breakdown cannot distinguish a tool call: %#v", breakdown)
	}
}

func TestCostBreakdownsExposeEveryTokenAndPriceDimension(t *testing.T) {
	breakdown := automationCostBreakdown{
		InputTokens: 120, OutputTokens: 40, CachedInputTokens: 25, CacheWriteTokens: 10, ReasoningTokens: 12,
		InputCostMicros: 17, OutputCostMicros: 9, CachedCostMicros: 2, CacheWriteCostMicros: 1,
	}
	encoded, err := json.Marshal(breakdown)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"input_tokens", "output_tokens", "cached_input_tokens", "cache_write_tokens", "reasoning_tokens", "input_cost_microusd", "output_cost_microusd", "cached_cost_microusd", "cache_write_cost_microusd"} {
		if !strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("global cost breakdown omitted %s: %s", field, encoded)
		}
	}
}

func TestDeliveryTaskMemberCanManageCancellationWithoutGrantingViewersControl(t *testing.T) {
	for _, member := range []models.DeliveryProjectMember{
		{Role: "owner"}, {Role: "delivery_manager"}, {Role: "viewer", Permissions: `["delivery:manage"]`},
	} {
		if !deliveryTaskMemberCanManage(member) {
			t.Fatalf("expected cancellation authority for %#v", member)
		}
	}
	for _, member := range []models.DeliveryProjectMember{
		{Role: "reviewer"}, {Role: "qa_reviewer"}, {Role: "viewer"}, {Role: "requester", Permissions: `["delivery:view"]`},
	} {
		if deliveryTaskMemberCanManage(member) {
			t.Fatalf("unexpected cancellation authority for %#v", member)
		}
	}
}

func TestCancelledTaskResultRemainsInspectableWithoutMakingItSuccessful(t *testing.T) {
	for _, status := range []string{"completed", "failed", "cancelled"} {
		if !taskResultIsInspectable(status) {
			t.Fatalf("expected %q output to remain auditable", status)
		}
	}
	for _, status := range []string{"queued", "running", "cancel_requested", "dispatch_failed"} {
		if taskResultIsInspectable(status) {
			t.Fatalf("unexpected inspectable output for unsettled state %q", status)
		}
	}
}

func TestCancellationRequestedTaskRetainsItsBudgetAdmissionHold(t *testing.T) {
	statuses := activeAutomationBudgetStatuses()
	if !strings.Contains(strings.Join(statuses, ","), "cancel_requested") {
		t.Fatalf("a cancelling in-flight task must retain its reservation: %#v", statuses)
	}
	if strings.Contains(strings.Join(statuses, ","), "cancelled") {
		t.Fatalf("a settled cancellation must release its reservation: %#v", statuses)
	}
}

func TestExpiredRunningTaskCancellationSettlesAndReleasesItsReservation(t *testing.T) {
	now := time.Now().UTC()
	expired := now.Add(-time.Second)
	task := models.AutomationTask{Status: "running", LeaseExpiresAt: &expired, BudgetReservationMicros: 99}
	updates, statusCode, message, err := automationCancellationTransition(task, now, "worker interrupted")
	if err != nil || statusCode != http.StatusOK || message != "Expired automation task cancelled" {
		t.Fatalf("expired cancellation did not settle: updates=%#v status=%d message=%q err=%v", updates, statusCode, message, err)
	}
	if updates["status"] != "cancelled" || updates["budget_reservation_micros"] != int64(0) || updates["lease_expires_at"] != nil || updates["budget_reservation_expires_at"] != nil {
		t.Fatalf("expired cancellation retained live authority or budget: %#v", updates)
	}
}

func TestLiveRunningTaskCancellationStillWaitsForWorkerAccounting(t *testing.T) {
	now := time.Now().UTC()
	active := now.Add(time.Minute)
	updates, statusCode, message, err := automationCancellationTransition(models.AutomationTask{Status: "running", LeaseExpiresAt: &active}, now, "operator request")
	if err != nil || statusCode != http.StatusAccepted || message != "Automation cancellation requested" || updates["status"] != "cancel_requested" {
		t.Fatalf("live cancellation bypassed worker settlement: updates=%#v status=%d message=%q err=%v", updates, statusCode, message, err)
	}
}

func TestRunningTaskWithoutLeaseCancellationSettlesAsAbandoned(t *testing.T) {
	now := time.Now().UTC()
	updates, statusCode, message, err := automationCancellationTransition(models.AutomationTask{Status: "running", BudgetReservationMicros: 99}, now, "missing lease")
	if err != nil || statusCode != http.StatusOK || message != "Expired automation task cancelled" {
		t.Fatalf("lease-less cancellation did not settle: updates=%#v status=%d message=%q err=%v", updates, statusCode, message, err)
	}
	if updates["status"] != "cancelled" || updates["budget_reservation_micros"] != int64(0) || updates["lease_expires_at"] != nil || updates["budget_reservation_expires_at"] != nil {
		t.Fatalf("lease-less cancellation retained live authority or budget: %#v", updates)
	}
}

func TestCanonicalTraceEntriesKeepKindsAndPrivateReferencesOutOfTheResponse(t *testing.T) {
	finishedAt := time.Now().UTC()
	taskID := uuid.Must(uuid.NewV4())
	instanceID := uuid.Must(uuid.NewV4())
	agent := traceEntryFromAgentExecution(models.AutomationExecution{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: taskID, AgentInstanceID: &instanceID, StepKey: "delivery.plan", Provider: "minimax", Model: "MiniMax-M3",
		InputTokens: 100, OutputTokens: 20, TotalTokens: 120, TotalCostMicros: 47, PricingBasis: "snapshot", CompletedAt: finishedAt,
		RequestRef: "s3://private/request.json", ResponseRef: "s3://private/response.json",
		UsageJSON: `{"input_tokens":100,"_itbem_provider":{"finish_reason":"stop","input_sensitive":true,"status_code":200,"ignored":"must-not-leak"}}`,
	})
	tool := traceEntryFromToolExecution(models.AutomationToolExecution{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: taskID, AgentInstanceID: &instanceID, Tool: "stagehand", StepKey: "qa.semantic_browser", Provider: "minimax", Model: "MiniMax-M3",
		InputTokens: 50, OutputTokens: 10, TotalTokens: 60, TotalCostMicros: 23, PricingBasis: "snapshot", CompletedAt: finishedAt,
		RequestRef: "s3://private/report.json", ResponseRef: "s3://private/report.json",
		UsageJSON: `{"_itbem_provider":{"finish_reason":"length","output_sensitive":true,"status_code":429}}`,
	})
	if agent.ExecutionKind != "agent" || tool.ExecutionKind != "tool" || tool.Tool != "stagehand" || agent.AgentInstanceID == nil || *agent.AgentInstanceID != instanceID || tool.AgentInstanceID == nil || *tool.AgentInstanceID != instanceID || agent.TotalCostMicros != 47 || tool.TotalTokens != 60 || agent.ProviderOutcome == nil || agent.ProviderOutcome.FinishReason != "stop" || tool.ProviderOutcome == nil || tool.ProviderOutcome.StatusCode != 429 {
		t.Fatalf("canonical entries lost billing metadata: agent=%#v tool=%#v", agent, tool)
	}
	encoded, err := json.Marshal([]automationCostExecution{agent, tool})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"request_ref", "response_ref", "s3://private", "ignored"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("canonical trace entry leaked private data %q: %s", forbidden, encoded)
		}
	}
}

func TestCostLedgerAttributionUsesOnlyAuthenticatedCallbackIdentity(t *testing.T) {
	trustedInstanceID := uuid.Must(uuid.NewV4())
	spoofedInstanceID := uuid.Must(uuid.NewV4())
	e := echo.New()
	request := httptest.NewRequest(http.MethodPost, "/internal/automation/result", strings.NewReader(`{"agent_instance_id":"`+spoofedInstanceID.String()+`"}`))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := e.NewContext(request, httptest.NewRecorder())
	c.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{InstanceID: trustedInstanceID, AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String()})
	var untrusted callbackRequest
	if err := c.Bind(&untrusted); err != nil {
		t.Fatalf("callback request with unknown instance field should bind without granting it authority: %v", err)
	}
	execution := &models.AutomationExecution{}
	toolExecutions := []models.AutomationToolExecution{{}}
	if !attributeAutomationCostRowsToCallbackIdentity(c, execution, toolExecutions) {
		t.Fatal("authenticated callback identity was not available for ledger attribution")
	}
	if execution.AgentInstanceID == nil || *execution.AgentInstanceID != trustedInstanceID || toolExecutions[0].AgentInstanceID == nil || *toolExecutions[0].AgentInstanceID != trustedInstanceID {
		t.Fatalf("cost rows were not attributed to authenticated instance: execution=%#v tool=%#v", execution.AgentInstanceID, toolExecutions[0].AgentInstanceID)
	}
	if *execution.AgentInstanceID == spoofedInstanceID || *toolExecutions[0].AgentInstanceID == spoofedInstanceID {
		t.Fatal("cost attribution trusted agent_instance_id from request JSON")
	}
	if attributeAutomationCostRowsToCallbackIdentity(e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder()), execution, toolExecutions) {
		t.Fatal("ledger attribution succeeded without an authenticated callback identity")
	}
}

func TestFinalizeBudgetWatchClassifiesHardBudgetWithoutFloatingPointDrift(t *testing.T) {
	tests := []struct {
		name       string
		spent      int64
		wantStatus string
		wantUse    int
		wantLeft   int64
	}{
		{name: "healthy", spent: 799, wantStatus: "healthy", wantUse: 79, wantLeft: 201},
		{name: "alert threshold", spent: 800, wantStatus: "attention", wantUse: 80, wantLeft: 200},
		{name: "exceeded", spent: 1200, wantStatus: "exceeded", wantUse: 100, wantLeft: 0},
		{name: "negative input", spent: -1, wantStatus: "healthy", wantUse: 0, wantLeft: 1000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			watch := finalizeBudgetWatch(automationCostBudgetWatch{
				MonthlyBudgetMicros: 1000,
				AlertPercent:        80,
				SpentMicros:         test.spent,
			})
			if watch.Status != test.wantStatus || watch.UsagePercent != test.wantUse || watch.RemainingMicros != test.wantLeft {
				t.Fatalf("watch = %#v", watch)
			}
		})
	}
}

func TestFinalizeBudgetWatchCountsPendingReservations(t *testing.T) {
	watch := finalizeBudgetWatch(automationCostBudgetWatch{
		MonthlyBudgetMicros: 1000,
		AlertPercent:        80,
		SpentMicros:         300,
		ReservedMicros:      500,
	})
	if watch.AllocatedMicros != 800 || watch.RemainingMicros != 200 || watch.UsagePercent != 80 || watch.Status != "attention" {
		t.Fatalf("reservation-aware watch = %#v", watch)
	}
}

func TestFinalizeTaskBudgetWatchUsesLifetimeTaskCapAndReservations(t *testing.T) {
	watch := finalizeTaskBudgetWatch(automationCostTaskBudgetWatch{
		BudgetMicros:   1000,
		AlertPercent:   80,
		SpentMicros:    300,
		ReservedMicros: 500,
	})
	if watch.AllocatedMicros != 800 || watch.RemainingMicros != 200 || watch.UsagePercent != 80 || watch.Status != "attention" {
		t.Fatalf("task reservation-aware watch = %#v", watch)
	}

	exceeded := finalizeTaskBudgetWatch(automationCostTaskBudgetWatch{
		BudgetMicros: 1000,
		AlertPercent: 80,
		SpentMicros:  1001,
	})
	if exceeded.Status != "exceeded" || exceeded.RemainingMicros != 0 || exceeded.UsagePercent != 100 {
		t.Fatalf("task cap was not enforced: %#v", exceeded)
	}
}

func TestInputReferenceMatchesDedicatedBucketAndPrefix(t *testing.T) {
	cfg := &models.Config{AutomationInputBucket: "itbem-ai-inputs-prod-123-us-east-2"}
	valid := "s3://itbem-ai-inputs-prod-123-us-east-2/automation/inputs/task-1/input.json"
	if !inputReferenceMatches(cfg, valid) {
		t.Fatal("expected generated input reference to be accepted")
	}
	for _, reference := range []string{
		"s3://itbem-ai-inputs-prod-123-us-east-2/other/input.json",
		"s3://itbem-ai-inputs-other/automation/inputs/task-1/input.json",
		"s3://itbem-ai-inputs-prod-123-us-east-2/automation/inputs/task-1/source.txt",
	} {
		if inputReferenceMatches(cfg, reference) {
			t.Fatalf("unexpected input reference accepted: %s", reference)
		}
	}
}

func TestOutputReferenceMatchesTaskScopedLegacyOrImmutableRunResult(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	runID := uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationOutputBucket: "itbem-ai-outputs-prod-123-us-east-2"}
	valid := "s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/result.json"
	if !outputReferenceMatches(cfg, id, valid) {
		t.Fatal("expected legacy output reference to be accepted")
	}
	immutable := "s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + runID.String() + "/result.json"
	if !outputReferenceMatches(cfg, id, immutable) {
		t.Fatal("expected immutable execution output reference to be accepted")
	}
	if outputReferenceMatches(cfg, id, immutable+".bak") || outputReferenceMatches(cfg, id, "s3://itbem-ai-outputs-prod-123-us-east-2/automation/"+id.String()+"/runs/not-a-run/result.json") {
		t.Fatal("unexpected output reference accepted")
	}
}

func TestExecutionRequestReferenceMatchesOnlyItsOwnRun(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	runID := uuid.Must(uuid.NewV4())
	otherRunID := uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationOutputBucket: "itbem-ai-outputs-prod-123-us-east-2"}
	valid := "s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + runID.String() + "/request.json"
	if !executionRequestReferenceMatches(cfg, id, runID.String(), valid) {
		t.Fatal("expected exact immutable execution request to be accepted")
	}
	for _, reference := range []string{
		"s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + otherRunID.String() + "/request.json",
		"s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + runID.String() + "/result.json",
		"s3://itbem-ai-outputs-other/automation/" + id.String() + "/runs/" + runID.String() + "/request.json",
	} {
		if executionRequestReferenceMatches(cfg, id, runID.String(), reference) {
			t.Fatalf("unexpected execution request reference accepted: %s", reference)
		}
	}
}

func TestExecutionResultReferenceMatchesOnlyItsOriginalRun(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	runID := uuid.Must(uuid.NewV4())
	otherRunID := uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationOutputBucket: "itbem-ai-outputs-prod-123-us-east-2"}
	valid := "s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + runID.String() + "/result.json"
	if !executionResultReferenceMatches(cfg, id, runID.String(), valid) {
		t.Fatal("expected exact immutable execution result to be accepted")
	}
	for _, reference := range []string{
		"s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + otherRunID.String() + "/result.json",
		"s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/runs/" + runID.String() + "/request.json",
		"s3://itbem-ai-outputs-prod-123-us-east-2/automation/" + id.String() + "/result.json",
	} {
		if executionResultReferenceMatches(cfg, id, runID.String(), reference) {
			t.Fatalf("unexpected execution result reference accepted: %s", reference)
		}
	}
}

func TestDeliveryOperationsRemainExplicitlyAllowlisted(t *testing.T) {
	for _, operation := range []string{
		"ai.chat", "document.analyze", "code.review", "product.ideate",
		"delivery.plan", "delivery.implementation", "delivery.assessment", "delivery.onboarding_probe", "delivery.publish", "delivery.qa", "delivery.summary",
	} {
		if _, allowed := allowedOperations[operation]; !allowed {
			t.Fatalf("expected operation to be enabled: %s", operation)
		}
	}
	if _, allowed := allowedOperations["shell.execute"]; allowed {
		t.Fatal("arbitrary command execution must never be enabled")
	}
}

func TestGenericTaskOperationsCannotBypassDeliveryGates(t *testing.T) {
	for _, operation := range []string{"ai.chat", "document.analyze", "code.review", "product.ideate"} {
		if !genericTaskOperationAllowed(operation) {
			t.Fatalf("generic operation %s should remain available", operation)
		}
	}
	for _, operation := range []string{"delivery.plan", "delivery.implementation", "delivery.assessment", "delivery.onboarding_probe", "delivery.publish", "delivery.qa", "delivery.summary", "shell.execute"} {
		if genericTaskOperationAllowed(operation) {
			t.Fatalf("operation %s must not be started through the generic task endpoint", operation)
		}
	}
}

func TestProviderAllowedUsesStrictNormalizedAllowlist(t *testing.T) {
	for _, provider := range []string{"minimax", " OpenAI ", "ANTHROPIC", "deepseek", "OpenRouter"} {
		if !providerAllowed(provider) {
			t.Fatalf("expected provider to be allowed: %s", provider)
		}
	}
	for _, provider := range []string{"", "azure-openai", "unknown"} {
		if providerAllowed(provider) {
			t.Fatalf("unexpected provider allowed: %s", provider)
		}
	}
}

func TestArtifactNamePatternRejectsTraversalAndPaths(t *testing.T) {
	for _, value := range []string{"01-screen.png", "report.webm", "result.json"} {
		if !artifactNamePattern.MatchString(value) {
			t.Fatalf("expected valid artifact name: %s", value)
		}
	}
	for _, value := range []string{"../secret", "nested/file.png", "", ".hidden"} {
		if artifactNamePattern.MatchString(value) {
			t.Fatalf("unexpected valid artifact name: %s", value)
		}
	}
}

func TestTaskOwnerAccessDoesNotNeedRootLookup(t *testing.T) {
	context := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	task := &models.AutomationTask{RequestedBy: "owner"}
	if !mayAccessTask(context, task, "owner") {
		t.Fatal("task owner should be allowed")
	}
}

func TestAuthorizedToolExecutionReportHidesDeliveryReportFromRemovedRequester(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	requester := "former-project-member"
	restoreHooks := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		return &models.User{CognitoSub: cognitoSub}, nil
	}})
	t.Cleanup(restoreHooks)

	executionID, taskID, workItemID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	mock.ExpectQuery(`SELECT .* FROM "automation_tool_executions".*`).
		WithArgs(executionID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id", "tool", "call_key", "run_id", "request_ref", "response_ref"}).
			AddRow(executionID, taskID, "stagehand", "semantic-assessment", uuid.Must(uuid.NewV4()).String(), "", ""))
	mock.ExpectQuery(`SELECT .* FROM "automation_tasks".*`).
		WithArgs(taskID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "requested_by", "delivery_work_item_id", "operation"}).
			AddRow(taskID, requester, workItemID, "delivery.qa"))
	mock.ExpectQuery(`SELECT .* FROM "delivery_work_items".*`).
		WithArgs(workItemID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(projectID))
	mock.ExpectQuery(`SELECT .* FROM "delivery_project_members".*`).
		WithArgs(projectID, requester, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "cognito_sub", "role", "permissions", "created_by"}))

	e := echo.New()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/automation/tool-executions/"+executionID.String()+"/report", nil)
	context := e.NewContext(request, recorder)
	context.SetParamNames("id")
	context.SetParamValues(executionID.String())
	context.Set("cognito_sub", requester)
	context.Set("config", &models.Config{AutomationOutputBucket: "private-output"})
	_, _, err := authorizedToolExecutionReport(context)
	if err != nil {
		t.Fatalf("authorization denial should be a handled HTTP response: %v", err)
	}
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "Automation tool execution not found") {
		t.Fatalf("removed project member must receive the same not-found response as an absent report: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expected project membership to be checked before exposing the report: %v", err)
	}
}

func TestDeliveryTaskMemberCanViewPrivateExecutionEvidence(t *testing.T) {
	for _, member := range []models.DeliveryProjectMember{
		{Role: "owner"},
		{Role: "delivery_manager"},
		{Role: "reviewer"},
		{Role: "qa_reviewer"},
		{Role: "requester"},
		{Role: "viewer"},
		{Role: "custom", Permissions: `["delivery:view"]`},
	} {
		if !deliveryTaskMemberCanView(member) {
			t.Fatalf("delivery reader was denied private execution evidence: %#v", member)
		}
	}
	for _, member := range []models.DeliveryProjectMember{
		{Role: "custom"},
		{Role: "custom", Permissions: `["manage"]`},
		{Role: "custom", Permissions: `invalid`},
	} {
		if deliveryTaskMemberCanView(member) {
			t.Fatalf("unscoped project member gained private execution access: %#v", member)
		}
	}
}

func TestValidateCallbackArtifactsBindsQAAssetsToTheirTask(t *testing.T) {
	taskID := uuid.Must(uuid.NewV4())
	workItemID := uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationOutputBucket: "itbem-ai-outputs-local"}
	task := &models.AutomationTask{Operation: "delivery.qa", DeliveryWorkItemID: &workItemID}
	valid := callbackArtifact{
		Name:        "01-preview.png",
		Reference:   "s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/01-preview.png",
		ContentType: "image/png",
		SizeBytes:   120,
		SHA256:      strings.Repeat("a", 64),
	}
	if err := validateCallbackArtifacts(cfg, task, taskID, []callbackArtifact{valid}); err != nil {
		t.Fatalf("expected bounded QA artifact to be accepted: %v", err)
	}
	invalid := valid
	invalid.Reference = "s3://itbem-ai-outputs-local/automation/other/artifacts/01-preview.png"
	if err := validateCallbackArtifacts(cfg, task, taskID, []callbackArtifact{invalid}); err == nil {
		t.Fatal("expected artifact from another task to be rejected")
	}
	invalid = valid
	invalid.SHA256 = "untrusted"
	if err := validateCallbackArtifacts(cfg, task, taskID, []callbackArtifact{invalid}); err == nil {
		t.Fatal("expected artifact without a SHA-256 integrity digest to be rejected")
	}
	task.Operation = "delivery.plan"
	if err := validateCallbackArtifacts(cfg, task, taskID, []callbackArtifact{valid}); err == nil {
		t.Fatal("expected non-QA artifact callback to be rejected")
	}
}

func TestToolReportReferenceStaysInsideTheTaskArtifactNamespace(t *testing.T) {
	taskID := uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationOutputBucket: "itbem-ai-outputs-local"}
	valid := "s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/dashboard-semantic-qa.json"
	if !toolReportReferenceMatches(cfg, taskID, valid) {
		t.Fatal("expected the uploaded Stagehand report reference to be inspectable")
	}
	for _, reference := range []string{
		"s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/runs/other/result.json",
		"s3://itbem-ai-outputs-local/automation/other/artifacts/dashboard-semantic-qa.json",
		"s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/nested/semantic-qa.json",
		"s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/preview.png",
	} {
		if toolReportReferenceMatches(cfg, taskID, reference) {
			t.Fatalf("tool report inspector accepted an unrelated reference: %s", reference)
		}
	}
}

func TestPrivateToolExecutionReportRedactsNestedCredentialsAndPrivateReasoning(t *testing.T) {
	const providerKeyCanary = "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789"
	const apiKeyCanary = "nested-api-key-canary"
	const bearerCanary = "nested-bearer-canary"
	const urlCanary = "signed-url-query-canary"
	const reasoningCanary = "private-reasoning-canary"
	raw := []byte(`{
		"summary":"QA completed; api_key=` + apiKeyCanary + `",
		"status":"passed",
		"url":"https://preview.example.test/result?X-Amz-Signature=` + urlCanary + `&page=2#` + reasoningCanary + `",
		"usage":{"input_tokens":12,"output_tokens":8,"reasoning_tokens":3,"total_cost_microusd":21},
		"provider_items":[{"type":"reasoning","summary":"` + reasoningCanary + `"},{"role":"analysis","content":"` + reasoningCanary + `"},{"type":"message","content":"safe final answer"}],
		"calls":[{"request":{"headers":{"Authorization":"Bearer ` + bearerCanary + `","X-Api-Key":"` + apiKeyCanary + `"},"body":{"prompt":"safe request"}},
			"response":{"summary":"safe provider summary","text":"provider echoed ` + providerKeyCanary + `","reasoning_content":"` + reasoningCanary + `","chain_of_thought":"` + reasoningCanary + `","analysis":"` + reasoningCanary + `"}}]
	}`)
	sanitized, err := sanitizePrivateExecutionReport(raw)
	if err != nil {
		t.Fatalf("sanitizePrivateExecutionReport() error = %v", err)
	}
	for _, canary := range []string{providerKeyCanary, apiKeyCanary, bearerCanary, urlCanary, reasoningCanary} {
		if strings.Contains(string(sanitized), canary) {
			t.Fatalf("sanitized report retained a sensitive canary %q", canary)
		}
	}
	var result map[string]any
	if err := json.Unmarshal(sanitized, &result); err != nil {
		t.Fatalf("sanitized report is not valid JSON: %v", err)
	}
	if result["summary"] != "QA completed; api_key=<redacted>" || result["status"] != "passed" || result["url"] != "https://preview.example.test/result" {
		t.Fatalf("safe summary, status, or URL projection was not preserved: %#v", result)
	}
	usage, ok := result["usage"].(map[string]any)
	if !ok || usage["input_tokens"] != float64(12) || usage["output_tokens"] != float64(8) || usage["reasoning_tokens"] != float64(3) || usage["total_cost_microusd"] != float64(21) {
		t.Fatalf("operational token and cost metrics should remain available: %#v", result["usage"])
	}
	calls := result["calls"].([]any)
	response := calls[0].(map[string]any)["response"].(map[string]any)
	if response["summary"] != "safe provider summary" || response["text"] != "provider echoed [REDACTED]" {
		t.Fatalf("safe provider response fields were not preserved/redacted: %#v", response)
	}
	providerItems := result["provider_items"].([]any)
	if len(providerItems) != 1 || providerItems[0].(map[string]any)["content"] != "safe final answer" {
		t.Fatalf("private reasoning items should be omitted while normal provider output is retained: %#v", providerItems)
	}
	for _, privateField := range []string{"reasoning_content", "chain_of_thought", "analysis"} {
		if _, exists := response[privateField]; exists {
			t.Fatalf("private reasoning field %q must be omitted", privateField)
		}
	}
	request := calls[0].(map[string]any)["request"].(map[string]any)
	headers := request["headers"].(map[string]any)
	if len(headers) != 0 {
		t.Fatalf("credential-bearing headers must be omitted recursively: %#v", headers)
	}
}

func TestPrivateToolExecutionReportRejectsNonObjectAndInvalidJSON(t *testing.T) {
	for _, body := range [][]byte{[]byte(`[]`), []byte(`"not-an-object"`), []byte(`{invalid}`)} {
		if _, err := sanitizePrivateExecutionReport(body); err == nil {
			t.Fatalf("invalid or non-object report must fail closed: %q", body)
		}
	}
}

func TestImplementationHandoffCreatesAnAuditableLocalChangeSet(t *testing.T) {
	taskID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	branch := "itbem-agent/" + taskID.String()
	raw, err := json.Marshal(map[string]any{
		"workspace":          "workspace://events-backend",
		"worktree":           "workspace://events-backend#" + branch,
		"branch":             branch,
		"base_sha":           "0123456789abcdef0123456789abcdef01234567",
		"github_repository":  "itbem-corp/itbem-events-backend",
		"review_diff_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"diff_check_passed":  true,
		"validations":        []map[string]bool{{"passed": true}, {"passed": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	change, err := implementationChangeSetForHandoff(&models.AutomationTask{ID: taskID, Operation: "delivery.implementation", DeliveryWorkItemID: &workItemID}, raw, time.Now().UTC())
	if err != nil {
		t.Fatalf("expected bounded implementation handoff: %v", err)
	}
	if change.ReviewType != "local_worktree" || change.CIStatus != "passed" || change.RepositoryRef != "workspace://events-backend" {
		t.Fatalf("unexpected change set: %#v", change)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(change.MetadataJSON), &metadata); err != nil || metadata["verification_source"] != "itbem-local-agent" {
		t.Fatalf("missing verification provenance: %s", change.MetadataJSON)
	}
}

func TestImplementationHandoffRejectsMismatchedWorktree(t *testing.T) {
	taskID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	raw := []byte(`{"workspace":"workspace://events-backend","worktree":"workspace://events-backend#itbem-agent/other","branch":"itbem-agent/` + taskID.String() + `","base_sha":"0123456789abcdef0123456789abcdef01234567","diff_check_passed":true,"validations":[{"passed":true}]}`)
	if _, err := implementationChangeSetForHandoff(&models.AutomationTask{ID: taskID, Operation: "delivery.implementation", DeliveryWorkItemID: &workItemID}, raw, time.Now().UTC()); err == nil {
		t.Fatal("expected mismatched worktree handoff to be rejected")
	}
}

func TestImplementationHandoffCreatesOneImmutableReviewPerRepository(t *testing.T) {
	taskID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	branch := "itbem-agent/" + taskID.String()
	changeSet := func(workspace, digest string) map[string]any {
		return map[string]any{
			"workspace": workspace, "worktree": workspace + "#" + branch, "branch": branch,
			"base_sha": "0123456789abcdef0123456789abcdef01234567", "github_repository": "itbem-corp/" + strings.TrimPrefix(workspace, "workspace://"),
			"review_diff_sha256": digest, "diff_check_passed": true, "validations": []map[string]bool{{"passed": true}},
		}
	}
	raw, err := json.Marshal(map[string]any{"change_sets": []map[string]any{
		changeSet("workspace://backend", strings.Repeat("a", 64)),
		changeSet("workspace://dashboard", strings.Repeat("b", 64)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	changes, err := implementationChangeSetsForHandoff(&models.AutomationTask{ID: taskID, Operation: "delivery.implementation", DeliveryWorkItemID: &workItemID}, raw, time.Now().UTC())
	if err != nil || len(changes) != 2 || changes[0].RepositoryRef != "workspace://backend" || changes[1].RepositoryRef != "workspace://dashboard" {
		t.Fatalf("multi-repository handoff must persist independent reviews: %#v / %v", changes, err)
	}
	duplicate, _ := json.Marshal(map[string]any{"change_sets": []map[string]any{
		changeSet("workspace://backend", strings.Repeat("a", 64)),
		changeSet("workspace://backend", strings.Repeat("b", 64)),
	}})
	if _, err := implementationChangeSetsForHandoff(&models.AutomationTask{ID: taskID, Operation: "delivery.implementation", DeliveryWorkItemID: &workItemID}, duplicate, time.Now().UTC()); err == nil {
		t.Fatal("a repeated repository must fail closed")
	}
}

func TestDeliveryQAEvidenceTitlesDescribeResponsiveScreenshots(t *testing.T) {
	for name, expected := range map[string]string{
		"backend-preview-desktop.png":  "QA visual · Escritorio",
		"dashboard-preview-mobile.png": "QA visual · Móvil",
		"preview.png":                  "QA visual · Preview",
	} {
		if got := deliveryQAEvidenceTitle(name, "screenshot"); got != expected {
			t.Fatalf("title for %q = %q; want %q", name, got, expected)
		}
	}
	if got := deliveryQAEvidenceTitle("report.json", "artifact"); got != "Evidencia QA: report.json" {
		t.Fatalf("non-visual artifact title changed: %q", got)
	}
	for name, expected := range map[string]string{
		"dashboard-semantic-qa-case-01-before.png": "QA visual · Caso 01 · Antes",
		"dashboard-semantic-qa-case-01-after.png":  "QA visual · Caso 01 · Después",
	} {
		if got := deliveryQAEvidenceTitle(name, "screenshot"); got != expected {
			t.Fatalf("comparison title for %q = %q; want %q", name, got, expected)
		}
	}
	if key, role := deliveryQAEvidenceComparison("dashboard-semantic-qa-case-02-before.png"); key != "case-02" || role != "before" {
		t.Fatalf("case evidence pair metadata was not derived: %q / %q", key, role)
	}
	if key, role := deliveryQAEvidenceComparison("dashboard-semantic-qa-case-untrusted-before.png"); key != "" || role != "" {
		t.Fatalf("unbounded evidence name must not become a comparison pair: %q / %q", key, role)
	}
}

func TestDelegatedSubmissionOnlyAdvancesCompletedRoleHandoffs(t *testing.T) {
	cases := []struct {
		operation string
		state     string
		action    deliveryworkflow.Action
		phase     string
		allowed   bool
	}{
		{"delivery.implementation", deliveryworkflow.StateImplementation, deliveryworkflow.ActionSubmitCodeReview, "implementation", true},
		{"delivery.qa", deliveryworkflow.StateQARunning, deliveryworkflow.ActionSubmitQA, "qa", true},
		{"delivery.plan", deliveryworkflow.StatePlanning, "", "", false},
		{"delivery.release_gate", deliveryworkflow.StateReleaseReview, "", "", false},
		{"delivery.implementation", deliveryworkflow.StateCodeReview, deliveryworkflow.ActionSubmitCodeReview, "implementation", false},
	}
	for _, check := range cases {
		action, phase := delegatedSubmissionAction(check.operation)
		allowed := action != "" && delegatedSubmissionStateMatches(check.state, action)
		if action != check.action || phase != check.phase || allowed != check.allowed {
			t.Fatalf("delegated submission for %q/%q = (%q, %q, %t), want (%q, %q, %t)", check.operation, check.state, action, phase, allowed, check.action, check.phase, check.allowed)
		}
	}
}

func TestLocalAutomationInputProxyIsExplicitlyLocalOnly(t *testing.T) {
	t.Setenv("ENV", "local")
	if !localAutomationInputProxyAllowed() {
		t.Fatal("expected the authenticated input proxy to be available in local development")
	}
	for _, environment := range []string{"", "development", "staging", "production"} {
		t.Setenv("ENV", environment)
		if localAutomationInputProxyAllowed() {
			t.Fatalf("input proxy must not be available when ENV=%q", environment)
		}
	}
}

func TestPlanStepFailoverAdmissionIsSameProfileButDelegatesMachineToLeaseValidator(t *testing.T) {
	assignment, _, _, _, _, _, _ := validPlanStepAssignmentFixture()
	replacement := automationagent.AgentIdentity{
		WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: assignment.TargetAgentKey,
		MachineID: uuid.Must(uuid.NewV4()).String(),
	}
	if !planStepAssignmentTargetProfileMatches(assignment, replacement) {
		t.Fatal("same-profile replacement should reach the transactional lease and heartbeat validator")
	}
	replacement.AgentKey = "different-profile"
	if planStepAssignmentTargetProfileMatches(assignment, replacement) {
		t.Fatal("a different profile must never pass failover admission")
	}
	assignment.Status = models.DeliveryPlanStepAssignmentCompleted
	replacement.AgentKey = assignment.TargetAgentKey
	if planStepAssignmentTargetProfileMatches(assignment, replacement) {
		t.Fatal("terminal assignments must not be admitted for failover")
	}
}
