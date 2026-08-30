// Package releasegatecontrol resolves control-plane-owned policy and Vault
// evidence immediately before a Gatekeeper decision is appended to the ledger.
package releasegatecontrol

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/deliverypolicy"
	"events-stocks/internal/deliverypolicystore"
	"events-stocks/internal/projectvault"
	"events-stocks/internal/qaevidence"
	"events-stocks/internal/releasegate"
	"events-stocks/internal/securityevidence"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

const (
	maxPolicyRevisions       = 500
	maxPublishedChanges      = 128
	maxAssuranceObservations = 128
	maxDependencyRows        = 128
	compatibilityTestKind    = "assurance:compatibility"
	migrationsTestKind       = "assurance:migrations"
)

// Resolve replaces all candidate policy and Vault claims with current rows from
// the control-plane database. Missing policy or Vault data remains structured
// blocking evidence; corrupt or over-limit ledgers fail the callback entirely.
func Resolve(db *gorm.DB, workItemID uuid.UUID, input releasegate.Input, evaluatedAt time.Time) (releasegate.Input, error) {
	if db == nil || workItemID == uuid.Nil || evaluatedAt.IsZero() || input.ChangeSetID != workItemID.String() {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper control-plane context is invalid")
	}
	if _, err := releasegate.RevisionMatrixDigest(input.Revisions); err != nil {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper revision matrix is invalid")
	}
	var item models.DeliveryWorkItem
	if err := db.Select("id", "project_id", "plan_json").First(&item, workItemID).Error; err != nil {
		return releasegate.Input{}, fmt.Errorf("load release Gatekeeper work item: %w", err)
	}
	var project models.DeliveryProject
	if err := db.Select("id", "client_id").First(&project, item.ProjectID).Error; err != nil {
		return releasegate.Input{}, fmt.Errorf("load release Gatekeeper project: %w", err)
	}
	if project.ID == uuid.Nil || project.ClientID == uuid.Nil {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper project identity is invalid")
	}

	subject, err := loadPublishedReleaseSubject(db, item)
	if err != nil {
		return releasegate.Input{}, err
	}
	if !sameRevisionMatrix(input.Revisions, subject.Revisions) {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper GitHub evidence does not match the current control-plane publication matrix")
	}
	input.Revisions = subject.Revisions
	discardUntrustedAssurance(&input)
	repositories, references, err := repositoryReferences(input.Revisions)
	if err != nil {
		return releasegate.Input{}, err
	}
	vaults, err := loadLatestVaults(db, project.ID, references)
	if err != nil {
		return releasegate.Input{}, err
	}
	if err := validateVaultProvenance(db, vaults); err != nil {
		return releasegate.Input{}, err
	}
	revisions, decisions, err := loadPolicyLedger(db, project, input.ChangeSetID, references)
	if err != nil {
		return releasegate.Input{}, err
	}
	resolved, requiredByRepository, err := resolveStoredPolicyAndVault(input, project, repositories, vaults, revisions, decisions, evaluatedAt.UTC())
	if err != nil {
		return releasegate.Input{}, err
	}
	resolved, err = resolveStoredQAEvidence(db, item.ID, resolved, subject, requiredByRepository)
	if err != nil {
		return releasegate.Input{}, err
	}
	return resolveStoredSecurityEvidence(db, item.ID, resolved, subject)
}

type publishedChangeMetadata struct {
	PublicationGrantID string `json:"publication_grant_id"`
	BaseSHA            string `json:"base_sha"`
	RemoteRepository   string `json:"remote_repository"`
	TargetBranch       string `json:"target_branch"`
	BranchPublished    bool   `json:"branch_published"`
	VerificationSource string `json:"verification_source"`
}

type selectedPublishedChange struct {
	change   models.DeliveryChangeSet
	metadata publishedChangeMetadata
	grantID  uuid.UUID
}

type publishedReleaseSubject struct {
	Revisions                 []releasegate.Revision
	RepositoryByReference     map[string]string
	WorktreeBranchByReference map[string]string
}

// loadPublishedReleaseSubject rebuilds the exact release subject from the
// approved plan, GitHub App publication records, and their consumed grants.
// The callback's candidate matrix is deliberately ignored.
func loadPublishedReleaseSubject(db *gorm.DB, item models.DeliveryWorkItem) (publishedReleaseSubject, error) {
	required, err := requiredChangedRepositories(item.PlanJSON)
	if err != nil {
		return publishedReleaseSubject{}, err
	}
	var changes []models.DeliveryChangeSet
	if err := db.Where("work_item_id = ? AND review_type = ?", item.ID, "pull_request").
		Order("created_at DESC, id DESC").Limit(maxPublishedChanges + 1).Find(&changes).Error; err != nil {
		return publishedReleaseSubject{}, fmt.Errorf("load release Gatekeeper published changes: %w", err)
	}
	if len(changes) > maxPublishedChanges {
		return publishedReleaseSubject{}, fmt.Errorf("release Gatekeeper published change history exceeds the bounded limit")
	}
	selected := make(map[string]selectedPublishedChange, len(required))
	for _, change := range changes {
		reference := strings.TrimSpace(change.RepositoryRef)
		if _, needed := required[reference]; !needed {
			continue
		}
		if _, alreadySelected := selected[reference]; alreadySelected {
			continue
		}
		metadata, grantID, parseErr := parsePublishedChange(change)
		if parseErr != nil {
			return publishedReleaseSubject{}, fmt.Errorf("release Gatekeeper latest publication for %s is invalid: %w", reference, parseErr)
		}
		selected[reference] = selectedPublishedChange{change: change, metadata: metadata, grantID: grantID}
	}
	if len(selected) != len(required) {
		return publishedReleaseSubject{}, fmt.Errorf("release Gatekeeper requires one current GitHub App publication for every changed repository")
	}

	grantIDs := make([]uuid.UUID, 0, len(selected))
	for _, value := range selected {
		grantIDs = append(grantIDs, value.grantID)
	}
	var grants []models.DeliveryPublicationGrant
	if err := db.Where("id IN ?", grantIDs).Find(&grants).Error; err != nil {
		return publishedReleaseSubject{}, fmt.Errorf("load release Gatekeeper publication grants: %w", err)
	}
	if len(grants) != len(grantIDs) {
		return publishedReleaseSubject{}, fmt.Errorf("release Gatekeeper publication grant evidence is missing or ambiguous")
	}
	grantByID := make(map[uuid.UUID]models.DeliveryPublicationGrant, len(grants))
	for _, grant := range grants {
		if grant.ID == uuid.Nil {
			return publishedReleaseSubject{}, fmt.Errorf("release Gatekeeper publication grant identity is invalid")
		}
		if _, duplicate := grantByID[grant.ID]; duplicate {
			return publishedReleaseSubject{}, fmt.Errorf("release Gatekeeper publication grant evidence is duplicated")
		}
		grantByID[grant.ID] = grant
	}

	revisions := make([]releasegate.Revision, 0, len(selected))
	repositoryByReference := make(map[string]string, len(selected))
	worktreeBranchByReference := make(map[string]string, len(selected))
	for reference, value := range selected {
		grant, ok := grantByID[value.grantID]
		if !ok {
			return publishedReleaseSubject{}, fmt.Errorf("release Gatekeeper publication grant is missing")
		}
		if err := validatePublicationGrant(item.ID, reference, value, grant); err != nil {
			return publishedReleaseSubject{}, err
		}
		revision := releasegate.Revision{
			Repository: strings.ToLower(strings.TrimSpace(value.metadata.RemoteRepository)),
			Branch:     strings.TrimSpace(value.metadata.TargetBranch),
			SHA:        strings.ToLower(strings.TrimSpace(value.change.CommitSHA)),
		}
		if _, err := releasegate.RevisionMatrixDigest([]releasegate.Revision{revision}); err != nil {
			return publishedReleaseSubject{}, fmt.Errorf("release Gatekeeper stored publication revision is invalid")
		}
		revisions = append(revisions, revision)
		repositoryByReference[reference] = revision.Repository
		worktreeBranchByReference[reference] = value.change.Branch
	}
	sort.Slice(revisions, func(left, right int) bool {
		return strings.ToLower(revisions[left].Repository) < strings.ToLower(revisions[right].Repository)
	})
	if _, err := releasegate.RevisionMatrixDigest(revisions); err != nil {
		return publishedReleaseSubject{}, fmt.Errorf("release Gatekeeper stored publication matrix is invalid")
	}
	return publishedReleaseSubject{Revisions: revisions, RepositoryByReference: repositoryByReference, WorktreeBranchByReference: worktreeBranchByReference}, nil
}

// loadPublishedRevisionMatrix remains the narrow helper used by existing
// callers and tests; release resolution uses the richer subject so local
// workspace evidence can be bound to its exact published GitHub repository.
func loadPublishedRevisionMatrix(db *gorm.DB, item models.DeliveryWorkItem) ([]releasegate.Revision, error) {
	subject, err := loadPublishedReleaseSubject(db, item)
	return subject.Revisions, err
}

func requiredChangedRepositories(planJSON string) (map[string]struct{}, error) {
	var plan struct {
		RepositoryImpact json.RawMessage `json:"repository_impact"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(planJSON)), &plan); err != nil || len(plan.RepositoryImpact) == 0 {
		return nil, fmt.Errorf("release Gatekeeper approved plan repository matrix is invalid")
	}
	var entries []struct {
		Reference string `json:"reference"`
		Impact    string `json:"impact"`
	}
	if err := json.Unmarshal(plan.RepositoryImpact, &entries); err != nil || len(entries) == 0 || len(entries) > 64 {
		return nil, fmt.Errorf("release Gatekeeper approved plan repository matrix is invalid")
	}
	result := make(map[string]struct{}, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		reference := strings.TrimSpace(entry.Reference)
		impact := strings.ToLower(strings.TrimSpace(entry.Impact))
		if !strings.HasPrefix(reference, "workspace://") || len(reference) > 500 || (impact != "changes" && impact != "consulted" && impact != "untouched") {
			return nil, fmt.Errorf("release Gatekeeper approved plan repository matrix is invalid")
		}
		if _, duplicate := seen[reference]; duplicate {
			return nil, fmt.Errorf("release Gatekeeper approved plan repository matrix is duplicated")
		}
		seen[reference] = struct{}{}
		if impact == "changes" {
			result[reference] = struct{}{}
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("release Gatekeeper approved plan has no changed repository")
	}
	return result, nil
}

func parsePublishedChange(change models.DeliveryChangeSet) (publishedChangeMetadata, uuid.UUID, error) {
	var metadata publishedChangeMetadata
	if change.ID == uuid.Nil || strings.TrimSpace(change.CreatedBy) != "itbem-github-app" ||
		!strings.EqualFold(strings.TrimSpace(change.ReviewType), "pull_request") || !strings.HasPrefix(strings.TrimSpace(change.Branch), "itbem-agent/") ||
		!validHex(change.CommitSHA, 40) || json.Unmarshal([]byte(change.MetadataJSON), &metadata) != nil {
		return publishedChangeMetadata{}, uuid.Nil, fmt.Errorf("publication record is invalid")
	}
	metadata.PublicationGrantID = strings.TrimSpace(metadata.PublicationGrantID)
	metadata.BaseSHA = strings.ToLower(strings.TrimSpace(metadata.BaseSHA))
	metadata.RemoteRepository = strings.ToLower(strings.TrimSpace(metadata.RemoteRepository))
	metadata.TargetBranch = strings.TrimSpace(metadata.TargetBranch)
	metadata.VerificationSource = strings.TrimSpace(metadata.VerificationSource)
	grantID, err := uuid.FromString(metadata.PublicationGrantID)
	if err != nil || grantID == uuid.Nil || !validHex(metadata.BaseSHA, 40) || !metadata.BranchPublished || metadata.VerificationSource != "itbem-github-app" || !validGitHubPullRequest(change.PullRequestURL, metadata.RemoteRepository) {
		return publishedChangeMetadata{}, uuid.Nil, fmt.Errorf("publication provenance is invalid")
	}
	return metadata, grantID, nil
}

func validatePublicationGrant(workItemID uuid.UUID, reference string, value selectedPublishedChange, grant models.DeliveryPublicationGrant) error {
	if grant.ID != value.grantID || grant.WorkItemID != workItemID || grant.RepositoryRef != reference {
		return fmt.Errorf("release Gatekeeper publication grant scope does not match")
	}
	if grant.Branch != value.change.Branch {
		return fmt.Errorf("release Gatekeeper publication grant branch does not match")
	}
	if !strings.EqualFold(grant.GitHubRepository, value.metadata.RemoteRepository) {
		return fmt.Errorf("release Gatekeeper publication grant repository does not match")
	}
	if !strings.EqualFold(grant.BaseSHA, value.metadata.BaseSHA) {
		return fmt.Errorf("release Gatekeeper publication grant base does not match")
	}
	if !validHex(grant.ReviewDiffSHA256, 64) || grant.RevokedAt == nil || strings.TrimSpace(grant.RevokedBy) != "itbem-github-app" {
		return fmt.Errorf("release Gatekeeper publication grant consumption is invalid")
	}
	if !grant.RevokedAt.UTC().Equal(value.change.CreatedAt.UTC()) {
		return fmt.Errorf("release Gatekeeper publication grant consumption time does not match")
	}
	return nil
}

func validGitHubPullRequest(value, repository string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" || !strings.EqualFold(parts[0]+"/"+parts[1], repository) || parts[3] == "" || parts[3] == "0" {
		return false
	}
	for _, character := range parts[3] {
		if character < '0' || character > '9' {
			return false
		}
	}
	_, err = projectvault.CanonicalGitHubReference("github://" + repository)
	return err == nil
}

// Assurance is accepted only from the dedicated immutable QA/security and
// environment ledgers. Until those resolvers attach it, a release callback
// cannot self-attest any of these safety floors.
func discardUntrustedAssurance(input *releasegate.Input) {
	input.Tests = []releasegate.TestEvidence{}
	input.Security = []releasegate.SecurityEvidence{}
	input.Compatibility = releasegate.MatrixEvidence{}
	input.Migrations = releasegate.MatrixEvidence{}
	input.Dependencies = releasegate.MatrixEvidence{}
	input.Environment = releasegate.MatrixEvidence{}
	input.Recovery = releasegate.RecoveryEvidence{}
}

func sameRevisionMatrix(left, right []releasegate.Revision) bool {
	leftDigest, leftErr := releasegate.RevisionMatrixDigest(left)
	rightDigest, rightErr := releasegate.RevisionMatrixDigest(right)
	return leftErr == nil && rightErr == nil && strings.EqualFold(leftDigest, rightDigest)
}

func repositoryReferences(revisions []releasegate.Revision) (map[string]string, []string, error) {
	result := make(map[string]string, len(revisions))
	references := make([]string, 0, len(revisions))
	for _, revision := range revisions {
		repository := strings.ToLower(strings.TrimSpace(revision.Repository))
		reference, err := projectvault.CanonicalGitHubReference("github://" + repository)
		if err != nil {
			return nil, nil, fmt.Errorf("release Gatekeeper repository identity is invalid")
		}
		if _, duplicate := result[repository]; duplicate {
			return nil, nil, fmt.Errorf("release Gatekeeper repository identity is duplicated")
		}
		result[repository] = reference
		references = append(references, reference)
	}
	sort.Strings(references)
	return result, references, nil
}

func loadLatestVaults(db *gorm.DB, projectID uuid.UUID, references []string) ([]models.DeliveryProjectVaultRevision, error) {
	var vaults []models.DeliveryProjectVaultRevision
	if err := db.Select("DISTINCT ON (LOWER(repository_reference)) delivery_project_vault_revisions.*").
		Where("project_id = ? AND LOWER(repository_reference) IN ?", projectID, lowerValues(references)).
		Order("LOWER(repository_reference), version DESC, published_at DESC, id DESC").
		Find(&vaults).Error; err != nil {
		return nil, fmt.Errorf("load release Gatekeeper Vault evidence: %w", err)
	}
	if len(vaults) > len(references) {
		return nil, fmt.Errorf("release Gatekeeper Vault evidence is ambiguous")
	}
	return vaults, nil
}

func validateVaultProvenance(db *gorm.DB, vaults []models.DeliveryProjectVaultRevision) error {
	if len(vaults) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(vaults))
	for _, vault := range vaults {
		ids = append(ids, vault.SourceOnboardingID)
	}
	var onboardings []models.DeliveryRepositoryOnboarding
	if err := db.Where("id IN ?", ids).Find(&onboardings).Error; err != nil {
		return fmt.Errorf("load release Gatekeeper Vault provenance: %w", err)
	}
	return validateVaultProvenanceRows(vaults, onboardings)
}

func validateVaultProvenanceRows(vaults []models.DeliveryProjectVaultRevision, onboardings []models.DeliveryRepositoryOnboarding) error {
	if len(onboardings) != len(vaults) {
		return fmt.Errorf("release Gatekeeper Vault provenance is missing or ambiguous")
	}
	byID := make(map[uuid.UUID]models.DeliveryRepositoryOnboarding, len(onboardings))
	for _, onboarding := range onboardings {
		if onboarding.ID == uuid.Nil {
			return fmt.Errorf("release Gatekeeper Vault provenance is invalid")
		}
		if _, duplicate := byID[onboarding.ID]; duplicate {
			return fmt.Errorf("release Gatekeeper Vault provenance is duplicated")
		}
		byID[onboarding.ID] = onboarding
	}
	for _, vault := range vaults {
		onboarding, ok := byID[vault.SourceOnboardingID]
		if !ok || onboarding.Status != "approved" || onboarding.ProjectID != vault.ProjectID || !strings.EqualFold(onboarding.RepositoryReference, vault.RepositoryReference) || !strings.EqualFold(onboarding.Revision, vault.Revision) || !strings.EqualFold(onboarding.VaultSHA256, vault.ContentSHA256) || strings.TrimSpace(onboarding.ApprovedBy) == "" || onboarding.ApprovedAt == nil || !strings.EqualFold(strings.TrimSpace(onboarding.ApprovedBy), strings.TrimSpace(vault.PublishedBy)) || !onboarding.ApprovedAt.UTC().Equal(vault.PublishedAt.UTC()) {
			return fmt.Errorf("release Gatekeeper Vault provenance does not match its approved onboarding")
		}
	}
	return nil
}

func loadPolicyLedger(db *gorm.DB, project models.DeliveryProject, changeSetID string, references []string) ([]models.DeliveryPolicyRevision, []models.DeliveryPolicyDecision, error) {
	organizationID := project.ClientID.String()
	scopeSQL := `(level = 'platform') OR
		(level = 'organization' AND organization_id = ?) OR
		(level = 'project' AND organization_id = ? AND project_id = ?) OR
		(level = 'repository' AND organization_id = ? AND project_id = ? AND LOWER(repository_reference) IN ?) OR
		(level = 'override' AND organization_id = ? AND project_id = ? AND change_set_id = ? AND (repository_reference = '' OR LOWER(repository_reference) IN ?))`
	args := []any{organizationID, organizationID, project.ID, organizationID, project.ID, lowerValues(references), organizationID, project.ID, changeSetID, lowerValues(references)}
	var revisions []models.DeliveryPolicyRevision
	if err := db.Where(scopeSQL, args...).Order("created_at DESC, id DESC").Limit(maxPolicyRevisions + 1).Find(&revisions).Error; err != nil {
		return nil, nil, fmt.Errorf("load release Gatekeeper policy revisions: %w", err)
	}
	if len(revisions) > maxPolicyRevisions {
		return nil, nil, fmt.Errorf("release Gatekeeper policy history exceeds the bounded limit")
	}
	if len(revisions) == 0 {
		return []models.DeliveryPolicyRevision{}, []models.DeliveryPolicyDecision{}, nil
	}
	ids := make([]uuid.UUID, 0, len(revisions))
	for _, revision := range revisions {
		ids = append(ids, revision.ID)
	}
	var decisions []models.DeliveryPolicyDecision
	if err := db.Select("DISTINCT ON (policy_revision_id) delivery_policy_decisions.*").
		Where("policy_revision_id IN ?", ids).
		Order("policy_revision_id, occurred_at DESC, id DESC").Find(&decisions).Error; err != nil {
		return nil, nil, fmt.Errorf("load release Gatekeeper policy decisions: %w", err)
	}
	if len(decisions) > len(revisions) {
		return nil, nil, fmt.Errorf("release Gatekeeper policy decisions are ambiguous")
	}
	return revisions, decisions, nil
}

func resolveStoredEvidence(input releasegate.Input, project models.DeliveryProject, repositories map[string]string, vaults []models.DeliveryProjectVaultRevision, revisions []models.DeliveryPolicyRevision, decisions []models.DeliveryPolicyDecision, evaluatedAt time.Time) (releasegate.Input, error) {
	resolved, _, err := resolveStoredPolicyAndVault(input, project, repositories, vaults, revisions, decisions, evaluatedAt)
	return resolved, err
}

// resolveStoredPolicyAndVault also retains the per-repository test contract.
// Collapsing it to the policy-wide union before QA resolution would let a test
// observed in one repository incorrectly satisfy another repository's gate.
func resolveStoredPolicyAndVault(input releasegate.Input, project models.DeliveryProject, repositories map[string]string, vaults []models.DeliveryProjectVaultRevision, revisions []models.DeliveryPolicyRevision, decisions []models.DeliveryPolicyDecision, evaluatedAt time.Time) (releasegate.Input, map[string][]string, error) {
	input.Policy = releasegate.Policy{Resolved: true, RequiredTestKinds: []string{}, Repositories: []releasegate.RepositoryPolicyEvidence{}}
	input.Vault = []releasegate.VaultEvidence{}
	vaultByReference := make(map[string]models.DeliveryProjectVaultRevision, len(vaults))
	for _, vault := range vaults {
		if err := validateVaultRevision(project.ID, vault); err != nil {
			return releasegate.Input{}, nil, err
		}
		key := strings.ToLower(strings.TrimSpace(vault.RepositoryReference))
		if _, duplicate := vaultByReference[key]; duplicate {
			return releasegate.Input{}, nil, fmt.Errorf("release Gatekeeper Vault repository is duplicated")
		}
		vaultByReference[key] = vault
	}

	requiredTests := map[string]string{}
	requiredByRepository := make(map[string][]string, len(input.Revisions))
	recoveryByRepository := make(map[string]releasegate.RecoveryClassification, len(input.Revisions))
	for _, matrixRevision := range input.Revisions {
		repository := strings.ToLower(strings.TrimSpace(matrixRevision.Repository))
		reference := repositories[repository]
		context := deliverypolicy.Context{
			OrganizationID: project.ClientID.String(), ProjectID: project.ID.String(), Repository: policyRepositoryReference(reference, revisions), ChangeSetID: input.ChangeSetID,
		}
		policy, err := deliverypolicystore.ResolveEffective(context, revisions, decisions, evaluatedAt)
		if err != nil {
			return releasegate.Input{}, nil, fmt.Errorf("resolve release Gatekeeper policy for %s: %w", repository, err)
		}
		gatePolicy := policy.GatePolicyFor(input.Action)
		actionAllowed := gatePolicy.Resolved
		branchAllowed := policy.AllowsTargetBranch(matrixRevision.Branch)
		evidence := releasegate.RepositoryPolicyEvidence{
			Repository: repository, Digest: policy.Digest, Resolved: policy.Resolved,
			ActionAllowed: actionAllowed, BranchAllowed: branchAllowed,
		}
		input.Policy.Repositories = append(input.Policy.Repositories, evidence)
		if !evidence.Resolved || !evidence.ActionAllowed || !evidence.BranchAllowed {
			input.Policy.Resolved = false
		}
		for _, kind := range policy.RequiredTestKinds {
			key := strings.ToLower(strings.TrimSpace(kind))
			if key != "" {
				requiredTests[key] = strings.TrimSpace(kind)
				requiredByRepository[repository] = append(requiredByRepository[repository], strings.TrimSpace(kind))
			}
		}
		sort.Slice(requiredByRepository[repository], func(left, right int) bool {
			return strings.ToLower(requiredByRepository[repository][left]) < strings.ToLower(requiredByRepository[repository][right])
		})
		if policy.Resolved && gatePolicy.Resolved && strings.TrimSpace(policy.RecoveryDefault) != "" {
			recoveryByRepository[repository] = releasegate.RecoveryClassification(strings.TrimSpace(policy.RecoveryDefault))
		}

		if vault, ok := vaultByReference[strings.ToLower(reference)]; ok {
			revisionID := fmt.Sprintf("%s:%d:%s", vault.ID.String(), vault.Version, vault.ContentSHA256)
			input.Vault = append(input.Vault, releasegate.VaultEvidence{
				Repository: repository, HeadSHA: strings.ToLower(vault.Revision), RevisionID: revisionID,
				Reconciled: strings.EqualFold(strings.TrimSpace(vault.Revision), matrixRevision.SHA),
			})
		}
	}
	input.Policy.RequiredTestKinds = make([]string, 0, len(requiredTests))
	for _, kind := range requiredTests {
		input.Policy.RequiredTestKinds = append(input.Policy.RequiredTestKinds, kind)
	}
	sort.Slice(input.Policy.RequiredTestKinds, func(left, right int) bool {
		return strings.ToLower(input.Policy.RequiredTestKinds[left]) < strings.ToLower(input.Policy.RequiredTestKinds[right])
	})
	digest, err := releasegate.CompositePolicyDigest(input.Policy.Repositories)
	if err != nil {
		return releasegate.Input{}, nil, fmt.Errorf("release Gatekeeper composite policy is invalid: %w", err)
	}
	input.Policy.Digest = digest
	if len(recoveryByRepository) == len(input.Revisions) {
		classification, err := compositeRecoveryClassification(recoveryByRepository)
		if err != nil {
			return releasegate.Input{}, nil, err
		}
		matrixDigest, err := releasegate.RevisionMatrixDigest(input.Revisions)
		if err != nil {
			return releasegate.Input{}, nil, fmt.Errorf("release Gatekeeper recovery matrix is invalid")
		}
		input.Recovery = releasegate.RecoveryEvidence{MatrixDigest: matrixDigest, Classification: classification, Evaluated: true}
	}
	return input, requiredByRepository, nil
}

// compositeRecoveryClassification represents the safest executable posture for
// the coordinated release. A component that cannot use a lower-risk strategy
// raises the classification for the entire matrix. Irreversible remains
// separately blocked until an exact-subject human approval exists.
func compositeRecoveryClassification(values map[string]releasegate.RecoveryClassification) (releasegate.RecoveryClassification, error) {
	if len(values) == 0 || len(values) > 64 {
		return "", fmt.Errorf("release Gatekeeper recovery policy is missing")
	}
	rank := map[releasegate.RecoveryClassification]int{
		releasegate.RecoveryRollback: 1, releasegate.RecoveryRollForward: 2,
		releasegate.RecoveryExpandContract: 3, releasegate.RecoveryIrreversible: 4,
	}
	classification := releasegate.RecoveryClassification("")
	highest := 0
	seen := make(map[string]struct{}, len(values))
	for repository, candidate := range values {
		key := strings.ToLower(strings.TrimSpace(repository))
		value, valid := rank[candidate]
		_, repositoryErr := projectvault.CanonicalGitHubReference("github://" + key)
		if !valid || repositoryErr != nil {
			return "", fmt.Errorf("release Gatekeeper repository recovery policy is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			return "", fmt.Errorf("release Gatekeeper repository recovery policy is duplicated")
		}
		seen[key] = struct{}{}
		if value > highest {
			highest, classification = value, candidate
		}
	}
	return classification, nil
}

// resolveStoredQAEvidence accepts only the newest completed QA task for the
// exact publication matrix. Old matrix observations are ignored by the query;
// malformed current-matrix ledger rows fail closed instead of being skipped.
func resolveStoredQAEvidence(db *gorm.DB, workItemID uuid.UUID, input releasegate.Input, subject publishedReleaseSubject, requiredByRepository map[string][]string) (releasegate.Input, error) {
	if db == nil || workItemID == uuid.Nil {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper QA context is invalid")
	}
	matrixDigest, err := releasegate.RevisionMatrixDigest(input.Revisions)
	if err != nil || !sameRevisionMatrix(input.Revisions, subject.Revisions) {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper QA matrix is invalid")
	}
	var events []models.DeliveryEvent
	if err := db.Where("work_item_id = ? AND event_type = ? AND subject_digest = ?", workItemID, deliveryledger.EventTypeQAObserved, matrixDigest).
		Order("sequence DESC, occurred_at DESC, id DESC").Limit(maxAssuranceObservations + 1).Find(&events).Error; err != nil {
		return releasegate.Input{}, fmt.Errorf("load release Gatekeeper QA evidence: %w", err)
	}
	if len(events) > maxAssuranceObservations {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper QA evidence history exceeds the bounded limit")
	}
	if len(events) == 0 {
		return input, nil
	}
	projected := make([]deliveryledger.QAObservation, 0, len(events))
	for _, event := range events {
		observation, projectionErr := deliveryledger.ProjectQAObservation(event)
		if projectionErr != nil {
			return releasegate.Input{}, fmt.Errorf("release Gatekeeper QA evidence integrity failed: %w", projectionErr)
		}
		if !strings.EqualFold(observation.Observation.MatrixDigest, matrixDigest) {
			return releasegate.Input{}, fmt.Errorf("release Gatekeeper QA evidence subject does not match")
		}
		projected = append(projected, observation)
	}
	latest := projected[0]
	taskID, err := uuid.FromString(latest.Observation.TaskID)
	if err != nil || taskID == uuid.Nil {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper QA task identity is invalid")
	}
	var task models.AutomationTask
	if err := db.Select("id", "delivery_work_item_id", "operation", "evidence_subject_digest", "status", "completed_at").First(&task, taskID).Error; err != nil {
		return releasegate.Input{}, fmt.Errorf("load release Gatekeeper QA task: %w", err)
	}
	if err := validateQATaskProvenance(task, workItemID, taskID, matrixDigest, latest.OccurredAt); err != nil {
		return releasegate.Input{}, err
	}
	tests, err := testsFromQAObservation(input.Policy.RequiredTestKinds, requiredByRepository, subject, latest.Observation)
	if err != nil {
		return releasegate.Input{}, err
	}
	input.Tests = tests
	input.Compatibility, err = namedMatrixEvidenceFromQA(compatibilityTestKind, subject, latest.Observation)
	if err != nil {
		return releasegate.Input{}, err
	}
	input.Migrations, err = namedMatrixEvidenceFromQA(migrationsTestKind, subject, latest.Observation)
	if err != nil {
		return releasegate.Input{}, err
	}
	input.Dependencies, err = resolveStoredDependencyEvidence(db, workItemID, subject, latest.Observation)
	if err != nil {
		return releasegate.Input{}, err
	}
	return input, nil
}

func validateQATaskProvenance(task models.AutomationTask, workItemID, taskID uuid.UUID, matrixDigest string, occurredAt time.Time) error {
	if task.ID != taskID || task.DeliveryWorkItemID == nil || *task.DeliveryWorkItemID != workItemID || task.Operation != "delivery.qa" || task.Status != "completed" ||
		task.CompletedAt == nil || !task.CompletedAt.UTC().Equal(occurredAt.UTC()) || !strings.EqualFold(strings.TrimSpace(task.EvidenceSubjectDigest), matrixDigest) || !validHex(matrixDigest, 64) {
		return fmt.Errorf("release Gatekeeper QA task provenance does not match")
	}
	return nil
}

// resolveStoredSecurityEvidence uses only the latest immutable observation for
// the exact matrix. Operator-owned local scanners run in the reviewed worktree;
// their workspace identity is mapped through the consumed publication grant.
func resolveStoredSecurityEvidence(db *gorm.DB, workItemID uuid.UUID, input releasegate.Input, subject publishedReleaseSubject) (releasegate.Input, error) {
	if db == nil || workItemID == uuid.Nil {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper security context is invalid")
	}
	matrixDigest, err := releasegate.RevisionMatrixDigest(input.Revisions)
	if err != nil || !sameRevisionMatrix(input.Revisions, subject.Revisions) {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper security matrix is invalid")
	}
	var events []models.DeliveryEvent
	if err := db.Where("work_item_id = ? AND event_type = ? AND subject_digest = ?", workItemID, deliveryledger.EventTypeSecurityObserved, matrixDigest).
		Order("sequence DESC, occurred_at DESC, id DESC").Limit(maxAssuranceObservations + 1).Find(&events).Error; err != nil {
		return releasegate.Input{}, fmt.Errorf("load release Gatekeeper security evidence: %w", err)
	}
	if len(events) > maxAssuranceObservations {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper security evidence history exceeds the bounded limit")
	}
	if len(events) == 0 {
		return input, nil
	}
	projected := make([]deliveryledger.SecurityObservation, 0, len(events))
	for _, event := range events {
		observation, projectionErr := deliveryledger.ProjectSecurityObservation(event)
		if projectionErr != nil {
			return releasegate.Input{}, fmt.Errorf("release Gatekeeper security evidence integrity failed: %w", projectionErr)
		}
		projected = append(projected, observation)
	}
	latest := projected[0]
	taskID, err := uuid.FromString(latest.Observation.TaskID)
	if err != nil || taskID == uuid.Nil {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper security task identity is invalid")
	}
	var task models.AutomationTask
	if err := db.Select("id", "delivery_work_item_id", "operation", "evidence_subject_digest", "status", "completed_at").First(&task, taskID).Error; err != nil {
		return releasegate.Input{}, fmt.Errorf("load release Gatekeeper security task: %w", err)
	}
	if err := validateSecurityTaskProvenance(task, workItemID, taskID, matrixDigest, latest.OccurredAt); err != nil {
		return releasegate.Input{}, err
	}
	security, err := securityFromObservation(input.Revisions, subject, latest.Observation)
	if err != nil {
		return releasegate.Input{}, err
	}
	input.Security = security
	return input, nil
}

func validateSecurityTaskProvenance(task models.AutomationTask, workItemID, taskID uuid.UUID, matrixDigest string, occurredAt time.Time) error {
	if task.ID != taskID || task.DeliveryWorkItemID == nil || *task.DeliveryWorkItemID != workItemID || task.Operation != "delivery.qa" || task.Status != "completed" ||
		task.CompletedAt == nil || !task.CompletedAt.UTC().Equal(occurredAt.UTC()) || !strings.EqualFold(strings.TrimSpace(task.EvidenceSubjectDigest), matrixDigest) || !validHex(matrixDigest, 64) {
		return fmt.Errorf("release Gatekeeper security task provenance does not match")
	}
	return nil
}

func securityFromObservation(revisions []releasegate.Revision, subject publishedReleaseSubject, observation securityevidence.Observation) ([]releasegate.SecurityEvidence, error) {
	if err := securityevidence.Validate(observation); err != nil || len(observation.Repositories) != len(revisions) || len(subject.RepositoryByReference) != len(revisions) || len(subject.WorktreeBranchByReference) != len(revisions) {
		return nil, fmt.Errorf("release Gatekeeper security repository set does not match the publication subject")
	}
	required := make(map[string]string, len(revisions))
	for _, revision := range revisions {
		required[strings.ToLower(strings.TrimSpace(revision.Repository))] = strings.ToLower(strings.TrimSpace(revision.SHA))
	}
	result := make([]releasegate.SecurityEvidence, 0, len(observation.Repositories))
	seen := make(map[string]struct{}, len(observation.Repositories))
	for _, repository := range observation.Repositories {
		remoteRepository, exists := subject.RepositoryByReference[repository.Reference]
		expectedBranch, branchExists := subject.WorktreeBranchByReference[repository.Reference]
		remoteRepository = strings.ToLower(strings.TrimSpace(remoteRepository))
		expectedSHA, revisionExists := required[remoteRepository]
		if !exists || !branchExists || !revisionExists || repository.Branch != expectedBranch {
			return nil, fmt.Errorf("release Gatekeeper security repository or SHA does not match")
		}
		if _, duplicate := seen[remoteRepository]; duplicate {
			return nil, fmt.Errorf("release Gatekeeper security repository is duplicated")
		}
		seen[remoteRepository] = struct{}{}
		result = append(result, releasegate.SecurityEvidence{
			Repository: remoteRepository, HeadSHA: expectedSHA, SecretScanPassed: repository.SecretScanPassed,
			HighFindings: repository.HighFindings, CriticalFindings: repository.CriticalFindings,
		})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Repository < result[right].Repository })
	return result, nil
}

// namedMatrixEvidenceFromQA projects one reserved operator-owned command
// identity only when it ran in every exact reviewed worktree. Missing commands
// remain missing Gatekeeper evidence; a failure in any repository fails the
// coordinated matrix.
func namedMatrixEvidenceFromQA(kind string, subject publishedReleaseSubject, observation qaevidence.Observation) (releasegate.MatrixEvidence, error) {
	if err := qaevidence.Validate(observation); err != nil {
		return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper assurance observation is invalid: %w", err)
	}
	matrixDigest, digestErr := releasegate.RevisionMatrixDigest(subject.Revisions)
	if kind != strings.ToLower(strings.TrimSpace(kind)) || kind == "" || len(subject.RepositoryByReference) == 0 ||
		digestErr != nil || !strings.EqualFold(matrixDigest, observation.MatrixDigest) || len(subject.Revisions) != len(subject.RepositoryByReference) ||
		len(subject.RepositoryByReference) != len(subject.WorktreeBranchByReference) || len(observation.Repositories) != len(subject.RepositoryByReference) {
		return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper assurance repository set is invalid")
	}
	observed := make(map[string]qaevidence.Repository, len(observation.Repositories))
	for _, repository := range observation.Repositories {
		expectedRepository, exists := subject.RepositoryByReference[repository.Reference]
		expectedBranch, branchExists := subject.WorktreeBranchByReference[repository.Reference]
		if !exists || !branchExists || strings.TrimSpace(expectedRepository) == "" || repository.Branch != expectedBranch {
			return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper assurance repository provenance does not match")
		}
		observed[repository.Reference] = repository
	}
	status := releasegate.StatusPassed
	for reference := range subject.RepositoryByReference {
		repository, exists := observed[reference]
		if !exists {
			return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper assurance repository set does not match")
		}
		found := false
		for _, command := range repository.Commands {
			if strings.EqualFold(command.Kind, kind) {
				found = true
				if !command.Passed {
					status = releasegate.StatusFailed
				}
				break
			}
		}
		if !found {
			return releasegate.MatrixEvidence{}, nil
		}
	}
	return releasegate.MatrixEvidence{MatrixDigest: observation.MatrixDigest, Status: status}, nil
}

type workItemDependencyState struct {
	ID                  uuid.UUID `gorm:"column:id"`
	WorkItemID          uuid.UUID `gorm:"column:work_item_id"`
	DependsOnWorkItemID uuid.UUID `gorm:"column:depends_on_work_item_id"`
	State               string    `gorm:"column:state"`
}

// resolveStoredDependencyEvidence combines immutable repository topology with
// the exact QA execution order and current control-plane work-item prerequisites.
// Model text and worker-supplied dependency claims are never consulted.
func resolveStoredDependencyEvidence(db *gorm.DB, workItemID uuid.UUID, subject publishedReleaseSubject, observation qaevidence.Observation) (releasegate.MatrixEvidence, error) {
	if db == nil || workItemID == uuid.Nil {
		return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper dependency context is invalid")
	}
	var snapshots []models.DeliveryContextSnapshot
	if err := db.Select("id", "work_item_id", "kind", "reference", "metadata_json").
		Where("work_item_id = ? AND kind = ?", workItemID, "repository").Order("id ASC").Limit(maxDependencyRows + 1).Find(&snapshots).Error; err != nil {
		return releasegate.MatrixEvidence{}, fmt.Errorf("load release Gatekeeper repository dependencies: %w", err)
	}
	if len(snapshots) > maxDependencyRows {
		return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper repository dependency history exceeds the bounded limit")
	}
	var dependencies []workItemDependencyState
	if err := db.Table("delivery_work_item_dependencies AS dependency").
		Select("dependency.id", "dependency.work_item_id", "dependency.depends_on_work_item_id", "dependency_item.state").
		Joins("LEFT JOIN delivery_work_items AS dependency_item ON dependency_item.id = dependency.depends_on_work_item_id").
		Where("dependency.work_item_id = ?", workItemID).Order("dependency.id ASC").Limit(maxDependencyRows + 1).Scan(&dependencies).Error; err != nil {
		return releasegate.MatrixEvidence{}, fmt.Errorf("load release Gatekeeper work-item dependencies: %w", err)
	}
	if len(dependencies) > maxDependencyRows {
		return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper work-item dependency history exceeds the bounded limit")
	}
	return dependencyEvidenceFromControlPlane(workItemID, subject, observation, snapshots, dependencies)
}

func dependencyEvidenceFromControlPlane(workItemID uuid.UUID, subject publishedReleaseSubject, observation qaevidence.Observation, snapshots []models.DeliveryContextSnapshot, workItemDependencies []workItemDependencyState) (releasegate.MatrixEvidence, error) {
	matrixDigest, digestErr := releasegate.RevisionMatrixDigest(subject.Revisions)
	if workItemID == uuid.Nil || digestErr != nil || !strings.EqualFold(matrixDigest, observation.MatrixDigest) || qaevidence.Validate(observation) != nil ||
		len(subject.Revisions) == 0 || len(subject.Revisions) != len(subject.RepositoryByReference) || len(subject.RepositoryByReference) != len(subject.WorktreeBranchByReference) ||
		len(observation.Repositories) != len(subject.RepositoryByReference) || len(snapshots) == 0 || len(snapshots) > maxDependencyRows || len(workItemDependencies) > maxDependencyRows {
		return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper dependency evidence identity is invalid")
	}
	known := make(map[string]models.DeliveryContextSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		reference := strings.TrimSpace(snapshot.Reference)
		if snapshot.ID == uuid.Nil || snapshot.WorkItemID != workItemID || snapshot.Kind != "repository" || reference != snapshot.Reference || !validFrozenRepositoryReference(reference) {
			return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper frozen repository dependency is invalid")
		}
		if _, duplicate := known[reference]; duplicate {
			return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper frozen repository dependency is duplicated")
		}
		known[reference] = snapshot
	}
	edges := make(map[string][]string, len(known))
	for reference, snapshot := range known {
		var metadata map[string]json.RawMessage
		rawMetadata := strings.TrimSpace(snapshot.MetadataJSON)
		if rawMetadata == "" {
			rawMetadata = "{}"
		}
		if len(rawMetadata) > 64<<10 || json.Unmarshal([]byte(rawMetadata), &metadata) != nil || metadata == nil {
			return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper frozen repository metadata is invalid")
		}
		dependencies := []string{}
		if raw, exists := metadata["depends_on_repositories"]; exists {
			if string(raw) == "null" || json.Unmarshal(raw, &dependencies) != nil || len(dependencies) > maxDependencyRows {
				return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper frozen repository dependencies are invalid")
			}
		}
		seen := make(map[string]struct{}, len(dependencies))
		for _, dependency := range dependencies {
			if dependency != strings.TrimSpace(dependency) || dependency == reference {
				return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper frozen repository dependency edge is invalid")
			}
			if _, exists := known[dependency]; !exists {
				return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper frozen repository dependency target is missing")
			}
			if _, duplicate := seen[dependency]; duplicate {
				return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper frozen repository dependency edge is duplicated")
			}
			seen[dependency] = struct{}{}
			edges[reference] = append(edges[reference], dependency)
		}
	}
	if err := validateDependencyDAG(edges); err != nil {
		return releasegate.MatrixEvidence{}, err
	}
	observedBranches := make(map[string]string, len(observation.Repositories))
	for _, repository := range observation.Repositories {
		expectedRepository, exists := subject.RepositoryByReference[repository.Reference]
		expectedBranch, branchExists := subject.WorktreeBranchByReference[repository.Reference]
		if !exists || !branchExists || strings.TrimSpace(expectedRepository) == "" || repository.Branch != expectedBranch {
			return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper dependency QA provenance does not match")
		}
		observedBranches[repository.Reference] = repository.Branch
	}
	positions := make(map[string]int, len(observation.RepositoryExecutionOrder))
	for index, reference := range observation.RepositoryExecutionOrder {
		if _, selected := subject.RepositoryByReference[reference]; !selected {
			return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper dependency QA order is outside the publication subject")
		}
		positions[reference] = index
	}
	status := releasegate.StatusPassed
	for reference := range subject.RepositoryByReference {
		if _, exists := known[reference]; !exists {
			return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper dependency topology omits a published repository")
		}
		if _, exists := observedBranches[reference]; !exists {
			return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper dependency QA set is incomplete")
		}
		for _, dependency := range edges[reference] {
			if _, selected := subject.RepositoryByReference[dependency]; selected && positions[dependency] >= positions[reference] {
				status = releasegate.StatusFailed
			}
		}
	}
	seenWorkItems := make(map[uuid.UUID]struct{}, len(workItemDependencies))
	for _, dependency := range workItemDependencies {
		state := strings.TrimSpace(dependency.State)
		if dependency.ID == uuid.Nil || dependency.WorkItemID != workItemID || dependency.DependsOnWorkItemID == uuid.Nil || dependency.DependsOnWorkItemID == workItemID || state == "" {
			return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper work-item dependency is invalid")
		}
		if _, duplicate := seenWorkItems[dependency.DependsOnWorkItemID]; duplicate {
			return releasegate.MatrixEvidence{}, fmt.Errorf("release Gatekeeper work-item dependency is duplicated")
		}
		seenWorkItems[dependency.DependsOnWorkItemID] = struct{}{}
		if state != deliveryworkflow.StateReleased {
			status = releasegate.StatusFailed
		}
	}
	return releasegate.MatrixEvidence{MatrixDigest: matrixDigest, Status: status}, nil
}

var frozenWorkspaceReferencePattern = regexp.MustCompile(`^workspace://[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func validFrozenRepositoryReference(reference string) bool {
	if frozenWorkspaceReferencePattern.MatchString(reference) {
		return true
	}
	canonical, err := projectvault.CanonicalGitHubReference(reference)
	return err == nil && canonical == reference
}

func validateDependencyDAG(edges map[string][]string) error {
	states := make(map[string]uint8, len(edges))
	var visit func(string) error
	visit = func(reference string) error {
		switch states[reference] {
		case 1:
			return fmt.Errorf("release Gatekeeper frozen repository dependencies contain a cycle")
		case 2:
			return nil
		}
		states[reference] = 1
		for _, dependency := range edges[reference] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		states[reference] = 2
		return nil
	}
	for reference := range edges {
		if err := visit(reference); err != nil {
			return err
		}
	}
	return nil
}

type testAggregate struct {
	name     string
	expected int
	missing  bool
	failed   bool
}

// testsFromQAObservation resolves named commands per repository before it
// creates the policy-wide evidence expected by releasegate.Evaluate. A command
// from another repository can therefore never satisfy the same named gate.
func testsFromQAObservation(requiredKinds []string, requiredByRepository map[string][]string, subject publishedReleaseSubject, observation qaevidence.Observation) ([]releasegate.TestEvidence, error) {
	if err := qaevidence.Validate(observation); err != nil {
		return nil, fmt.Errorf("release Gatekeeper QA observation is invalid: %w", err)
	}
	if len(subject.RepositoryByReference) == 0 || len(subject.RepositoryByReference) != len(subject.WorktreeBranchByReference) || len(observation.Repositories) != len(subject.RepositoryByReference) {
		return nil, fmt.Errorf("release Gatekeeper QA repository set does not match the publication subject")
	}
	runsByReference := make(map[string]qaevidence.Repository, len(observation.Repositories))
	for _, repository := range observation.Repositories {
		expectedRepository, exists := subject.RepositoryByReference[repository.Reference]
		expectedBranch, branchExists := subject.WorktreeBranchByReference[repository.Reference]
		if !exists || !branchExists || expectedRepository == "" || repository.Branch != expectedBranch {
			return nil, fmt.Errorf("release Gatekeeper QA repository provenance does not match")
		}
		runsByReference[repository.Reference] = repository
	}
	referenceByRepository := make(map[string]string, len(subject.RepositoryByReference))
	for reference, repository := range subject.RepositoryByReference {
		key := strings.ToLower(strings.TrimSpace(repository))
		if key == "" || strings.TrimSpace(reference) == "" {
			return nil, fmt.Errorf("release Gatekeeper QA publication mapping is invalid")
		}
		if _, duplicate := referenceByRepository[key]; duplicate {
			return nil, fmt.Errorf("release Gatekeeper QA publication mapping is ambiguous")
		}
		if _, observed := runsByReference[reference]; !observed {
			return nil, fmt.Errorf("release Gatekeeper QA repository set does not match the publication subject")
		}
		referenceByRepository[key] = reference
	}

	aggregates := make(map[string]*testAggregate, len(requiredKinds))
	for _, kind := range requiredKinds {
		key := strings.ToLower(strings.TrimSpace(kind))
		if key == "" {
			return nil, fmt.Errorf("release Gatekeeper QA policy test identity is invalid")
		}
		aggregates[key] = &testAggregate{name: strings.TrimSpace(kind)}
	}
	for repository, kinds := range requiredByRepository {
		reference, exists := referenceByRepository[strings.ToLower(strings.TrimSpace(repository))]
		if !exists {
			return nil, fmt.Errorf("release Gatekeeper QA policy repository is outside the publication subject")
		}
		commands := make(map[string]bool, len(runsByReference[reference].Commands))
		for _, command := range runsByReference[reference].Commands {
			if command.Kind != "" {
				commands[strings.ToLower(command.Kind)] = command.Passed
			}
		}
		for _, kind := range kinds {
			key := strings.ToLower(strings.TrimSpace(kind))
			aggregate, exists := aggregates[key]
			if !exists {
				return nil, fmt.Errorf("release Gatekeeper QA repository policy does not match its composite policy")
			}
			aggregate.expected++
			passed, observed := commands[key]
			if !observed {
				aggregate.missing = true
			} else if !passed {
				aggregate.failed = true
			}
		}
	}

	tests := make([]releasegate.TestEvidence, 0, len(requiredKinds))
	for _, kind := range requiredKinds {
		aggregate := aggregates[strings.ToLower(strings.TrimSpace(kind))]
		if aggregate == nil || aggregate.expected == 0 {
			return nil, fmt.Errorf("release Gatekeeper QA composite policy has no repository requirement")
		}
		if aggregate.missing {
			continue
		}
		status := releasegate.StatusPassed
		if aggregate.failed {
			status = releasegate.StatusFailed
		}
		tests = append(tests, releasegate.TestEvidence{Kind: aggregate.name, MatrixDigest: observation.MatrixDigest, Status: status})
	}
	return tests, nil
}

func policyRepositoryReference(fallback string, revisions []models.DeliveryPolicyRevision) string {
	for _, revision := range revisions {
		candidate := strings.TrimSpace(revision.RepositoryReference)
		if candidate == "" {
			continue
		}
		canonical, err := projectvault.CanonicalGitHubReference(candidate)
		if err == nil && strings.EqualFold(canonical, fallback) {
			return canonical
		}
	}
	return fallback
}

func lowerValues(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.ToLower(strings.TrimSpace(value))
	}
	return result
}

func validateVaultRevision(projectID uuid.UUID, vault models.DeliveryProjectVaultRevision) error {
	if vault.ID == uuid.Nil || vault.ProjectID != projectID || vault.Version < 1 || vault.SchemaVersion != projectvault.SchemaVersion || vault.SourceOnboardingID == uuid.Nil || strings.TrimSpace(vault.PublishedBy) == "" || vault.PublishedAt.IsZero() {
		return fmt.Errorf("release Gatekeeper Vault revision provenance is invalid")
	}
	if !validHex(vault.Revision, 40) || !validHex(vault.ContentSHA256, 64) {
		return fmt.Errorf("release Gatekeeper Vault revision identity is invalid")
	}
	reference, err := projectvault.CanonicalGitHubReference(vault.RepositoryReference)
	if err != nil || reference != vault.RepositoryReference {
		return fmt.Errorf("release Gatekeeper Vault repository is invalid")
	}
	var manifest projectvault.Manifest
	if err := json.Unmarshal([]byte(vault.ManifestJSON), &manifest); err != nil {
		return fmt.Errorf("release Gatekeeper Vault manifest is invalid")
	}
	digest, err := projectvault.ManifestSHA256(manifest)
	if err != nil || !strings.EqualFold(digest, strings.TrimSpace(vault.ContentSHA256)) || manifest.SchemaVersion != projectvault.SchemaVersion || manifest.Repository.Reference != reference || !strings.EqualFold(manifest.Repository.Revision, strings.TrimSpace(vault.Revision)) {
		return fmt.Errorf("release Gatekeeper Vault manifest failed integrity checks")
	}
	return nil
}

func validHex(value string, size int) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != size {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
