package delivery

import (
	"testing"

	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"github.com/gofrs/uuid"
)

func TestAgentOperationForSubmission(t *testing.T) {
	tests := []struct {
		action    deliveryworkflow.Action
		operation string
		phase     string
	}{
		{deliveryworkflow.ActionSubmitPlan, "delivery.plan", "plan"},
		{deliveryworkflow.ActionSubmitCodeReview, "delivery.implementation", "implementation"},
		{deliveryworkflow.ActionSubmitQA, "delivery.qa", "qa"},
		{deliveryworkflow.ActionApproveRelease, "delivery.summary", "summary"},
	}

	for _, test := range tests {
		operation, phase := agentOperationForSubmission(test.action)
		if operation != test.operation || phase != test.phase {
			t.Fatalf("%s = (%q, %q), want (%q, %q)", test.action, operation, phase, test.operation, test.phase)
		}
	}
}

func TestValidWebURL(t *testing.T) {
	for _, value := range []string{"https://github.com/itbem/project/pull/42", "http://localhost:3000/preview"} {
		if !validWebURL(value) {
			t.Fatalf("expected %q to be accepted", value)
		}
	}
	for _, value := range []string{"", "ftp://example.test/change", "https:///missing-host", "javascript:alert(1)"} {
		if validWebURL(value) {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestDeliveryArtifactReferenceAcceptsOnlyTaskScopedAssets(t *testing.T) {
	taskID := uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationOutputBucket: "itbem-ai-outputs-local"}
	reference := "s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/01-preview.png"
	parsedTaskID, key, name, ok := deliveryArtifactReference(cfg, reference)
	if !ok || parsedTaskID != taskID || key != "automation/"+taskID.String()+"/artifacts/01-preview.png" || name != "01-preview.png" {
		t.Fatalf("unexpected private artifact parse: %s / %s / %s / %v", parsedTaskID, key, name, ok)
	}
	if _, _, _, ok := deliveryArtifactReference(cfg, "s3://itbem-ai-outputs-local/automation/"+taskID.String()+"/result.json"); ok {
		t.Fatal("result documents must not be served as visual artifacts")
	}
}

func TestPublicationGrantInvalidationFollowsWorkflowBoundary(t *testing.T) {
	invalidating := []deliveryworkflow.Action{
		deliveryworkflow.ActionPreviewReady,
		deliveryworkflow.ActionRequestQAChanges,
		deliveryworkflow.ActionApproveQA,
		deliveryworkflow.ActionApproveRelease,
		deliveryworkflow.ActionBlock,
		deliveryworkflow.ActionCancel,
	}
	for _, action := range invalidating {
		if !publicationGrantsInvalidatedBy(action) {
			t.Fatalf("%q must close a live publication grant", action)
		}
	}
	for _, action := range []deliveryworkflow.Action{deliveryworkflow.ActionSubmitPlan, deliveryworkflow.ActionApprovePlan, deliveryworkflow.ActionSubmitCodeReview} {
		if publicationGrantsInvalidatedBy(action) {
			t.Fatalf("%q must not invalidate a grant outside the publishing lifecycle", action)
		}
	}
}

func TestDeliveryEvidenceSHA256AcceptsCanonicalDigestAndFailsClosedForMalformedMetadata(t *testing.T) {
	digest, present, err := deliveryEvidenceSHA256(`{"sha256":"ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}`)
	if err != nil || !present || digest != "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" {
		t.Fatalf("unexpected canonical digest parse: %q / %v / %v", digest, present, err)
	}
	if _, present, err := deliveryEvidenceSHA256(`{}`); err != nil || present {
		t.Fatalf("legacy evidence must remain readable: %v / %v", present, err)
	}
	if _, _, err := deliveryEvidenceSHA256(`{"sha256":"not-a-digest"}`); err == nil {
		t.Fatal("malformed explicit integrity metadata must fail closed")
	}
}

func TestCodeReviewRequiredRepositoriesUsesOnlyApprovedChangedScope(t *testing.T) {
	plan := `{"repository_impact":[{"reference":"workspace://api","impact":"changes"},{"reference":"workspace://dashboard","impact":"consulted"},{"reference":"workspace://worker","impact":"changes"}]}`
	required, err := codeReviewRequiredRepositories(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 2 {
		t.Fatalf("required repositories = %#v, want two", required)
	}
	for _, reference := range []string{"workspace://api", "workspace://worker"} {
		if _, ok := required[reference]; !ok {
			t.Fatalf("%s must require its own review", reference)
		}
	}
	if _, ok := required["workspace://dashboard"]; ok {
		t.Fatal("consulted repositories must not require a change review")
	}
}

func TestCodeReviewRequiredRepositoriesFailsClosedForMalformedModernMatrix(t *testing.T) {
	for _, plan := range []string{
		`{"summary":"missing matrix"}`,
		`{"repository_impact":"workspace://api"}`,
		`{"repository_impact":[{"reference":"workspace://api","impact":"unknown"}]}`,
		`{"repository_impact":[{"reference":"","impact":"changes"}]}`,
	} {
		if _, err := codeReviewRequiredRepositories(plan); err == nil {
			t.Fatalf("modern plan must fail closed: %s", plan)
		}
	}
	for _, legacy := range []string{"", "{}"} {
		required, err := codeReviewRequiredRepositories(legacy)
		if err != nil || len(required) != 0 {
			t.Fatalf("legacy plan should retain one-review compatibility: %#v / %v", required, err)
		}
	}
}

func TestValidPublishedChangeRecordRequiresGitHubAppEvidence(t *testing.T) {
	valid := models.DeliveryChangeSet{
		RepositoryRef: "workspace://backend", Branch: "itbem-agent/123", ReviewType: "pull_request",
		MetadataJSON: `{"branch_published":true,"verification_source":"itbem-github-app"}`,
	}
	if !validPublishedChangeRecord(valid) {
		t.Fatal("published GitHub App evidence should be accepted")
	}
	for _, invalid := range []models.DeliveryChangeSet{
		{RepositoryRef: valid.RepositoryRef, Branch: valid.Branch, ReviewType: "pull_request", MetadataJSON: `{"branch_published":true}`},
		{RepositoryRef: valid.RepositoryRef, Branch: valid.Branch, ReviewType: "local_worktree", MetadataJSON: valid.MetadataJSON},
		{RepositoryRef: valid.RepositoryRef, Branch: valid.Branch, ReviewType: "pull_request", MetadataJSON: `{"branch_published":false,"verification_source":"itbem-github-app"}`},
	} {
		if validPublishedChangeRecord(invalid) {
			t.Fatalf("unproven publication must not unlock preview: %#v", invalid)
		}
	}
}

func TestMissingPublishedRepositoriesRequiresEveryChangedRepository(t *testing.T) {
	required := map[string]struct{}{"workspace://backend": {}, "workspace://dashboard": {}}
	backend := models.DeliveryChangeSet{RepositoryRef: "workspace://backend", Branch: "itbem-agent/1", ReviewType: "pull_request", MetadataJSON: `{"branch_published":true,"verification_source":"itbem-github-app"}`}
	if missing := missingPublishedRepositories(required, []models.DeliveryChangeSet{backend}); len(missing) != 1 || missing[0] != "workspace://dashboard" {
		t.Fatalf("preview must remain blocked for the unpublished repository: %#v", missing)
	}
	dashboard := backend
	dashboard.RepositoryRef, dashboard.Branch = "workspace://dashboard", "itbem-agent/2"
	if missing := missingPublishedRepositories(required, []models.DeliveryChangeSet{backend, dashboard}); len(missing) != 0 {
		t.Fatalf("all published repositories should satisfy preview coverage: %#v", missing)
	}
}
