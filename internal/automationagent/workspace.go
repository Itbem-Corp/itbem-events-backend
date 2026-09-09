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

	"events-stocks/internal/projectvault"
)

const (
	maxWorkspaceFiles        = 2500
	maxWorkspaceFileBytes    = 180000
	maxWorkspaceExcerptChars = 180000
	maxWorkspaceExcerptBytes = 24000
	maxWorkspaceExcerpts     = 24
	maxReadOnlyFixturePaths  = 16
)

// sensitiveWorkspaceKey matches common secret-bearing configuration keys with
// optional vendor prefixes/suffixes, e.g. AWS_SECRET_ACCESS_KEY or
// GITHUB_API_TOKEN, without treating arbitrary prose as a credential.
const sensitiveWorkspaceKey = `(?:[A-Za-z0-9]+[_-])*(?:api[_-]?key|apikey|access[_-]?key|client[_-]?secret|private[_-]?key|password|secret|token|authorization|service[_-]?account)(?:[_-][A-Za-z0-9]+)*`

type WorkspaceConfig struct {
	Path string `json:"path"`
	// RepositoryURL and BaseBranch are used only by the operator-invoked
	// checkout synchronizer. They let one worker host maintain dedicated,
	// reproducible base checkouts for many projects without letting a task pick
	// a path, remote, or branch.
	RepositoryURL      string     `json:"repository_url"`
	BaseBranch         string     `json:"base_branch"`
	Capabilities       []string   `json:"capabilities"`
	ValidationCommands [][]string `json:"validation_commands"`
	// ValidationCommandKinds and QACommandKinds are operator-owned identities
	// for the commands at the matching index. Agent input may choose whether an
	// approved command runs, but it can never rename a command to satisfy a
	// release policy. Empty lists retain legacy execution without gate-bearing
	// named evidence.
	ValidationCommandKinds []string   `json:"validation_command_kinds"`
	QACommands             [][]string `json:"qa_commands"`
	QACommandKinds         []string   `json:"qa_command_kinds"`
	// ReadOnlyFixturePaths names operator-owned, repository-relative content
	// that is intentionally outside Git but required by isolated validation
	// worktrees (for example a pinned local contract checkout). The paths are
	// copied without Git metadata, links, credentials or special files. A task
	// can never add to or override this allowlist.
	ReadOnlyFixturePaths []string `json:"read_only_fixture_paths"`
	QAArtifactPatterns   []string `json:"qa_artifact_patterns"`
	QAScreenshotCommand  []string `json:"qa_screenshot_command"`
	// QASemanticCommand is an operator-owned, opt-in browser QA layer (for
	// example the pinned Stagehand runner). It receives only the reviewed
	// preview URL and a private evidence output path; a task or model response
	// can never select its executable or arguments.
	QASemanticCommand []string `json:"qa_semantic_command"`
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
		if err := validateCommandList("qa_commands", config.QACommands); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", id, err)
		}
		if err := validateCommandKinds(config.ValidationCommands, config.ValidationCommandKinds, config.QACommands, config.QACommandKinds); err != nil {
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
		result[id] = Workspace{ID: id, Root: root, Config: config}
	}
	return result, nil
}

func validateReadOnlyFixturePaths(paths []string) error {
	if len(paths) > maxReadOnlyFixturePaths {
		return fmt.Errorf("read_only_fixture_paths may contain at most %d entries", maxReadOnlyFixturePaths)
	}
	seen := make(map[string]struct{}, len(paths))
	for index, configured := range paths {
		configured = strings.TrimSpace(configured)
		if configured == "" || filepath.IsAbs(configured) || filepath.VolumeName(configured) != "" || strings.ContainsAny(configured, "\x00\r\n") {
			return fmt.Errorf("read_only_fixture_paths[%d] is invalid", index)
		}
		clean := filepath.Clean(configured)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("read_only_fixture_paths[%d] must remain inside the workspace", index)
		}
		for _, segment := range strings.FieldsFunc(filepath.ToSlash(clean), func(r rune) bool { return r == '/' }) {
			if segment == ".git" || excludedDirectory(segment) {
				return fmt.Errorf("read_only_fixture_paths[%d] targets an excluded directory", index)
			}
		}
		if !safeContextFile(clean) {
			return fmt.Errorf("read_only_fixture_paths[%d] may contain credentials", index)
		}
		key := strings.ToLower(filepath.ToSlash(clean))
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("read_only_fixture_paths[%d] is duplicated", index)
		}
		seen[key] = struct{}{}
		paths[index] = clean
	}
	return nil
}

var gitBranchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,126}$`)
var workspaceSSHHostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

func validateWorkspaceBase(repositoryURL, baseBranch string) error {
	if strings.ContainsAny(repositoryURL, "\x00\r\n") {
		return fmt.Errorf("repository_url is invalid")
	}
	// A managed checkout must name the branch detected during onboarding (or
	// another explicitly approved branch). Silently assuming `main` would make
	// non-main repositories sync the wrong ref and violate the frozen context.
	// Read-only local workspaces do not need a configured remote or base branch.
	if repositoryURL == "" && baseBranch == "" {
		return nil
	}
	if repositoryURL == "" || baseBranch == "" {
		return fmt.Errorf("repository_url and base_branch must be configured together")
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
		"gitleaks": true, "govulncheck": true, "osv-scanner": true, "cargo-audit": true,
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

var workspaceTestKind = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/+-]{0,127}$`)

// validateCommandKinds keeps test identity next to operator-owned executable
// configuration. A partial positional map is ambiguous and duplicate names
// could let one command masquerade as multiple policy requirements.
func validateCommandKinds(validationCommands [][]string, validationKinds []string, qaCommands [][]string, qaKinds []string) error {
	for _, pair := range []struct {
		name     string
		commands [][]string
		kinds    []string
	}{{"validation_command_kinds", validationCommands, validationKinds}, {"qa_command_kinds", qaCommands, qaKinds}} {
		if len(pair.kinds) != 0 && len(pair.kinds) != len(pair.commands) {
			return fmt.Errorf("%s must be empty or contain one identity per command", pair.name)
		}
	}
	seen := make(map[string]struct{}, len(validationKinds)+len(qaKinds))
	for _, kind := range append(append([]string(nil), validationKinds...), qaKinds...) {
		if kind != strings.TrimSpace(kind) || !workspaceTestKind.MatchString(kind) {
			return fmt.Errorf("test command identity %q is invalid", kind)
		}
		key := strings.ToLower(kind)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("test command identity %q is duplicated", kind)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func configuredCommandKind(kinds []string, index int) string {
	if len(kinds) == 0 || index < 0 || index >= len(kinds) {
		return ""
	}
	return kinds[index]
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
	windowsAbsolute := len(command) >= 3 && command[1] == ':' && (command[2] == '/' || command[2] == '\\')
	return (filepath.IsAbs(command) || windowsAbsolute) && strings.EqualFold(filepath.Base(command), "node.exe")
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
	ValidationCommandCount      int    `json:"validation_command_count"`
	NamedValidationCommandCount int    `json:"named_validation_command_count"`
	QACommandCount              int    `json:"qa_command_count"`
	NamedQACommandCount         int    `json:"named_qa_command_count"`
	ArtifactCollection          bool   `json:"artifact_collection"`
	ScreenshotMode              string `json:"screenshot_mode"`
	SemanticQAMode              string `json:"semantic_qa_mode"`
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
	workspaceSensitiveAssignment = regexp.MustCompile(`(?im)(\b` + sensitiveWorkspaceKey + `\b\s*[:=]\s*)(?:"([^"\r\n]*)"|'([^'\r\n]*)'|([^\s"'\r\n]+))`)
	workspaceBearerCredential    = regexp.MustCompile(`(?im)(\bauthorization\s*:\s*bearer\s+)[^\s\r\n]+`)
	workspaceURLCredential       = regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.-]*://[^\s:@/]+:)[^\s@/]+(@)`)
	workspacePEMBlock            = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	workspaceGitHubToken         = regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{20,}\b|\bgithub_pat_[A-Za-z0-9_]{20,}\b`)
	workspaceAWSAccessKey        = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)
	workspaceSlackToken          = regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)
	workspaceFormatVerb          = regexp.MustCompile(`^%[A-Za-z]$`)
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
	assignmentSource := content
	assignmentOffset := 0
	content = workspaceSensitiveAssignment.ReplaceAllStringFunc(content, func(match string) string {
		parts := workspaceSensitiveAssignment.FindStringSubmatch(match)
		relativeStart := strings.Index(assignmentSource[assignmentOffset:], match)
		matchStart := assignmentOffset + relativeStart
		assignmentOffset = matchStart + len(match)
		valueStart := matchStart + len(parts[1])
		if relativeStart >= 0 && valueStart < len(assignmentSource) {
			quote := sourceQuoteAt(assignmentSource, matchStart)
			if quote != 0 && assignmentSource[valueStart] == quote {
				// The assignment-shaped token ends at the surrounding source
				// literal's closing quote, so it names a key but carries no value.
				return match
			}
		}
		value := ""
		quote := ""
		switch {
		case parts[2] != "":
			value, quote = parts[2], `"`
		case parts[3] != "":
			value, quote = parts[3], `'`
		case parts[4] != "":
			value = parts[4]
		default:
			// Empty quoted assignments and source-code string literals such as
			// `"API_KEY ="` carry no credential. Preserving them byte-for-byte
			// prevents redaction from fabricating review evidence.
			return match
		}
		if workspaceFormatVerb.MatchString(value) {
			// A label such as `token: %w` in source code describes an error and
			// does not carry a credential. Preserving the format verb is important:
			// replacing it changes program semantics and can fabricate findings.
			return match
		}
		redactions++
		return parts[1] + quote + "<redacted>" + quote
	})
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

func sourceQuoteAt(content string, position int) byte {
	lineStart := strings.LastIndex(content[:position], "\n") + 1
	var quote byte
	escaped := false
	for index := lineStart; index < position; index++ {
		character := content[index]
		if quote != 0 {
			if quote != '`' && escaped {
				escaped = false
				continue
			}
			if quote != '`' && character == '\\' {
				escaped = true
				continue
			}
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '"' || character == '\'' || character == '`' {
			quote = character
		}
	}
	return quote
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
	return WorkspaceHarness{
		ValidationCommandCount:      len(config.ValidationCommands),
		NamedValidationCommandCount: len(config.ValidationCommandKinds),
		QACommandCount:              len(config.QACommands),
		NamedQACommandCount:         len(config.QACommandKinds),
		ArtifactCollection:          len(config.QAArtifactPatterns) > 0,
		ScreenshotMode:              screenshotMode,
		SemanticQAMode:              semanticQAMode,
	}
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
	state := workspaceGitState(workspace.Root)
	if !state.Available || state.GitHubRepository != "" {
		return state
	}
	// A developer may use an SSH Host alias (for example github.com-work) in
	// their local Git configuration. That alias is deliberately not accepted as
	// proof that an arbitrary network endpoint is GitHub. It can, however, be
	// bound to the operator-owned registry only when the actual origin is the
	// exact registered remote. This restores the Vault identity used for local
	// planning without widening publication: publication still requires the
	// canonical GitHub App checkpoint and its independent remote validation.
	if repository, err := operatorRegisteredGitHubRepository(workspace); err == nil {
		state.GitHubRepository = repository.Owner + "/" + repository.Name
	}
	return state
}

func operatorRegisteredGitHubRepository(workspace Workspace) (githubRepository, error) {
	registered := strings.TrimSpace(workspace.Config.RepositoryURL)
	if registered == "" {
		return githubRepository{}, fmt.Errorf("workspace has no operator-registered remote")
	}
	origin, err := managedWorkspaceOrigin(context.Background(), workspace)
	if err != nil || !sameOperatorRegisteredRemote(origin, registered) {
		return githubRepository{}, fmt.Errorf("workspace origin does not match its operator-registered remote")
	}
	if repository, parseErr := parseGitHubRemote(registered); parseErr == nil {
		return repository, nil
	}
	return parseOperatorSSHGitHubAlias(registered)
}

// sameOperatorRegisteredRemote intentionally does not canonicalize SSH aliases
// to HTTPS URLs. The registry's remote is the operator's explicit trust
// binding; accepting a different transport or host would let a task attach a
// Vault identity to a checkout that the operator did not register.
func sameOperatorRegisteredRemote(actual, registered string) bool {
	canonical := func(value string) string {
		return strings.TrimSuffix(strings.TrimSpace(value), "/")
	}
	return canonical(actual) != "" && canonical(actual) == canonical(registered)
}

// parseOperatorSSHGitHubAlias extracts only the owner/repository identity from
// the standard git@host:owner/repository.git SSH form. The host can be an SSH
// alias solely because repository_url is operator-owned and must exactly match
// origin above. This function never authorizes a network request or a push.
func parseOperatorSSHGitHubAlias(value string) (githubRepository, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "git@") {
		return githubRepository{}, fmt.Errorf("operator remote is not a supported SSH alias")
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "git@"), ":", 2)
	if len(parts) != 2 || !workspaceSSHHostPattern.MatchString(parts[0]) || strings.ContainsAny(parts[1], "?#@\\\r\n") {
		return githubRepository{}, fmt.Errorf("operator remote is not a safe SSH alias")
	}
	return parseGitHubRemote("https://github.com/" + parts[1])
}

// FetchWorkspaceRemote refreshes only origin's remote refs for a registered
// workspace after an explicit human action. It does not use pull, change HEAD,
// apply a merge/rebase, create a worktree or make a commit. Credentials remain
// outside the process environment and interactive prompts are disabled.
func FetchWorkspaceRemote(ctx context.Context, workspace Workspace) (WorkspaceGitState, error) {
	return fetchWorkspaceRemote(ctx, workspace, "origin", nil)
}

// FetchAuthorizedWorkspaceRemote refreshes a GitHub workspace through the
// dedicated read-only Source App. GitHub repositories never fall back to a
// developer credential, a cached credential helper, SSH, or unauthenticated
// public access merely because a repository happens to be public today.
// Local/non-GitHub fixtures keep the legacy operator-owned fetch path so the
// deterministic test harness does not need a cloud identity.
func FetchAuthorizedWorkspaceRemote(ctx context.Context, workspace Workspace, lookup func(string) string) (WorkspaceGitState, error) {
	repository, remote, required, err := gitHubSourceWorkspaceRemote(workspace)
	if err != nil {
		return WorkspaceGitState{}, err
	}
	if !required {
		return FetchWorkspaceRemote(ctx, workspace)
	}
	config, err := LoadGitHubSourceAppConfig(lookup)
	if err != nil {
		return WorkspaceGitState{}, fmt.Errorf("GitHub source App is required for workspace %s: %w", workspace.ID, err)
	}
	return FetchWorkspaceRemoteWithGitHubApp(ctx, workspace, repository, remote, config, nil, time.Now().UTC())
}

// FetchWorkspaceRemoteWithGitHubApp performs one remote-ref refresh using an
// installation token scoped to the exact operator-registered repository. It
// is exported for the process entrypoint and test fixtures; callers should use
// FetchAuthorizedWorkspaceRemote with their process configuration.
func FetchWorkspaceRemoteWithGitHubApp(ctx context.Context, workspace Workspace, repository githubRepository, remote string, config GitHubAppConfig, client *http.Client, now time.Time) (WorkspaceGitState, error) {
	if err := ensureManagedWorkspaceOrigin(ctx, workspace); err != nil {
		return WorkspaceGitState{}, err
	}
	token, err := MintGitHubRepositoryToken(ctx, config, client, now, repository.Owner+"/"+repository.Name)
	if err != nil {
		return WorkspaceGitState{}, fmt.Errorf("GitHub source App could not authenticate the registered repository")
	}
	environment, cleanup, err := gitHubInstallationTokenEnvironment(token.Token)
	if err != nil {
		return WorkspaceGitState{}, err
	}
	defer cleanup()
	return fetchWorkspaceRemote(ctx, workspace, remote, environment)
}

func fetchWorkspaceRemote(ctx context.Context, workspace Workspace, remote string, environment map[string]string) (WorkspaceGitState, error) {
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
	fetched, err := runLocalWithEnv(ctx, workspace.Root, 60*time.Second, "", gitWorkspaceEnvironment(environment), "git", gitWorkspaceFetchArguments(remote, gitWorkspaceUsesInstallationToken(environment))...)
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

// SyncAuthorizedManagedWorkspace is the Delivery path for managed source
// checkouts. Every GitHub repository is synchronized with a dedicated,
// read-only Source App token selected for that exact repository. The legacy
// synchronizer remains available only for local/non-GitHub fixtures.
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

// SyncManagedWorkspaceWithGitHubApp keeps an operator-managed GitHub checkout
// on its configured base branch using only a short-lived, repository-scoped
// Source App token. The token is never written into a remote URL, command
// argument, log, task result, Vault, or evidence object.
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
	// Implementation worktrees are runtime-owned children of the dedicated
	// managed checkout.  Tell Git about that reserved directory as well as
	// excluding it in workspaceGitState: command-line status, external tooling
	// and a later agent restart must all see the base checkout as clean while
	// the separately reviewable task worktree exists.  This is deliberately a
	// local git-info exclude, never a repository .gitignore change, and it
	// exempts no other local path from the dirty-check gate.
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
	origin, err := managedWorkspaceOrigin(ctx, workspace)
	if err != nil {
		return WorkspaceGitState{}, fmt.Errorf("managed workspace has no readable origin remote")
	}
	if !sameManagedRemote(origin, remoteURL) {
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
	// A successful fast-forward alone is not enough: Git accepts the no-op
	// merge when a locally-created base branch is ahead of origin. Delivery
	// must never start from unpublished local history, so the operator-managed
	// checkout has to be byte-for-byte at the fetched remote base afterwards.
	// This also makes the later task worktree's base an explicit GitHub
	// checkpoint rather than an implicit local HEAD.
	head, headErr := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "rev-parse", "HEAD")
	remoteHead, remoteErr := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "rev-parse", remoteBase)
	if headErr != nil || remoteErr != nil || head.ExitCode != 0 || remoteHead.ExitCode != 0 || !strings.EqualFold(strings.TrimSpace(head.Output), strings.TrimSpace(remoteHead.Output)) {
		return WorkspaceGitState{}, fmt.Errorf("managed workspace base branch is not identical to fetched origin")
	}
	return workspaceGitState(workspace.Root), nil
}

const managedWorkspaceRuntimeExclude = ".itbem-agent-worktrees/"

func ensureManagedWorkspaceRuntimeExclude(ctx context.Context, root string) error {
	resolved, err := runLocal(ctx, root, 20*time.Second, "", "git", "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil || resolved.ExitCode != 0 {
		return fmt.Errorf("resolve managed workspace runtime exclude")
	}
	path := filepath.Clean(strings.TrimSpace(resolved.Output))
	if path == "." || !filepath.IsAbs(path) {
		return fmt.Errorf("managed workspace runtime exclude path is invalid")
	}
	common, commonErr := runLocal(ctx, root, 20*time.Second, "", "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if commonErr != nil || common.ExitCode != 0 {
		return fmt.Errorf("resolve managed workspace git directory")
	}
	gitDirectory := filepath.Clean(strings.TrimSpace(common.Output))
	relative, relativeErr := filepath.Rel(gitDirectory, path)
	if relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("managed workspace runtime exclude is outside its Git directory")
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("managed workspace runtime exclude is not a regular file")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect managed workspace runtime exclude: %w", statErr)
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read managed workspace runtime exclude: %w", readErr)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSpace(line) == managedWorkspaceRuntimeExclude {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("prepare managed workspace runtime exclude: %w", err)
	}
	entry := string(contents)
	if entry != "" && !strings.HasSuffix(entry, "\n") {
		entry += "\n"
	}
	entry += managedWorkspaceRuntimeExclude + "\n"
	if err := os.WriteFile(path, []byte(entry), 0600); err != nil {
		return fmt.Errorf("write managed workspace runtime exclude: %w", err)
	}
	return nil
}

func gitHubSourceWorkspaceRemote(workspace Workspace) (githubRepository, string, bool, error) {
	registered := strings.TrimSpace(workspace.Config.RepositoryURL)
	if registered == "" {
		return githubRepository{}, "", false, nil
	}
	lower := strings.ToLower(registered)
	if !strings.HasPrefix(lower, "https://github.com/") && !strings.HasPrefix(lower, "git@") {
		return githubRepository{}, "", false, nil
	}
	repository, err := parseGitHubRemote(registered)
	if err != nil {
		repository, err = parseOperatorSSHGitHubAlias(registered)
	}
	if err != nil {
		return githubRepository{}, "", true, fmt.Errorf("workspace %s has an invalid GitHub repository_url", workspace.ID)
	}
	return repository, "https://github.com/" + repository.Owner + "/" + repository.Name + ".git", true, nil
}

// GitHubSourceAccessRequired reports whether this lane has any
// operator-registered GitHub workspace that must be synchronized through the
// dedicated Source App. It is safe for doctor/preflight use: it reads only the
// registry and never opens a checkout, contacts GitHub, or loads a credential.
func GitHubSourceAccessRequired(lookup func(string) string) (bool, error) {
	workspaces, err := LoadWorkspaceRegistry(lookup("ITBEM_AI_WORKSPACES_JSON"))
	if err != nil {
		return false, err
	}
	for _, workspace := range workspaces {
		_, _, required, sourceErr := gitHubSourceWorkspaceRemote(workspace)
		if sourceErr != nil {
			return false, sourceErr
		}
		if required {
			return true, nil
		}
	}
	return false, nil
}

func ensureManagedWorkspaceOrigin(ctx context.Context, workspace Workspace) error {
	if err := workspace.RequireCapability(WorkspaceCapabilityFetchRemote); err != nil {
		return err
	}
	state := workspaceGitState(workspace.Root)
	if !state.Available || strings.TrimSpace(state.HeadSHA) == "" {
		return fmt.Errorf("workspace is not a readable Git repository")
	}
	origin, err := managedWorkspaceOrigin(ctx, workspace)
	if err != nil || !sameManagedRemote(origin, workspace.Config.RepositoryURL) {
		return fmt.Errorf("workspace origin does not match its registered repository_url")
	}
	return nil
}

// managedWorkspaceOrigin reads the literal remote value owned by the
// checkout. Unlike `git remote get-url`, this deliberately does not expand
// url.*.insteadOf rules: those rules are transport routing, not evidence that
// the checkout is registered to a particular repository.
func managedWorkspaceOrigin(ctx context.Context, workspace Workspace) (string, error) {
	origin, err := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "config", "--get", "remote.origin.url")
	if err != nil || origin.ExitCode != 0 || strings.TrimSpace(origin.Output) == "" {
		return "", fmt.Errorf("workspace has no readable origin remote")
	}
	return strings.TrimSpace(origin.Output), nil
}

func gitWorkspaceEnvironment(environment map[string]string) map[string]string {
	if len(environment) == 0 {
		return map[string]string{"GIT_TERMINAL_PROMPT": "0"}
	}
	result := make(map[string]string, len(environment)+1)
	for key, value := range environment {
		result[key] = value
	}
	result["GIT_TERMINAL_PROMPT"] = "0"
	return result
}

func gitWorkspaceUsesInstallationToken(environment map[string]string) bool {
	return strings.TrimSpace(environment["ITBEM_GITHUB_INSTALLATION_TOKEN"]) != ""
}

func gitWorkspaceCloneArguments(branch, remote, directory string, suppressCredentialHelper bool) []string {
	arguments := []string{"clone", "--origin", "origin", "--branch", branch, "--no-recurse-submodules", remote, directory}
	if suppressCredentialHelper {
		arguments = append(gitHubInstallationGitConfigArguments(), arguments...)
	}
	return arguments
}

func gitWorkspaceFetchArguments(remote string, suppressCredentialHelper bool) []string {
	arguments := []string{"fetch", "--prune", "--tags", "--no-recurse-submodules", remote}
	if suppressCredentialHelper {
		arguments = append(gitHubInstallationGitConfigArguments(), arguments...)
	}
	if remote != "origin" {
		arguments = append(arguments, "+refs/heads/*:refs/remotes/origin/*")
	}
	return arguments
}

// gitHubInstallationGitConfigArguments overrides the few local Git settings
// that could replace or intercept a repository-scoped App token. The checkout
// is still used for Git objects and the registered origin binding, but an
// operator/developer local configuration cannot silently select a credential
// helper, an HTTP proxy, a weakened TLS policy, or an extra authorization
// header for the authenticated network operation.
func gitHubInstallationGitConfigArguments() []string {
	return []string{
		"-c", "credential.helper=",
		"-c", "http.proxy=",
		"-c", "http.sslVerify=true",
		"-c", "http.extraHeader=",
	}
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
	ID                          string            `json:"id"`
	Ready                       bool              `json:"ready"`
	Issue                       string            `json:"issue,omitempty"`
	Capabilities                []string          `json:"capabilities"`
	ValidationCommandCount      int               `json:"validation_command_count"`
	NamedValidationCommandCount int               `json:"named_validation_command_count"`
	QACommandCount              int               `json:"qa_command_count"`
	NamedQACommandCount         int               `json:"named_qa_command_count"`
	ScreenshotMode              string            `json:"screenshot_mode"`
	SemanticQAMode              string            `json:"semantic_qa_mode"`
	Git                         WorkspaceGitState `json:"git"`
}

// WorkspaceReadiness is the small, continuously reportable projection of a
// workspace diagnostic. It is safe to send with a worker heartbeat: it never
// contains a local path, remote URL, branch, revision, command, source excerpt
// or credential. The dashboard uses it to prevent an operator from confusing
// a live worker with a worker that is actually ready to execute a given kind
// of Delivery work.
type WorkspaceReadiness struct {
	ID                          string `json:"id"`
	Ready                       bool   `json:"ready"`
	QAReady                     bool   `json:"qa_ready"`
	VisualQAReady               bool   `json:"visual_qa_ready"`
	PublicationReady            bool   `json:"publication_ready"`
	ValidationCommandCount      int    `json:"validation_command_count"`
	NamedValidationCommandCount int    `json:"named_validation_command_count"`
	QACommandCount              int    `json:"qa_command_count"`
	NamedQACommandCount         int    `json:"named_qa_command_count"`
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
	for _, diagnostic := range diagnostics {
		canPublish := capabilityPresent(diagnostic.Capabilities, WorkspaceCapabilityStageCommit) &&
			capabilityPresent(diagnostic.Capabilities, WorkspaceCapabilityPublishBranch) &&
			capabilityPresent(diagnostic.Capabilities, WorkspaceCapabilityCreatePullReq)
		result = append(result, WorkspaceReadiness{
			ID: diagnostic.ID, Ready: diagnostic.Ready,
			QAReady: diagnostic.Ready && diagnostic.QACommandCount > 0, VisualQAReady: diagnostic.Ready && diagnostic.SemanticQAMode == "configured_command",
			PublicationReady: diagnostic.Ready && canPublish, ValidationCommandCount: diagnostic.ValidationCommandCount,
			NamedValidationCommandCount: diagnostic.NamedValidationCommandCount, QACommandCount: diagnostic.QACommandCount, NamedQACommandCount: diagnostic.NamedQACommandCount,
		})
	}
	return result, nil
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
		harnessExecutablesReady := workspaceHarnessExecutablesReady(workspace.Config)
		screenshotMode := "default_responsive"
		if harness.ScreenshotMode == "configured_command" {
			screenshotMode = "configured_command"
		}
		diagnostic := WorkspaceDiagnostic{
			ID: id, Ready: state.Available && harnessExecutablesReady, Capabilities: append([]string(nil), workspace.Config.Capabilities...),
			ValidationCommandCount: len(workspace.Config.ValidationCommands), NamedValidationCommandCount: len(workspace.Config.ValidationCommandKinds),
			QACommandCount: len(workspace.Config.QACommands), NamedQACommandCount: len(workspace.Config.QACommandKinds),
			ScreenshotMode: screenshotMode, SemanticQAMode: harness.SemanticQAMode, Git: state,
		}
		if !state.Available {
			diagnostic.Issue = "configured directory is not a readable Git worktree"
		} else if !harnessExecutablesReady {
			// Keep the diagnostic useful without publishing a local executable or
			// absolute path through doctor output or recurring heartbeats.
			diagnostic.Issue = "configured workspace harness executable is unavailable"
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	return diagnostics, nil
}

func workspaceHarnessExecutablesReady(config WorkspaceConfig) bool {
	commands := make([][]string, 0, len(config.ValidationCommands)+len(config.QACommands)+2)
	commands = append(commands, config.ValidationCommands...)
	commands = append(commands, config.QACommands...)
	if len(config.QAScreenshotCommand) > 0 {
		commands = append(commands, config.QAScreenshotCommand)
	}
	if len(config.QASemanticCommand) > 0 {
		commands = append(commands, config.QASemanticCommand)
	}
	for _, command := range commands {
		if len(command) == 0 {
			return false
		}
		if _, err := exec.LookPath(command[0]); err != nil {
			return false
		}
	}
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

// PrepareDeliveryWorkspaces synchronizes every operator-managed repository
// checkout before a Delivery phase can read code, run tests, or create a
// task worktree. It is deliberately separate from context rendering: summary
// and ledger-only phases can still read a frozen snapshot through a read-only
// lane, while plan, implementation, and QA must prove their local base is the
// fetched remote branch at the exact frozen SHA.
//
// A workspace without repository_url/base_branch is retained only for legacy
// local-only tasks. Every real GitHub onboarding is required to register both
// values and repository:fetch, so it follows the managed path below.
func PrepareDeliveryWorkspaces(ctx context.Context, delivery json.RawMessage, lookup func(string) string) error {
	var value struct {
		ContextSources []struct {
			Kind      string `json:"kind"`
			Reference string `json:"reference"`
			Revision  string `json:"revision"`
		} `json:"context_sources"`
	}
	if err := json.Unmarshal(delivery, &value); err != nil {
		return fmt.Errorf("delivery input must be a JSON object")
	}
	seen := make(map[string]struct{}, len(value.ContextSources))
	for _, source := range value.ContextSources {
		if source.Kind != "repository" || !strings.HasPrefix(strings.TrimSpace(source.Reference), "workspace://") {
			continue
		}
		reference := strings.TrimSpace(source.Reference)
		if _, duplicate := seen[reference]; duplicate {
			continue
		}
		seen[reference] = struct{}{}
		workspace, err := RegisteredWorkspace(reference, lookup)
		if err != nil {
			return err
		}
		remoteURL := strings.TrimSpace(workspace.Config.RepositoryURL)
		baseBranch := strings.TrimSpace(workspace.Config.BaseBranch)
		if remoteURL == "" && baseBranch == "" {
			continue
		}
		if remoteURL == "" || baseBranch == "" {
			return fmt.Errorf("workspace %s must configure both repository_url and base_branch for managed Delivery synchronization", workspace.ID)
		}
		expected := strings.ToLower(strings.TrimSpace(source.Revision))
		if !projectvault.ValidRevision(expected) {
			return fmt.Errorf("workspace %s managed Delivery source has no immutable frozen revision", workspace.ID)
		}
		state, err := SyncAuthorizedManagedWorkspace(ctx, workspace, lookup)
		if err != nil {
			return fmt.Errorf("workspace %s could not synchronize its managed base before Delivery: %w", workspace.ID, err)
		}
		if !strings.EqualFold(state.HeadSHA, expected) {
			return fmt.Errorf("workspace %s fetched origin has advanced beyond the frozen context revision; refresh the project checkpoint and replan", workspace.ID)
		}
	}
	return nil
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
