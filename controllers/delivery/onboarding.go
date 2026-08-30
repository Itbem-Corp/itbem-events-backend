package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/projectvault"
	"events-stocks/models"
	automationqueue "events-stocks/repositories/automationqueuerepository"
	awsrepository "events-stocks/repositories/awsrepository"
	outboxService "events-stocks/services/outbox"
	"events-stocks/utils"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type repositoryOnboardingRequest struct {
	RepositoryURL string `json:"repository_url"`
	Revision      string `json:"revision,omitempty"`
}

type repositoryOnboardingApprovalRequest struct {
	ExpectedRevision       string `json:"expected_revision"`
	ExpectedProposalSHA256 string `json:"expected_proposal_sha256"`
}

type repositoryOnboardingProbeRequest struct {
	ExpectedRevision   string   `json:"expected_revision"`
	WorkspaceReference string   `json:"workspace_reference"`
	Capabilities       []string `json:"capabilities"`
}

type repositoryCapabilityProbeTask struct {
	ID           uuid.UUID  `json:"id"`
	Status       string     `json:"status"`
	ErrorMessage string     `json:"error_message,omitempty"`
	AttemptCount int        `json:"attempt_count"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

var errOnboardingRejected = errors.New("repository onboarding approval rejected")
var onboardingProposalDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

// InspectRepositoryOnboarding performs only bounded, read-only GitHub API
// calls. It never clones code, executes repository commands or treats README
// prose as instructions. The result remains a proposal until a human approves
// the exact inspected SHA through ApproveRepositoryOnboarding.
func InspectRepositoryOnboarding(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	actor, err := projectActor(c, projectID, deliveryManage)
	if err != nil {
		return err
	}
	if err := projectPresent(projectID); err != nil {
		return lookup(c, "Delivery project", err)
	}
	var request repositoryOnboardingRequest
	if err := c.Bind(&request); err != nil {
		return badRequest(c, "Invalid repository onboarding", "repository_url is required")
	}
	reference, err := projectvault.CanonicalGitHubReference(request.RepositoryURL)
	if err != nil {
		return badRequest(c, "Invalid repository onboarding", err.Error())
	}
	expectedRevision := strings.ToLower(strings.TrimSpace(request.Revision))
	if expectedRevision != "" && !projectvault.ValidRevision(expectedRevision) {
		return badRequest(c, "Invalid repository onboarding", "revision must be an immutable full Git commit SHA")
	}
	appConfig, err := automationagent.LoadGitHubAppConfig(os.Getenv)
	if err != nil {
		return conflict(c, "Repository onboarding unavailable", "Configure the GitHub App before inspecting a repository")
	}
	proposal, err := inspectGitHubRepositoryForOnboarding(c.Request().Context(), appConfig, reference, expectedRevision, time.Now().UTC())
	if err != nil {
		return conflict(c, "Repository onboarding blocked", "No configured GitHub App installation could inspect the repository and its immutable tree")
	}
	proposal, err = reconcileOnboardingProposal(configuration.DB, projectID, proposal)
	if err != nil {
		return conflict(c, "Repository Vault reconciliation blocked", "The latest approved Vault could not be verified and reconciled safely")
	}
	proposalJSON, err := json.Marshal(proposal)
	if err != nil {
		return utilsError(c, err)
	}
	proposalSHA256, err := projectvault.ProposalSHA256(proposal)
	if err != nil {
		return utilsError(c, err)
	}
	var existing models.DeliveryRepositoryOnboarding
	find := configuration.DB.Where("project_id = ? AND repository_reference = ? AND revision = ?", projectID, proposal.Repository.Reference, proposal.Repository.Revision).First(&existing)
	if find.Error == nil {
		return success(c, "Repository onboarding already inspected", existing)
	}
	if find.Error != gorm.ErrRecordNotFound {
		return utilsError(c, find.Error)
	}
	onboarding := models.DeliveryRepositoryOnboarding{
		ProjectID: projectID, RepositoryReference: proposal.Repository.Reference,
		DefaultBranch: proposal.Repository.DefaultBranch, Revision: proposal.Repository.Revision,
		Status: "proposed", Readiness: proposal.Readiness, ProposalJSON: string(proposalJSON),
		ProposalSHA256: proposalSHA256, VaultSHA256: proposal.VaultSHA256,
		ProposedBy: actor.CognitoSub,
	}
	if err := configuration.DB.Create(&onboarding).Error; err != nil {
		// A concurrent retry may have won the unique checkpoint race. Return that
		// same immutable proposal instead of creating a second workflow.
		if retryErr := configuration.DB.Where("project_id = ? AND repository_reference = ? AND revision = ?", projectID, proposal.Repository.Reference, proposal.Repository.Revision).First(&existing).Error; retryErr == nil {
			return success(c, "Repository onboarding already inspected", existing)
		}
		return utilsError(c, err)
	}
	return created(c, "Repository onboarding proposed", onboarding)
}

func inspectGitHubRepositoryForOnboarding(ctx context.Context, config automationagent.GitHubAppConfig, reference, expectedRevision string, now time.Time) (projectvault.Proposal, error) {
	configs := []automationagent.GitHubAppConfig{config}
	if len(config.InstallationIDs) > 0 {
		configs = make([]automationagent.GitHubAppConfig, 0, len(config.InstallationIDs))
		for _, rawID := range config.InstallationIDs {
			id, err := strconv.ParseInt(rawID, 10, 64)
			if err != nil {
				continue
			}
			candidate, err := config.WithInstallationID(id)
			if err == nil {
				configs = append(configs, candidate)
			}
		}
	}
	var lastErr error
	for _, candidate := range configs {
		installation, err := automationagent.MintGitHubInstallationToken(ctx, candidate, nil, now)
		if err != nil {
			lastErr = err
			continue
		}
		snapshot, err := automationagent.ReadGitHubRepositorySnapshotAtRevision(ctx, candidate, installation.Token, reference, expectedRevision)
		if err != nil {
			lastErr = err
			continue
		}
		inventory, err := automationagent.ReadGitHubRepositoryMap(ctx, candidate, installation.Token, snapshot)
		if err != nil {
			lastErr = err
			continue
		}
		excerpts := make([]projectvault.Excerpt, 0)
		if source, sourceErr := automationagent.ReadGitHubRepositorySourceContext(ctx, candidate, installation.Token, snapshot, inventory); sourceErr == nil {
			for _, excerpt := range source.Excerpts {
				excerpts = append(excerpts, projectvault.Excerpt{Path: excerpt.Path, Content: excerpt.Content})
			}
		}
		environment := make([]projectvault.EnvironmentDeclaration, 0)
		if declarations, declarationErr := automationagent.ReadGitHubRepositoryEnvironmentDeclarations(ctx, candidate, installation.Token, snapshot, inventory); declarationErr == nil {
			for _, declaration := range declarations {
				environment = append(environment, projectvault.EnvironmentDeclaration{Path: declaration.Path, Names: declaration.Names})
			}
		}
		return projectvault.Build(projectvault.Input{
			Repository: projectvault.Repository{Reference: snapshot.Reference, DefaultBranch: snapshot.DefaultBranch, Revision: snapshot.Revision},
			Files:      inventory.Files, InventoryFileCount: inventory.FileCount,
			InventoryTruncated: inventory.InventoryTruncated, Excerpts: excerpts, EnvironmentDeclarations: environment,
		})
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no configured GitHub App installations")
	}
	return projectvault.Proposal{}, lastErr
}

func reconcileOnboardingProposal(db *gorm.DB, projectID uuid.UUID, proposal projectvault.Proposal) (projectvault.Proposal, error) {
	if db == nil || projectID == uuid.Nil {
		return projectvault.Proposal{}, fmt.Errorf("vault reconciliation context is invalid")
	}
	var previous models.DeliveryProjectVaultRevision
	err := db.Where("project_id = ? AND repository_reference = ?", projectID, proposal.Repository.Reference).
		Order("version DESC, published_at DESC, id DESC").First(&previous).Error
	if err == gorm.ErrRecordNotFound {
		return proposal, nil
	}
	if err != nil {
		return projectvault.Proposal{}, err
	}
	var manifest projectvault.Manifest
	if previous.ProjectID != projectID || previous.RepositoryReference != proposal.Repository.Reference || previous.SchemaVersion != projectvault.SchemaVersion ||
		json.Unmarshal([]byte(previous.ManifestJSON), &manifest) != nil || manifest.Repository.Reference != previous.RepositoryReference || !strings.EqualFold(manifest.Repository.Revision, previous.Revision) {
		return projectvault.Proposal{}, fmt.Errorf("latest approved Vault identity is invalid")
	}
	digest, err := projectvault.ManifestSHA256(manifest)
	if err != nil || !strings.EqualFold(digest, previous.ContentSHA256) {
		return projectvault.Proposal{}, fmt.Errorf("latest approved Vault digest is invalid")
	}
	var source models.DeliveryRepositoryOnboarding
	if err := db.Where("id = ? AND project_id = ?", previous.SourceOnboardingID, projectID).First(&source).Error; err != nil {
		return projectvault.Proposal{}, fmt.Errorf("latest approved Vault provenance is missing")
	}
	if source.Status != "approved" || source.RepositoryReference != previous.RepositoryReference || !strings.EqualFold(source.Revision, previous.Revision) || !strings.EqualFold(source.VaultSHA256, previous.ContentSHA256) || source.ApprovedAt == nil || strings.TrimSpace(source.ApprovedBy) == "" || !strings.EqualFold(source.ApprovedBy, previous.PublishedBy) || !source.ApprovedAt.UTC().Equal(previous.PublishedAt.UTC()) {
		return projectvault.Proposal{}, fmt.Errorf("latest approved Vault provenance is invalid")
	}
	return projectvault.Reconcile(proposal, manifest)
}

// ApproveRepositoryOnboarding is the explicit human boundary that publishes a
// repository checkpoint and its next immutable Vault revision. The caller
// must repeat the expected SHA, preventing a stale UI approval from silently
// accepting a later repository state.
func ApproveRepositoryOnboarding(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	actor, err := projectActor(c, projectID, deliveryManage)
	if err != nil {
		return err
	}
	onboardingID, err := uuid.FromString(strings.TrimSpace(c.Param("onboardingID")))
	if err != nil || onboardingID == uuid.Nil {
		return badRequest(c, "Invalid repository onboarding", "onboarding ID must be a UUID")
	}
	var request repositoryOnboardingApprovalRequest
	if err := c.Bind(&request); err != nil {
		return badRequest(c, "Invalid repository onboarding approval", "expected_revision is required")
	}
	expectedRevision := strings.ToLower(strings.TrimSpace(request.ExpectedRevision))
	expectedProposalSHA256 := strings.ToLower(strings.TrimSpace(request.ExpectedProposalSHA256))
	if !projectvault.ValidRevision(expectedRevision) || !onboardingProposalDigest.MatchString(expectedProposalSHA256) {
		return badRequest(c, "Invalid repository onboarding approval", "expected_revision and expected_proposal_sha256 must identify the reviewed proposal")
	}
	var approved models.DeliveryRepositoryOnboarding
	var vault models.DeliveryProjectVaultRevision
	var rejection string
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		// Locking the project serializes version allocation across different
		// repository approvals without introducing a global onboarding lock.
		var project models.DeliveryProject
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&project, projectID).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND project_id = ?", onboardingID, projectID).First(&approved).Error; err != nil {
			return err
		}
		if approved.Status == "approved" {
			return tx.Where("source_onboarding_id = ?", approved.ID).First(&vault).Error
		}
		if approved.Status != "proposed" || approved.Readiness == "blocked" {
			rejection = "Only a non-blocked proposed onboarding can be approved"
			return errOnboardingRejected
		}
		if !strings.EqualFold(approved.Revision, expectedRevision) {
			rejection = "The inspected revision changed in the approval request; inspect again before approving"
			return errOnboardingRejected
		}
		if !strings.EqualFold(approved.ProposalSHA256, expectedProposalSHA256) {
			rejection = "The capability matrix or Vault proposal changed; review the latest proposal digest before approving"
			return errOnboardingRejected
		}
		var activeProbes int64
		if err := tx.Model(&models.AutomationTask{}).Where("delivery_onboarding_id = ? AND operation = ? AND status IN ?", approved.ID, "delivery.onboarding_probe", []string{"queued", "running", "cancel_requested"}).Count(&activeProbes).Error; err != nil {
			return err
		}
		if activeProbes > 0 {
			rejection = "A capability probe is still active; wait for its exact-SHA evidence and review the updated proposal"
			return errOnboardingRejected
		}
		proposal, err := validateStoredOnboardingProposal(approved)
		if err != nil {
			rejection = "The stored proposal no longer matches its immutable repository checkpoint"
			return errOnboardingRejected
		}
		manifestJSON, err := json.Marshal(proposal.Vault)
		if err != nil {
			return err
		}
		metadata := map[string]any{
			"github_default_branch": proposal.Repository.DefaultBranch,
			"github_context_mode":   "static_onboarding",
			"repository_kind":       "unclassified",
			"onboarding_id":         approved.ID.String(),
			"vault_sha256":          proposal.VaultSHA256,
			"capability_matrix":     proposal.Capabilities,
		}
		metadata, err = normalizeRepositoryContextMetadata(approved.RepositoryReference, metadata)
		if err != nil {
			return err
		}
		encodedMetadata, err := json.Marshal(metadata)
		if err != nil || len(encodedMetadata) > 16*1024 {
			rejection = "The approved repository metadata exceeds the safe context boundary"
			return errOnboardingRejected
		}
		now := time.Now().UTC()
		var sources []models.DeliveryContextSource
		if err := tx.Where("project_id = ? AND kind = ? AND reference = ?", projectID, "repository", approved.RepositoryReference).Limit(2).Find(&sources).Error; err != nil {
			return err
		}
		if len(sources) > 1 {
			rejection = "Duplicate repository context must be reconciled before onboarding can be approved"
			return errOnboardingRejected
		}
		if len(sources) == 0 {
			source := models.DeliveryContextSource{ProjectID: projectID, Kind: "repository", Name: onboardingRepositoryName(approved.RepositoryReference), Reference: approved.RepositoryReference, Revision: approved.Revision, Status: "ready", MetadataJSON: string(encodedMetadata), SyncedAt: &now}
			if err := tx.Create(&source).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Model(&sources[0]).Updates(map[string]any{"revision": approved.Revision, "status": "ready", "metadata_json": string(encodedMetadata), "synced_at": now}).Error; err != nil {
				return err
			}
		}
		var latest struct{ Version int64 }
		if err := tx.Model(&models.DeliveryProjectVaultRevision{}).Select("COALESCE(MAX(version), 0) AS version").Where("project_id = ? AND repository_reference = ?", projectID, approved.RepositoryReference).Scan(&latest).Error; err != nil {
			return err
		}
		vault = models.DeliveryProjectVaultRevision{
			ProjectID: projectID, RepositoryReference: approved.RepositoryReference,
			Version: latest.Version + 1, Revision: approved.Revision, SchemaVersion: proposal.SchemaVersion,
			ManifestJSON: string(manifestJSON), ContentSHA256: proposal.VaultSHA256,
			SourceOnboardingID: approved.ID, PublishedBy: actor.CognitoSub, PublishedAt: now,
		}
		if err := tx.Create(&vault).Error; err != nil {
			return err
		}
		if err := tx.Model(&approved).Updates(map[string]any{"status": "approved", "approved_by": actor.CognitoSub, "approved_at": now}).Error; err != nil {
			return err
		}
		approved.Status, approved.ApprovedBy, approved.ApprovedAt = "approved", actor.CognitoSub, &now
		return nil
	})
	if errors.Is(err, errOnboardingRejected) {
		return conflict(c, "Repository onboarding approval rejected", rejection)
	}
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Repository onboarding not found", "")
		}
		return utilsError(c, err)
	}
	return success(c, "Repository onboarding approved", map[string]any{"onboarding": approved, "vault_revision": vault})
}

// ProbeRepositoryOnboarding queues a deterministic QA-lane dry-run for the
// exact inspected SHA. The request chooses only capability identities and a
// registered workspace ID; executable argv remains operator-owned on Linux.
func ProbeRepositoryOnboarding(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	actor, err := projectActor(c, projectID, deliveryManage)
	if err != nil {
		return err
	}
	onboardingID, err := uuid.FromString(strings.TrimSpace(c.Param("onboardingID")))
	if err != nil || onboardingID == uuid.Nil {
		return badRequest(c, "Invalid repository onboarding", "onboarding ID must be a UUID")
	}
	var request repositoryOnboardingProbeRequest
	if err := c.Bind(&request); err != nil {
		return badRequest(c, "Invalid repository onboarding probe", "expected_revision, workspace_reference, and capabilities are required")
	}
	expectedRevision := strings.ToLower(strings.TrimSpace(request.ExpectedRevision))
	capabilities, err := automationagent.NormalizeOnboardingProbeCapabilities(request.Capabilities)
	if err != nil || !projectvault.ValidRevision(expectedRevision) {
		return badRequest(c, "Invalid repository onboarding probe", "only supported command capabilities and the inspected full Git SHA may be probed")
	}
	cfg, _ := c.Get("config").(*models.Config)
	if cfg == nil || strings.TrimSpace(cfg.AutomationInputBucket) == "" || !automationqueue.IsConfigured() {
		return conflict(c, "Repository onboarding probe unavailable", "The automation queue or private input storage is not configured")
	}
	var onboarding models.DeliveryRepositoryOnboarding
	if err := configuration.DB.Where("id = ? AND project_id = ?", onboardingID, projectID).First(&onboarding).Error; err != nil {
		return lookup(c, "Repository onboarding", err)
	}
	if onboarding.Status != "proposed" || !strings.EqualFold(onboarding.Revision, expectedRevision) {
		return conflict(c, "Repository onboarding probe rejected", "Probe the current proposed onboarding at its exact inspected SHA")
	}
	if _, err := validateStoredOnboardingProposal(onboarding); err != nil {
		return conflict(c, "Repository onboarding probe rejected", "The stored proposal no longer matches its immutable checkpoint")
	}
	delivery, err := automationagent.BuildOnboardingProbeDelivery(request.WorkspaceReference, onboarding.RepositoryReference, onboarding.DefaultBranch, onboarding.Revision, capabilities)
	if err != nil {
		return badRequest(c, "Invalid repository onboarding probe", err.Error())
	}
	taskID, jobID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	inputJSON, err := json.Marshal(automationagent.TaskInput{Delivery: delivery})
	if err != nil {
		return utilsError(c, err)
	}
	inputKey := "automation/inputs/" + taskID.String() + "/input.json"
	if err := awsrepository.UploadEncryptedJSON(c.Request().Context(), inputJSON, inputKey, cfg.AutomationInputBucket); err != nil {
		return conflict(c, "Repository onboarding probe unavailable", "Could not write the private exact-SHA probe input")
	}
	task := models.AutomationTask{
		ID: taskID, JobID: jobID, RequestedBy: actor.CognitoSub, DeliveryOnboardingID: &onboarding.ID,
		CorrelationID: "onboarding:" + onboarding.ID.String(), Operation: "delivery.onboarding_probe",
		EvidenceSubjectDigest: onboarding.ProposalSHA256, MaxCompletionTokens: 0,
		InputRef: "s3://" + cfg.AutomationInputBucket + "/" + inputKey, Status: "queued",
	}
	message := automationqueue.Message{SchemaVersion: 1, JobID: jobID.String(), TenantCode: "itbem", CorrelationID: task.CorrelationID, Type: "ai.local.process"}
	message.Payload.TaskID, message.Payload.Operation, message.Payload.MaxCompletionTokens, message.Payload.InputRef, message.Payload.Attempt = task.ID.String(), task.Operation, 0, task.InputRef, 1
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		var lockedProject models.DeliveryProject
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&lockedProject, projectID).Error; err != nil {
			return err
		}
		var locked models.DeliveryRepositoryOnboarding
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND project_id = ?", onboarding.ID, projectID).First(&locked).Error; err != nil {
			return err
		}
		if locked.Status != "proposed" || !strings.EqualFold(locked.Revision, expectedRevision) || !strings.EqualFold(locked.ProposalSHA256, task.EvidenceSubjectDigest) {
			return fmt.Errorf("onboarding proposal changed before the probe was queued")
		}
		var active int64
		if err := tx.Model(&models.AutomationTask{}).Where("delivery_onboarding_id = ? AND operation = ? AND status IN ?", locked.ID, task.Operation, []string{"queued", "running", "cancel_requested"}).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return fmt.Errorf("an active capability probe already exists for this onboarding")
		}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		enqueued, err := outboxService.EnqueueAutomationProcess(c.Request().Context(), tx, message)
		if err != nil || !enqueued {
			if err != nil {
				return err
			}
			return fmt.Errorf("onboarding capability probe was not enqueued")
		}
		return nil
	}); err != nil {
		return conflict(c, "Repository onboarding probe rejected", err.Error())
	}
	return utils.Success(c, http.StatusAccepted, "Repository onboarding capability probe queued", task)
}

func ListRepositoryCapabilityProbes(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryView); err != nil {
		return err
	}
	onboardingID, err := uuid.FromString(strings.TrimSpace(c.Param("onboardingID")))
	if err != nil || onboardingID == uuid.Nil {
		return badRequest(c, "Invalid repository onboarding", "onboarding ID must be a UUID")
	}
	var onboarding models.DeliveryRepositoryOnboarding
	if err := configuration.DB.Select("id").Where("id = ? AND project_id = ?", onboardingID, projectID).First(&onboarding).Error; err != nil {
		return lookup(c, "Repository onboarding", err)
	}
	var probes []models.DeliveryRepositoryCapabilityProbe
	if err := configuration.DB.Where("project_id = ? AND onboarding_id = ?", projectID, onboardingID).Order("observed_at DESC, created_at DESC, id DESC").Limit(200).Find(&probes).Error; err != nil {
		return utilsError(c, err)
	}
	var tasks []models.AutomationTask
	if err := configuration.DB.Select("id", "status", "error_message", "attempt_count", "completed_at", "created_at").Where("delivery_onboarding_id = ? AND operation = ?", onboardingID, "delivery.onboarding_probe").Order("created_at DESC, id DESC").Limit(50).Find(&tasks).Error; err != nil {
		return utilsError(c, err)
	}
	taskViews := make([]repositoryCapabilityProbeTask, 0, len(tasks))
	for _, task := range tasks {
		taskViews = append(taskViews, repositoryCapabilityProbeTask{
			ID: task.ID, Status: task.Status, ErrorMessage: task.ErrorMessage,
			AttemptCount: task.AttemptCount, CompletedAt: task.CompletedAt, CreatedAt: task.CreatedAt,
		})
	}
	return success(c, "Repository capability probes retrieved", map[string]any{"probes": probes, "tasks": taskViews})
}

func validateStoredOnboardingProposal(onboarding models.DeliveryRepositoryOnboarding) (projectvault.Proposal, error) {
	return projectvault.ValidateStoredProposal(
		onboarding.ProposalJSON, onboarding.RepositoryReference, onboarding.DefaultBranch,
		onboarding.Revision, onboarding.Readiness, onboarding.ProposalSHA256, onboarding.VaultSHA256,
	)
}

func onboardingRepositoryName(reference string) string {
	parts := strings.Split(strings.TrimPrefix(reference, "github://"), "/")
	if len(parts) == 2 {
		return parts[0] + "/" + parts[1]
	}
	return reference
}

func ListRepositoryOnboardings(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryView); err != nil {
		return err
	}
	var values []models.DeliveryRepositoryOnboarding
	if err := configuration.DB.Where("project_id = ?", projectID).Order("created_at DESC").Find(&values).Error; err != nil {
		return utilsError(c, err)
	}
	return success(c, "Repository onboardings", values)
}

func ListProjectVaultRevisions(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryView); err != nil {
		return err
	}
	var values []models.DeliveryProjectVaultRevision
	if err := configuration.DB.Where("project_id = ?", projectID).Order("repository_reference, version DESC").Find(&values).Error; err != nil {
		return utilsError(c, err)
	}
	return success(c, "Project Vault revisions", values)
}
