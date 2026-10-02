package automationagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"events-stocks/internal/inferencecapability"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxQAArtifacts     = 12
	maxQAArtifactBytes = 25 << 20
)

type screenshotViewport struct {
	Name   string
	Width  int
	Height int
}

var qaScreenshotViewports = []screenshotViewport{
	{Name: "desktop", Width: 1440, Height: 1200},
	{Name: "mobile", Width: 412, Height: 915},
}

var browserQACaseIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var browserQAEnvironmentReference = regexp.MustCompile(`^ITBEM_QA_[A-Z0-9_]{1,60}$`)
var toolCallKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
var qaURLInTextPattern = regexp.MustCompile(`https?://[^\s\"'<>]+`)
var qaSensitiveReportKeyPattern = regexp.MustCompile(`(?i)^(?:` + sensitiveWorkspaceKey + `|(?:[A-Za-z0-9]+[_-])*(?:cookie|set[_-]?cookie|session|credential|credentials|auth)(?:[_-][A-Za-z0-9]+)*)$`)

// RedactSourceExcerpt handles keyed secrets and several established token
// formats. These extra high-confidence patterns cover provider keys/JWTs that
// browser tools sometimes print inline without a field name.
var qaProviderTokenPattern = regexp.MustCompile(`\bsk-(?:proj-|live-|test-|ant-|or-v1-)?[A-Za-z0-9_-]{20,}\b`)
var qaJWTTokenPattern = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)
var qaPrivateReasoningTextPattern = regexp.MustCompile(`(?im)(\b(?:reasoning(?:[\s-]?content)?|hidden[\s_-]*reasoning|private[\s_-]*reasoning|chain[\s_-]*of[\s_-]*thoughts?|cot|thoughts?|thinking|analysis|scratchpad|internal[\s_-]*monologue|deliberation)\b\s*[:=]\s*)[^\r\n]*`)

type LocalArtifact struct {
	Name        string
	Body        []byte
	ContentType string
}

// QAExecutionError preserves the work already observed before a later
// repository/tool failure. The caller must keep the task failed; this is a
// private diagnostic handoff, never a gate-eligible QA result.
type QAExecutionError struct {
	Result    map[string]any
	Artifacts []LocalArtifact
	Cause     error
}

var errQACapabilityNotAccepted = errors.New("QA task lease is no longer accepted for execution")

type qaCapabilityRefreshError struct {
	cause error
}

func (e *qaCapabilityRefreshError) Error() string {
	if e == nil || e.cause == nil {
		return "QA execution authority refresh failed"
	}
	return fmt.Sprintf("QA execution authority refresh failed: %v", e.cause)
}

func (e *qaCapabilityRefreshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *QAExecutionError) Error() string {
	if e == nil || e.Cause == nil {
		return "QA execution failed"
	}
	return e.Cause.Error()
}

func (e *QAExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func RunQA(ctx context.Context, taskID, runID string, delivery json.RawMessage, lookup func(string) string) (map[string]any, []LocalArtifact, error) {
	return runQAWithCapabilityRefresh(ctx, taskID, runID, delivery, lookup, nil)
}

func runQAWithCapabilityRefresh(ctx context.Context, taskID, runID string, delivery json.RawMessage, lookup func(string) string, refresh func(context.Context) (bool, error)) (map[string]any, []LocalArtifact, error) {
	ctx = withSandboxTaskID(ctx, taskID)
	previewURL, err := deliveryPreviewURL(delivery)
	if err != nil {
		return nil, nil, err
	}
	targets, err := deliveryQATargetsWithContext(ctx, delivery, lookup)
	if err != nil {
		return nil, nil, err
	}
	matrixDigest, err := qaPublishedMatrixDigest(ctx, delivery, targets)
	if err != nil {
		return nil, nil, err
	}
	result := map[string]any{"preview": checkPreview(ctx, previewURL), "repository_runs": []any{}, "repository_execution_order": []string{}}
	artifacts := make([]LocalArtifact, 0)
	// The preview is a single deployed surface, while commands and artifacts
	// remain repository-specific. Prefer the repository explicitly approved for
	// semantic browser QA; otherwise retain one evidence-producing target for
	// the responsive preview capture.
	var captureTarget *qaTarget
	for index := range targets {
		target := &targets[index]
		if target.execution.RunStagehand && len(target.workspace.Config.QASemanticCommand) > 0 {
			captureTarget = target
			break
		}
	}
	if captureTarget == nil {
		for index := range targets {
			target := &targets[index]
			if target.execution.CollectEvidence {
				captureTarget = target
				break
			}
		}
	}
	// Screenshots are required review evidence, not best-effort leftovers
	// after arbitrary reports. Reserve their bounded slots before collecting
	// optional repository artifacts so a noisy test suite cannot hide visual QA.
	regularArtifactLimit := maxQAArtifacts
	if captureTarget != nil {
		regularArtifactLimit -= qaScreenshotArtifactSlots(captureTarget.workspace.Config.QAScreenshotCommand)
		if captureTarget.execution.RunStagehand && len(captureTarget.workspace.Config.QASemanticCommand) > 0 {
			regularArtifactLimit -= qaSemanticArtifactSlots(captureTarget.workspace.Config.QASemanticCommand)
		}
	}
	for _, target := range targets {
		commands := make([]any, 0, len(target.workspace.Config.ValidationCommands)+len(target.workspace.Config.QACommands))
		if target.reviewBinding != nil || target.reviewCommitSHA != "" {
			if err := verifyQATargetRevision(ctx, target); err != nil {
				return qaRevisionFailure(result, artifacts, target, commands, err)
			}
		}
		result["repository_execution_order"] = append(result["repository_execution_order"].([]string), target.reference)
		if target.execution.RunValidation {
			for index, command := range target.workspace.Config.ValidationCommands {
				if err := refreshQAExecutionAuthority(ctx, refresh); err != nil {
					return qaRevisionFailure(result, artifacts, target, commands, err)
				}
				if target.reviewBinding != nil || target.reviewCommitSHA != "" {
					if err := verifyQATargetRevision(ctx, target); err != nil {
						return qaRevisionFailure(result, artifacts, target, commands, err)
					}
				}
				completed, runErr := runQACommandWithAuthority(ctx, refresh, func(commandCtx context.Context) (commandResult, error) {
					return runWorkspaceCommand(commandCtx, target.workspace, target.root, commandTimeout, "", nil, command[0], command[1:]...)
				})
				if runErr != nil {
					result["repository_runs"] = append(result["repository_runs"].([]any), qaRepositoryRun(target, commands, runErr))
					return result, artifacts, &QAExecutionError{Result: result, Artifacts: artifacts, Cause: runErr}
				}
				commands = append(commands, map[string]any{"phase": "validation", "kind": configuredCommandKind(target.workspace.Config.ValidationCommandKinds, index), "command": command, "passed": completed.ExitCode == 0, "output": completed.Output, "sandbox_lease": completed.SandboxLease})
			}
		}
		if target.execution.RunQA {
			for index, command := range target.workspace.Config.QACommands {
				if err := refreshQAExecutionAuthority(ctx, refresh); err != nil {
					return qaRevisionFailure(result, artifacts, target, commands, err)
				}
				if target.reviewBinding != nil || target.reviewCommitSHA != "" {
					if err := verifyQATargetRevision(ctx, target); err != nil {
						return qaRevisionFailure(result, artifacts, target, commands, err)
					}
				}
				completed, runErr := runQACommandWithAuthority(ctx, refresh, func(commandCtx context.Context) (commandResult, error) {
					return runWorkspaceCommand(commandCtx, target.workspace, target.root, commandTimeout, "", nil, command[0], command[1:]...)
				})
				if runErr != nil {
					result["repository_runs"] = append(result["repository_runs"].([]any), qaRepositoryRun(target, commands, runErr))
					return result, artifacts, &QAExecutionError{Result: result, Artifacts: artifacts, Cause: runErr}
				}
				commands = append(commands, map[string]any{"phase": "qa", "kind": configuredCommandKind(target.workspace.Config.QACommandKinds, index), "command": command, "passed": completed.ExitCode == 0, "output": completed.Output, "sandbox_lease": completed.SandboxLease})
			}
		}
		if target.execution.CollectEvidence {
			repositoryArtifacts, collectErr := collectQAArtifacts(target.root, target.workspace.Config.QAArtifactPatterns)
			if collectErr != nil {
				result["repository_runs"] = append(result["repository_runs"].([]any), qaRepositoryRun(target, commands, collectErr))
				return result, artifacts, &QAExecutionError{Result: result, Artifacts: artifacts, Cause: collectErr}
			}
			if remaining := regularArtifactLimit - len(artifacts); remaining > 0 {
				if len(repositoryArtifacts) > remaining {
					repositoryArtifacts = repositoryArtifacts[:remaining]
				}
				artifacts = append(artifacts, prefixQAArtifacts(repositoryArtifacts, target.workspace.ID)...)
			}
		}
		if target.reviewBinding != nil || target.reviewCommitSHA != "" {
			if err := verifyQATargetRevision(ctx, target); err != nil {
				return qaRevisionFailure(result, artifacts, target, commands, err)
			}
		}
		result["repository_runs"] = append(result["repository_runs"].([]any), map[string]any{
			"workspace": "workspace://" + target.workspace.ID, "branch": target.branch,
			"tested_directory": target.testedDirectory, "commands": commands,
			"execution_contract": target.execution.asMap(),
			"review_binding":     qaReviewBindingEvidence(target),
		})
	}
	if preview, ok := result["preview"].(map[string]any); ok && preview["passed"] == true && captureTarget != nil {
		if captureTarget.execution.RunStagehand && len(captureTarget.workspace.Config.QASemanticCommand) > 0 {
			var semantic map[string]any
			var semanticArtifacts []LocalArtifact
			semanticErr := runQAStageWithAuthority(ctx, refresh, func(stageCtx context.Context) error {
				var captureErr error
				semantic, semanticArtifacts, captureErr = captureSemanticQAWithRefresh(stageCtx, taskID, runID, previewURL, delivery, captureTarget.workspace, captureTarget.root, captureTarget.workspace.Config.QASemanticCommand, lookup, refresh)
				return captureErr
			})
			result["semantic"] = semantic
			if semanticErr != nil {
				return result, artifacts, &QAExecutionError{Result: result, Artifacts: artifacts, Cause: semanticErr}
			}
			artifacts = append(artifacts, prefixQAArtifacts(semanticArtifacts, captureTarget.workspace.ID)...)
		}
		if len(captureTarget.workspace.Config.QAScreenshotCommand) > 0 {
			var screenshot map[string]any
			var artifact *LocalArtifact
			captureErr := runQAStageWithAuthority(ctx, refresh, func(stageCtx context.Context) error {
				if err := refreshQAExecutionAuthority(stageCtx, refresh); err != nil {
					return err
				}
				var captureErr error
				screenshot, artifact, captureErr = captureScreenshot(stageCtx, taskID, previewURL, captureTarget.workspace, captureTarget.root, captureTarget.workspace.Config.QAScreenshotCommand)
				return captureErr
			})
			result["screenshot"] = screenshot
			if captureErr != nil {
				return result, artifacts, &QAExecutionError{Result: result, Artifacts: artifacts, Cause: captureErr}
			}
			if artifact != nil {
				artifacts = append(artifacts, prefixQAArtifacts([]LocalArtifact{*artifact}, captureTarget.workspace.ID)...)
			}
		} else if !semanticArtifactsContainPNG(artifacts) {
			captures := make([]any, 0, len(qaScreenshotViewports))
			for index, viewport := range qaScreenshotViewports {
				var screenshot map[string]any
				var artifact *LocalArtifact
				captureErr := runQAStageWithAuthority(ctx, refresh, func(stageCtx context.Context) error {
					if err := refreshQAExecutionAuthority(stageCtx, refresh); err != nil {
						return err
					}
					var captureErr error
					screenshot, artifact, captureErr = captureScreenshotAt(stageCtx, taskID, previewURL, captureTarget.workspace, captureTarget.root, nil, viewport)
					return captureErr
				})
				if captureErr != nil {
					return result, artifacts, &QAExecutionError{Result: result, Artifacts: artifacts, Cause: captureErr}
				}
				screenshot["viewport"] = map[string]int{"width": viewport.Width, "height": viewport.Height}
				captures = append(captures, screenshot)
				if index == 0 {
					// Keep the original field for existing clients, while the full
					// array makes responsive evidence first-class for new views.
					result["screenshot"] = screenshot
				}
				if artifact != nil {
					artifacts = append(artifacts, prefixQAArtifacts([]LocalArtifact{*artifact}, captureTarget.workspace.ID)...)
				}
			}
			result["screenshots"] = captures
		}
	}
	for _, target := range targets {
		if target.reviewBinding != nil || target.reviewCommitSHA != "" {
			if err := verifyQATargetRevision(ctx, target); err != nil {
				return result, artifacts, &QAExecutionError{Result: result, Artifacts: artifacts, Cause: err}
			}
		}
	}
	if _, err := qaPublishedMatrixDigest(ctx, delivery, targets); err != nil {
		return result, artifacts, &QAExecutionError{Result: result, Artifacts: artifacts, Cause: err}
	}
	observation, err := qaLedgerObservation(taskID, matrixDigest, result)
	if err != nil {
		return result, artifacts, &QAExecutionError{Result: result, Artifacts: artifacts, Cause: err}
	}
	if observation != nil {
		result["ledger_observation"] = observation
	}
	return result, artifacts, nil
}

func qaRepositoryRun(target qaTarget, commands []any, cause error) map[string]any {
	message := "QA command failed before a complete repository run was recorded"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		message = cause.Error()
	}
	return map[string]any{
		"workspace":          "workspace://" + target.workspace.ID,
		"branch":             target.branch,
		"tested_directory":   target.testedDirectory,
		"commands":           commands,
		"execution_contract": target.execution.asMap(),
		"review_binding":     qaReviewBindingEvidence(target),
		"error":              message,
	}
}

func qaReviewBindingEvidence(target qaTarget) map[string]string {
	if target.reviewCommitSHA != "" {
		return map[string]string{"commit_sha": target.reviewCommitSHA, "review_source_sha256": target.reviewSourceSHA256, "branch": target.branch}
	}
	if target.reviewBinding == nil {
		return nil
	}
	return map[string]string{"base_sha": target.reviewBinding.BaseSHA, "review_diff_sha256": target.reviewBinding.ReviewDiffSHA256, "review_source_sha256": target.reviewSourceSHA256, "branch": target.reviewBinding.Branch}
}

func refreshQAExecutionAuthority(ctx context.Context, refresh func(context.Context) (bool, error)) error {
	if refresh == nil {
		return nil
	}
	accepted, err := refresh(ctx)
	if err != nil {
		return &qaCapabilityRefreshError{cause: err}
	}
	if !accepted {
		return &qaCapabilityRefreshError{cause: errQACapabilityNotAccepted}
	}
	return nil
}

func verifyQATargetRevision(ctx context.Context, target qaTarget) error {
	if target.reviewCommitSHA != "" {
		for _, check := range []struct {
			args     []string
			expected string
		}{
			{[]string{"branch", "--show-current"}, target.branch},
			{[]string{"rev-parse", "HEAD"}, target.reviewCommitSHA},
			{[]string{"diff", "--no-ext-diff", "--no-textconv", "--quiet", "HEAD"}, ""},
			{[]string{"ls-files", "--others"}, ""},
		} {
			result, err := runLocal(ctx, target.root, commandTimeout, "", "git", check.args...)
			if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Output) != check.expected {
				return fmt.Errorf("published QA worktree does not match its exact clean commit")
			}
		}
	} else if err := verifyReviewedWorktree(ctx, target.root, target.reviewBinding); err != nil {
		return err
	}
	// Historical sealed attempts have no source manifest. Never retrofit their
	// metadata; newly emitted implementation handoffs include this binding.
	if target.reviewSourceSHA256 == "" {
		return nil
	}
	if !sha256DigestPattern.MatchString(target.reviewSourceSHA256) {
		return fmt.Errorf("QA reviewed source manifest digest is invalid")
	}
	digest, err := sandboxWorktreeDigest(target.root)
	if err != nil || fmt.Sprintf("%x", digest) != target.reviewSourceSHA256 {
		return fmt.Errorf("QA source manifest changed after review")
	}
	return nil
}

func qaRevisionFailure(result map[string]any, artifacts []LocalArtifact, target qaTarget, commands []any, cause error) (map[string]any, []LocalArtifact, error) {
	result["repository_runs"] = append(result["repository_runs"].([]any), qaRepositoryRun(target, commands, cause))
	return result, artifacts, &QAExecutionError{Result: result, Artifacts: artifacts, Cause: cause}
}

func semanticArtifactsContainPNG(artifacts []LocalArtifact) bool {
	for _, artifact := range artifacts {
		if artifact.ContentType == "image/png" {
			return true
		}
	}
	return false
}

func qaScreenshotArtifactSlots(command []string) int {
	if len(command) > 0 {
		return 1
	}
	return len(qaScreenshotViewports)
}

func qaSemanticArtifactSlots(command []string) int {
	if len(command) == 0 {
		return 0
	}
	// A semantic run produces its structured report, a desktop landing
	// screenshot, an explicit mobile responsive smoke screenshot, and up to
	// three before/after case pairs from the approved browser E2E plan. Keep those
	// review artifacts ahead of optional suite output so a noisy test run
	// cannot hide browser evidence.
	return 9
}

type qaTarget struct {
	workspace          Workspace
	reference          string
	root               string
	branch             string
	testedDirectory    string
	execution          qaExecutionPolicy
	reviewBinding      *publicationAuthorization
	reviewSourceSHA256 string
	reviewCommitSHA    string
}

// qaExecutionPolicy is persisted inside the human-approved plan. The worker
// does not accept commands from it; it only switches the operator-configured
// harness capabilities on or off for the exact reviewed repository.
type qaExecutionPolicy struct {
	RunValidation   bool
	RunQA           bool
	RunStagehand    bool
	CollectEvidence bool
}

func (policy qaExecutionPolicy) asMap() map[string]bool {
	return map[string]bool{
		"run_validation":   policy.RunValidation,
		"run_qa":           policy.RunQA,
		"run_stagehand":    policy.RunStagehand,
		"collect_evidence": policy.CollectEvidence,
	}
}

func defaultQAExecutionPolicy() qaExecutionPolicy {
	// Older approved plans predate the execution matrix. Keep their historical
	// QA behavior: run the QA harness, collect evidence and use Stagehand when
	// it was configured, without unexpectedly repeating implementation checks.
	return qaExecutionPolicy{RunQA: true, RunStagehand: true, CollectEvidence: true}
}

func approvedQAExecutionPolicies(delivery json.RawMessage) (map[string]qaExecutionPolicy, bool, error) {
	var input struct {
		ApprovedPlan map[string]json.RawMessage `json:"approved_plan"`
	}
	if err := json.Unmarshal(delivery, &input); err != nil {
		return nil, false, fmt.Errorf("delivery input must be a JSON object")
	}
	raw, present := input.ApprovedPlan["qa_execution_matrix"]
	if !present {
		return nil, false, nil
	}
	var entries []struct {
		RepositoryRef   string `json:"repository_ref"`
		RunValidation   *bool  `json:"run_validation"`
		RunQA           *bool  `json:"run_qa"`
		RunStagehand    *bool  `json:"run_stagehand"`
		CollectEvidence *bool  `json:"collect_evidence"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 || len(entries) > 16 {
		return nil, false, fmt.Errorf("approved QA execution matrix is invalid")
	}
	policies := make(map[string]qaExecutionPolicy, len(entries))
	for _, entry := range entries {
		reference := strings.TrimSpace(entry.RepositoryRef)
		if reference == "" || entry.RunValidation == nil || entry.RunQA == nil || entry.RunStagehand == nil || entry.CollectEvidence == nil {
			return nil, false, fmt.Errorf("approved QA execution matrix is invalid")
		}
		if *entry.RunStagehand && !*entry.CollectEvidence {
			return nil, false, fmt.Errorf("approved QA execution matrix requires evidence when Stagehand is enabled")
		}
		if _, duplicate := policies[reference]; duplicate {
			return nil, false, fmt.Errorf("approved QA execution matrix repeats a repository")
		}
		policies[reference] = qaExecutionPolicy{
			RunValidation: *entry.RunValidation, RunQA: *entry.RunQA,
			RunStagehand: *entry.RunStagehand, CollectEvidence: *entry.CollectEvidence,
		}
	}
	return policies, true, nil
}

// deliveryQATargets binds local validation to the exact reviewed branches
// attached to the delivery item. A QA task receives a new task ID, so using
// that ID as a worktree directory would silently test the base checkout.
func deliveryQATargets(delivery json.RawMessage, lookup func(string) string) ([]qaTarget, error) {
	return deliveryQATargetsWithContext(context.Background(), delivery, lookup)
}

func deliveryQATargetsWithContext(ctx context.Context, delivery json.RawMessage, lookup func(string) string) ([]qaTarget, error) {
	var input struct {
		Gatekeeper *struct {
			Revisions []struct {
				Repository string `json:"repository"`
				Branch     string `json:"branch"`
				SHA        string `json:"sha"`
			} `json:"revisions"`
		} `json:"gatekeeper"`
		ChangeSets []struct {
			RepositoryRef string `json:"repository_ref"`
			Branch        string `json:"branch"`
			ReviewType    string `json:"review_type"`
			CIStatus      string `json:"ci_status"`
			CommitSHA     string `json:"commit_sha"`
			Metadata      struct {
				RemoteRepository   string `json:"remote_repository"`
				TargetBranch       string `json:"target_branch"`
				BaseSHA            string `json:"base_sha"`
				ReviewDiffSHA256   string `json:"review_diff_sha256"`
				ReviewSourceSHA256 string `json:"review_source_sha256"`
			} `json:"metadata"`
		} `json:"change_sets"`
		RepositoryTopology []repositoryTopologyEntry `json:"repository_topology"`
	}
	if err := json.Unmarshal(delivery, &input); err != nil {
		return nil, fmt.Errorf("delivery input must be a JSON object")
	}
	policies, hasMatrix, err := approvedQAExecutionPolicies(delivery)
	if err != nil {
		return nil, err
	}
	if input.ChangeSets == nil {
		if input.Gatekeeper != nil {
			return nil, fmt.Errorf("published QA matrix requires change sets")
		}
		// Compatibility for legacy work items that predate immutable change-set
		// delivery context. New control-plane runs always include a matrix.
		workspace, err := deliveryRepositoryWorkspace(delivery, lookup)
		if err != nil {
			return nil, err
		}
		return []qaTarget{{workspace: workspace, reference: "workspace://" + workspace.ID, root: workspace.Root, testedDirectory: "registered base workspace (legacy task)", execution: defaultQAExecutionPolicy()}}, nil
	}
	targets := make([]qaTarget, 0, len(input.ChangeSets))
	seen := map[string]struct{}{}
	for _, change := range input.ChangeSets {
		published := strings.EqualFold(strings.TrimSpace(change.ReviewType), "pull_request")
		if input.Gatekeeper != nil {
			// Historical local or older published handoffs must not shadow the
			// exact immutable revision selected by the server for this task.
			matches := false
			for _, revision := range input.Gatekeeper.Revisions {
				if published && strings.EqualFold(change.Metadata.RemoteRepository, revision.Repository) && change.Metadata.TargetBranch == revision.Branch && strings.EqualFold(change.CommitSHA, revision.SHA) {
					matches = true
				}
			}
			if !matches {
				continue
			}
		}
		if (!published && !strings.EqualFold(strings.TrimSpace(change.ReviewType), "local_worktree")) || !strings.EqualFold(strings.TrimSpace(change.CIStatus), "passed") {
			continue
		}
		reference, branch := strings.TrimSpace(change.RepositoryRef), strings.TrimSpace(change.Branch)
		if _, duplicate := seen[reference]; duplicate {
			continue
		}
		if !strings.HasPrefix(branch, "itbem-agent/") || !taskIDPattern.MatchString(strings.TrimPrefix(branch, "itbem-agent/")) {
			return nil, fmt.Errorf("QA reviewed worktree branch is invalid")
		}
		workspace, err := RegisteredWorkspace(reference, lookup)
		if err != nil {
			return nil, err
		}
		policy := defaultQAExecutionPolicy()
		if hasMatrix {
			var found bool
			policy, found = policies[reference]
			if !found {
				return nil, fmt.Errorf("approved QA execution matrix omits reviewed repository %s", reference)
			}
		}
		if hasMatrix && policy.RunStagehand && len(workspace.Config.QASemanticCommand) == 0 {
			return nil, fmt.Errorf("approved QA execution matrix requests Stagehand for %s but its workspace has no configured semantic QA runner", reference)
		}
		root := filepath.Join(workspace.Root, ".itbem-agent-worktrees", strings.TrimPrefix(branch, "itbem-agent/"))
		if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
			return nil, fmt.Errorf("QA requires the exact reviewed local worktree for %s", reference)
		}
		if published {
			commit := strings.ToLower(strings.TrimSpace(change.CommitSHA))
			if !gitCommitPattern.MatchString(commit) {
				return nil, fmt.Errorf("published QA requires an exact commit SHA")
			}
			target := qaTarget{workspace: workspace, reference: reference, root: root, branch: branch, testedDirectory: "exact published commit worktree", execution: policy, reviewCommitSHA: commit}
			if err := verifyQATargetRevision(ctx, target); err != nil {
				return nil, err
			}
			digest, err := sandboxWorktreeDigest(root)
			if err != nil {
				return nil, err
			}
			target.reviewSourceSHA256 = fmt.Sprintf("%x", digest)
			if err := verifyQATargetRevision(ctx, target); err != nil {
				return nil, err
			}
			seen[reference] = struct{}{}
			targets = append(targets, target)
			continue
		}
		binding := &publicationAuthorization{Branch: branch, BaseSHA: strings.ToLower(strings.TrimSpace(change.Metadata.BaseSHA)), ReviewDiffSHA256: strings.ToLower(strings.TrimSpace(change.Metadata.ReviewDiffSHA256))}
		if !gitCommitPattern.MatchString(binding.BaseSHA) || !sha256DigestPattern.MatchString(binding.ReviewDiffSHA256) {
			return nil, fmt.Errorf("QA requires an immutable reviewed base and diff digest")
		}
		target := qaTarget{workspace: workspace, reference: reference, root: root, branch: branch, testedDirectory: "reviewed isolated worktree", execution: policy, reviewBinding: binding, reviewSourceSHA256: strings.ToLower(strings.TrimSpace(change.Metadata.ReviewSourceSHA256))}
		if err := verifyQATargetRevision(ctx, target); err != nil {
			return nil, fmt.Errorf("QA reviewed revision binding failed: %w", err)
		}
		seen[reference] = struct{}{}
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("QA requires at least one passed reviewed local worktree")
	}
	references := make([]string, 0, len(targets))
	targetByReference := make(map[string]qaTarget, len(targets))
	for _, target := range targets {
		references = append(references, target.reference)
		targetByReference[target.reference] = target
	}
	orderedReferences, err := topologicalRepositoryOrder(references, input.RepositoryTopology)
	if err != nil {
		return nil, err
	}
	orderedTargets := make([]qaTarget, 0, len(orderedReferences))
	for _, reference := range orderedReferences {
		orderedTargets = append(orderedTargets, targetByReference[reference])
	}
	return orderedTargets, nil
}

func prefixQAArtifacts(artifacts []LocalArtifact, workspaceID string) []LocalArtifact {
	prefix := strings.TrimSpace(workspaceID)
	result := make([]LocalArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		artifact.Name = prefix + "-" + artifact.Name
		result = append(result, artifact)
	}
	return result
}

func deliveryPreviewURL(delivery json.RawMessage) (string, error) {
	var value struct {
		WorkItem struct {
			PreviewURL string `json:"preview_url"`
		} `json:"work_item"`
	}
	if json.Unmarshal(delivery, &value) != nil {
		return "", fmt.Errorf("delivery input must be a JSON object")
	}
	parsed, err := url.Parse(strings.TrimSpace(value.WorkItem.PreviewURL))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil {
		return "", fmt.Errorf("QA requires the human-recorded HTTP(S) preview URL")
	}
	return parsed.String(), nil
}

func checkPreview(ctx context.Context, previewURL string) map[string]any {
	safeURL := sanitizedPreviewURL(previewURL)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, previewURL, nil)
	if err != nil {
		return map[string]any{"url": safeURL, "passed": false, "error": "preview request could not be created"}
	}
	request.Header.Set("User-Agent", "ITBEM-Delivery-QA/1.0")
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return map[string]any{"url": safeURL, "passed": false, "error": "preview request failed"}
	}
	defer response.Body.Close()
	_, _ = io.CopyN(io.Discard, response.Body, 2048)
	return map[string]any{"url": safeURL, "passed": response.StatusCode >= 200 && response.StatusCode < 400, "status": response.StatusCode, "content_type": response.Header.Get("Content-Type")}
}

// sanitizedPreviewURL is for logs, reports, command results, and UI payloads.
// The raw URL remains in memory only for the actual HTTP/browser navigation.
func sanitizedPreviewURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "[redacted preview URL]"
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

func previewURLHasQueryOrFragment(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return true
	}
	// strings.Contains catches a syntactically present but empty fragment (#),
	// which is not distinguishable from an absent fragment in url.URL.
	return parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(raw, "#")
}

// sanitizePreviewText removes URL query/fragment material from textual output
// and also removes the exact preview credential components if a tool echoes
// them without the URL surrounding them.
func sanitizePreviewText(value, rawPreviewURL string) string {
	value = qaURLInTextPattern.ReplaceAllStringFunc(value, func(candidate string) string {
		trimmed := strings.TrimRight(candidate, ".,;:!?)]}")
		trailing := candidate[len(trimmed):]
		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return candidate
		}
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		parsed.RawFragment = ""
		return parsed.String() + trailing
	})
	if previewURLHasQueryOrFragment(rawPreviewURL) {
		parsed, err := url.Parse(rawPreviewURL)
		if err == nil {
			for _, secret := range []string{parsed.RawQuery, parsed.Fragment, parsed.RawFragment} {
				if secret != "" {
					value = strings.ReplaceAll(value, secret, "[REDACTED]")
				}
			}
			if decoded, decodeErr := url.QueryUnescape(parsed.RawQuery); decodeErr == nil && decoded != "" && decoded != parsed.RawQuery {
				value = strings.ReplaceAll(value, decoded, "[REDACTED]")
			}
		}
	}
	value, _ = RedactSourceExcerpt(value)
	value = qaProviderTokenPattern.ReplaceAllString(value, "[REDACTED]")
	value = qaJWTTokenPattern.ReplaceAllString(value, "[REDACTED]")
	value = qaPrivateReasoningTextPattern.ReplaceAllString(value, "$1[REDACTED PRIVATE REASONING]")
	return value
}

func privateQAReasoningField(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("_", "", "-", "", " ", "", ".", "", "/", "").Replace(normalized)
	switch normalized {
	case "analysis", "reasoning", "reasoningcontent", "hiddenreasoning", "privatereasoning",
		"chainofthought", "chainofthoughts", "cot", "thought", "thoughts", "thinking",
		"thoughtprocess", "internalmonologue", "scratchpad", "deliberation":
		return true
	default:
		return false
	}
}

func sanitizePreviewCommand(command []string, rawPreviewURL string) []string {
	sanitized := make([]string, 0, len(command))
	redactNextArgument := false
	for _, argument := range command {
		if redactNextArgument {
			sanitized = append(sanitized, "[REDACTED]")
			redactNextArgument = false
			continue
		}
		clean := sanitizePreviewText(argument, rawPreviewURL)
		sanitized = append(sanitized, clean)
		if !strings.Contains(argument, "=") && workspaceSensitiveCommandArgument.MatchString(strings.TrimSpace(argument)) {
			redactNextArgument = true
		}
	}
	return sanitized
}

func sanitizePreviewValue(value any, rawPreviewURL string) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, entry := range typed {
			if privateQAReasoningField(key) {
				continue
			}
			if qaSensitiveReportKeyPattern.MatchString(strings.TrimSpace(key)) {
				result[key] = "[REDACTED]"
				continue
			}
			if strings.Contains(strings.ToLower(key), "url") {
				if rawURL, ok := entry.(string); ok {
					result[key] = sanitizePreviewText(sanitizedPreviewURL(rawURL), rawPreviewURL)
					continue
				}
			}
			result[key] = sanitizePreviewValue(entry, rawPreviewURL)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, entry := range typed {
			result[index] = sanitizePreviewValue(entry, rawPreviewURL)
		}
		return result
	case string:
		return sanitizePreviewText(typed, rawPreviewURL)
	default:
		return value
	}
}

func sanitizePreviewReport(body []byte, rawPreviewURL string) ([]byte, map[string]any) {
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return []byte(sanitizePreviewText(string(body), rawPreviewURL)), nil
	}
	sanitized := sanitizePreviewValue(decoded, rawPreviewURL)
	encoded, err := json.Marshal(sanitized)
	if err != nil {
		return []byte(sanitizePreviewText(string(body), rawPreviewURL)), nil
	}
	report, _ := sanitized.(map[string]any)
	return encoded, report
}

// stagehandCommandWithPrivatePreviewURL adapts only the pinned Stagehand
// runner's --url argument to its --url-file contract. The private file is
// created with restrictive permissions and removed after the subprocess
// exits; no token-bearing URL is ever placed in argv or a returned command.
func stagehandCommandWithPrivatePreviewURL(command []string, directory, previewURL string) ([]string, func(), error) {
	if !isPinnedStagehandCommand(command) {
		return nil, func() {}, fmt.Errorf("signed preview URLs require the pinned Stagehand URL-file contract")
	}
	urlIndex := -1
	urlArgumentWidth := 1
	for index, argument := range command {
		if argument == "--url" {
			if index+1 >= len(command) {
				return nil, func() {}, fmt.Errorf("pinned Stagehand URL argument is invalid")
			}
			urlIndex = index
			urlArgumentWidth = 2
			break
		}
		if strings.HasPrefix(argument, "--url=") {
			urlIndex = index
			break
		}
	}
	if urlIndex < 0 {
		return nil, func() {}, fmt.Errorf("pinned Stagehand command must use --url for signed preview navigation")
	}
	privateDirectory, err := os.MkdirTemp(directory, ".preview-url-")
	if err != nil {
		return nil, func() {}, fmt.Errorf("prepare private preview URL file")
	}
	filePath := filepath.Join(privateDirectory, "url")
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = os.Remove(privateDirectory)
		return nil, func() {}, fmt.Errorf("create private preview URL file")
	}
	_, writeErr := io.WriteString(file, previewURL)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(filePath)
		_ = os.Remove(privateDirectory)
		return nil, func() {}, fmt.Errorf("write private preview URL file")
	}
	cleanup := func() {
		_ = os.Remove(filePath)
		_ = os.Remove(privateDirectory)
	}
	result := make([]string, 0, len(command)-urlArgumentWidth+2)
	result = append(result, command[:urlIndex]...)
	result = append(result, "--url-file", filePath)
	result = append(result, command[urlIndex+urlArgumentWidth:]...)
	return result, cleanup, nil
}

func captureScreenshot(ctx context.Context, taskID, previewURL string, workspace Workspace, root string, command []string) (map[string]any, *LocalArtifact, error) {
	return captureScreenshotAt(ctx, taskID, previewURL, workspace, root, command, qaScreenshotViewports[0])
}

// captureSemanticQA runs one configured semantic browser probe and records
// only its bounded JSON report plus its sibling PNG evidence. The worker
// treats its exit status as a QA check; it never lets a language model decide
// whether a human gate opens.
func captureSemanticQA(ctx context.Context, taskID, runID, previewURL string, delivery json.RawMessage, workspace Workspace, root string, command []string, lookup func(string) string) (map[string]any, []LocalArtifact, error) {
	return captureSemanticQAWithRefresh(ctx, taskID, runID, previewURL, delivery, workspace, root, command, lookup, nil)
}

func captureSemanticQAWithRefresh(ctx context.Context, taskID, runID, previewURL string, delivery json.RawMessage, workspace Workspace, root string, command []string, lookup func(string) string, refresh func(context.Context) (bool, error)) (map[string]any, []LocalArtifact, error) {
	safePreviewURL := sanitizedPreviewURL(previewURL)
	directory := filepath.Join(root, ".itbem-agent-evidence", taskID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, nil, fmt.Errorf("prepare semantic QA evidence directory: %w", err)
	}
	path := filepath.Join(directory, "semantic-qa.json")
	planPath := filepath.Join(directory, "browser-qa-plan.json")
	plan, err := browserQAPlan(delivery)
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(planPath, plan, 0600); err != nil {
		return nil, nil, fmt.Errorf("write browser QA plan: %w", err)
	}
	rendered := make([]string, len(command))
	for index, part := range command {
		// Command arguments and any resulting error/report are persisted in the
		// QA result, so only the query/fragment-free URL may be substituted here.
		rendered[index] = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(part, "{preview_url}", safePreviewURL), "{artifact_path}", path), "{qa_plan_path}", planPath)
	}
	rendered, err = resolveSemanticQACommand(rendered, lookup)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve semantic QA command: %s", sanitizePreviewText(err.Error(), previewURL))
	}
	if previewURLHasQueryOrFragment(previewURL) {
		if !isPinnedStagehandCommand(rendered) {
			return nil, nil, fmt.Errorf("signed preview URLs require the pinned Stagehand URL-file contract")
		}
		var cleanup func()
		rendered, cleanup, err = stagehandCommandWithPrivatePreviewURL(rendered, directory, previewURL)
		if err != nil {
			return nil, nil, err
		}
		defer cleanup()
	}
	// Authenticated browser cases name their local test values, never embed
	// them in a plan, task, command argument or evidence artifact. Supply
	// exactly the human-approved references to the pinned Stagehand runner.
	testEnvironment, err := browserQATestEnvironment(delivery, lookup)
	if err != nil {
		return nil, nil, err
	}
	if len(testEnvironment) > 0 && !isPinnedStagehandCommand(rendered) {
		return nil, nil, fmt.Errorf("approved browser QA test flow requires the pinned Stagehand runner")
	}
	// Registered validation and QA commands can run for several minutes. Refresh
	// only after those commands and immediately before preparing the Stagehand
	// environment, so the child receives a newly issued run capability.
	if isPinnedStagehandCommand(rendered) && refresh != nil {
		accepted, refreshErr := refresh(ctx)
		if refreshErr != nil {
			return nil, nil, &qaCapabilityRefreshError{cause: refreshErr}
		}
		if !accepted {
			return nil, nil, &qaCapabilityRefreshError{cause: errQACapabilityNotAccepted}
		}
	}
	environment, err := semanticQAEnvironment(rendered, taskID, runID, lookup)
	if err != nil {
		return nil, nil, err
	}
	for key, value := range testEnvironment {
		environment[key] = value
	}
	completed, err := runWorkspaceCommand(ctx, workspace, root, 3*time.Minute, "", environment, rendered[0], rendered[1:]...)
	if err != nil {
		_ = os.Remove(path)
		return nil, nil, fmt.Errorf("semantic QA command failed: %s", sanitizePreviewText(err.Error(), previewURL))
	}
	result := map[string]any{"command": sanitizePreviewCommand(rendered, previewURL), "passed": completed.ExitCode == 0, "output": sanitizePreviewText(completed.Output, previewURL)}
	artifacts := make([]LocalArtifact, 0, qaSemanticArtifactSlots(command))
	var report map[string]any
	if body, readErr := readLocalArtifact(path); readErr == nil {
		body, report = sanitizePreviewReport(body, previewURL)
		if writeErr := os.WriteFile(path, body, 0600); writeErr != nil {
			_ = os.Remove(path)
			return nil, nil, fmt.Errorf("sanitize semantic QA report before persistence")
		}
		artifacts = append(artifacts, LocalArtifact{Name: filepath.Base(path), Body: body, ContentType: "application/json"})
		if report != nil {
			result["report"] = report
		}
	} else {
		_ = os.Remove(path)
		if completed.ExitCode == 0 {
			result["passed"] = false
			result["output"] = "semantic QA did not produce its required report"
		}
	}
	screenshotPaths, globErr := filepath.Glob(strings.TrimSuffix(path, filepath.Ext(path)) + "*.png")
	if globErr == nil {
		sort.Strings(screenshotPaths)
		for _, screenshotPath := range screenshotPaths {
			if len(artifacts) == qaSemanticArtifactSlots(command) {
				break
			}
			if body, readErr := readLocalArtifact(screenshotPath); readErr == nil {
				artifacts = append(artifacts, LocalArtifact{Name: filepath.Base(screenshotPath), Body: body, ContentType: "image/png"})
			}
		}
	}
	if err := verifyStagehandEvidenceManifest(report, artifacts); err != nil {
		return nil, nil, fmt.Errorf("verify semantic QA evidence: %s", sanitizePreviewText(err.Error(), previewURL))
	}
	return result, artifacts, nil
}

// verifyStagehandEvidenceManifest binds every visual artifact uploaded by the
// worker to the SHA-256 and byte count reported by the pinned Stagehand
// runner.  The runner report is private, but without this check a stale,
// incomplete or swapped PNG could still look like credible QA evidence in a
// human gate.  Non-Stagehand semantic commands keep their existing contract.
func verifyStagehandEvidenceManifest(report map[string]any, artifacts []LocalArtifact) error {
	if strings.TrimSpace(fmt.Sprint(report["tool"])) != "stagehand" {
		return nil
	}
	evidence, ok := report["evidence"].(map[string]any)
	if !ok {
		return fmt.Errorf("stagehand report is missing its evidence manifest")
	}
	rawManifest, ok := evidence["artifacts"].([]any)
	if !ok || len(rawManifest) < 2 || len(rawManifest) > 8 {
		return fmt.Errorf("stagehand evidence manifest is invalid")
	}
	images := make(map[string]LocalArtifact)
	for _, artifact := range artifacts {
		if artifact.ContentType != "image/png" {
			continue
		}
		if _, exists := images[artifact.Name]; exists {
			return fmt.Errorf("stagehand evidence contains duplicate image artifacts")
		}
		images[artifact.Name] = artifact
	}
	if len(images) != len(rawManifest) {
		return fmt.Errorf("stagehand evidence manifest does not match captured image artifacts")
	}
	seen := make(map[string]struct{}, len(rawManifest))
	for _, raw := range rawManifest {
		entry, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("stagehand evidence manifest is invalid")
		}
		name := strings.TrimSpace(fmt.Sprint(entry["name"]))
		contentType := strings.TrimSpace(fmt.Sprint(entry["content_type"]))
		expectedSHA := strings.ToLower(strings.TrimSpace(fmt.Sprint(entry["sha256"])))
		rawBytes, bytesOK := entry["bytes"].(float64)
		if name == "" || filepath.Base(name) != name || strings.ToLower(filepath.Ext(name)) != ".png" || contentType != "image/png" || !bytesOK || rawBytes < 1 || rawBytes != math.Trunc(rawBytes) || len(expectedSHA) != 64 {
			return fmt.Errorf("stagehand evidence manifest is invalid")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("stagehand evidence manifest contains duplicate images")
		}
		seen[name] = struct{}{}
		artifact, found := images[name]
		if !found || int64(len(artifact.Body)) != int64(rawBytes) {
			return fmt.Errorf("stagehand evidence artifact %q does not match its manifest", name)
		}
		actualSHA := fmt.Sprintf("%x", sha256.Sum256(artifact.Body))
		if actualSHA != expectedSHA {
			return fmt.Errorf("stagehand evidence artifact %q failed integrity verification", name)
		}
	}
	return nil
}

// resolveSemanticQACommand permits a local worker to use an explicitly
// configured Node runtime when the system installation is older than
// Stagehand's supported minimum. The executable remains machine-owned config,
// and only the runtime token is substituted; the runner and its arguments are
// still the reviewed workspace configuration.
func resolveSemanticQACommand(command []string, lookup func(string) string) ([]string, error) {
	resolved := append([]string(nil), command...)
	if len(resolved) == 0 || resolved[0] != "node" || lookup == nil {
		return resolved, nil
	}
	runtime := strings.TrimSpace(lookup("ITBEM_STAGEHAND_NODE_EXECUTABLE"))
	if runtime == "" {
		return resolved, nil
	}
	if !approvedSemanticRuntime(runtime) {
		return nil, fmt.Errorf("configured Stagehand Node runtime is invalid")
	}
	resolved[0] = runtime
	return resolved, nil
}

// semanticQAEnvironment passes the already-issued run capability to the
// pinned Stagehand runner. It never receives the callback master or signing key.
func semanticQAEnvironment(command []string, taskID, runID string, lookup func(string) string) (map[string]string, error) {
	if !isPinnedStagehandCommand(command) {
		return nil, nil
	}
	if lookup == nil {
		return nil, fmt.Errorf("stagehand semantic QA requires a gateway configuration")
	}
	config, err := LoadGatewayProviderConfig(lookup)
	if err != nil || strings.TrimSpace(taskID) == "" || strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("stagehand semantic QA requires a bound AI gateway lease")
	}
	capability, ok := inferenceCapabilityForRun(taskID, runID, time.Now().UTC())
	if !ok {
		return nil, fmt.Errorf("stagehand requires a current server-issued inference capability")
	}
	return map[string]string{
		"STAGEHAND_QA_INFERENCE_URL":        config.Endpoint,
		"STAGEHAND_QA_INFERENCE_CAPABILITY": capability,
		"STAGEHAND_QA_TASK_ID":              taskID,
		"STAGEHAND_QA_RUN_ID":               runID,
		"STAGEHAND_QA_OPERATION":            inferencecapability.OperationDeliveryQA,
	}, nil
}

// browserQATestEnvironment resolves only explicitly reviewed test-value
// references used by approved_test_flow cases. It intentionally works from
// the immutable plan rather than inherited process state, so a customer repo
// cannot discover arbitrary ITBEM_* values while its QA command is running.
func browserQATestEnvironment(delivery json.RawMessage, lookup func(string) string) (map[string]string, error) {
	if lookup == nil {
		return nil, fmt.Errorf("browser QA test flow requires a credential lookup")
	}
	var input struct {
		ApprovedPlan map[string]json.RawMessage `json:"approved_plan"`
	}
	if err := json.Unmarshal(delivery, &input); err != nil {
		return nil, fmt.Errorf("delivery input must be a JSON object")
	}
	rawCases, present := input.ApprovedPlan["browser_qa_cases"]
	if !present || len(rawCases) == 0 {
		return map[string]string{}, nil
	}
	var cases []map[string]any
	if err := json.Unmarshal(rawCases, &cases); err != nil {
		return nil, fmt.Errorf("approved browser QA cases are invalid")
	}
	values := map[string]string{}
	for _, testCase := range cases {
		steps, _ := testCase["steps"].([]any)
		for _, step := range steps {
			row, ok := step.(map[string]any)
			if !ok || strings.TrimSpace(fmt.Sprint(row["kind"])) != "fill" {
				continue
			}
			reference, _ := row["value_env"].(string)
			reference = strings.TrimSpace(reference)
			if !browserQAEnvironmentReference.MatchString(reference) {
				return nil, fmt.Errorf("approved browser QA test value reference is invalid")
			}
			if _, alreadyPresent := values[reference]; alreadyPresent {
				continue
			}
			value := lookup(reference)
			if strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("approved browser QA test value is not configured: %s", reference)
			}
			values[reference] = value
		}
	}
	return values, nil
}

func isPinnedStagehandCommand(command []string) bool {
	runtimeBase := ""
	if len(command) > 0 {
		runtimeBase = filepath.Base(strings.ReplaceAll(command[0], `\`, "/"))
	}
	if len(command) < 2 || !approvedSemanticRuntime(command[0]) || (!strings.EqualFold(runtimeBase, "node.exe") && runtimeBase != "node") {
		return false
	}
	for _, part := range command[1:] {
		clean := strings.ToLower(filepath.ToSlash(strings.TrimSpace(part)))
		if strings.HasSuffix(clean, "/itbem-events-backend/tools/stagehand-qa/run.mjs") || clean == "tools/stagehand-qa/run.mjs" {
			return true
		}
	}
	return false
}

// browserQAPlan compiles the human-approved browser cases from the immutable
// delivery plan. The runner receives a local file rather than user-controlled
// command arguments. It has no credentials and is valid even when a plan has
// no browser cases, which keeps semantic smoke compatible with older items.
func browserQAPlan(delivery json.RawMessage) ([]byte, error) {
	plan := map[string]any{"schema_version": 1, "mode": "read_only", "cases": []any{}}
	var input struct {
		ApprovedPlan map[string]json.RawMessage `json:"approved_plan"`
	}
	if err := json.Unmarshal(delivery, &input); err != nil {
		return nil, fmt.Errorf("delivery input must be a JSON object")
	}
	if len(input.ApprovedPlan) == 0 {
		return json.Marshal(plan)
	}
	if raw, ok := input.ApprovedPlan["browser_qa_mode"]; ok {
		var mode string
		if json.Unmarshal(raw, &mode) != nil || (mode != "read_only" && mode != "approved_navigation" && mode != "approved_test_flow") {
			return nil, fmt.Errorf("approved browser QA mode is invalid")
		}
		plan["mode"] = mode
	}
	if raw, ok := input.ApprovedPlan["browser_qa_cases"]; ok {
		var cases []any
		if json.Unmarshal(raw, &cases) != nil || len(cases) > 3 {
			return nil, fmt.Errorf("approved browser QA cases are invalid")
		}
		plan["cases"] = cases
	}
	if err := ValidateApprovedBrowserQAPlan(map[string]any{
		"browser_qa_mode":  plan["mode"],
		"browser_qa_cases": plan["cases"],
	}); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(plan)
	if err != nil || len(encoded) > 24_000 {
		return nil, fmt.Errorf("approved browser QA plan is too large")
	}
	return encoded, nil
}

// ValidateApprovedBrowserQAPlan makes browser E2E a reviewable part of the
// Delivery plan. It accepts only deterministic, same-origin navigation and
// assertions; Stagehand receives this exact bounded plan after the human plan
// gate, never free-form browser instructions from a task or model response.
func ValidateApprovedBrowserQAPlan(plan map[string]any) error {
	if plan == nil {
		return fmt.Errorf("approved browser QA plan is invalid")
	}
	if err := NormalizeBrowserQAProposal(plan); err != nil {
		return err
	}
	mode := "read_only"
	if raw, present := plan["browser_qa_mode"]; present {
		value, ok := raw.(string)
		mode = strings.TrimSpace(value)
		if !ok || (mode != "read_only" && mode != "approved_navigation" && mode != "approved_test_flow") {
			return fmt.Errorf("approved browser QA mode is invalid")
		}
	}
	rawCases, present := plan["browser_qa_cases"]
	if !present || rawCases == nil {
		return nil
	}
	cases, ok := rawCases.([]any)
	if !ok || len(cases) > 3 {
		return fmt.Errorf("approved browser QA cases are invalid")
	}
	seenCases := map[string]struct{}{}
	for _, rawCase := range cases {
		testCase, ok := rawCase.(map[string]any)
		if !ok {
			return fmt.Errorf("approved browser QA case is invalid")
		}
		id, _ := testCase["id"].(string)
		id = strings.TrimSpace(id)
		if !browserQACaseIdentifier.MatchString(id) {
			return fmt.Errorf("approved browser QA case ID is invalid")
		}
		if _, duplicate := seenCases[id]; duplicate {
			return fmt.Errorf("approved browser QA case IDs must be unique")
		}
		seenCases[id] = struct{}{}
		title, _ := testCase["title"].(string)
		if title = strings.TrimSpace(title); title == "" || len(title) > 160 {
			return fmt.Errorf("approved browser QA case title is invalid")
		}
		steps, ok := testCase["steps"].([]any)
		if !ok || len(steps) == 0 || len(steps) > 8 {
			return fmt.Errorf("approved browser QA case steps are invalid")
		}
		seenSteps := map[string]struct{}{}
		for index, rawStep := range steps {
			step, ok := rawStep.(map[string]any)
			if !ok {
				return fmt.Errorf("approved browser QA step is invalid")
			}
			stepID := fmt.Sprintf("step-%d", index+1)
			if rawID, present := step["id"]; present {
				value, ok := rawID.(string)
				if !ok {
					return fmt.Errorf("approved browser QA step ID is invalid")
				}
				stepID = strings.TrimSpace(value)
			}
			if !browserQACaseIdentifier.MatchString(stepID) {
				return fmt.Errorf("approved browser QA step ID is invalid")
			}
			if _, duplicate := seenSteps[stepID]; duplicate {
				return fmt.Errorf("approved browser QA step IDs must be unique within a case")
			}
			seenSteps[stepID] = struct{}{}
			kind, _ := step["kind"].(string)
			kind = strings.TrimSpace(kind)
			switch kind {
			case "navigate":
				if !safeBrowserQAPath(step["path"]) {
					return fmt.Errorf("approved browser QA navigation path is invalid")
				}
			case "assert_visible":
				if !safeBrowserQASelector(step["selector"]) {
					return fmt.Errorf("approved browser QA selector is invalid")
				}
			case "assert_text":
				if !safeBrowserQAAssertionText(step["text"]) {
					return fmt.Errorf("approved browser QA expected text is invalid")
				}
			case "click":
				if !safeBrowserQASelector(step["selector"]) {
					return fmt.Errorf("approved browser QA click selector is invalid")
				}
				if mode == "approved_navigation" && !safeBrowserQAPath(step["expected_path"]) {
					return fmt.Errorf("approved browser QA navigation click requires an expected same-origin path")
				}
				if mode != "approved_navigation" && mode != "approved_test_flow" {
					return fmt.Errorf("approved browser QA click requires a reviewed interaction mode")
				}
			case "fill":
				if mode != "approved_test_flow" || !safeBrowserQASelector(step["selector"]) || !safeBrowserQAEnvironmentReference(step["value_env"]) {
					return fmt.Errorf("approved browser QA fill requires a reviewed test value reference")
				}
			case "assert_path":
				if mode != "approved_test_flow" || !safeBrowserQAPath(step["path"]) {
					return fmt.Errorf("approved browser QA path assertion requires a reviewed test flow")
				}
			default:
				return fmt.Errorf("approved browser QA step kind is invalid")
			}
			if kind == "click" && mode == "approved_test_flow" {
				if index+1 >= len(steps) {
					return fmt.Errorf("approved browser QA test-flow click requires a following assertion")
				}
				next, ok := steps[index+1].(map[string]any)
				nextKind, _ := next["kind"].(string)
				if !ok || (nextKind != "assert_visible" && nextKind != "assert_text" && nextKind != "assert_path") {
					return fmt.Errorf("approved browser QA test-flow click requires a following assertion")
				}
			}
		}
	}
	return nil
}

// NormalizeBrowserQAProposal makes the provider-facing plan boundary tolerant
// of the common action alias without expanding what a browser plan may do.
// The persisted and executed representation is always the canonical `kind`.
// A model cannot use this to introduce a new action or to smuggle a conflict
// between two names for the same step.
func NormalizeBrowserQAProposal(plan map[string]any) error {
	rawCases, present := plan["browser_qa_cases"]
	if !present || rawCases == nil {
		return nil
	}
	cases, ok := rawCases.([]any)
	if !ok {
		return fmt.Errorf("approved browser QA cases are invalid")
	}
	for _, rawCase := range cases {
		testCase, ok := rawCase.(map[string]any)
		if !ok {
			return fmt.Errorf("approved browser QA case is invalid")
		}
		rawSteps, ok := testCase["steps"].([]any)
		if !ok {
			return fmt.Errorf("approved browser QA case steps are invalid")
		}
		for _, rawStep := range rawSteps {
			step, ok := rawStep.(map[string]any)
			if !ok {
				return fmt.Errorf("approved browser QA step is invalid")
			}
			rawKind, hasKind := step["kind"]
			rawAction, hasAction := step["action"]
			if !hasAction {
				continue
			}
			action, ok := rawAction.(string)
			action = strings.TrimSpace(action)
			if !ok || action == "" {
				return fmt.Errorf("approved browser QA step action is invalid")
			}
			if hasKind {
				kind, ok := rawKind.(string)
				kind = strings.TrimSpace(kind)
				if !ok || kind == "" || kind != action {
					return fmt.Errorf("approved browser QA step kind and action conflict")
				}
			} else {
				step["kind"] = action
			}
			delete(step, "action")
		}
	}
	return nil
}

func safeBrowserQAPath(raw any) bool {
	value, ok := raw.(string)
	value = strings.TrimSpace(value)
	return ok && strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") && !strings.Contains(value, "\\") && len(value) <= 1024
}

func safeBrowserQASelector(raw any) bool {
	value, ok := raw.(string)
	value = strings.TrimSpace(value)
	return ok && value != "" && len(value) <= 300 && !strings.ContainsAny(value, "\x00\r\n")
}

func safeBrowserQAAssertionText(raw any) bool {
	value, ok := raw.(string)
	value = strings.TrimSpace(value)
	return ok && value != "" && len(value) <= 500 && !strings.ContainsAny(value, "\x00\r\n")
}

func safeBrowserQAEnvironmentReference(raw any) bool {
	value, ok := raw.(string)
	return ok && browserQAEnvironmentReference.MatchString(strings.TrimSpace(value))
}

func captureScreenshotAt(ctx context.Context, taskID, previewURL string, workspace Workspace, root string, command []string, viewport screenshotViewport) (map[string]any, *LocalArtifact, error) {
	safePreviewURL := sanitizedPreviewURL(previewURL)
	if previewURLHasQueryOrFragment(previewURL) {
		return map[string]any{"url": safePreviewURL, "passed": false, "error": "signed preview URLs cannot be passed to argv-based screenshot runners"}, nil, fmt.Errorf("signed preview URLs cannot be passed to argv-based screenshot runners")
	}
	directory := filepath.Join(root, ".itbem-agent-evidence", taskID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, nil, fmt.Errorf("prepare QA evidence directory: %w", err)
	}
	path := filepath.Join(directory, "preview-"+viewport.Name+".png")
	generatedDefaultCommand := len(command) == 0
	if len(command) == 0 {
		var err error
		command, err = defaultScreenshotCommandAt(previewURL, path, viewport)
		if err != nil {
			return nil, nil, err
		}
	}
	if generatedDefaultCommand {
		// Chrome can leave GPU subprocesses alive for a moment after the
		// headless parent exits on Windows. Each capture receives a unique
		// profile, so a retry or another QA task never contends on a stale
		// PersistentCache lock. Cleanup remains best-effort by design.
		for _, argument := range command {
			if strings.HasPrefix(argument, "--user-data-dir=") {
				defer os.RemoveAll(strings.TrimPrefix(argument, "--user-data-dir="))
				break
			}
		}
	}
	rendered := make([]string, len(command))
	for index, part := range command {
		rendered[index] = strings.ReplaceAll(strings.ReplaceAll(part, "{preview_url}", safePreviewURL), "{artifact_path}", path)
	}
	completed, err := runWorkspaceCommand(ctx, workspace, root, 2*time.Minute, "", nil, rendered[0], rendered[1:]...)
	if err != nil {
		return map[string]any{"url": safePreviewURL, "command": rendered, "passed": false, "error": sanitizePreviewText(err.Error(), previewURL)}, nil, fmt.Errorf("screenshot command failed: %s", sanitizePreviewText(err.Error(), previewURL))
	}
	result := map[string]any{"url": safePreviewURL, "command": rendered, "passed": completed.ExitCode == 0, "output": sanitizePreviewText(completed.Output, previewURL)}
	if completed.ExitCode != 0 {
		return result, nil, nil
	}
	body, err := readLocalArtifact(path)
	if err != nil {
		result["passed"] = false
		result["output"] = sanitizePreviewText(err.Error(), previewURL)
		return result, nil, nil
	}
	return result, &LocalArtifact{Name: filepath.Base(path), Body: body, ContentType: "image/png"}, nil
}

// defaultScreenshotCommand provides a zero-config local harness for the
// Windows developer environment. Production runners can still supply a
// pinned Playwright command in qa_screenshot_command; neither route lets the
// agent choose a shell command.
func defaultScreenshotCommand(previewURL, outputPath string) ([]string, error) {
	return defaultScreenshotCommandAt(previewURL, outputPath, qaScreenshotViewports[0])
}

func defaultScreenshotCommandAt(previewURL, outputPath string, viewport screenshotViewport) ([]string, error) {
	if previewURLHasQueryOrFragment(previewURL) {
		return nil, fmt.Errorf("signed preview URLs cannot be passed to argv-based screenshot runners")
	}
	if strings.TrimSpace(viewport.Name) == "" || viewport.Width < 320 || viewport.Width > 4096 || viewport.Height < 320 || viewport.Height > 4096 {
		return nil, fmt.Errorf("QA screenshot viewport is invalid")
	}
	candidates := []string{
		"chrome", "chromium", "msedge",
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
	}
	for _, candidate := range candidates {
		if candidate == "" || candidate == "." {
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			// A unique private profile avoids Chrome's shared Temp GPU/cache
			// directories. The disk-cache flags avoid a Windows file lock that a
			// just-exited GPU subprocess can otherwise retain during retries.
			profile := filepath.Join(filepath.Dir(outputPath), "chrome-profile-"+strconv.FormatInt(time.Now().UTC().UnixNano(), 10))
			windowSize := "--window-size=" + strconv.Itoa(viewport.Width) + "," + strconv.Itoa(viewport.Height)
			return []string{path, "--headless=new", "--disable-gpu", "--disable-gpu-shader-disk-cache", "--disable-gpu-program-cache", "--disable-features=Vulkan", "--disable-dev-shm-usage", "--no-first-run", "--hide-scrollbars", "--user-data-dir=" + profile, windowSize, "--screenshot=" + outputPath, previewURL}, nil
		}
	}
	return nil, fmt.Errorf("QA screenshot requires Chrome/Chromium/Edge locally or qa_screenshot_command in the registered workspace")
}

func collectQAArtifacts(root string, patterns []string) ([]LocalArtifact, error) {
	seen, paths := map[string]bool{}, make([]string, 0)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil {
			return nil, fmt.Errorf("configured QA artifact pattern is invalid")
		}
		for _, path := range matches {
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	sort.Strings(paths)
	artifacts := make([]LocalArtifact, 0, len(paths))
	for _, path := range paths {
		if len(artifacts) == maxQAArtifacts {
			break
		}
		body, err := readSafeWorkspaceArtifact(root, path)
		if err != nil {
			continue
		}
		contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		artifacts = append(artifacts, LocalArtifact{Name: filepath.Base(path), Body: body, ContentType: contentType})
	}
	return artifacts, nil
}

func readSafeWorkspaceArtifact(root, path string) ([]byte, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("evidence root is invalid")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("evidence root must be a real directory")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("evidence artifact is invalid")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("evidence artifact is outside the registered workspace")
	}
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		if sandboxWorktreeCredential(component) || sandboxWorktreeExcluded(component) {
			return nil, fmt.Errorf("evidence artifact contains a restricted authority path")
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxQAArtifactBytes {
		return nil, fmt.Errorf("evidence artifact must be a regular local file")
	}
	allowed := map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".mp4": true, ".webm": true, ".json": true, ".txt": true}
	if !allowed[strings.ToLower(filepath.Ext(path))] {
		return nil, fmt.Errorf("evidence artifact type is not allowed")
	}
	confined, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("evidence root is unavailable")
	}
	defer confined.Close()
	openedRoot, err := confined.Stat(".")
	if err != nil || !os.SameFile(rootInfo, openedRoot) {
		return nil, fmt.Errorf("evidence root changed before reading")
	}
	file, err := openSandboxSourceFile(confined, relative)
	if err != nil {
		return nil, fmt.Errorf("evidence artifact cannot be opened within its workspace")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		return nil, fmt.Errorf("evidence artifact changed before reading")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxQAArtifactBytes+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || int64(len(body)) != opened.Size() || len(body) > maxQAArtifactBytes || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return nil, fmt.Errorf("evidence artifact changed or exceeds the size limit")
	}
	return body, nil
}

func readLocalArtifact(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxQAArtifactBytes {
		return nil, fmt.Errorf("evidence artifact is unavailable or exceeds the size limit")
	}
	allowed := map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".mp4": true, ".webm": true, ".json": true, ".txt": true}
	if !allowed[strings.ToLower(filepath.Ext(path))] {
		return nil, fmt.Errorf("evidence artifact type is not allowed")
	}
	return os.ReadFile(path)
}
