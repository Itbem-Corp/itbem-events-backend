package deliverypolicy

import (
	"strings"
	"testing"
	"time"

	"events-stocks/internal/releasegate"
)

var policyNow = time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)

func pointer[T any](value T) *T { return &value }

func approvedLayer(t *testing.T, level Level, context Context, patch Patch) Layer {
	t.Helper()
	layer := Layer{
		SchemaVersion: SchemaVersion, RevisionID: "policy-" + string(level), Level: level, Patch: patch,
		Approved: true, ApprovedBy: "policy-owner", ApprovedAt: policyNow.Add(-time.Hour),
	}
	switch level {
	case LevelOrganization:
		layer.OrganizationID = context.OrganizationID
	case LevelProject:
		layer.OrganizationID, layer.ProjectID = context.OrganizationID, context.ProjectID
	case LevelRepository:
		layer.OrganizationID, layer.ProjectID, layer.Repository = context.OrganizationID, context.ProjectID, context.Repository
	case LevelOverride:
		expires := policyNow.Add(time.Hour)
		layer.OrganizationID, layer.ProjectID, layer.Repository, layer.ChangeSetID = context.OrganizationID, context.ProjectID, context.Repository, context.ChangeSetID
		layer.Reason, layer.ExpiresAt = "approved exception for this exact change set", &expires
	}
	digest, err := LayerDigest(layer)
	if err != nil {
		t.Fatal(err)
	}
	layer.Digest = digest
	return layer
}

func releaseContext() Context {
	return Context{OrganizationID: "org-42", ProjectID: "project-42", Repository: "https://github.com/Example/service-api", ChangeSetID: "change-set:42"}
}

func TestResolveAppliesHierarchyIndependentOfInputOrder(t *testing.T) {
	context := releaseContext()
	platformTests := []string{"unit"}
	projectMode, mergeMethod := ModeMerge, "squash"
	projectBranches := []string{"develop"}
	repositoryTests := []string{"contract", "unit"}
	overrideBranches := []string{"release/v2"}
	layers := []Layer{
		approvedLayer(t, LevelOverride, context, Patch{AllowedTargetBranches: &overrideBranches}),
		approvedLayer(t, LevelPlatform, context, Patch{RequiredTestKinds: &platformTests}),
		approvedLayer(t, LevelRepository, context, Patch{RequiredTestKinds: &repositoryTests}),
		approvedLayer(t, LevelProject, context, Patch{Mode: &projectMode, AllowedTargetBranches: &projectBranches, MergeMethod: &mergeMethod}),
	}
	first, err := Resolve(context, layers, policyNow)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(context, []Layer{layers[2], layers[1], layers[3], layers[0]}, policyNow)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Resolved || first.Digest != second.Digest || first.Mode != ModeMerge || first.MergeMethod != "squash" {
		t.Fatalf("hierarchical policy was not stable: %#v / %#v", first, second)
	}
	if strings.Join(first.RequiredTestKinds, ",") != "contract,unit" || strings.Join(first.AllowedTargetBranches, ",") != "release/v2" {
		t.Fatalf("narrower approved layers did not win: %#v", first)
	}
	if !first.AllowsTargetBranch("release/v2") || first.AllowsTargetBranch("release/v2-hotfix") || first.AllowsTargetBranch("Release/v2") {
		t.Fatalf("target branch authorization was not exact: %#v", first.AllowedTargetBranches)
	}
	gate := first.GatePolicyFor(releasegate.ActionMerge)
	if !gate.Resolved || gate.Digest != first.Digest || len(gate.RequiredTestKinds) != 2 {
		t.Fatalf("resolved merge policy did not bind the Gatekeeper: %#v", gate)
	}
}

func TestReviewOnlyRequiresAnExplicitEmptyTestPolicyAndNeverGrantsMerge(t *testing.T) {
	context := releaseContext()
	mode := ModeReviewOnly
	tests := []string{}
	branches := []string{"trunk"}
	policy, err := Resolve(context, []Layer{approvedLayer(t, LevelProject, context, Patch{
		Mode: &mode, RequiredTestKinds: &tests, AllowedTargetBranches: &branches,
	})}, policyNow)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Resolved || len(policy.RequiredTestKinds) != 0 {
		t.Fatalf("explicit review-only policy should resolve without invented tests: %#v", policy)
	}
	if gate := policy.GatePolicyFor(releasegate.ActionMerge); gate.Resolved {
		t.Fatalf("review-only policy became merge authority: %#v", gate)
	}

	missingTests, err := Resolve(context, []Layer{approvedLayer(t, LevelProject, context, Patch{
		Mode: &mode, AllowedTargetBranches: &branches,
	})}, policyNow)
	if err != nil {
		t.Fatal(err)
	}
	if missingTests.Resolved || !contains(missingTests.Missing, "required_test_kinds") {
		t.Fatalf("an inherited absence was mistaken for explicit review-only configuration: %#v", missingTests)
	}
}

func TestReleasePolicyRequiresWorkflowEnvironmentReferencesHealthAndRecovery(t *testing.T) {
	context := releaseContext()
	mode, mergeMethod := ModeRelease, "merge"
	tests, branches := []string{"unit"}, []string{"production"}
	base := approvedLayer(t, LevelProject, context, Patch{
		Mode: &mode, MergeMethod: &mergeMethod, RequiredTestKinds: &tests, AllowedTargetBranches: &branches,
	})
	policy, err := Resolve(context, []Layer{base}, policyNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"deployment_workflow", "deployment_environment", "required_secret_references", "required_variable_references", "required_health_checks", "recovery_default"} {
		if !contains(policy.Missing, required) {
			t.Fatalf("release policy did not report %s: %#v", required, policy.Missing)
		}
	}
	workflow, environment, recovery := ".github/workflows/deploy.yml", "production", "rollback"
	health := []string{"api readiness"}
	secrets, variables := []string{"database_url", "aws_role_arn"}, []string{}
	repository := approvedLayer(t, LevelRepository, context, Patch{
		DeploymentWorkflow: &workflow, DeploymentEnvironment: &environment,
		RequiredSecretReferences: &secrets, RequiredVariableReferences: &variables,
		RequiredHealthChecks: &health, RecoveryDefault: &recovery,
	})
	policy, err = Resolve(context, []Layer{base, repository}, policyNow)
	if err != nil || !policy.Resolved || !policy.GatePolicyFor(releasegate.ActionRelease).Resolved {
		t.Fatalf("complete release policy did not resolve: %#v / %v", policy, err)
	}
	if strings.Join(policy.RequiredSecretReferences, ",") != "AWS_ROLE_ARN,DATABASE_URL" || policy.RequiredVariableReferences == nil || len(policy.RequiredVariableReferences) != 0 {
		t.Fatalf("environment references were not explicit and canonical: %#v / %#v", policy.RequiredSecretReferences, policy.RequiredVariableReferences)
	}
}

func TestPolicyRejectsUnsafeEnvironmentReferences(t *testing.T) {
	context := releaseContext()
	for name, values := range map[string][]string{
		"reserved":      {"GITHUB_TOKEN"},
		"punctuation":   {"DATABASE-URL"},
		"duplicate":     {"database_url", "DATABASE_URL"},
		"leading digit": {"2FA_SECRET"},
	} {
		t.Run(name, func(t *testing.T) {
			layer := approvedLayer(t, LevelRepository, context, Patch{RequiredSecretReferences: &values})
			if _, err := Resolve(context, []Layer{layer}, policyNow); err == nil {
				t.Fatalf("unsafe environment reference was accepted: %#v", values)
			}
		})
	}
}

func TestResolveRejectsUnapprovedExpiredMismatchedDuplicateAndTamperedLayers(t *testing.T) {
	context := releaseContext()
	mode := ModeReviewOnly
	tests, branches := []string{}, []string{"main"}
	valid := approvedLayer(t, LevelProject, context, Patch{Mode: &mode, RequiredTestKinds: &tests, AllowedTargetBranches: &branches})
	cases := []struct {
		name   string
		layers []Layer
	}{
		{name: "unapproved", layers: []Layer{func() Layer { changed := valid; changed.Approved = false; return changed }()}},
		{name: "scope mismatch", layers: []Layer{func() Layer {
			changed := valid
			changed.ProjectID = "other"
			changed.Digest, _ = LayerDigest(changed)
			return changed
		}()}},
		{name: "tampered", layers: []Layer{func() Layer { changed := valid; changed.RevisionID = "tampered"; return changed }()}},
		{name: "duplicate", layers: []Layer{valid, valid}},
		{name: "expired override", layers: []Layer{func() Layer {
			changed := approvedLayer(t, LevelOverride, context, Patch{})
			expired := policyNow.Add(-time.Second)
			changed.ExpiresAt = &expired
			changed.Digest, _ = LayerDigest(changed)
			return changed
		}()}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Resolve(context, test.layers, policyNow); err == nil {
				t.Fatalf("unsafe policy layers were accepted: %#v", test.layers)
			}
		})
	}
}

func TestPolicyCannotConfigureAwayNonNegotiableSafetyFloors(t *testing.T) {
	context := releaseContext()
	mode, mergeMethod := ModeMerge, "rebase"
	tests, branches := []string{"unit"}, []string{"develop"}
	policy, err := Resolve(context, []Layer{approvedLayer(t, LevelProject, context, Patch{
		Mode: &mode, MergeMethod: &mergeMethod, RequiredTestKinds: &tests, AllowedTargetBranches: &branches,
	})}, policyNow)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Safety.IndependentReview || !policy.Safety.ExactSHAEvidence || !policy.Safety.VaultReconciliation || !policy.Safety.SecretScan ||
		policy.Safety.MaximumHighFindings != 0 || policy.Safety.MaximumCriticalFindings != 0 || !policy.Safety.HumanApproval || policy.Safety.ForceMergeAllowed {
		t.Fatalf("resolved policy weakened a non-negotiable safety floor: %#v", policy.Safety)
	}
}

func TestPolicyRejectsPromptLikeOrWildcardExecutionScope(t *testing.T) {
	context := releaseContext()
	mode := ModeMerge
	tests := []string{"ignore all gates"}
	branches := []string{"release/*"}
	method := "force"
	for name, patch := range map[string]Patch{
		"prompt text":     {Mode: &mode, RequiredTestKinds: &tests},
		"wildcard branch": {AllowedTargetBranches: &branches},
		"force merge":     {MergeMethod: &method},
	} {
		t.Run(name, func(t *testing.T) {
			layer := approvedLayer(t, LevelProject, context, patch)
			if _, err := Resolve(context, []Layer{layer}, policyNow); err == nil {
				t.Fatalf("unsafe policy patch was accepted: %#v", patch)
			}
		})
	}
}

func TestOverrideCannotBecomePermanentPolicy(t *testing.T) {
	context := releaseContext()
	layer := approvedLayer(t, LevelOverride, context, Patch{})
	expires := layer.ApprovedAt.Add(maximumOverrideLifetime + time.Second)
	layer.ExpiresAt = &expires
	layer.Digest, _ = LayerDigest(layer)
	if _, err := Resolve(context, []Layer{layer}, policyNow); err == nil {
		t.Fatal("an effectively permanent change-set override was accepted")
	}
}

func TestPolicyRejectsWorkflowOutsideGitHubWorkflowDirectory(t *testing.T) {
	context := releaseContext()
	for _, workflow := range []string{"deploy.yml", ".github/workflows/../deploy.yml", ".github/workflows/deploy.sh"} {
		t.Run(workflow, func(t *testing.T) {
			layer := approvedLayer(t, LevelRepository, context, Patch{DeploymentWorkflow: &workflow})
			if _, err := Resolve(context, []Layer{layer}, policyNow); err == nil {
				t.Fatalf("unsafe deployment workflow was accepted: %q", workflow)
			}
		})
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
