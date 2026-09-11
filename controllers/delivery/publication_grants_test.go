package delivery

import (
	"strings"
	"testing"

	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"

	"github.com/gofrs/uuid"
)

func TestNormalizedPublicationCapabilitiesRequiresSafePublishingChain(t *testing.T) {
	capabilities, err := normalizedPublicationCapabilities(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(capabilities) != 3 || capabilities[0] != "branch:publish" || capabilities[1] != "commit:stage" || capabilities[2] != "pull_request:create" {
		t.Fatalf("unexpected default capabilities: %#v", capabilities)
	}
	if _, err := normalizedPublicationCapabilities([]string{"branch:publish"}); err == nil {
		t.Fatal("publishing without a staged reviewed commit must be rejected")
	}
	if _, err := normalizedPublicationCapabilities([]string{"commit:stage", "branch:publish", "merge:main"}); err == nil {
		t.Fatal("unsupported publishing capabilities must be rejected")
	}
}

func TestPublicationGrantPreconditionAllowsPendingCodeReviewWithoutAnApprovalGate(t *testing.T) {
	// A grant binds a reviewed local worktree for later publication; it is not
	// itself a code-review decision. Keep this test at the controller boundary
	// so a future refactor cannot accidentally restore an "approved gate"
	// prerequisite and make the publication/review ordering impossible.
	if err := publicationGrantPrecondition(models.DeliveryWorkItem{State: " " + deliveryworkflow.StateCodeReview + " "}); err != nil {
		t.Fatalf("pending code review must allow a publication grant without a prior approval gate: %v", err)
	}
	for _, state := range []string{"", deliveryworkflow.StateImplementation, deliveryworkflow.StatePreviewPending, deliveryworkflow.StateQARunning} {
		if err := publicationGrantPrecondition(models.DeliveryWorkItem{State: state}); err == nil {
			t.Fatalf("publication grant unexpectedly allowed outside pending code review: %q", state)
		}
	}
}

func TestApprovedPrimaryRepositoryReferenceUsesFrozenPrimary(t *testing.T) {
	primary := models.DeliveryContextSnapshot{ID: uuid.Must(uuid.NewV4()), Kind: "repository", Reference: "workspace://backend", MetadataJSON: `{"repository_role":"primary"}`}
	supporting := models.DeliveryContextSnapshot{ID: uuid.Must(uuid.NewV4()), Kind: "repository", Reference: "workspace://frontend", MetadataJSON: `{"repository_role":"supporting"}`}
	got, err := approvedPrimaryRepositoryReference([]models.DeliveryContextSnapshot{supporting, primary})
	if err != nil || got != "workspace://backend" {
		t.Fatalf("primary repository = %q, %v", got, err)
	}
	if _, err := approvedPrimaryRepositoryReference([]models.DeliveryContextSnapshot{supporting, models.DeliveryContextSnapshot{Kind: "repository", Reference: "workspace://other"}}); err == nil {
		t.Fatal("an ambiguous multi-repository scope must be rejected")
	}
}

func TestReviewedChangeSetDigestRequiresARealImplementationFingerprint(t *testing.T) {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	baseSHA := strings.Repeat("b", 40)
	change := models.DeliveryChangeSet{MetadataJSON: `{"review_diff_sha256":"` + digest + `","github_repository":"itbem-corp/itbem-events-backend","base_sha":"` + baseSHA + `"}`}
	if got, err := reviewedChangeSetDigest(change); err != nil || got != digest {
		t.Fatalf("reviewed digest = %q, %v", got, err)
	}
	if _, err := reviewedChangeSetDigest(models.DeliveryChangeSet{MetadataJSON: `{}`}); err == nil {
		t.Fatal("a passed review without a diff fingerprint must not authorize publication")
	}
	if got, err := reviewedChangeSetGitHubRepository(change); err != nil || got != "itbem-corp/itbem-events-backend" {
		t.Fatalf("reviewed GitHub repository = %q, %v", got, err)
	}
	if _, err := reviewedChangeSetGitHubRepository(models.DeliveryChangeSet{MetadataJSON: `{}`}); err == nil {
		t.Fatal("a passed review without a GitHub repository identity must not authorize publication")
	}
	if got, err := reviewedChangeSetBaseSHA(change); err != nil || got != baseSHA {
		t.Fatalf("reviewed base SHA = %q, %v", got, err)
	}
	if _, err := reviewedChangeSetBaseSHA(models.DeliveryChangeSet{MetadataJSON: `{}`}); err == nil {
		t.Fatal("a passed review without a base SHA must not authorize publication")
	}
}

func TestTrustedImplementationChangeSetRequiresAgentProvenance(t *testing.T) {
	taskID := uuid.Must(uuid.NewV4())
	valid := models.DeliveryChangeSet{
		RepositoryRef: "workspace://backend",
		Branch:        "itbem-agent/123",
		ReviewType:    "local_worktree",
		CIStatus:      "passed",
		CreatedBy:     "itbem-local-agent",
		MetadataJSON:  `{"automation_task_id":"` + taskID.String() + `","verification_source":"itbem-local-agent","worktree":"workspace://backend#itbem-agent/123"}`,
	}
	if !trustedImplementationChangeSet(valid) {
		t.Fatal("an agent callback with a matching worktree must be trusted")
	}
	for _, invalid := range []models.DeliveryChangeSet{
		{RepositoryRef: valid.RepositoryRef, Branch: valid.Branch, ReviewType: valid.ReviewType, CIStatus: valid.CIStatus, CreatedBy: "reviewer", MetadataJSON: valid.MetadataJSON},
		{RepositoryRef: valid.RepositoryRef, Branch: valid.Branch, ReviewType: valid.ReviewType, CIStatus: valid.CIStatus, CreatedBy: valid.CreatedBy, MetadataJSON: `{"automation_task_id":"` + taskID.String() + `","verification_source":"itbem-local-agent","worktree":"workspace://other#itbem-agent/123"}`},
		{RepositoryRef: valid.RepositoryRef, Branch: valid.Branch, ReviewType: valid.ReviewType, CIStatus: valid.CIStatus, CreatedBy: valid.CreatedBy, MetadataJSON: `{"automation_task_id":"not-a-uuid","verification_source":"itbem-local-agent","worktree":"workspace://backend#itbem-agent/123"}`},
	} {
		if trustedImplementationChangeSet(invalid) {
			t.Fatalf("untrusted implementation record became publishable: %#v", invalid)
		}
	}
}

func TestGrantRepositoryReferenceScopesEveryMultiRepositoryAuthorization(t *testing.T) {
	backend := models.DeliveryContextSnapshot{Kind: "repository", Reference: "workspace://backend"}
	dashboard := models.DeliveryContextSnapshot{Kind: "repository", Reference: "workspace://dashboard"}
	if got, err := grantRepositoryReference("", []models.DeliveryContextSnapshot{backend}); err != nil || got != backend.Reference {
		t.Fatalf("single repository grant should remain concise: %q / %v", got, err)
	}
	if _, err := grantRepositoryReference("", []models.DeliveryContextSnapshot{backend, dashboard}); err == nil {
		t.Fatal("multi-repository grant must require an explicit reviewed repository")
	}
	if got, err := grantRepositoryReference(dashboard.Reference, []models.DeliveryContextSnapshot{backend, dashboard}); err != nil || got != dashboard.Reference {
		t.Fatalf("explicit supporting repository grant should be allowed: %q / %v", got, err)
	}
	if _, err := grantRepositoryReference("workspace://unknown", []models.DeliveryContextSnapshot{backend, dashboard}); err == nil {
		t.Fatal("grant must reject a repository outside frozen context")
	}
}
