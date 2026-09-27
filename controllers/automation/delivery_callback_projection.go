package automation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"events-stocks/internal/automationagent"
	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/environmentevidence"
	"events-stocks/internal/projectvault"
	"events-stocks/internal/qaevidence"
	"events-stocks/internal/releasegate"
	"events-stocks/internal/releasegatecontrol"
	"events-stocks/internal/securityevidence"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var workspaceAttestationBranchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)

func automationLeaseRetryAfterSeconds(expiresAt, now time.Time) int64 {
	if !expiresAt.After(now) {
		return 0
	}
	remaining := expiresAt.Sub(now)
	seconds := int64(remaining / time.Second)
	if remaining%time.Second != 0 {
		seconds++
	}
	return seconds
}

// advanceDelegatedDeliverySubmission advances only non-decision workflow
// transitions after the authenticated worker has persisted its strict handoff.
// Human approvals, merge, and deployment remain outside this helper.
func advanceDelegatedDeliverySubmission(tx *gorm.DB, task *models.AutomationTask, completedAt time.Time) error {
	if tx == nil || task == nil || task.ID == uuid.Nil || task.DeliveryWorkItemID == nil || completedAt.IsZero() {
		return nil
	}
	action, phase := delegatedSubmissionAction(task.Operation)
	if action == "" {
		return nil
	}
	var item models.DeliveryWorkItem
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&item, *task.DeliveryWorkItemID).Error; err != nil {
		return err
	}
	if !delegatedSubmissionStateMatches(item.State, action) {
		return nil
	}
	var event models.DeliveryEvent
	err := tx.Where("work_item_id = ? AND event_type = ?", item.ID, deliveryledger.EventTypeAutonomySnapshot).Order("sequence ASC").First(&event).Error
	if err == gorm.ErrRecordNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	snapshot, err := deliveryledger.ProjectAutonomySnapshot(event)
	if err != nil || snapshot.ProjectID != item.ProjectID || !snapshot.Delegated {
		if err != nil {
			return fmt.Errorf("delegated delivery authority is invalid: %w", err)
		}
		return nil
	}
	if err := recordDelegatedSubmissionEvidence(tx, *task, item.ID, phase, completedAt); err != nil {
		return err
	}
	if err := deliveryworkflow.Advance(&item, action, nil, completedAt); err != nil {
		return err
	}
	return tx.Save(&item).Error
}

func delegatedSubmissionAction(operation string) (deliveryworkflow.Action, string) {
	switch strings.TrimSpace(operation) {
	case "delivery.implementation":
		return deliveryworkflow.ActionSubmitCodeReview, "implementation"
	case "delivery.assessment":
		return deliveryworkflow.ActionSubmitAssessment, "assessment"
	case "delivery.qa":
		return deliveryworkflow.ActionSubmitQA, "qa"
	default:
		return "", ""
	}
}

func delegatedSubmissionStateMatches(state string, action deliveryworkflow.Action) bool {
	switch action {
	case deliveryworkflow.ActionSubmitCodeReview, deliveryworkflow.ActionSubmitAssessment:
		return strings.TrimSpace(state) == deliveryworkflow.StateImplementation
	case deliveryworkflow.ActionSubmitQA:
		return strings.TrimSpace(state) == deliveryworkflow.StateQARunning
	default:
		return false
	}
}

func recordDelegatedSubmissionEvidence(tx *gorm.DB, task models.AutomationTask, workItemID uuid.UUID, phase string, completedAt time.Time) error {
	if strings.TrimSpace(task.OutputRef) == "" {
		return fmt.Errorf("delegated %s submission has no private result reference", phase)
	}
	var existing models.DeliveryEvidence
	err := tx.Where("work_item_id = ? AND reference = ?", workItemID, task.OutputRef).First(&existing).Error
	if err == nil {
		return nil
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}
	evidence := models.DeliveryEvidence{
		WorkItemID: workItemID, Kind: "report", Phase: phase,
		Title: "Resultado del agente: " + phase, Reference: task.OutputRef,
		MetadataJSON: fmt.Sprintf(`{"automation_task_id":%q,"operation":%q,"provider":%q,"model":%q,"submission_authority":"delegated"}`, task.ID.String(), task.Operation, task.Provider, task.Model),
		CapturedBy:   "itbem-control-plane", CapturedAt: &completedAt,
	}
	return tx.Create(&evidence).Error
}

func persistCodeReviewPublication(tx *gorm.DB, task *models.AutomationTask, raw json.RawMessage, completedAt time.Time) error {
	publication, err := codeReviewPublicationForTask(task, raw)
	if err != nil {
		return err
	}
	publication.AutomationTaskID, publication.PublishedAt = task.ID, publication.PublishedAt.UTC()
	if publication.PublishedAt.After(completedAt.Add(time.Minute)) {
		return fmt.Errorf("code review publication time is invalid")
	}
	return tx.Create(&publication).Error
}

func codeReviewPublicationForTask(task *models.AutomationTask, raw json.RawMessage) (models.AutomationCodeReviewPublication, error) {
	if task == nil || task.ID == uuid.Nil || task.Operation != "code.review" || task.RequestedBy != "github-app-review" || !artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(task.EvidenceSubjectDigest))) {
		return models.AutomationCodeReviewPublication{}, fmt.Errorf("only an exact webhook review task may publish GitHub review evidence")
	}
	var execution automationagent.GitHubCodeReviewPublication
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&execution); err != nil {
		return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review publication evidence is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review publication evidence is invalid")
	}
	repository := strings.ToLower(strings.TrimSpace(execution.Repository))
	verdict := strings.ToLower(strings.TrimSpace(execution.Verdict))
	event := strings.ToUpper(strings.TrimSpace(execution.Event))
	actor := strings.ToLower(strings.TrimSpace(execution.ReviewerActor))
	author := strings.ToLower(strings.TrimSpace(execution.AuthorActor))
	checkName := strings.TrimSpace(execution.CheckName)
	checkConclusion := strings.ToLower(strings.TrimSpace(execution.CheckConclusion))
	if execution.SchemaVersion != 2 || !githubRepositoryPattern.MatchString(repository) || execution.PullRequest < 1 || !gitCommitSHA.MatchString(strings.ToLower(strings.TrimSpace(execution.HeadSHA))) || !artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(execution.PatchSHA256))) || !artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(execution.SubjectSHA256))) || !artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(execution.PayloadSHA256))) || !strings.EqualFold(execution.SubjectSHA256, task.EvidenceSubjectDigest) || execution.ReviewID < 1 || actor == "" || execution.CheckRunID < 1 || checkName != "Bema Review / exact-sha" || (checkConclusion != "success" && checkConclusion != "failure") || execution.PublishedAt.IsZero() {
		return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review publication evidence is invalid")
	}
	expectedCorrelationID, correlationErr := githubReviewCorrelationID(repository, execution.PullRequest, execution.HeadSHA)
	if correlationErr != nil || task.CorrelationID != expectedCorrelationID || !validGitHubReviewURL(execution.ReviewURL, repository, execution.PullRequest, execution.ReviewID) || !validGitHubCheckRunURL(execution.CheckRunURL) {
		return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review publication does not match its queued pull request")
	}
	switch event {
	case "APPROVE":
		if verdict != "approve" || !execution.ReviewGatePassed || author == "" || strings.EqualFold(actor, author) || checkConclusion != "success" {
			return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review approval is not independent")
		}
	case "REQUEST_CHANGES":
		if verdict != "request_changes" || execution.ReviewGatePassed || checkConclusion != "failure" {
			return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review event contradicts its verdict")
		}
	case "COMMENT":
		nonBlockingComment := verdict == "comment" && execution.ReviewGatePassed && author != "" && !strings.EqualFold(actor, author) && checkConclusion == "success"
		blockingComment := (verdict == "comment" || verdict == "blocked" || (verdict == "approve" && author != "" && strings.EqualFold(actor, author))) && !execution.ReviewGatePassed && checkConclusion == "failure"
		if !nonBlockingComment && !blockingComment {
			return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review comment contradicts its verdict")
		}
	default:
		return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review event is invalid")
	}
	checkRunID, checkRunURL := execution.CheckRunID, strings.TrimSpace(execution.CheckRunURL)
	return models.AutomationCodeReviewPublication{
		Repository: repository, PullRequest: execution.PullRequest, HeadSHA: strings.ToLower(execution.HeadSHA), PatchSHA256: strings.ToLower(execution.PatchSHA256),
		SubjectSHA256: strings.ToLower(execution.SubjectSHA256), PayloadSHA256: strings.ToLower(execution.PayloadSHA256), Verdict: verdict, Event: event, ReviewGatePassed: execution.ReviewGatePassed,
		ReviewID: execution.ReviewID, ReviewURL: strings.TrimSpace(execution.ReviewURL), ReviewerActor: actor, AuthorActor: author, PublishedAt: execution.PublishedAt,
		CheckRunID: &checkRunID, CheckRunURL: &checkRunURL, CheckName: &checkName, CheckConclusion: &checkConclusion,
	}, nil
}

func validGitHubCheckRunURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Scheme == "https" && strings.EqualFold(parsed.Hostname(), "github.com") && parsed.User == nil && parsed.RawQuery == "" && strings.Trim(parsed.Path, "/") != ""
}

func validGitHubReviewURL(value, repository string, pullRequest int, reviewID int64) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil || parsed.RawQuery != "" || pullRequest < 1 || reviewID < 1 {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || !strings.EqualFold(parts[0]+"/"+parts[1], repository) || parts[2] != "pull" || parts[3] != strconv.Itoa(pullRequest) {
		return false
	}
	return parsed.Fragment == "pullrequestreview-"+strconv.FormatInt(reviewID, 10)
}

func persistOnboardingCapabilityProbes(tx *gorm.DB, task *models.AutomationTask, raw json.RawMessage, completedAt time.Time) error {
	if tx == nil || completedAt.IsZero() {
		return fmt.Errorf("only a bounded onboarding task may append capability probes")
	}
	execution, queuedSubject, err := onboardingCapabilityProbeForTask(task, raw)
	if err != nil {
		return err
	}
	var onboarding models.DeliveryRepositoryOnboarding
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", *task.DeliveryOnboardingID).First(&onboarding).Error; err != nil {
		return err
	}
	if onboarding.Status != "proposed" || !strings.EqualFold(onboarding.ProposalSHA256, queuedSubject) || onboarding.RepositoryReference != execution.RepositoryReference || onboarding.DefaultBranch != execution.DefaultBranch || !strings.EqualFold(onboarding.Revision, execution.Revision) {
		return fmt.Errorf("onboarding capability probe subject is stale or mismatched")
	}
	proposal, err := projectvault.ValidateStoredProposal(onboarding.ProposalJSON, onboarding.RepositoryReference, onboarding.DefaultBranch, onboarding.Revision, onboarding.Readiness, onboarding.ProposalSHA256, onboarding.VaultSHA256)
	if err != nil {
		return err
	}
	updated, err := projectvault.ApplyCapabilityProbes(proposal, execution.Probes)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		return err
	}
	proposalDigest, err := projectvault.ProposalSHA256(updated)
	if err != nil {
		return err
	}
	for _, probe := range execution.Probes {
		row := models.DeliveryRepositoryCapabilityProbe{
			ProjectID: onboarding.ProjectID, OnboardingID: onboarding.ID, AutomationTaskID: task.ID,
			RepositoryReference: execution.RepositoryReference, Revision: execution.Revision,
			Capability: probe.Name, State: probe.State, ExecutorRole: execution.ExecutorRole,
			EvidenceSHA256: strings.ToLower(probe.EvidenceSHA256), SubjectSHA256: strings.ToLower(probe.SubjectSHA256),
			Reason: probe.Reason, ObservedAt: completedAt.UTC(),
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	result := tx.Model(&models.DeliveryRepositoryOnboarding{}).Where("id = ? AND status = ? AND proposal_sha256 = ?", onboarding.ID, "proposed", onboarding.ProposalSHA256).
		Updates(map[string]any{"proposal_json": string(encoded), "proposal_sha256": proposalDigest, "readiness": updated.Readiness})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("onboarding capability probe lost its proposal checkpoint")
	}
	return nil
}

func onboardingCapabilityProbeForTask(task *models.AutomationTask, raw json.RawMessage) (automationagent.OnboardingProbeExecution, string, error) {
	if task == nil || task.ID == uuid.Nil || task.Operation != "delivery.onboarding_probe" || task.DeliveryOnboardingID == nil || *task.DeliveryOnboardingID == uuid.Nil {
		return automationagent.OnboardingProbeExecution{}, "", fmt.Errorf("only a bounded onboarding task may append capability probes")
	}
	execution, err := automationagent.DecodeOnboardingProbeExecution(raw)
	if err != nil || execution.TaskID != task.ID.String() {
		return automationagent.OnboardingProbeExecution{}, "", fmt.Errorf("onboarding capability probes do not match their automation task")
	}
	queuedSubject := strings.ToLower(strings.TrimSpace(task.EvidenceSubjectDigest))
	if !artifactDigestPattern.MatchString(queuedSubject) {
		return automationagent.OnboardingProbeExecution{}, "", fmt.Errorf("onboarding capability probe task has no exact proposal subject")
	}
	return execution, queuedSubject, nil
}

func persistReleaseGateEvaluation(tx *gorm.DB, task *models.AutomationTask, raw json.RawMessage, completedAt time.Time) error {
	if tx == nil || completedAt.IsZero() {
		return fmt.Errorf("only a bounded release Gatekeeper task may append an evaluation")
	}
	workItemID, actor, input, environment, err := releaseGateCandidateForTask(task, raw)
	if err != nil {
		return err
	}
	if environment != nil {
		if _, _, err := deliveryledger.RecordEnvironmentObservation(tx, workItemID, *environment, completedAt.UTC()); err != nil {
			return err
		}
	}
	input, err = releasegatecontrol.Resolve(tx, workItemID, input, completedAt.UTC())
	if err != nil {
		return err
	}
	preApproval := releasegate.Evaluate(input)
	if preApproval.SubjectDigest == "" {
		return fmt.Errorf("release Gatekeeper execution has no exact subject")
	}
	input.HumanApproval = &releasegate.HumanApproval{Actor: actor, ActorType: "human", SubjectDigest: preApproval.SubjectDigest, Approved: true}
	if _, _, err := deliveryledger.RecordGateEvaluation(tx, workItemID, input, completedAt.UTC()); err != nil {
		return err
	}
	return nil
}

func persistQAObservation(tx *gorm.DB, task *models.AutomationTask, raw json.RawMessage, completedAt time.Time) error {
	if tx == nil || completedAt.IsZero() {
		return fmt.Errorf("only a bounded QA task may append an observation")
	}
	workItemID, observation, err := qaObservationForTask(task, raw)
	if err != nil {
		return err
	}
	if _, _, err := deliveryledger.RecordQAObservation(tx, workItemID, observation, completedAt.UTC()); err != nil {
		return err
	}
	security, complete, err := securityObservationFromQA(observation)
	if err != nil {
		return err
	}
	if complete {
		if _, _, err := deliveryledger.RecordSecurityObservation(tx, workItemID, security, completedAt.UTC()); err != nil {
			return err
		}
	}
	return nil
}

const (
	securitySecretsTestKind      = "security:secrets"
	securityHighCriticalTestKind = "security:high-critical"
)

func securityObservationFromQA(observation qaevidence.Observation) (securityevidence.Observation, bool, error) {
	if err := qaevidence.Validate(observation); err != nil {
		return securityevidence.Observation{}, false, err
	}
	repositories := make([]securityevidence.Repository, 0, len(observation.Repositories))
	for _, repository := range observation.Repositories {
		commands := make(map[string]qaevidence.Command, len(repository.Commands))
		for _, command := range repository.Commands {
			commands[strings.ToLower(command.Kind)] = command
		}
		secretScan, hasSecretScan := commands[securitySecretsTestKind]
		highCritical, hasHighCritical := commands[securityHighCriticalTestKind]
		if !hasSecretScan || !hasHighCritical {
			return securityevidence.Observation{}, false, nil
		}
		highFindings := 0
		if !highCritical.Passed {
			highFindings = 1
		}
		repositories = append(repositories, securityevidence.Repository{
			Reference: repository.Reference, Branch: repository.Branch, SecretScanPassed: secretScan.Passed,
			HighFindings: highFindings, CriticalFindings: 0,
		})
	}
	security := securityevidence.Observation{
		SchemaVersion: securityevidence.SchemaVersion, TaskID: observation.TaskID, MatrixDigest: observation.MatrixDigest, Repositories: repositories,
	}
	if err := securityevidence.Validate(security); err != nil {
		return securityevidence.Observation{}, false, err
	}
	return security, true, nil
}

func qaObservationForTask(task *models.AutomationTask, raw json.RawMessage) (uuid.UUID, qaevidence.Observation, error) {
	if task == nil || task.ID == uuid.Nil || task.Operation != "delivery.qa" || task.DeliveryWorkItemID == nil || *task.DeliveryWorkItemID == uuid.Nil {
		return uuid.Nil, qaevidence.Observation{}, fmt.Errorf("only a bounded QA task may append an observation")
	}
	observation, err := qaevidence.Decode(raw)
	if err != nil {
		return uuid.Nil, qaevidence.Observation{}, err
	}
	subject := strings.ToLower(strings.TrimSpace(task.EvidenceSubjectDigest))
	if observation.TaskID != task.ID.String() || !strings.EqualFold(observation.MatrixDigest, subject) || !artifactDigestPattern.MatchString(subject) {
		return uuid.Nil, qaevidence.Observation{}, fmt.Errorf("QA observation does not match its exact queued subject")
	}
	return *task.DeliveryWorkItemID, observation, nil
}

func releaseGateCandidateForTask(task *models.AutomationTask, raw json.RawMessage) (uuid.UUID, string, releasegate.Input, *environmentevidence.Observation, error) {
	if task == nil || task.Operation != "delivery.release_gate" || task.DeliveryWorkItemID == nil || *task.DeliveryWorkItemID == uuid.Nil {
		return uuid.Nil, "", releasegate.Input{}, nil, fmt.Errorf("only a bounded release Gatekeeper task may append an evaluation")
	}
	actor := strings.TrimSpace(task.RequestedBy)
	if actor == "" || actor == "github-app-review" || actor == "itbem-local-agent" || actor == "itbem-github-app" {
		return uuid.Nil, "", releasegate.Input{}, nil, fmt.Errorf("release Gatekeeper task does not have an authenticated human requester")
	}
	var handoff map[string]json.RawMessage
	if err := json.Unmarshal(raw, &handoff); err != nil || (len(handoff) != 2 && len(handoff) != 3) {
		return uuid.Nil, "", releasegate.Input{}, nil, fmt.Errorf("release Gatekeeper execution metadata is invalid")
	}
	var schemaVersion int
	if err := json.Unmarshal(handoff["schema_version"], &schemaVersion); err != nil || (schemaVersion != 1 && schemaVersion != 2) || (schemaVersion == 1 && len(handoff) != 2) || (schemaVersion == 2 && len(handoff) != 3) {
		return uuid.Nil, "", releasegate.Input{}, nil, fmt.Errorf("release Gatekeeper execution schema is invalid")
	}
	input, err := releasegate.DecodeInput(handoff["gatekeeper_input"])
	if err != nil || input.SchemaVersion != releasegate.SchemaVersion || input.Action != releasegate.ActionRelease || input.ChangeSetID != task.DeliveryWorkItemID.String() || input.HumanApproval != nil {
		return uuid.Nil, "", releasegate.Input{}, nil, fmt.Errorf("release Gatekeeper execution candidate is invalid")
	}
	if schemaVersion == 1 {
		return *task.DeliveryWorkItemID, actor, input, nil, nil
	}
	environment, err := environmentevidence.Decode(handoff["environment_observation"])
	subject := strings.ToLower(strings.TrimSpace(task.EvidenceSubjectDigest))
	if err != nil || environment.TaskID != task.ID.String() || !strings.EqualFold(environment.MatrixDigest, subject) || !artifactDigestPattern.MatchString(subject) {
		return uuid.Nil, "", releasegate.Input{}, nil, fmt.Errorf("release environment observation does not match its exact queued subject")
	}
	return *task.DeliveryWorkItemID, actor, input, &environment, nil
}
