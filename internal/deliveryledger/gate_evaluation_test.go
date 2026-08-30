package deliveryledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/releasegate"
	"events-stocks/models"

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
	if projection.State != "allowed" || projection.Sequence != 7 || projection.PolicyDigest == "" || projection.VaultDigest == "" || projection.SubjectDigest == "" {
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
	legacyEnvelope := event
	legacyEnvelope.EventType = "delivery.release_gate.evaluated.v1"
	if _, err := ProjectGateEvaluation(legacyEnvelope); err == nil {
		t.Fatal("a legacy Gatekeeper event must not authorize the v2 contract")
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

func TestAuthorizeGateEvaluationBindsCurrentHumanWorkItemActionAndTime(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.August, 30, 15, 0, 0, 0, time.UTC)
	input := validGateInput(t)
	input.ChangeSetID = workItemID.String()
	input.Action = releasegate.ActionRelease
	decision := releasegate.Evaluate(input)
	input.HumanApproval = &releasegate.HumanApproval{
		Actor: "release-owner", ActorType: "human", SubjectDigest: decision.SubjectDigest, Approved: true,
	}
	event, err := newGateEvaluationEvent(workItemID, input, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	event.ID = uuid.Must(uuid.NewV4())
	event.Sequence = 12

	projection, err := AuthorizeGateEvaluation(event, workItemID, releasegate.ActionRelease, "release-owner", now, 10*time.Minute)
	if err != nil || projection.State != "allowed" || projection.ChangeSetID != workItemID.String() {
		t.Fatalf("exact authorization was rejected: %#v / %v", projection, err)
	}

	tests := []struct {
		name     string
		event    models.DeliveryEvent
		workItem uuid.UUID
		action   releasegate.Action
		actor    string
		now      time.Time
	}{
		{name: "different work item", event: event, workItem: uuid.Must(uuid.NewV4()), action: releasegate.ActionRelease, actor: "release-owner", now: now},
		{name: "different action", event: event, workItem: workItemID, action: releasegate.ActionMerge, actor: "release-owner", now: now},
		{name: "different human", event: event, workItem: workItemID, action: releasegate.ActionRelease, actor: "another-human", now: now},
		{name: "stale", event: event, workItem: workItemID, action: releasegate.ActionRelease, actor: "release-owner", now: now.Add(11 * time.Minute)},
		{name: "far future", event: event, workItem: workItemID, action: releasegate.ActionRelease, actor: "release-owner", now: now.Add(-3 * time.Minute)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := AuthorizeGateEvaluation(test.event, test.workItem, test.action, test.actor, test.now, 10*time.Minute); err == nil {
				t.Fatal("mismatched or stale authorization must fail closed")
			}
		})
	}
}

func TestAuthorizeGateEvaluationRejectsBlockedAndUnboundChangeSets(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	now := time.Now().UTC()
	for _, mutate := range []func(*releasegate.Input){
		func(input *releasegate.Input) { input.ChangeSetID = "another-change-set" },
		func(input *releasegate.Input) { input.Security[0].HighFindings = 1 },
	} {
		input := validGateInput(t)
		input.ChangeSetID = workItemID.String()
		mutate(&input)
		decision := releasegate.Evaluate(input)
		input.HumanApproval = &releasegate.HumanApproval{Actor: "release-owner", ActorType: "human", SubjectDigest: decision.SubjectDigest, Approved: true}
		event, err := newGateEvaluationEvent(workItemID, input, now)
		if err != nil {
			t.Fatal(err)
		}
		event.ID, event.Sequence = uuid.Must(uuid.NewV4()), 1
		if _, err := AuthorizeGateEvaluation(event, workItemID, releasegate.ActionRelease, "release-owner", now, 10*time.Minute); err == nil {
			t.Fatal("blocked or unbound release Gatekeeper event must fail closed")
		}
	}
}

func TestAuthorizeGateEvaluationRecomputesStoredDecision(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	now := time.Now().UTC()
	input := validGateInput(t)
	input.ChangeSetID = workItemID.String()
	input.Security[0].HighFindings = 1
	decision := releasegate.Evaluate(input)
	input.HumanApproval = &releasegate.HumanApproval{Actor: "release-owner", ActorType: "human", SubjectDigest: decision.SubjectDigest, Approved: true}
	event, err := newGateEvaluationEvent(workItemID, input, now)
	if err != nil {
		t.Fatal(err)
	}
	event.ID, event.Sequence = uuid.Must(uuid.NewV4()), 1

	var payload gateEvaluationPayload
	if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	payload.Decision.State = "allowed"
	payload.Decision.Reasons = []releasegate.Reason{}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	event.PayloadJSON = string(encoded)
	event.PayloadDigest = hex.EncodeToString(digest[:])

	if _, err := AuthorizeGateEvaluation(event, workItemID, releasegate.ActionRelease, "release-owner", now, 10*time.Minute); err == nil {
		t.Fatal("a forged allowed decision must fail deterministic recomputation")
	}
}
