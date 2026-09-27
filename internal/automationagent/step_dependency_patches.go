package automationagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"events-stocks/models"
	"github.com/gofrs/uuid"
)

// buildIntegrationProposal captures only the already-merged state of the
// isolated integration worktrees. It cannot synthesize new edits: the
// downstream implementation runner must prove each exact patch is already
// applied, rerun registered validation/acceptance checks, and emit the final
// per-repository artifact.
func buildIntegrationProposal(ctx context.Context, taskID string, delivery json.RawMessage, lookup func(string) string) (string, error) {
	if !taskIDPattern.MatchString(strings.ToLower(strings.TrimSpace(taskID))) {
		return "", fmt.Errorf("integration workspace identity is invalid")
	}
	var envelope struct {
		ContextSources []struct {
			Kind      string `json:"kind"`
			Reference string `json:"reference"`
		} `json:"context_sources"`
		ApprovedPlan struct {
			RepositoryImpact []struct {
				Reference string `json:"reference"`
				Impact    string `json:"impact"`
			} `json:"repository_impact"`
		} `json:"approved_plan"`
	}
	if err := json.Unmarshal(delivery, &envelope); err != nil {
		return "", fmt.Errorf("integration plan scope is invalid")
	}
	contextRepositories := make(map[string]struct{})
	for _, source := range envelope.ContextSources {
		if source.Kind == "repository" && strings.HasPrefix(strings.TrimSpace(source.Reference), "workspace://") {
			contextRepositories[strings.TrimSpace(source.Reference)] = struct{}{}
		}
	}
	required := make([]string, 0)
	seen := make(map[string]struct{})
	for _, repository := range envelope.ApprovedPlan.RepositoryImpact {
		if !strings.EqualFold(strings.TrimSpace(repository.Impact), "changes") {
			continue
		}
		reference := strings.TrimSpace(repository.Reference)
		if !strings.HasPrefix(reference, "workspace://") {
			return "", fmt.Errorf("integration repository reference is invalid")
		}
		if _, duplicate := seen[reference]; duplicate {
			return "", fmt.Errorf("integration repository scope is duplicated")
		}
		if _, frozen := contextRepositories[reference]; !frozen {
			return "", fmt.Errorf("integration repository is absent from frozen context")
		}
		seen[reference] = struct{}{}
		required = append(required, reference)
	}
	if len(required) == 0 || len(required) > maxStepPatchArtifacts {
		return "", fmt.Errorf("integration requires a bounded, explicit changed-repository set")
	}
	sort.Strings(required)
	patches := make([]RepositoryPatchProposal, 0, len(required))
	for _, reference := range required {
		workspace, err := RegisteredWorkspace(reference, lookup)
		if err != nil {
			return "", fmt.Errorf("integration workspace is unavailable")
		}
		if err := workspace.RequireCapability(WorkspaceCapabilityCreateWorktree); err != nil {
			return "", fmt.Errorf("integration workspace is not permitted")
		}
		worktree, _, err := isolatedWorktree(withSandboxTaskID(ctx, taskID), workspace, taskID)
		if err != nil {
			return "", fmt.Errorf("integration worktree could not be prepared")
		}
		revision, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "rev-parse", "HEAD")
		if err != nil || revision.ExitCode != 0 {
			return "", fmt.Errorf("integration worktree base could not be verified")
		}
		baseSHA := strings.ToLower(strings.TrimSpace(revision.Output))
		if !gitCommitPattern.MatchString(baseSHA) {
			return "", fmt.Errorf("integration worktree base is invalid")
		}
		_, patch, err := captureWorktreePatch(ctx, worktree, baseSHA, maxStepPatchArtifactBytes)
		if err != nil {
			return "", fmt.Errorf("integration merge is incomplete or conflicted")
		}
		if err := scanStepPatchForCredentials(patch); err != nil {
			wipeBytes(patch)
			return "", fmt.Errorf("integration merged patch was rejected by the credential scanner")
		}
		patches = append(patches, RepositoryPatchProposal{RepositoryRef: reference, Patch: string(patch)})
		wipeBytes(patch)
	}
	encoded, err := json.Marshal(ChangeProposal{Summary: "Verify the frozen implementation branches and produce final repository artifacts", Patches: patches})
	if err != nil {
		return "", fmt.Errorf("integration proposal could not be encoded")
	}
	return string(encoded), nil
}

// Dependency patch bodies are intentionally short-lived and non-serializable.
// Never put this type in a checkpoint, task result, callback payload, or log.
type dependencyPatchPayload struct {
	Reference models.DeliveryPlanStepDependencyPatchReference `json:"-"`
	Body      []byte                                          `json:"-"`
	Depth     int                                             `json:"-"`
}

type planStepDependencyPatchCallback interface {
	GetPlanStepDependencyPatchManifest(context.Context, PlanStepLeaseRequest) (PlanStepDependencyPatchManifest, error)
	GetPlanStepDependencyPatch(context.Context, PlanStepLeaseRequest, string) ([]byte, error)
}

func (step *planStepExecution) applyDirectDependencyPatches(ctx context.Context, taskID string, planSteps []PlanStepDTO, lookup func(string) string) error {
	if step == nil || step.claim.Step == nil {
		return fmt.Errorf("claimed plan step is unavailable")
	}
	callback, ok := step.callback.(planStepDependencyPatchCallback)
	if !ok {
		return fmt.Errorf("plan-step dependency patch callback is unavailable")
	}
	manifest, err := callback.GetPlanStepDependencyPatchManifest(ctx, step.lease)
	if err != nil {
		return err
	}
	if err := validatePlanStepDependencyPatchManifest(manifest); err != nil {
		return err
	}
	if err := validateDirectDependencyPatchManifest(manifest, *step.claim.Step, planSteps); err != nil {
		return err
	}
	depthByID := dependencyPatchDepthByID(planSteps)

	patches := make([]dependencyPatchPayload, 0, len(manifest.Patches))
	defer wipeDependencyPatchPayloads(patches)
	for _, reference := range manifest.Patches {
		body, downloadErr := callback.GetPlanStepDependencyPatch(ctx, step.lease, reference.SHA256)
		if downloadErr != nil {
			return downloadErr
		}
		if int64(len(body)) != reference.SizeBytes || !validDependencyPatchDigest(body, reference.SHA256) {
			wipeBytes(body)
			return fmt.Errorf("dependency patch bytes do not match their manifest")
		}
		if err := scanStepPatchForCredentials(body); err != nil {
			wipeBytes(body)
			return err
		}
		patches = append(patches, dependencyPatchPayload{Reference: reference, Body: body, Depth: depthByID[reference.DependencyStepID]})
	}
	patches = omitCoveredAncestorPatchArtifacts(patches, planSteps)
	if err := applyDependencyPatchPayloads(ctx, taskID, patches, lookup); err != nil {
		return err
	}
	step.mu.Lock()
	step.appliedDependencyManifestSHA256 = manifest.ManifestSHA256
	step.appliedDependencyPatchCount = manifest.PatchCount
	step.mu.Unlock()
	return nil
}

// omitCoveredAncestorPatchArtifacts removes a dependency artifact only when
// a direct descendant artifact for the exact same repository and base exists.
// Producer patches are cumulative from the frozen base, so that descendant
// already carries the ancestor's changes; sibling artifacts remain distinct.
func omitCoveredAncestorPatchArtifacts(patches []dependencyPatchPayload, planSteps []PlanStepDTO) []dependencyPatchPayload {
	byKey := make(map[string]PlanStepDTO, len(planSteps))
	byID := make(map[string]PlanStepDTO, len(planSteps))
	for _, step := range planSteps {
		byKey[step.StepKey] = step
		byID[step.ID] = step
	}
	drop := make([]bool, len(patches))
	for index, ancestor := range patches {
		for otherIndex, descendant := range patches {
			if index == otherIndex || ancestor.Reference.RepositoryRef != descendant.Reference.RepositoryRef || ancestor.Reference.BaseSHA != descendant.Reference.BaseSHA {
				continue
			}
			if planStepIsAncestor(ancestor.Reference.DependencyStepID, descendant.Reference.DependencyStepID, byKey, byID, make(map[string]bool)) {
				drop[index] = true
				break
			}
		}
	}
	filtered := make([]dependencyPatchPayload, 0, len(patches))
	for index, patch := range patches {
		if drop[index] {
			wipeBytes(patch.Body)
			continue
		}
		filtered = append(filtered, patch)
	}
	return filtered
}

func planStepIsAncestor(ancestorID, descendantID string, byKey, byID map[string]PlanStepDTO, visiting map[string]bool) bool {
	if visiting[descendantID] {
		return false
	}
	visiting[descendantID] = true
	defer delete(visiting, descendantID)
	descendant, exists := byID[descendantID]
	if !exists {
		return false
	}
	for _, dependencyKey := range descendant.DependsOn {
		dependency, exists := byKey[dependencyKey]
		if !exists {
			continue
		}
		if dependency.ID == ancestorID || planStepIsAncestor(ancestorID, dependency.ID, byKey, byID, visiting) {
			return true
		}
	}
	return false
}

func validateDirectDependencyPatchManifest(manifest PlanStepDependencyPatchManifest, claimed PlanStepDTO, planSteps []PlanStepDTO) error {
	if err := validatePlanStepDependencyPatchManifest(manifest); err != nil {
		return err
	}
	dependencyIDs := make(map[string]struct{}, len(claimed.DependsOn))
	stepByKey := make(map[string]PlanStepDTO, len(planSteps))
	for _, candidate := range planSteps {
		stepByKey[candidate.StepKey] = candidate
	}
	for _, dependencyKey := range claimed.DependsOn {
		dependency, exists := stepByKey[dependencyKey]
		if !exists {
			return fmt.Errorf("claimed step references an unknown direct dependency")
		}
		dependencyIDs[dependency.ID] = struct{}{}
	}
	for _, reference := range manifest.Patches {
		if _, isDirectDependency := dependencyIDs[reference.DependencyStepID]; !isDirectDependency {
			return fmt.Errorf("dependency patch manifest contains a non-direct dependency")
		}
	}
	return nil
}

func applyDependencyPatchPayloads(ctx context.Context, taskID string, patches []dependencyPatchPayload, lookup func(string) string) error {
	if len(patches) > models.MaxDeliveryPlanStepDependencyPatchReferences {
		return fmt.Errorf("dependency patch set exceeds the supported size")
	}
	references := make([]models.DeliveryPlanStepDependencyPatchReference, 0, len(patches))
	for _, patch := range patches {
		if int64(len(patch.Body)) != patch.Reference.SizeBytes || !validDependencyPatchDigest(patch.Body, patch.Reference.SHA256) {
			return fmt.Errorf("dependency patch bytes do not match their manifest")
		}
		references = append(references, patch.Reference)
	}
	if _, err := models.DeliveryPlanStepDependencyPatchManifestSHA256(references); err != nil {
		return fmt.Errorf("dependency patch manifest is invalid")
	}
	if len(patches) == 0 {
		return nil
	}
	if lookup == nil || !taskIDPattern.MatchString(strings.ToLower(strings.TrimSpace(taskID))) {
		return fmt.Errorf("dependency patch workspace identity is invalid")
	}
	ordered := append([]dependencyPatchPayload(nil), patches...)
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i].Reference, ordered[j].Reference
		if ordered[i].Depth != ordered[j].Depth {
			return ordered[i].Depth > ordered[j].Depth
		}
		if left.DependencyStepID != right.DependencyStepID {
			return left.DependencyStepID < right.DependencyStepID
		}
		if left.RepositoryRef != right.RepositoryRef {
			return left.RepositoryRef < right.RepositoryRef
		}
		return left.SHA256 < right.SHA256
	})
	byRepository := make(map[string][]dependencyPatchPayload)
	repositoryRefs := make([]string, 0)
	for _, patch := range ordered {
		repositoryRef := patch.Reference.RepositoryRef
		if _, exists := byRepository[repositoryRef]; !exists {
			repositoryRefs = append(repositoryRefs, repositoryRef)
		}
		byRepository[repositoryRef] = append(byRepository[repositoryRef], patch)
	}
	sort.Strings(repositoryRefs)
	for _, repositoryRef := range repositoryRefs {
		if !models.IsSafeDeliveryPlanStepPatchRepositoryReference(repositoryRef) {
			return fmt.Errorf("dependency patch repository reference is invalid")
		}
		workspace, workspaceErr := RegisteredWorkspace(repositoryRef, lookup)
		if workspaceErr != nil {
			return fmt.Errorf("dependency patch workspace is unavailable")
		}
		if err := workspace.RequireCapability(WorkspaceCapabilityCreateWorktree); err != nil {
			return fmt.Errorf("dependency patch workspace is not permitted")
		}
		if err := workspace.RequireCapability(WorkspaceCapabilityApplyPatch); err != nil {
			return fmt.Errorf("dependency patch workspace is not permitted")
		}
		worktree, _, worktreeErr := isolatedWorktree(ctx, workspace, taskID)
		if worktreeErr != nil {
			return fmt.Errorf("could not prepare isolated dependency worktree")
		}
		revision, revisionErr := runLocal(ctx, worktree, 30*time.Second, "", "git", "rev-parse", "HEAD")
		if revisionErr != nil || revision.ExitCode != 0 || !gitCommitPattern.MatchString(strings.ToLower(strings.TrimSpace(revision.Output))) {
			return fmt.Errorf("could not verify dependency worktree base revision")
		}
		baseSHA := strings.ToLower(strings.TrimSpace(revision.Output))
		repositoryPatches := byRepository[repositoryRef]
		for _, patch := range repositoryPatches {
			if patch.Reference.BaseSHA != baseSHA {
				return fmt.Errorf("dependency patch base does not match the isolated worktree")
			}
		}
		if err := mergeDependencyPatchGroup(ctx, workspace.Root, worktree, taskID, baseSHA, repositoryPatches); err != nil {
			return err
		}
	}
	return nil
}

func applyDependencyPatch(ctx context.Context, worktree string, patch []byte) error {
	if len(patch) == 0 || len(patch) > models.MaxDeliveryPlanStepPatchArtifactBytes {
		return fmt.Errorf("dependency patch size is invalid")
	}
	if err := scanStepPatchForCredentials(patch); err != nil {
		return err
	}
	result, err := runLocalWithBytes(ctx, worktree, 90*time.Second, patch, "git", "apply", "--3way", "--binary", "--whitespace=nowarn", "-")
	if err != nil {
		return fmt.Errorf("dependency patch application failed")
	}
	if result.ExitCode != 0 {
		reversed, reverseErr := runLocalWithBytes(ctx, worktree, 90*time.Second, patch, "git", "apply", "--reverse", "--check", "--binary", "--whitespace=nowarn", "-")
		if reverseErr == nil && reversed.ExitCode == 0 {
			return nil
		}
		return fmt.Errorf("dependency patch conflicted with the isolated worktree")
	}
	return nil
}

// mergeDependencyPatchGroup applies each source patch at its exact BaseSHA in
// a clean worktree, then merges those trees into a scratch copy of the active
// task state. Descendant artifacts are cumulative, so direct sequential apply
// would replay ancestor hunks and conflict instead of producing their union.
func mergeDependencyPatchGroup(ctx context.Context, repositoryRoot, targetWorktree, taskID, baseSHA string, patches []dependencyPatchPayload) error {
	unapplied := make([]dependencyPatchPayload, 0, len(patches))
	for _, patch := range patches {
		// A retry skips only when Git proves this exact patch is already present.
		reversed, err := runLocalWithBytes(ctx, targetWorktree, 90*time.Second, patch.Body, "git", "apply", "--reverse", "--check", "--binary", "--whitespace=nowarn", "-")
		if err != nil {
			return fmt.Errorf("could not verify dependency patch recovery state")
		}
		if reversed.ExitCode != 0 {
			unapplied = append(unapplied, patch)
		}
	}
	if len(unapplied) == 0 {
		return nil
	}
	intent, err := runLocal(ctx, targetWorktree, 30*time.Second, "", "git", "add", "--intent-to-add", "--all")
	if err != nil || intent.ExitCode != 0 {
		return fmt.Errorf("could not snapshot the isolated task worktree")
	}
	currentPatch, err := captureWorktreePatchIfChanged(ctx, targetWorktree, baseSHA, models.MaxDeliveryPlanStepDependencyPatchBytes)
	if err != nil {
		return fmt.Errorf("could not snapshot the isolated task worktree")
	}
	defer wipeBytes(currentPatch)
	worktreeParent := filepath.Join(repositoryRoot, ".itbem-agent-worktrees")
	suffix := uuid.Must(uuid.NewV4()).String()
	hooks := filepath.Join(worktreeParent, ".dh-"+suffix)
	if err := os.MkdirAll(hooks, 0700); err != nil {
		return fmt.Errorf("could not prepare isolated dependency merge")
	}
	defer os.Remove(hooks)
	scratch := filepath.Join(worktreeParent, ".dm-"+suffix)
	created := []string{}
	cleanup := func() {
		for index := len(created) - 1; index >= 0; index-- {
			_, _ = runLocal(context.Background(), repositoryRoot, 30*time.Second, "", "git", "worktree", "remove", "--force", created[index])
		}
	}
	defer cleanup()
	if err := addDependencyScratchWorktree(ctx, repositoryRoot, scratch, baseSHA); err != nil {
		return err
	}
	created = append(created, scratch)
	if len(currentPatch) > 0 {
		if err := applyDependencyScratchDelta(ctx, scratch, currentPatch); err != nil {
			return err
		}
		stage, stageErr := runLocal(ctx, scratch, 30*time.Second, "", "git", "add", "--all")
		if stageErr != nil || stage.ExitCode != 0 {
			return fmt.Errorf("could not stage isolated task snapshot")
		}
	}
	if err := commitDependencyScratchState(ctx, scratch, hooks, "snapshot current task state"); err != nil {
		return err
	}
	currentStateSHA, err := dependencyWorktreeHead(ctx, scratch)
	if err != nil {
		return err
	}

	for index, patch := range unapplied {
		reversedAncestor, reverseErr := runLocalWithBytes(ctx, scratch, 90*time.Second, patch.Body, "git", "apply", "--reverse", "--check", "--binary", "--whitespace=nowarn", "-")
		if reverseErr != nil {
			return fmt.Errorf("could not verify dependency patch ancestry")
		}
		if reversedAncestor.ExitCode == 0 {
			continue
		}
		candidate := filepath.Join(worktreeParent, ".dp-"+suffix+"-"+fmt.Sprint(index))
		if err := addDependencyScratchWorktree(ctx, repositoryRoot, candidate, baseSHA); err != nil {
			return err
		}
		created = append(created, candidate)
		candidateHead, err := dependencyWorktreeHead(ctx, candidate)
		if err != nil || candidateHead != patch.Reference.BaseSHA {
			return fmt.Errorf("dependency patch source worktree has an unexpected base")
		}
		if err := applyDependencyPatch(ctx, candidate, patch.Body); err != nil {
			return err
		}
		stage, stageErr := runLocal(ctx, candidate, 30*time.Second, "", "git", "add", "--all")
		if stageErr != nil || stage.ExitCode != 0 {
			return fmt.Errorf("could not stage isolated dependency patch state")
		}
		if err := commitDependencyScratchState(ctx, candidate, hooks, "apply verified dependency patch"); err != nil {
			return err
		}
		candidateSHA, err := dependencyWorktreeHead(ctx, candidate)
		if err != nil {
			return err
		}
		if candidateSHA == baseSHA {
			continue
		}
		merge, mergeErr := runLocal(ctx, scratch, 90*time.Second, "", "git", "-c", "core.hooksPath="+hooks, "-c", "commit.gpgsign=false", "-c", "user.name=ITBEM Automation", "-c", "user.email=automation@invalid", "merge", "--no-ff", "--no-edit", candidateSHA)
		if mergeErr != nil || merge.ExitCode != 0 {
			return fmt.Errorf("dependency patch set conflicts at its shared base: %s", merge.Output)
		}
	}
	finalDelta, err := captureWorktreePatchIfChanged(ctx, scratch, currentStateSHA, models.MaxDeliveryPlanStepDependencyPatchBytes)
	if err != nil {
		return fmt.Errorf("could not verify merged dependency patch set")
	}
	defer wipeBytes(finalDelta)
	if len(finalDelta) == 0 {
		return nil
	}
	if err := applyDependencyScratchDelta(ctx, targetWorktree, finalDelta); err != nil {
		return err
	}
	return nil
}

func captureWorktreePatchIfChanged(ctx context.Context, worktree, baseSHA string, maxBytes int) ([]byte, error) {
	if maxBytes < 1 {
		return nil, fmt.Errorf("worktree patch bound is invalid")
	}
	changed, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "diff", "--quiet", baseSHA, "--")
	if err != nil {
		return nil, fmt.Errorf("could not inspect isolated worktree changes")
	}
	if changed.ExitCode == 0 {
		return nil, nil
	}
	if changed.ExitCode != 1 {
		return nil, fmt.Errorf("could not inspect isolated worktree changes")
	}
	_, body, err := captureWorktreePatch(ctx, worktree, baseSHA, maxBytes)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func addDependencyScratchWorktree(ctx context.Context, repositoryRoot, path, baseSHA string) error {
	result, err := runLocal(ctx, repositoryRoot, 90*time.Second, "", "git", "worktree", "add", "--detach", path, baseSHA)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("could not create a clean dependency worktree")
	}
	return nil
}

func dependencyWorktreeHead(ctx context.Context, worktree string) (string, error) {
	result, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "rev-parse", "HEAD")
	if err != nil || result.ExitCode != 0 || !gitCommitPattern.MatchString(strings.ToLower(strings.TrimSpace(result.Output))) {
		return "", fmt.Errorf("could not verify a dependency worktree base")
	}
	return strings.ToLower(strings.TrimSpace(result.Output)), nil
}

func applyDependencyScratchDelta(ctx context.Context, worktree string, patch []byte) error {
	checked, err := runLocalWithBytes(ctx, worktree, 90*time.Second, patch, "git", "apply", "--check", "--binary", "--whitespace=nowarn", "-")
	if err != nil {
		return fmt.Errorf("merged dependency delta could not be checked")
	}
	if checked.ExitCode != 0 {
		reversed, reverseErr := runLocalWithBytes(ctx, worktree, 90*time.Second, patch, "git", "apply", "--reverse", "--check", "--binary", "--whitespace=nowarn", "-")
		if reverseErr == nil && reversed.ExitCode == 0 {
			return nil
		}
		return fmt.Errorf("merged dependency delta conflicts with isolated task state")
	}
	applied, err := runLocalWithBytes(ctx, worktree, 90*time.Second, patch, "git", "apply", "--binary", "--whitespace=nowarn", "-")
	if err != nil || applied.ExitCode != 0 {
		return fmt.Errorf("merged dependency delta could not be applied")
	}
	return nil
}

func commitDependencyScratchState(ctx context.Context, worktree, hooks, message string) error {
	changed, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "diff", "--cached", "--quiet", "HEAD", "--")
	if err != nil {
		return fmt.Errorf("could not inspect scratch dependency state")
	}
	if changed.ExitCode == 0 {
		return nil
	}
	if changed.ExitCode != 1 {
		return fmt.Errorf("could not inspect scratch dependency state")
	}
	result, err := runLocal(ctx, worktree, 90*time.Second, "", "git", "-c", "core.hooksPath="+hooks, "-c", "commit.gpgsign=false", "-c", "user.name=ITBEM Automation", "-c", "user.email=automation@invalid", "commit", "--no-verify", "-m", message)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("could not finalize scratch dependency state")
	}
	return nil
}

func dependencyPatchDepthByID(steps []PlanStepDTO) map[string]int {
	byKey := make(map[string]PlanStepDTO, len(steps))
	for _, step := range steps {
		byKey[step.StepKey] = step
	}
	depths := make(map[string]int, len(steps))
	visiting := make(map[string]bool, len(steps))
	var depth func(PlanStepDTO) int
	depth = func(step PlanStepDTO) int {
		if cached, ok := depths[step.ID]; ok {
			return cached
		}
		if visiting[step.StepKey] {
			return 0
		}
		visiting[step.StepKey] = true
		maximum := -1
		for _, dependencyKey := range step.DependsOn {
			dependency, ok := byKey[dependencyKey]
			if !ok {
				continue
			}
			if candidate := depth(dependency); candidate > maximum {
				maximum = candidate
			}
		}
		delete(visiting, step.StepKey)
		value := maximum + 1
		depths[step.ID] = value
		return value
	}
	for _, step := range steps {
		depth(step)
	}
	return depths
}

func validDependencyPatchDigest(patch []byte, expected string) bool {
	digest := sha256.Sum256(patch)
	return hex.EncodeToString(digest[:]) == expected
}

func wipeDependencyPatchPayloads(patches []dependencyPatchPayload) {
	for index := range patches {
		wipeBytes(patches[index].Body)
		patches[index].Body = nil
	}
}
