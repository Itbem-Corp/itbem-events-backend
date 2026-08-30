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
	githubComponentPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	scriptNamePattern              = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]{0,79}$`)
	environmentVariableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
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

// CapabilityProbe is a value-free attestation produced by an isolated,
// allow-listed dry-run. The evidence body remains in private object storage;
// this contract carries only its digest and exact repository checkpoint.
type CapabilityProbe struct {
	Name           string `json:"name"`
	State          string `json:"state"`
	Reason         string `json:"reason"`
	Revision       string `json:"revision"`
	EvidenceSHA256 string `json:"evidence_sha256"`
	SubjectSHA256  string `json:"subject_sha256"`
	ExecutorRole   string `json:"executor_role"`
}

type VaultHistoryEntry struct {
	Kind            string         `json:"kind"`
	Lifecycle       string         `json:"lifecycle"`
	Value           map[string]any `json:"value"`
	Provenance      []Provenance   `json:"provenance"`
	ValidFromSHA    string         `json:"valid_from_sha"`
	ValidThroughSHA string         `json:"valid_through_sha"`
	TransitionSHA   string         `json:"transition_sha"`
}

type VaultEntry struct {
	Key              string              `json:"key"`
	Kind             string              `json:"kind"`
	Lifecycle        string              `json:"lifecycle"`
	LifecycleSHA     string              `json:"lifecycle_sha"`
	ValidFromSHA     string              `json:"valid_from_sha"`
	ValidThroughSHA  string              `json:"valid_through_sha"`
	Value            map[string]any      `json:"value"`
	Provenance       []Provenance        `json:"provenance"`
	History          []VaultHistoryEntry `json:"history,omitempty"`
	HistoryTruncated bool                `json:"history_truncated,omitempty"`
}

type Manifest struct {
	SchemaVersion    int          `json:"schema_version"`
	Scope            string       `json:"scope"`
	Repository       Repository   `json:"repository"`
	Entries          []VaultEntry `json:"entries"`
	HistoryTruncated bool         `json:"history_truncated,omitempty"`
}

type VaultDiff struct {
	Added     []string `json:"added"`
	Modified  []string `json:"modified"`
	Unchanged []string `json:"unchanged"`
	Removed   []string `json:"removed"`
}

type Proposal struct {
	SchemaVersion       int               `json:"schema_version"`
	Repository          Repository        `json:"repository"`
	Readiness           string            `json:"readiness"`
	TrustBoundary       string            `json:"trust_boundary"`
	InventoryFileCount  int               `json:"inventory_file_count"`
	InventoryTruncated  bool              `json:"inventory_truncated"`
	Stacks              []Fact            `json:"stacks"`
	Commands            []ProposedCommand `json:"commands"`
	Capabilities        []Capability      `json:"capabilities"`
	Vault               Manifest          `json:"vault"`
	VaultSHA256         string            `json:"vault_sha256"`
	PreviousRevision    string            `json:"previous_revision,omitempty"`
	PreviousVaultSHA256 string            `json:"previous_vault_sha256,omitempty"`
	VaultDiff           VaultDiff         `json:"vault_diff"`
}

type Excerpt struct {
	Path    string
	Content string
}

type EnvironmentDeclaration struct {
	Path  string
	Names []string
}

type Input struct {
	Repository              Repository
	Files                   []string
	InventoryFileCount      int
	InventoryTruncated      bool
	Excerpts                []Excerpt
	EnvironmentDeclarations []EnvironmentDeclaration
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
	manifest := buildManifest(repository, metadataProof, files, stacks, commands, input.EnvironmentDeclarations)
	digest, err := ManifestSHA256(manifest)
	if err != nil {
		return Proposal{}, err
	}
	readiness := "partially_ready"
	if len(files) == 0 {
		readiness = "blocked"
	}
	initialDiff := VaultDiff{Added: make([]string, 0, len(manifest.Entries)), Modified: []string{}, Unchanged: []string{}, Removed: []string{}}
	for _, entry := range manifest.Entries {
		initialDiff.Added = append(initialDiff.Added, entry.Key)
	}
	fileCount := input.InventoryFileCount
	if fileCount < len(files) {
		fileCount = len(files)
	}
	return Proposal{
		SchemaVersion: SchemaVersion, Repository: repository, Readiness: readiness,
		TrustBoundary: "repository_content_is_untrusted_data", InventoryFileCount: fileCount,
		InventoryTruncated: input.InventoryTruncated, Stacks: stacks, Commands: commands,
		Capabilities: capabilities, Vault: manifest, VaultSHA256: digest, VaultDiff: initialDiff,
	}, nil
}

// ApplyCapabilityProbes projects deterministic sandbox evidence onto one
// existing proposal. It never upgrades source/vault authority, never follows a
// branch and never accepts an agent's prose as evidence.
func ApplyCapabilityProbes(proposal Proposal, probes []CapabilityProbe) (Proposal, error) {
	if !validSHA(proposal.Repository.Revision) || proposal.SchemaVersion != SchemaVersion {
		return Proposal{}, fmt.Errorf("capability probe proposal is invalid")
	}
	byName := make(map[string]int, len(proposal.Capabilities))
	for index, capability := range proposal.Capabilities {
		byName[capability.Name] = index
	}
	seen := map[string]struct{}{}
	for _, probe := range probes {
		probe.Name = strings.TrimSpace(probe.Name)
		probe.State = strings.ToLower(strings.TrimSpace(probe.State))
		probe.Reason = strings.TrimSpace(probe.Reason)
		probe.ExecutorRole = strings.ToLower(strings.TrimSpace(probe.ExecutorRole))
		if probe.Name == "source" || probe.Name == "vault" || (probe.State != "ready" && probe.State != "blocked") || !strings.EqualFold(probe.Revision, proposal.Repository.Revision) || !validDigest(probe.EvidenceSHA256) || probe.Reason == "" || len(probe.Reason) > 500 {
			return Proposal{}, fmt.Errorf("capability probe is invalid")
		}
		if probe.ExecutorRole != "qa" && probe.ExecutorRole != "release" && probe.ExecutorRole != "orchestrator" {
			return Proposal{}, fmt.Errorf("capability probe executor role is invalid")
		}
		index, exists := byName[probe.Name]
		if !exists {
			return Proposal{}, fmt.Errorf("capability probe name is unknown")
		}
		if _, duplicate := seen[probe.Name]; duplicate {
			return Proposal{}, fmt.Errorf("duplicate capability probe")
		}
		subject, err := CapabilityProbeSubjectSHA256(proposal.Repository, probe)
		if err != nil || !strings.EqualFold(subject, probe.SubjectSHA256) {
			return Proposal{}, fmt.Errorf("capability probe subject digest is invalid")
		}
		seen[probe.Name] = struct{}{}
		proposal.Capabilities[index] = Capability{
			Name: probe.Name, State: probe.State, Reason: probe.Reason,
			Evidence: []Provenance{{Source: "sandbox_probe:" + probe.ExecutorRole, Path: "sha256:" + strings.ToLower(probe.EvidenceSHA256), Revision: proposal.Repository.Revision, Confidence: 1}},
		}
	}
	proposal.Readiness = "partially_ready"
	for _, capability := range proposal.Capabilities {
		if capability.State == "blocked" {
			proposal.Readiness = "blocked"
			break
		}
	}
	return proposal, nil
}

// CapabilityProbeSubjectSHA256 prevents an evidence digest from being replayed
// across repositories, commits, capabilities or execution roles.
func CapabilityProbeSubjectSHA256(repository Repository, probe CapabilityProbe) (string, error) {
	state := strings.ToLower(strings.TrimSpace(probe.State))
	if repository.Reference == "" || !validSHA(strings.ToLower(strings.TrimSpace(repository.Revision))) || probe.Name == "" || (state != "ready" && state != "blocked") || !validDigest(probe.EvidenceSHA256) || probe.ExecutorRole == "" {
		return "", fmt.Errorf("capability probe subject is invalid")
	}
	encoded, err := json.Marshal(struct {
		SchemaVersion  int    `json:"schema_version"`
		Repository     string `json:"repository"`
		Revision       string `json:"revision"`
		Capability     string `json:"capability"`
		State          string `json:"state"`
		ExecutorRole   string `json:"executor_role"`
		EvidenceSHA256 string `json:"evidence_sha256"`
	}{SchemaVersion, repository.Reference, strings.ToLower(strings.TrimSpace(repository.Revision)), strings.TrimSpace(probe.Name), strings.ToLower(strings.TrimSpace(probe.State)), strings.ToLower(strings.TrimSpace(probe.ExecutorRole)), strings.ToLower(strings.TrimSpace(probe.EvidenceSHA256))})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validDigest(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

const maxVaultHistoryPerEntry = 64

// Reconcile carries a previously approved Vault forward to one newly inspected
// immutable repository SHA. Current static evidence remains authoritative;
// changed facts retain the replaced value as deprecated history and absent
// facts remain visible as removed. No repository prose or agent output can
// choose lifecycle state.
func Reconcile(proposal Proposal, previous Manifest) (Proposal, error) {
	current := proposal.Vault
	if proposal.SchemaVersion != SchemaVersion || current.SchemaVersion != SchemaVersion || previous.SchemaVersion != SchemaVersion || current.Scope != "repository" || previous.Scope != "repository" {
		return Proposal{}, fmt.Errorf("vault reconciliation schema is invalid")
	}
	if current.Repository.Reference != previous.Repository.Reference || !validSHA(current.Repository.Revision) || !validSHA(previous.Repository.Revision) {
		return Proposal{}, fmt.Errorf("vault reconciliation checkpoints are invalid")
	}
	if current.Repository.Revision == previous.Repository.Revision {
		digest, err := ManifestSHA256(previous)
		if err != nil {
			return Proposal{}, err
		}
		proposal.Vault, proposal.VaultSHA256 = previous, digest
		proposal.PreviousRevision, proposal.PreviousVaultSHA256 = previous.Repository.Revision, digest
		proposal.VaultDiff = VaultDiff{Added: []string{}, Modified: []string{}, Unchanged: []string{}, Removed: []string{}}
		return proposal, nil
	}
	previousDigest, err := ManifestSHA256(previous)
	if err != nil {
		return Proposal{}, err
	}
	diff := VaultDiff{Added: []string{}, Modified: []string{}, Unchanged: []string{}, Removed: []string{}}
	previousByKey := make(map[string]VaultEntry, len(previous.Entries))
	for _, entry := range previous.Entries {
		if strings.TrimSpace(entry.Key) == "" {
			return Proposal{}, fmt.Errorf("previous vault contains an invalid entry")
		}
		if _, duplicate := previousByKey[entry.Key]; duplicate {
			return Proposal{}, fmt.Errorf("previous vault contains duplicate entries")
		}
		previousByKey[entry.Key] = normalizeVaultEntry(entry, previous.Repository.Revision)
	}
	result := make([]VaultEntry, 0, len(current.Entries)+len(previous.Entries))
	seen := make(map[string]struct{}, len(current.Entries))
	for _, entry := range current.Entries {
		if strings.TrimSpace(entry.Key) == "" {
			return Proposal{}, fmt.Errorf("current vault contains an invalid entry")
		}
		if _, duplicate := seen[entry.Key]; duplicate {
			return Proposal{}, fmt.Errorf("current vault contains duplicate entries")
		}
		seen[entry.Key] = struct{}{}
		entry = normalizeVaultEntry(entry, current.Repository.Revision)
		if prior, exists := previousByKey[entry.Key]; exists {
			if vaultEntryEquivalent(entry, prior) && prior.Lifecycle != "removed" {
				diff.Unchanged = append(diff.Unchanged, entry.Key)
				entry.ValidFromSHA = prior.ValidFromSHA
				entry.History = append([]VaultHistoryEntry(nil), prior.History...)
				entry.HistoryTruncated = prior.HistoryTruncated
			} else {
				diff.Modified = append(diff.Modified, entry.Key)
				lifecycle := "deprecated"
				transition := current.Repository.Revision
				if prior.Lifecycle == "removed" {
					lifecycle, transition = "removed", prior.LifecycleSHA
				}
				entry.History = append([]VaultHistoryEntry(nil), prior.History...)
				entry.History = append(entry.History, vaultHistorySnapshot(prior, lifecycle, transition, previous.Repository.Revision))
				entry.HistoryTruncated = prior.HistoryTruncated
				trimVaultHistory(&entry)
			}
		} else {
			diff.Added = append(diff.Added, entry.Key)
		}
		entry.Lifecycle = "active"
		entry.LifecycleSHA = current.Repository.Revision
		entry.ValidThroughSHA = current.Repository.Revision
		result = append(result, entry)
	}
	for _, prior := range previousByKey {
		if _, exists := seen[prior.Key]; exists {
			continue
		}
		if prior.Lifecycle != "removed" {
			diff.Removed = append(diff.Removed, prior.Key)
			prior.Lifecycle = "removed"
			prior.LifecycleSHA = current.Repository.Revision
			prior.ValidThroughSHA = previous.Repository.Revision
		}
		result = append(result, prior)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Key < result[right].Key })
	current.Entries = result
	for _, entry := range result {
		if entry.HistoryTruncated {
			current.HistoryTruncated = true
			break
		}
	}
	for _, values := range [][]string{diff.Added, diff.Modified, diff.Unchanged, diff.Removed} {
		sort.Strings(values)
	}
	digest, err := ManifestSHA256(current)
	if err != nil {
		return Proposal{}, err
	}
	proposal.Vault, proposal.VaultSHA256 = current, digest
	proposal.PreviousRevision, proposal.PreviousVaultSHA256, proposal.VaultDiff = previous.Repository.Revision, previousDigest, diff
	return proposal, nil
}

func normalizeVaultEntry(entry VaultEntry, revision string) VaultEntry {
	if entry.Lifecycle == "" {
		entry.Lifecycle = "active"
	}
	if entry.LifecycleSHA == "" {
		entry.LifecycleSHA = revision
	}
	if entry.ValidFromSHA == "" {
		entry.ValidFromSHA = revision
	}
	if entry.ValidThroughSHA == "" {
		entry.ValidThroughSHA = revision
	}
	return entry
}

func vaultEntryEquivalent(left, right VaultEntry) bool {
	leftJSON, leftErr := json.Marshal(struct {
		Kind  string         `json:"kind"`
		Value map[string]any `json:"value"`
	}{left.Kind, left.Value})
	rightJSON, rightErr := json.Marshal(struct {
		Kind  string         `json:"kind"`
		Value map[string]any `json:"value"`
	}{right.Kind, right.Value})
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func vaultHistorySnapshot(entry VaultEntry, lifecycle, transitionSHA, fallbackThroughSHA string) VaultHistoryEntry {
	through := entry.ValidThroughSHA
	if through == "" {
		through = fallbackThroughSHA
	}
	return VaultHistoryEntry{
		Kind: entry.Kind, Lifecycle: lifecycle, Value: entry.Value, Provenance: entry.Provenance,
		ValidFromSHA: entry.ValidFromSHA, ValidThroughSHA: through, TransitionSHA: transitionSHA,
	}
}

func trimVaultHistory(entry *VaultEntry) {
	if len(entry.History) <= maxVaultHistoryPerEntry {
		return
	}
	entry.History = append([]VaultHistoryEntry(nil), entry.History[len(entry.History)-maxVaultHistoryPerEntry:]...)
	entry.HistoryTruncated = true
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
		if !unsafe && safeVaultInventoryPath(value) {
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

// safeVaultInventoryPath is deliberately stricter than a normal source tree
// walk. Vault onboarding records file names as evidence, so secret-bearing
// paths are excluded even though this package never reads their contents. A
// small allow-list admits environment *templates* by name; their contents are
// still never selected as source context by the GitHub adapter.
func safeVaultInventoryPath(value string) bool {
	lower := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), `\`, "/"))
	base := lower[strings.LastIndex(lower, "/")+1:]
	if isEnvironmentDeclarationPath(lower) {
		return !hasSensitivePathSegment(lower)
	}
	if strings.HasPrefix(base, ".env") || base == "id_rsa" || base == "id_ed25519" || hasSensitivePathSegment(lower) {
		return false
	}
	for _, fragment := range []string{"credential", "secret", "private_key", "api_key", "apikey", "access_key", "token", "password", "service_account"} {
		if strings.Contains(lower, fragment) {
			return false
		}
	}
	for _, extension := range []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore"} {
		if strings.HasSuffix(base, extension) {
			return false
		}
	}
	return true
}

func hasSensitivePathSegment(value string) bool {
	for _, segment := range strings.Split(value, "/") {
		switch segment {
		case ".git", ".aws", ".ssh", "secrets", "credentials", "node_modules", "vendor", ".next", ".turbo", "dist", "build", "coverage", "__pycache__":
			return true
		}
	}
	return false
}

func isEnvironmentDeclarationPath(value string) bool {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), `\`, "/"))
	base := value[strings.LastIndex(value, "/")+1:]
	for _, suffix := range []string{".example", ".sample", ".template", ".dist"} {
		if base == ".env"+suffix || base == "env"+suffix || strings.HasSuffix(base, ".env"+suffix) || strings.HasSuffix(base, ".tfvars"+suffix) {
			return true
		}
	}
	return false
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

func buildManifest(repository Repository, identity Provenance, files []string, stacks []Fact, commands []ProposedCommand, environment []EnvironmentDeclaration) Manifest {
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
	addMarkerEntry("repository.api_contracts", "contract", isAPIContractPath)
	addMarkerEntry("repository.data_schemas", "data_schema", isDataSchemaPath)
	addMarkerEntry("repository.dependencies", "dependency", isDependencyManifestPath)
	addMarkerEntry("repository.environment_declarations", "configuration", isEnvironmentDeclarationPath)
	addMarkerEntry("repository.tests", "testing", isTestPath)
	addMarkerEntry("repository.runbooks_and_decisions", "operations", isRunbookOrDecisionPath)
	declarations, declarationProofs := normalizedEnvironmentDeclarations(environment, files, repository.Revision)
	if len(declarations) > 0 {
		entries = append(entries, VaultEntry{Key: "repository.environment_variables", Kind: "configuration", Lifecycle: "active", Value: map[string]any{"declarations": declarations}, Provenance: declarationProofs})
	}
	for index := range entries {
		entries[index] = normalizeVaultEntry(entries[index], repository.Revision)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return Manifest{SchemaVersion: SchemaVersion, Scope: "repository", Repository: repository, Entries: entries}
}

func normalizedEnvironmentDeclarations(input []EnvironmentDeclaration, files []string, revision string) ([]map[string]any, []Provenance) {
	eligible := map[string]struct{}{}
	for _, file := range files {
		if isEnvironmentDeclarationPath(strings.ToLower(file)) {
			eligible[file] = struct{}{}
		}
	}
	byPath := map[string][]string{}
	for _, declaration := range input {
		path := strings.Trim(strings.ReplaceAll(strings.TrimSpace(declaration.Path), `\`, "/"), "/")
		if _, ok := eligible[path]; !ok {
			continue
		}
		seen := map[string]struct{}{}
		for _, name := range declaration.Names {
			name = strings.TrimSpace(name)
			if environmentVariableNamePattern.MatchString(name) {
				seen[name] = struct{}{}
			}
		}
		names := make([]string, 0, len(seen))
		for name := range seen {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) > 0 {
			byPath[path] = names
		}
	}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	values := make([]map[string]any, 0, len(paths))
	proofs := make([]Provenance, 0, len(paths))
	for _, path := range paths {
		values = append(values, map[string]any{"path": path, "names": byPath[path]})
		proofs = append(proofs, Provenance{Source: "structured_environment_template", Path: path, Revision: revision, Confidence: .95})
	}
	return values, proofs
}

func pathBase(value string) string {
	return value[strings.LastIndex(value, "/")+1:]
}

func hasPathSegment(value string, candidates ...string) bool {
	for _, segment := range strings.Split(value, "/") {
		for _, candidate := range candidates {
			if segment == candidate {
				return true
			}
		}
	}
	return false
}

func isAPIContractPath(path string) bool {
	base := pathBase(path)
	return strings.HasSuffix(base, ".proto") || strings.HasSuffix(base, ".graphql") || strings.HasSuffix(base, ".graphqls") ||
		base == "openapi.json" || base == "openapi.yaml" || base == "openapi.yml" ||
		base == "swagger.json" || base == "swagger.yaml" || base == "swagger.yml" ||
		base == "asyncapi.json" || base == "asyncapi.yaml" || base == "asyncapi.yml"
}

func isDataSchemaPath(path string) bool {
	base := pathBase(path)
	return hasPathSegment(path, "migration", "migrations", "alembic") || base == "schema.prisma" || base == "schema.sql" ||
		(strings.HasSuffix(base, ".sql") && (strings.Contains(base, "schema") || strings.Contains(base, "migration")))
}

func isDependencyManifestPath(path string) bool {
	base := pathBase(path)
	switch base {
	case "package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb", "deno.json", "deno.lock",
		"go.mod", "go.sum", "cargo.toml", "cargo.lock", "pyproject.toml", "poetry.lock", "pipfile", "pipfile.lock", "uv.lock",
		"gemfile", "gemfile.lock", "composer.json", "composer.lock", "pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts",
		"deps.edn", "mix.exs", "mix.lock", "package.swift", "package.resolved", "pubspec.yaml", "pubspec.lock":
		return true
	}
	return strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt")
}

func isTestPath(path string) bool {
	base := pathBase(path)
	if hasPathSegment(path, "test", "tests", "__tests__", "e2e", "integration", "integration-tests") || strings.HasPrefix(base, "playwright.config.") || strings.HasPrefix(base, "cypress.config.") {
		return true
	}
	for _, marker := range []string{"_test.go", "_test.rs", ".test.js", ".test.jsx", ".test.ts", ".test.tsx", ".spec.js", ".spec.jsx", ".spec.ts", ".spec.tsx"} {
		if strings.HasSuffix(base, marker) {
			return true
		}
	}
	return false
}

func isRunbookOrDecisionPath(path string) bool {
	base := pathBase(path)
	return hasPathSegment(path, "runbook", "runbooks", "adr", "adrs", "decisions") || strings.HasPrefix(base, "runbook") || strings.HasPrefix(base, "adr-")
}

func validSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func ValidRevision(value string) bool {
	return validSHA(strings.ToLower(strings.TrimSpace(value)))
}
