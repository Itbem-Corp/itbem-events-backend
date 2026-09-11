package delivery

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/projectvault"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

// freezeWorkItemAutonomy records the authority available to one work item at
// its first run. It never reloads a live policy after that point. Projects
// without a complete policy continue through the existing human-gated flow,
// but receive no snapshot and therefore cannot use delegated progression.
func freezeWorkItemAutonomy(db *gorm.DB, item models.DeliveryWorkItem, project models.DeliveryProject, snapshots []models.DeliveryContextSnapshot, vaults []models.DeliveryProjectVaultRevision, now time.Time) (deliveryledger.AutonomySnapshot, bool, error) {
	if db == nil || item.ID == uuid.Nil || item.ProjectID == uuid.Nil || project.ID != item.ProjectID || now.IsZero() {
		return deliveryledger.AutonomySnapshot{}, false, fmt.Errorf("delivery autonomy freeze context is invalid")
	}
	var stored models.DeliveryEvent
	err := db.Where("work_item_id = ? AND event_type = ?", item.ID, deliveryledger.EventTypeAutonomySnapshot).Order("sequence ASC").First(&stored).Error
	if err == nil {
		projection, projectErr := deliveryledger.ProjectAutonomySnapshot(stored)
		if projectErr != nil || projection.ProjectID != item.ProjectID || projection.ChangeSetID != item.ID.String() {
			return deliveryledger.AutonomySnapshot{}, false, fmt.Errorf("stored delivery autonomy snapshot failed integrity checks")
		}
		return projection, true, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return deliveryledger.AutonomySnapshot{}, false, err
	}

	input := deliveryledger.AutonomySnapshotInput{ProjectID: item.ProjectID, ChangeSetID: item.ID.String(), Repositories: []deliveryledger.AutonomyRepository{}}
	seen := map[string]struct{}{}
	for _, snapshot := range snapshots {
		if !strings.EqualFold(strings.TrimSpace(snapshot.Kind), "repository") {
			continue
		}
		repository, referenceErr := vaultRepositoryReference(snapshot)
		if referenceErr != nil {
			return deliveryledger.AutonomySnapshot{}, false, referenceErr
		}
		key := strings.ToLower(repository)
		if _, duplicate := seen[key]; duplicate {
			return deliveryledger.AutonomySnapshot{}, false, fmt.Errorf("frozen work item contains the repository %s more than once", repository)
		}
		seen[key] = struct{}{}
		vault, vaultErr := frozenWorkItemVault(repository, snapshot.Revision, vaults)
		if vaultErr != nil {
			return deliveryledger.AutonomySnapshot{}, false, vaultErr
		}
		policy, policyErr := resolveEffectiveProjectPolicy(db, project, repository, item.ID.String(), true, now)
		if policyErr != nil {
			return deliveryledger.AutonomySnapshot{}, false, fmt.Errorf("resolve delivery policy for %s: %w", repository, policyErr)
		}
		// A partial configuration is an explicit manual-only posture. We do not
		// persist half an authority snapshot that a later component might treat
		// as delegated by mistake.
		if !policy.Resolved {
			return deliveryledger.AutonomySnapshot{}, false, nil
		}
		input.Repositories = append(input.Repositories, deliveryledger.AutonomyRepository{
			Repository: repository, SourceReference: strings.TrimSpace(snapshot.Reference), SourceRevision: strings.TrimSpace(snapshot.Revision),
			VaultRevisionID: vault.ID.String(), VaultVersion: vault.Version, VaultRevision: vault.Revision, VaultDigest: vault.ContentSHA256,
			Policy: policy,
		})
	}
	if len(input.Repositories) == 0 {
		return deliveryledger.AutonomySnapshot{}, false, nil
	}
	event, _, recordErr := deliveryledger.RecordAutonomySnapshot(db, item.ID, input, now)
	if recordErr != nil {
		return deliveryledger.AutonomySnapshot{}, false, recordErr
	}
	projection, projectErr := deliveryledger.ProjectAutonomySnapshot(event)
	if projectErr != nil || projection.ProjectID != item.ProjectID || projection.ChangeSetID != item.ID.String() {
		return deliveryledger.AutonomySnapshot{}, false, fmt.Errorf("new delivery autonomy snapshot failed integrity checks")
	}
	return projection, true, nil
}

// frozenWorkItemVault verifies the Vault manifest rather than trusting a row
// selected by ID. Both its repository and revision must exactly equal the
// context snapshot taken from main (or the configured source branch).
func frozenWorkItemVault(repository, revision string, values []models.DeliveryProjectVaultRevision) (models.DeliveryProjectVaultRevision, error) {
	repository, err := projectvault.CanonicalGitHubReference(repository)
	if err != nil {
		return models.DeliveryProjectVaultRevision{}, err
	}
	revision = strings.TrimSpace(revision)
	for _, value := range values {
		if !strings.EqualFold(strings.TrimSpace(value.RepositoryReference), repository) || !strings.EqualFold(strings.TrimSpace(value.Revision), revision) {
			continue
		}
		var manifest projectvault.Manifest
		if value.ID == uuid.Nil || value.Version < 1 || value.SchemaVersion != projectvault.SchemaVersion || json.Unmarshal([]byte(value.ManifestJSON), &manifest) != nil {
			continue
		}
		digest, digestErr := projectvault.ManifestSHA256(manifest)
		if digestErr == nil && strings.EqualFold(digest, value.ContentSHA256) && strings.EqualFold(manifest.Repository.Reference, repository) && strings.EqualFold(manifest.Repository.Revision, revision) {
			return value, nil
		}
	}
	return models.DeliveryProjectVaultRevision{}, fmt.Errorf("repository %s has no approved Vault matching frozen SHA %s", repository, revision)
}
