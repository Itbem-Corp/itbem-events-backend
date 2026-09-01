package automationagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func validCodeReviewInput() []byte {
	patch := "diff --git a/controllers/orders.go b/controllers/orders.go\nindex abc..def 100644\n--- a/controllers/orders.go\n+++ b/controllers/orders.go\n@@ -40,2 +40,11 @@\n-old40\n-old41\n+line40\n+line41\n+line42\n+line43\n+line44\n+line45\n+line46\n+line47\n+line48\n+line49\n+line50\n"
	digest := sha256.Sum256([]byte(patch))
	value, _ := json.Marshal(map[string]any{
		"repository_ref": "github://itbem/example", "base_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "head_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"changed_files": []string{"controllers/orders.go"}, "changed_line_ranges": []map[string]any{{"file": "controllers/orders.go", "side": "base", "start": 40, "end": 41}, {"file": "controllers/orders.go", "side": "head", "start": 40, "end": 50}},
		"patch": patch, "patch_sha256": hex.EncodeToString(digest[:]),
	})
	return value
}

func TestParseCodeReviewInputPinsOneImmutableChangeSet(t *testing.T) {
	review, err := ParseCodeReviewInput(validCodeReviewInput())
	if err != nil {
		t.Fatal(err)
	}
	if review.HeadSHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || len(review.ChangedFiles) != 1 {
		t.Fatalf("review boundary was not retained: %#v", review)
	}
	if review.SanitizedPatch() == "" {
		t.Fatal("review must retain a model-safe frozen patch")
	}
	if len(review.ChangedLines) != 2 || review.ChangedLines[0].Side != "base" || review.ChangedLines[1].Start != 40 || review.ChangedLines[1].End != 50 {
		t.Fatalf("changed lines must be derived from the patch: %#v", review.ChangedLines)
	}
	var invalid map[string]any
	if err := json.Unmarshal(validCodeReviewInput(), &invalid); err != nil {
		t.Fatal(err)
	}
	invalid["patch_sha256"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	invalidRaw, _ := json.Marshal(invalid)
	if _, err := ParseCodeReviewInput(invalidRaw); err == nil {
		t.Fatal("a review must reject a patch whose digest was altered")
	}
	if _, err := ParseCodeReviewInput([]byte(`{"repository_ref":"github://itbem/example","base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","changed_files":["../.env"],"changed_line_ranges":[{"file":"../.env","start":1,"end":1}],"patch":"diff --git a/.env b/.env","patch_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)); err == nil {
		t.Fatal("a review must reject a mutable or unsafe boundary")
	}
}

func TestNewCodeReviewInputDerivesManifestFromFrozenPatch(t *testing.T) {
	var source map[string]any
	if err := json.Unmarshal(validCodeReviewInput(), &source); err != nil {
		t.Fatal(err)
	}
	input, err := NewCodeReviewInput(source["repository_ref"].(string), source["base_sha"].(string), source["head_sha"].(string), source["patch"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(input.ChangedFiles) != 1 || input.ChangedFiles[0] != "controllers/orders.go" || len(input.ChangedLines) != 2 {
		t.Fatalf("manifest must derive exactly from patch: %#v", input)
	}
	if input.PatchSHA256 == "" {
		t.Fatal("derived input must pin the patch digest")
	}
}

func TestCodeReviewAllowsEnvironmentTemplatesButRejectsMutableEnvironmentFiles(t *testing.T) {
	for _, file := range []string{".env.ai.local.example", "config/.env.sample", "deploy/service.env.template"} {
		patch := "diff --git a/" + file + " b/" + file + "\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/" + file + "\n@@ -0,0 +1 @@\n+TOKEN=REPLACE_ME\n"
		if _, err := NewCodeReviewInput("github://itbem/example", strings.Repeat("a", 40), strings.Repeat("b", 40), patch); err != nil {
			t.Fatalf("safe environment template %q must remain reviewable: %v", file, err)
		}
	}
	for _, file := range []string{".env", "config/.env.production", "deploy/service.env"} {
		patch := "diff --git a/" + file + " b/" + file + "\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/" + file + "\n@@ -0,0 +1 @@\n+TOKEN=private\n"
		if _, err := NewCodeReviewInput("github://itbem/example", strings.Repeat("a", 40), strings.Repeat("b", 40), patch); err == nil {
			t.Fatalf("mutable environment file %q entered the review boundary", file)
		}
	}
}

func TestBindCodeReviewRemoteTargetSealsExactGitHubSubject(t *testing.T) {
	input, err := ParseCodeReviewInput(validCodeReviewInput())
	if err != nil {
		t.Fatal(err)
	}
	input, err = BindCodeReviewRemoteTarget(input, 42, 67890)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := CodeReviewPublicationSubjectSHA256(input)
	if err != nil || len(digest) != 64 {
		t.Fatalf("remote review subject was not sealed: %q / %v", digest, err)
	}
	input.HeadSHA = strings.Repeat("c", 40)
	changed, err := CodeReviewPublicationSubjectSHA256(input)
	if err != nil || changed == digest {
		t.Fatal("remote review subject did not change with the exact head SHA")
	}
	if _, err := BindCodeReviewRemoteTarget(input, 0, 67890); err == nil {
		t.Fatal("invalid pull request target was accepted")
	}
}

func TestNewCodeReviewInputRetainsDeletedFilesAsBaseSideReviewScope(t *testing.T) {
	patch := "diff --git a/controllers/legacy_auth.go b/controllers/legacy_auth.go\ndeleted file mode 100644\nindex abc..0000000\n--- a/controllers/legacy_auth.go\n+++ /dev/null\n@@ -7,2 +0,0 @@\n-requireOwner(user, order)\n-return forbidden\n"
	input, err := NewCodeReviewInput("github://itbem/example", strings.Repeat("a", 40), strings.Repeat("b", 40), patch)
	if err != nil {
		t.Fatalf("a complete file deletion must stay reviewable: %v", err)
	}
	if len(input.ChangedFiles) != 1 || input.ChangedFiles[0] != "controllers/legacy_auth.go" {
		t.Fatalf("deleted file was lost from manifest: %#v", input.ChangedFiles)
	}
	if len(input.ChangedLines) != 1 || input.ChangedLines[0] != (CodeReviewChangedLineRange{File: "controllers/legacy_auth.go", Side: "base", Start: 7, End: 8}) {
		t.Fatalf("deleted lines must be retained as base-side evidence: %#v", input.ChangedLines)
	}
	result, err := ParseCodeReview(`{"summary":"Ownership protection was removed.","verdict":"request_changes","review_scope":["authorization"],"findings":[{"id":"deleted-owner-check","severity":"high","category":"security","title":"Owner guard was deleted","file":"controllers/legacy_auth.go","side":"base","line_start":7,"line_end":8,"evidence":"The only owner guard was removed.","evidence_quote":"requireOwner(user, order)","recommendation":"Restore equivalent authorization before serving the mutation.","confidence":0.95}],"test_plan":["Verify a non-owner is rejected."],"coverage_gaps":[]}`)
	if err != nil || ValidateCodeReviewBoundary(result, input) != nil {
		t.Fatalf("valid deletion finding must remain bound to the frozen diff: parse=%v boundary=%v", err, ValidateCodeReviewBoundary(result, input))
	}
}

func TestCodeReviewCanProveRiskyDeletionFromBaseSide(t *testing.T) {
	review, err := ParseCodeReview(`{"summary":"A security guard was removed.","verdict":"request_changes","review_scope":["authorization"],"findings":[{"id":"removed-auth","severity":"high","category":"security","title":"Authorization was removed","file":"controllers/orders.go","side":"base","line_start":40,"line_end":41,"evidence":"The deleted guard allowed only authorized callers.","evidence_quote":"old40","recommendation":"Restore an equivalent authorization check.","confidence":0.95}],"test_plan":["Verify non-owner rejection."],"coverage_gaps":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := ParseCodeReviewInput(validCodeReviewInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCodeReviewBoundary(review, boundary); err != nil {
		t.Fatalf("removed code must be reviewable with base-side evidence: %v", err)
	}
}

func TestNormalizeCodeReviewCoveragePreventsUnqualifiedApprovalWithoutTestEvidence(t *testing.T) {
	boundary, err := ParseCodeReviewInput(validCodeReviewInput())
	if err != nil {
		t.Fatal(err)
	}
	review, err := ParseCodeReview(`{"summary":"The patch is internally consistent.","verdict":"approve","review_scope":["handler"],"findings":[],"test_plan":["Run the handler test suite."],"coverage_gaps":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	NormalizeCodeReviewCoverage(review, boundary)
	if review["verdict"] != "comment" {
		t.Fatalf("source change without a test diff must not stay approved: %#v", review)
	}
	gaps := review["coverage_gaps"].([]any)
	if len(gaps) != 1 || !strings.Contains(gaps[0].(string), "No test change") {
		t.Fatalf("coverage uncertainty must be explicit: %#v", gaps)
	}
	if err := ValidateCodeReviewBoundary(review, boundary); err != nil {
		t.Fatalf("normalized advisory review must retain the same immutable boundary: %v", err)
	}
}

func TestNormalizeCodeReviewCoverageDoesNotPenalizeIncludedTestEvidence(t *testing.T) {
	for _, testFile := range []string{"src/orders.test.ts", "tests/test_orders.py", "test/orders_spec.rb", "OrdersTests.cs", "OrderTest.java", "tests/OrdersTest.php"} {
		boundary := CodeReviewInput{ChangedFiles: []string{"src/orders.ts", testFile}}
		if reviewNeedsCoverageGap(boundary) {
			t.Fatalf("changed test %q must satisfy the diff-only coverage signal", testFile)
		}
	}
	boundary := CodeReviewInput{ChangedFiles: []string{"src/orders.ts", "src/orders.test.ts"}}
	review := map[string]any{"verdict": "approve", "coverage_gaps": []any{}}
	NormalizeCodeReviewCoverage(review, boundary)
	if review["verdict"] != "approve" || len(review["coverage_gaps"].([]any)) != 0 {
		t.Fatalf("test evidence must not be rewritten: %#v", review)
	}
}

func TestCodeReviewRejectsChangedFilesThatDoNotMatchPatch(t *testing.T) {
	var input map[string]any
	if err := json.Unmarshal(validCodeReviewInput(), &input); err != nil {
		t.Fatal(err)
	}
	input["changed_files"] = []string{"controllers/orders.go", "extra.go"}
	raw, _ := json.Marshal(input)
	if _, err := ParseCodeReviewInput(raw); err == nil {
		t.Fatal("declared changed files must match the frozen patch")
	}
}

func TestCodeReviewRejectsManifestRangesThatDoNotMatchPatch(t *testing.T) {
	var input map[string]any
	if err := json.Unmarshal(validCodeReviewInput(), &input); err != nil {
		t.Fatal(err)
	}
	input["changed_line_ranges"] = []map[string]any{{"file": "controllers/orders.go", "start": 1, "end": 99}}
	raw, _ := json.Marshal(input)
	if _, err := ParseCodeReviewInput(raw); err == nil {
		t.Fatal("declared changed ranges must be checked against the patch")
	}
}

func TestCodeReviewRejectsTruncatedOrInconsistentUnifiedHunks(t *testing.T) {
	truncated := "diff --git a/controllers/orders.go b/controllers/orders.go\n--- a/controllers/orders.go\n+++ b/controllers/orders.go\n@@ -10,2 +10,2 @@\n-old\n+new\n"
	if _, err := patchReviewLineRanges(truncated); err == nil {
		t.Fatal("truncated hunk must be rejected")
	}
	overfull := "diff --git a/controllers/orders.go b/controllers/orders.go\n--- a/controllers/orders.go\n+++ b/controllers/orders.go\n@@ -10 +10 @@\n+new\n+unexpected\n"
	if _, err := patchReviewLineRanges(overfull); err == nil {
		t.Fatal("hunk that exceeds declared counts must be rejected")
	}
}

func validCodeReview() string {
	return `{"summary":"The change is safe to merge after one correction.","verdict":"request_changes","review_scope":["API handler","authorization path"],"findings":[{"id":"auth-missing-owner-check","severity":"high","category":"security","title":"Mutation lacks an ownership check","file":"controllers/orders.go","line_start":42,"line_end":45,"evidence":"The handler updates the record after parsing an ID without checking the caller.","evidence_quote":"line42","recommendation":"Require ownership before executing the update.","confidence":0.96}],"test_plan":["Add a non-owner rejection test."],"coverage_gaps":["No authorization regression test exists."]}`
}

func TestParseCodeReviewProducesStructuredActionableFindings(t *testing.T) {
	review, err := ParseCodeReview(validCodeReview())
	if err != nil {
		t.Fatal(err)
	}
	if review["verdict"] != "request_changes" {
		t.Fatalf("unexpected verdict: %#v", review["verdict"])
	}
	findings := review["findings"].([]any)
	finding := findings[0].(map[string]any)
	if finding["line_start"] != 42 || finding["severity"] != "high" {
		t.Fatalf("finding lost its precise evidence: %#v", finding)
	}
}

func TestCodeReviewCannotEscapeFrozenDiffOrDowngradeHighImpactFinding(t *testing.T) {
	review, err := ParseCodeReview(validCodeReview())
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := ParseCodeReviewInput(validCodeReviewInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCodeReviewBoundary(review, boundary); err != nil {
		t.Fatal(err)
	}
	badFile := review["findings"].([]any)[0].(map[string]any)
	badFile["file"] = "unrelated/hidden.go"
	if err := ValidateCodeReviewBoundary(review, boundary); err == nil {
		t.Fatal("findings must not reference files outside the frozen diff")
	}
	badFile["file"] = "controllers/orders.go"
	badFile["line_start"] = 70
	badFile["line_end"] = 71
	if err := ValidateCodeReviewBoundary(review, boundary); err == nil {
		t.Fatal("findings must point to changed lines, not merely changed files")
	}
	badFile["line_start"] = 42
	badFile["line_end"] = 45
	badFile["evidence_quote"] = "not in patch"
	if err := ValidateCodeReviewBoundary(review, boundary); err == nil {
		t.Fatal("finding evidence must quote an added line from the frozen patch")
	}
	badFile["evidence_quote"] = "line42"
	badFile["line_start"] = 45
	badFile["line_end"] = 45
	if err := ValidateCodeReviewBoundary(review, boundary); err == nil {
		t.Fatal("quote must belong to the exact finding line range")
	}
	badFile["line_start"] = 42
	badFile["line_end"] = 45
	review["verdict"] = "comment"
	if err := ValidateCodeReviewBoundary(review, boundary); err == nil {
		t.Fatal("high impact findings must request changes")
	}
}

func TestParseCodeReviewRejectsUnsafeOrAmbiguousApproval(t *testing.T) {
	withFindingAndApproval := `{"summary":"Looks good.","verdict":"approve","review_scope":[],"findings":[{"id":"x","severity":"low","category":"maintainability","title":"x","file":"x.go","line_start":1,"line_end":1,"evidence":"x","evidence_quote":"x","recommendation":"x","confidence":1}],"test_plan":[],"coverage_gaps":[]}`
	if _, err := ParseCodeReview(withFindingAndApproval); err == nil {
		t.Fatal("an approval with findings must fail closed")
	}
	traversal := `{"summary":"Needs a fix.","verdict":"request_changes","review_scope":[],"findings":[{"id":"x","severity":"low","category":"maintainability","title":"x","file":"../secret","line_start":1,"line_end":1,"evidence":"x","evidence_quote":"x","recommendation":"x","confidence":1}],"test_plan":[],"coverage_gaps":[]}`
	if _, err := ParseCodeReview(traversal); err == nil {
		t.Fatal("review locations must not accept path traversal")
	}
}

func TestCodeReviewConsolidatesLocationsAndDoesNotBlockOnLowOnlyFindings(t *testing.T) {
	duplicate := `{"summary":"Two descriptions of the same concern.","verdict":"comment","review_scope":["handler"],"findings":[{"id":"one","severity":"low","category":"maintainability","title":"One","file":"controllers/orders.go","side":"head","line_start":42,"line_end":42,"evidence":"One","evidence_quote":"line42","recommendation":"One","confidence":0.8},{"id":"two","severity":"low","category":"maintainability","title":"Two","file":"controllers/orders.go","side":"head","line_start":42,"line_end":42,"evidence":"Two","evidence_quote":"line42","recommendation":"Two","confidence":0.8}],"test_plan":[],"coverage_gaps":[]}`
	if _, err := ParseCodeReview(duplicate); err == nil {
		t.Fatal("review must consolidate duplicate source locations")
	}
	lowOnlyBlock := `{"summary":"Minor styling note.","verdict":"request_changes","review_scope":["handler"],"findings":[{"id":"low","severity":"low","category":"maintainability","title":"Minor","file":"controllers/orders.go","side":"head","line_start":42,"line_end":42,"evidence":"Minor","evidence_quote":"line42","recommendation":"Tidy it","confidence":0.8}],"test_plan":[],"coverage_gaps":[]}`
	if _, err := ParseCodeReview(lowOnlyBlock); err == nil {
		t.Fatal("low-only findings must not block a pull request")
	}
}

func TestCodeReviewDoesNotHideBlockingFindingsOrSpeculateWhenBlocked(t *testing.T) {
	mediumComment := `{"summary":"A correctness issue needs a fix.","verdict":"comment","review_scope":["handler"],"findings":[{"id":"medium","severity":"medium","category":"correctness","title":"Missing fallback","file":"controllers/orders.go","side":"head","line_start":42,"line_end":42,"evidence":"A required fallback is absent.","evidence_quote":"line42","recommendation":"Handle the fallback explicitly.","confidence":0.8}],"test_plan":[],"coverage_gaps":[]}`
	if _, err := ParseCodeReview(mediumComment); err == nil {
		t.Fatal("medium findings must not be hidden behind a non-blocking verdict")
	}
	blockedWithFinding := `{"summary":"The diff is incomplete.","verdict":"blocked","review_scope":["handler"],"findings":[{"id":"guess","severity":"low","category":"maintainability","title":"Speculative concern","file":"controllers/orders.go","side":"head","line_start":42,"line_end":42,"evidence":"Not enough context.","evidence_quote":"line42","recommendation":"Provide more context.","confidence":0.2}],"test_plan":[],"coverage_gaps":["Missing dependency context."]}`
	if _, err := ParseCodeReview(blockedWithFinding); err == nil {
		t.Fatal("blocked reviews must surface evidence gaps without speculative findings")
	}
	blockedWithoutGap := `{"summary":"The diff is incomplete.","verdict":"blocked","review_scope":["handler"],"findings":[],"test_plan":[],"coverage_gaps":[]}`
	if _, err := ParseCodeReview(blockedWithoutGap); err == nil {
		t.Fatal("blocked reviews must state the evidence needed to unblock them")
	}
	approvalWithGap := `{"summary":"Looks safe.","verdict":"approve","review_scope":["handler"],"findings":[],"test_plan":[],"coverage_gaps":["No regression test is present."]}`
	if _, err := ParseCodeReview(approvalWithGap); err == nil {
		t.Fatal("approvals must not hide known coverage gaps")
	}
	approvalWithoutValidation := `{"summary":"Looks safe.","verdict":"approve","review_scope":["handler"],"findings":[],"test_plan":[],"coverage_gaps":[]}`
	if _, err := ParseCodeReview(approvalWithoutValidation); err == nil {
		t.Fatal("a conclusive review must leave a concrete validation step")
	}
	blockedWithGap := `{"summary":"The diff lacks dependency context.","verdict":"blocked","review_scope":["handler"],"findings":[],"test_plan":[],"coverage_gaps":["Provide the called interface contract before review."]}`
	if _, err := ParseCodeReview(blockedWithGap); err != nil {
		t.Fatalf("blocked review with an actionable evidence gap should remain valid: %v", err)
	}
}

func TestCodeReviewSeverityRequiresCommensurateConfidenceAndDistinctRemedy(t *testing.T) {
	lowConfidenceHigh := `{"summary":"A risky issue was guessed.","verdict":"request_changes","review_scope":["handler"],"findings":[{"id":"guess","severity":"high","category":"security","title":"Possible authorization flaw","file":"controllers/orders.go","side":"head","line_start":42,"line_end":42,"evidence":"The patch might be unsafe.","evidence_quote":"line42","recommendation":"Investigate the authorization policy.","confidence":0.5}],"test_plan":[],"coverage_gaps":[]}`
	if _, err := ParseCodeReview(lowConfidenceHigh); err == nil {
		t.Fatal("high severity must not be assigned to low-confidence speculation")
	}
	repeatedText := `{"summary":"A correction is needed.","verdict":"request_changes","review_scope":["handler"],"findings":[{"id":"repeat","severity":"medium","category":"correctness","title":"Fallback missing","file":"controllers/orders.go","side":"head","line_start":42,"line_end":42,"evidence":"Add a fallback branch.","evidence_quote":"line42","recommendation":"Add a fallback branch.","confidence":0.8}],"test_plan":[],"coverage_gaps":[]}`
	if _, err := ParseCodeReview(repeatedText); err == nil {
		t.Fatal("review finding must distinguish observed evidence from the proposed remedy")
	}
}
