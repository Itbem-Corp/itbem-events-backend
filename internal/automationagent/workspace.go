package automationagent

// The local workspace registry is operator-owned configuration. A task may
// reference a registered workspace by ID, never a filesystem path.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	RepositoryURL       string     `json:"repository_url"`
	BaseBranch          string     `json:"base_branch"`
	Capabilities        []string   `json:"capabilities"`
	ValidationCommands  [][]string `json:"validation_commands"`
	QACommands          [][]string `json:"qa_commands"`
	QAArtifactPatterns  []string   `json:"qa_artifact_patterns"`
	QAScreenshotCommand []string   `json:"qa_screenshot_command"`
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
		if err := validateCommandList("qa_commands", config.QACommands); err != nil {
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
	allowed := map[string]bool{"npm": true, "npx": true, "go": true, "python": true, "pytest": true, "cargo": true}
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
	return filepath.IsAbs(command) && strings.EqualFold(filepath.Base(command), "node.exe")
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
	ValidationCommandCount int    `json:"validation_command_count"`
	QACommandCount         int    `json:"qa_command_count"`
	ArtifactCollection     bool   `json:"artifact_collection"`
	ScreenshotMode         string `json:"screenshot_mode"`
	SemanticQAMode         string `json:"semantic_qa_mode"`
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
	return WorkspaceHarness{
		ValidationCommandCount: len(config.ValidationCommands),
		QACommandCount:         len(config.QACommands),
		ArtifactCollection:     len(config.QAArtifactPatterns) > 0,
		ScreenshotMode:         screenshotMode,
		SemanticQAMode:         semanticQAMode,
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
	if _, err := os.Stat(workspace.Root); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(workspace.Root), 0700); err != nil {
			return WorkspaceGitState{}, fmt.Errorf("prepare managed workspace parent: %w", err)
		}
		cloned, cloneErr := runLocalWithEnv(ctx, filepath.Dir(workspace.Root), 2*time.Minute, "", map[string]string{"GIT_TERMINAL_PROMPT": "0"}, "git", "clone", "--origin", "origin", "--branch", baseBranch, "--no-recurse-submodules", remoteURL, workspace.Root)
		if cloneErr != nil || cloned.ExitCode != 0 {
			return WorkspaceGitState{}, fmt.Errorf("managed workspace clone could not complete")
		}
	} else if err != nil {
		return WorkspaceGitState{}, fmt.Errorf("inspect managed workspace: %w", err)
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
	fetched, err := runLocalWithEnv(ctx, workspace.Root, 60*time.Second, "", map[string]string{"GIT_TERMINAL_PROMPT": "0"}, "git", "fetch", "--prune", "--tags", "--no-recurse-submodules", "origin")
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
	ID                     string `json:"id"`
	Ready                  bool   `json:"ready"`
	QAReady                bool   `json:"qa_ready"`
	VisualQAReady          bool   `json:"visual_qa_ready"`
	PublicationReady       bool   `json:"publication_ready"`
	ValidationCommandCount int    `json:"validation_command_count"`
	QACommandCount         int    `json:"qa_command_count"`
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
			ID:                     diagnostic.ID,
			Ready:                  diagnostic.Ready,
			QAReady:                diagnostic.Ready && diagnostic.QACommandCount > 0,
			VisualQAReady:          diagnostic.Ready && diagnostic.SemanticQAMode == "configured_command",
			PublicationReady:       diagnostic.Ready && canPublish,
			ValidationCommandCount: diagnostic.ValidationCommandCount,
			QACommandCount:         diagnostic.QACommandCount,
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
		screenshotMode := "default_responsive"
		if harness.ScreenshotMode == "configured_command" {
			screenshotMode = "configured_command"
		}
		diagnostic := WorkspaceDiagnostic{
			ID: id, Ready: state.Available, Capabilities: append([]string(nil), workspace.Config.Capabilities...),
			ValidationCommandCount: len(workspace.Config.ValidationCommands), QACommandCount: len(workspace.Config.QACommands),
			ScreenshotMode: screenshotMode, SemanticQAMode: harness.SemanticQAMode, Git: state,
		}
		if !state.Available {
			diagnostic.Issue = "configured directory is not a readable Git worktree"
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	return diagnostics, nil
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
