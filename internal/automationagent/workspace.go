package automationagent

// The local workspace registry is operator-owned configuration. A task may
// reference a registered workspace by ID, never a filesystem path.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	maxWorkspaceFiles        = 2500
	maxWorkspaceFileBytes    = 180000
	maxWorkspaceExcerptChars = 180000
	maxWorkspaceExcerptBytes = 24000
	maxWorkspaceExcerpts     = 24
	maxReadOnlyFixturePaths  = 16
	maxReadOnlyFixtureFiles  = 500
	maxReadOnlyFixtureBytes  = 64 << 20
)

// sensitiveWorkspaceKey matches common secret-bearing configuration keys with
// optional vendor prefixes/suffixes, e.g. AWS_SECRET_ACCESS_KEY or
// GITHUB_API_TOKEN, without treating arbitrary prose as a credential.
const sensitiveWorkspaceKey = `(?:[A-Za-z0-9]+[_-])*(?:api[_-]?key|apikey|access[_-]?key|client[_-]?secret|private[_-]?key|password|secret|token|authorization|service[_-]?account)(?:[_-][A-Za-z0-9]+)*`

var workspaceTestKind = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/+-]{0,127}$`)

type WorkspaceConfig struct {
	AcceptanceChecks []AcceptanceCheck `json:"acceptance_checks"`
	Path             string            `json:"path"`
	// RepositoryURL and BaseBranch are used only by the operator-invoked
	// checkout synchronizer. They let one worker host maintain dedicated,
	// reproducible base checkouts for many projects without letting a task pick
	// a path, remote, or branch.
	RepositoryURL      string     `json:"repository_url"`
	BaseBranch         string     `json:"base_branch"`
	Capabilities       []string   `json:"capabilities"`
	ValidationCommands [][]string `json:"validation_commands"`
	// Command kinds are operator-owned labels for the matching command. Agents
	// may request an approved command, but cannot invent or rename the evidence.
	ValidationCommandKinds []string `json:"validation_command_kinds"`
	QACommandKinds         []string `json:"qa_command_kinds"`
	// ReadOnlyFixturePaths are bounded, operator-owned files copied into exact-
	// revision worktrees without Git metadata, symlinks, or credential paths.
	ReadOnlyFixturePaths []string `json:"read_only_fixture_paths"`
	// ComponentValidationCommands maps an operator-owned monorepo component
	// root to the additional validation commands that cover that component.
	// The model can select an already-approved scope, but never a command.
	ComponentValidationCommands map[string][][]string `json:"component_validation_commands"`
	QACommands                  [][]string            `json:"qa_commands"`
	QAArtifactPatterns          []string              `json:"qa_artifact_patterns"`
	QAScreenshotCommand         []string              `json:"qa_screenshot_command"`
	// QASemanticCommand is an operator-owned, opt-in browser QA layer (for
	// example the pinned Stagehand runner). It receives only the reviewed
	// preview URL and a private evidence output path; a task or model response
	// can never select its executable or arguments.
	QASemanticCommand []string `json:"qa_semantic_command"`
	// SandboxRuntime controls where registered validation/QA commands execute.
	// "process" preserves the existing operator-trusted runner; "docker"
	// enables the explicit resource/network boundary in sandbox.go. Git
	// plumbing remains host-side and is never inferred from this setting.
	SandboxRuntime     string `json:"sandbox_runtime"`
	SandboxImage       string `json:"sandbox_image"`
	SandboxImageDigest string `json:"sandbox_image_digest"`
	SandboxNetwork     string `json:"sandbox_network"`
	SandboxCPUs        string `json:"sandbox_cpus"`
	SandboxMemory      string `json:"sandbox_memory"`
	SandboxPIDsLimit   int    `json:"sandbox_pids_limit"`
	// SandboxSupervisorCommand is an operator-owned executable that creates
	// and tears down one Firecracker VM per command. It receives a bounded JSON
	// request on stdin and must return the supervised result plus a verified
	// guest attestation. A task/model can never select or alter this command.
	SandboxSupervisorCommand []string `json:"sandbox_supervisor_command"`
	// RequireSandbox makes process mode invalid for this workspace. It is the
	// fail-closed switch for repositories whose validation/QA toolchain is not
	// operator-trusted; a worktree alone is never treated as a sandbox.
	RequireSandbox bool `json:"require_sandbox"`
}

const (
	WorkspaceCapabilityReadRepository = "repository:read"
	// WorkspaceCapabilityFetchRemote permits a human-triggered `git fetch`
	// only. It never permits pull, checkout, merge, rebase or a worktree write.
	WorkspaceCapabilityFetchRemote    = "repository:fetch"
	WorkspaceCapabilityCreateWorktree = "worktree:create"
	WorkspaceCapabilityApplyPatch     = "patch:apply"
	WorkspaceCapabilityStageCommit    = "commit:stage"
	WorkspaceCapabilityPublishBranch  = "branch:publish"
	WorkspaceCapabilityCreatePullReq  = "pull_request:create"
)

const (
	WorkspaceSandboxProcess     = "process"
	WorkspaceSandboxDocker      = "docker"
	WorkspaceSandboxFirecracker = "firecracker"
)

var workspaceCapabilities = map[string]struct{}{
	WorkspaceCapabilityReadRepository: {}, WorkspaceCapabilityFetchRemote: {}, WorkspaceCapabilityCreateWorktree: {}, WorkspaceCapabilityApplyPatch: {},
	WorkspaceCapabilityStageCommit: {}, WorkspaceCapabilityPublishBranch: {}, WorkspaceCapabilityCreatePullReq: {},
}

func (workspace Workspace) AllowsCapability(capability string) bool {
	for _, configured := range workspace.Config.Capabilities {
		if configured == capability {
			return true
		}
	}
	return false
}

func (workspace Workspace) RequireCapability(capability string) error {
	if workspace.AllowsCapability(capability) {
		return nil
	}
	return fmt.Errorf("workspace %s is not granted capability %s", workspace.ID, capability)
}

type Workspace struct {
	ID     string
	Root   string
	Config WorkspaceConfig
}

// WorkspaceValidationCommand is the executable plus its operator-owned
// component scope. Scope is evidence metadata; it never comes from model
// supplied command text.
type WorkspaceValidationCommand struct {
	Scope   string
	Command []string
}

// Harness returns the safe capability summary that may be persisted in
// Delivery context or shown to an operator. Command arguments stay private to
// the local runner and are never included in this projection.
func (workspace Workspace) Harness() WorkspaceHarness {
	return workspaceHarness(workspace.Config)
}

func LoadWorkspaces(raw string) (map[string]Workspace, error) {
	return loadWorkspaces(raw, true)
}

// LoadWorkspaceRegistry validates operator configuration even when a managed
// checkout has not been cloned yet. Runtime paths must continue to use
// LoadWorkspaces, which requires every configured directory to exist.
func LoadWorkspaceRegistry(raw string) (map[string]Workspace, error) {
	return loadWorkspaces(raw, false)
}

func loadWorkspaces(raw string, requireDirectory bool) (map[string]Workspace, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("ITBEM_AI_WORKSPACES_JSON must be a JSON object")
	}
	result := make(map[string]Workspace, len(entries))
	for id, encoded := range entries {
		id = strings.TrimSpace(id)
		if id == "" || strings.ContainsAny(id, "\\/") {
			return nil, fmt.Errorf("workspace ID is invalid")
		}
		var config WorkspaceConfig
		if err := json.Unmarshal(encoded, &config); err != nil {
			// A string path is supported only for read-only planning context.
			var path string
			if json.Unmarshal(encoded, &path) != nil {
				return nil, fmt.Errorf("workspace %s configuration is invalid", id)
			}
			config.Path = path
		}
		root, err := filepath.Abs(strings.TrimSpace(config.Path))
		if err != nil || root == "" {
			return nil, fmt.Errorf("workspace %s path is invalid", id)
		}
		info, err := os.Stat(root)
		if (err != nil || !info.IsDir()) && (requireDirectory || strings.TrimSpace(config.RepositoryURL) == "") {
			return nil, fmt.Errorf("configured workspace is not a directory: %s", id)
		}
		config.Path = root
		config.RepositoryURL = strings.TrimSpace(config.RepositoryURL)
		config.BaseBranch = strings.TrimSpace(config.BaseBranch)
		if config.BaseBranch == "" {
			config.BaseBranch = "main"
		}
		if err := validateWorkspaceBase(config.RepositoryURL, config.BaseBranch); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", id, err)
		}
		if len(config.Capabilities) == 0 {
			// Compatibility default for existing local workspace registries. These
			// are the only capabilities used by the current isolated workflow;
			// staging, publishing and PR creation always need an explicit grant.
			config.Capabilities = []string{WorkspaceCapabilityReadRepository, WorkspaceCapabilityCreateWorktree, WorkspaceCapabilityApplyPatch}
		}
		if err := validateWorkspaceCapabilities(config.Capabilities); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", id, err)
		}
		if err := validateCommandList("validation_commands", config.ValidationCommands); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", id, err)
		}
		if err := validateCommandKinds(config.ValidationCommands, config.ValidationCommandKinds, config.QACommands, config.QACommandKinds); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", id, err)
		}
		if err := normalizeComponentValidationCommands(&config); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", id, err)
		}
		if len(config.AcceptanceChecks) > 6 {
			return nil, fmt.Errorf("acceptance_checks may contain at most six checks")
		}
		for _, check := range config.AcceptanceChecks {
			if strings.TrimSpace(check.Criterion) == "" {
				return nil, fmt.Errorf("acceptance check needs an exact criterion")
			}
			if err := validateCommandList("acceptance_checks", [][]string{check.Command}); err != nil {
				return nil, err
			}
		}
		if err := validateCommandList("qa_commands", config.QACommands); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", id, err)
		}
		if err := validateReadOnlyFixturePaths(config.ReadOnlyFixturePaths); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", id, err)
		}
		if err := validateArtifactPatterns(config.QAArtifactPatterns); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", id, err)
		}
		if err := validateScreenshotCommand(config.QAScreenshotCommand); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", id, err)
		}
		if err := validateSemanticQACommand(config.QASemanticCommand); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", id, err)
		}
		if err := validateWorkspaceSandbox(&config); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", id, err)
		}
		result[id] = Workspace{ID: id, Root: root, Config: config}
	}
	return result, nil
}

func normalizeComponentValidationCommands(config *WorkspaceConfig) error {
	if len(config.ComponentValidationCommands) > 32 {
		return fmt.Errorf("component_validation_commands may contain at most 32 components")
	}
	normalized := make(map[string][][]string, len(config.ComponentValidationCommands))
	for rawRoot, commands := range config.ComponentValidationCommands {
		root := normalizeWorkspaceComponentRoot(rawRoot)
		if root == "" {
			return fmt.Errorf("component_validation_commands contains an unsafe component root")
		}
		if _, duplicate := normalized[root]; duplicate {
			return fmt.Errorf("component_validation_commands repeats component root %s", root)
		}
		if err := validateCommandList("component_validation_commands", commands); err != nil {
			return err
		}
		normalized[root] = append([][]string(nil), commands...)
	}
	config.ComponentValidationCommands = normalized
	return nil
}

func normalizeWorkspaceComponentRoot(raw string) string {
	root := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if strings.HasPrefix(root, "/") || workspaceAbsolutePathPattern.MatchString(root) {
		return ""
	}
	root = strings.Trim(root, " /")
	if root == "" || root == "." || root == ".." || strings.HasPrefix(root, ".git") || strings.HasPrefix(root, ".env") || strings.Contains(root, "../") || strings.ContainsAny(root, "\x00\r\n") {
		return ""
	}
	return root
}

// ValidationCommandsForScopes returns only configured component checks whose
// roots overlap an approved plan scope. Empty scopes intentionally return no
// component-specific commands: the legacy repository-wide validation suite is
// still authoritative for whole-repository work.
func (workspace Workspace) ValidationCommandsForScopes(scopes []string) []WorkspaceValidationCommand {
	if len(scopes) == 0 || len(workspace.Config.ComponentValidationCommands) == 0 {
		return nil
	}
	roots := make([]string, 0, len(workspace.Config.ComponentValidationCommands))
	for rawRoot := range workspace.Config.ComponentValidationCommands {
		if root := normalizeWorkspaceComponentRoot(rawRoot); root != "" {
			roots = append(roots, root)
		}
	}
	sort.Strings(roots)
	result := make([]WorkspaceValidationCommand, 0)
	seen := map[string]struct{}{}
	for _, root := range roots {
		matched := false
		for _, rawScope := range scopes {
			scope := normalizeWorkspaceComponentRoot(rawScope)
			if scope == "" {
				continue
			}
			if workspaceScopeWithin(root, scope) || workspaceScopeWithin(scope, root) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		commands := workspace.Config.ComponentValidationCommands[root]
		if commands == nil {
			for rawRoot, configured := range workspace.Config.ComponentValidationCommands {
				if normalizeWorkspaceComponentRoot(rawRoot) == root {
					commands = configured
					break
				}
			}
		}
		for _, command := range commands {
			keyBytes, _ := json.Marshal(command)
			key := root + "\x00" + string(keyBytes)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, WorkspaceValidationCommand{Scope: root, Command: append([]string(nil), command...)})
		}
	}
	return result
}

func workspaceScopeWithin(root, candidate string) bool {
	root = strings.Trim(strings.ReplaceAll(root, "\\", "/"), " /")
	candidate = strings.Trim(strings.ReplaceAll(candidate, "\\", "/"), " /")
	return root != "" && candidate != "" && (candidate == root || strings.HasPrefix(candidate, root+"/"))
}

var sandboxImagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9./:_@-]{0,255}$`)
var sandboxImageDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var sandboxCPUPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)
var sandboxMemoryPattern = regexp.MustCompile(`^[0-9]+[KkMmGgTt]?$`)
var workspaceAbsolutePathPattern = regexp.MustCompile(`^[A-Za-z]:/`)

func validateWorkspaceSandbox(config *WorkspaceConfig) error {
	runtime := strings.ToLower(strings.TrimSpace(config.SandboxRuntime))
	if runtime == "" {
		runtime = WorkspaceSandboxProcess
	}
	if runtime != WorkspaceSandboxProcess && runtime != WorkspaceSandboxDocker && runtime != WorkspaceSandboxFirecracker {
		return fmt.Errorf("sandbox_runtime must be process, docker, or firecracker")
	}
	config.SandboxRuntime = runtime
	if runtime != WorkspaceSandboxDocker {
		if config.RequireSandbox {
			if runtime != WorkspaceSandboxFirecracker {
				return fmt.Errorf("require_sandbox requires sandbox_runtime=docker or firecracker")
			}
		}
		if runtime == WorkspaceSandboxFirecracker {
			if err := validateSandboxSupervisorCommand(config.SandboxSupervisorCommand); err != nil {
				return err
			}
			config.SandboxSupervisorCommand = append([]string(nil), config.SandboxSupervisorCommand...)
			config.SandboxNetwork = "isolated"
			return nil
		}
		// Process mode is explicitly honest: it inherits the worker network and
		// therefore is not a network sandbox. Resource values are ignored.
		config.SandboxNetwork = "inherited"
		return nil
	}
	if strings.TrimSpace(config.SandboxNetwork) == "" {
		config.SandboxNetwork = "none"
	}
	config.SandboxNetwork = strings.ToLower(strings.TrimSpace(config.SandboxNetwork))
	if config.SandboxNetwork != "none" && config.SandboxNetwork != "bridge" {
		return fmt.Errorf("sandbox_network must be none or bridge")
	}
	if !sandboxImagePattern.MatchString(strings.TrimSpace(config.SandboxImage)) {
		return fmt.Errorf("docker sandbox requires a valid sandbox_image")
	}
	config.SandboxImage = strings.TrimSpace(config.SandboxImage)
	config.SandboxImageDigest = strings.ToLower(strings.TrimSpace(config.SandboxImageDigest))
	if config.SandboxImageDigest != "" && !sandboxImageDigestPattern.MatchString(config.SandboxImageDigest) {
		return fmt.Errorf("sandbox_image_digest must be a sha256 digest")
	}
	if strings.TrimSpace(config.SandboxCPUs) == "" {
		config.SandboxCPUs = "2"
	}
	if !sandboxCPUPattern.MatchString(strings.TrimSpace(config.SandboxCPUs)) {
		return fmt.Errorf("sandbox_cpus must be a positive Docker CPU value")
	}
	config.SandboxCPUs = strings.TrimSpace(config.SandboxCPUs)
	if strings.TrimSpace(config.SandboxMemory) == "" {
		config.SandboxMemory = "2g"
	}
	if !sandboxMemoryPattern.MatchString(strings.TrimSpace(config.SandboxMemory)) {
		return fmt.Errorf("sandbox_memory must be a Docker memory value")
	}
	config.SandboxMemory = strings.TrimSpace(config.SandboxMemory)
	if config.SandboxPIDsLimit == 0 {
		config.SandboxPIDsLimit = 256
	}
	if config.SandboxPIDsLimit < 32 || config.SandboxPIDsLimit > 4096 {
		return fmt.Errorf("sandbox_pids_limit must be between 32 and 4096")
	}
	return nil
}

func validateSandboxSupervisorCommand(command []string) error {
	if len(command) == 0 || len(command) > 16 {
		return fmt.Errorf("firecracker sandbox requires one supervisor command with at most sixteen elements")
	}
	for index, part := range command {
		part = strings.TrimSpace(part)
		if part == "" || strings.ContainsAny(part, "\x00\r\n") || strings.ContainsAny(part, ";&|<>`$(){}") {
			return fmt.Errorf("sandbox_supervisor_command contains an unsafe argument")
		}
		if workspaceSensitiveCommandArgument.MatchString(part) {
			return fmt.Errorf("sandbox_supervisor_command must not contain secret-shaped arguments")
		}
		if index == 0 && (part == "." || part == ".." || strings.HasPrefix(part, "-")) {
			return fmt.Errorf("sandbox_supervisor_command executable is invalid")
		}
	}
	return nil
}

var gitBranchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,126}$`)

func validateWorkspaceBase(repositoryURL, baseBranch string) error {
	if strings.ContainsAny(repositoryURL, "\x00\r\n") {
		return fmt.Errorf("repository_url is invalid")
	}
	if !gitBranchName.MatchString(baseBranch) || strings.Contains(baseBranch, "..") || strings.HasSuffix(baseBranch, "/") {
		return fmt.Errorf("base_branch is invalid")
	}
	return nil
}

func validateWorkspaceCapabilities(capabilities []string) error {
	if len(capabilities) > len(workspaceCapabilities) {
		return fmt.Errorf("capabilities may contain at most %d entries", len(workspaceCapabilities))
	}
	seen := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		capability = strings.TrimSpace(capability)
		if _, allowed := workspaceCapabilities[capability]; !allowed {
			return fmt.Errorf("capability %q is not allowed", capability)
		}
		if _, duplicate := seen[capability]; duplicate {
			return fmt.Errorf("capability %q is duplicated", capability)
		}
		seen[capability] = struct{}{}
	}
	if _, canRead := seen[WorkspaceCapabilityReadRepository]; !canRead {
		return fmt.Errorf("repository:read is required")
	}
	// Publishing is deliberately a chain, not a set of independent toggles.
	// A workspace with an incomplete chain could never exercise those rights
	// safely, and would be easy to misread in the Delivery UI. A live human
	// grant still narrows every individual publication further.
	if _, createsPR := seen[WorkspaceCapabilityCreatePullReq]; createsPR {
		if _, publishesBranch := seen[WorkspaceCapabilityPublishBranch]; !publishesBranch {
			return fmt.Errorf("pull_request:create requires branch:publish")
		}
	}
	if _, publishesBranch := seen[WorkspaceCapabilityPublishBranch]; publishesBranch {
		if _, stagesCommit := seen[WorkspaceCapabilityStageCommit]; !stagesCommit {
			return fmt.Errorf("branch:publish requires commit:stage")
		}
	}
	return nil
}

func RegisteredWorkspace(reference string, lookup func(string) string) (Workspace, error) {
	if !strings.HasPrefix(reference, "workspace://") {
		return Workspace{}, fmt.Errorf("repository context must use a workspace:// reference")
	}
	id := strings.Trim(strings.TrimPrefix(reference, "workspace://"), "/ ")
	if id == "" || strings.ContainsAny(id, "#/\\") {
		return Workspace{}, fmt.Errorf("workspace reference is invalid")
	}
	workspaces, err := LoadWorkspaces(lookup("ITBEM_AI_WORKSPACES_JSON"))
	if err != nil {
		return Workspace{}, err
	}
	workspace, ok := workspaces[id]
	if !ok {
		return Workspace{}, fmt.Errorf("workspace is not registered locally: %s", id)
	}
	return workspace, nil
}

func validateCommandList(name string, commands [][]string) error {
	if len(commands) > 6 {
		return fmt.Errorf("%s may contain at most six command arrays", name)
	}
	allowed := map[string]bool{
		"npm": true, "npx": true, "go": true, "python": true, "pytest": true, "cargo": true,
		// These are deterministic, operator-owned security gates used by the
		// registered Go and TypeScript workspaces. They remain command-array
		// only; shell interpreters are intentionally not allowed.
		"gitleaks": true, "govulncheck": true,
	}
	for _, command := range commands {
		if len(command) == 0 || !allowed[command[0]] {
			return fmt.Errorf("%s must use approved non-empty command arrays", name)
		}
		for _, part := range command {
			if strings.TrimSpace(part) == "" || strings.ContainsAny(part, "\x00\r\n") {
				return fmt.Errorf("%s contains an invalid command argument", name)
			}
			if workspaceSensitiveCommandArgument.MatchString(strings.TrimSpace(part)) {
				return fmt.Errorf("%s must not contain secret-shaped command arguments", name)
			}
		}
	}
	return nil
}

func validateArtifactPatterns(patterns []string) error {
	if len(patterns) > 12 {
		return fmt.Errorf("qa_artifact_patterns may contain at most twelve patterns")
	}
	for _, pattern := range patterns {
		clean := filepath.ToSlash(strings.TrimSpace(pattern))
		if clean == "" || strings.HasPrefix(clean, "/") || strings.Contains(clean, "../") || clean == ".." {
			return fmt.Errorf("qa_artifact_patterns must be safe relative patterns")
		}
	}
	return nil
}

func validateScreenshotCommand(command []string) error {
	if len(command) == 0 {
		return nil
	}
	if len(command) > 24 || (command[0] != "npx" && command[0] != "node" && command[0] != "go") {
		return fmt.Errorf("qa_screenshot_command must be an approved command array")
	}
	urls, paths := 0, 0
	for _, part := range command {
		if strings.TrimSpace(part) == "" || strings.ContainsAny(part, "\x00\r\n") {
			return fmt.Errorf("qa_screenshot_command contains an invalid argument")
		}
		if part == "{preview_url}" {
			urls++
		}
		if part == "{artifact_path}" {
			paths++
		}
	}
	if urls != 1 || paths != 1 {
		return fmt.Errorf("qa_screenshot_command requires exactly one {preview_url} and {artifact_path}")
	}
	return nil
}

// validateSemanticQACommand is deliberately separate from screenshots: its
// JSON report is review evidence and must not become a generic shell escape
// hatch. The command has the same narrow substitution boundary as the visual
// capture command and is limited to runtimes that can execute a pinned local
// Stagehand entrypoint.
func validateSemanticQACommand(command []string) error {
	if len(command) == 0 {
		return nil
	}
	if len(command) > 24 || !approvedSemanticRuntime(command[0]) {
		return fmt.Errorf("qa_semantic_command must be an approved command array")
	}
	urls, paths, plans := 0, 0, 0
	for _, part := range command {
		if strings.TrimSpace(part) == "" || strings.ContainsAny(part, "\x00\r\n") {
			return fmt.Errorf("qa_semantic_command contains an invalid argument")
		}
		if workspaceSensitiveCommandArgument.MatchString(strings.TrimSpace(part)) {
			return fmt.Errorf("qa_semantic_command must not contain secret-shaped command arguments")
		}
		if part == "{preview_url}" {
			urls++
		}
		if part == "{artifact_path}" {
			paths++
		}
		if part == "{qa_plan_path}" {
			plans++
		}
	}
	if urls != 1 || paths != 1 || plans > 1 {
		return fmt.Errorf("qa_semantic_command requires exactly one {preview_url}, {artifact_path}, and at most one {qa_plan_path}")
	}
	return nil
}

// approvedSemanticRuntime accepts the standard managed runtimes and an
// absolute Node executable. The latter is needed only in local development
// when the system Node is below Stagehand's supported version; it remains an
// operator-owned config value rather than a model-controlled command.
func approvedSemanticRuntime(command string) bool {
	if command == "npx" || command == "node" || command == "go" {
		return true
	}
	// Workspace configuration is shared by Windows and WSL workers, so check
	// POSIX-rooted and Windows-rooted paths independently of the host OS.
	normalized := strings.ReplaceAll(command, `\`, "/")
	absPath := filepath.IsAbs(command) || strings.HasPrefix(command, "/") ||
		workspaceAbsolutePathPattern.MatchString(normalized) || strings.HasPrefix(command, `\\`)
	if !absPath || strings.HasSuffix(command, "/") || strings.HasSuffix(command, `\`) {
		return false
	}
	base := filepath.Base(normalized)
	return base == "node" || base == "node.exe"
}

type WorkspaceContext struct {
	WorkspaceID        string                `json:"workspace_id"`
	Capabilities       []string              `json:"capabilities"`
	Harness            WorkspaceHarness      `json:"harness"`
	Architecture       WorkspaceArchitecture `json:"architecture"`
	FileCount          int                   `json:"file_count"`
	InventoryTruncated bool                  `json:"inventory_truncated"`
	RedactedValues     int                   `json:"redacted_values"`
	Files              []string              `json:"files"`
	Excerpts           []WorkspaceExcerpt    `json:"excerpts"`
	Excluded           string                `json:"excluded"`
	Git                WorkspaceGitState     `json:"git"`
}

// WorkspaceArchitecture is a bounded, evidence-based orientation map. Every
// item is inferred from an already safe inventory path; it never parses
// dependency manifests, runs package managers or makes architectural claims
// that are not observable from the registered immutable checkout.
type WorkspaceArchitecture struct {
	RuntimeHints       []string `json:"runtime_hints"`
	EntrypointPaths    []string `json:"entrypoint_paths"`
	TestRoots          []string `json:"test_roots"`
	DocumentationPaths []string `json:"documentation_paths"`
}

// WorkspaceHarness is the safe, capability-level view of the local test
// harness provided to the model. It intentionally exposes counts and evidence
// support, never the configured command arguments: environment-owned command
// configuration can contain internal paths or credentials and is executed by
// the deterministic QA/validation runner rather than the model.
type WorkspaceHarness struct {
	ValidationCommandCount   int    `json:"validation_command_count"`
	ComponentValidationCount int    `json:"component_validation_count"`
	QACommandCount           int    `json:"qa_command_count"`
	ArtifactCollection       bool   `json:"artifact_collection"`
	ScreenshotMode           string `json:"screenshot_mode"`
	SemanticQAMode           string `json:"semantic_qa_mode"`
	SandboxMode              string `json:"sandbox_mode"`
	SandboxNetwork           string `json:"sandbox_network"`
	SandboxResourcePolicy    string `json:"sandbox_resource_policy"`
}

// RemoteRepositoryContext distinguishes a frozen GitHub checkpoint from a
// locally registered workspace. The planner may use the revision, role and
// dependency identity to reason about a composed product, but must not claim
// it inspected source code that is not present on this worker.
type RemoteRepositoryContext struct {
	Name          string `json:"name"`
	Reference     string `json:"reference"`
	Revision      string `json:"revision"`
	Role          string `json:"role"`
	CodeAvailable bool   `json:"code_available"`
	Access        string `json:"access"`
}

// WorkspaceGitState is intentionally metadata-only. It gives the planner a
// reproducible freshness signal without exposing the origin URL, credentials,
// remote refs, or a diff from the local workspace.
type WorkspaceGitState struct {
	Available       bool   `json:"available"`
	HeadSHA         string `json:"head_sha,omitempty"`
	Branch          string `json:"branch,omitempty"`
	HasLocalChanges bool   `json:"has_local_changes"`
	// LocalChangeCount is a non-sensitive readiness signal only. It never
	// includes file names, status codes, paths or diff content.
	LocalChangeCount int    `json:"local_change_count,omitempty"`
	GitHubRepository string `json:"github_repository,omitempty"`
	// TrackingBranch and the counts are derived from existing local refs only;
	// collecting context never contacts a remote or pulls into a user workspace.
	TrackingBranch string `json:"tracking_branch,omitempty"`
	LocalAhead     int    `json:"local_ahead,omitempty"`
	RemoteAhead    int    `json:"remote_ahead,omitempty"`
}

type WorkspaceExcerpt struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

var (
	workspaceSensitiveJSONValue  = regexp.MustCompile(`(?im)("` + sensitiveWorkspaceKey + `"\s*:\s*")[^"\r\n]*(")`)
	workspaceSensitiveAssignment = regexp.MustCompile(`(?im)(\b` + sensitiveWorkspaceKey + `\b\s*[:=]\s*)["']?[^\s"'\r\n]+`)
	workspaceBearerCredential    = regexp.MustCompile(`(?im)(\bauthorization\s*:\s*bearer\s+)[^\s\r\n]+`)
	workspaceURLCredential       = regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.-]*://[^\s:@/]+:)[^\s@/]+(@)`)
	workspacePEMBlock            = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	workspaceGitHubToken         = regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{20,}\b|\bgithub_pat_[A-Za-z0-9_]{20,}\b`)
	workspaceAWSAccessKey        = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)
	workspaceSlackToken          = regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)
	// A workspace harness must never use command arguments as a secret
	// transport. The runner config is operator-owned, but rejecting common
	// secret-shaped flags here prevents accidental persistence and makes the
	// capability-only model context a defense in depth measure.
	workspaceSensitiveCommandArgument = regexp.MustCompile(`(?i)^(?:--?[^=\s]*(?:api[_-]?key|access[_-]?key|client[_-]?secret|private[_-]?key|password|secret|token|authorization)[^=\s]*)(?:=|$)|^(?:[A-Za-z0-9_-]*(?:api[_-]?key|access[_-]?key|client[_-]?secret|private[_-]?key|password|secret|token|authorization)[A-Za-z0-9_-]*)=`)
)

// redactWorkspaceExcerpt keeps useful source structure available to planning
// while removing values that are unsafe to send to an external model. Filename
// filtering remains the first defense; this protects the harder case where a
// secret is accidentally embedded in an otherwise legitimate source or doc.
func redactWorkspaceExcerpt(content string) (string, int) {
	redactions := 0
	replace := func(pattern *regexp.Regexp, value string, replacement func([]string) string) string {
		return pattern.ReplaceAllStringFunc(value, func(match string) string {
			redactions++
			return replacement(pattern.FindStringSubmatch(match))
		})
	}
	content = replace(workspacePEMBlock, content, func(_ []string) string { return "<redacted private key>" })
	content = replace(workspaceSensitiveJSONValue, content, func(parts []string) string { return parts[1] + "<redacted>" + parts[2] })
	content = replace(workspaceBearerCredential, content, func(parts []string) string { return parts[1] + "<redacted>" })
	content = replace(workspaceSensitiveAssignment, content, func(parts []string) string { return parts[1] + "<redacted>" })
	for _, pattern := range []*regexp.Regexp{workspaceURLCredential, workspaceGitHubToken, workspaceAWSAccessKey, workspaceSlackToken} {
		content = replace(pattern, content, func(parts []string) string {
			if len(parts) == 3 { // URL credentials retain a valid structural delimiter.
				return parts[1] + "<redacted>" + parts[2]
			}
			return "<redacted>"
		})
	}
	return content, redactions
}

// RedactSourceExcerpt is the common final redaction boundary for source text
// that can leave ITBEM for provider inference. Local workspaces and bounded
// GitHub source context intentionally use the same implementation.
func RedactSourceExcerpt(content string) (string, int) {
	return redactWorkspaceExcerpt(content)
}

func DescribeWorkspace(workspace Workspace, scope []string) (WorkspaceContext, error) {
	return describeWorkspace(workspace, scope, nil)
}

// describeWorkspace keeps the planner grounded in a small, explicit subset of
// a repository. Exact human scope wins, then task-derived domain terms, then
// the repository's architectural documents. This avoids both sending the
// entire tree and making a plan from documentation alone when relevant source
// code is safely available.
func describeWorkspace(workspace Workspace, scope, focus []string) (WorkspaceContext, error) {
	files := make([]string, 0)
	truncated := false
	err := filepath.WalkDir(workspace.Root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		relative, err := filepath.Rel(workspace.Root, path)
		if err != nil || relative == "." {
			return nil
		}
		if entry.IsDir() {
			if excludedDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= maxWorkspaceFiles {
			truncated = true
			return nil
		}
		if entry.Type().IsRegular() && safeContextFile(relative) {
			files = append(files, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		return WorkspaceContext{}, fmt.Errorf("read registered workspace: %w", err)
	}
	sort.Slice(files, func(left, right int) bool {
		leftPriority := contextFilePriority(files[left], scope, focus)
		rightPriority := contextFilePriority(files[right], scope, focus)
		if leftPriority == rightPriority {
			return files[left] < files[right]
		}
		return leftPriority < rightPriority
	})
	remaining := maxWorkspaceExcerptChars
	excerpts := make([]WorkspaceExcerpt, 0)
	redactedValues := 0
	for _, relative := range files {
		if remaining == 0 || len(excerpts) >= maxWorkspaceExcerpts || contextFilePriority(relative, scope, focus) > 2 {
			continue
		}
		path := filepath.Join(workspace.Root, filepath.FromSlash(relative))
		info, err := os.Stat(path)
		if err != nil || info.Size() > maxWorkspaceFileBytes {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil || !validText(content) {
			continue
		}
		limit := min(maxWorkspaceExcerptBytes, remaining)
		if len(content) > limit {
			content = content[:limit]
		}
		sanitized, redactions := redactWorkspaceExcerpt(string(content))
		redactedValues += redactions
		excerpts = append(excerpts, WorkspaceExcerpt{Path: relative, Content: sanitized})
		remaining -= len(content)
	}
	return WorkspaceContext{WorkspaceID: workspace.ID, Capabilities: append([]string(nil), workspace.Config.Capabilities...), Harness: workspaceHarness(workspace.Config), Architecture: workspaceArchitecture(files), FileCount: len(files), InventoryTruncated: truncated, RedactedValues: redactedValues, Files: files, Excerpts: excerpts, Excluded: "secrets, .env files, VCS metadata, dependencies, build artifacts, oversized or binary files, and sensitive values embedded in eligible files", Git: workspaceGitState(workspace.Root)}, nil
}

func workspaceArchitecture(files []string) WorkspaceArchitecture {
	runtimes := map[string]struct{}{}
	entrypoints := make([]string, 0, 8)
	testRoots := map[string]struct{}{}
	documentation := make([]string, 0, 6)
	for _, file := range files {
		lower := strings.ToLower(filepath.ToSlash(file))
		base := filepath.Base(lower)
		switch base {
		case "go.mod":
			runtimes["go"] = struct{}{}
		case "cargo.toml":
			runtimes["rust"] = struct{}{}
		case "package.json":
			runtimes["node"] = struct{}{}
		case "pyproject.toml", "requirements.txt":
			runtimes["python"] = struct{}{}
		case "pom.xml", "build.gradle", "build.gradle.kts":
			runtimes["jvm"] = struct{}{}
		}
		if (strings.HasPrefix(lower, "cmd/") && strings.HasSuffix(lower, "/main.go")) ||
			(strings.HasPrefix(lower, "src/") && (base == "main.rs" || base == "main.ts" || base == "main.tsx" || base == "index.ts")) ||
			(base == "handler.ts" || base == "handler.js" || base == "lambda_function.py") {
			entrypoints = appendBoundedUnique(entrypoints, file, 12)
		}
		if base == "playwright.config.ts" || base == "playwright.config.js" {
			testRoots["playwright"] = struct{}{}
		}
		for _, marker := range []string{"/tests/", "/test/", "/e2e/", "/__tests__/"} {
			if strings.Contains("/"+lower, marker) {
				testRoots[strings.TrimSuffix(marker, "/")[1:]] = struct{}{}
				break
			}
		}
		if base == "readme.md" || base == "architecture.md" || base == "code_index.md" || strings.HasPrefix(lower, "docs/") {
			documentation = appendBoundedUnique(documentation, file, 12)
		}
	}
	sort.Strings(entrypoints)
	sort.Strings(documentation)
	return WorkspaceArchitecture{
		RuntimeHints:       sortedWorkspaceSignals(runtimes),
		EntrypointPaths:    entrypoints,
		TestRoots:          sortedWorkspaceSignals(testRoots),
		DocumentationPaths: documentation,
	}
}

func appendBoundedUnique(values []string, value string, limit int) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	if len(values) < limit {
		return append(values, value)
	}
	return values
}

func sortedWorkspaceSignals(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func workspaceHarness(config WorkspaceConfig) WorkspaceHarness {
	screenshotMode := "responsive_default"
	if len(config.QAScreenshotCommand) > 0 {
		screenshotMode = "configured_command"
	}
	semanticQAMode := "disabled"
	if len(config.QASemanticCommand) > 0 {
		semanticQAMode = "configured_command"
	}
	sandboxMode := strings.ToLower(strings.TrimSpace(config.SandboxRuntime))
	if sandboxMode == "" {
		sandboxMode = WorkspaceSandboxProcess
	}
	resourcePolicy := "process-group-timeout-output-bounded"
	switch sandboxMode {
	case WorkspaceSandboxDocker:
		resourcePolicy = fmt.Sprintf("docker:cpus=%s,memory=%s,pids=%d", config.SandboxCPUs, config.SandboxMemory, config.SandboxPIDsLimit)
	case WorkspaceSandboxFirecracker:
		resourcePolicy = "firecracker:operator-supervisor-per-task"
	}
	return WorkspaceHarness{
		ValidationCommandCount:   len(config.ValidationCommands),
		ComponentValidationCount: componentValidationCommandCount(config.ComponentValidationCommands),
		QACommandCount:           len(config.QACommands),
		ArtifactCollection:       len(config.QAArtifactPatterns) > 0,
		ScreenshotMode:           screenshotMode,
		SemanticQAMode:           semanticQAMode,
		SandboxMode:              sandboxMode,
		SandboxNetwork:           config.SandboxNetwork,
		SandboxResourcePolicy:    resourcePolicy,
	}
}

func componentValidationCommandCount(commands map[string][][]string) int {
	total := 0
	for _, component := range commands {
		total += len(component)
	}
	return total
}

func contextFilePriority(relative string, scope, focus []string) int {
	if scopeMatches(relative, scope) {
		return 0
	}
	if scopeMatches(relative, focus) {
		return 1
	}
	if preferredContextFile(relative) {
		return 2
	}
	return 3
}

func workspaceGitState(root string) WorkspaceGitState {
	ctx := context.Background()
	inside, err := runLocal(ctx, root, 15*time.Second, "", "git", "rev-parse", "--is-inside-work-tree")
	if err != nil || inside.ExitCode != 0 || strings.TrimSpace(inside.Output) != "true" {
		return WorkspaceGitState{}
	}
	head, err := runLocal(ctx, root, 15*time.Second, "", "git", "rev-parse", "HEAD")
	if err != nil || head.ExitCode != 0 || !gitCommitPattern.MatchString(strings.ToLower(strings.TrimSpace(head.Output))) {
		return WorkspaceGitState{}
	}
	branch, _ := runLocal(ctx, root, 15*time.Second, "", "git", "branch", "--show-current")
	status, _ := runLocal(ctx, root, 15*time.Second, "", "git", "status", "--porcelain", "--untracked-files=all")
	state := WorkspaceGitState{Available: true, HeadSHA: strings.ToLower(strings.TrimSpace(head.Output)), Branch: strings.TrimSpace(branch.Output), LocalChangeCount: workspaceChangeCount(status.Output)}
	state.HasLocalChanges = state.LocalChangeCount > 0
	if origin, originErr := runLocal(ctx, root, 15*time.Second, "", "git", "remote", "get-url", "origin"); originErr == nil && origin.ExitCode == 0 {
		if repository, parseErr := parseGitHubRemote(origin.Output); parseErr == nil {
			state.GitHubRepository = repository.Owner + "/" + repository.Name
		}
	}
	if upstream, upstreamErr := runLocal(ctx, root, 15*time.Second, "", "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); upstreamErr == nil && upstream.ExitCode == 0 {
		state.TrackingBranch = strings.TrimSpace(upstream.Output)
		if counts, countErr := runLocal(ctx, root, 15*time.Second, "", "git", "rev-list", "--left-right", "--count", "HEAD...@{upstream}"); countErr == nil && counts.ExitCode == 0 {
			state.LocalAhead, state.RemoteAhead = parseGitAheadBehind(counts.Output)
		}
	}
	return state
}

func workspaceChangeCount(status string) int {
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		trimmed := strings.TrimSpace(line)
		// Task worktrees are intentionally stored beneath the dedicated base
		// checkout. Git reports their directory as untracked on some versions;
		// that is runtime state, not a developer change to the base revision.
		// No other path is exempt from the dirty-check gate.
		if strings.Contains(trimmed, ".itbem-agent-worktrees/") {
			continue
		}
		if trimmed != "" {
			count++
		}
	}
	return count
}

func parseGitAheadBehind(raw string) (localAhead, remoteAhead int) {
	fields := strings.Fields(raw)
	if len(fields) != 2 {
		return 0, 0
	}
	if _, err := fmt.Sscan(fields[0], &localAhead); err != nil || localAhead < 0 {
		return 0, 0
	}
	if _, err := fmt.Sscan(fields[1], &remoteAhead); err != nil || remoteAhead < 0 {
		return 0, 0
	}
	return localAhead, remoteAhead
}

// ReadWorkspaceGitState exposes only the local repository checkpoint needed
// by the delivery control plane when a registered workspace is attached to a
// project. It never contacts a remote, fetches, pulls, or returns a diff.
func ReadWorkspaceGitState(workspace Workspace) WorkspaceGitState {
	return workspaceGitState(workspace.Root)
}

// FetchWorkspaceRemote refreshes only origin's remote refs for a registered
// workspace after an explicit human action. It does not use pull, change HEAD,
// apply a merge/rebase, create a worktree or make a commit. Credentials remain
// outside the process environment and interactive prompts are disabled.
func FetchWorkspaceRemote(ctx context.Context, workspace Workspace) (WorkspaceGitState, error) {
	if err := workspace.RequireCapability(WorkspaceCapabilityFetchRemote); err != nil {
		return WorkspaceGitState{}, err
	}
	state := workspaceGitState(workspace.Root)
	if !state.Available || strings.TrimSpace(state.HeadSHA) == "" {
		return WorkspaceGitState{}, fmt.Errorf("workspace is not a readable Git repository")
	}
	origin, err := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "remote", "get-url", "origin")
	if err != nil || origin.ExitCode != 0 || strings.TrimSpace(origin.Output) == "" {
		return WorkspaceGitState{}, fmt.Errorf("workspace has no readable origin remote")
	}
	fetched, err := runLocalWithEnv(ctx, workspace.Root, 60*time.Second, "", map[string]string{"GIT_TERMINAL_PROMPT": "0"}, "git", "fetch", "--prune", "--tags", "--no-recurse-submodules", "origin")
	if err != nil || fetched.ExitCode != 0 {
		return WorkspaceGitState{}, fmt.Errorf("remote fetch could not complete")
	}
	return workspaceGitState(workspace.Root), nil
}

// SyncManagedWorkspace makes an operator-owned checkout ready to become a
// Delivery checkpoint. It is intentionally a maintenance action, not part of
// task execution: a task always uses a revision frozen before planning.
//
// A missing directory is cloned only from repository_url in the local registry.
// An existing directory must be clean; the synchronizer fetches origin, safely
// returns to base_branch and fast-forwards it. It never resets, rebases, merges
// non-fast-forward history, touches an agent worktree, or accepts task input.
func SyncManagedWorkspace(ctx context.Context, workspace Workspace) (WorkspaceGitState, error) {
	return syncManagedWorkspace(ctx, workspace, "", nil)
}

// SyncAuthorizedManagedWorkspace uses only the dedicated contents-read Source
// App for registered GitHub repositories. Non-GitHub local fixtures retain the
// operator-owned legacy fetch path.
func SyncAuthorizedManagedWorkspace(ctx context.Context, workspace Workspace, lookup func(string) string) (WorkspaceGitState, error) {
	repository, remote, required, err := gitHubSourceWorkspaceRemote(workspace)
	if err != nil {
		return WorkspaceGitState{}, err
	}
	if !required {
		return SyncManagedWorkspace(ctx, workspace)
	}
	config, err := LoadGitHubSourceAppConfig(lookup)
	if err != nil {
		return WorkspaceGitState{}, fmt.Errorf("GitHub source App is required for workspace %s: %w", workspace.ID, err)
	}
	return SyncManagedWorkspaceWithGitHubApp(ctx, workspace, repository, remote, config, nil, time.Now().UTC())
}

func SyncManagedWorkspaceWithGitHubApp(ctx context.Context, workspace Workspace, repository githubRepository, remote string, config GitHubAppConfig, client *http.Client, now time.Time) (WorkspaceGitState, error) {
	token, err := MintGitHubRepositoryToken(ctx, config, client, now, repository.Owner+"/"+repository.Name)
	if err != nil {
		return WorkspaceGitState{}, fmt.Errorf("GitHub source App could not authenticate the registered repository")
	}
	environment, cleanup, err := gitHubInstallationTokenEnvironment(token.Token)
	if err != nil {
		return WorkspaceGitState{}, err
	}
	defer cleanup()
	registeredRepository, registeredRemote, required, err := gitHubSourceWorkspaceRemote(workspace)
	if err != nil || !required || !strings.EqualFold(registeredRepository.Owner+"/"+registeredRepository.Name, repository.Owner+"/"+repository.Name) || !sameManagedRemote(registeredRemote, remote) {
		return WorkspaceGitState{}, fmt.Errorf("GitHub source sync target does not match the registered workspace")
	}
	return syncManagedWorkspace(ctx, workspace, remote, environment)
}

func syncManagedWorkspace(ctx context.Context, workspace Workspace, authenticatedRemote string, environment map[string]string) (WorkspaceGitState, error) {
	if err := workspace.RequireCapability(WorkspaceCapabilityFetchRemote); err != nil {
		return WorkspaceGitState{}, err
	}
	remoteURL := strings.TrimSpace(workspace.Config.RepositoryURL)
	if remoteURL == "" {
		return WorkspaceGitState{}, fmt.Errorf("workspace %s has no repository_url for managed synchronization", workspace.ID)
	}
	baseBranch := strings.TrimSpace(workspace.Config.BaseBranch)
	if err := validateWorkspaceBase(remoteURL, baseBranch); err != nil {
		return WorkspaceGitState{}, err
	}
	cloneRemote, fetchRemote := remoteURL, "origin"
	if strings.TrimSpace(authenticatedRemote) != "" {
		cloneRemote, fetchRemote = authenticatedRemote, authenticatedRemote
	}
	if _, err := os.Stat(workspace.Root); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(workspace.Root), 0700); err != nil {
			return WorkspaceGitState{}, fmt.Errorf("prepare managed workspace parent: %w", err)
		}
		cloned, cloneErr := runLocalWithEnv(ctx, filepath.Dir(workspace.Root), 2*time.Minute, "", gitWorkspaceEnvironment(environment), "git", gitWorkspaceCloneArguments(baseBranch, cloneRemote, workspace.Root, gitWorkspaceUsesInstallationToken(environment))...)
		if cloneErr != nil || cloned.ExitCode != 0 {
			return WorkspaceGitState{}, fmt.Errorf("managed workspace clone could not complete")
		}
		if !sameManagedRemote(cloneRemote, remoteURL) {
			bound, bindErr := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "remote", "set-url", "origin", remoteURL)
			if bindErr != nil || bound.ExitCode != 0 {
				return WorkspaceGitState{}, fmt.Errorf("managed workspace could not bind its registered origin")
			}
		}
	} else if err != nil {
		return WorkspaceGitState{}, fmt.Errorf("inspect managed workspace: %w", err)
	}
	if err := ensureManagedWorkspaceRuntimeExclude(ctx, workspace.Root); err != nil {
		return WorkspaceGitState{}, err
	}
	state := workspaceGitState(workspace.Root)
	if !state.Available || strings.TrimSpace(state.HeadSHA) == "" {
		return WorkspaceGitState{}, fmt.Errorf("managed workspace is not a readable Git repository")
	}
	if state.HasLocalChanges {
		return WorkspaceGitState{}, fmt.Errorf("managed workspace has local changes; refusing to switch its base branch")
	}
	origin, err := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "remote", "get-url", "origin")
	if err != nil || origin.ExitCode != 0 || strings.TrimSpace(origin.Output) == "" {
		return WorkspaceGitState{}, fmt.Errorf("managed workspace has no readable origin remote")
	}
	if !sameManagedRemote(origin.Output, remoteURL) {
		return WorkspaceGitState{}, fmt.Errorf("managed workspace origin does not match its registered repository_url")
	}
	fetched, err := runLocalWithEnv(ctx, workspace.Root, 60*time.Second, "", gitWorkspaceEnvironment(environment), "git", gitWorkspaceFetchArguments(fetchRemote, gitWorkspaceUsesInstallationToken(environment))...)
	if err != nil || fetched.ExitCode != 0 {
		return WorkspaceGitState{}, fmt.Errorf("managed workspace remote fetch could not complete")
	}
	remoteBase := "origin/" + baseBranch
	known, err := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "rev-parse", "--verify", "--quiet", remoteBase+"^{commit}")
	if err != nil || known.ExitCode != 0 {
		return WorkspaceGitState{}, fmt.Errorf("managed workspace base branch is unavailable on origin")
	}
	localBranch, err := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "show-ref", "--verify", "--quiet", "refs/heads/"+baseBranch)
	if err != nil {
		return WorkspaceGitState{}, err
	}
	if localBranch.ExitCode == 0 {
		switched, switchErr := runLocal(ctx, workspace.Root, 30*time.Second, "", "git", "switch", baseBranch)
		if switchErr != nil || switched.ExitCode != 0 {
			return WorkspaceGitState{}, fmt.Errorf("managed workspace could not switch to its base branch")
		}
	} else {
		switched, switchErr := runLocal(ctx, workspace.Root, 30*time.Second, "", "git", "switch", "--track", "-c", baseBranch, remoteBase)
		if switchErr != nil || switched.ExitCode != 0 {
			return WorkspaceGitState{}, fmt.Errorf("managed workspace could not create its tracked base branch")
		}
	}
	updated, err := runLocal(ctx, workspace.Root, 45*time.Second, "", "git", "merge", "--ff-only", remoteBase)
	if err != nil || updated.ExitCode != 0 {
		return WorkspaceGitState{}, fmt.Errorf("managed workspace base branch cannot fast-forward; resolve it without rewriting history")
	}
	head, headErr := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "rev-parse", "HEAD")
	remoteHead, remoteErr := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "rev-parse", remoteBase)
	if headErr != nil || remoteErr != nil || head.ExitCode != 0 || remoteHead.ExitCode != 0 || !strings.EqualFold(strings.TrimSpace(head.Output), strings.TrimSpace(remoteHead.Output)) {
		return WorkspaceGitState{}, fmt.Errorf("managed workspace base branch is not identical to fetched origin")
	}
	return workspaceGitState(workspace.Root), nil
}

// sameManagedRemote deliberately accepts only cosmetic trailing slashes. A
// managed checkout may be modified locally, so SyncManagedWorkspace must bind
// its network fetch to the operator-owned repository_url rather than merely
// trusting that a remote named origin exists.
func sameManagedRemote(actual, registered string) bool {
	canonical := func(value string) string {
		return strings.TrimSuffix(strings.TrimSpace(value), "/")
	}
	return canonical(actual) != "" && canonical(actual) == canonical(registered)
}

// WorkspaceDiagnostic is deliberately operational metadata only. It lets an
// operator verify that a local runner can safely serve a project without
// emitting paths, remotes, source excerpts, credentials, or command output.
type WorkspaceDiagnostic struct {
	ID                     string            `json:"id"`
	Ready                  bool              `json:"ready"`
	Issue                  string            `json:"issue,omitempty"`
	DependencyState        string            `json:"dependency_state,omitempty"`
	DependencyReason       string            `json:"dependency_reason,omitempty"`
	DependencyNextAction   string            `json:"dependency_next_action,omitempty"`
	SandboxMode            string            `json:"sandbox_mode"`
	IsolationMode          string            `json:"isolation_mode"`
	SandboxReady           bool              `json:"sandbox_ready"`
	Capabilities           []string          `json:"capabilities"`
	ValidationCommandCount int               `json:"validation_command_count"`
	QACommandCount         int               `json:"qa_command_count"`
	ScreenshotMode         string            `json:"screenshot_mode"`
	SemanticQAMode         string            `json:"semantic_qa_mode"`
	Git                    WorkspaceGitState `json:"git"`
}

// WorkspaceReadiness is the small, continuously reportable projection of a
// workspace diagnostic. It is safe to send with a worker heartbeat: it never
// contains a local path, remote URL, branch, revision, command, source excerpt
// or credential. The dashboard uses it to prevent an operator from confusing
// a live worker with a worker that is actually ready to execute a given kind
// of Delivery work.
type WorkspaceReadiness struct {
	ID    string `json:"id"`
	Ready bool   `json:"ready"`
	// IsolationMode is an honest attestation, not a capability grant. The
	// host_process value means repository commands inherit the worker host and
	// must never be treated as a hostile-code sandbox by the control plane.
	IsolationMode    string `json:"isolation_mode"`
	SandboxReady     bool   `json:"sandbox_ready"`
	QAReady          bool   `json:"qa_ready"`
	VisualQAReady    bool   `json:"visual_qa_ready"`
	PublicationReady bool   `json:"publication_ready"`
	// Dependency fields explain an unavailable local prerequisite. They are
	// observational and never grant a capability or bypass a server gate.
	DependencyState      string `json:"dependency_state,omitempty"`
	DependencyReason     string `json:"dependency_reason,omitempty"`
	DependencyNextAction string `json:"dependency_next_action,omitempty"`
	// SandboxAttestation is observational metadata from an operator-owned
	// supervisor. It never grants readiness by itself; the control plane still
	// enforces the runtime-specific sandbox checks below.
	SandboxAttestation     *SandboxAttestation `json:"sandbox_attestation,omitempty"`
	ValidationCommandCount int                 `json:"validation_command_count"`
	QACommandCount         int                 `json:"qa_command_count"`
}

// SandboxAttestation is deliberately small and credential-free. It records
// what the supervisor actually proved, not what a model requested. A missing
// or invalid value is omitted from the heartbeat rather than treated as a
// successful sandbox. The local Firecracker fixture may use serial_console only
// with evidence_scope=local_task_guest_command; production profiles must use
// virtio_vsock.
type SandboxAttestation struct {
	Runtime              string `json:"runtime"`
	RuntimeVersion       string `json:"runtime_version,omitempty"`
	Transport            string `json:"transport,omitempty"`
	EvidenceScope        string `json:"evidence_scope"`
	GuestCommandVerified bool   `json:"guest_command_verified"`
	EvidenceDigest       string `json:"evidence_digest,omitempty"`
	ToolchainImageSHA256 string `json:"toolchain_image_sha256,omitempty"`
	RegisteredCommand    string `json:"registered_command,omitempty"`
}

// WorkspaceReadinessSnapshot validates only local registry state and returns
// a privacy-preserving readiness projection suitable for a recurring worker
// heartbeat. It performs no provider, AWS or GitHub request.
func WorkspaceReadinessSnapshot(lookup func(string) string) ([]WorkspaceReadiness, error) {
	diagnostics, err := DiagnoseWorkspaces(lookup)
	if err != nil {
		return nil, err
	}
	result := make([]WorkspaceReadiness, 0, len(diagnostics))
	attestation := sandboxAttestation(lookup("ITBEM_AI_SANDBOX_ATTESTATION_JSON"))
	for _, diagnostic := range diagnostics {
		canPublish := capabilityPresent(diagnostic.Capabilities, WorkspaceCapabilityStageCommit) &&
			capabilityPresent(diagnostic.Capabilities, WorkspaceCapabilityPublishBranch) &&
			capabilityPresent(diagnostic.Capabilities, WorkspaceCapabilityCreatePullReq)
		result = append(result, WorkspaceReadiness{
			ID:                     diagnostic.ID,
			Ready:                  diagnostic.Ready,
			IsolationMode:          diagnostic.IsolationMode,
			SandboxReady:           diagnostic.SandboxReady,
			QAReady:                diagnostic.Ready && diagnostic.QACommandCount > 0,
			VisualQAReady:          diagnostic.Ready && diagnostic.SemanticQAMode == "configured_command",
			PublicationReady:       diagnostic.Ready && canPublish,
			DependencyState:        diagnostic.DependencyState,
			DependencyReason:       diagnostic.DependencyReason,
			DependencyNextAction:   diagnostic.DependencyNextAction,
			SandboxAttestation:     attestation,
			ValidationCommandCount: diagnostic.ValidationCommandCount,
			QACommandCount:         diagnostic.QACommandCount,
		})
	}
	return result, nil
}

func sandboxAttestation(raw string) *SandboxAttestation {
	if strings.TrimSpace(raw) == "" || len(raw) > 2048 {
		return nil
	}
	var value SandboxAttestation
	if json.Unmarshal([]byte(raw), &value) != nil {
		return nil
	}
	value.Runtime = strings.ToLower(strings.TrimSpace(value.Runtime))
	value.RuntimeVersion = strings.TrimSpace(value.RuntimeVersion)
	value.Transport = strings.ToLower(strings.TrimSpace(value.Transport))
	value.EvidenceScope = strings.ToLower(strings.TrimSpace(value.EvidenceScope))
	value.EvidenceDigest = strings.TrimSpace(value.EvidenceDigest)
	transportValid := value.Transport == "virtio_vsock" || (value.Transport == "serial_console" && value.EvidenceScope == "local_task_guest_command")
	if value.Runtime != "firecracker" || !transportValid || value.EvidenceScope == "" || !value.GuestCommandVerified {
		return nil
	}
	if len(value.RuntimeVersion) > 64 || len(value.EvidenceScope) > 64 || len(value.EvidenceDigest) > 128 {
		return nil
	}
	if value.ToolchainImageSHA256 != "" || value.RegisteredCommand != "" {
		if value.RegisteredCommand != "go.test.json.offline" || len(value.ToolchainImageSHA256) != 64 || strings.Trim(value.ToolchainImageSHA256, "0123456789abcdef") != "" {
			return nil
		}
	}
	return &value
}

// SandboxAttestationSnapshot exposes the already-validated, observational
// evidence carried by the worker heartbeat. It deliberately does not turn
// Firecracker evidence into a readiness grant: lifecycle ownership,
// worktree binding and task execution still belong to the control plane.
func SandboxAttestationSnapshot(lookup func(string) string) *SandboxAttestation {
	return sandboxAttestation(lookup("ITBEM_AI_SANDBOX_ATTESTATION_JSON"))
}

func capabilityPresent(capabilities []string, capability string) bool {
	for _, configured := range capabilities {
		if configured == capability {
			return true
		}
	}
	return false
}

// DiagnoseWorkspaces validates the configured local registry without making a
// network request or reading workspace source. It is used by the worker's
// doctor command before a human allows an agent to begin a delivery.
func DiagnoseWorkspaces(lookup func(string) string) ([]WorkspaceDiagnostic, error) {
	workspaces, err := LoadWorkspaces(lookup("ITBEM_AI_WORKSPACES_JSON"))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(workspaces))
	for id := range workspaces {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	diagnostics := make([]WorkspaceDiagnostic, 0, len(ids))
	for _, id := range ids {
		workspace := workspaces[id]
		state := ReadWorkspaceGitState(workspace)
		harness := workspace.Harness()
		screenshotMode := "default_responsive"
		if harness.ScreenshotMode == "configured_command" {
			screenshotMode = "configured_command"
		}
		isolationMode := workspaceIsolationMode(workspace)
		dependencyState, dependencyReason, dependencyNextAction := workspaceDependency(workspace)
		diagnostic := WorkspaceDiagnostic{
			ID: id, Ready: state.Available, Capabilities: append([]string(nil), workspace.Config.Capabilities...),
			SandboxMode:     strings.ToLower(strings.TrimSpace(workspace.Config.SandboxRuntime)),
			IsolationMode:   isolationMode,
			DependencyState: dependencyState, DependencyReason: dependencyReason, DependencyNextAction: dependencyNextAction,
			// A host process is an operator-trusted execution mode, not a
			// sandbox. Keep this false so downstream readiness cannot mistake
			// an inherited host boundary for isolation.
			SandboxReady:           isolationMode == "docker_container",
			ValidationCommandCount: len(workspace.Config.ValidationCommands), QACommandCount: len(workspace.Config.QACommands),
			ScreenshotMode: screenshotMode, SemanticQAMode: harness.SemanticQAMode, Git: state,
		}
		if !state.Available {
			diagnostic.Issue = "configured directory is not a readable Git worktree"
		}
		if workspace.Config.RequireSandbox || diagnostic.SandboxMode == WorkspaceSandboxDocker || diagnostic.SandboxMode == WorkspaceSandboxFirecracker {
			diagnostic.SandboxReady = sandboxRuntimeReady(workspace)
			if !diagnostic.SandboxReady {
				diagnostic.Ready = false
				diagnostic.Issue = "configured sandbox runtime is unavailable"
				if diagnostic.DependencyState == "ready" {
					diagnostic.DependencyState = "sandbox_unavailable"
					diagnostic.DependencyReason = diagnostic.Issue
					diagnostic.DependencyNextAction = "Repair the registered sandbox runtime and rerun the worker doctor."
				}
			}
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	return diagnostics, nil
}

func workspaceIsolationMode(workspace Workspace) string {
	if strings.EqualFold(strings.TrimSpace(workspace.Config.SandboxRuntime), WorkspaceSandboxDocker) {
		return "docker_container"
	}
	if strings.EqualFold(strings.TrimSpace(workspace.Config.SandboxRuntime), WorkspaceSandboxFirecracker) {
		return "firecracker_microvm"
	}
	return "host_process"
}

// workspaceDependency turns local preflight failures into an actionable,
// privacy-safe signal. It is explanatory only: a task must still pass the
// runtime-specific sandbox and authorization gates at execution time.
func workspaceDependency(workspace Workspace) (state, reason, nextAction string) {
	runtime := strings.ToLower(strings.TrimSpace(workspace.Config.SandboxRuntime))
	switch runtime {
	case WorkspaceSandboxDocker:
		return "ready", "Docker is the registered local sandbox runtime.", ""
	case WorkspaceSandboxFirecracker:
		command := workspace.Config.SandboxSupervisorCommand
		if !firecrackerSupervisorAvailable(command) {
			return "supervisor_unavailable", "The operator-owned Firecracker supervisor is not available on this worker.", "Register an executable supervisor command and rerun the worker doctor."
		}
		device, err := os.Stat("/dev/kvm")
		if err != nil || device.IsDir() {
			return "kvm_device_missing", "The WSL host does not expose a usable /dev/kvm device.", "Enable KVM for this WSL instance before starting a Firecracker worker."
		}
		file, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
		if err != nil {
			return "kvm_permission_denied", "The current WSL user cannot read/write /dev/kvm.", "In WSL run `sudo usermod -aG kvm $(whoami)`, restart the WSL session, then rerun the Firecracker round-trip."
		}
		_ = file.Close()
		if !firecrackerSupervisorProductionProfile(command) {
			return "production_profile_not_registered", "The Firecracker supervisor is not registered with the production Jailer profile.", "Register the operator-owned supervisor with `--profile production --jailer`; keep the local proof profile out of hostile repository execution."
		}
		if cgroupState, ok := firecrackerCgroupState(); ok && cgroupState == "not_delegated" {
			return "cgroup_delegation_required", "The WSL worker cannot create the delegated Firecracker parent cgroup.", "Delegate a root-owned cgroup with CPU, memory and PID limits to the worker, then rerun the production preflight."
		}
		return "runtime_ready_requires_lifecycle", "Firecracker is reachable and KVM is accessible; task-scoped lifecycle is still verified at execution time.", "Run the control-plane supervisor lifecycle and persist its attestation before claiming microVM readiness."
	default:
		return "host_process_not_sandbox", "This workspace uses a host process and is not a hostile-code sandbox.", "Register Docker or Firecracker for implementation work that requires isolation."
	}
}

// sandboxRuntimeReady probes only the local Docker daemon. It never mounts a
// workspace, runs repository code or contacts a provider. A required sandbox
// is not considered ready merely because its JSON configuration parsed.
func sandboxRuntimeReady(workspace Workspace) bool {
	if strings.ToLower(strings.TrimSpace(workspace.Config.SandboxRuntime)) != WorkspaceSandboxDocker {
		if strings.ToLower(strings.TrimSpace(workspace.Config.SandboxRuntime)) == WorkspaceSandboxFirecracker {
			return firecrackerSupervisorAvailable(workspace.Config.SandboxSupervisorCommand)
		}
		return !workspace.Config.RequireSandbox
	}
	result, err := runLocal(context.Background(), workspace.Root, 5*time.Second, "", "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Output) == "" {
		return false
	}
	image := workspace.Config.SandboxImage
	if digest := strings.TrimSpace(workspace.Config.SandboxImageDigest); digest != "" && !strings.Contains(image, "@") {
		image += "@" + digest
	}
	imageResult, imageErr := runLocal(context.Background(), workspace.Root, 5*time.Second, "", "docker", "image", "inspect", image, "--format", "{{.Id}}")
	return imageErr == nil && imageResult.ExitCode == 0 && strings.TrimSpace(imageResult.Output) != ""
}

func firecrackerSupervisorAvailable(command []string) bool {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return false
	}
	path := strings.TrimSpace(command[0])
	if filepath.IsAbs(path) || workspaceAbsolutePathPattern.MatchString(filepath.ToSlash(path)) {
		info, err := os.Stat(path)
		return err == nil && !info.IsDir()
	}
	// Resolve through the operator's PATH without executing it. The supervisor
	// itself remains responsible for proving a VM was actually created.
	_, err := exec.LookPath(path)
	return err == nil
}

func firecrackerSupervisorProductionProfile(command []string) bool {
	for index := 0; index < len(command); index++ {
		if command[index] == "--profile" && index+1 < len(command) && strings.EqualFold(strings.TrimSpace(command[index+1]), "production") {
			for _, argument := range command[index+2:] {
				if argument == "--jailer" {
					return true
				}
			}
		}
	}
	return false
}

// firecrackerCgroupState is deliberately conservative and read-only. An
// absent cgroup mount means this process is not running on the WSL worker host;
// in that case the operator-owned worker doctor remains the authority.
func firecrackerCgroupState() (string, bool) {
	root := "/sys/fs/cgroup"
	controllers, err := os.ReadFile(filepath.Join(root, "cgroup.controllers"))
	if err != nil {
		return "", false
	}
	for _, required := range []string{"cpu", "memory", "pids"} {
		if !strings.Contains(" "+string(controllers)+" ", " "+required+" ") {
			return "controllers_missing", true
		}
	}
	// The worker may be running below a delegated systemd user scope while the
	// cgroup mount root remains root-owned. Probe the process' actual cgroup
	// parent first; checking only /sys/fs/cgroup would reject the safe local
	// launcher and encourage an unsafe privileged fallback.
	probePath := currentCgroupPath(root)
	if delegatedControllers, readErr := os.ReadFile(filepath.Join(probePath, "cgroup.controllers")); readErr == nil {
		for _, required := range []string{"cpu", "memory", "pids"} {
			if !strings.Contains(" "+string(delegatedControllers)+" ", " "+required+" ") {
				return "controllers_missing", true
			}
		}
	}
	if !isWritableDirectory(probePath) {
		return "not_delegated", true
	}
	return "ready", true
}

func currentCgroupPath(root string) string {
	contents, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return root
	}
	for _, line := range strings.Split(string(contents), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || parts[0] != "0" {
			continue
		}
		relative := strings.TrimPrefix(strings.TrimSpace(parts[2]), "/")
		if relative == "" {
			return root
		}
		candidate := filepath.Join(root, filepath.FromSlash(relative))
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
		return root
	}
	return root
}

func isWritableDirectory(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	// Opening the controller without writing is enough to observe delegation;
	// never create a probe file or mutate cgroup state from a readiness check.
	test, err := os.OpenFile(filepath.Join(path, "cgroup.subtree_control"), os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	_ = test.Close()
	return true
}

func DeliveryWorkspaceContext(delivery json.RawMessage, lookup func(string) string) ([]WorkspaceContext, error) {
	var value struct {
		WorkItem struct {
			Title           string   `json:"title"`
			Description     string   `json:"description"`
			ExpectedOutcome string   `json:"expected_outcome"`
			IncludedScope   []string `json:"included_scope"`
		} `json:"work_item"`
		ContextSources []struct {
			Kind      string `json:"kind"`
			Reference string `json:"reference"`
			Revision  string `json:"revision"`
		} `json:"context_sources"`
	}
	if err := json.Unmarshal(delivery, &value); err != nil {
		return nil, fmt.Errorf("delivery input must be a JSON object")
	}
	result := make([]WorkspaceContext, 0)
	for _, source := range value.ContextSources {
		if source.Kind != "repository" || !strings.HasPrefix(strings.TrimSpace(source.Reference), "workspace://") {
			continue
		}
		workspace, err := RegisteredWorkspace(source.Reference, lookup)
		if err != nil {
			return nil, err
		}
		// A plan must describe the same immutable revision that the later
		// isolated worktree will receive. Reading a dirty base folder would send
		// uncommitted code to the model while implementation starts from HEAD,
		// making the human-approved plan non-reproducible. Never silently carry
		// that difference into a provider call.
		state := ReadWorkspaceGitState(workspace)
		if state.Available && state.HasLocalChanges {
			return nil, fmt.Errorf("workspace %s has local changes; commit, stash, or register an immutable checkpoint before running Delivery", workspace.ID)
		}
		// The work item freezes the exact source revision before any provider
		// call. A local workspace can move after that snapshot (or be registered
		// incorrectly), so reject it rather than giving the model code that no
		// longer matches the human-reviewable Delivery context.
		expectedRevision := strings.ToLower(strings.TrimSpace(source.Revision))
		if state.Available && expectedRevision != "" && !strings.EqualFold(state.HeadSHA, expectedRevision) {
			return nil, fmt.Errorf("workspace %s HEAD no longer matches frozen context revision; refresh the project checkpoint before running Delivery", workspace.ID)
		}
		// This uses only already-known local tracking refs; it does not fetch or
		// contact GitHub. A positive remote-ahead value means a human needs to
		// consciously synchronize and checkpoint the base before the agent
		// builds a worktree from an obsolete revision.
		if state.Available && state.RemoteAhead > 0 {
			return nil, fmt.Errorf("workspace %s is %d known commits behind its tracking branch; synchronize and refresh the checkpoint before running Delivery", workspace.ID, state.RemoteAhead)
		}
		focus := deliveryFocusTerms(value.WorkItem.Title, value.WorkItem.Description, value.WorkItem.ExpectedOutcome)
		context, err := describeWorkspace(workspace, value.WorkItem.IncludedScope, focus)
		if err != nil {
			return nil, err
		}
		result = append(result, context)
	}
	return result, nil
}

// DeliveryRemoteRepositoryContexts returns only the bounded GitHub metadata
// that was frozen by the control plane. It never fetches, clones, reads or
// exposes code from those repositories. A github:// source is therefore
// useful as a dependency checkpoint, but cannot be marked as an
// implementation target until the operator registers a local workspace://
// checkout for it.
func DeliveryRemoteRepositoryContexts(delivery json.RawMessage) ([]RemoteRepositoryContext, error) {
	var value struct {
		ContextSources []struct {
			Reference string         `json:"reference"`
			Metadata  map[string]any `json:"metadata"`
		} `json:"context_sources"`
		RepositoryTopology []struct {
			Name      string `json:"name"`
			Reference string `json:"reference"`
			Revision  string `json:"revision"`
			Role      string `json:"role"`
		} `json:"repository_topology"`
	}
	if err := json.Unmarshal(delivery, &value); err != nil {
		return nil, fmt.Errorf("delivery input must be a JSON object")
	}
	contextModes := make(map[string]string, len(value.ContextSources))
	for _, source := range value.ContextSources {
		reference := strings.TrimSpace(source.Reference)
		if !strings.HasPrefix(strings.ToLower(reference), "github://") {
			continue
		}
		mode, _ := source.Metadata["github_context_mode"].(string)
		contextModes[reference] = strings.TrimSpace(mode)
	}
	result := make([]RemoteRepositoryContext, 0)
	for _, repository := range value.RepositoryTopology {
		reference := strings.TrimSpace(repository.Reference)
		if !strings.HasPrefix(strings.ToLower(reference), "github://") {
			continue
		}
		mode := contextModes[reference]
		codeAvailable := strings.EqualFold(mode, "bounded_source")
		access := "metadata_only"
		if codeAvailable {
			access = "bounded_source_excerpt"
		}
		result = append(result, RemoteRepositoryContext{
			Name: strings.TrimSpace(repository.Name), Reference: reference, Revision: strings.TrimSpace(repository.Revision),
			Role: strings.ToLower(strings.TrimSpace(repository.Role)), CodeAvailable: codeAvailable, Access: access,
		})
	}
	return result, nil
}

// deliveryFocusTerms is deliberately conservative: it uses only meaningful
// words from the work item, never commands, paths supplied by a model, or
// hidden repository content. It is a relevance hint, not an authorization to
// read files outside the safe inventory.
func deliveryFocusTerms(values ...string) []string {
	stopWords := map[string]struct{}{
		"para": {}, "desde": {}, "sobre": {}, "entre": {}, "hasta": {}, "antes": {}, "despues": {},
		"tarea": {}, "entrega": {}, "cambio": {}, "cambios": {}, "revisar": {}, "validar": {},
		"plan": {}, "codigo": {}, "prueba": {}, "pruebas": {}, "sistema": {}, "proyecto": {},
	}
	seen := make(map[string]struct{})
	terms := make([]string, 0, 8)
	for _, value := range values {
		for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
			if len([]rune(token)) < 4 {
				continue
			}
			if _, ignored := stopWords[token]; ignored {
				continue
			}
			if _, exists := seen[token]; exists {
				continue
			}
			seen[token] = struct{}{}
			terms = append(terms, token)
			if len(terms) == 12 {
				return terms
			}
		}
	}
	return terms
}

func excludedDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".next", ".turbo", "node_modules", "dist", "build", "coverage", "vendor", "__pycache__", ".itbem-agent-worktrees", ".itbem-agent-evidence", ".aws", ".ssh", "secrets", "credentials":
		return true
	}
	return false
}
func safeContextFile(relative string) bool {
	lowerPath := strings.ToLower(filepath.ToSlash(relative))
	lower := strings.ToLower(filepath.Base(relative))
	if strings.HasPrefix(lower, ".env") || lower == "id_rsa" || lower == "id_ed25519" {
		return false
	}
	for _, sensitive := range []string{"credential", "secret", "private_key", "api_key", "apikey", "access_key", "token", "password", "service_account"} {
		if strings.Contains(lowerPath, sensitive) {
			return false
		}
	}
	switch strings.ToLower(filepath.Ext(lower)) {
	case ".pem", ".key", ".p12", ".pfx", ".jks", ".keystore":
		return false
	}
	return true
}
func preferredContextFile(relative string) bool {
	switch strings.ToLower(filepath.Base(relative)) {
	case "readme.md", "code_index.md", "architecture.md", "package.json", "go.mod", "pyproject.toml", "cargo.toml":
		return true
	}
	return false
}
func scopeMatches(relative string, scope []string) bool {
	value := strings.ToLower(relative)
	for _, item := range scope {
		item = strings.ToLower(strings.Trim(strings.TrimSpace(item), "/\\"))
		if item != "" && strings.Contains(value, item) {
			return true
		}
	}
	return false
}
func validText(content []byte) bool { return !strings.ContainsRune(string(content), '\x00') }
