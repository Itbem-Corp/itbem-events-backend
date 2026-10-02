package automationagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"events-stocks/models"
)

const (
	maxPatchBytes    = 600000
	maxCommandOutput = 12000
	commandTimeout   = 10 * time.Minute
)

var (
	taskIDPattern     = regexp.MustCompile(`^[a-f0-9-]{36}$`)
	gitCommitPattern  = regexp.MustCompile(`^[a-f0-9]{40}$`)
	unifiedHunkHeader = regexp.MustCompile(`^@@ -([0-9]+)(?:,[0-9]+)? \+([0-9]+)(?:,[0-9]+)? @@(.*)$`)
)

type ChangeProposal struct {
	Summary string                    `json:"summary"`
	Patch   string                    `json:"patch,omitempty"`
	Patches []RepositoryPatchProposal `json:"patches,omitempty"`
}

// RepositoryPatchProposal binds a diff to an immutable workspace reference
// from the human-approved repository impact matrix. A multi-repository task
// must never collapse independent changes into an unnamed aggregate diff.
type RepositoryPatchProposal struct {
	RepositoryRef string `json:"repository_ref"`
	Patch         string `json:"patch"`
}

func ParseChangeProposal(content string) (ChangeProposal, error) {
	candidate := strings.TrimSpace(content)
	if strings.HasPrefix(candidate, "```") && strings.HasSuffix(candidate, "```") {
		parts := strings.SplitN(candidate, "\n", 2)
		if len(parts) != 2 {
			return ChangeProposal{}, fmt.Errorf("implementation model response must be JSON")
		}
		candidate = strings.TrimSpace(strings.TrimSuffix(parts[1], "```"))
	}
	value, ok := decodeJSONObject(candidate)
	if !ok {
		return ChangeProposal{}, fmt.Errorf("implementation model response must be JSON")
	}
	proposal := ChangeProposal{
		Summary: firstProposalString(value, "summary", "Summary", "description", "Description"),
		Patch:   firstProposalString(value, "patch", "Patch", "diff", "Diff"),
	}
	if strings.TrimSpace(proposal.Summary) == "" || len(proposal.Summary) > maxCommandOutput {
		return ChangeProposal{}, fmt.Errorf("implementation response needs a bounded summary")
	}
	if rawPatches, exists := value["patches"]; exists {
		entries, ok := rawPatches.([]any)
		if !ok || len(entries) == 0 || strings.TrimSpace(proposal.Patch) != "" {
			return ChangeProposal{}, fmt.Errorf("implementation response must provide either one patch or a non-empty repository patch list")
		}
		seen := make(map[string]struct{}, len(entries))
		proposal.Patches = make([]RepositoryPatchProposal, 0, len(entries))
		for _, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok {
				return ChangeProposal{}, fmt.Errorf("implementation repository patches must be structured")
			}
			reference := strings.TrimSpace(firstProposalString(entry, "repository_ref", "repositoryRef", "reference"))
			patch := firstProposalString(entry, "patch", "Patch", "diff", "Diff")
			if !strings.HasPrefix(reference, "workspace://") || len(reference) > 256 {
				return ChangeProposal{}, fmt.Errorf("implementation repository patch reference is invalid")
			}
			if _, duplicate := seen[reference]; duplicate {
				return ChangeProposal{}, fmt.Errorf("implementation repository patch references must be unique")
			}
			if err := validateProposalPatch(patch); err != nil {
				return ChangeProposal{}, err
			}
			seen[reference] = struct{}{}
			proposal.Patches = append(proposal.Patches, RepositoryPatchProposal{RepositoryRef: reference, Patch: patch})
		}
	} else if err := validateProposalPatch(proposal.Patch); err != nil {
		return ChangeProposal{}, err
	}
	proposal.Summary = strings.TrimSpace(proposal.Summary)
	return proposal, nil
}

func validateProposalPatch(patch string) error {
	if strings.TrimSpace(patch) == "" || len([]byte(patch)) > maxPatchBytes || !strings.Contains(patch, "diff --git ") || !strings.Contains(patch, "\n--- ") || !strings.Contains(patch, "\n+++ ") {
		return fmt.Errorf("implementation response needs a bounded unified Git diff")
	}
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "diff --git ") {
			if unsafePatchPath(line) {
				return fmt.Errorf("implementation patch touches a forbidden path")
			}
		}
	}
	return nil
}

// firstProposalString accepts harmless naming differences from providers but
// never relaxes the subsequent bounded-summary and unified-diff checks.
func firstProposalString(value map[string]any, names ...string) string {
	for _, name := range names {
		if text, ok := value[name].(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

// normalizeUnifiedPatchHunkCounts repairs only a mechanically inconsistent
// hunk count. It never changes paths or added/removed content, and callers
// must still prove the result with git apply --check before using it.
func normalizeUnifiedPatchHunkCounts(patch string) (string, bool) {
	hadTrailingNewline := strings.HasSuffix(patch, "\n")
	lines := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")
	changed := false
	for index := 0; index < len(lines); index++ {
		matches := unifiedHunkHeader.FindStringSubmatch(lines[index])
		if matches == nil {
			continue
		}
		oldCount, newCount := 0, 0
		end := index + 1
		for ; end < len(lines) && !strings.HasPrefix(lines[end], "@@ ") && !strings.HasPrefix(lines[end], "diff --git "); end++ {
			line := lines[end]
			if line == "\\ No newline at end of file" {
				continue
			}
			if line == "" {
				return patch, false
			}
			switch line[0] {
			case ' ':
				oldCount++
				newCount++
			case '-':
				oldCount++
			case '+':
				newCount++
			default:
				return patch, false
			}
		}
		replacement := fmt.Sprintf("@@ -%s,%d +%s,%d @@%s", matches[1], oldCount, matches[2], newCount, matches[3])
		if replacement != lines[index] {
			lines[index] = replacement
			changed = true
		}
		index = end - 1
	}
	if !changed {
		return patch, false
	}
	result := strings.Join(lines, "\n")
	if hadTrailingNewline {
		result += "\n"
	}
	return result, true
}

func unsafePatchPath(line string) bool {
	for _, field := range strings.Fields(strings.ReplaceAll(line, "\\", "/")) {
		field = strings.Trim(field, "\"")
		if !strings.HasPrefix(field, "a/") && !strings.HasPrefix(field, "b/") {
			continue
		}
		path := strings.TrimPrefix(strings.TrimPrefix(field, "a/"), "b/")
		if path == "" || strings.HasPrefix(path, "/") {
			return true
		}
		for _, segment := range strings.Split(strings.ToLower(path), "/") {
			if segment == "" || segment == "." || segment == ".." || segment == ".git" || strings.HasPrefix(segment, ".env") {
				return true
			}
		}
	}
	return strings.Contains(strings.ReplaceAll(line, "\\", "/"), "../")
}

func RunImplementation(ctx context.Context, taskID string, delivery json.RawMessage, modelContent string, lookup func(string) string) (map[string]any, error) {
	if required, declared := approvedChangedRepositoryReferences(delivery); declared && len(required) == 0 {
		return nil, fmt.Errorf("approved plan declares no repository changes; implementation must not create a worktree")
	}
	proposal, err := ParseChangeProposal(modelContent)
	if err != nil {
		return nil, err
	}
	if !taskIDPattern.MatchString(strings.ToLower(taskID)) {
		return nil, fmt.Errorf("task ID is invalid for a local worktree")
	}
	if err := PrepareDeliveryWorkspaces(ctx, delivery, lookup); err != nil {
		return nil, err
	}
	targets, err := deliveryImplementationTargets(delivery, proposal, lookup)
	if err != nil {
		return nil, err
	}
	if len(targets) > maxStepPatchArtifacts {
		return nil, fmt.Errorf("implementation exceeds the supported repository patch count")
	}
	changeSets := make([]any, 0, len(targets))
	executionOrder := make([]string, 0, len(targets))
	for _, target := range targets {
		var changeSet map[string]any
		operationErr, reportErr := runStepActivityWithDetails(ctx, "file_change", "workspace_patch", func(phase string) *models.DeliveryPlanStepActivityDetails {
			details := &models.DeliveryPlanStepActivityDetails{ResourceReferences: []string{"workspace://" + target.workspace.ID}}
			if phase == models.DeliveryPlanStepActivityCompleted {
				files, filesErr := patchChangedFiles(target.patch)
				if filesErr != nil {
					return nil
				}
				details.ChangedFiles = files
			}
			if models.ValidateDeliveryPlanStepActivityDetails(models.DeliveryPlanStepActivityFileChange, phase, details) != nil {
				return nil
			}
			return details
		}, func() error {
			var runErr error
			repositoryRef := strings.TrimSpace(target.reference)
			if repositoryRef == "" {
				repositoryRef = "workspace://" + target.workspace.ID
			}
			changeSet, runErr = runWorkspaceImplementation(ctx, taskID, target.workspace, repositoryRef, target.revision, target.patch, target.allowedPaths)
			return runErr
		})
		if reportErr != nil {
			return nil, reportErr
		}
		runErr := operationErr
		if runErr != nil {
			// Preserve completed repositories even when a later repository fails.
			// The caller still receives the error and keeps the task failed; this
			// projection is private reconciliation evidence, never a success signal
			// or permission to retry an external effect.
			return map[string]any{
				"summary":                    proposal.Summary,
				"change_sets":                changeSets,
				"repository_execution_order": executionOrder,
				"completed_repositories":     append([]string(nil), executionOrder...),
				"failed_repository":          "workspace://" + target.workspace.ID,
				"partial":                    len(changeSets) > 0,
				"deployment":                 "not attempted; partial repository work requires reconciliation before any retry",
			}, runErr
		}
		changeSets = append(changeSets, changeSet)
		executionOrder = append(executionOrder, "workspace://"+target.workspace.ID)
	}
	if len(changeSets) == 1 {
		result := changeSets[0].(map[string]any)
		result["summary"] = proposal.Summary
		result["repository_execution_order"] = executionOrder
		return result, nil
	}
	return map[string]any{
		"summary": proposal.Summary, "change_sets": changeSets, "repository_execution_order": executionOrder,
		"deployment": "not attempted; every repository change needs human code review before preview deployment",
	}, nil
}

type implementationTarget struct {
	workspace    Workspace
	reference    string
	revision     string
	patch        string
	allowedPaths []string
}

// repositoryTopologyEntry is supplied by the human-frozen project map. It
// carries only references and dependency edges; it never lets model output
// choose a new repository or execution order.
type repositoryTopologyEntry struct {
	Reference string   `json:"reference"`
	DependsOn []string `json:"depends_on"`
}

// topologicalRepositoryOrder returns dependencies before their consumers and
// uses lexical order for independent nodes, making a multi-repository run
// reproducible even if a model returns its patch list in a different order.
func topologicalRepositoryOrder(references []string, topology []repositoryTopologyEntry) ([]string, error) {
	known := make(map[string]struct{}, len(references))
	for _, reference := range references {
		reference = strings.TrimSpace(reference)
		if reference == "" {
			return nil, fmt.Errorf("repository execution reference is invalid")
		}
		known[reference] = struct{}{}
	}
	dependencies := make(map[string][]string, len(known))
	for reference := range known {
		dependencies[reference] = nil
	}
	for _, repository := range topology {
		reference := strings.TrimSpace(repository.Reference)
		if _, selected := known[reference]; !selected {
			continue
		}
		for _, rawDependency := range repository.DependsOn {
			dependency := strings.TrimSpace(rawDependency)
			if _, selected := known[dependency]; selected {
				dependencies[reference] = append(dependencies[reference], dependency)
			}
		}
		sort.Strings(dependencies[reference])
	}
	orderedRoots := make([]string, 0, len(known))
	for reference := range known {
		orderedRoots = append(orderedRoots, reference)
	}
	sort.Strings(orderedRoots)
	states := make(map[string]uint8, len(known))
	ordered := make([]string, 0, len(known))
	var visit func(string) error
	visit = func(reference string) error {
		switch states[reference] {
		case 1:
			return fmt.Errorf("repository execution dependencies contain a cycle")
		case 2:
			return nil
		}
		states[reference] = 1
		for _, dependency := range dependencies[reference] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		states[reference] = 2
		ordered = append(ordered, reference)
		return nil
	}
	for _, reference := range orderedRoots {
		if err := visit(reference); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

// deliveryImplementationTargets joins the model's explicit patch list to the
// frozen, human-approved impact matrix. Any missing, extra, or supporting
// repository patch fails before a worktree is created.
func deliveryImplementationTargets(delivery json.RawMessage, proposal ChangeProposal, lookup func(string) string) ([]implementationTarget, error) {
	if len(proposal.Patches) == 0 {
		if required, declared := approvedChangedRepositoryReferences(delivery); declared && len(required) > 1 {
			return nil, fmt.Errorf("implementation must provide a separate repository patch for every repository marked as changed in the approved plan")
		} else if declared {
			if len(required) != 1 {
				return nil, fmt.Errorf("implementation requires an explicitly changed repository")
			}
			for reference := range required {
				proposal.Patches = []RepositoryPatchProposal{{RepositoryRef: reference, Patch: proposal.Patch}}
			}
			return deliveryImplementationTargets(delivery, proposal, lookup)
		}
		workspace, err := deliveryRepositoryWorkspace(delivery, lookup)
		if err != nil {
			return nil, err
		}
		revision, err := deliveryWorkspaceRevision(delivery, "workspace://"+workspace.ID)
		if err != nil {
			return nil, err
		}
		return []implementationTarget{{workspace: workspace, revision: revision, patch: proposal.Patch}}, nil
	}
	var value struct {
		ContextSources []struct {
			Kind      string `json:"kind"`
			Reference string `json:"reference"`
			Revision  string `json:"revision"`
		} `json:"context_sources"`
		ApprovedPlan struct {
			RepositoryImpact []struct {
				Reference    string   `json:"reference"`
				Impact       string   `json:"impact"`
				AllowedPaths []string `json:"allowed_paths"`
			} `json:"repository_impact"`
		} `json:"approved_plan"`
		RepositoryTopology []repositoryTopologyEntry `json:"repository_topology"`
	}
	if err := json.Unmarshal(delivery, &value); err != nil {
		return nil, fmt.Errorf("delivery input must be a JSON object")
	}
	contextRepositories := make(map[string]string)
	for _, source := range value.ContextSources {
		if source.Kind == "repository" && strings.HasPrefix(strings.TrimSpace(source.Reference), "workspace://") {
			contextRepositories[strings.TrimSpace(source.Reference)] = strings.ToLower(strings.TrimSpace(source.Revision))
		}
	}
	required := make(map[string]struct{})
	allowedPathsByReference := make(map[string][]string)
	for _, impact := range value.ApprovedPlan.RepositoryImpact {
		if strings.EqualFold(strings.TrimSpace(impact.Impact), "changes") {
			required[strings.TrimSpace(impact.Reference)] = struct{}{}
			allowedPathsByReference[strings.TrimSpace(impact.Reference)] = append([]string(nil), impact.AllowedPaths...)
		}
	}
	if len(required) == 0 || len(required) != len(proposal.Patches) {
		return nil, fmt.Errorf("implementation repository patches must cover every repository marked as changed in the approved plan")
	}
	targets := make([]implementationTarget, 0, len(proposal.Patches))
	for _, proposalPatch := range proposal.Patches {
		reference := strings.TrimSpace(proposalPatch.RepositoryRef)
		if _, approved := required[reference]; !approved {
			return nil, fmt.Errorf("implementation patch is outside the approved repository impact: %s", reference)
		}
		revision, present := contextRepositories[reference]
		if !present {
			return nil, fmt.Errorf("implementation patch repository is absent from frozen context: %s", reference)
		}
		if revision != "" && !gitCommitPattern.MatchString(revision) {
			return nil, fmt.Errorf("implementation patch repository has an invalid frozen revision: %s", reference)
		}
		workspace, err := RegisteredWorkspace(reference, lookup)
		if err != nil {
			return nil, err
		}
		targets = append(targets, implementationTarget{workspace: workspace, reference: reference, revision: revision, patch: proposalPatch.Patch, allowedPaths: allowedPathsByReference[reference]})
	}
	references := make([]string, 0, len(targets))
	targetByReference := make(map[string]implementationTarget, len(targets))
	for _, target := range targets {
		references = append(references, target.reference)
		targetByReference[target.reference] = target
	}
	orderedReferences, err := topologicalRepositoryOrder(references, value.RepositoryTopology)
	if err != nil {
		return nil, err
	}
	orderedTargets := make([]implementationTarget, 0, len(orderedReferences))
	for _, reference := range orderedReferences {
		orderedTargets = append(orderedTargets, targetByReference[reference])
	}
	return orderedTargets, nil
}

func deliveryWorkspaceRevision(delivery json.RawMessage, reference string) (string, error) {
	var value struct {
		ContextSources []struct {
			Kind      string `json:"kind"`
			Reference string `json:"reference"`
			Revision  string `json:"revision"`
		} `json:"context_sources"`
	}
	if err := json.Unmarshal(delivery, &value); err != nil {
		return "", fmt.Errorf("delivery input must be a JSON object")
	}
	for _, source := range value.ContextSources {
		if source.Kind == "repository" && strings.TrimSpace(source.Reference) == reference {
			revision := strings.ToLower(strings.TrimSpace(source.Revision))
			if revision != "" && !gitCommitPattern.MatchString(revision) {
				return "", fmt.Errorf("frozen repository revision is invalid")
			}
			return revision, nil
		}
	}
	return "", nil
}

// approvedChangedRepositoryReferences intentionally reports whether the plan
// actually declared a matrix. Legacy inputs predate that contract and remain
// compatible with one primary-repository patch; modern multi-repository plans
// must use explicit repository patches.
func approvedChangedRepositoryReferences(delivery json.RawMessage) (map[string]struct{}, bool) {
	var value struct {
		ApprovedPlan map[string]json.RawMessage `json:"approved_plan"`
	}
	if json.Unmarshal(delivery, &value) != nil || value.ApprovedPlan == nil {
		return nil, false
	}
	raw, ok := value.ApprovedPlan["repository_impact"]
	if !ok {
		return nil, false
	}
	var entries []struct {
		Reference string `json:"reference"`
		Impact    string `json:"impact"`
	}
	if json.Unmarshal(raw, &entries) != nil {
		return nil, true
	}
	required := make(map[string]struct{})
	for _, entry := range entries {
		if strings.EqualFold(strings.TrimSpace(entry.Impact), "changes") {
			required[strings.TrimSpace(entry.Reference)] = struct{}{}
		}
	}
	return required, true
}

func runWorkspaceImplementation(ctx context.Context, taskID string, workspace Workspace, repositoryRef, expectedRevision, patch string, allowedPaths []string) (map[string]any, error) {
	ctx = withSandboxTaskID(ctx, taskID)
	if err := workspace.RequireCapability(WorkspaceCapabilityCreateWorktree); err != nil {
		return nil, err
	}
	if err := workspace.RequireCapability(WorkspaceCapabilityApplyPatch); err != nil {
		return nil, err
	}
	if err := validatePatchPaths(patch, allowedPaths); err != nil {
		return nil, err
	}
	worktree, branch, err := isolatedWorktreeAt(ctx, workspace, taskID, expectedRevision)
	if err != nil {
		return nil, err
	}
	baseRevision, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "rev-parse", "HEAD")
	if err != nil || baseRevision.ExitCode != 0 || !gitCommitPattern.MatchString(strings.ToLower(strings.TrimSpace(baseRevision.Output))) {
		return nil, fmt.Errorf("could not determine the isolated worktree base revision")
	}
	baseSHA := strings.ToLower(strings.TrimSpace(baseRevision.Output))
	// A workspace may legitimately be local-only during implementation. When it
	// has a GitHub origin, however, capture that immutable identity now so a
	// later publication grant cannot be redirected to another remote.
	githubRepository := ""
	if remote, remoteErr := gitHubOrigin(ctx, worktree); remoteErr == nil {
		githubRepository = remote.Owner + "/" + remote.Name
	}
	patchNormalized := false
	check, err := runLocal(ctx, worktree, 90*time.Second, patch, "git", "apply", "--check", "--whitespace=nowarn", "-")
	if err != nil {
		return nil, err
	}
	alreadyApplied := false
	if check.ExitCode != 0 {
		if normalized, changed := normalizeUnifiedPatchHunkCounts(patch); changed {
			repaired, repairErr := runLocal(ctx, worktree, 90*time.Second, normalized, "git", "apply", "--check", "--whitespace=nowarn", "-")
			if repairErr != nil {
				return nil, repairErr
			}
			if repaired.ExitCode == 0 {
				patch = normalized
				patchNormalized = true
				check = repaired
			}
		}
		if check.ExitCode != 0 {
			reverse, runErr := runLocal(ctx, worktree, 90*time.Second, patch, "git", "apply", "--reverse", "--check", "--whitespace=nowarn", "-")
			if runErr != nil {
				return nil, runErr
			}
			if reverse.ExitCode != 0 {
				return nil, fmt.Errorf("proposed patch cannot be applied safely: %s", check.Output)
			}
			alreadyApplied = true
		}
	}
	if !alreadyApplied {
		applied, runErr := runLocal(ctx, worktree, 90*time.Second, patch, "git", "apply", "--whitespace=nowarn", "-")
		if runErr != nil {
			return nil, runErr
		}
		if applied.ExitCode != 0 {
			return nil, fmt.Errorf("proposed patch failed to apply: %s", applied.Output)
		}
	}
	// Record new, untracked files as intent-to-add before validation and the
	// review digest. `git diff` otherwise omits newly-created files, allowing a
	// reviewer to approve a fingerprint that says nothing about them. Intent
	// entries keep contents unstaged; publication later verifies the identical
	// full staged diff before committing.
	intent, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "add", "--intent-to-add", "--all")
	if err != nil || intent.ExitCode != 0 {
		return nil, fmt.Errorf("could not prepare new files for reviewed diff")
	}
	componentValidations := workspace.ValidationCommandsForScopes(allowedPaths)
	validations := make([]map[string]any, 0, len(workspace.Config.ValidationCommands)+len(componentValidations))
	validationCtx := withStepActivityAction(ctx, "validation", "registered_validation")
	for _, command := range workspace.Config.ValidationCommands {
		result, runErr := runWorkspaceCommand(validationCtx, workspace, worktree, commandTimeout, "", nil, command[0], command[1:]...)
		if runErr != nil {
			return nil, runErr
		}
		validations = append(validations, map[string]any{"scope": "repository", "command": command, "passed": result.ExitCode == 0, "output": result.Output, "sandbox_lease": result.SandboxLease})
	}
	for _, scoped := range componentValidations {
		result, runErr := runWorkspaceCommand(validationCtx, workspace, worktree, commandTimeout, "", nil, scoped.Command[0], scoped.Command[1:]...)
		if runErr != nil {
			return nil, runErr
		}
		validations = append(validations, map[string]any{"scope": scoped.Scope, "command": scoped.Command, "passed": result.ExitCode == 0, "output": result.Output, "sandbox_lease": result.SandboxLease})
	}
	diffCheck, err := runLocal(withStepActivityAction(ctx, "validation", "diff_check"), worktree, 30*time.Second, "", "git", "diff", "--check")
	if err != nil {
		return nil, err
	}
	stat, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "diff", "--stat")
	if err != nil {
		return nil, err
	}
	reviewDiffSHA256, err := worktreeDiffSHA256(ctx, worktree, baseSHA, false)
	if err != nil {
		return nil, err
	}
	capturedDiffSHA256, patchBytes, err := captureWorktreePatch(ctx, worktree, baseSHA, maxStepPatchArtifactBytes)
	if err != nil {
		return nil, err
	}
	if capturedDiffSHA256 != reviewDiffSHA256 {
		return nil, fmt.Errorf("captured patch digest does not match reviewed worktree diff")
	}
	if len(patchBytes) == 0 || len(patchBytes) > maxStepPatchArtifactBytes {
		return nil, fmt.Errorf("reviewed patch exceeds the supported artifact size")
	}
	sourceDigest, err := sandboxWorktreeDigest(worktree)
	if err != nil {
		return nil, fmt.Errorf("could not bind reviewed source manifest: %w", err)
	}
	return map[string]any{
		"workspace": "workspace://" + workspace.ID,
		"worktree":  "workspace://" + workspace.ID + "#" + branch, "branch": branch,
		"base_sha":              baseSHA,
		"github_repository":     githubRepository,
		"review_diff_sha256":    reviewDiffSHA256,
		"review_source_sha256":  fmt.Sprintf("%x", sourceDigest),
		"patch_already_applied": alreadyApplied, "patch_hunk_counts_normalized": patchNormalized, "diff_check_passed": diffCheck.ExitCode == 0,
		"diff_check": diffCheck.Output, "diff_stat": stat.Output, "validations": validations,
		"allowed_paths":             allowedPaths,
		"deployment":                "not attempted; a human code-review gate is required before preview deployment",
		privateStepPatchArtifactKey: newStepPatchArtifactPayload(repositoryRef, baseSHA, reviewDiffSHA256, patchBytes),
	}, nil
}

// validatePatchPaths enforces an approved monorepo/component scope before a
// worktree is even created. Empty scopes preserve the single-repository
// contract; non-empty scopes require every touched path to live under one of
// the exact relative roots approved by the human plan.
func validatePatchPaths(patch string, allowedPaths []string) error {
	if len(allowedPaths) == 0 {
		return nil
	}
	allowed := make([]string, 0, len(allowedPaths))
	for _, raw := range allowedPaths {
		path := strings.Trim(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"), " /")
		if path != "" {
			allowed = append(allowed, path)
		}
	}
	if len(allowed) == 0 {
		return fmt.Errorf("implementation component scope is empty")
	}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(strings.ReplaceAll(patch, "\\", "/"), "\n") {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return fmt.Errorf("implementation diff has an invalid file header")
		}
		for _, field := range fields[2:4] {
			path := strings.TrimPrefix(strings.Trim(field, "\""), "a/")
			path = strings.TrimPrefix(path, "b/")
			if path == "/dev/null" {
				continue
			}
			seen[path] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return fmt.Errorf("implementation diff has no file paths")
	}
	for path := range seen {
		matched := false
		for _, scope := range allowed {
			if path == scope || strings.HasPrefix(path, scope+"/") {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("implementation patch touches %s outside approved component scope", path)
		}
	}
	return nil
}

// worktreeDiffSHA256 pins the exact reviewed change set, including untracked
// additions once they are staged. It is deliberately computed from Git's
// binary/full-index representation so the publication worker can reproduce
// the same value before committing. A missing diff is never publishable.
func worktreeDiffSHA256(ctx context.Context, worktree, baseSHA string, staged bool) (string, error) {
	args := []string{"diff"}
	if staged {
		args = append(args, "--cached")
	}
	args = append(args, "--binary", "--full-index", "--no-ext-diff", "--no-textconv", strings.ToLower(strings.TrimSpace(baseSHA)))
	commandCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, "git", args...)
	command.Dir = worktree
	command.Env = repositoryCommandEnvironment(os.Environ(), nil)
	digest := sha256.New()
	output := &countedDigest{Writer: digest}
	command.Stdout = output
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("could not calculate reviewed worktree digest")
	}
	if output.Bytes == 0 {
		return "", fmt.Errorf("reviewed worktree has no publishable diff")
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

type countedDigest struct {
	io.Writer
	Bytes int64
}

func (w *countedDigest) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.Bytes += int64(n)
	return n, err
}

func deliveryRepositoryWorkspace(delivery json.RawMessage, lookup func(string) string) (Workspace, error) {
	var value struct {
		ContextSources []struct {
			Kind      string         `json:"kind"`
			Reference string         `json:"reference"`
			Metadata  map[string]any `json:"metadata"`
		} `json:"context_sources"`
	}
	if json.Unmarshal(delivery, &value) != nil {
		return Workspace{}, fmt.Errorf("delivery input must be a JSON object")
	}
	var references []string
	var primaryReferences []string
	for _, source := range value.ContextSources {
		if source.Kind == "repository" {
			references = append(references, source.Reference)
			if role, _ := source.Metadata["repository_role"].(string); strings.EqualFold(strings.TrimSpace(role), "primary") {
				primaryReferences = append(primaryReferences, source.Reference)
			}
		}
	}
	if len(references) == 1 {
		return RegisteredWorkspace(references[0], lookup)
	}
	if len(primaryReferences) == 1 {
		return RegisteredWorkspace(primaryReferences[0], lookup)
	}
	if len(references) == 0 {
		return Workspace{}, fmt.Errorf("implementation requires a registered repository context")
	}
	return Workspace{}, fmt.Errorf("multi-repository implementation requires exactly one context source with metadata.repository_role=primary")
}

func isolatedWorktree(ctx context.Context, workspace Workspace, taskID string) (string, string, error) {
	inside, err := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "rev-parse", "--is-inside-work-tree")
	if err != nil || inside.ExitCode != 0 || strings.TrimSpace(inside.Output) != "true" {
		return "", "", fmt.Errorf("registered workspace must be a Git worktree")
	}
	branch := "itbem-agent/" + taskID
	directory := filepath.Join(workspace.Root, ".itbem-agent-worktrees", taskID)
	if info, statErr := os.Stat(directory); statErr == nil && info.IsDir() {
		actual, err := filepath.EvalSymlinks(directory)
		if err != nil || !strings.EqualFold(filepath.Clean(actual), filepath.Clean(directory)) {
			return "", "", fmt.Errorf("isolated worktree cannot be a linked directory")
		}
		current, err := runLocal(ctx, directory, 20*time.Second, "", "git", "branch", "--show-current")
		if err != nil || current.ExitCode != 0 || strings.TrimSpace(current.Output) != branch {
			return "", "", fmt.Errorf("isolated worktree branch no longer matches the task")
		}
		if err := copyPinnedContractFixture(workspace.Root, directory); err != nil {
			return "", "", err
		}
		return directory, branch, nil
	}
	if err := os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
		return "", "", fmt.Errorf("prepare isolated worktree: %w", err)
	}
	created, err := runLocal(ctx, workspace.Root, 90*time.Second, "", "git", "worktree", "add", "-b", branch, directory, "HEAD")
	if err != nil {
		return "", "", err
	}
	if created.ExitCode != 0 {
		return "", "", fmt.Errorf("could not create isolated local worktree: %s", created.Output)
	}
	if err := copyPinnedContractFixture(workspace.Root, directory); err != nil {
		return "", "", err
	}
	return directory, branch, nil
}

// copyPinnedContractFixture makes the repository's explicitly versioned
// product contract available in an isolated worktree. The contract is kept
// outside the Git worktree in this workspace, but backend unit tests resolve
// it through a relative path. Copying only this known, read-only fixture keeps
// tests representative without exposing arbitrary parent files or secrets.
func copyPinnedContractFixture(workspaceRoot, worktreeRoot string) error {
	source := filepath.Join(workspaceRoot, ".contracts", "itbem-product-contract")
	info, err := os.Stat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect pinned contract fixture: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("pinned contract fixture is not a directory")
	}
	destination := filepath.Join(worktreeRoot, ".contracts", "itbem-product-contract")
	// The contract source may itself be a Git worktree. Its `.git` metadata
	// must never be copied into the task worktree: only the pinned files are
	// required by tests, and nested metadata makes `git diff --check` unsafe.
	if err := os.Remove(filepath.Join(destination, ".git")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove nested contract metadata: %w", err)
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("invalid pinned contract path")
		}
		target := filepath.Join(destination, relative)
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("pinned contract fixture must not contain symlinks")
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("pinned contract fixture contains an unsupported file")
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

type commandResult struct {
	ExitCode            int
	Output              string
	CapturedOutputBytes int
	SandboxLease        map[string]any
	SandboxAttestation  *SandboxAttestation
	SandboxLifecycle    map[string]any
}

func runLocal(parent context.Context, directory string, timeout time.Duration, input string, command string, arguments ...string) (commandResult, error) {
	return runLocalWithEnv(parent, directory, timeout, input, nil, command, arguments...)
}

// runLocalWithEnv runs repository-owned commands with a deliberately reduced
// environment. A validation script, git hook, or compromised dependency must
// not inherit the worker's provider keys, GitHub App material, or deployment
// credentials just because it runs in the same process tree. The optional
// overrides are only for a short-lived, explicit operation such as GitHub
// App askpass; callers must never put credentials in command arguments.
func runLocalWithEnv(parent context.Context, directory string, timeout time.Duration, input string, environment map[string]string, command string, arguments ...string) (commandResult, error) {
	var reader io.Reader
	if input != "" {
		reader = strings.NewReader(input)
	}
	return runLocalWithReader(parent, directory, timeout, reader, environment, command, arguments...)
}

// runLocalWithBytes pipes binary patch bytes directly to stdin without first
// converting them into an immutable Go string that cannot be wiped afterward.
func runLocalWithBytes(parent context.Context, directory string, timeout time.Duration, input []byte, command string, arguments ...string) (commandResult, error) {
	return runLocalWithReader(parent, directory, timeout, bytes.NewReader(input), nil, command, arguments...)
}

func runLocalWithReader(parent context.Context, directory string, timeout time.Duration, input io.Reader, environment map[string]string, command string, arguments ...string) (commandResult, error) {
	if stepActivityFromContext(parent) == nil || stepActivityIsSuppressed(parent) {
		return runLocalWithReaderUninstrumented(parent, directory, timeout, input, environment, command, arguments...)
	}
	action, toolName := activityActionFromContext(parent, "command", "repository_command")
	var result commandResult
	observed := false
	operationErr, reportErr := runStepActivityWithDetails(parent, action, toolName, func(phase string) *models.DeliveryPlanStepActivityDetails {
		if !observed {
			return nil
		}
		return commandActivityDetails(action, phase, command, arguments, result, "")
	}, func() error {
		var runErr error
		result, runErr = runLocalWithReaderUninstrumented(withStepActivitySuppressed(parent), directory, timeout, input, environment, command, arguments...)
		observed = runErr == nil
		if runErr == nil && result.ExitCode != 0 {
			return errActivityReportedNonzeroExit
		}
		return runErr
	})
	if reportErr != nil {
		return result, reportErr
	}
	if errors.Is(operationErr, errActivityReportedNonzeroExit) {
		return result, nil
	}
	return result, operationErr
}

func commandActivityDetails(action, phase, command string, arguments []string, result commandResult, resourceReference string) *models.DeliveryPlanStepActivityDetails {
	executable := strings.ToLower(filepath.Base(strings.ReplaceAll(strings.TrimSpace(command), "\\", "/")))
	for _, extension := range []string{".exe", ".cmd", ".bat"} {
		executable = strings.TrimSuffix(executable, extension)
	}
	argumentCount, exitCode := len(arguments), result.ExitCode
	outputBytes := result.CapturedOutputBytes
	if outputBytes <= 0 {
		outputBytes = len([]byte(result.Output))
	}
	details := &models.DeliveryPlanStepActivityDetails{
		ExecutableName: executable, ArgumentCount: &argumentCount, ExitCode: &exitCode, CapturedOutputBytes: &outputBytes,
	}
	if resourceReference != "" {
		details.ResourceReferences = []string{resourceReference}
	}
	if models.ValidateDeliveryPlanStepActivityDetails(action, phase, details) != nil {
		return nil
	}
	return details
}

func runLocalWithEnvUninstrumented(parent context.Context, directory string, timeout time.Duration, input string, environment map[string]string, command string, arguments ...string) (commandResult, error) {
	var reader io.Reader
	if input != "" {
		reader = strings.NewReader(input)
	}
	return runLocalWithReaderUninstrumented(parent, directory, timeout, reader, environment, command, arguments...)
}

func runLocalWithReaderUninstrumented(parent context.Context, directory string, timeout time.Duration, input io.Reader, environment map[string]string, command string, arguments ...string) (commandResult, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	process := exec.CommandContext(ctx, command, arguments...)
	// CommandContext stops the direct process when the deadline expires, but
	// repository-owned tools frequently spawn package managers, test runners,
	// browsers, or child shells. Keep the whole command tree in a dedicated
	// process group so a timeout cannot leave orphaned work consuming the
	// worker indefinitely. This is process-tree cleanup, not a substitute for
	// a container/VM sandbox; the readiness contract still reports that limit.
	stopProcessGroup := configureCommandProcessGroup(process)
	// CommandContext otherwise waits for inherited stdout/stderr pipes to close.
	// A child such as `sh -c 'sleep 30'` can keep those pipes open after the
	// shell itself is cancelled, making a nominal timeout block for the full
	// child lifetime. Cancel the process group immediately and bound the final
	// pipe-drain wait as a second line of defense.
	process.Cancel = func() error {
		stopProcessGroup()
		return nil
	}
	process.WaitDelay = 750 * time.Millisecond
	process.Dir = directory
	process.Env = repositoryCommandEnvironment(os.Environ(), environment)
	if input != nil {
		process.Stdin = input
	}
	var stdout, stderr boundedCommandBuffer
	process.Stdout, process.Stderr = &stdout, &stderr
	err := process.Run()
	output := strings.TrimSpace(stdout.String() + stderr.String())
	if len(output) > maxCommandOutput {
		output = output[:maxCommandOutput]
	}
	outputBytes := len([]byte(output))
	if ctx.Err() != nil {
		stopProcessGroup()
		return commandResult{}, fmt.Errorf("local command timed out")
	}
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return commandResult{ExitCode: exitError.ExitCode(), Output: output, CapturedOutputBytes: outputBytes}, nil
		}
		return commandResult{}, fmt.Errorf("local command failed to start")
	}
	return commandResult{ExitCode: 0, Output: output, CapturedOutputBytes: outputBytes}, nil
}

// Command logging is bounded while streaming, not after accumulating unlimited
// output in memory. Cryptographic diffs use their separate full-byte stream.
type boundedCommandBuffer struct{ bytes.Buffer }

func (b *boundedCommandBuffer) ReadFrom(reader io.Reader) (int64, error) {
	// Do not inherit bytes.Buffer.ReadFrom: io.Copy may otherwise bypass Write.
	return io.Copy(struct{ io.Writer }{b}, reader)
}

func (b *boundedCommandBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxCommandOutput - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

// repositoryCommandEnvironment removes credentials before any command touches
// a customer repository. This is defense in depth rather than a substitute
// for OS/container isolation: local workspaces remain trusted only to the
// degree the operator trusts their checked-out code. Keeping this transform
// deterministic also lets callers add one narrowly-scoped ephemeral value
// without accidentally retaining an inherited variable with a different case
// on Windows.
func repositoryCommandEnvironment(inherited []string, overrides map[string]string) []string {
	values := make(map[string]string, len(inherited)+len(overrides))
	for _, entry := range inherited {
		key, value, found := strings.Cut(entry, "=")
		if !found || key == "" || repositoryCommandSecretEnvironmentKey(key) {
			continue
		}
		values[strings.ToUpper(key)] = key + "=" + value
	}
	for key, value := range overrides {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		values[strings.ToUpper(key)] = key + "=" + value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}

func repositoryCommandSecretEnvironmentKey(key string) bool {
	key = strings.ToUpper(strings.TrimSpace(key))
	if key == "" {
		return true
	}
	if strings.HasPrefix(key, "ITBEM_") {
		return true
	}
	for _, suffix := range []string{"_TOKEN", "_SECRET", "_PASSWORD", "_PRIVATE_KEY", "_API_KEY", "_ACCESS_KEY"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	switch key {
	case "AWS_ACCESS_KEY_ID", "AWS_SESSION_TOKEN", "AZURE_CLIENT_ID", "AZURE_TENANT_ID", "AZURE_CLIENT_SECRET", "GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "MINIMAX_API_KEY", "DATABASE_URL", "REDIS_URL":
		return true
	default:
		return false
	}
}
