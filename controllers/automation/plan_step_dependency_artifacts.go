package automation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/services/deliveryplansteps"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxPlanStepDependencyPatchManifestResponseBytes = 64 * 1024
	dependencyPatchContentType                      = "application/vnd.git-patch"
)

type planStepDependencyPatchManifestResponse struct {
	ManifestSHA256 string                                            `json:"manifest_sha256"`
	PatchCount     int                                               `json:"patch_count"`
	TotalSizeBytes int64                                             `json:"total_size_bytes"`
	Patches        []models.DeliveryPlanStepDependencyPatchReference `json:"patches"`
}

type resolvedPlanStepDependencyPatch struct {
	Reference models.DeliveryPlanStepDependencyPatchReference
	Artifact  models.DeliveryPlanStepPatchArtifact
}

// GetDeliveryPlanStepDependencyPatchManifest returns only metadata for patch
// artifacts from direct, completed dependencies. S3 bucket and object key
// remain server-side and are never represented in this DTO.
func GetDeliveryPlanStepDependencyPatchManifest(c echo.Context) error {
	stepID, callbackIdentity, tuple, fence, ok := parsePlanStepDependencyPatchCallback(c)
	if !ok {
		return nil
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Dependency patches unavailable", "Database is unavailable")
	}
	now := time.Now().UTC()
	var response planStepDependencyPatchManifestResponse
	err := configuration.DB.WithContext(planStepCallbackContext(c, callbackIdentity.InstanceID)).Transaction(func(tx *gorm.DB) error {
		task, step, plan, err := validatePlanStepDependencyPatchLease(tx, stepID, tuple, fence, callbackIdentity, now)
		if err != nil {
			return err
		}
		cfg, _ := c.Get("config").(*models.Config)
		if cfg == nil {
			return errPlanStepPatchObjectUnavailable
		}
		resolved, manifestSHA256, totalSize, err := resolvePlanStepDirectDependencyPatches(tx, step, *task.DeliveryWorkItemID, plan, cfg.AutomationOutputBucket)
		if err != nil {
			return err
		}
		response = planStepDependencyPatchManifestResponse{
			ManifestSHA256: manifestSHA256, PatchCount: len(resolved), TotalSizeBytes: totalSize,
			Patches: make([]models.DeliveryPlanStepDependencyPatchReference, 0, len(resolved)),
		}
		for _, item := range resolved {
			response.Patches = append(response.Patches, item.Reference)
		}
		encoded, err := json.Marshal(response)
		if err != nil || len(encoded) > maxPlanStepDependencyPatchManifestResponseBytes {
			return deliveryplansteps.ErrStepInputInvalid
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errPlanStepPatchObjectUnavailable) {
			return utils.Error(c, http.StatusServiceUnavailable, "Dependency patches unavailable", "Private artifact storage is unavailable")
		}
		return planStepRuntimeError(c, "Dependency patch manifest rejected", err)
	}
	return utils.Success(c, http.StatusOK, "Dependency patch manifest loaded", response)
}

// GetDeliveryPlanStepDependencyPatch streams one raw patch only when its digest
// belongs to the current step's direct completed dependencies and lease. The
// server verifies the object again and records an append-only handoff receipt.
func GetDeliveryPlanStepDependencyPatch(c echo.Context) error {
	stepID, callbackIdentity, tuple, fence, ok := parsePlanStepDependencyPatchCallback(c)
	if !ok {
		return nil
	}
	digest := strings.TrimSpace(c.Param("sha256"))
	if !validDeliveryPlanPatchSHA256(digest) {
		return utils.Error(c, http.StatusBadRequest, "Invalid dependency patch", "The patch identifier is invalid")
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Dependency patch unavailable", "Database is unavailable")
	}
	now := time.Now().UTC()
	var body []byte
	err := configuration.DB.WithContext(planStepCallbackContext(c, callbackIdentity.InstanceID)).Transaction(func(tx *gorm.DB) error {
		task, step, plan, err := validatePlanStepDependencyPatchLease(tx, stepID, tuple, fence, callbackIdentity, now)
		if err != nil {
			return err
		}
		cfg, _ := c.Get("config").(*models.Config)
		if cfg == nil || strings.TrimSpace(cfg.AutomationOutputBucket) == "" {
			return errPlanStepPatchObjectUnavailable
		}
		resolved, manifestSHA256, _, err := resolvePlanStepDirectDependencyPatches(tx, step, *task.DeliveryWorkItemID, plan, cfg.AutomationOutputBucket)
		if err != nil {
			return err
		}
		matches := make([]resolvedPlanStepDependencyPatch, 0, 1)
		for _, item := range resolved {
			if item.Reference.SHA256 == digest {
				matches = append(matches, item)
			}
		}
		if len(matches) == 0 {
			return gorm.ErrRecordNotFound
		}
		// Identical SHA-256 values identify identical bytes. Read one canonical
		// source object, then record every matching direct dependency/repository.
		objectRef := matches[0].Artifact
		object, err := getPlanStepPatchObject(c.Request().Context(), objectRef.ObjectKey, objectRef.Bucket)
		if err != nil {
			return errPlanStepPatchObjectUnavailable
		}
		patch, readErr := io.ReadAll(io.LimitReader(object, int64(models.MaxDeliveryPlanStepPatchArtifactBytes)+1))
		closeErr := object.Close()
		if readErr != nil || closeErr != nil {
			return errPlanStepPatchObjectUnavailable
		}
		digestBytes := sha256.Sum256(patch)
		if len(patch) == 0 || len(patch) > models.MaxDeliveryPlanStepPatchArtifactBytes ||
			int64(len(patch)) != objectRef.SizeBytes || hex.EncodeToString(digestBytes[:]) != digest ||
			validateUnifiedGitPatch(patch) != nil || containsHighConfidencePatchSecret(patch) {
			return errPlanStepPatchObjectUnavailable
		}
		for _, item := range matches {
			if item.Reference.SizeBytes != int64(len(patch)) {
				return deliveryplansteps.ErrStepLeaseConflict
			}
			receipt := models.DeliveryPlanStepDependencyPatch{
				PlanID: plan.ID, AutomationTaskID: task.ID, RunID: tuple.RunID, StepID: step.ID, FencingToken: fence,
				DependencyStepID:      uuid.Must(uuid.FromString(item.Reference.DependencyStepID)),
				SourcePatchArtifactID: item.Artifact.ID, SourceAutomationTaskID: item.Artifact.AutomationTaskID, SourceRunID: item.Artifact.RunID,
				WorkerID: tuple.WorkerID, AgentKey: tuple.AgentKey, MachineID: tuple.MachineID, AgentInstanceID: &callbackIdentity.InstanceID,
				ManifestSHA256: manifestSHA256, RepositoryRef: item.Reference.RepositoryRef, BaseSHA: item.Reference.BaseSHA,
				SHA256: item.Reference.SHA256, SizeBytes: item.Reference.SizeBytes, CreatedAt: now,
			}
			result := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "run_id"}, {Name: "step_id"}, {Name: "fencing_token"}, {Name: "dependency_step_id"}, {Name: "repository_ref"}},
				DoNothing: true,
			}).Create(&receipt)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				continue
			}
			var existing models.DeliveryPlanStepDependencyPatch
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&existing,
				"run_id = ? AND step_id = ? AND fencing_token = ? AND dependency_step_id = ? AND repository_ref = ?",
				receipt.RunID, receipt.StepID, receipt.FencingToken, receipt.DependencyStepID, receipt.RepositoryRef).Error; err != nil {
				return err
			}
			if !sameDeliveryPlanStepDependencyPatch(existing, receipt) {
				return deliveryplansteps.ErrStepLeaseConflict
			}
		}
		body = patch
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return utils.Error(c, http.StatusNotFound, "Dependency patch unavailable", "No matching direct dependency patch exists")
	}
	if errors.Is(err, errPlanStepPatchObjectUnavailable) {
		return utils.Error(c, http.StatusServiceUnavailable, "Dependency patch unavailable", "The private patch could not be verified")
	}
	if err != nil {
		return planStepRuntimeError(c, "Dependency patch rejected", err)
	}
	c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
	c.Response().Header().Set(echo.HeaderXContentTypeOptions, "nosniff")
	return c.Blob(http.StatusOK, dependencyPatchContentType, body)
}

func parsePlanStepDependencyPatchCallback(c echo.Context) (uuid.UUID, authenticatedAgentCallback, planStepRuntimeTuple, int64, bool) {
	stepID, err := uuid.FromString(strings.TrimSpace(c.Param("id")))
	if err != nil || stepID == uuid.Nil {
		_ = utils.Error(c, http.StatusBadRequest, "Invalid plan step", "The step identifier is invalid")
		return uuid.Nil, authenticatedAgentCallback{}, planStepRuntimeTuple{}, 0, false
	}
	var request planStepLeaseRequest
	if err := bindPlanStepRuntimePayload(c, &request); err != nil {
		_ = utils.Error(c, http.StatusBadRequest, "Invalid dependency patch request", "The request body is invalid")
		return uuid.Nil, authenticatedAgentCallback{}, planStepRuntimeTuple{}, 0, false
	}
	callbackIdentity, ok := bindCallbackProfileIdentity(c, &request.AgentKey, &request.MachineID)
	if !ok {
		return uuid.Nil, authenticatedAgentCallback{}, planStepRuntimeTuple{}, 0, false
	}
	taskID, runID, identity, _, err := parsePlanStepRuntimeTuple(request.TaskID, request.RunID, request.WorkerID, request.AgentKey, request.MachineID, 0)
	fence, fenceErr := parsePlanStepFence(request.FencingToken)
	if err != nil || fenceErr != nil {
		_ = utils.Error(c, http.StatusBadRequest, "Invalid dependency patch request", "The task, run, worker, or fencing parameters are invalid")
		return uuid.Nil, authenticatedAgentCallback{}, planStepRuntimeTuple{}, 0, false
	}
	return stepID, callbackIdentity, planStepRuntimeTuple{
		TaskID: taskID, RunID: runID, WorkerID: identity.WorkerID, AgentKey: identity.AgentKey, MachineID: identity.MachineID,
	}, fence, true
}

func validatePlanStepDependencyPatchLease(tx *gorm.DB, stepID uuid.UUID, tuple planStepRuntimeTuple, fence int64, callbackIdentity authenticatedAgentCallback, now time.Time) (*models.AutomationTask, models.DeliveryPlanStep, models.DeliveryPlan, error) {
	task, err := validatePlanStepRuntimeTask(tx, tuple)
	if err != nil {
		return nil, models.DeliveryPlanStep{}, models.DeliveryPlan{}, err
	}
	if task.Operation != "delivery.implementation" || task.DeliveryWorkItemID == nil {
		return nil, models.DeliveryPlanStep{}, models.DeliveryPlan{}, deliveryplansteps.ErrStepLeaseConflict
	}
	var step models.DeliveryPlanStep
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&step, "id = ?", stepID).Error; err != nil {
		return nil, models.DeliveryPlanStep{}, models.DeliveryPlan{}, err
	}
	if !planStepActivityLeaseMatches(step, *task, tuple.RunID, tuple.WorkerID, tuple.AgentKey, tuple.MachineID, fence, now) || callbackIdentity.InstanceID == uuid.Nil {
		return nil, models.DeliveryPlanStep{}, models.DeliveryPlan{}, deliveryplansteps.ErrStepLeaseConflict
	}
	var plan models.DeliveryPlan
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&plan, "id = ?", step.PlanID).Error; err != nil {
		return nil, models.DeliveryPlanStep{}, models.DeliveryPlan{}, err
	}
	if plan.WorkItemID != *task.DeliveryWorkItemID || plan.Status != "approved" || plan.ApprovedGateID == nil {
		return nil, models.DeliveryPlanStep{}, models.DeliveryPlan{}, deliveryplansteps.ErrStepLeaseConflict
	}
	if _, err := validateAssignedPlanStepCallback(tx, *task, stepID); err != nil {
		return nil, models.DeliveryPlanStep{}, models.DeliveryPlan{}, err
	}
	return task, step, plan, nil
}

func resolvePlanStepDirectDependencyPatches(tx *gorm.DB, consumer models.DeliveryPlanStep, workItemID uuid.UUID, plan models.DeliveryPlan, outputBucket string) ([]resolvedPlanStepDependencyPatch, string, int64, error) {
	var edges []models.DeliveryPlanStepDependency
	if err := tx.Where("plan_id = ? AND step_id = ?", consumer.PlanID, consumer.ID).Order("depends_on_step_id ASC").Find(&edges).Error; err != nil {
		return nil, "", 0, err
	}
	resolved := make([]resolvedPlanStepDependencyPatch, 0)
	for _, edge := range edges {
		var dependency models.DeliveryPlanStep
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&dependency, "id = ?", edge.DependsOnStepID).Error; err != nil {
			return nil, "", 0, err
		}
		if edge.PlanID != consumer.PlanID || dependency.PlanID != plan.ID || dependency.Status != models.DeliveryPlanStepCompleted ||
			dependency.AutomationTaskID == nil || dependency.RunID == "" || dependency.LeaseFence < 1 {
			return nil, "", 0, deliveryplansteps.ErrStepLeaseConflict
		}
		var sourceTask models.AutomationTask
		if err := tx.Select("id", "operation", "delivery_work_item_id", "run_id", "worker_id", "agent_key", "machine_id").First(&sourceTask, "id = ?", *dependency.AutomationTaskID).Error; err != nil {
			return nil, "", 0, err
		}
		if sourceTask.Operation != "delivery.implementation" || sourceTask.DeliveryWorkItemID == nil || *sourceTask.DeliveryWorkItemID != workItemID ||
			sourceTask.RunID != dependency.RunID || sourceTask.WorkerID != dependency.WorkerID || sourceTask.AgentKey != dependency.AgentKey || sourceTask.MachineID != dependency.MachineID {
			return nil, "", 0, deliveryplansteps.ErrStepLeaseConflict
		}
		identity := automationagent.AgentIdentity{WorkerID: dependency.WorkerID, AgentKey: dependency.AgentKey, MachineID: dependency.MachineID}
		var evidence models.DeliveryPlanStepActivityEvent
		if err := tx.Where(`step_id = ? AND plan_id = ? AND automation_task_id = ? AND run_id = ? AND worker_id = ?
			AND agent_key = ? AND machine_id = ? AND fencing_token = ? AND action = ? AND phase = ?`,
			dependency.ID, plan.ID, *dependency.AutomationTaskID, dependency.RunID, dependency.WorkerID, dependency.AgentKey,
			dependency.MachineID, dependency.LeaseFence, models.DeliveryPlanStepActivityEvidence, models.DeliveryPlanStepActivityCompleted).
			Order("sequence DESC").First(&evidence).Error; err != nil {
			return nil, "", 0, err
		}
		if evidence.AgentInstanceID == nil || requirePlanStepAcceptanceEvidence(tx, dependency, *dependency.AutomationTaskID, dependency.RunID, identity, dependency.LeaseFence, *evidence.AgentInstanceID) != nil {
			return nil, "", 0, deliveryplansteps.ErrStepLeaseConflict
		}
		if err := validatePlanStepActivityAcceptanceCriteria(dependency.AcceptanceCriteriaJSON, evidence); err != nil {
			return nil, "", 0, deliveryplansteps.ErrStepLeaseConflict
		}
		details, err := parsePlanStepActivityDetailsJSON(evidence.DetailsJSON, evidence.Action, evidence.Phase)
		if err != nil || details == nil {
			return nil, "", 0, deliveryplansteps.ErrStepLeaseConflict
		}
		if len(details.PatchArtifacts) > 0 {
			patchManifestSHA256, manifestErr := models.DeliveryPlanStepPatchArtifactManifestSHA256(details.PatchArtifacts)
			if manifestErr != nil || details.ReviewDiffSHA256 != patchManifestSHA256 {
				return nil, "", 0, deliveryplansteps.ErrStepLeaseConflict
			}
		}

		var artifactRows []models.DeliveryPlanStepPatchArtifact
		if err := tx.Where("plan_id = ? AND automation_task_id = ? AND run_id = ? AND step_id = ?", plan.ID, *dependency.AutomationTaskID, dependency.RunID, dependency.ID).
			Order("repository_ref ASC").Find(&artifactRows).Error; err != nil {
			return nil, "", 0, err
		}
		if len(artifactRows) != len(details.PatchArtifacts) {
			return nil, "", 0, deliveryplansteps.ErrStepLeaseConflict
		}
		artifactByRepository := make(map[string]models.DeliveryPlanStepPatchArtifact, len(artifactRows))
		for _, artifact := range artifactRows {
			if artifact.PlanID != plan.ID || artifact.AutomationTaskID != *dependency.AutomationTaskID || artifact.RunID != dependency.RunID || artifact.StepID != dependency.ID ||
				artifact.WorkerID != dependency.WorkerID || artifact.AgentKey != dependency.AgentKey || artifact.MachineID != dependency.MachineID ||
				artifact.AgentInstanceID == nil || *artifact.AgentInstanceID != *evidence.AgentInstanceID || artifact.FencingToken != dependency.LeaseFence ||
				artifact.Bucket != outputBucket || artifact.ObjectKey != planStepPatchObjectKey(*dependency.AutomationTaskID, dependency.RunID, dependency.ID, artifact.SHA256) {
				return nil, "", 0, deliveryplansteps.ErrStepLeaseConflict
			}
			if _, duplicate := artifactByRepository[artifact.RepositoryRef]; duplicate {
				return nil, "", 0, deliveryplansteps.ErrStepLeaseConflict
			}
			artifactByRepository[artifact.RepositoryRef] = artifact
		}
		for _, reference := range details.PatchArtifacts {
			artifact, found := artifactByRepository[reference.RepositoryRef]
			if !found || artifact.BaseSHA != reference.BaseSHA || artifact.SHA256 != reference.SHA256 || artifact.SizeBytes != reference.SizeBytes {
				return nil, "", 0, deliveryplansteps.ErrStepLeaseConflict
			}
			resolved = append(resolved, resolvedPlanStepDependencyPatch{
				Reference: models.DeliveryPlanStepDependencyPatchReference{
					DependencyStepID: dependency.ID.String(), RepositoryRef: reference.RepositoryRef,
					BaseSHA: reference.BaseSHA, SHA256: reference.SHA256, SizeBytes: reference.SizeBytes,
				},
				Artifact: artifact,
			})
		}
	}
	if _, _, err := dependencyPatchManifestTotals(resolved); err != nil {
		return nil, "", 0, err
	}
	sort.Slice(resolved, func(i, j int) bool {
		left, right := resolved[i].Reference, resolved[j].Reference
		if left.DependencyStepID != right.DependencyStepID {
			return left.DependencyStepID < right.DependencyStepID
		}
		if left.RepositoryRef != right.RepositoryRef {
			return left.RepositoryRef < right.RepositoryRef
		}
		return left.SHA256 < right.SHA256
	})
	references := make([]models.DeliveryPlanStepDependencyPatchReference, 0, len(resolved))
	for _, item := range resolved {
		references = append(references, item.Reference)
	}
	manifestSHA256, err := models.DeliveryPlanStepDependencyPatchManifestSHA256(references)
	if err != nil {
		return nil, "", 0, deliveryplansteps.ErrStepInputInvalid
	}
	_, totalSize, _ := dependencyPatchManifestTotals(resolved)
	return resolved, manifestSHA256, totalSize, nil
}

func dependencyPatchManifestTotals(resolved []resolvedPlanStepDependencyPatch) (int, int64, error) {
	if len(resolved) > models.MaxDeliveryPlanStepDependencyPatchReferences {
		return 0, 0, deliveryplansteps.ErrStepInputInvalid
	}
	total := int64(0)
	for _, item := range resolved {
		if item.Reference.SizeBytes < 1 || item.Reference.SizeBytes > models.MaxDeliveryPlanStepPatchArtifactBytes || total > models.MaxDeliveryPlanStepDependencyPatchBytes-item.Reference.SizeBytes {
			return 0, 0, deliveryplansteps.ErrStepInputInvalid
		}
		total += item.Reference.SizeBytes
	}
	return len(resolved), total, nil
}

func validateAppliedPlanStepDependencyPatchManifest(tx *gorm.DB, step models.DeliveryPlanStep, task models.AutomationTask, identity automationagent.AgentIdentity, instanceID uuid.UUID, fence int64, event models.DeliveryPlanStepActivityEvent) error {
	if event.Action != models.DeliveryPlanStepActivityEvidence || event.Phase != models.DeliveryPlanStepActivityCompleted {
		return nil
	}
	details, err := parsePlanStepActivityDetailsJSON(event.DetailsJSON, event.Action, event.Phase)
	if err != nil {
		return err
	}
	var dependencyCount int64
	if err := tx.Model(&models.DeliveryPlanStepDependency{}).Where("plan_id = ? AND step_id = ?", step.PlanID, step.ID).Count(&dependencyCount).Error; err != nil {
		return err
	}
	hasManifest := details != nil && (details.AppliedDependencyManifestSHA256 != "" || details.AppliedDependencyPatchCount != nil)
	if dependencyCount == 0 && !hasManifest {
		return nil
	}
	if details == nil || !hasManifest || details.AppliedDependencyPatchCount == nil || task.DeliveryWorkItemID == nil || instanceID == uuid.Nil || fence < 1 {
		return errPlanStepAcceptanceEvidenceRequired
	}
	var plan models.DeliveryPlan
	if err := tx.First(&plan, "id = ?", step.PlanID).Error; err != nil {
		return err
	}
	cfg := configuration.FromContext(tx.Statement.Context)
	if cfg == nil {
		return errPlanStepPatchObjectUnavailable
	}
	resolved, manifestSHA256, _, err := resolvePlanStepDirectDependencyPatches(tx, step, *task.DeliveryWorkItemID, plan, cfg.AutomationOutputBucket)
	if err != nil {
		return err
	}
	if details.AppliedDependencyManifestSHA256 != manifestSHA256 || *details.AppliedDependencyPatchCount != len(resolved) {
		return errPlanStepAcceptanceEvidenceRequired
	}
	var receipts []models.DeliveryPlanStepDependencyPatch
	if err := tx.Where(`automation_task_id = ? AND run_id = ? AND step_id = ? AND fencing_token = ? AND worker_id = ?
		AND agent_key = ? AND machine_id = ? AND agent_instance_id = ?`, task.ID, event.RunID, step.ID, fence,
		identity.WorkerID, identity.AgentKey, identity.MachineID, instanceID).Find(&receipts).Error; err != nil {
		return err
	}
	if len(receipts) != len(resolved) {
		return errPlanStepAcceptanceEvidenceRequired
	}
	receiptByKey := make(map[string]models.DeliveryPlanStepDependencyPatch, len(receipts))
	for _, receipt := range receipts {
		key := receipt.DependencyStepID.String() + "\x00" + receipt.RepositoryRef
		if _, duplicate := receiptByKey[key]; duplicate {
			return errPlanStepAcceptanceEvidenceRequired
		}
		receiptByKey[key] = receipt
	}
	for _, item := range resolved {
		dependencyStepID, parseErr := uuid.FromString(item.Reference.DependencyStepID)
		if parseErr != nil {
			return errPlanStepAcceptanceEvidenceRequired
		}
		receipt, found := receiptByKey[dependencyStepID.String()+"\x00"+item.Reference.RepositoryRef]
		if !found || receipt.PlanID != plan.ID || receipt.AutomationTaskID != task.ID || receipt.RunID != event.RunID || receipt.StepID != step.ID ||
			receipt.FencingToken != fence || receipt.DependencyStepID != dependencyStepID || receipt.SourcePatchArtifactID != item.Artifact.ID ||
			receipt.SourceAutomationTaskID != item.Artifact.AutomationTaskID || receipt.SourceRunID != item.Artifact.RunID ||
			receipt.WorkerID != identity.WorkerID || receipt.AgentKey != identity.AgentKey || receipt.MachineID != identity.MachineID ||
			receipt.AgentInstanceID == nil || *receipt.AgentInstanceID != instanceID || receipt.ManifestSHA256 != manifestSHA256 ||
			receipt.BaseSHA != item.Reference.BaseSHA || receipt.SHA256 != item.Reference.SHA256 || receipt.SizeBytes != item.Reference.SizeBytes {
			return errPlanStepAcceptanceEvidenceRequired
		}
	}
	return nil
}

func planStepCallbackContext(c echo.Context, instanceID uuid.UUID) context.Context {
	ctx := models.WithDeliveryPlanStepAgentInstanceID(c.Request().Context(), instanceID)
	if cfg, ok := c.Get("config").(*models.Config); ok && cfg != nil {
		return configuration.WithConfig(ctx, cfg)
	}
	return ctx
}

func sameDeliveryPlanStepDependencyPatch(left, right models.DeliveryPlanStepDependencyPatch) bool {
	return left.PlanID == right.PlanID && left.AutomationTaskID == right.AutomationTaskID && left.RunID == right.RunID && left.StepID == right.StepID &&
		left.FencingToken == right.FencingToken && left.DependencyStepID == right.DependencyStepID && left.SourcePatchArtifactID == right.SourcePatchArtifactID &&
		left.SourceAutomationTaskID == right.SourceAutomationTaskID && left.SourceRunID == right.SourceRunID && left.WorkerID == right.WorkerID &&
		left.AgentKey == right.AgentKey && left.MachineID == right.MachineID && equalUUIDPointer(left.AgentInstanceID, right.AgentInstanceID) &&
		left.ManifestSHA256 == right.ManifestSHA256 && left.RepositoryRef == right.RepositoryRef && left.BaseSHA == right.BaseSHA &&
		left.SHA256 == right.SHA256 && left.SizeBytes == right.SizeBytes
}

func validDeliveryPlanPatchSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		switch {
		case char >= '0' && char <= '9', char >= 'a' && char <= 'f':
		default:
			return false
		}
	}
	return true
}
