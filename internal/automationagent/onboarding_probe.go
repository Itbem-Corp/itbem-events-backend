package automationagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"events-stocks/internal/projectvault"
)

const onboardingProbeSchemaVersion = 1

var onboardingCommandProbeCapabilities = map[string]struct{}{
	"unit": {}, "integration": {}, "contract": {}, "e2e": {},
	"preview": {}, "staging": {}, "health": {}, "recovery": {},
}

// OnboardingProbeExecution is the value-free callback contract. Command
// arguments and output remain only in the encrypted private result object.
type OnboardingProbeExecution struct {
	SchemaVersion       int                            `json:"schema_version"`
	TaskID              string                         `json:"task_id"`
	RepositoryReference string                         `json:"repository_reference"`
	DefaultBranch       string                         `json:"default_branch"`
	Revision            string                         `json:"revision"`
	WorkspaceReference  string                         `json:"workspace_reference"`
	ExecutorRole        string                         `json:"executor_role"`
	Probes              []projectvault.CapabilityProbe `json:"probes"`
}

type onboardingProbeSpec struct {
	SchemaVersion       int      `json:"schema_version"`
	WorkspaceReference  string   `json:"workspace_reference"`
	RepositoryReference string   `json:"repository_reference"`
	DefaultBranch       string   `json:"default_branch"`
	Revision            string   `json:"revision"`
	Capabilities        []string `json:"capabilities"`
}

type onboardingProbeDelivery struct {
	OnboardingProbe onboardingProbeSpec `json:"onboarding_probe"`
}

type onboardingPrivateEvidence struct {
	SchemaVersion       int      `json:"schema_version"`
	TaskID              string   `json:"task_id"`
	RepositoryReference string   `json:"repository_reference"`
	DefaultBranch       string   `json:"default_branch"`
	Revision            string   `json:"revision"`
	WorkspaceReference  string   `json:"workspace_reference"`
	Capability          string   `json:"capability"`
	Phase               string   `json:"phase"`
	Configured          bool     `json:"configured"`
	Command             []string `json:"command,omitempty"`
	CommandSHA256       string   `json:"command_sha256,omitempty"`
	ExitCode            int      `json:"exit_code"`
	RedactedOutput      string   `json:"redacted_output,omitempty"`
	OutputSHA256        string   `json:"output_sha256,omitempty"`
	RedactedValues      int      `json:"redacted_values,omitempty"`
	ExecutionError      bool     `json:"execution_error,omitempty"`
}

type configuredOnboardingProbeCommand struct {
	phase   string
	command []string
}

// NormalizeOnboardingProbeCapabilities accepts only non-destructive local
// capability checks. GitHub write/review permissions and release authority
// require separate API/policy probes and cannot be inferred from a command.
func NormalizeOnboardingProbeCapabilities(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > len(onboardingCommandProbeCapabilities) {
		return nil, fmt.Errorf("onboarding capability probes require one to eight supported capabilities")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if _, allowed := onboardingCommandProbeCapabilities[value]; !allowed {
			return nil, fmt.Errorf("onboarding capability %q requires a different deterministic probe", value)
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("onboarding capability %q is duplicated", value)
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

// BuildOnboardingProbeDelivery creates the only task input accepted by the
// deterministic probe worker. It carries identities, never a command or path.
func BuildOnboardingProbeDelivery(workspaceReference, repositoryReference, defaultBranch, revision string, capabilities []string) (json.RawMessage, error) {
	capabilities, err := NormalizeOnboardingProbeCapabilities(capabilities)
	if err != nil {
		return nil, err
	}
	workspaceReference = strings.TrimSpace(workspaceReference)
	if !strings.HasPrefix(workspaceReference, "workspace://") {
		return nil, fmt.Errorf("onboarding probe requires a registered workspace reference")
	}
	repositoryReference, err = projectvault.CanonicalGitHubReference(repositoryReference)
	if err != nil {
		return nil, err
	}
	defaultBranch, revision = strings.TrimSpace(defaultBranch), strings.ToLower(strings.TrimSpace(revision))
	if !gitBranchName.MatchString(defaultBranch) || strings.Contains(defaultBranch, "..") || strings.HasSuffix(defaultBranch, "/") || !projectvault.ValidRevision(revision) {
		return nil, fmt.Errorf("onboarding probe requires the inspected default branch and immutable full Git SHA")
	}
	return json.Marshal(onboardingProbeDelivery{OnboardingProbe: onboardingProbeSpec{
		SchemaVersion: onboardingProbeSchemaVersion, WorkspaceReference: workspaceReference,
		RepositoryReference: repositoryReference, DefaultBranch: defaultBranch, Revision: revision, Capabilities: capabilities,
	}})
}

func decodeOnboardingProbeDelivery(raw json.RawMessage) (onboardingProbeSpec, error) {
	var input onboardingProbeDelivery
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return onboardingProbeSpec{}, fmt.Errorf("onboarding probe input is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return onboardingProbeSpec{}, fmt.Errorf("onboarding probe input is invalid")
	}
	normalized, err := BuildOnboardingProbeDelivery(input.OnboardingProbe.WorkspaceReference, input.OnboardingProbe.RepositoryReference, input.OnboardingProbe.DefaultBranch, input.OnboardingProbe.Revision, input.OnboardingProbe.Capabilities)
	if err != nil || input.OnboardingProbe.SchemaVersion != onboardingProbeSchemaVersion {
		return onboardingProbeSpec{}, fmt.Errorf("onboarding probe input is invalid")
	}
	var canonical onboardingProbeDelivery
	if json.Unmarshal(normalized, &canonical) != nil {
		return onboardingProbeSpec{}, fmt.Errorf("onboarding probe input is invalid")
	}
	return canonical.OnboardingProbe, nil
}

// RunOnboardingCapabilityProbes fetches origin, creates an ephemeral detached
// worktree at the exact inspected SHA, and executes only operator-owned named
// commands. Repository content can never select argv or inherit worker secrets.
func RunOnboardingCapabilityProbes(ctx context.Context, taskID string, delivery json.RawMessage, lookup func(string) string) (privateResult map[string]any, execution OnboardingProbeExecution, err error) {
	if !taskIDPattern.MatchString(strings.ToLower(strings.TrimSpace(taskID))) {
		return nil, execution, fmt.Errorf("onboarding probe task ID is invalid")
	}
	spec, err := decodeOnboardingProbeDelivery(delivery)
	if err != nil {
		return nil, execution, err
	}
	workspace, err := RegisteredWorkspace(spec.WorkspaceReference, lookup)
	if err != nil {
		return nil, execution, err
	}
	worktree, err := prepareOnboardingProbeWorktree(ctx, workspace, taskID, spec.RepositoryReference, spec.DefaultBranch, spec.Revision)
	if err != nil {
		return nil, execution, err
	}
	defer func() {
		if cleanupErr := removeOnboardingProbeWorktree(context.Background(), workspace, taskID, worktree); cleanupErr != nil && err == nil {
			privateResult, execution, err = nil, OnboardingProbeExecution{}, cleanupErr
		}
	}()

	configured := configuredOnboardingProbeCommands(workspace.Config)
	evidence := make([]onboardingPrivateEvidence, 0, len(spec.Capabilities))
	probes := make([]projectvault.CapabilityProbe, 0, len(spec.Capabilities))
	for _, capability := range spec.Capabilities {
		item := onboardingPrivateEvidence{
			SchemaVersion: onboardingProbeSchemaVersion, TaskID: taskID,
			RepositoryReference: spec.RepositoryReference, DefaultBranch: spec.DefaultBranch, Revision: spec.Revision,
			WorkspaceReference: spec.WorkspaceReference, Capability: capability, ExitCode: -1,
		}
		state, reason := "blocked", "no operator-owned command is configured for this capability"
		if command, ok := configured[capability]; ok {
			item.Configured, item.Phase = true, command.phase
			item.Command = append([]string(nil), command.command...)
			item.CommandSHA256, err = canonicalSHA256(item.Command)
			if err != nil {
				return nil, execution, err
			}
			completed, runErr := runLocal(ctx, worktree, commandTimeout, "", command.command[0], command.command[1:]...)
			if ctx.Err() != nil {
				return nil, execution, ctx.Err()
			}
			if runErr != nil {
				item.ExecutionError = true
				reason = "operator-owned capability command could not complete"
			} else {
				item.ExitCode = completed.ExitCode
				redactedOutput, redactedValues := RedactSourceExcerpt(completed.Output)
				item.RedactedValues = redactedValues
				item.RedactedOutput = redactedOutput
				item.OutputSHA256, err = canonicalSHA256(redactedOutput)
				if err != nil {
					return nil, execution, err
				}
				if completed.ExitCode == 0 {
					state, reason = "ready", "operator-owned capability command exited zero at the exact repository SHA"
				} else {
					reason = "operator-owned capability command exited non-zero at the exact repository SHA"
				}
			}
			if verifyErr := verifyOnboardingProbeWorktree(ctx, worktree, spec.Revision); verifyErr != nil {
				return nil, execution, verifyErr
			}
		}
		digest, digestErr := canonicalSHA256(item)
		if digestErr != nil {
			return nil, execution, digestErr
		}
		probe := projectvault.CapabilityProbe{
			Name: capability, State: state, Reason: reason, Revision: spec.Revision,
			EvidenceSHA256: digest, ExecutorRole: "qa",
		}
		probe.SubjectSHA256, err = projectvault.CapabilityProbeSubjectSHA256(projectvault.Repository{Reference: spec.RepositoryReference, Revision: spec.Revision}, probe)
		if err != nil {
			return nil, execution, err
		}
		evidence, probes = append(evidence, item), append(probes, probe)
	}

	execution = OnboardingProbeExecution{
		SchemaVersion: onboardingProbeSchemaVersion, TaskID: taskID,
		RepositoryReference: spec.RepositoryReference, DefaultBranch: spec.DefaultBranch, Revision: spec.Revision,
		WorkspaceReference: spec.WorkspaceReference, ExecutorRole: "qa", Probes: probes,
	}
	privateResult = map[string]any{
		"schema_version": onboardingProbeSchemaVersion, "task_id": taskID,
		"repository_reference": spec.RepositoryReference, "default_branch": spec.DefaultBranch, "revision": spec.Revision,
		"workspace_reference": spec.WorkspaceReference, "executor_role": "qa",
		"worktree_mode": "ephemeral_detached_exact_sha", "evidence": evidence,
	}
	return privateResult, execution, nil
}

func configuredOnboardingProbeCommands(config WorkspaceConfig) map[string]configuredOnboardingProbeCommand {
	result := make(map[string]configuredOnboardingProbeCommand)
	for _, group := range []struct {
		phase    string
		commands [][]string
		kinds    []string
	}{{"validation", config.ValidationCommands, config.ValidationCommandKinds}, {"qa", config.QACommands, config.QACommandKinds}} {
		if len(group.commands) != len(group.kinds) {
			continue
		}
		for index, kind := range group.kinds {
			kind = strings.ToLower(strings.TrimSpace(kind))
			if _, allowed := onboardingCommandProbeCapabilities[kind]; allowed {
				result[kind] = configuredOnboardingProbeCommand{phase: group.phase, command: append([]string(nil), group.commands[index]...)}
			}
		}
	}
	return result
}

func prepareOnboardingProbeWorktree(ctx context.Context, workspace Workspace, taskID, repositoryReference, defaultBranch, revision string) (string, error) {
	if err := workspace.RequireCapability(WorkspaceCapabilityFetchRemote); err != nil {
		return "", err
	}
	if err := workspace.RequireCapability(WorkspaceCapabilityCreateWorktree); err != nil {
		return "", err
	}
	if strings.TrimSpace(workspace.Config.RepositoryURL) == "" || strings.TrimSpace(workspace.Config.BaseBranch) == "" {
		return "", fmt.Errorf("onboarding probes require an operator-managed repository_url and base_branch")
	}
	if workspace.Config.BaseBranch != defaultBranch {
		return "", fmt.Errorf("onboarding probe workspace base branch does not match the inspected default branch")
	}
	state := ReadWorkspaceGitState(workspace)
	if !state.Available || state.HasLocalChanges {
		return "", fmt.Errorf("onboarding probe base workspace must be a clean readable Git checkout")
	}
	rawOrigin, originErr := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "config", "--get", "remote.origin.url")
	if originErr != nil || rawOrigin.ExitCode != 0 {
		return "", fmt.Errorf("onboarding probe workspace has no operator-owned origin")
	}
	remote, err := parseGitHubRemote(rawOrigin.Output)
	if err != nil || !strings.EqualFold("github://"+remote.Owner+"/"+remote.Name, repositoryReference) {
		return "", fmt.Errorf("onboarding probe workspace origin does not match the inspected repository")
	}
	if _, err := FetchWorkspaceRemote(ctx, workspace); err != nil {
		return "", err
	}
	remoteBase := "refs/remotes/origin/" + workspace.Config.BaseBranch
	known, knownErr := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "rev-parse", "--verify", "--quiet", revision+"^{commit}")
	if knownErr != nil || known.ExitCode != 0 || !strings.EqualFold(strings.TrimSpace(known.Output), revision) {
		return "", fmt.Errorf("onboarding probe revision is unavailable after the remote fetch")
	}
	ancestor, ancestorErr := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "merge-base", "--is-ancestor", revision, remoteBase)
	if ancestorErr != nil || ancestor.ExitCode != 0 {
		return "", fmt.Errorf("onboarding probe revision is not part of the registered remote base branch")
	}
	directory := filepath.Join(workspace.Root, ".itbem-agent-worktrees", "probe-"+taskID)
	if err := validateOnboardingProbeDirectory(workspace.Root, taskID, directory); err != nil {
		return "", err
	}
	if info, statErr := os.Stat(directory); statErr == nil && info.IsDir() {
		if err := removeOnboardingProbeWorktree(ctx, workspace, taskID, directory); err != nil {
			return "", fmt.Errorf("stale onboarding probe worktree could not be removed safely")
		}
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", fmt.Errorf("onboarding probe worktree path is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
		return "", fmt.Errorf("prepare onboarding probe worktree parent: %w", err)
	}
	created, createErr := runLocal(ctx, workspace.Root, 90*time.Second, "", "git", "worktree", "add", "--detach", directory, revision)
	if createErr != nil || created.ExitCode != 0 {
		return "", fmt.Errorf("onboarding probe exact-SHA worktree could not be created")
	}
	if err := copyReadOnlyWorkspaceFixtures(workspace, directory); err != nil {
		_ = removeOnboardingProbeWorktree(context.Background(), workspace, taskID, directory)
		return "", err
	}
	if err := verifyOnboardingProbeWorktree(ctx, directory, revision); err != nil {
		_ = removeOnboardingProbeWorktree(context.Background(), workspace, taskID, directory)
		return "", err
	}
	return directory, nil
}

func verifyOnboardingProbeWorktree(ctx context.Context, directory, revision string) error {
	head, err := runLocal(ctx, directory, 20*time.Second, "", "git", "rev-parse", "HEAD")
	if err != nil || head.ExitCode != 0 || !strings.EqualFold(strings.TrimSpace(head.Output), revision) {
		return fmt.Errorf("onboarding probe command changed the exact repository revision")
	}
	tracked, err := runLocal(ctx, directory, 20*time.Second, "", "git", "status", "--porcelain", "--untracked-files=no")
	if err != nil || tracked.ExitCode != 0 || strings.TrimSpace(tracked.Output) != "" {
		return fmt.Errorf("onboarding probe command modified tracked repository content")
	}
	return nil
}

func removeOnboardingProbeWorktree(ctx context.Context, workspace Workspace, taskID, directory string) error {
	if err := validateOnboardingProbeDirectory(workspace.Root, taskID, directory); err != nil {
		return err
	}
	listed, err := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "worktree", "list", "--porcelain")
	if err != nil || listed.ExitCode != 0 {
		return fmt.Errorf("onboarding probe worktree registry is unavailable")
	}
	registered := false
	wanted, _ := filepath.Abs(directory)
	for _, line := range strings.Split(listed.Output, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		candidate, absErr := filepath.Abs(strings.TrimSpace(strings.TrimPrefix(line, "worktree ")))
		if absErr == nil && strings.EqualFold(filepath.Clean(candidate), filepath.Clean(wanted)) {
			registered = true
			break
		}
	}
	if !registered {
		if _, statErr := os.Stat(directory); os.IsNotExist(statErr) {
			return nil
		}
		return fmt.Errorf("refusing to remove an unregistered onboarding probe directory")
	}
	removed, removeErr := runLocal(ctx, workspace.Root, 45*time.Second, "", "git", "worktree", "remove", "--force", directory)
	if removeErr != nil || removed.ExitCode != 0 {
		return fmt.Errorf("onboarding probe worktree cleanup failed")
	}
	return nil
}

func validateOnboardingProbeDirectory(root, taskID, directory string) error {
	if !taskIDPattern.MatchString(strings.ToLower(strings.TrimSpace(taskID))) {
		return fmt.Errorf("onboarding probe task ID is invalid")
	}
	expected, err := filepath.Abs(filepath.Join(root, ".itbem-agent-worktrees", "probe-"+taskID))
	actual, actualErr := filepath.Abs(directory)
	if err != nil || actualErr != nil || !strings.EqualFold(filepath.Clean(expected), filepath.Clean(actual)) {
		return fmt.Errorf("onboarding probe worktree path is invalid")
	}
	return nil
}

func canonicalSHA256(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// DecodeOnboardingProbeExecution validates the callback projection without
// trusting worker prose or private command output.
func DecodeOnboardingProbeExecution(raw json.RawMessage) (OnboardingProbeExecution, error) {
	var execution OnboardingProbeExecution
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&execution); err != nil {
		return execution, fmt.Errorf("onboarding capability probe execution is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return execution, fmt.Errorf("onboarding capability probe execution is invalid")
	}
	if execution.SchemaVersion != onboardingProbeSchemaVersion || !taskIDPattern.MatchString(strings.ToLower(strings.TrimSpace(execution.TaskID))) || execution.ExecutorRole != "qa" || !strings.HasPrefix(execution.WorkspaceReference, "workspace://") || !gitBranchName.MatchString(execution.DefaultBranch) || strings.Contains(execution.DefaultBranch, "..") || strings.HasSuffix(execution.DefaultBranch, "/") || len(execution.Probes) == 0 || len(execution.Probes) > len(onboardingCommandProbeCapabilities) {
		return execution, fmt.Errorf("onboarding capability probe execution is invalid")
	}
	reference, err := projectvault.CanonicalGitHubReference(execution.RepositoryReference)
	if err != nil || reference != execution.RepositoryReference || !projectvault.ValidRevision(execution.Revision) {
		return execution, fmt.Errorf("onboarding capability probe execution is invalid")
	}
	seen := make(map[string]struct{}, len(execution.Probes))
	repository := projectvault.Repository{Reference: execution.RepositoryReference, Revision: execution.Revision}
	for _, probe := range execution.Probes {
		if _, allowed := onboardingCommandProbeCapabilities[probe.Name]; !allowed || probe.ExecutorRole != execution.ExecutorRole || !strings.EqualFold(probe.Revision, execution.Revision) {
			return execution, fmt.Errorf("onboarding capability probe execution is invalid")
		}
		if _, duplicate := seen[probe.Name]; duplicate {
			return execution, fmt.Errorf("onboarding capability probe execution is invalid")
		}
		subject, subjectErr := projectvault.CapabilityProbeSubjectSHA256(repository, probe)
		if subjectErr != nil || !strings.EqualFold(subject, probe.SubjectSHA256) {
			return execution, fmt.Errorf("onboarding capability probe execution is invalid")
		}
		seen[probe.Name] = struct{}{}
	}
	return execution, nil
}

func onboardingProbeExecutionMap(execution OnboardingProbeExecution) (map[string]any, error) {
	encoded, err := json.Marshal(execution)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}
