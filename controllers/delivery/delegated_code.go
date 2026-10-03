package delivery

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"events-stocks/internal/automationagent"
	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/deliverypolicy"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A delegated repository policy cannot override a work item's human mandate.
func delegatedActionAuthority(tx *gorm.DB, item models.DeliveryWorkItem, action string) (deliveryledger.AutonomySnapshotInput, bool, error) {
	var event models.DeliveryEvent
	err := tx.Where("work_item_id = ? AND event_type = ?", item.ID, deliveryledger.EventTypeAutonomySnapshot).Order("sequence ASC").First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return deliveryledger.AutonomySnapshotInput{}, false, nil
	}
	if err != nil {
		return deliveryledger.AutonomySnapshotInput{}, false, err
	}
	projection, err := deliveryledger.ProjectAutonomySnapshot(event)
	if err != nil {
		return deliveryledger.AutonomySnapshotInput{}, false, err
	}
	if projection.ProjectID != item.ProjectID || projection.ChangeSetID != item.ID.String() {
		return deliveryledger.AutonomySnapshotInput{}, false, fmt.Errorf("delegated authority belongs to another work item")
	}
	if !projection.Delegated {
		return deliveryledger.AutonomySnapshotInput{}, false, nil
	}
	mandate, err := resolveDeliveryMandate(item, nil)
	if err != nil {
		return deliveryledger.AutonomySnapshotInput{}, false, err
	}
	for _, human := range mandate.HumanActions {
		if human == action {
			return deliveryledger.AutonomySnapshotInput{}, false, nil
		}
	}
	authority, err := deliveryledger.ReadAutonomyAuthority(event)
	return authority, err == nil, err
}

// Runs inside completion's work-item lock and transaction, after submission.
// A grant authorizes only publication of the exact validated diff; it is not
// an approval of code, merge or deployment.
func coordinateDelegatedPublication(tx *gorm.DB, item *models.DeliveryWorkItem, intent models.DeliveryContinuation, task models.AutomationTask, now time.Time) (bool, error) {
	authority, allowed, err := delegatedActionAuthority(tx, *item, "authorize_publication")
	if err != nil || !allowed {
		return false, err
	}
	mandate, err := resolveDeliveryMandate(*item, nil)
	if err != nil {
		return false, err
	}
	tools := map[string]bool{}
	for _, tool := range mandate.AllowedTools {
		tools[tool] = true
	}
	if !tools["git.commit"] || !tools["github.pr.create"] {
		return false, fmt.Errorf("publication exceeds work-item tool authority")
	}
	scopes := map[string]bool{}
	for _, reference := range mandate.RepositoryRefs {
		scopes[reference] = true
	}
	if item.State != deliveryworkflow.StateCodeReview || task.Operation != "delivery.implementation" || task.Status != "completed" || task.CompletedAt == nil || task.ContinuationID == nil || *task.ContinuationID != intent.ID || task.DeliveryWorkItemID == nil || *task.DeliveryWorkItemID != item.ID || task.AgentInstanceID == nil {
		return false, fmt.Errorf("publication requires the current signed completed implementation")
	}
	var plan models.DeliveryPlan
	if err := tx.Where("work_item_id = ? AND status = ?", item.ID, "approved").Order("version DESC").First(&plan).Error; err != nil {
		return false, err
	}
	if plan.StructuredJSON != item.PlanJSON || plan.ApprovedGateID == nil {
		return false, fmt.Errorf("publication definition differs from its approved plan")
	}
	var approval models.DeliveryGate
	if err := tx.First(&approval, *plan.ApprovedGateID).Error; err != nil {
		return false, err
	}
	if approval.WorkItemID != item.ID || approval.Kind != deliveryworkflow.GatePlan || approval.Decision != deliveryworkflow.DecisionApproved {
		return false, fmt.Errorf("publication lacks original plan approval")
	}
	required, err := codeReviewRequiredRepositories(item.PlanJSON)
	if err != nil || len(required) == 0 {
		return false, fmt.Errorf("publication requires explicit repository coverage")
	}
	references := make([]string, 0, len(required))
	for reference := range required {
		references = append(references, reference)
	}
	sort.Strings(references)
	capabilities, _ := json.Marshal([]string{"commit:stage", "branch:publish", "pull_request:create"})
	for _, reference := range references {
		if !scopes[reference] {
			return false, fmt.Errorf("publication exceeds work-item repository scope")
		}
		var local models.DeliveryChangeSet
		if err := tx.Where("work_item_id = ? AND repository_ref = ? AND review_type = ?", item.ID, reference, "local_worktree").Order("created_at DESC, id DESC").First(&local).Error; err != nil {
			return false, err
		}
		if !trustedImplementationChangeSet(local) || local.CIStatus != "passed" {
			return false, fmt.Errorf("latest local diff is not validated")
		}
		var metadata map[string]any
		if json.Unmarshal([]byte(local.MetadataJSON), &metadata) != nil || metadata["automation_task_id"] != task.ID.String() {
			return false, fmt.Errorf("local diff belongs to another implementation")
		}
		base, err := reviewedChangeSetBaseSHA(local)
		if err != nil {
			return false, err
		}
		repository, err := reviewedChangeSetGitHubRepository(local)
		if err != nil {
			return false, err
		}
		digest, err := reviewedChangeSetDigest(local)
		if err != nil {
			return false, err
		}
		covered := false
		var targetBranches []string
		for _, frozen := range authority.Repositories {
			if frozen.SourceReference == reference && strings.EqualFold(frozen.Repository, "github://"+repository) && frozen.SourceRevision == base && frozen.Policy.Resolved && (frozen.Policy.Mode == deliverypolicy.ModeMerge || frozen.Policy.Mode == deliverypolicy.ModeRelease) && frozen.Policy.Safety.SecretScan && frozen.Policy.Safety.IndependentReview && frozen.Policy.Safety.ExactSHAEvidence && !frozen.Policy.Safety.ForceMergeAllowed && len(frozen.Policy.AllowedTargetBranches) > 0 && len(frozen.Policy.RequiredTestKinds) > 0 {
				covered = true
				targetBranches = append([]string(nil), frozen.Policy.AllowedTargetBranches...)
			}
		}
		if !covered {
			return false, fmt.Errorf("publication diff exceeds frozen repository authority")
		}
		var active int64
		if err := tx.Model(&models.DeliveryPublicationGrant{}).Where("work_item_id = ? AND repository_ref = ? AND revoked_at IS NULL AND expires_at > ?", item.ID, reference, now).Count(&active).Error; err != nil {
			return false, err
		}
		if active != 0 {
			return false, fmt.Errorf("publication already has an active grant")
		}
		reason, _ := json.Marshal(map[string]any{"schema_version": 1, "authority": "delegated", "implementation_task_id": task.ID.String(), "approved_plan_id": plan.ID.String(), "epoch": item.AutomationEpoch, "diff_sha256": digest, "allowed_target_branches": targetBranches})
		grant := models.DeliveryPublicationGrant{WorkItemID: item.ID, RepositoryRef: reference, BaseSHA: base, GitHubRepository: repository, ReviewDiffSHA256: digest, Branch: local.Branch, CapabilitiesJSON: string(capabilities), Reason: string(reason), GrantedBy: delegatedCoordinatorActor, GrantedAt: now, ExpiresAt: now.Add(defaultPublicationGrantMinutes * time.Minute)}
		if err := tx.Create(&grant).Error; err != nil {
			return false, err
		}
		if err := scheduleContinuation(tx, *item, "publish", intent.RequestedBy, grant.ID.String()); err != nil {
			return false, err
		}
	}
	if err := scheduleContinuation(tx, *item, "code_review", intent.RequestedBy, ""); err != nil {
		return false, err
	}
	item.AgentProgress = "queued"
	return true, nil
}

// No inference is enqueued for this phase. The independent webhook Reviewer
// produces immutable publications; this durable intent waits for all exact PR
// heads and survives dispatcher restart without repeating publication.
func advanceDelegatedCodeReview(db *gorm.DB, intent models.DeliveryContinuation) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var item models.DeliveryWorkItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&item, intent.WorkItemID).Error; err != nil {
			return err
		}
		if item.AutomationEpoch != intent.Epoch {
			return tx.Model(&intent).Update("status", "superseded").Error
		}
		var current models.DeliveryContinuation
		if err := tx.First(&current, intent.ID).Error; err != nil {
			return err
		}
		if current.Status == "done" || current.Status == "superseded" || current.Status == "blocked" {
			return nil
		}
		if item.State != deliveryworkflow.StateCodeReview {
			return fmt.Errorf("code review intent is outside the review window")
		}
		authority, allowed, err := delegatedActionAuthority(tx, item, "approve_code_review")
		if err != nil {
			return err
		}
		if !allowed {
			item.AgentProgress = "waiting_for_user"
			if err := tx.Save(&item).Error; err != nil {
				return err
			}
			return tx.Model(&intent).Update("status", "done").Error
		}
		now := time.Now().UTC()
		block := func(reason string) error {
			item.AgentProgress, item.BlockedReason = "blocked", reason
			if err := tx.Save(&item).Error; err != nil {
				return err
			}
			return tx.Model(&intent).Update("status", "blocked").Error
		}
		wait := func(reason string) error {
			if intent.CreatedAt.IsZero() || now.Sub(intent.CreatedAt) >= 24*time.Hour {
				return block(reason)
			}
			return tx.Model(&intent).Updates(map[string]any{"status": "pending", "available_at": now.Add(time.Minute)}).Error
		}
		var changes []models.DeliveryChangeSet
		if err := tx.Where("work_item_id = ?", item.ID).Order("created_at DESC, id DESC").Find(&changes).Error; err != nil {
			return err
		}
		if !delegatedCodePublicationsReady(item.PlanJSON, changes) {
			return wait("Falta la publicación verificable del diff vigente de todos los repositorios.")
		}
		candidate, err := storedReleaseGateCandidate(item, changes)
		if err != nil {
			return wait("Falta la publicación verificable de todos los repositorios.")
		}
		repositories, err := delegatedQARepositoryBoundaries(item.PlanJSON, authority, candidate, changes)
		if err != nil {
			return err
		}
		checklist := []string{}
		diffs, findings := []string{}, []json.RawMessage{}
		changesRequested := false
		for _, revision := range candidate.Revisions {
			pr, err := delegatedQAPullRequest(revision, repositories, changes)
			if err != nil {
				return wait("Falta CI del PR exacto para revisar código.")
			}
			var publication *models.DeliveryChangeSet
			for i := range changes {
				frozen, covered := repositories[changes[i].RepositoryRef]
				if covered && strings.EqualFold(frozen.Repository, "github://"+revision.Repository) && changes[i].CommitSHA == revision.SHA && validPublishedChangeRecord(changes[i]) {
					publication = &changes[i]
					break
				}
			}
			if publication == nil {
				return fmt.Errorf("code review lost its publication binding")
			}
			var local *models.DeliveryChangeSet
			for i := range changes {
				if changes[i].RepositoryRef == publication.RepositoryRef && changes[i].ReviewType == "local_worktree" {
					local = &changes[i]
					break
				}
			}
			if local == nil || !trustedImplementationChangeSet(*local) || !publishedPreviewMatchesReview(*local, *publication) {
				return wait("Falta publicar el diff local vigente antes de revisar código.")
			}
			var metadata struct {
				GrantID        string   `json:"publication_grant_id"`
				TaskID         string   `json:"automation_task_id"`
				SecurityChecks []string `json:"security_checks"`
			}
			if json.Unmarshal([]byte(publication.MetadataJSON), &metadata) != nil || metadata.GrantID == "" || len(metadata.SecurityChecks) != 2 || metadata.SecurityChecks[0] != "security:secrets" || metadata.SecurityChecks[1] != "security:high-critical" {
				return fmt.Errorf("publication lacks its security and grant receipt")
			}
			var grant models.DeliveryPublicationGrant
			if err := tx.First(&grant, "id = ?", metadata.GrantID).Error; err != nil {
				return err
			}
			var reason struct {
				Epoch int64 `json:"epoch"`
			}
			if json.Unmarshal([]byte(grant.Reason), &reason) != nil || reason.Epoch != intent.Epoch || grant.WorkItemID != item.ID || grant.GrantedBy != delegatedCoordinatorActor || grant.RepositoryRef != local.RepositoryRef || grant.Branch != local.Branch || grant.BaseSHA != repositories[local.RepositoryRef].SourceRevision || !strings.EqualFold(grant.GitHubRepository, revision.Repository) || grant.GrantedAt.After(publication.CreatedAt) || !grant.ExpiresAt.After(publication.CreatedAt) || grant.RevokedAt == nil || grant.RevokedBy != "itbem-github-app" {
				return fmt.Errorf("publication is outside its consumed delegated grant")
			}
			if err := validatePublicationGrantReviewBinding(grant, *local); err != nil {
				return err
			}
			diffs = append(diffs, local.RepositoryRef+":"+grant.ReviewDiffSHA256)
			var publishedTask models.AutomationTask
			if err := tx.First(&publishedTask, "id = ?", metadata.TaskID).Error; err != nil {
				return err
			}
			if publishedTask.Status != "completed" || publishedTask.Operation != "delivery.publish" || publishedTask.ContinuationID == nil || publishedTask.DeliveryWorkItemID == nil || *publishedTask.DeliveryWorkItemID != item.ID || publishedTask.EvidenceSubjectDigest != grant.ReviewDiffSHA256 {
				return fmt.Errorf("publication task has no current completion receipt")
			}
			var publishedIntent models.DeliveryContinuation
			if err := tx.First(&publishedIntent, *publishedTask.ContinuationID).Error; err != nil {
				return err
			}
			if publishedIntent.WorkItemID != item.ID || publishedIntent.Epoch != intent.Epoch || publishedIntent.PublicationGrantID != grant.ID.String() || publishedIntent.Phase != "publish" {
				return fmt.Errorf("publication completed outside its authorized intent")
			}
			if publishedIntent.Status != "done" {
				return wait("La publicación está pendiente de reconciliar su resultado.")
			}
			var review models.AutomationCodeReviewPublication
			err = tx.Where("repository = ? AND pull_request = ? AND head_sha = ?", strings.ToLower(revision.Repository), pr, revision.SHA).Order("published_at DESC, review_id DESC").First(&review).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return wait("Falta revisión independiente del commit exacto.")
			}
			if err != nil {
				return err
			}
			if review.PublishedAt.Before(publication.CreatedAt.Add(-time.Second)) || review.PublishedAt.After(now.Add(time.Minute)) || review.ReviewerActor == "" || review.AuthorActor == "" || strings.EqualFold(review.ReviewerActor, review.AuthorActor) || review.ReviewID < 1 {
				return fmt.Errorf("code review is stale or not independent")
			}
			if !validDelegatedIndependentReview(review, revision, pr, now) {
				if review.Event != "REQUEST_CHANGES" || review.ReviewGatePassed || review.Verdict != "request_changes" {
					return wait("La revisión independiente requiere atención antes de avanzar.")
				}
				changesRequested = true
				result, err := automationagent.ParseCodeReview(review.ReviewResultJSON)
				if err != nil || result["verdict"] != "request_changes" {
					return block("Faltan hallazgos verificables para corregir automáticamente.")
				}
				canonical, err := json.Marshal(result)
				if err != nil || fmt.Sprintf("%x", sha256.Sum256(canonical)) != review.ReviewResultSHA256 {
					return fmt.Errorf("independent review findings seal does not match")
				}
				findings = append(findings, canonical)
				for _, raw := range result["findings"].([]any) {
					finding := raw.(map[string]any)
					if finding["category"] == "security" && (finding["severity"] == "critical" || finding["severity"] == "high") {
						return block("La revisión detectó un riesgo de seguridad que requiere atención humana.")
					}
				}
			}
			checklist = append(checklist, "independent_review:"+review.ID.String(), "published_change:"+publication.ID.String(), "local_diff:"+local.ID.String(), "publication_grant:"+grant.ID.String())
		}
		action, phase := deliveryworkflow.ActionApproveCodeReview, "preview"
		sort.Strings(diffs)
		encodedDiffs, _ := json.Marshal(diffs)
		diffDigest := fmt.Sprintf("%x", sha256.Sum256(encodedDiffs))
		comment, _ := json.Marshal(map[string]any{"schema_version": 1, "diff_digest": diffDigest, "independent_findings": findings})
		if len(comment) > 3800 {
			return block("La corrección excede el contexto acotado y necesita revisión humana.")
		}
		if changesRequested {
			var previous []models.DeliveryGate
			if err := tx.Where("work_item_id = ? AND decision = ? AND kind IN ?", item.ID, deliveryworkflow.DecisionChangesRequested, []string{deliveryworkflow.GateCodeReview, deliveryworkflow.GateQAReview}).Order("decided_at DESC, id DESC").Find(&previous).Error; err != nil {
				return err
			}
			if len(previous) >= maximumDelegatedCorrections {
				return block("Se agotó el límite de correcciones de código y QA.")
			}
			for _, last := range previous {
				if last.Kind != deliveryworkflow.GateCodeReview {
					continue
				}
				var decision struct {
					DiffDigest string `json:"diff_digest"`
				}
				if json.Unmarshal([]byte(last.Comment), &decision) == nil && decision.DiffDigest == diffDigest {
					return block("La revisión volvió a fallar sin cambios en el diff.")
				}
				break
			}
			if err := cloneApprovedCorrectionPlan(tx, item, now); err != nil {
				return err
			}
			action, phase = deliveryworkflow.ActionRequestCodeChanges, "implementation"
		}
		encoded, _ := json.Marshal(checklist)
		gate := gate(action, item.ID, delegatedCoordinatorActor, string(comment), string(encoded))
		gate.Authority = "delegated"
		if err := deliveryworkflow.Advance(&item, action, gate, now); err != nil {
			return err
		}
		if err := tx.Create(gate).Error; err != nil {
			return err
		}
		if err := invalidatePublicationGrantsForTransition(tx, item.ID, delegatedCoordinatorActor, action, now); err != nil {
			return err
		}
		item.AutomationEpoch++
		item.AgentProgress, item.BlockedReason = "queued", ""
		if err := scheduleContinuation(tx, item, phase, intent.RequestedBy, ""); err != nil {
			return err
		}
		if err := tx.Save(&item).Error; err != nil {
			return err
		}
		if err := recordContinuationMessage(tx, intent, "ready", "La revisión independiente del commit exacto determinó el siguiente paso. No autorizó merge ni despliegue."); err != nil {
			return err
		}
		return tx.Model(&intent).Update("status", "done").Error
	})
}

// Changes arrive newest first. A partial multi-repository publication is an
// expected wait, not a policy error that exhausts dispatcher retries.
func delegatedCodePublicationsReady(plan string, changes []models.DeliveryChangeSet) bool {
	required, err := codeReviewRequiredRepositories(plan)
	if err != nil || len(required) == 0 {
		return false
	}
	for reference := range required {
		var local, published *models.DeliveryChangeSet
		for i := range changes {
			change := &changes[i]
			if change.RepositoryRef != reference {
				continue
			}
			if local == nil && change.ReviewType == "local_worktree" {
				local = change
			}
			if published == nil && validPublishedChangeRecord(*change) {
				published = change
			}
		}
		if local == nil || published == nil || !trustedImplementationChangeSet(*local) || !publishedPreviewMatchesReview(*local, *published) {
			return false
		}
	}
	return true
}
