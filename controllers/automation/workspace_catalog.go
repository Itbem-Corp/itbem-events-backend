package automation

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

type workspaceCatalogScope struct {
	AgentKey         string            `json:"agent_key"`
	MachineIDs       []string          `json:"machine_ids"`
	ClientIDs        []string          `json:"client_ids"`
	Roles            []string          `json:"roles"`
	RepositoryOwners []string          `json:"repository_owners"`
	DefaultProfile   string            `json:"default_profile"`
	AllowedProfiles  []string          `json:"allowed_profiles"`
	ManifestProfiles map[string]string `json:"manifest_profiles"`
}

func catalogContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func catalogManifestProfile(scope workspaceCatalogScope, inventory automationagent.GitHubRepositoryMap, revision string) string {
	if !strings.EqualFold(inventory.Revision, revision) {
		return scope.DefaultProfile
	}
	selected := ""
	for _, file := range inventory.Files {
		switch file {
		case "go.mod", "package.json", "pyproject.toml", "Cargo.toml":
		default:
			continue
		}
		profile := scope.ManifestProfiles[file]
		if profile == "" {
			continue
		}
		if selected != "" && selected != profile {
			return scope.DefaultProfile
		}
		selected = profile
	}
	if selected == "" {
		return scope.DefaultProfile
	}
	return selected
}

func catalogReadyClientScope(c echo.Context, entry automationagent.WorkspaceCatalogEntry) []string {
	identity, ok := currentAgentCallbackIdentity(c)
	if !ok {
		return nil
	}
	gateway, ok := gatewayIdentityFromRequest(c)
	if !ok {
		return nil
	}
	var policy struct {
		Version int                     `json:"version"`
		Scopes  []workspaceCatalogScope `json:"scopes"`
	}
	if json.Unmarshal([]byte(os.Getenv("AUTOMATION_WORKSPACE_CATALOG_POLICY_JSON")), &policy) != nil || policy.Version != 1 {
		return nil
	}
	owner, _, _ := strings.Cut(entry.Repository, "/")
	clients := []string{}
	for _, scope := range policy.Scopes {
		if scope.AgentKey == identity.AgentKey && catalogContains(scope.MachineIDs, identity.MachineID) && catalogContains(scope.Roles, string(gateway.Role)) && catalogContains(scope.RepositoryOwners, strings.ToLower(owner)) && catalogContains(scope.AllowedProfiles, entry.Profile) {
			clients = append(clients, scope.ClientIDs...)
		}
	}
	return clients
}

// Central scope is server-owned policy. Ready project checkpoints identify
// content, not authority to access a different organization or execute code.
func workspaceCatalogForRequest(c echo.Context) (automationagent.WorkspaceCatalog, int, error) {
	identity, ok := requireAgentCallbackIdentity(c)
	if !ok {
		return automationagent.WorkspaceCatalog{}, http.StatusUnauthorized, errors.New("workspace catalog authentication required")
	}
	gateway, ok := gatewayIdentityFromRequest(c)
	if !ok {
		return automationagent.WorkspaceCatalog{}, http.StatusUnauthorized, errors.New("workspace catalog rejected")
	}
	if configuration.DB == nil {
		return automationagent.WorkspaceCatalog{}, http.StatusServiceUnavailable, errors.New("workspace catalog unavailable")
	}
	raw := os.Getenv("AUTOMATION_WORKSPACE_CATALOG_POLICY_JSON")
	var policy struct {
		Version int                     `json:"version"`
		Scopes  []workspaceCatalogScope `json:"scopes"`
	}
	if json.Unmarshal([]byte(raw), &policy) != nil || policy.Version != automationagent.WorkspaceCatalogVersion || len(policy.Scopes) > 128 {
		return automationagent.WorkspaceCatalog{}, http.StatusServiceUnavailable, errors.New("workspace catalog policy unavailable")
	}
	entries := []automationagent.WorkspaceCatalogEntry{}
	seen := map[string]bool{}
	authorized := false
	for _, scope := range policy.Scopes {
		if scope.AgentKey != identity.AgentKey || !catalogContains(scope.MachineIDs, identity.MachineID) || !catalogContains(scope.Roles, string(gateway.Role)) {
			continue
		}
		authorized = true
		if len(scope.ClientIDs) == 0 || len(scope.ClientIDs) > 128 || len(scope.RepositoryOwners) == 0 || !catalogContains(scope.AllowedProfiles, scope.DefaultProfile) {
			return automationagent.WorkspaceCatalog{}, http.StatusServiceUnavailable, errors.New("workspace catalog scope invalid")
		}
		for _, id := range scope.ClientIDs {
			parsed, err := uuid.FromString(id)
			if err != nil || parsed == uuid.Nil {
				return automationagent.WorkspaceCatalog{}, http.StatusServiceUnavailable, errors.New("workspace catalog scope invalid")
			}
		}
		var sources []models.DeliveryContextSource
		query := configuration.DB.Table("delivery_context_sources AS source").Select("source.*").
			Joins("JOIN delivery_projects AS project ON project.id = source.project_id").
			Joins("JOIN clients AS client ON client.id = project.client_id").
			Where("project.client_id IN ? AND project.status = ? AND project.deleted_at IS NULL AND client.is_active = ? AND client.deleted_at IS NULL AND source.kind = ? AND source.status = ? AND source.reference LIKE ?", scope.ClientIDs, "active", true, "repository", "ready", "github://%").Order("source.id").Limit(automationagent.MaxWorkspaceCatalogEntries + 1).Find(&sources)
		if query.Error != nil {
			return automationagent.WorkspaceCatalog{}, http.StatusServiceUnavailable, errors.New("workspace catalog query unavailable")
		}
		for _, source := range sources {
			repository := strings.TrimPrefix(source.Reference, "github://")
			owner, _, valid := strings.Cut(repository, "/")
			if !valid || !catalogContains(scope.RepositoryOwners, strings.ToLower(owner)) {
				continue
			}
			var metadata struct {
				Branch    string                              `json:"github_default_branch"`
				Profile   string                              `json:"workspace_profile"`
				Inventory automationagent.GitHubRepositoryMap `json:"github_code_map"`
			}
			if json.Unmarshal([]byte(source.MetadataJSON), &metadata) != nil {
				continue
			}
			profile := metadata.Profile
			if profile == "" {
				profile = catalogManifestProfile(scope, metadata.Inventory, source.Revision)
			}
			if !catalogContains(scope.AllowedProfiles, profile) {
				continue
			}
			entry := automationagent.WorkspaceCatalogEntry{ID: automationagent.CatalogWorkspaceID(source.ID.String()), ProjectID: source.ProjectID.String(), SourceID: source.ID.String(), Repository: repository, Branch: metadata.Branch, Revision: strings.ToLower(source.Revision), Profile: profile}
			if entry.Validate() != nil || seen[entry.ID] {
				continue
			}
			entries = append(entries, entry)
			seen[entry.ID] = true
		}
		if len(sources) > automationagent.MaxWorkspaceCatalogEntries || len(entries) > automationagent.MaxWorkspaceCatalogEntries {
			return automationagent.WorkspaceCatalog{}, http.StatusServiceUnavailable, errors.New("workspace catalog exceeds bounded capacity")
		}
	}
	if !authorized {
		return automationagent.WorkspaceCatalog{}, http.StatusForbidden, errors.New("workspace catalog scope unavailable")
	}
	return automationagent.WorkspaceCatalog{Version: automationagent.WorkspaceCatalogVersion, Digest: automationagent.WorkspaceCatalogDigest(entries, raw), ExpiresAt: time.Now().UTC().Add(5 * time.Minute), Entries: entries}, http.StatusOK, nil
}

func WorkspaceCatalog(c echo.Context) error {
	catalog, status, err := workspaceCatalogForRequest(c)
	if err != nil {
		return utils.Error(c, status, err.Error(), "")
	}
	return utils.Success(c, status, "Authorized workspace catalog", catalog)
}
