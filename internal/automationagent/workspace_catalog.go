package automationagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"events-stocks/internal/agentwork"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

const WorkspaceCatalogVersion = 1
const MaxWorkspaceCatalogEntries = 256

type WorkspaceCatalogRejectionError struct{ StatusCode int }

func (err *WorkspaceCatalogRejectionError) Error() string {
	return fmt.Sprintf("workspace catalog rejected (%d)", err.StatusCode)
}

type WorkspaceCatalogReadyRequest struct {
	Digest string                  `json:"digest"`
	Ready  []CatalogReadyWorkspace `json:"ready"`
}

type CatalogReadyWorkspace struct {
	ID           string             `json:"id"`
	Revision     string             `json:"revision"`
	Readiness    WorkspaceReadiness `json:"readiness"`
	Capabilities []string           `json:"capabilities"`
}

func SyncCentralWorkspaceCatalog(ctx context.Context, lookup func(string) string) error {
	if !strings.EqualFold(lookup("ITBEM_AI_WORKSPACE_CATALOG_ENABLED"), "true") {
		return nil
	}
	identity, err := LoadLocalMachineIdentity(lookup("ITBEM_AI_MACHINE_ID"), lookup("ITBEM_AI_STATE_DIR"))
	if err != nil {
		return err
	}
	agentKey := strings.TrimSpace(lookup("ITBEM_AI_AGENT_KEY"))
	if agentKey == "" {
		agentKey = "generalist"
	}
	instance, err := identity.RegisteredAgentInstanceID(agentKey)
	if err != nil || instance == "" {
		if _, err := EnsureGatewayAgentInstance(ctx, lookup); err != nil {
			return err
		}
	}
	config, err := LoadRuntimeConfig(lookup)
	if err != nil {
		return err
	}
	callback, err := NewHTTPCallback(config.APIBaseURL, config.CallbackIdentity, config.AgentInstanceID, nil)
	if err != nil {
		return err
	}
	catalog, err := callback.FetchWorkspaceCatalog(ctx, config)
	if err != nil {
		var rejected *WorkspaceCatalogRejectionError
		if errors.As(err, &rejected) && (rejected.StatusCode == http.StatusUnauthorized || rejected.StatusCode == http.StatusForbidden) {
			_ = os.Remove(lookup("ITBEM_AI_WORKSPACE_CATALOG_FILE"))
		}
		return err
	}
	if err := ReconcileWorkspaceCatalog(ctx, catalog, lookup); err != nil {
		return err
	}
	if config.Role != agentwork.RolePrincipalEngineer {
		return nil
	}
	workspaces, err := LoadWorkspaces(ConfiguredWorkspaceRegistry(lookup))
	if err != nil {
		return err
	}
	readiness, err := WorkspaceReadinessSnapshot(lookup)
	if err != nil {
		return err
	}
	byID := map[string]WorkspaceReadiness{}
	for _, value := range readiness {
		byID[value.ID] = value
	}
	ready := WorkspaceCatalogReadyRequest{Digest: catalog.Digest, Ready: []CatalogReadyWorkspace{}}
	for _, entry := range catalog.Entries {
		workspace, exists := workspaces[entry.ID]
		state := byID[entry.ID]
		if !exists || !state.Ready || !state.SandboxReady {
			continue
		}
		ready.Ready = append(ready.Ready, CatalogReadyWorkspace{ID: entry.ID, Revision: entry.Revision, Readiness: state, Capabilities: workspace.Config.Capabilities})
	}
	if len(ready.Ready) == 0 {
		return nil
	}
	return callback.ReportWorkspaceCatalogReady(ctx, config, ready)
}

func (c *HTTPCallback) ReportWorkspaceCatalogReady(ctx context.Context, config RuntimeConfig, ready WorkspaceCatalogReadyRequest) error {
	body, err := json.Marshal(ready)
	if err != nil {
		return err
	}
	request, err := c.newSignedRequest(ctx, http.MethodPost, c.baseURL+"/api/internal/automation/workspace-catalog/ready", body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Agent-Role", string(config.Role))
	request.Header.Set("X-Agent-Lane", string(config.Lane))
	request.Header.Set("X-Agent-Gateway-Token", config.GatewayToken)
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("workspace catalog readiness transport unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("workspace catalog readiness rejected (%d)", response.StatusCode)
	}
	return nil
}

type WorkspaceCatalogEntry struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	SourceID   string `json:"source_id"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Revision   string `json:"revision"`
	Profile    string `json:"profile"`
}

type WorkspaceCatalog struct {
	Version   int                     `json:"version"`
	Digest    string                  `json:"digest"`
	ExpiresAt time.Time               `json:"expires_at"`
	Entries   []WorkspaceCatalogEntry `json:"entries"`
}

func CatalogWorkspaceID(sourceID string) string {
	return "catalog-" + strings.ReplaceAll(sourceID, "-", "")
}

func WorkspaceCatalogDigest(entries []WorkspaceCatalogEntry, policy string) string {
	encoded, _ := json.Marshal(struct {
		Entries []WorkspaceCatalogEntry
		Policy  string
	}{entries, policy})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (entry WorkspaceCatalogEntry) Validate() error {
	project, pErr := uuid.FromString(entry.ProjectID)
	source, sErr := uuid.FromString(entry.SourceID)
	if pErr != nil || sErr != nil || project == uuid.Nil || source == uuid.Nil || entry.ID != CatalogWorkspaceID(source.String()) || !gitCommitPattern.MatchString(entry.Revision) || !runtimeAgentKeyPattern.MatchString(entry.Profile) {
		return fmt.Errorf("workspace catalog entry identity is invalid")
	}
	if _, err := parseGitHubRemote("https://github.com/" + entry.Repository + ".git"); err != nil {
		return fmt.Errorf("workspace catalog repository is invalid")
	}
	return validateWorkspaceBase("https://github.com/"+entry.Repository+".git", entry.Branch)
}

func (c *HTTPCallback) FetchWorkspaceCatalog(ctx context.Context, config RuntimeConfig) (WorkspaceCatalog, error) {
	request, err := c.newSignedRequest(ctx, http.MethodGet, c.baseURL+"/api/internal/automation/workspace-catalog", nil)
	if err != nil {
		return WorkspaceCatalog{}, err
	}
	request.Header.Set("X-Agent-Role", string(config.Role))
	request.Header.Set("X-Agent-Lane", string(config.Lane))
	request.Header.Set("X-Agent-Gateway-Token", config.GatewayToken)
	response, err := c.client.Do(request)
	if err != nil {
		return WorkspaceCatalog{}, fmt.Errorf("workspace catalog transport unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return WorkspaceCatalog{}, &WorkspaceCatalogRejectionError{StatusCode: response.StatusCode}
	}
	var envelope struct {
		Data WorkspaceCatalog `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return WorkspaceCatalog{}, fmt.Errorf("workspace catalog response invalid")
	}
	catalog := envelope.Data
	if catalog.Version != WorkspaceCatalogVersion || !catalog.ExpiresAt.After(time.Now()) || catalog.ExpiresAt.After(time.Now().Add(10*time.Minute)) || len(catalog.Entries) > MaxWorkspaceCatalogEntries || len(catalog.Digest) != 64 {
		return WorkspaceCatalog{}, fmt.Errorf("workspace catalog bounds invalid")
	}
	seen := map[string]bool{}
	for _, entry := range catalog.Entries {
		if err := entry.Validate(); err != nil {
			return WorkspaceCatalog{}, err
		}
		if seen[entry.ID] {
			return WorkspaceCatalog{}, fmt.Errorf("duplicate workspace catalog identity")
		}
		seen[entry.ID] = true
	}
	return catalog, nil
}

type workspaceCatalogCache struct {
	Catalog       WorkspaceCatalog              `json:"catalog"`
	Registry      map[string]WorkspaceConfig    `json:"registry"`
	Preparation   []CatalogWorkspacePreparation `json:"preparation"`
	ProfileDigest string                        `json:"profile_digest"`
}

func verifyCatalogProfileBinding(id string, metadata map[string]any, lookup func(string) string) error {
	raw, err := os.ReadFile(lookup("ITBEM_AI_WORKSPACE_CATALOG_FILE"))
	var cache workspaceCatalogCache
	if err != nil || json.Unmarshal(raw, &cache) != nil || !cache.Catalog.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("catalog profile authority unavailable")
	}
	for _, entry := range cache.Catalog.Entries {
		if entry.ID == id && metadata["catalog_profile"] == entry.Profile {
			return nil
		}
	}
	return fmt.Errorf("catalog execution profile changed; refresh the checkpoint and replan")
}

type CatalogWorkspacePreparation struct {
	ID     string `json:"id"`
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
}

func CatalogRoleCapabilitiesAllowed(role string, capabilities []string) bool {
	if validateWorkspaceCapabilities(capabilities) != nil {
		return false
	}
	for _, capability := range capabilities {
		capability = strings.TrimSpace(capability)
		switch agentwork.Role(role) {
		case agentwork.RoleOrchestrator, agentwork.RoleReviewer:
			if capability != WorkspaceCapabilityReadRepository && capability != WorkspaceCapabilityFetchRemote {
				return false
			}
		case agentwork.RolePrincipalEngineer:
			if capability == WorkspaceCapabilityPublishBranch || capability == WorkspaceCapabilityCreatePullReq {
				return false
			}
		case agentwork.RoleQA:
			if capability != WorkspaceCapabilityReadRepository && capability != WorkspaceCapabilityFetchRemote && capability != WorkspaceCapabilityCreateWorktree {
				return false
			}
		case agentwork.RoleReleaseManager:
		default:
			return false
		}
	}
	return true
}

// Registry policy and execution profiles are distinct: the server selects an
// authorized repository and profile ID, never local paths or executable argv.
func ReconcileWorkspaceCatalog(ctx context.Context, catalog WorkspaceCatalog, lookup func(string) string) error {
	if catalog.Version != WorkspaceCatalogVersion || !catalog.ExpiresAt.After(time.Now()) || len(catalog.Entries) > MaxWorkspaceCatalogEntries {
		return fmt.Errorf("workspace catalog is expired or invalid")
	}
	root := strings.TrimSpace(lookup("ITBEM_AI_WORKSPACE_CATALOG_ROOT"))
	cachePath := strings.TrimSpace(lookup("ITBEM_AI_WORKSPACE_CATALOG_FILE"))
	if root == "" || !filepath.IsAbs(root) || cachePath == "" || !filepath.IsAbs(cachePath) {
		return fmt.Errorf("workspace catalog requires private absolute root and cache paths")
	}
	var profiles map[string]WorkspaceConfig
	if json.Unmarshal([]byte(lookup("ITBEM_AI_WORKSPACE_PROFILES_JSON")), &profiles) != nil || len(profiles) == 0 {
		return fmt.Errorf("workspace catalog execution profiles are required")
	}
	registry := map[string]WorkspaceConfig{}
	preparation := []CatalogWorkspacePreparation{}
	seen := map[string]bool{}
	static, err := LoadWorkspaceRegistry(lookup("ITBEM_AI_WORKSPACES_JSON"))
	if err != nil {
		return err
	}
	for _, entry := range catalog.Entries {
		if err := entry.Validate(); err != nil {
			return err
		}
		if seen[entry.ID] {
			return fmt.Errorf("duplicate workspace catalog identity")
		}
		seen[entry.ID] = true
		if _, exists := registry[entry.ID]; exists {
			return fmt.Errorf("duplicate workspace catalog identity")
		}
		if _, exists := static[entry.ID]; exists {
			return fmt.Errorf("workspace catalog cannot shadow static registry")
		}
		profile, ok := profiles[entry.Profile]
		if !ok {
			preparation = append(preparation, CatalogWorkspacePreparation{ID: entry.ID, Reason: "profile_unavailable"})
			continue
		}
		if !CatalogRoleCapabilitiesAllowed(lookup("ITBEM_AI_ROLE"), profile.Capabilities) {
			return fmt.Errorf("workspace profile exceeds role capability boundary")
		}
		role := agentwork.Role(lookup("ITBEM_AI_ROLE"))
		if (role == agentwork.RolePrincipalEngineer || role == agentwork.RoleQA) && !profile.RequireSandbox {
			return fmt.Errorf("catalog execution profile requires sandbox isolation")
		}
		if profile.Path != "" || profile.RepositoryURL != "" || profile.BaseBranch != "" {
			return fmt.Errorf("workspace profile cannot supply repository or path bindings")
		}
		profile.Path = filepath.Join(root, entry.ID)
		profile.RepositoryURL = "https://github.com/" + entry.Repository + ".git"
		profile.BaseBranch = entry.Branch
		registry[entry.ID] = profile
	}
	encoded, err := json.Marshal(registry)
	if err != nil {
		return err
	}
	workspaces, err := LoadWorkspaceRegistry(string(encoded))
	if err != nil {
		return err
	}
	// Validate the complete desired set before creating any checkout.
	for _, entry := range catalog.Entries {
		workspace, configured := workspaces[entry.ID]
		if !configured {
			continue
		}
		state := ReadWorkspaceGitState(workspace)
		if !state.Available {
			if _, err := SyncAuthorizedManagedWorkspace(ctx, workspace, lookup); err != nil {
				preparation = append(preparation, CatalogWorkspacePreparation{ID: entry.ID, Reason: "source_preparation_unavailable"})
				delete(registry, entry.ID)
				continue
			}
		} else if !strings.EqualFold(state.GitHubRepository, entry.Repository) || state.HasLocalChanges {
			preparation = append(preparation, CatalogWorkspacePreparation{ID: entry.ID, Reason: "source_binding_or_cleanliness_invalid"})
			delete(registry, entry.ID)
			continue
		}
		known, err := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "rev-parse", "--verify", "--quiet", entry.Revision+"^{commit}")
		if err != nil || known.ExitCode != 0 || !strings.EqualFold(strings.TrimSpace(known.Output), entry.Revision) {
			if _, syncErr := SyncAuthorizedManagedWorkspace(ctx, workspace, lookup); syncErr == nil {
				known, err = runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "rev-parse", "--verify", "--quiet", entry.Revision+"^{commit}")
			}
			if err != nil || known.ExitCode != 0 || !strings.EqualFold(strings.TrimSpace(known.Output), entry.Revision) {
				preparation = append(preparation, CatalogWorkspacePreparation{ID: entry.ID, Reason: "frozen_revision_unavailable"})
				delete(registry, entry.ID)
				continue
			}
		}
		preparation = append(preparation, CatalogWorkspacePreparation{ID: entry.ID, Ready: true})
	}
	profileDigest := sha256.Sum256([]byte(lookup("ITBEM_AI_WORKSPACE_PROFILES_JSON")))
	cache, err := json.Marshal(workspaceCatalogCache{Catalog: catalog, Registry: registry, Preparation: preparation, ProfileDigest: hex.EncodeToString(profileDigest[:])})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(cachePath), ".workspace-catalog-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(cache); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), cachePath)
}

// Catalog expiry/removal prevents new resolution; existing files are retained
// for recovery rather than deleted underneath another execution attempt.
func ConfiguredWorkspaceRegistry(lookup func(string) string) string {
	base := lookup("ITBEM_AI_WORKSPACES_JSON")
	if !strings.EqualFold(lookup("ITBEM_AI_WORKSPACE_CATALOG_ENABLED"), "true") {
		return base
	}
	var registry map[string]WorkspaceConfig
	if strings.TrimSpace(base) == "" {
		base = "{}"
	}
	if json.Unmarshal([]byte(base), &registry) != nil || registry == nil {
		return "invalid static workspace registry"
	}
	raw, err := os.ReadFile(lookup("ITBEM_AI_WORKSPACE_CATALOG_FILE"))
	var cache workspaceCatalogCache
	if err != nil || json.Unmarshal(raw, &cache) != nil || cache.Catalog.Version != WorkspaceCatalogVersion || !cache.Catalog.ExpiresAt.After(time.Now()) {
		return "workspace catalog unavailable or expired"
	}
	profileDigest := sha256.Sum256([]byte(lookup("ITBEM_AI_WORKSPACE_PROFILES_JSON")))
	if cache.ProfileDigest != hex.EncodeToString(profileDigest[:]) {
		return "workspace catalog execution policy changed; reconcile required"
	}
	for id, profile := range cache.Registry {
		if _, exists := registry[id]; exists {
			return "workspace catalog cannot shadow static registry"
		}
		registry[id] = profile
	}
	encoded, err := json.Marshal(registry)
	if err != nil {
		return "workspace catalog registry invalid"
	}
	return string(encoded)
}
