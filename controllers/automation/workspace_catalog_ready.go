package automation

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A fresh catalog authorizes allocation, not successful execution. Only an
// Engineering machine can register an editable source after preparing it with
// its approved sandbox profile; implementation/publication gates remain intact.
func WorkspaceCatalogReady(c echo.Context) error {
	catalog, status, err := workspaceCatalogForRequest(c)
	if err != nil {
		return utils.Error(c, status, err.Error(), "")
	}
	gateway, _ := gatewayIdentityFromRequest(c)
	if gateway.Role != agentwork.RolePrincipalEngineer {
		return utils.Error(c, http.StatusForbidden, "Catalog source registration requires Engineering", "")
	}
	var request automationagent.WorkspaceCatalogReadyRequest
	if c.Bind(&request) != nil || request.Digest != catalog.Digest || len(request.Ready) > automationagent.MaxWorkspaceCatalogEntries {
		return utils.Error(c, http.StatusConflict, "Workspace catalog readiness is stale or invalid", "")
	}
	entries := map[string]automationagent.WorkspaceCatalogEntry{}
	for _, entry := range catalog.Entries {
		entries[entry.ID] = entry
	}
	seen := map[string]bool{}
	for _, ready := range request.Ready {
		entry, ok := entries[ready.ID]
		if !ok || seen[ready.ID] || ready.Revision != entry.Revision || !ready.Readiness.Ready || !ready.Readiness.SandboxReady || ready.Readiness.ID != ready.ID || (ready.Readiness.IsolationMode != "docker" && ready.Readiness.IsolationMode != "firecracker") || !automationagent.CatalogRoleCapabilitiesAllowed(string(gateway.Role), ready.Capabilities) || validateWorkerWorkspaceReadiness([]automationWorkspaceHealth{{ID: ready.ID, Ready: ready.Readiness.Ready, QAReady: ready.Readiness.QAReady, VisualQAReady: ready.Readiness.VisualQAReady, PublicationReady: ready.Readiness.PublicationReady, ValidationCommandCount: ready.Readiness.ValidationCommandCount, QACommandCount: ready.Readiness.QACommandCount}}) != nil {
			return utils.Error(c, http.StatusBadRequest, "Workspace readiness does not match authorized catalog", "")
		}
		seen[ready.ID] = true
	}
	sort.Slice(request.Ready, func(i, j int) bool {
		a, b := entries[request.Ready[i].ID], entries[request.Ready[j].ID]
		if a.ProjectID != b.ProjectID {
			return a.ProjectID < b.ProjectID
		}
		return a.ID < b.ID
	})
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		for _, ready := range request.Ready {
			entry := entries[ready.ID]
			projectID, _ := uuid.FromString(entry.ProjectID)
			var project models.DeliveryProject
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ? AND client_id IN ?", projectID, "active", catalogReadyClientScope(c, entry)).First(&project).Error; err != nil {
				return err
			}
			var checkpoint models.DeliveryContextSource
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND project_id = ? AND status = ?", entry.SourceID, projectID, "ready").First(&checkpoint).Error; err != nil {
				return err
			}
			if checkpoint.Reference != "github://"+entry.Repository || strings.ToLower(checkpoint.Revision) != entry.Revision {
				return errors.New("catalog checkpoint changed")
			}
			metadataValues := map[string]any{}
			if err := json.Unmarshal([]byte(checkpoint.MetadataJSON), &metadataValues); err != nil {
				return err
			}
			if metadataValues == nil {
				metadataValues = map[string]any{}
			}
			metadataValues["github_repository"] = entry.Repository
			metadataValues["linked_source_id"] = entry.SourceID
			metadataValues["catalog_managed"] = true
			metadataValues["catalog_profile"] = entry.Profile
			metadataValues["catalog_digest"] = catalog.Digest
			metadataValues["workspace_capabilities"] = ready.Capabilities
			metadata, err := json.Marshal(metadataValues)
			if err != nil {
				return err
			}
			var source models.DeliveryContextSource
			err = tx.Where("project_id = ? AND reference = ?", projectID, "workspace://"+entry.ID).First(&source).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			now := time.Now().UTC()
			if errors.Is(err, gorm.ErrRecordNotFound) {
				source = models.DeliveryContextSource{ProjectID: projectID, Kind: "repository", Name: checkpoint.Name + " workspace", Reference: "workspace://" + entry.ID, Revision: entry.Revision, Status: "ready", MetadataJSON: string(metadata), SyncedAt: &now}
				if err := tx.Create(&source).Error; err != nil {
					return err
				}
			} else if err := tx.Model(&source).Updates(map[string]any{"revision": entry.Revision, "status": "ready", "metadata_json": string(metadata), "synced_at": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return utils.Error(c, http.StatusConflict, "Workspace catalog checkpoints changed; reconcile again", "")
	}
	return utils.Success(c, http.StatusOK, "Prepared catalog workspaces registered", map[string]any{"registered": len(request.Ready)})
}
