// Package projectvault builds deterministic, reviewable repository onboarding
// proposals. Repository contents are untrusted input: this package only uses a
// bounded path inventory and parses allow-listed structured manifests. It does
// not execute commands or interpret repository prose as instructions.
package projectvault

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const SchemaVersion = 1

var (
	githubComponentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	scriptNamePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]{0,79}$`)
)

type Provenance struct {
	Source     string  `json:"source"`
	Path       string  `json:"path"`
	Revision   string  `json:"revision"`
	Confidence float64 `json:"confidence"`
}

type Repository struct {
	Reference     string `json:"reference"`
	DefaultBranch string `json:"default_branch"`
	Revision      string `json:"revision"`
}

type Fact struct {
	Name       string       `json:"name"`
	Confidence float64      `json:"confidence"`
	Provenance []Provenance `json:"provenance"`
}

type ProposedCommand struct {
	Capability       string     `json:"capability"`
	WorkingDirectory string     `json:"working_directory"`
	Command          []string   `json:"command"`
	Status           string     `json:"status"`
	Provenance       Provenance `json:"provenance"`
}

type Capability struct {
	Name     string       `json:"name"`
	State    string       `json:"state"`
	Reason   string       `json:"reason"`
	Evidence []Provenance `json:"evidence,omitempty"`
}

type VaultEntry struct {
	Key        string         `json:"key"`
	Kind       string         `json:"kind"`
	Lifecycle  string         `json:"lifecycle"`
	Value      map[string]any `json:"value"`
	Provenance []Provenance   `json:"provenance"`
}

type Manifest struct {
	SchemaVersion int          `json:"schema_version"`
	Scope         string       `json:"scope"`
	Repository    Repository   `json:"repository"`
	Entries       []VaultEntry `json:"entries"`
}

type Proposal struct {
	SchemaVersion      int               `json:"schema_version"`
	Repository         Repository        `json:"repository"`
	Readiness          string            `json:"readiness"`
	TrustBoundary      string            `json:"trust_boundary"`
	InventoryFileCount int               `json:"inventory_file_count"`
	InventoryTruncated bool              `json:"inventory_truncated"`
	Stacks             []Fact            `json:"stacks"`
	Commands           []ProposedCommand `json:"commands"`
	Capabilities       []Capability      `json:"capabilities"`
	Vault              Manifest          `json:"vault"`
	VaultSHA256        string            `json:"vault_sha256"`
}

type Excerpt struct {
	Path    string
	Content string
}

type Input struct {
	Repository         Repository
	Files              []string
	InventoryFileCount int
	InventoryTruncated bool
	Excerpts           []Excerpt
}

// CanonicalGitHubReference accepts the user-facing HTTPS URL and the internal
// github:// reference. It rejects credentials, query strings, fragments,
// subpaths and non-GitHub hosts before any network operation occurs.
func CanonicalGitHubReference(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("repository URL must identify one github.com repository")
	}
	var owner, name string
	switch strings.ToLower(parsed.Scheme) {
	case "github":
		if parsed.Host == "" {
			return "", fmt.Errorf("repository URL must identify one github.com repository")
		}
		owner = parsed.Host
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) == 1 {
			name = parts[0]
		}
	case "https":
		if !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.Port() != "" {
			return "", fmt.Errorf("repository URL must use https://github.com")
		}
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) == 2 {
			owner, name = parts[0], parts[1]
		}
	default:
		return "", fmt.Errorf("repository URL must use https://github.com or github://")
	}
	name = strings.TrimSuffix(name, ".git")
	if len(owner) > 39 || len(name) > 100 || !githubComponentPattern.MatchString(owner) || !githubComponentPattern.MatchString(name) {
		return "", fmt.Errorf("repository URL must identify one github.com owner/repository")
	}
	return "github://" + owner + "/" + name, nil
}

func Build(input Input) (Proposal, error) {
	reference, err := CanonicalGitHubReference(input.Repository.Reference)
	if err != nil {
		return Proposal{}, err
	}
	revision := strings.ToLower(strings.TrimSpace(input.Repository.Revision))
	if !validSHA(revision) {
		return Proposal{}, fmt.Errorf("repository revision must be a full Git commit SHA")
	}
	defaultBranch := strings.TrimSpace(input.Repository.DefaultBranch)
	if defaultBranch == "" || len(defaultBranch) > 255 || strings.ContainsAny(defaultBranch, "\x00\r\n") {
		return Proposal{}, fmt.Errorf("default branch is invalid")
	}
	files := safeFiles(input.Files)
	repository := Repository{Reference: reference, DefaultBranch: defaultBranch, Revision: revision}
	metadataProof := Provenance{Source: "github_api", Path: "repository_metadata", Revision: revision, Confidence: 1}
	stacks := detectStacks(files, revision)
	commands := detectCommands(files, input.Excerpts, revision)
	capabilities := capabilityMatrix(metadataProof, commands)
	manifest := buildManifest(repository, metadataProof, files, stacks, commands)
	digest, err := ManifestSHA256(manifest)
	if err != nil {
		return Proposal{}, err
	}
	readiness := "partially_ready"
	if len(files) == 0 {
		readiness = "blocked"
	}
	fileCount := input.InventoryFileCount
	if fileCount < len(files) {
		fileCount = len(files)
	}
	return Proposal{
		SchemaVersion: SchemaVersion, Repository: repository, Readiness: readiness,
		TrustBoundary: "repository_content_is_untrusted_data", InventoryFileCount: fileCount,
		InventoryTruncated: input.InventoryTruncated, Stacks: stacks, Commands: commands,
		Capabilities: capabilities, Vault: manifest, VaultSHA256: digest,
	}, nil
}

// ManifestSHA256 is the stable content identity stored next to each immutable
// Vault revision and rechecked at the human approval boundary.
func ManifestSHA256(manifest Manifest) (string, error) {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode vault manifest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ProposalSHA256 seals capability states, detected facts and proposed commands
// in addition to the embedded Vault digest. The workflow status remains a
// separate mutable field; the inspected evidence does not.
func ProposalSHA256(proposal Proposal) (string, error) {
	encoded, err := json.Marshal(proposal)
	if err != nil {
		return "", fmt.Errorf("encode onboarding proposal: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func safeFiles(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.Trim(strings.ReplaceAll(strings.TrimSpace(value), `\`, "/"), "/")
		if value == "" || len(value) > 240 || strings.IndexFunc(value, func(character rune) bool { return character < 32 || character == 127 }) >= 0 {
			continue
		}
		unsafe := false
		for _, part := range strings.Split(value, "/") {
			if part == "" || part == "." || part == ".." {
				unsafe = true
				break
			}
		}
		if !unsafe {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func detectStacks(files []string, revision string) []Fact {
	markers := map[string][]string{
		"go": {"go.mod"}, "node": {"package.json"}, "rust": {"Cargo.toml"},
		"python": {"pyproject.toml", "requirements.txt"}, "terraform": {"main.tf"},
		"aws_cdk": {"cdk.json"},
	}
	result := make([]Fact, 0, len(markers))
	for stack, candidates := range markers {
		for _, file := range files {
			base := file[strings.LastIndex(file, "/")+1:]
			for _, marker := range candidates {
				if strings.EqualFold(base, marker) {
					result = append(result, Fact{Name: stack, Confidence: .95, Provenance: []Provenance{{Source: "static_inventory", Path: file, Revision: revision, Confidence: .95}}})
					goto nextStack
				}
			}
		}
	nextStack:
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func detectCommands(files []string, excerpts []Excerpt, revision string) []ProposedCommand {
	fileSet := map[string]string{}
	for _, file := range files {
		fileSet[strings.ToLower(file)] = file
	}
	commands := make([]ProposedCommand, 0, 8)
	for lowerPath, path := range fileSet {
		base := lowerPath[strings.LastIndex(lowerPath, "/")+1:]
		switch base {
		case "go.mod":
			commands = append(commands, command("unit", []string{"go", "test", "./..."}, path, revision))
		case "cargo.toml":
			commands = append(commands, command("unit", []string{"cargo", "test", "--all-targets"}, path, revision))
		}
	}
	for _, excerpt := range excerpts {
		path := strings.Trim(strings.ReplaceAll(excerpt.Path, `\`, "/"), "/")
		base := path[strings.LastIndex(path, "/")+1:]
		if !strings.EqualFold(base, "package.json") || len(excerpt.Content) > 64*1024 {
			continue
		}
		var manifest struct {
			Scripts map[string]json.RawMessage `json:"scripts"`
		}
		if json.Unmarshal([]byte(excerpt.Content), &manifest) != nil {
			continue
		}
		packageManager := packageManagerFor(path, fileSet)
		for _, candidate := range []struct{ name, capability string }{{"test", "unit"}, {"test:integration", "integration"}, {"test:contract", "contract"}, {"test:e2e", "e2e"}, {"build", "build"}} {
			if _, exists := manifest.Scripts[candidate.name]; exists && scriptNamePattern.MatchString(candidate.name) {
				args := []string{packageManager, "run", candidate.name}
				if packageManager == "yarn" {
					args = []string{"yarn", candidate.name}
				}
				commands = append(commands, command(candidate.capability, args, path, revision))
			}
		}
	}
	sort.Slice(commands, func(i, j int) bool {
		if commands[i].Capability == commands[j].Capability {
			if commands[i].WorkingDirectory != commands[j].WorkingDirectory {
				return commands[i].WorkingDirectory < commands[j].WorkingDirectory
			}
			return strings.Join(commands[i].Command, "\x00") < strings.Join(commands[j].Command, "\x00")
		}
		return commands[i].Capability < commands[j].Capability
	})
	if len(commands) > 64 {
		commands = commands[:64]
	}
	return commands
}

func command(capability string, value []string, path, revision string) ProposedCommand {
	workingDirectory := "."
	if index := strings.LastIndex(path, "/"); index >= 0 {
		workingDirectory = path[:index]
	}
	return ProposedCommand{Capability: capability, WorkingDirectory: workingDirectory, Command: value, Status: "proposed_not_executed", Provenance: Provenance{Source: "structured_manifest", Path: path, Revision: revision, Confidence: .9}}
}

func packageManagerFor(manifestPath string, files map[string]string) string {
	directory := ""
	if index := strings.LastIndex(strings.ToLower(manifestPath), "/"); index >= 0 {
		directory = strings.ToLower(manifestPath[:index+1])
	}
	for _, candidate := range []struct{ file, manager string }{{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"}, {"package-lock.json", "npm"}} {
		if _, exists := files[directory+candidate.file]; exists {
			return candidate.manager
		}
	}
	// A root lockfile governs workspaces when a nested package has no local
	// lockfile. Prefer explicit workspace managers over npm's default.
	for _, candidate := range []struct{ file, manager string }{{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"}} {
		if _, exists := files[candidate.file]; exists {
			return candidate.manager
		}
	}
	return "npm"
}

func capabilityMatrix(source Provenance, commands []ProposedCommand) []Capability {
	names := []string{"source", "branch_write", "pr_write", "review", "unit", "integration", "contract", "e2e", "preview", "staging", "release", "health", "recovery", "vault"}
	proposed := map[string]Provenance{}
	for _, item := range commands {
		proposed[item.Capability] = item.Provenance
	}
	result := make([]Capability, 0, len(names))
	for _, name := range names {
		item := Capability{Name: name, State: "unknown", Reason: "not proven by static onboarding"}
		switch name {
		case "source":
			item.State, item.Reason, item.Evidence = "ready", "immutable default-branch SHA and bounded tree inventory verified", []Provenance{source}
		case "vault":
			item.State, item.Reason, item.Evidence = "proposed", "vault draft requires explicit human approval", []Provenance{source}
		default:
			if proof, ok := proposed[name]; ok {
				item.State, item.Reason, item.Evidence = "proposed", "command detected but not executed in an allow-listed sandbox", []Provenance{proof}
			}
		}
		result = append(result, item)
	}
	return result
}

func buildManifest(repository Repository, identity Provenance, files []string, stacks []Fact, commands []ProposedCommand) Manifest {
	entries := []VaultEntry{{Key: "repository.identity", Kind: "repository", Lifecycle: "active", Value: map[string]any{"reference": repository.Reference, "default_branch": repository.DefaultBranch, "revision": repository.Revision}, Provenance: []Provenance{identity}}}
	if len(stacks) > 0 {
		names, proofs := make([]string, 0, len(stacks)), make([]Provenance, 0, len(stacks))
		for _, stack := range stacks {
			names, proofs = append(names, stack.Name), append(proofs, stack.Provenance...)
		}
		entries = append(entries, VaultEntry{Key: "repository.stacks", Kind: "architecture", Lifecycle: "active", Value: map[string]any{"detected": names}, Provenance: proofs})
	}
	if len(commands) > 0 {
		values, proofs := make([]map[string]any, 0, len(commands)), make([]Provenance, 0, len(commands))
		for _, item := range commands {
			values, proofs = append(values, map[string]any{"capability": item.Capability, "working_directory": item.WorkingDirectory, "argv": item.Command, "status": item.Status}), append(proofs, item.Provenance)
		}
		entries = append(entries, VaultEntry{Key: "repository.commands", Kind: "testing", Lifecycle: "active", Value: map[string]any{"commands": values}, Provenance: proofs})
	}
	addMarkerEntry := func(key, kind string, predicate func(string) bool) {
		paths := make([]string, 0)
		for _, file := range files {
			if predicate(strings.ToLower(file)) {
				paths = append(paths, file)
			}
		}
		if len(paths) == 0 {
			return
		}
		proofs := make([]Provenance, 0, len(paths))
		for _, path := range paths {
			proofs = append(proofs, Provenance{Source: "static_inventory", Path: path, Revision: repository.Revision, Confidence: .9})
		}
		entries = append(entries, VaultEntry{Key: key, Kind: kind, Lifecycle: "active", Value: map[string]any{"paths": paths}, Provenance: proofs})
	}
	addMarkerEntry("repository.ci", "workflow", func(path string) bool { return strings.HasPrefix(path, ".github/workflows/") })
	addMarkerEntry("repository.ownership", "ownership", func(path string) bool { return strings.HasSuffix(path, "codeowners") })
	addMarkerEntry("repository.infrastructure", "infrastructure", func(path string) bool {
		return strings.HasSuffix(path, ".tf") || strings.Contains(path, "cloudformation") || strings.Contains(path, "serverless.") || strings.HasSuffix(path, "cdk.json")
	})
	addMarkerEntry("repository.documentation", "documentation", func(path string) bool { return path == "readme.md" || strings.HasPrefix(path, "docs/") })
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return Manifest{SchemaVersion: SchemaVersion, Scope: "repository", Repository: repository, Entries: entries}
}

func validSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
