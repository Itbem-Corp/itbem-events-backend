package delivery

import (
	"encoding/json"
	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"events-stocks/utils"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	minimumPublicationGrantMinutes = 5
	maximumPublicationGrantMinutes = 60
	defaultPublicationGrantMinutes = 30
)

var publicationGrantCapabilities = map[string]struct{}{
	"commit:stage":        {},
	"branch:publish":      {},
	"pull_request:create": {},
}

var gitCommitPattern = regexp.MustCompile(`^[a-fA-F0-9]{7,64}$`)
var reviewDiffDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var githubRepositoryPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_.-]*$`)

type publicationGrantInput struct {
	RepositoryRef    string   `json:"repository_ref"`
	BaseSHA          string   `json:"base_sha"`
	Branch           string   `json:"branch"`
	Capabilities     []string `json:"capabilities"`
	ExpiresInMinutes int      `json:"expires_in_minutes"`
	Reason           string   `json:"reason"`
}

type revokePublicationGrantInput struct {
	Reason string `json:"reason"`
}

// ListPublicationGrants exposes scope and expiry, never a credential. A
// reviewer can therefore verify exactly what could have been published.
func ListPublicationGrants(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	if _, _, err := workItemActor(c, workItemID, deliveryView); err != nil {
		return err
	}
	var grants []models.DeliveryPublicationGrant
	if err := configuration.DB.Where("work_item_id = ?", workItemID).Order("granted_at DESC").Find(&grants).Error; err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery publication grants", grants)
}

// CreatePublicationGrant creates a narrow authorization only after a human
// approved the code review. It cannot create a token or publish anything; the
// eventual GitHub App adapter must independently verify this exact scope.
func CreatePublicationGrant(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	actor, item, err := workItemActor(c, workItemID, deliveryManage)
	if err != nil {
		return err
	}
	if readiness := publicationReadinessForEnvironment(os.Getenv); readiness.State != "ready" {
		return conflict(c, "Publication integration unavailable", readiness.Message)
	}
	var input publicationGrantInput
	if err := c.Bind(&input); err != nil {
		return badRequest(c, "Invalid publication grant", err.Error())
	}
	if strings.TrimSpace(input.Reason) == "" || len(strings.TrimSpace(input.Reason)) > 4000 {
		return badRequest(c, "Invalid publication grant", "reason is required and must be at most 4,000 characters")
	}
	baseSHA := strings.ToLower(strings.TrimSpace(input.BaseSHA))
	if !gitCommitPattern.MatchString(baseSHA) {
		return badRequest(c, "Invalid publication grant", "base_sha must be a 7 to 64 character Git commit SHA")
	}
	branch := strings.TrimSpace(input.Branch)
	if branch == "" {
		return badRequest(c, "Invalid publication grant", "branch must identify the reviewed isolated worktree")
	}
	if !strings.HasPrefix(branch, "itbem-agent/") || len(branch) > 255 || strings.ContainsAny(branch, " ~^:?*[\\") {
		return badRequest(c, "Invalid publication grant", "branch must be a safe itbem-agent/* branch")
	}
	capabilities, err := normalizedPublicationCapabilities(input.Capabilities)
	if err != nil {
		return badRequest(c, "Invalid publication grant", err.Error())
	}
	expiresIn := input.ExpiresInMinutes
	if expiresIn == 0 {
		expiresIn = defaultPublicationGrantMinutes
	}
	if expiresIn < minimumPublicationGrantMinutes || expiresIn > maximumPublicationGrantMinutes {
		return badRequest(c, "Invalid publication grant", "expires_in_minutes must be between 5 and 60")
	}

	now := time.Now().UTC()
	encodedCapabilities, _ := json.Marshal(capabilities)
	var grant models.DeliveryPublicationGrant
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		var lockedItem models.DeliveryWorkItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedItem, item.ID).Error; err != nil {
			return err
		}
		if lockedItem.State != deliveryworkflow.StatePreviewPending {
			return fmt.Errorf("a publication grant can only be issued after code review approval")
		}
		var snapshots []models.DeliveryContextSnapshot
		if err := tx.Where("work_item_id = ?", lockedItem.ID).Find(&snapshots).Error; err != nil {
			return err
		}
		repositoryRef, err := grantRepositoryReference(input.RepositoryRef, snapshots)
		if err != nil {
			return err
		}
		var activeCount int64
		if err := tx.Model(&models.DeliveryPublicationGrant{}).Where("work_item_id = ? AND repository_ref = ? AND revoked_at IS NULL AND expires_at > ?", lockedItem.ID, repositoryRef, now).Count(&activeCount).Error; err != nil {
			return err
		}
		if activeCount > 0 {
			return fmt.Errorf("revoke or wait for the active publication grant for this repository before issuing another one")
		}
		var reviewedChange models.DeliveryChangeSet
		if err := tx.Where("work_item_id = ? AND repository_ref = ? AND branch = ? AND review_type = ? AND ci_status = ?", lockedItem.ID, repositoryRef, branch, "local_worktree", "passed").Order("created_at DESC").First(&reviewedChange).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return fmt.Errorf("a passed reviewed local worktree for this exact branch is required")
			}
			return err
		}
		if !trustedImplementationChangeSet(reviewedChange) {
			return fmt.Errorf("the reviewed local worktree must be recorded by the ITBEM local agent")
		}
		reviewDiffSHA256, err := reviewedChangeSetDigest(reviewedChange)
		if err != nil {
			return err
		}
		reviewedBaseSHA, err := reviewedChangeSetBaseSHA(reviewedChange)
		if err != nil {
			return err
		}
		if baseSHA != reviewedBaseSHA {
			return fmt.Errorf("base_sha must match the reviewed local worktree exactly")
		}
		githubRepository, err := reviewedChangeSetGitHubRepository(reviewedChange)
		if err != nil {
			return err
		}
		var approvedGate models.DeliveryGate
		if err := tx.Where("work_item_id = ? AND kind = ? AND decision = ?", lockedItem.ID, deliveryworkflow.GateCodeReview, deliveryworkflow.DecisionApproved).Order("decided_at DESC").First(&approvedGate).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return fmt.Errorf("an approved code review gate is required")
			}
			return err
		}
		grant = models.DeliveryPublicationGrant{
			WorkItemID: lockedItem.ID, RepositoryRef: repositoryRef, BaseSHA: baseSHA, GitHubRepository: githubRepository, ReviewDiffSHA256: reviewDiffSHA256, Branch: branch,
			CapabilitiesJSON: string(encodedCapabilities), Reason: strings.TrimSpace(input.Reason),
			GrantedBy: actor.CognitoSub, GrantedAt: now, ExpiresAt: now.Add(time.Duration(expiresIn) * time.Minute),
		}
		return tx.Create(&grant).Error
	}); err != nil {
		return conflict(c, "Publication grant rejected", err.Error())
	}
	return success(c, "Delivery publication grant created", grant)
}

// grantRepositoryReference requires an explicit repository for a project with
// multiple frozen repositories. This makes a human authorization one precise
// operation: a specific reviewed worktree, branch and remote, never a project
// wide capability. Single-repository legacy work items retain a concise API.
func grantRepositoryReference(candidate string, snapshots []models.DeliveryContextSnapshot) (string, error) {
	candidate = strings.TrimSpace(candidate)
	references := make(map[string]struct{})
	for _, snapshot := range snapshots {
		if snapshot.Kind == "repository" && strings.TrimSpace(snapshot.Reference) != "" {
			references[strings.TrimSpace(snapshot.Reference)] = struct{}{}
		}
	}
	if len(references) == 0 {
		return "", fmt.Errorf("a frozen repository context is required")
	}
	if candidate != "" {
		if _, ok := references[candidate]; !ok {
			return "", fmt.Errorf("repository_ref must match a frozen repository context")
		}
		return candidate, nil
	}
	if len(references) != 1 {
		return "", fmt.Errorf("repository_ref is required for a multi-repository delivery")
	}
	for reference := range references {
		return reference, nil
	}
	return "", fmt.Errorf("a frozen repository context is required")
}

// trustedImplementationChangeSet distinguishes an actual isolated-worktree
// callback from a manually documented change set. A human may attach external
// evidence, but cannot turn metadata supplied to the generic endpoint into
// authority to publish a branch through the GitHub App.
func trustedImplementationChangeSet(change models.DeliveryChangeSet) bool {
	if strings.TrimSpace(change.CreatedBy) != "itbem-local-agent" || change.ReviewType != "local_worktree" || change.CIStatus != "passed" || !validLocalWorktreeReview(change.RepositoryRef, change.Branch) {
		return false
	}
	metadata := map[string]any{}
	if err := json.Unmarshal([]byte(change.MetadataJSON), &metadata); err != nil {
		return false
	}
	verificationSource, _ := metadata["verification_source"].(string)
	automationTaskID, _ := metadata["automation_task_id"].(string)
	worktree, _ := metadata["worktree"].(string)
	if verificationSource != "itbem-local-agent" || worktree != change.RepositoryRef+"#"+change.Branch {
		return false
	}
	parsedTaskID, err := uuid.FromString(strings.TrimSpace(automationTaskID))
	return err == nil && parsedTaskID != uuid.Nil
}

// reviewedChangeSetDigest is the immutable local-worktree fingerprint shown
// during code review. A publication grant carries this exact value so edits
// made after a human approval cannot be silently staged or pushed.
func reviewedChangeSetDigest(change models.DeliveryChangeSet) (string, error) {
	metadata := map[string]any{}
	if err := json.Unmarshal([]byte(change.MetadataJSON), &metadata); err != nil {
		return "", fmt.Errorf("reviewed local worktree metadata is invalid")
	}
	digest, _ := metadata["review_diff_sha256"].(string)
	digest = strings.ToLower(strings.TrimSpace(digest))
	if !reviewDiffDigestPattern.MatchString(digest) {
		return "", fmt.Errorf("reviewed local worktree does not have a valid diff fingerprint")
	}
	return digest, nil
}

func reviewedChangeSetGitHubRepository(change models.DeliveryChangeSet) (string, error) {
	metadata := map[string]any{}
	if err := json.Unmarshal([]byte(change.MetadataJSON), &metadata); err != nil {
		return "", fmt.Errorf("reviewed local worktree metadata is invalid")
	}
	repository, _ := metadata["github_repository"].(string)
	repository = strings.ToLower(strings.TrimSpace(repository))
	if !githubRepositoryPattern.MatchString(repository) {
		return "", fmt.Errorf("reviewed local worktree does not have a valid GitHub repository identity")
	}
	return repository, nil
}

func reviewedChangeSetBaseSHA(change models.DeliveryChangeSet) (string, error) {
	metadata := map[string]any{}
	if err := json.Unmarshal([]byte(change.MetadataJSON), &metadata); err != nil {
		return "", fmt.Errorf("reviewed local worktree metadata is invalid")
	}
	baseSHA, _ := metadata["base_sha"].(string)
	baseSHA = strings.ToLower(strings.TrimSpace(baseSHA))
	if !gitCommitPattern.MatchString(baseSHA) {
		return "", fmt.Errorf("reviewed local worktree does not have a valid base SHA")
	}
	return baseSHA, nil
}

func RevokePublicationGrant(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	actor, _, err := workItemActor(c, workItemID, deliveryManage)
	if err != nil {
		return err
	}
	grantID, err := uuid.FromString(c.Param("grantID"))
	if err != nil || grantID == uuid.Nil {
		return badRequest(c, "Invalid publication grant", "grant ID must be a UUID")
	}
	var input revokePublicationGrantInput
	if err := c.Bind(&input); err != nil {
		return badRequest(c, "Invalid publication grant", err.Error())
	}
	if strings.TrimSpace(input.Reason) == "" || len(strings.TrimSpace(input.Reason)) > 4000 {
		return badRequest(c, "Invalid publication grant", "revocation reason is required and must be at most 4,000 characters")
	}
	var grant models.DeliveryPublicationGrant
	if err := configuration.DB.Where("id = ? AND work_item_id = ?", grantID, workItemID).First(&grant).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Delivery publication grant not found", "")
		}
		return utilsError(c, err)
	}
	if grant.RevokedAt != nil {
		return conflict(c, "Publication grant already revoked", "this publication grant is already inactive")
	}
	now := time.Now().UTC()
	if err := configuration.DB.Model(&grant).Updates(map[string]any{"revoked_by": actor.CognitoSub, "revoked_at": now, "revocation_reason": strings.TrimSpace(input.Reason)}).Error; err != nil {
		return utilsError(c, err)
	}
	grant.RevokedBy, grant.RevokedAt, grant.RevocationReason = actor.CognitoSub, &now, strings.TrimSpace(input.Reason)
	return success(c, "Delivery publication grant revoked", grant)
}

func normalizedPublicationCapabilities(input []string) ([]string, error) {
	if len(input) == 0 {
		input = []string{"commit:stage", "branch:publish", "pull_request:create"}
	}
	if len(input) > len(publicationGrantCapabilities) {
		return nil, fmt.Errorf("capabilities may contain at most three values")
	}
	seen := map[string]struct{}{}
	for _, capability := range input {
		capability = strings.TrimSpace(capability)
		if _, valid := publicationGrantCapabilities[capability]; !valid {
			return nil, fmt.Errorf("capability %q is not publishable", capability)
		}
		if _, duplicate := seen[capability]; duplicate {
			return nil, fmt.Errorf("capability %q is duplicated", capability)
		}
		seen[capability] = struct{}{}
	}
	if _, allowed := seen["branch:publish"]; !allowed {
		return nil, fmt.Errorf("branch:publish is required")
	}
	if _, allowed := seen["commit:stage"]; !allowed {
		return nil, fmt.Errorf("commit:stage is required before branch:publish")
	}
	sorted := make([]string, 0, len(seen))
	for capability := range seen {
		sorted = append(sorted, capability)
	}
	sort.Strings(sorted)
	return sorted, nil
}

func approvedPrimaryRepositoryReference(snapshots []models.DeliveryContextSnapshot) (string, error) {
	repositories := make([]models.DeliveryContextSnapshot, 0)
	primaries := make([]models.DeliveryContextSnapshot, 0)
	for _, snapshot := range snapshots {
		if snapshot.Kind != "repository" {
			continue
		}
		repositories = append(repositories, snapshot)
		metadata := map[string]any{}
		if raw := strings.TrimSpace(snapshot.MetadataJSON); raw != "" && raw != "{}" {
			if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
				return "", fmt.Errorf("frozen repository metadata is invalid")
			}
		}
		if role, _ := metadata["repository_role"].(string); strings.EqualFold(strings.TrimSpace(role), "primary") {
			primaries = append(primaries, snapshot)
		}
	}
	if len(repositories) == 1 {
		return repositories[0].Reference, nil
	}
	if len(primaries) == 1 {
		return primaries[0].Reference, nil
	}
	if len(repositories) == 0 {
		return "", fmt.Errorf("a frozen repository context is required")
	}
	return "", fmt.Errorf("multi-repository delivery requires exactly one frozen primary repository")
}
