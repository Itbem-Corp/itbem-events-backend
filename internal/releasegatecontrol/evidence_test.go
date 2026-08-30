package releasegatecontrol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/deliverypolicy"
	"events-stocks/internal/projectvault"
	"events-stocks/internal/releasegate"
	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var controlNow = time.Date(2026, time.August, 30, 18, 0, 0, 0, time.UTC)

func TestControlPlaneQueriesUseBoundedCaseInsensitiveRepositoryScopes(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: uuid.Must(uuid.NewV4())}
	mock.ExpectQuery(`SELECT DISTINCT ON \(LOWER\(repository_reference\)\).*FROM "delivery_project_vault_revisions"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if vaults, err := loadLatestVaults(db, project.ID, []string{"github://Example/API"}); err != nil || len(vaults) != 0 {
		t.Fatalf("bounded Vault query failed: %#v / %v", vaults, err)
	}
	mock.ExpectQuery(`SELECT \* FROM "delivery_policy_revisions".*LOWER\(repository_reference\).*LIMIT`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	revisions, decisions, err := loadPolicyLedger(db, project, "change-set:query", []string{"github://Example/API"})
	if err != nil || len(revisions) != 0 || len(decisions) != 0 {
		t.Fatalf("bounded policy query failed: %#v / %#v / %v", revisions, decisions, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPublishedRevisionMatrixUsesPlanAndConsumedPublicationGrants(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	workItemID := uuid.Must(uuid.NewV4())
	apiChangeID, webChangeID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	apiGrantID, webGrantID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	item := models.DeliveryWorkItem{
		ID:       workItemID,
		PlanJSON: `{"repository_impact":[{"reference":"workspace://api","impact":"changes"},{"reference":"workspace://web","impact":"changes"},{"reference":"workspace://docs","impact":"consulted"}]}`,
	}
	changeRows := sqlmock.NewRows([]string{"id", "work_item_id", "repository_ref", "branch", "commit_sha", "review_type", "pull_request_url", "metadata_json", "created_by", "created_at"}).
		AddRow(webChangeID, workItemID, "workspace://web", "itbem-agent/22222222-2222-4222-8222-222222222222", strings.Repeat("b", 40), "pull_request", "https://github.com/Example/Web/pull/8", publicationMetadata(webGrantID, strings.Repeat("2", 40), "Example/Web", "trunk"), "itbem-github-app", controlNow).
		AddRow(apiChangeID, workItemID, "workspace://api", "itbem-agent/11111111-1111-4111-8111-111111111111", strings.Repeat("a", 40), "pull_request", "https://github.com/Example/API/pull/7", publicationMetadata(apiGrantID, strings.Repeat("1", 40), "Example/API", "main"), "itbem-github-app", controlNow)
	mock.ExpectQuery(`SELECT \* FROM "delivery_change_sets".*ORDER BY created_at DESC, id DESC LIMIT`).WillReturnRows(changeRows)
	grantRows := sqlmock.NewRows([]string{"id", "work_item_id", "repository_ref", "base_sha", "git_hub_repository", "review_diff_sha256", "branch", "revoked_by", "revoked_at"}).
		AddRow(apiGrantID, workItemID, "workspace://api", strings.Repeat("1", 40), "example/api", strings.Repeat("c", 64), "itbem-agent/11111111-1111-4111-8111-111111111111", "itbem-github-app", controlNow).
		AddRow(webGrantID, workItemID, "workspace://web", strings.Repeat("2", 40), "example/web", strings.Repeat("d", 64), "itbem-agent/22222222-2222-4222-8222-222222222222", "itbem-github-app", controlNow)
	mock.ExpectQuery(`SELECT \* FROM "delivery_publication_grants" WHERE id IN`).WillReturnRows(grantRows)

	revisions, err := loadPublishedRevisionMatrix(db, item)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || revisions[0].Repository != "example/api" || revisions[0].Branch != "main" || revisions[0].SHA != strings.Repeat("a", 40) || revisions[1].Repository != "example/web" || revisions[1].Branch != "trunk" {
		t.Fatalf("unexpected authoritative publication matrix: %#v", revisions)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardUntrustedAssuranceRemovesReleaseWorkerClaims(t *testing.T) {
	input := releasegate.Input{
		Tests:         []releasegate.TestEvidence{{Kind: "unit", MatrixDigest: strings.Repeat("a", 64), Status: releasegate.StatusPassed}},
		Security:      []releasegate.SecurityEvidence{{Repository: "example/api", HeadSHA: strings.Repeat("a", 40), SecretScanPassed: true}},
		Compatibility: releasegate.MatrixEvidence{MatrixDigest: strings.Repeat("a", 64), Status: releasegate.StatusPassed},
		Migrations:    releasegate.MatrixEvidence{MatrixDigest: strings.Repeat("a", 64), Status: releasegate.StatusPassed},
		Dependencies:  releasegate.MatrixEvidence{MatrixDigest: strings.Repeat("a", 64), Status: releasegate.StatusPassed},
		Environment:   releasegate.MatrixEvidence{MatrixDigest: strings.Repeat("a", 64), Status: releasegate.StatusPassed},
		Recovery:      releasegate.RecoveryEvidence{MatrixDigest: strings.Repeat("a", 64), Classification: releasegate.RecoveryRollback, Evaluated: true},
	}
	discardUntrustedAssurance(&input)
	if len(input.Tests) != 0 || len(input.Security) != 0 || input.Compatibility.Status != "" || input.Migrations.Status != "" || input.Dependencies.Status != "" || input.Environment.Status != "" || input.Recovery.Evaluated {
		t.Fatalf("release worker assurance claim survived control-plane reset: %#v", input)
	}
}

func TestSameRevisionMatrixRejectsChangedRepositoryBranchOrSHA(t *testing.T) {
	want := []releasegate.Revision{
		{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)},
		{Repository: "example/web", Branch: "trunk", SHA: strings.Repeat("b", 40)},
	}
	reordered := []releasegate.Revision{want[1], want[0]}
	if !sameRevisionMatrix(want, reordered) {
		t.Fatal("canonical matrix order changed the release identity")
	}
	for _, forged := range [][]releasegate.Revision{
		{{Repository: "attacker/api", Branch: "main", SHA: strings.Repeat("a", 40)}, want[1]},
		{{Repository: "example/api", Branch: "production", SHA: strings.Repeat("a", 40)}, want[1]},
		{{Repository: "example/api", Branch: "main", SHA: strings.Repeat("c", 40)}, want[1]},
		{want[0]},
	} {
		if sameRevisionMatrix(want, forged) {
			t.Fatalf("changed release matrix was accepted: %#v", forged)
		}
	}
}

func publicationMetadata(grantID uuid.UUID, baseSHA, repository, targetBranch string) string {
	encoded, _ := json.Marshal(map[string]any{
		"publication_grant_id": grantID.String(), "base_sha": baseSHA, "remote_repository": repository,
		"target_branch": targetBranch, "branch_published": true, "verification_source": "itbem-github-app",
	})
	return string(encoded)
}

func TestResolveStoredEvidenceReplacesCandidateClaimsForMultiRepoMatrix(t *testing.T) {
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: uuid.Must(uuid.NewV4())}
	revisions := []releasegate.Revision{
		{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)},
		{Repository: "example/web", Branch: "main", SHA: strings.Repeat("b", 40)},
	}
	input := releasegate.Input{
		SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: "change-set:control",
		Revisions: revisions,
		Policy:    releasegate.Policy{Resolved: true, Digest: strings.Repeat("f", 64), RequiredTestKinds: []string{"candidate"}, Repositories: []releasegate.RepositoryPolicyEvidence{{Repository: "attacker/repo", Digest: strings.Repeat("f", 64), Resolved: true, ActionAllowed: true, BranchAllowed: true}}},
		Vault:     []releasegate.VaultEvidence{{Repository: "attacker/repo", HeadSHA: strings.Repeat("f", 40), RevisionID: "candidate", Reconciled: true}},
	}
	policyRevision, decision := controlPolicy(t, project, []string{"main"})
	vaults := []models.DeliveryProjectVaultRevision{
		controlVault(t, project.ID, "github://Example/Api", revisions[0].SHA),
		controlVault(t, project.ID, "github://EXAMPLE/Web", strings.Repeat("c", 40)),
	}
	repositories, _, err := repositoryReferences(revisions)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveStoredEvidence(input, project, repositories, vaults, []models.DeliveryPolicyRevision{policyRevision}, []models.DeliveryPolicyDecision{decision}, controlNow)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Policy.Resolved || len(resolved.Policy.Repositories) != 2 || strings.Join(resolved.Policy.RequiredTestKinds, ",") != "contract,unit" || len(resolved.Vault) != 2 {
		t.Fatalf("authoritative multi-repo evidence was not resolved: %#v", resolved)
	}
	if resolved.Policy.Digest == input.Policy.Digest || resolved.Policy.Repositories[0].Repository == "attacker/repo" || resolved.Vault[0].Repository == "attacker/repo" {
		t.Fatalf("candidate policy or Vault claim survived control-plane resolution: %#v", resolved)
	}
	if !resolved.Vault[0].Reconciled || resolved.Vault[1].Reconciled {
		t.Fatalf("Vault reconciliation was not bound to each exact SHA: %#v", resolved.Vault)
	}
	decisionResult := releasegate.Evaluate(resolved)
	if !hasControlReason(decisionResult, "vault_evidence_stale") {
		t.Fatalf("stale repository Vault was not visible to Gatekeeper: %#v", decisionResult)
	}
}

func TestResolveStoredEvidenceKeepsMissingPolicyAndVaultBlocked(t *testing.T) {
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: uuid.Must(uuid.NewV4())}
	revision := releasegate.Revision{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)}
	input := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: "change-set:missing", Revisions: []releasegate.Revision{revision}}
	repositories, _, _ := repositoryReferences(input.Revisions)
	resolved, err := resolveStoredEvidence(input, project, repositories, nil, nil, nil, controlNow)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Policy.Resolved || len(resolved.Vault) != 0 || len(resolved.Policy.Repositories) != 1 {
		t.Fatalf("missing authoritative evidence was invented: %#v", resolved)
	}
	decision := releasegate.Evaluate(resolved)
	for _, code := range []string{"repository_policy_unresolved", "policy_action_not_allowed", "target_branch_not_allowed", "vault_evidence_missing"} {
		if !hasControlReason(decision, code) {
			t.Fatalf("missing control-plane evidence reason %s: %#v", code, decision)
		}
	}
}

func TestResolveStoredEvidenceBlocksBranchOutsidePolicy(t *testing.T) {
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: uuid.Must(uuid.NewV4())}
	revision := releasegate.Revision{Repository: "example/api", Branch: "release/v2", SHA: strings.Repeat("a", 40)}
	input := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: "change-set:branch", Revisions: []releasegate.Revision{revision}}
	policyRevision, decision := controlPolicy(t, project, []string{"main"})
	repositories, _, _ := repositoryReferences(input.Revisions)
	resolved, err := resolveStoredEvidence(input, project, repositories, []models.DeliveryProjectVaultRevision{controlVault(t, project.ID, "github://example/api", revision.SHA)}, []models.DeliveryPolicyRevision{policyRevision}, []models.DeliveryPolicyDecision{decision}, controlNow)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Policy.Resolved || resolved.Policy.Repositories[0].BranchAllowed {
		t.Fatalf("branch outside policy was authorized: %#v", resolved.Policy)
	}
	if decision := releasegate.Evaluate(resolved); !hasControlReason(decision, "target_branch_not_allowed") {
		t.Fatalf("branch policy block was not explained: %#v", decision)
	}
}

func TestResolveStoredEvidenceRejectsTamperedVaultManifest(t *testing.T) {
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: uuid.Must(uuid.NewV4())}
	revision := releasegate.Revision{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)}
	input := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: "change-set:tampered", Revisions: []releasegate.Revision{revision}}
	vault := controlVault(t, project.ID, "github://example/api", revision.SHA)
	vault.ManifestJSON = `{"schema_version":1,"scope":"repository"}`
	repositories, _, _ := repositoryReferences(input.Revisions)
	if _, err := resolveStoredEvidence(input, project, repositories, []models.DeliveryProjectVaultRevision{vault}, nil, nil, controlNow); err == nil {
		t.Fatal("tampered Vault content was accepted by its stored digest")
	}
}

func TestValidateVaultProvenanceRowsBindsApprovedOnboarding(t *testing.T) {
	projectID := uuid.Must(uuid.NewV4())
	vault := controlVault(t, projectID, "github://example/api", strings.Repeat("a", 40))
	approvedAt := vault.PublishedAt
	onboarding := models.DeliveryRepositoryOnboarding{
		ID: vault.SourceOnboardingID, ProjectID: projectID, RepositoryReference: vault.RepositoryReference,
		Revision: vault.Revision, Status: "approved", VaultSHA256: vault.ContentSHA256,
		ApprovedBy: vault.PublishedBy, ApprovedAt: &approvedAt,
	}
	if err := validateVaultProvenanceRows([]models.DeliveryProjectVaultRevision{vault}, []models.DeliveryRepositoryOnboarding{onboarding}); err != nil {
		t.Fatalf("matching approved Vault provenance was rejected: %v", err)
	}
	onboarding.Revision = strings.Repeat("b", 40)
	if err := validateVaultProvenanceRows([]models.DeliveryProjectVaultRevision{vault}, []models.DeliveryRepositoryOnboarding{onboarding}); err == nil {
		t.Fatal("Vault provenance for another repository revision was accepted")
	}
}

func controlPolicy(t *testing.T, project models.DeliveryProject, branches []string) (models.DeliveryPolicyRevision, models.DeliveryPolicyDecision) {
	t.Helper()
	mode, method := deliverypolicy.ModeRelease, "squash"
	tests, health := []string{"unit", "contract"}, []string{"health"}
	workflow, environment, recovery := ".github/workflows/deploy.yml", "production", string(releasegate.RecoveryRollback)
	patch := deliverypolicy.Patch{
		Mode: &mode, MergeMethod: &method, RequiredTestKinds: &tests, AllowedTargetBranches: &branches,
		DeploymentWorkflow: &workflow, DeploymentEnvironment: &environment, RequiredHealthChecks: &health, RecoveryDefault: &recovery,
	}
	id := uuid.Must(uuid.NewV4())
	layer := deliverypolicy.Layer{
		SchemaVersion: deliverypolicy.SchemaVersion, RevisionID: id.String(), Level: deliverypolicy.LevelProject,
		OrganizationID: project.ClientID.String(), ProjectID: project.ID.String(), Patch: patch,
	}
	digest, err := deliverypolicy.LayerDigest(layer)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(patch)
	revision := models.DeliveryPolicyRevision{
		ID: id, SchemaVersion: deliverypolicy.SchemaVersion, Level: string(deliverypolicy.LevelProject),
		OrganizationID: project.ClientID.String(), ProjectID: &project.ID, PatchJSON: string(encoded), ContentSHA256: digest,
		ProposedBy: "policy-author", CreatedAt: controlNow.Add(-2 * time.Hour),
	}
	decision := models.DeliveryPolicyDecision{
		ID: uuid.Must(uuid.NewV4()), PolicyRevisionID: id, PolicyDigest: digest, Action: "approved",
		ActorCognitoSub: "independent-policy-reviewer", OccurredAt: controlNow.Add(-time.Hour),
	}
	return revision, decision
}

func controlVault(t *testing.T, projectID uuid.UUID, reference, revision string) models.DeliveryProjectVaultRevision {
	t.Helper()
	manifest := projectvault.Manifest{
		SchemaVersion: projectvault.SchemaVersion, Scope: "repository",
		Repository: projectvault.Repository{Reference: reference, DefaultBranch: "main", Revision: revision}, Entries: []projectvault.VaultEntry{},
	}
	digest, err := projectvault.ManifestSHA256(manifest)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(manifest)
	return models.DeliveryProjectVaultRevision{
		ID: uuid.Must(uuid.NewV4()), ProjectID: projectID, RepositoryReference: reference, Version: 1,
		Revision: revision, SchemaVersion: projectvault.SchemaVersion, ManifestJSON: string(encoded), ContentSHA256: digest,
		SourceOnboardingID: uuid.Must(uuid.NewV4()), PublishedBy: "vault-reviewer", PublishedAt: controlNow.Add(-time.Hour),
	}
}

func hasControlReason(decision releasegate.Decision, code string) bool {
	for _, reason := range decision.Reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}
