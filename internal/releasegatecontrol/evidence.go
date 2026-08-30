// Package releasegatecontrol resolves control-plane-owned policy and Vault
// evidence immediately before a Gatekeeper decision is appended to the ledger.
package releasegatecontrol

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"events-stocks/internal/deliverypolicy"
	"events-stocks/internal/deliverypolicystore"
	"events-stocks/internal/projectvault"
	"events-stocks/internal/releasegate"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

const (
	maxPolicyRevisions  = 500
	maxPublishedChanges = 128
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

	authoritativeRevisions, err := loadPublishedRevisionMatrix(db, item)
	if err != nil {
		return releasegate.Input{}, err
	}
	if !sameRevisionMatrix(input.Revisions, authoritativeRevisions) {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper GitHub evidence does not match the current control-plane publication matrix")
	}
	input.Revisions = authoritativeRevisions
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
	return resolveStoredEvidence(input, project, repositories, vaults, revisions, decisions, evaluatedAt.UTC())
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

// loadPublishedRevisionMatrix rebuilds the exact release subject from the
// approved plan, GitHub App publication records, and their consumed grants.
// The callback's candidate matrix is deliberately ignored.
func loadPublishedRevisionMatrix(db *gorm.DB, item models.DeliveryWorkItem) ([]releasegate.Revision, error) {
	required, err := requiredChangedRepositories(item.PlanJSON)
	if err != nil {
		return nil, err
	}
	var changes []models.DeliveryChangeSet
	if err := db.Where("work_item_id = ? AND review_type = ?", item.ID, "pull_request").
		Order("created_at DESC, id DESC").Limit(maxPublishedChanges + 1).Find(&changes).Error; err != nil {
		return nil, fmt.Errorf("load release Gatekeeper published changes: %w", err)
	}
	if len(changes) > maxPublishedChanges {
		return nil, fmt.Errorf("release Gatekeeper published change history exceeds the bounded limit")
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
			return nil, fmt.Errorf("release Gatekeeper latest publication for %s is invalid: %w", reference, parseErr)
		}
		selected[reference] = selectedPublishedChange{change: change, metadata: metadata, grantID: grantID}
	}
	if len(selected) != len(required) {
		return nil, fmt.Errorf("release Gatekeeper requires one current GitHub App publication for every changed repository")
	}

	grantIDs := make([]uuid.UUID, 0, len(selected))
	for _, value := range selected {
		grantIDs = append(grantIDs, value.grantID)
	}
	var grants []models.DeliveryPublicationGrant
	if err := db.Where("id IN ?", grantIDs).Find(&grants).Error; err != nil {
		return nil, fmt.Errorf("load release Gatekeeper publication grants: %w", err)
	}
	if len(grants) != len(grantIDs) {
		return nil, fmt.Errorf("release Gatekeeper publication grant evidence is missing or ambiguous")
	}
	grantByID := make(map[uuid.UUID]models.DeliveryPublicationGrant, len(grants))
	for _, grant := range grants {
		if grant.ID == uuid.Nil {
			return nil, fmt.Errorf("release Gatekeeper publication grant identity is invalid")
		}
		if _, duplicate := grantByID[grant.ID]; duplicate {
			return nil, fmt.Errorf("release Gatekeeper publication grant evidence is duplicated")
		}
		grantByID[grant.ID] = grant
	}

	revisions := make([]releasegate.Revision, 0, len(selected))
	for reference, value := range selected {
		grant, ok := grantByID[value.grantID]
		if !ok {
			return nil, fmt.Errorf("release Gatekeeper publication grant is missing")
		}
		if err := validatePublicationGrant(item.ID, reference, value, grant); err != nil {
			return nil, err
		}
		revision := releasegate.Revision{
			Repository: strings.ToLower(strings.TrimSpace(value.metadata.RemoteRepository)),
			Branch:     strings.TrimSpace(value.metadata.TargetBranch),
			SHA:        strings.ToLower(strings.TrimSpace(value.change.CommitSHA)),
		}
		if _, err := releasegate.RevisionMatrixDigest([]releasegate.Revision{revision}); err != nil {
			return nil, fmt.Errorf("release Gatekeeper stored publication revision is invalid")
		}
		revisions = append(revisions, revision)
	}
	sort.Slice(revisions, func(left, right int) bool {
		return strings.ToLower(revisions[left].Repository) < strings.ToLower(revisions[right].Repository)
	})
	if _, err := releasegate.RevisionMatrixDigest(revisions); err != nil {
		return nil, fmt.Errorf("release Gatekeeper stored publication matrix is invalid")
	}
	return revisions, nil
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
	input.Policy = releasegate.Policy{Resolved: true, RequiredTestKinds: []string{}, Repositories: []releasegate.RepositoryPolicyEvidence{}}
	input.Vault = []releasegate.VaultEvidence{}
	vaultByReference := make(map[string]models.DeliveryProjectVaultRevision, len(vaults))
	for _, vault := range vaults {
		if err := validateVaultRevision(project.ID, vault); err != nil {
			return releasegate.Input{}, err
		}
		key := strings.ToLower(strings.TrimSpace(vault.RepositoryReference))
		if _, duplicate := vaultByReference[key]; duplicate {
			return releasegate.Input{}, fmt.Errorf("release Gatekeeper Vault repository is duplicated")
		}
		vaultByReference[key] = vault
	}

	requiredTests := map[string]string{}
	for _, matrixRevision := range input.Revisions {
		repository := strings.ToLower(strings.TrimSpace(matrixRevision.Repository))
		reference := repositories[repository]
		context := deliverypolicy.Context{
			OrganizationID: project.ClientID.String(), ProjectID: project.ID.String(), Repository: policyRepositoryReference(reference, revisions), ChangeSetID: input.ChangeSetID,
		}
		policy, err := deliverypolicystore.ResolveEffective(context, revisions, decisions, evaluatedAt)
		if err != nil {
			return releasegate.Input{}, fmt.Errorf("resolve release Gatekeeper policy for %s: %w", repository, err)
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
			}
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
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper composite policy is invalid: %w", err)
	}
	input.Policy.Digest = digest
	return input, nil
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
