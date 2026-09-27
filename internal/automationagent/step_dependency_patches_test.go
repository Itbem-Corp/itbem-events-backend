package automationagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"events-stocks/models"
	"github.com/gofrs/uuid"
)

func TestApplyDependencyPatchesParentAndDescendantCumulativeDiffs(t *testing.T) {
	root, baseSHA := dependencyPatchTestRepository(t, map[string]string{"shared.txt": "root\n"})
	parentPatch := dependencyPatchForFiles(t, root, baseSHA, map[string]string{"shared.txt": "root\nparent\n"})
	childPatch := dependencyPatchForFiles(t, root, baseSHA, map[string]string{"shared.txt": "root\nparent\ndescendant\n"})
	firstID, secondID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
	if secondID < firstID {
		firstID, secondID = secondID, firstID
	}
	patches := []dependencyPatchPayload{
		{Reference: dependencyPatchReference(t, firstID, baseSHA, parentPatch), Body: parentPatch, Depth: 1},
		{Reference: dependencyPatchReference(t, secondID, baseSHA, childPatch), Body: childPatch, Depth: 2},
	}
	defer wipeDependencyPatchPayloads(patches)
	planID := uuid.Must(uuid.NewV4()).String()
	planSteps := []PlanStepDTO{
		{ID: firstID, PlanID: planID, PlanVersion: 1, StepKey: "parent"},
		{ID: secondID, PlanID: planID, PlanVersion: 1, StepKey: "descendant", DependsOn: []string{"parent"}},
	}
	patches = omitCoveredAncestorPatchArtifacts(patches, planSteps)
	if len(patches) != 1 || patches[0].Reference.DependencyStepID != secondID {
		t.Fatalf("ancestor patch was not omitted in favor of the cumulative descendant: %#v", patches)
	}
	lookup := dependencyPatchWorkspaceLookupForTest(t, root)
	taskID := uuid.Must(uuid.NewV4()).String()
	if err := applyDependencyPatchPayloads(context.Background(), taskID, patches, lookup); err != nil {
		t.Fatalf("parent plus cumulative descendant patch failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, ".itbem-agent-worktrees", taskID, "shared.txt"))
	if err != nil || string(got) != "root\nparent\ndescendant\n" {
		t.Fatalf("cumulative direct-dependency state is not the exact union: %q, %v", got, err)
	}
}

func TestApplyDependencyPatchesFromSiblingBranchesShareTheirCommonBase(t *testing.T) {
	root, baseSHA := dependencyPatchTestRepository(t, map[string]string{"base.txt": "common\n"})
	left := dependencyPatchForFiles(t, root, baseSHA, map[string]string{"base.txt": "common\nancestor\n", "left.txt": "left\n"})
	right := dependencyPatchForFiles(t, root, baseSHA, map[string]string{"base.txt": "common\nancestor\n", "right.txt": "right\n"})
	ancestorID, leftID, rightID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
	patches := []dependencyPatchPayload{
		{Reference: dependencyPatchReference(t, leftID, baseSHA, left), Body: left},
		{Reference: dependencyPatchReference(t, rightID, baseSHA, right), Body: right},
	}
	defer wipeDependencyPatchPayloads(patches)
	planID := uuid.Must(uuid.NewV4()).String()
	planSteps := []PlanStepDTO{
		{ID: ancestorID, PlanID: planID, PlanVersion: 1, StepKey: "ancestor"},
		{ID: leftID, PlanID: planID, PlanVersion: 1, StepKey: "left", DependsOn: []string{"ancestor"}},
		{ID: rightID, PlanID: planID, PlanVersion: 1, StepKey: "right", DependsOn: []string{"ancestor"}},
	}
	patches = omitCoveredAncestorPatchArtifacts(patches, planSteps)
	if len(patches) != 2 {
		t.Fatalf("sibling dependency artifacts were incorrectly deduplicated: %#v", patches)
	}
	lookup := dependencyPatchWorkspaceLookupForTest(t, root)
	taskID := uuid.Must(uuid.NewV4()).String()
	if err := applyDependencyPatchPayloads(context.Background(), taskID, patches, lookup); err != nil {
		t.Fatalf("independent branches based on one ancestor did not combine: %v", err)
	}
	worktree := filepath.Join(root, ".itbem-agent-worktrees", taskID)
	for path, want := range map[string]string{"base.txt": "common\nancestor\n", "left.txt": "left\n", "right.txt": "right\n"} {
		got, err := os.ReadFile(filepath.Join(worktree, path))
		if err != nil || string(got) != want {
			t.Fatalf("branch union file %s = %q, want %q (%v)", path, got, want, err)
		}
	}
}

func TestApplyDependencyPatchesIsIdempotentAfterWorkerReentry(t *testing.T) {
	root, baseSHA := dependencyPatchTestRepository(t, map[string]string{"value.txt": "before\n"})
	patch := dependencyPatchForFiles(t, root, baseSHA, map[string]string{"value.txt": "after\n"})
	patches := []dependencyPatchPayload{{Reference: dependencyPatchReference(t, uuid.Must(uuid.NewV4()).String(), baseSHA, patch), Body: patch}}
	defer wipeDependencyPatchPayloads(patches)
	taskID := uuid.Must(uuid.NewV4()).String()
	lookup := dependencyPatchWorkspaceLookupForTest(t, root)
	if err := applyDependencyPatchPayloads(context.Background(), taskID, patches, lookup); err != nil {
		t.Fatalf("initial dependency patch application failed: %v", err)
	}
	worktree := filepath.Join(root, ".itbem-agent-worktrees", taskID)
	before, err := os.ReadFile(filepath.Join(worktree, "value.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := applyDependencyPatchPayloads(context.Background(), taskID, patches, lookup); err != nil {
		t.Fatalf("worker re-entry did not accept the exact already-applied patch: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(worktree, "value.txt"))
	if err != nil || string(after) != string(before) || string(after) != "after\n" {
		t.Fatalf("re-entry changed the accepted dependency state: %q, %v", after, err)
	}
}

func TestApplyDependencyPatchesFailsClosedOnConflictAndBaseMismatch(t *testing.T) {
	t.Run("conflict", func(t *testing.T) {
		root, baseSHA := dependencyPatchTestRepository(t, map[string]string{"value.txt": "base\n"})
		left := dependencyPatchForFiles(t, root, baseSHA, map[string]string{"value.txt": "left\n"})
		right := dependencyPatchForFiles(t, root, baseSHA, map[string]string{"value.txt": "right\n"})
		patches := []dependencyPatchPayload{
			{Reference: dependencyPatchReference(t, uuid.Must(uuid.NewV4()).String(), baseSHA, left), Body: left},
			{Reference: dependencyPatchReference(t, uuid.Must(uuid.NewV4()).String(), baseSHA, right), Body: right},
		}
		defer wipeDependencyPatchPayloads(patches)
		if err := applyDependencyPatchPayloads(context.Background(), uuid.Must(uuid.NewV4()).String(), patches, dependencyPatchWorkspaceLookupForTest(t, root)); err == nil {
			t.Fatal("conflicting sibling patches were accepted")
		}
	})
	t.Run("base mismatch", func(t *testing.T) {
		root, baseSHA := dependencyPatchTestRepository(t, map[string]string{"value.txt": "base\n"})
		patch := dependencyPatchForFiles(t, root, baseSHA, map[string]string{"value.txt": "changed\n"})
		wrongBase := strings.Repeat("f", 40)
		payloads := []dependencyPatchPayload{{Reference: dependencyPatchReference(t, uuid.Must(uuid.NewV4()).String(), wrongBase, patch), Body: patch}}
		defer wipeDependencyPatchPayloads(payloads)
		taskID := uuid.Must(uuid.NewV4()).String()
		if err := applyDependencyPatchPayloads(context.Background(), taskID, payloads, dependencyPatchWorkspaceLookupForTest(t, root)); err == nil {
			t.Fatal("patch against a different base was accepted")
		}
		got, err := os.ReadFile(filepath.Join(root, ".itbem-agent-worktrees", taskID, "value.txt"))
		if err != nil || string(got) != "base\n" {
			t.Fatalf("base mismatch modified the isolated worktree: %q, %v", got, err)
		}
	})
}

func TestDependencyPatchPayloadRejectsTamperedHashAndSize(t *testing.T) {
	body := []byte("diff --git a/a.txt b/a.txt\n")
	reference := dependencyPatchReference(t, uuid.Must(uuid.NewV4()).String(), strings.Repeat("a", 40), body)
	if int64(len(body)) != reference.SizeBytes || !validDependencyPatchDigest(body, reference.SHA256) {
		t.Fatal("valid patch metadata failed verification")
	}
	if validDependencyPatchDigest(append(body, 'x'), reference.SHA256) {
		t.Fatal("tampered patch bytes matched the immutable digest")
	}
	reference.SizeBytes++
	if int64(len(body)) == reference.SizeBytes {
		t.Fatal("tampered patch size matched the actual body")
	}
}

func TestDependencyManifestAllowsOnlyDirectFrozenStepDependencies(t *testing.T) {
	planID := uuid.Must(uuid.NewV4()).String()
	parentID, indirectID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
	parent := PlanStepDTO{ID: parentID, PlanID: planID, PlanVersion: 1, StepKey: "parent", Status: "completed"}
	indirect := PlanStepDTO{ID: indirectID, PlanID: planID, PlanVersion: 1, StepKey: "ancestor", Status: "completed"}
	claimed := PlanStepDTO{ID: uuid.Must(uuid.NewV4()).String(), PlanID: planID, PlanVersion: 1, StepKey: "child", DependsOn: []string{"parent"}, Status: "running"}
	body := []byte("diff --git a/file.txt b/file.txt\n")
	reference := dependencyPatchReference(t, parentID, strings.Repeat("a", 40), body)
	digest, err := models.DeliveryPlanStepDependencyPatchManifestSHA256([]models.DeliveryPlanStepDependencyPatchReference{reference})
	if err != nil {
		t.Fatal(err)
	}
	manifest := PlanStepDependencyPatchManifest{ManifestSHA256: digest, PatchCount: 1, TotalSizeBytes: int64(len(body)), Patches: []models.DeliveryPlanStepDependencyPatchReference{reference}}
	if err := validateDirectDependencyPatchManifest(manifest, claimed, []PlanStepDTO{parent, indirect, claimed}); err != nil {
		t.Fatalf("direct dependency patch was rejected: %v", err)
	}
	manifest.Patches[0].DependencyStepID = indirectID
	manifest.ManifestSHA256, err = models.DeliveryPlanStepDependencyPatchManifestSHA256(manifest.Patches)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDirectDependencyPatchManifest(manifest, claimed, []PlanStepDTO{parent, indirect, claimed}); err == nil {
		t.Fatal("manifest patch from a transitive/non-direct dependency was accepted")
	}
}

func TestAncestorArtifactPruningIsScopedToTheSameRepository(t *testing.T) {
	planID := uuid.Must(uuid.NewV4()).String()
	parentID, childID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
	steps := []PlanStepDTO{
		{ID: parentID, PlanID: planID, PlanVersion: 1, StepKey: "parent"},
		{ID: childID, PlanID: planID, PlanVersion: 1, StepKey: "child", DependsOn: []string{"parent"}},
	}
	body := []byte("diff --git a/file.txt b/file.txt\n")
	parentBackend := dependencyPatchReference(t, parentID, strings.Repeat("a", 40), body)
	childBackend := dependencyPatchReference(t, childID, strings.Repeat("a", 40), body)
	parentFrontend := parentBackend
	parentFrontend.RepositoryRef = "workspace://frontend"
	patches := []dependencyPatchPayload{
		{Reference: parentBackend, Body: append([]byte(nil), body...)},
		{Reference: childBackend, Body: append([]byte(nil), body...)},
		{Reference: parentFrontend, Body: append([]byte(nil), body...)},
	}
	defer wipeDependencyPatchPayloads(patches)
	filtered := omitCoveredAncestorPatchArtifacts(patches, steps)
	if len(filtered) != 2 {
		t.Fatalf("expected one child artifact and the unaffected frontend artifact, got %#v", filtered)
	}
	seen := map[string]string{}
	for _, patch := range filtered {
		seen[patch.Reference.RepositoryRef] = patch.Reference.DependencyStepID
	}
	if seen["workspace://backend"] != childID || seen["workspace://frontend"] != parentID {
		t.Fatalf("ancestor pruning crossed repository boundaries: %#v", seen)
	}
}

func dependencyPatchTestRepository(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	root := testRepository(t)
	testGit(t, root, "config", "core.autocrlf", "false")
	for path, body := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "base")
	return root, strings.ToLower(testGit(t, root, "rev-parse", "HEAD"))
}

func dependencyPatchForFiles(t *testing.T, root, baseSHA string, files map[string]string) []byte {
	t.Helper()
	worktree := filepath.Join(t.TempDir(), "source-worktree")
	testGit(t, root, "worktree", "add", "--detach", worktree, baseSHA)
	defer func() {
		_, _ = runLocal(context.Background(), root, 30*time.Second, "", "git", "worktree", "remove", "--force", worktree)
	}()
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(worktree, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktree, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, worktree, "add", "--intent-to-add", "--all")
	_, patch, err := captureWorktreePatch(context.Background(), worktree, baseSHA, models.MaxDeliveryPlanStepPatchArtifactBytes)
	if err != nil {
		t.Fatal(err)
	}
	return patch
}

func dependencyPatchReference(t *testing.T, dependencyStepID, baseSHA string, body []byte) models.DeliveryPlanStepDependencyPatchReference {
	t.Helper()
	digest := sha256.Sum256(body)
	return models.DeliveryPlanStepDependencyPatchReference{
		DependencyStepID: dependencyStepID, RepositoryRef: "workspace://backend", BaseSHA: baseSHA,
		SHA256: hex.EncodeToString(digest[:]), SizeBytes: int64(len(body)),
	}
}

func dependencyPatchWorkspaceLookupForTest(t *testing.T, root string) func(string) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]WorkspaceConfig{"backend": {
		Path: root, Capabilities: []string{WorkspaceCapabilityReadRepository, WorkspaceCapabilityCreateWorktree, WorkspaceCapabilityApplyPatch},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return string(encoded)
		}
		return ""
	}
}
