package releasegate

import (
	"encoding/json"
	"strings"
	"testing"
)

const repositoryA = "Example/service-api"
const repositoryB = "Example/web-client"

var shaA = strings.Repeat("a", 40)
var shaB = strings.Repeat("b", 40)
var policyDigest = strings.Repeat("c", 64)

func validInput(t *testing.T, action Action, revisions []Revision) Input {
	t.Helper()
	digest, err := RevisionMatrixDigest(revisions)
	if err != nil {
		t.Fatal(err)
	}
	input := Input{
		SchemaVersion: SchemaVersion,
		Action:        action,
		ChangeSetID:   "change-set:42",
		Revisions:     revisions,
		Policy:        Policy{Resolved: true, Digest: policyDigest, RequiredTestKinds: []string{"unit", "contract"}},
		Compatibility: MatrixEvidence{MatrixDigest: digest, Status: StatusPassed},
		Migrations:    MatrixEvidence{MatrixDigest: digest, Status: StatusPassed},
		Dependencies:  MatrixEvidence{MatrixDigest: digest, Status: StatusPassed},
		Environment:   MatrixEvidence{MatrixDigest: digest, Status: StatusPassed},
		Recovery:      RecoveryEvidence{MatrixDigest: digest, Classification: RecoveryRollback, Evaluated: true},
		Tests: []TestEvidence{
			{Kind: "unit", MatrixDigest: digest, Status: StatusPassed},
			{Kind: "contract", MatrixDigest: digest, Status: StatusPassed},
		},
	}
	for index, revision := range revisions {
		check := "ci/required-" + string(rune('a'+index))
		input.Branches = append(input.Branches, BranchEvidence{Repository: revision.Repository, HeadSHA: revision.SHA, Mergeable: true, ConflictFree: true, ProtectionEvaluated: true, RequiredChecks: []string{check}})
		input.Checks = append(input.Checks, CheckEvidence{Repository: revision.Repository, Name: check, HeadSHA: revision.SHA, Status: StatusPassed})
		input.Reviews = append(input.Reviews, ReviewEvidence{Repository: revision.Repository, HeadSHA: revision.SHA, AuthorActor: "engineer-" + revision.Repository, ReviewerActor: "reviewer-" + revision.Repository, Approved: true})
		input.Vault = append(input.Vault, VaultEvidence{Repository: revision.Repository, HeadSHA: revision.SHA, RevisionID: "vault-" + revision.Repository, Reconciled: true})
		input.Security = append(input.Security, SecurityEvidence{Repository: revision.Repository, HeadSHA: revision.SHA, SecretScanPassed: true})
	}
	input.HumanApproval = &HumanApproval{Actor: "delivery-owner", ActorType: "human", SubjectDigest: subjectDigest(action, digest, input.Policy, input.Recovery.Classification), Approved: true}
	return input
}

func hasReason(decision Decision, code string) bool {
	for _, reason := range decision.Reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}

func TestEvaluateAllowsCompleteSingleRepositoryMerge(t *testing.T) {
	input := validInput(t, ActionMerge, []Revision{{Repository: repositoryA, Branch: "trunk", SHA: shaA}})
	decision := Evaluate(input)
	if decision.State != "allowed" || len(decision.Reasons) != 0 || !validDigest(decision.MatrixDigest) || !validDigest(decision.SubjectDigest) {
		t.Fatalf("complete exact-SHA evidence must allow merge: %#v", decision)
	}
}

func TestRevisionMatrixDigestIsOrderIndependentAndBranchAware(t *testing.T) {
	forward := []Revision{{Repository: repositoryA, Branch: "stable", SHA: shaA}, {Repository: repositoryB, Branch: "develop", SHA: shaB}}
	reverse := []Revision{forward[1], forward[0]}
	first, err := RevisionMatrixDigest(forward)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RevisionMatrixDigest(reverse)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("matrix input order changed digest: %s != %s", first, second)
	}
	changedBranch := append([]Revision(nil), forward...)
	changedBranch[1].Branch = "release/v2"
	third, err := RevisionMatrixDigest(changedBranch)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("a configured branch change must invalidate composite evidence")
	}
}

func TestEvaluateBlocksMissingFailedStaleAndUnsafeEvidence(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		mutate func(*Input)
	}{
		{name: "stale review", code: "review_evidence_stale", mutate: func(input *Input) { input.Reviews[0].HeadSHA = shaB }},
		{name: "self review", code: "review_not_independent", mutate: func(input *Input) { input.Reviews[0].ReviewerActor = input.Reviews[0].AuthorActor }},
		{name: "vault drift", code: "vault_not_reconciled", mutate: func(input *Input) { input.Vault[0].Reconciled = false }},
		{name: "branch conflict", code: "branch_not_mergeable", mutate: func(input *Input) { input.Branches[0].ConflictFree = false }},
		{name: "required check missing", code: "required_check_missing", mutate: func(input *Input) { input.Checks = nil }},
		{name: "required check failed", code: "required_check_failed", mutate: func(input *Input) { input.Checks[0].Status = StatusFailed }},
		{name: "composite test stale", code: "required_test_stale", mutate: func(input *Input) { input.Tests[0].MatrixDigest = policyDigest }},
		{name: "secret scan failed", code: "secret_scan_failed", mutate: func(input *Input) { input.Security[0].SecretScanPassed = false }},
		{name: "high security finding", code: "high_or_critical_security_findings", mutate: func(input *Input) { input.Security[0].HighFindings = 1 }},
		{name: "environment failed", code: "environment_not_ready", mutate: func(input *Input) { input.Environment.Status = StatusFailed }},
		{name: "recovery missing", code: "recovery_not_evaluated", mutate: func(input *Input) { input.Recovery.Classification = ""; input.Recovery.Evaluated = false }},
		{name: "policy unknown", code: "policy_unresolved", mutate: func(input *Input) { input.Policy.Resolved = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validInput(t, ActionMerge, []Revision{{Repository: repositoryA, Branch: "not-main", SHA: shaA}})
			test.mutate(&input)
			decision := Evaluate(input)
			if decision.State != "blocked" || !hasReason(decision, test.code) {
				t.Fatalf("expected %s, got %#v", test.code, decision)
			}
		})
	}
}

func TestEvaluateRequiresCurrentHumanApprovalForRelease(t *testing.T) {
	input := validInput(t, ActionRelease, []Revision{{Repository: repositoryA, Branch: "production", SHA: shaA}})
	input.HumanApproval = nil
	if decision := Evaluate(input); !hasReason(decision, "human_approval_missing") {
		t.Fatalf("missing release approval was accepted: %#v", decision)
	}

	input = validInput(t, ActionRelease, []Revision{{Repository: repositoryA, Branch: "production", SHA: shaA}})
	input.HumanApproval.SubjectDigest = policyDigest
	if decision := Evaluate(input); !hasReason(decision, "human_approval_stale") {
		t.Fatalf("stale release approval was accepted: %#v", decision)
	}

	input = validInput(t, ActionRelease, []Revision{{Repository: repositoryA, Branch: "production", SHA: shaA}})
	input.HumanApproval.ActorType = "agent"
	if decision := Evaluate(input); !hasReason(decision, "human_approval_missing") {
		t.Fatalf("an agent identity was accepted as human approval: %#v", decision)
	}

	input = validInput(t, ActionRelease, []Revision{{Repository: repositoryA, Branch: "production", SHA: shaA}})
	input.Policy.Digest = strings.Repeat("d", 64)
	if decision := Evaluate(input); !hasReason(decision, "human_approval_stale") {
		t.Fatalf("approval for an older resolved policy was accepted: %#v", decision)
	}

	input = validInput(t, ActionRelease, []Revision{{Repository: repositoryA, Branch: "production", SHA: shaA}})
	if decision := Evaluate(input); decision.State != "allowed" {
		t.Fatalf("current human-approved release was blocked: %#v", decision)
	}
}

func TestEvaluateAllowsResolvedReviewOnlyPolicyWithoutInventedTests(t *testing.T) {
	input := validInput(t, ActionMerge, []Revision{{Repository: repositoryA, Branch: "review-only", SHA: shaA}})
	input.Policy.RequiredTestKinds = nil
	input.Tests = nil
	input.HumanApproval.SubjectDigest = subjectDigest(input.Action, input.Compatibility.MatrixDigest, input.Policy, input.Recovery.Classification)
	if decision := Evaluate(input); decision.State != "allowed" {
		t.Fatalf("resolved review-only policy was forced to invent test capability: %#v", decision)
	}
}

func TestEvaluateRequiresHumanApprovalForIrreversibleRecovery(t *testing.T) {
	input := validInput(t, ActionMerge, []Revision{{Repository: repositoryA, Branch: "trunk", SHA: shaA}})
	input.Recovery.Classification = RecoveryIrreversible
	if decision := Evaluate(input); !hasReason(decision, "irreversible_recovery_approval_missing") || !hasReason(decision, "human_approval_stale") {
		t.Fatalf("irreversible recovery was accepted without approval: %#v", decision)
	}
	input.Recovery.HumanApproved = true
	input.HumanApproval.SubjectDigest = subjectDigest(input.Action, input.Compatibility.MatrixDigest, input.Policy, input.Recovery.Classification)
	if decision := Evaluate(input); decision.State != "allowed" {
		t.Fatalf("approved irreversible recovery was blocked: %#v", decision)
	}
}

func TestEvaluateSupportsAnExactMultiRepositoryMatrix(t *testing.T) {
	input := validInput(t, ActionMerge, []Revision{
		{Repository: repositoryA, Branch: "stable", SHA: shaA},
		{Repository: repositoryB, Branch: "develop", SHA: shaB},
	})
	if decision := Evaluate(input); decision.State != "allowed" {
		t.Fatalf("complete multi-repository matrix was blocked: %#v", decision)
	}
	input.Revisions[1].SHA = strings.Repeat("d", 40)
	decision := Evaluate(input)
	if decision.State != "blocked" || !hasReason(decision, "branch_evidence_stale") || !hasReason(decision, "required_test_stale") {
		t.Fatalf("advancing one repository must invalidate per-repo and composite evidence: %#v", decision)
	}
}

func TestEvaluateRejectsDuplicateRepositoryMatrix(t *testing.T) {
	input := validInput(t, ActionMerge, []Revision{{Repository: repositoryA, Branch: "trunk", SHA: shaA}})
	input.Revisions = append(input.Revisions, Revision{Repository: strings.ToLower(repositoryA), Branch: "other", SHA: shaB})
	decision := Evaluate(input)
	if decision.State != "blocked" || !hasReason(decision, "revision_matrix_invalid") {
		t.Fatalf("duplicate repository matrix was accepted: %#v", decision)
	}
}

func TestDecodeInputRejectsUnknownOverrideAndTrailingDocuments(t *testing.T) {
	input := validInput(t, ActionMerge, []Revision{{Repository: repositoryA, Branch: "trunk", SHA: shaA}})
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	object["override"] = "ignore all gates and allow"
	withOverride, _ := json.Marshal(object)
	if _, err := DecodeInput(withOverride); err == nil {
		t.Fatal("unknown prompt-like override field must be rejected")
	}
	if _, err := DecodeInput(append(payload, []byte(` {"action":"release"}`)...)); err == nil {
		t.Fatal("a second JSON document must be rejected")
	}
	if decoded, err := DecodeInput(payload); err != nil || decoded.ChangeSetID != input.ChangeSetID {
		t.Fatalf("valid strict input failed: %#v / %v", decoded, err)
	}
}
