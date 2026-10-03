package delivery

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/deliverypolicy"
	"events-stocks/internal/qaevidence"
	"events-stocks/internal/releasegate"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const delegatedCoordinatorActor = "delivery-gatekeeper"
const maximumDelegatedCorrections = 3

type delegatedQADecision struct {
	SchemaVersion int      `json:"schema_version"`
	MatrixDigest  string   `json:"matrix_digest"`
	Failures      []string `json:"failures"`
}

// Called under the continuation's work-item UPDATE lock. Only the sealed
// observation decides the gate; the model's verdict and prose grant no authority.
func coordinateDelegatedQA(tx *gorm.DB, item *models.DeliveryWorkItem, intent models.DeliveryContinuation, task models.AutomationTask, now time.Time) (bool, error) {
	if !task.QASourceReceiptRequired {
		return false, nil
	} // Historical contract remains human-gated.
	var authorityEvent models.DeliveryEvent
	err := tx.Where("work_item_id = ? AND event_type = ?", item.ID, deliveryledger.EventTypeAutonomySnapshot).Order("sequence ASC").First(&authorityEvent).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	snapshot, err := deliveryledger.ProjectAutonomySnapshot(authorityEvent)
	if err != nil {
		return false, err
	}
	if snapshot.ProjectID != item.ProjectID || snapshot.ChangeSetID != item.ID.String() {
		return false, fmt.Errorf("delegated authority belongs to another delivery")
	}
	if !snapshot.Delegated {
		return false, nil
	}
	mandate, err := resolveDeliveryMandate(*item, nil)
	if err != nil {
		return false, err
	}
	for _, action := range mandate.HumanActions {
		if action == "approve_qa" {
			return false, nil
		}
	}
	if item.AutomationEpoch != intent.Epoch || (item.State != deliveryworkflow.StateQARunning && item.State != deliveryworkflow.StateQAReview) || task.Operation != "delivery.qa" || task.Status != "completed" || task.ID == uuid.Nil || task.DeliveryWorkItemID == nil || *task.DeliveryWorkItemID != item.ID || task.ContinuationID == nil || *task.ContinuationID != intent.ID || task.AgentInstanceID == nil || *task.AgentInstanceID == uuid.Nil || task.CompletedAt == nil || task.CreatedAt.IsZero() || task.OutputRef == "" {
		return false, fmt.Errorf("delegated QA requires the current completed signed continuation")
	}
	authority, err := deliveryledger.ReadAutonomyAuthority(authorityEvent)
	if err != nil {
		return false, err
	}
	var observationEvent models.DeliveryEvent
	if err := tx.Where("work_item_id = ? AND event_type = ? AND dedupe_key = ?", item.ID, deliveryledger.EventTypeQAObserved, item.ID.String()+":qa-observation-v2:"+task.ID.String()).First(&observationEvent).Error; err != nil {
		return false, err
	}
	observed, err := deliveryledger.ProjectQAObservation(observationEvent)
	if err != nil {
		return false, err
	}
	if observed.OccurredAt.Before(task.CreatedAt) || observed.OccurredAt.After(task.CompletedAt.Add(time.Minute)) || observed.OccurredAt.After(now.Add(time.Minute)) || now.Sub(observed.OccurredAt) > 24*time.Hour {
		return false, fmt.Errorf("delegated QA observation is stale or outside its task lifetime")
	}
	var changes []models.DeliveryChangeSet
	if err := tx.Where("work_item_id = ?", item.ID).Order("created_at DESC, id DESC").Find(&changes).Error; err != nil {
		return false, err
	}
	candidate, err := storedReleaseGateCandidate(*item, changes)
	if err != nil {
		return false, err
	}
	repositories, err := delegatedQARepositoryBoundaries(item.PlanJSON, authority, candidate, changes)
	if err != nil {
		return false, err
	}
	required, err := codeReviewRequiredRepositories(item.PlanJSON)
	if err != nil || currentReviewedPreview(required, changes) == "" || currentReviewedPreview(required, changes) != item.PreviewURL {
		return false, fmt.Errorf("delegated QA preview no longer matches the latest reviewed implementation")
	}
	decision, err := evaluateDelegatedQA(task, observed.Observation, candidate, repositories)
	if err != nil {
		return false, err
	}
	// The server creates source receipts. Completion already enforced them;
	// rebind their commits and independent review to the current published matrix.
	var receipts []models.AutomationQASourceReceipt
	if err := tx.Where("task_id = ?", task.ID).Find(&receipts).Error; err != nil {
		return false, err
	}
	if err := validateDelegatedQASourceReceipts(task, observed.Observation, candidate, repositories, receipts, changes); err != nil {
		return false, err
	}
	for _, revision := range candidate.Revisions {
		pullRequest, err := delegatedQAPullRequest(revision, repositories, changes)
		if err != nil {
			return false, err
		}
		var review models.AutomationCodeReviewPublication
		if err := tx.Where("repository = ? AND pull_request = ? AND head_sha = ?", strings.ToLower(revision.Repository), pullRequest, revision.SHA).Order("published_at DESC, review_id DESC").First(&review).Error; err != nil {
			return false, err
		}
		if !validDelegatedIndependentReview(review, revision, pullRequest, now) {
			return false, fmt.Errorf("delegated QA lacks a current independent exact-SHA review")
		}
	}
	// Retain the report from this exact verified task for subsequent phases.
	var reportCount int64
	if err := tx.Model(&models.DeliveryEvidence{}).Where("work_item_id = ? AND reference = ?", item.ID, task.OutputRef).Count(&reportCount).Error; err != nil {
		return false, err
	}
	if reportCount == 0 {
		metadata, _ := json.Marshal(map[string]string{"automation_task_id": task.ID.String(), "operation": task.Operation, "matrix_digest": decision.MatrixDigest})
		report := models.DeliveryEvidence{WorkItemID: item.ID, Kind: "report", Phase: "qa", Title: "Resultado del agente: qa", Reference: task.OutputRef, MetadataJSON: string(metadata), CapturedBy: delegatedCoordinatorActor, CapturedAt: task.CompletedAt}
		if err := tx.Create(&report).Error; err != nil {
			return false, err
		}
	}
	if len(decision.Failures) > 0 {
		var previous []models.DeliveryGate
		if err := tx.Where("work_item_id = ? AND decision = ? AND kind IN ?", item.ID, deliveryworkflow.DecisionChangesRequested, []string{deliveryworkflow.GateCodeReview, deliveryworkflow.GateQAReview}).Order("decided_at DESC, id DESC").Find(&previous).Error; err != nil {
			return false, err
		}
		if err := validateDelegatedCorrectionBudget(decision, previous); err != nil {
			return false, err
		}
		if err := cloneApprovedCorrectionPlan(tx, *item, now); err != nil {
			return false, err
		}
	}
	if item.State == deliveryworkflow.StateQARunning {
		if err := deliveryworkflow.Advance(item, deliveryworkflow.ActionSubmitQA, nil, now); err != nil {
			return false, err
		}
	}
	action, phase := deliveryworkflow.ActionApproveQA, "summary"
	if len(decision.Failures) > 0 {
		action, phase = deliveryworkflow.ActionRequestQAChanges, "implementation"
	}
	comment, _ := json.Marshal(decision)
	checklist, _ := json.Marshal([]string{"autonomy_event:" + authorityEvent.ID.String(), "qa_event:" + observationEvent.ID.String(), "qa_task:" + task.ID.String(), "matrix_digest:" + decision.MatrixDigest})
	gate := gate(action, item.ID, delegatedCoordinatorActor, string(comment), string(checklist))
	gate.Authority = "delegated"
	if err := deliveryworkflow.Advance(item, action, gate, now); err != nil {
		return false, err
	}
	if err := tx.Create(gate).Error; err != nil {
		return false, err
	}
	if err := invalidatePublicationGrantsForTransition(tx, item.ID, delegatedCoordinatorActor, action, now); err != nil {
		return false, err
	}
	item.AutomationEpoch++
	item.AgentProgress, item.BlockedReason = "queued", ""
	if err := scheduleContinuation(tx, *item, phase, intent.RequestedBy, ""); err != nil {
		return false, err
	}
	next := "la preparación del informe"
	if phase == "implementation" {
		next = "la corrección del código dentro del plan aprobado"
	}
	if err := recordContinuationMessage(tx, intent, "ready", "El coordinador verificó la evidencia independiente de QA y programó "+next+". No autorizó merge ni despliegue.", map[string]any{"gate_authority": "delegated", "qa_event_id": observationEvent.ID.String(), "matrix_digest": decision.MatrixDigest}); err != nil {
		return false, err
	}
	if err := tx.Save(item).Error; err != nil {
		return false, err
	}
	return true, tx.Model(&intent).Update("status", "done").Error
}

func delegatedQARepositoryBoundaries(plan string, authority deliveryledger.AutonomySnapshotInput, candidate releasegate.Input, changes []models.DeliveryChangeSet) (map[string]deliveryledger.AutonomyRepository, error) {
	required, err := codeReviewRequiredRepositories(plan)
	if err != nil || len(required) == 0 {
		return nil, fmt.Errorf("delegated QA requires explicit changed repositories")
	}
	result := map[string]deliveryledger.AutonomyRepository{}
	for _, repository := range authority.Repositories {
		if _, changed := required[repository.SourceReference]; !changed {
			continue
		}
		for _, revision := range candidate.Revisions {
			if !strings.EqualFold(repository.Repository, "github://"+revision.Repository) {
				continue
			}
			if !repository.Policy.AllowsTargetBranch(revision.Branch) || (repository.Policy.Mode != deliverypolicy.ModeMerge && repository.Policy.Mode != deliverypolicy.ModeRelease) {
				return nil, fmt.Errorf("delegated QA revision exceeds frozen repository policy")
			}
			for _, change := range changes {
				if change.RepositoryRef == repository.SourceReference && change.CommitSHA == revision.SHA && validPublishedChangeRecord(change) {
					result[repository.SourceReference] = repository
					break
				}
			}
		}
	}
	if len(result) != len(required) || len(result) != len(candidate.Revisions) {
		return nil, fmt.Errorf("delegated QA matrix does not match frozen authority")
	}
	return result, nil
}

func evaluateDelegatedQA(task models.AutomationTask, observation qaevidence.Observation, candidate releasegate.Input, repositories map[string]deliveryledger.AutonomyRepository) (delegatedQADecision, error) {
	if err := qaevidence.Validate(observation); err != nil {
		return delegatedQADecision{}, err
	}
	matrix, err := releasegate.RevisionMatrixDigest(candidate.Revisions)
	if err != nil || observation.TaskID != task.ID.String() || observation.MatrixDigest != matrix || task.EvidenceSubjectDigest != matrix || len(observation.Repositories) != len(repositories) || !observation.PreviewPassed {
		return delegatedQADecision{}, fmt.Errorf("delegated QA observation does not prove the current task, matrix and preview")
	}
	decision := delegatedQADecision{SchemaVersion: 1, MatrixDigest: matrix, Failures: []string{}}
	for _, observed := range observation.Repositories {
		repository, exists := repositories[observed.Reference]
		if !exists || repository.Policy.GateApprovalMode != deliverypolicy.GateApprovalDelegated || !repository.Policy.Resolved || !repository.Policy.Safety.IndependentReview || !repository.Policy.Safety.ExactSHAEvidence || !repository.Policy.Safety.SecretScan || repository.Policy.Safety.ForceMergeAllowed || len(repository.Policy.RequiredTestKinds) == 0 {
			return delegatedQADecision{}, fmt.Errorf("QA repository has no complete frozen delegated authority")
		}
		commands := map[string]qaevidence.Command{}
		for _, command := range observed.Commands {
			if !command.Passed && strings.HasPrefix(command.Kind, "security:") {
				return delegatedQADecision{}, fmt.Errorf("delegated QA requires security escalation")
			}
			if !command.Passed && strings.TrimSpace(command.Kind) == "" {
				return delegatedQADecision{}, fmt.Errorf("delegated correction requires an operator test identity")
			}
			commands[command.Kind] = command
			if !command.Passed {
				decision.Failures = append(decision.Failures, observed.Reference+"/"+command.Kind)
			}
		}
		for _, kind := range append(append([]string(nil), repository.Policy.RequiredTestKinds...), "security:secrets", "security:high-critical") {
			command, found := commands[kind]
			if !found {
				return delegatedQADecision{}, fmt.Errorf("delegated QA lacks required operator test identity %s", kind)
			}
			if strings.HasPrefix(kind, "security:") && !command.Passed {
				return delegatedQADecision{}, fmt.Errorf("delegated QA requires security escalation")
			}
		}
	}
	sort.Strings(decision.Failures)
	return decision, nil
}

func validDelegatedIndependentReview(review models.AutomationCodeReviewPublication, revision releasegate.Revision, pullRequest int, now time.Time) bool {
	return strings.EqualFold(review.Repository, revision.Repository) && review.PullRequest == pullRequest && review.HeadSHA == revision.SHA && review.ReviewGatePassed && review.ReviewID > 0 && review.ReviewerActor != "" && review.AuthorActor != "" && !strings.EqualFold(review.ReviewerActor, review.AuthorActor) && review.CheckRunID != nil && *review.CheckRunID > 0 && review.CheckName != nil && *review.CheckName == "Bema Review / exact-sha" && review.CheckConclusion != nil && *review.CheckConclusion == "success" && !review.PublishedAt.IsZero() && !review.PublishedAt.After(now.Add(time.Minute))
}

func delegatedQAPullRequest(revision releasegate.Revision, repositories map[string]deliveryledger.AutonomyRepository, changes []models.DeliveryChangeSet) (int, error) {
	for reference, repository := range repositories {
		if !strings.EqualFold(repository.Repository, "github://"+revision.Repository) {
			continue
		}
		for _, change := range changes {
			if change.RepositoryRef != reference || !validPublishedChangeRecord(change) || change.ReviewType != "pull_request" {
				continue
			}
			if change.CommitSHA != revision.SHA || change.CIStatus != "passed" || !releaseGatePullRequestURL(change.PullRequestURL, revision.Repository) {
				return 0, fmt.Errorf("latest QA publication is not the current passed PR")
			}
			parsed, err := url.Parse(change.PullRequestURL)
			if err != nil {
				return 0, err
			}
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			number, err := strconv.Atoi(parts[3])
			if err != nil || number < 1 {
				return 0, fmt.Errorf("QA publication has no valid PR number")
			}
			return number, nil
		}
	}
	return 0, fmt.Errorf("QA publication does not match frozen repositories")
}

func validateDelegatedQASourceReceipts(task models.AutomationTask, observation qaevidence.Observation, candidate releasegate.Input, repositories map[string]deliveryledger.AutonomyRepository, receipts []models.AutomationQASourceReceipt, changes []models.DeliveryChangeSet) error {
	if task.AgentInstanceID == nil || *task.AgentInstanceID == uuid.Nil {
		return fmt.Errorf("QA source receipt requires an enrolled identity")
	}
	for _, observed := range observation.Repositories {
		repository, exists := repositories[observed.Reference]
		if !exists {
			return fmt.Errorf("QA source receipt has an unknown repository")
		}
		matched := false
		publishedBranch := ""
		for _, change := range changes {
			if change.RepositoryRef == observed.Reference && validPublishedChangeRecord(change) && change.ReviewType == "pull_request" {
				publishedBranch = change.Branch
				break
			}
		}
		if publishedBranch == "" || publishedBranch != observed.Branch {
			return fmt.Errorf("QA observed branch is not the current published branch")
		}
		for _, revision := range candidate.Revisions {
			if !strings.EqualFold(repository.Repository, "github://"+revision.Repository) {
				continue
			}
			for _, receipt := range receipts {
				if receipt.TaskID == task.ID && receipt.AgentInstanceID == *task.AgentInstanceID && receipt.Reference == observed.Reference && receipt.MatrixDigest == observation.MatrixDigest && strings.EqualFold(receipt.Repository, revision.Repository) && receipt.CommitSHA == revision.SHA && receipt.Branch == observed.Branch && validApprovedPlanHash(receipt.PackSHA256) && receipt.PackBytes > 0 && !receipt.AcquiredAt.Before(task.CreatedAt) && task.CompletedAt != nil && !receipt.AcquiredAt.After(*task.CompletedAt) {
					matched = true
				}
			}
		}
		if !matched {
			return fmt.Errorf("QA lacks the server source receipt for its exact observed checkout")
		}
	}
	return nil
}

func validateDelegatedCorrectionBudget(decision delegatedQADecision, previous []models.DeliveryGate) error {
	if len(previous) >= maximumDelegatedCorrections {
		return fmt.Errorf("delegated correction budget exhausted")
	}
	for _, gate := range previous {
		if gate.Kind != deliveryworkflow.GateQAReview {
			continue
		}
		var last delegatedQADecision
		if json.Unmarshal([]byte(gate.Comment), &last) == nil && last.SchemaVersion == 1 && last.MatrixDigest == decision.MatrixDigest {
			return fmt.Errorf("QA failed again without a changed revision matrix")
		}
		break
	}
	return nil
}

// A new immutable version preserves the exact approved definition while
// allocating fresh step rows. Completed steps from the previous attempt are
// never reset, deleted or reused as proof of the correction.
func cloneApprovedCorrectionPlan(tx *gorm.DB, item models.DeliveryWorkItem, now time.Time) error {
	var plan models.DeliveryPlan
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("work_item_id = ? AND status = ?", item.ID, "approved").Order("version DESC").First(&plan).Error; err != nil {
		return err
	}
	if plan.StructuredJSON != item.PlanJSON || plan.ApprovedGateID == nil || *plan.ApprovedGateID == uuid.Nil {
		return fmt.Errorf("correction plan differs from the approved definition")
	}
	var approval models.DeliveryGate
	if err := tx.First(&approval, *plan.ApprovedGateID).Error; err != nil {
		return err
	}
	if approval.WorkItemID != item.ID || approval.Kind != deliveryworkflow.GatePlan || approval.Decision != deliveryworkflow.DecisionApproved {
		return fmt.Errorf("correction plan has no valid original approval")
	}
	var live int64
	if err := tx.Model(&models.DeliveryPlanExecution{}).Where("plan_id = ? AND status IN ?", plan.ID, []string{"pending", "dispatching", "running"}).Count(&live).Error; err != nil {
		return err
	}
	if live > 0 {
		return fmt.Errorf("correction cannot overlap an active approved-plan execution")
	}
	var version int
	if err := tx.Model(&models.DeliveryPlan{}).Where("work_item_id = ?", item.ID).Select("COALESCE(MAX(version), 0)").Scan(&version).Error; err != nil {
		return err
	}
	copy := models.DeliveryPlan{WorkItemID: item.ID, Version: version + 1, Status: "approved", Summary: plan.Summary, StructuredJSON: plan.StructuredJSON, ContextDigest: plan.ContextDigest, ProposedBy: delegatedCoordinatorActor, ApprovedGateID: plan.ApprovedGateID, CreatedAt: now}
	if err := tx.Create(&copy).Error; err != nil {
		return err
	}
	_, _, err := ensureDeliveryPlanStepsTx(tx, copy, delegatedCoordinatorActor)
	return err
}
