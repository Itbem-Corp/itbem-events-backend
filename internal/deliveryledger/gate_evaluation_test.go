package deliveryledger

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/releasegate"

	"github.com/gofrs/uuid"
)

const testRepositoryA = "Example/service-api"
const testRepositoryB = "Example/web-client"

func validGateInput(t *testing.T) releasegate.Input {
	t.Helper()
	revisions := []releasegate.Revision{
		{Repository: testRepositoryA, Branch: "stable", SHA: strings.Repeat("a", 40)},
		{Repository: testRepositoryB, Branch: "release/v2", SHA: strings.Repeat("b", 40)},
	}
	matrixDigest, err := releasegate.RevisionMatrixDigest(revisions)
	if err != nil {
		t.Fatal(err)
	}
	input := releasegate.Input{
		SchemaVersion: releasegate.SchemaVersion,
		Action:        releasegate.ActionRelease,
		ChangeSetID:   "change-set:ledger-test",
		Revisions:     revisions,
		Policy: releasegate.Policy{
			Resolved:          true,
			Digest:            strings.Repeat("c", 64),
			RequiredTestKinds: []string{"unit", "contract"},
		},
		Branches: []releasegate.BranchEvidence{
			{Repository: testRepositoryA, HeadSHA: revisions[0].SHA, Mergeable: true, ConflictFree: true, ProtectionEvaluated: true, RequiredChecks: []string{"security", "ci"}},
			{Repository: testRepositoryB, HeadSHA: revisions[1].SHA, Mergeable: true, ConflictFree: true, ProtectionEvaluated: true, RequiredChecks: []string{"build"}},
		},
		Checks: []releasegate.CheckEvidence{
			{Repository: testRepositoryA, Name: "ci", HeadSHA: revisions[0].SHA, Status: releasegate.StatusPassed},
			{Repository: testRepositoryA, Name: "security", HeadSHA: revisions[0].SHA, Status: releasegate.StatusPassed},
			{Repository: testRepositoryB, Name: "build", HeadSHA: revisions[1].SHA, Status: releasegate.StatusPassed},
		},
		Reviews: []releasegate.ReviewEvidence{
			{Repository: testRepositoryA, HeadSHA: revisions[0].SHA, AuthorActor: "author-a", ReviewerActor: "reviewer-a", Approved: true},
			{Repository: testRepositoryB, HeadSHA: revisions[1].SHA, AuthorActor: "author-b", ReviewerActor: "reviewer-b", Approved: true},
		},
		Vault: []releasegate.VaultEvidence{
			{Repository: testRepositoryA, HeadSHA: revisions[0].SHA, RevisionID: "vault-a", Reconciled: true},
			{Repository: testRepositoryB, HeadSHA: revisions[1].SHA, RevisionID: "vault-b", Reconciled: true},
		},
		Tests: []releasegate.TestEvidence{
			{Kind: "unit", MatrixDigest: matrixDigest, Status: releasegate.StatusPassed},
			{Kind: "contract", MatrixDigest: matrixDigest, Status: releasegate.StatusPassed},
		},
		Security: []releasegate.SecurityEvidence{
			{Repository: testRepositoryA, HeadSHA: revisions[0].SHA, SecretScanPassed: true},
			{Repository: testRepositoryB, HeadSHA: revisions[1].SHA, SecretScanPassed: true},
		},
		Compatibility: releasegate.MatrixEvidence{MatrixDigest: matrixDigest, Status: releasegate.StatusPassed},
		Migrations:    releasegate.MatrixEvidence{MatrixDigest: matrixDigest, Status: releasegate.StatusPassed},
		Dependencies:  releasegate.MatrixEvidence{MatrixDigest: matrixDigest, Status: releasegate.StatusPassed},
		Environment:   releasegate.MatrixEvidence{MatrixDigest: matrixDigest, Status: releasegate.StatusPassed},
		Recovery:      releasegate.RecoveryEvidence{MatrixDigest: matrixDigest, Classification: releasegate.RecoveryRollback, Evaluated: true},
	}
	decision := releasegate.Evaluate(input)
	input.HumanApproval = &releasegate.HumanApproval{
		Actor: "release-owner", ActorType: "human", SubjectDigest: decision.SubjectDigest, Approved: true,
	}
	return input
}

func reverse[T any](values []T) []T {
	result := append([]T(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func TestNewGateEvaluationEventCanonicalizesEquivalentEvidence(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	occurredAt := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	input := validGateInput(t)
	first, err := newGateEvaluationEvent(workItemID, input, occurredAt)
	if err != nil {
		t.Fatal(err)
	}

	reordered := input
	reordered.Revisions = reverse(input.Revisions)
	reordered.Policy.RequiredTestKinds = reverse(input.Policy.RequiredTestKinds)
	reordered.Branches = reverse(input.Branches)
	for index := range reordered.Branches {
		reordered.Branches[index].RequiredChecks = reverse(reordered.Branches[index].RequiredChecks)
	}
	reordered.Checks = reverse(input.Checks)
	reordered.Reviews = reverse(input.Reviews)
	reordered.Vault = reverse(input.Vault)
	reordered.Tests = reverse(input.Tests)
	reordered.Security = reverse(input.Security)
	second, err := newGateEvaluationEvent(workItemID, reordered, occurredAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first.PayloadDigest != second.PayloadDigest || first.DedupeKey != second.DedupeKey {
		t.Fatalf("equivalent evidence must have one idempotency identity: %s/%s != %s/%s", first.PayloadDigest, first.DedupeKey, second.PayloadDigest, second.DedupeKey)
	}
}

func TestProjectGateEvaluationVerifiesIntegrityAndKeepsPrivateEvidenceOut(t *testing.T) {
	event, err := newGateEvaluationEvent(uuid.Must(uuid.NewV4()), validGateInput(t), time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	event.ID = uuid.Must(uuid.NewV4())
	event.Sequence = 7
	projection, err := ProjectGateEvaluation(event)
	if err != nil {
		t.Fatal(err)
	}
	if projection.State != "allowed" || projection.Sequence != 7 || projection.SubjectDigest == "" {
		t.Fatalf("unexpected projection: %#v", projection)
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{testRepositoryA, testRepositoryB, "author-a", "reviewer-a", "release-owner", "vault-a", `"input"`, `"payload"`} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("public projection disclosed private evidence %q: %s", private, encoded)
		}
	}

	corrupted := event
	corrupted.PayloadJSON += " "
	if _, err := ProjectGateEvaluation(corrupted); err == nil {
		t.Fatal("a corrupted ledger payload must fail closed")
	}
	invalidEnvelope := event
	invalidEnvelope.WorkItemID = uuid.Nil
	if _, err := ProjectGateEvaluation(invalidEnvelope); err == nil {
		t.Fatal("an invalid ledger envelope must fail closed")
	}
}

func TestNewGateEvaluationEventPersistsBlockedDecisionsWithoutExecutingThem(t *testing.T) {
	input := validGateInput(t)
	input.Security[0].HighFindings = 1
	event, err := newGateEvaluationEvent(uuid.Must(uuid.NewV4()), input, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	event.ID = uuid.Must(uuid.NewV4())
	event.Sequence = 1
	projection, err := ProjectGateEvaluation(event)
	if err != nil {
		t.Fatal(err)
	}
	if projection.State != "blocked" {
		t.Fatalf("unsafe evidence must remain blocked in the ledger: %#v", projection)
	}
	if _, _, err := RecordGateEvaluation(nil, event.WorkItemID, input, time.Now().UTC()); err == nil {
		t.Fatal("persistence without a database must fail closed")
	}
}
