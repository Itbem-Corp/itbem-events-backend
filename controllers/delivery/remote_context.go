package delivery

import (
	"encoding/json"
	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/utils"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// applyWorkspaceDeliveryMetadata projects only the safe, inventory-derived
// workspace signals that help a human classify a repository in the project
// map. It never reads secrets, parses manifests for dependencies, or changes
// the repository; the agent still receives the fuller bounded context only
// inside a frozen task run.
func applyWorkspaceDeliveryMetadata(metadata map[string]any, workspace automationagent.Workspace) {
	metadata["workspace_capabilities"] = append([]string(nil), workspace.Config.Capabilities...)
	metadata["workspace_harness"] = workspace.Harness()
	if context, err := automationagent.DescribeWorkspace(workspace, nil); err == nil {
		metadata["workspace_architecture"] = context.Architecture
	}
}

// RefreshRemoteRepositoryContext reads the latest default-branch revision for
// an explicitly registered github:// source. It is a human-triggered,
// read-only operation: frozen task snapshots remain untouched and no workspace
// is fetched, pulled, committed or published.
func RefreshRemoteRepositoryContext(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryManage); err != nil {
		return err
	}
	sourceID, err := uuid.FromString(c.Param("sourceID"))
	if err != nil || sourceID == uuid.Nil {
		return badRequest(c, "Invalid context source", "source ID must be a UUID")
	}
	var source models.DeliveryContextSource
	if err := configuration.DB.Where("id = ? AND project_id = ?", sourceID, projectID).First(&source).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Delivery context source not found", "")
		}
		return utilsError(c, err)
	}
	if source.Kind != "repository" || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(source.Reference)), "github://") {
		return conflict(c, "Remote context refresh rejected", "Only an explicitly registered github:// repository can be refreshed remotely")
	}
	appConfig, err := automationagent.LoadGitHubAppConfig(os.Getenv)
	if err != nil {
		return conflict(c, "Remote context refresh unavailable", "GitHub App must be configured before remote repository context can be read")
	}
	installation, err := automationagent.MintGitHubInstallationToken(c.Request().Context(), appConfig, nil, time.Now().UTC())
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Remote context refresh unavailable", "Could not obtain a short-lived GitHub App token")
	}
	snapshot, err := automationagent.ReadGitHubRepositorySnapshot(c.Request().Context(), appConfig, installation.Token, source.Reference)
	if err != nil {
		return conflict(c, "Remote context refresh rejected", "GitHub could not verify the registered repository reference or its default branch")
	}
	metadata := map[string]any{}
	if strings.TrimSpace(source.MetadataJSON) != "" && json.Unmarshal([]byte(source.MetadataJSON), &metadata) != nil {
		return conflict(c, "Remote context refresh rejected", "The registered context metadata is invalid and must be repaired before refreshing")
	}
	now := time.Now().UTC()
	metadata["github_default_branch"] = snapshot.DefaultBranch
	metadata["github_synced_at"] = now.Format(time.RFC3339)
	// A repository tree is an optional orientation aid for planning. A GitHub
	// App that has metadata access but not Contents access can still refresh a
	// checkpoint; it simply remains metadata-only. Never retain an old map when
	// the frozen revision changes.
	delete(metadata, "github_code_map")
	delete(metadata, "github_code_context")
	metadata["github_context_mode"] = "metadata_only"
	if codeMap, mapErr := automationagent.ReadGitHubRepositoryMap(c.Request().Context(), appConfig, installation.Token, snapshot); mapErr == nil {
		metadata["github_code_map"] = codeMap
		metadata["github_context_mode"] = "inventory_only"
		// Contents access is optional. When it is granted, retain a small,
		// redacted, revision-pinned orientation context so a planner can reason
		// across related remote repositories without acquiring write authority.
		if sourceContext, contextErr := automationagent.ReadGitHubRepositorySourceContext(c.Request().Context(), appConfig, installation.Token, snapshot, codeMap); contextErr == nil && len(sourceContext.Excerpts) > 0 {
			metadata["github_code_context"] = sourceContext
			metadata["github_context_mode"] = "bounded_source"
		}
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return utilsError(c, err)
	}
	updates := map[string]any{"reference": snapshot.Reference, "revision": snapshot.Revision, "status": "ready", "metadata_json": string(encodedMetadata), "synced_at": now}
	if err := configuration.DB.Model(&source).Updates(updates).Error; err != nil {
		return utilsError(c, err)
	}
	source.Reference, source.Revision, source.Status, source.MetadataJSON, source.SyncedAt = snapshot.Reference, snapshot.Revision, "ready", string(encodedMetadata), &now
	return success(c, "Remote repository context refreshed", source)
}

// RefreshLocalWorkspaceContext records the current local Git checkpoint and
// the bounded capabilities configured for a registered workspace. It is a
// human-triggered observation only: no fetch, pull, add, commit, push or
// modification of the developer workspace occurs here.
func RefreshLocalWorkspaceContext(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryManage); err != nil {
		return err
	}
	sourceID, err := uuid.FromString(c.Param("sourceID"))
	if err != nil || sourceID == uuid.Nil {
		return badRequest(c, "Invalid context source", "source ID must be a UUID")
	}
	var source models.DeliveryContextSource
	if err := configuration.DB.Where("id = ? AND project_id = ?", sourceID, projectID).First(&source).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Delivery context source not found", "")
		}
		return utilsError(c, err)
	}
	if source.Kind != "repository" || !strings.HasPrefix(strings.TrimSpace(source.Reference), "workspace://") {
		return conflict(c, "Local context refresh rejected", "Only an explicitly registered workspace:// repository can be refreshed locally")
	}
	workspace, err := automationagent.RegisteredWorkspace(source.Reference, os.Getenv)
	if err != nil {
		return conflict(c, "Local context refresh unavailable", "The workspace is no longer registered or available on this control-plane host")
	}
	gitState := automationagent.ReadWorkspaceGitState(workspace)
	if !gitState.Available || strings.TrimSpace(gitState.HeadSHA) == "" {
		return conflict(c, "Local context refresh unavailable", "The registered workspace is not a readable Git repository with a checked-out revision")
	}
	if gitState.HasLocalChanges {
		return conflict(c, "Local context refresh rejected", "Commit or stash local changes before refreshing the Delivery checkpoint")
	}
	if gitState.RemoteAhead > 0 {
		return conflict(c, "Local context refresh rejected", "Synchronize the workspace with its known tracking branch before refreshing the Delivery checkpoint")
	}
	metadata := map[string]any{}
	if strings.TrimSpace(source.MetadataJSON) != "" && json.Unmarshal([]byte(source.MetadataJSON), &metadata) != nil {
		return conflict(c, "Local context refresh rejected", "The registered context metadata is invalid and must be repaired before refreshing")
	}
	now := time.Now().UTC()
	applyWorkspaceDeliveryMetadata(metadata, workspace)
	metadata["local_git_branch"] = gitState.Branch
	metadata["local_workspace_dirty"] = gitState.HasLocalChanges
	metadata["local_change_count"] = gitState.LocalChangeCount
	metadata["local_tracking_branch"] = gitState.TrackingBranch
	metadata["local_ahead"] = gitState.LocalAhead
	metadata["remote_ahead"] = gitState.RemoteAhead
	metadata["local_checkpointed_at"] = now.Format(time.RFC3339)
	if gitState.GitHubRepository != "" {
		metadata["github_repository"] = gitState.GitHubRepository
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return utilsError(c, err)
	}
	updates := map[string]any{"revision": gitState.HeadSHA, "status": "ready", "metadata_json": string(encodedMetadata), "synced_at": now}
	if err := configuration.DB.Model(&source).Updates(updates).Error; err != nil {
		return utilsError(c, err)
	}
	source.Revision, source.Status, source.MetadataJSON, source.SyncedAt = gitState.HeadSHA, "ready", string(encodedMetadata), &now
	return success(c, "Local workspace context refreshed", source)
}

// FetchLocalWorkspaceRemoteRefs refreshes origin refs only for a workspace
// explicitly granted repository:fetch. It is deliberately separate from the
// checkpoint refresh: fetching never changes HEAD, the worktree, the frozen
// source revision, or any task snapshot. A person must still update their
// branch and explicitly create a new checkpoint before a changed base can be
// used by a Delivery plan.
func FetchLocalWorkspaceRemoteRefs(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	actor, err := projectActor(c, projectID, deliveryManage)
	if err != nil {
		return err
	}
	sourceID, err := uuid.FromString(c.Param("sourceID"))
	if err != nil || sourceID == uuid.Nil {
		return badRequest(c, "Invalid context source", "source ID must be a UUID")
	}
	var source models.DeliveryContextSource
	if err := configuration.DB.Where("id = ? AND project_id = ?", sourceID, projectID).First(&source).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Delivery context source not found", "")
		}
		return utilsError(c, err)
	}
	if source.Kind != "repository" || !strings.HasPrefix(strings.TrimSpace(source.Reference), "workspace://") {
		return conflict(c, "Local remote fetch rejected", "Only an explicitly registered workspace:// repository can refresh remote references")
	}
	workspace, err := automationagent.RegisteredWorkspace(source.Reference, os.Getenv)
	if err != nil {
		return conflict(c, "Local remote fetch unavailable", "The workspace is no longer registered or available on this control-plane host")
	}
	gitState, err := automationagent.FetchWorkspaceRemote(c.Request().Context(), workspace)
	if err != nil {
		return conflict(c, "Local remote fetch rejected", "The workspace is not granted repository:fetch or its origin could not be fetched without interaction")
	}
	metadata := map[string]any{}
	if strings.TrimSpace(source.MetadataJSON) != "" && json.Unmarshal([]byte(source.MetadataJSON), &metadata) != nil {
		return conflict(c, "Local remote fetch rejected", "The registered context metadata is invalid and must be repaired before refreshing")
	}
	now := time.Now().UTC()
	applyWorkspaceDeliveryMetadata(metadata, workspace)
	metadata["local_remote_refs_fetched_at"] = now.Format(time.RFC3339)
	metadata["local_remote_refs_fetched_by_user_id"] = actor.ID.String()
	metadata["local_tracking_branch"] = gitState.TrackingBranch
	metadata["local_ahead"] = gitState.LocalAhead
	metadata["remote_ahead"] = gitState.RemoteAhead
	if gitState.GitHubRepository != "" {
		metadata["github_repository"] = gitState.GitHubRepository
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return utilsError(c, err)
	}
	if err := configuration.DB.Model(&source).Update("metadata_json", string(encodedMetadata)).Error; err != nil {
		return utilsError(c, err)
	}
	source.MetadataJSON = string(encodedMetadata)
	return success(c, "Local remote references refreshed", map[string]any{"source": source, "git": gitState, "checkpoint_changed": false})
}
