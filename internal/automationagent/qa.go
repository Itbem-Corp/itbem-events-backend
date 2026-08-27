package automationagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

type LocalArtifact struct {
	Name        string
	Body        []byte
	ContentType string
}

func RunQA(ctx context.Context, taskID string, delivery json.RawMessage, lookup func(string) string) (map[string]any, []LocalArtifact, error) {
	previewURL, err := deliveryPreviewURL(delivery)
	if err != nil {
		return nil, nil, err
	}
	targets, err := deliveryQATargets(delivery, lookup)
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
		result["repository_execution_order"] = append(result["repository_execution_order"].([]string), target.reference)
		commands := make([]any, 0, len(target.workspace.Config.ValidationCommands)+len(target.workspace.Config.QACommands))
		if target.execution.RunValidation {
			for _, command := range target.workspace.Config.ValidationCommands {
				completed, runErr := runLocal(ctx, target.root, commandTimeout, "", command[0], command[1:]...)
				if runErr != nil {
					return nil, nil, runErr
				}
				commands = append(commands, map[string]any{"phase": "validation", "command": command, "passed": completed.ExitCode == 0, "output": completed.Output})
			}
		}
		if target.execution.RunQA {
			for _, command := range target.workspace.Config.QACommands {
				completed, runErr := runLocal(ctx, target.root, commandTimeout, "", command[0], command[1:]...)
				if runErr != nil {
					return nil, nil, runErr
				}
				commands = append(commands, map[string]any{"phase": "qa", "command": command, "passed": completed.ExitCode == 0, "output": completed.Output})
			}
		}
		if target.execution.CollectEvidence {
			repositoryArtifacts, collectErr := collectQAArtifacts(target.root, target.workspace.Config.QAArtifactPatterns)
			if collectErr != nil {
				return nil, nil, collectErr
			}
			if remaining := regularArtifactLimit - len(artifacts); remaining > 0 {
				if len(repositoryArtifacts) > remaining {
					repositoryArtifacts = repositoryArtifacts[:remaining]
				}
				artifacts = append(artifacts, prefixQAArtifacts(repositoryArtifacts, target.workspace.ID)...)
			}
		}
		result["repository_runs"] = append(result["repository_runs"].([]any), map[string]any{
			"workspace": "workspace://" + target.workspace.ID, "branch": target.branch,
			"tested_directory": target.testedDirectory, "commands": commands,
			"execution_contract": target.execution.asMap(),
		})
	}
	if preview, ok := result["preview"].(map[string]any); ok && preview["passed"] == true && captureTarget != nil {
		if captureTarget.execution.RunStagehand && len(captureTarget.workspace.Config.QASemanticCommand) > 0 {
			semantic, semanticArtifacts, semanticErr := captureSemanticQA(ctx, taskID, previewURL, delivery, captureTarget.root, captureTarget.workspace.Config.QASemanticCommand, lookup)
			result["semantic"] = semantic
			if semanticErr != nil {
				return nil, nil, semanticErr
			}
			artifacts = append(artifacts, prefixQAArtifacts(semanticArtifacts, captureTarget.workspace.ID)...)
		}
		if len(captureTarget.workspace.Config.QAScreenshotCommand) > 0 {
			screenshot, artifact, captureErr := captureScreenshot(ctx, taskID, previewURL, captureTarget.root, captureTarget.workspace.Config.QAScreenshotCommand)
			result["screenshot"] = screenshot
			if captureErr != nil {
				return nil, nil, captureErr
			}
			if artifact != nil {
				artifacts = append(artifacts, prefixQAArtifacts([]LocalArtifact{*artifact}, captureTarget.workspace.ID)...)
			}
		} else {
			captures := make([]any, 0, len(qaScreenshotViewports))
			for index, viewport := range qaScreenshotViewports {
				screenshot, artifact, captureErr := captureScreenshotAt(ctx, taskID, previewURL, captureTarget.root, nil, viewport)
				if captureErr != nil {
					return nil, nil, captureErr
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
	return result, artifacts, nil
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
	workspace       Workspace
	reference       string
	root            string
	branch          string
	testedDirectory string
	execution       qaExecutionPolicy
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
	var input struct {
		ChangeSets []struct {
			RepositoryRef string `json:"repository_ref"`
			Branch        string `json:"branch"`
			ReviewType    string `json:"review_type"`
			CIStatus      string `json:"ci_status"`
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
		if !strings.EqualFold(strings.TrimSpace(change.ReviewType), "local_worktree") || !strings.EqualFold(strings.TrimSpace(change.CIStatus), "passed") {
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
		seen[reference] = struct{}{}
		targets = append(targets, qaTarget{workspace: workspace, reference: reference, root: root, branch: branch, testedDirectory: "reviewed isolated worktree", execution: policy})
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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, previewURL, nil)
	if err != nil {
		return map[string]any{"url": previewURL, "passed": false, "error": "preview request could not be created"}
	}
	request.Header.Set("User-Agent", "ITBEM-Delivery-QA/1.0")
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return map[string]any{"url": previewURL, "passed": false, "error": "preview request failed"}
	}
	defer response.Body.Close()
	_, _ = io.CopyN(io.Discard, response.Body, 2048)
	return map[string]any{"url": previewURL, "passed": response.StatusCode >= 200 && response.StatusCode < 400, "status": response.StatusCode, "content_type": response.Header.Get("Content-Type")}
}

func captureScreenshot(ctx context.Context, taskID, previewURL, root string, command []string) (map[string]any, *LocalArtifact, error) {
	return captureScreenshotAt(ctx, taskID, previewURL, root, command, qaScreenshotViewports[0])
}

// captureSemanticQA runs one configured semantic browser probe and records
// only its bounded JSON report plus its sibling PNG evidence. The worker
// treats its exit status as a QA check; it never lets a language model decide
// whether a human gate opens.
func captureSemanticQA(ctx context.Context, taskID, previewURL string, delivery json.RawMessage, root string, command []string, lookup func(string) string) (map[string]any, []LocalArtifact, error) {
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
		rendered[index] = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(part, "{preview_url}", previewURL), "{artifact_path}", path), "{qa_plan_path}", planPath)
	}
	rendered, err = resolveSemanticQACommand(rendered, lookup)
	if err != nil {
		return nil, nil, err
	}
	environment, err := semanticQAEnvironment(rendered, lookup)
	if err != nil {
		return nil, nil, err
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
	for key, value := range testEnvironment {
		environment[key] = value
	}
	completed, err := runLocalWithEnv(ctx, root, 3*time.Minute, "", environment, rendered[0], rendered[1:]...)
	if err != nil {
		return nil, nil, err
	}
	result := map[string]any{"command": rendered, "passed": completed.ExitCode == 0, "output": completed.Output}
	artifacts := make([]LocalArtifact, 0, qaSemanticArtifactSlots(command))
	var report map[string]any
	if body, readErr := readLocalArtifact(path); readErr == nil {
		artifacts = append(artifacts, LocalArtifact{Name: filepath.Base(path), Body: body, ContentType: "application/json"})
		if json.Unmarshal(body, &report) == nil {
			result["report"] = report
		}
	} else if completed.ExitCode == 0 {
		result["passed"] = false
		result["output"] = "semantic QA did not produce its required report"
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
		return nil, nil, err
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
		return fmt.Errorf("Stagehand report is missing its evidence manifest")
	}
	rawManifest, ok := evidence["artifacts"].([]any)
	if !ok || len(rawManifest) < 2 || len(rawManifest) > 8 {
		return fmt.Errorf("Stagehand evidence manifest is invalid")
	}
	images := make(map[string]LocalArtifact)
	for _, artifact := range artifacts {
		if artifact.ContentType != "image/png" {
			continue
		}
		if _, exists := images[artifact.Name]; exists {
			return fmt.Errorf("Stagehand evidence contains duplicate image artifacts")
		}
		images[artifact.Name] = artifact
	}
	if len(images) != len(rawManifest) {
		return fmt.Errorf("Stagehand evidence manifest does not match captured image artifacts")
	}
	seen := make(map[string]struct{}, len(rawManifest))
	for _, raw := range rawManifest {
		entry, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("Stagehand evidence manifest is invalid")
		}
		name := strings.TrimSpace(fmt.Sprint(entry["name"]))
		contentType := strings.TrimSpace(fmt.Sprint(entry["content_type"]))
		expectedSHA := strings.ToLower(strings.TrimSpace(fmt.Sprint(entry["sha256"])))
		rawBytes, bytesOK := entry["bytes"].(float64)
		if name == "" || filepath.Base(name) != name || strings.ToLower(filepath.Ext(name)) != ".png" || contentType != "image/png" || !bytesOK || rawBytes < 1 || rawBytes != math.Trunc(rawBytes) || len(expectedSHA) != 64 {
			return fmt.Errorf("Stagehand evidence manifest is invalid")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("Stagehand evidence manifest contains duplicate images")
		}
		seen[name] = struct{}{}
		artifact, found := images[name]
		if !found || int64(len(artifact.Body)) != int64(rawBytes) {
			return fmt.Errorf("Stagehand evidence artifact %q does not match its manifest", name)
		}
		actualSHA := fmt.Sprintf("%x", sha256.Sum256(artifact.Body))
		if actualSHA != expectedSHA {
			return fmt.Errorf("Stagehand evidence artifact %q failed integrity verification", name)
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

// semanticQAEnvironment keeps provider credentials out of every repository
// command. The only exception is ITBEM's pinned Stagehand runner, which needs
// inference to interpret browser state. The command is still configured by an
// operator, but its script location must be the owned Stagehand entrypoint;
// generic QA commands and customer repository code never receive this key.
func semanticQAEnvironment(command []string, lookup func(string) string) (map[string]string, error) {
	if !isPinnedStagehandCommand(command) {
		return nil, nil
	}
	if lookup == nil {
		return nil, fmt.Errorf("Stagehand semantic QA requires a credential lookup")
	}
	apiKey := strings.TrimSpace(lookup("MINIMAX_API_KEY"))
	if apiKey == "" {
		return nil, fmt.Errorf("Stagehand semantic QA requires the configured MiniMax credential")
	}
	return map[string]string{"MINIMAX_API_KEY": apiKey}, nil
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
	if len(command) < 2 || !approvedSemanticRuntime(command[0]) || (!strings.EqualFold(filepath.Base(command[0]), "node.exe") && command[0] != "node") {
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

func captureScreenshotAt(ctx context.Context, taskID, previewURL, root string, command []string, viewport screenshotViewport) (map[string]any, *LocalArtifact, error) {
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
		rendered[index] = strings.ReplaceAll(strings.ReplaceAll(part, "{preview_url}", previewURL), "{artifact_path}", path)
	}
	completed, err := runLocal(ctx, root, 2*time.Minute, "", rendered[0], rendered[1:]...)
	if err != nil {
		return nil, nil, err
	}
	result := map[string]any{"command": rendered, "passed": completed.ExitCode == 0, "output": completed.Output}
	if completed.ExitCode != 0 {
		return result, nil, nil
	}
	body, err := readLocalArtifact(path)
	if err != nil {
		result["passed"] = false
		result["output"] = err.Error()
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
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("evidence artifact is invalid")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("evidence artifact is outside the registered workspace")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("evidence artifact must be a regular local file")
	}
	return readLocalArtifact(path)
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
