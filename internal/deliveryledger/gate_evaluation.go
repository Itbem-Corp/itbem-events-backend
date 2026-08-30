package deliveryledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"events-stocks/internal/releasegate"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const EventTypeReleaseGateEvaluated = "delivery.release_gate.evaluated.v2"

type gateEvaluationPayload struct {
	SchemaVersion int                  `json:"schema_version"`
	Input         releasegate.Input    `json:"input"`
	Decision      releasegate.Decision `json:"decision"`
}

// GateEvaluation is the presentation-safe projection of one private ledger
// event. It deliberately excludes the exact input, actors, repository matrix,
// Vault revision IDs, and any future private evidence references.
type GateEvaluation struct {
	EventID       uuid.UUID            `json:"event_id"`
	Sequence      int64                `json:"sequence"`
	Action        releasegate.Action   `json:"action"`
	ChangeSetID   string               `json:"change_set_id"`
	MatrixDigest  string               `json:"matrix_digest,omitempty"`
	PolicyDigest  string               `json:"policy_digest,omitempty"`
	VaultDigest   string               `json:"vault_digest,omitempty"`
	SubjectDigest string               `json:"subject_digest,omitempty"`
	State         string               `json:"state"`
	Reasons       []releasegate.Reason `json:"reasons"`
	OccurredAt    time.Time            `json:"occurred_at"`
}

// RecordGateEvaluation evaluates and appends one immutable ledger event. It
// owns no merge/release side effect. The work-item row lock serializes sequence
// allocation, and the canonical payload digest makes an identical retry safe.
func RecordGateEvaluation(db *gorm.DB, workItemID uuid.UUID, input releasegate.Input, occurredAt time.Time) (models.DeliveryEvent, bool, error) {
	if db == nil || workItemID == uuid.Nil || occurredAt.IsZero() {
		return models.DeliveryEvent{}, false, fmt.Errorf("delivery gate evaluation persistence input is invalid")
	}
	event, err := newGateEvaluationEvent(workItemID, input, occurredAt)
	if err != nil {
		return models.DeliveryEvent{}, false, err
	}
	created := false
	err = db.Transaction(func(tx *gorm.DB) error {
		var workItem models.DeliveryWorkItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&workItem, workItemID).Error; err != nil {
			return err
		}
		var existing models.DeliveryEvent
		findErr := tx.Where("dedupe_key = ?", event.DedupeKey).First(&existing).Error
		if findErr == nil {
			event = existing
			return nil
		}
		if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}
		var lastSequence int64
		if err := tx.Model(&models.DeliveryEvent{}).Where("work_item_id = ?", workItemID).Select("COALESCE(MAX(sequence), 0)").Scan(&lastSequence).Error; err != nil {
			return err
		}
		event.Sequence = lastSequence + 1
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return models.DeliveryEvent{}, false, fmt.Errorf("append delivery gate evaluation: %w", err)
	}
	return event, created, nil
}

func newGateEvaluationEvent(workItemID uuid.UUID, input releasegate.Input, occurredAt time.Time) (models.DeliveryEvent, error) {
	if workItemID == uuid.Nil || occurredAt.IsZero() {
		return models.DeliveryEvent{}, fmt.Errorf("delivery gate evaluation event input is invalid")
	}
	input = canonicalGateInput(input)
	decision := releasegate.Evaluate(input)
	payload, err := json.Marshal(gateEvaluationPayload{SchemaVersion: releasegate.SchemaVersion, Input: input, Decision: decision})
	if err != nil {
		return models.DeliveryEvent{}, fmt.Errorf("encode delivery gate evaluation: %w", err)
	}
	digest := sha256.Sum256(payload)
	payloadDigest := hex.EncodeToString(digest[:])
	return models.DeliveryEvent{
		WorkItemID: workItemID, EventType: EventTypeReleaseGateEvaluated,
		DedupeKey:     workItemID.String() + ":release-gate-v2:" + payloadDigest,
		SubjectDigest: decision.SubjectDigest, PayloadJSON: string(payload), PayloadDigest: payloadDigest,
		ActorType: "system", ActorID: "release-gate/v2", OccurredAt: occurredAt.UTC(), CreatedAt: occurredAt.UTC(),
	}, nil
}

// ProjectGateEvaluation verifies the stored payload digest before exposing a
// bounded decision. A corrupted or mismatched ledger row fails closed.
func ProjectGateEvaluation(event models.DeliveryEvent) (GateEvaluation, error) {
	if event.ID == uuid.Nil || event.WorkItemID == uuid.Nil || event.Sequence < 1 || event.EventType != EventTypeReleaseGateEvaluated || event.OccurredAt.IsZero() {
		return GateEvaluation{}, fmt.Errorf("delivery gate event envelope is invalid")
	}
	payload := []byte(event.PayloadJSON)
	digest := sha256.Sum256(payload)
	if !strings.EqualFold(event.PayloadDigest, hex.EncodeToString(digest[:])) {
		return GateEvaluation{}, fmt.Errorf("delivery gate event payload digest does not match")
	}
	var decoded gateEvaluationPayload
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.SchemaVersion != releasegate.SchemaVersion {
		return GateEvaluation{}, fmt.Errorf("delivery gate event payload is invalid")
	}
	if decoded.Decision.SchemaVersion != releasegate.SchemaVersion || decoded.Decision.SubjectDigest != event.SubjectDigest {
		return GateEvaluation{}, fmt.Errorf("delivery gate event decision does not match its envelope")
	}
	return GateEvaluation{
		EventID: event.ID, Sequence: event.Sequence, Action: decoded.Decision.Action,
		ChangeSetID: decoded.Decision.ChangeSetID, MatrixDigest: decoded.Decision.MatrixDigest,
		PolicyDigest: decoded.Decision.PolicyDigest, VaultDigest: decoded.Decision.VaultDigest,
		SubjectDigest: decoded.Decision.SubjectDigest, State: decoded.Decision.State,
		Reasons: append([]releasegate.Reason(nil), decoded.Decision.Reasons...), OccurredAt: event.OccurredAt.UTC(),
	}, nil
}

// AuthorizeGateEvaluation verifies that an immutable Gatekeeper event is safe
// to consume for one immediate human-authorized action. Reading a historical
// "allowed" projection is not sufficient: the event must belong to this work
// item, target the expected action and change-set identity, contain the exact
// current human actor, still reproduce the stored deterministic decision, and
// be recent enough that the action adapter can revalidate its exact SHA matrix.
//
// This function performs no merge, release, or deployment side effect.
func AuthorizeGateEvaluation(event models.DeliveryEvent, workItemID uuid.UUID, action releasegate.Action, humanActor string, now time.Time, maxAge time.Duration) (GateEvaluation, error) {
	actor := strings.TrimSpace(humanActor)
	if workItemID == uuid.Nil || (action != releasegate.ActionMerge && action != releasegate.ActionRelease) || actor == "" || now.IsZero() || maxAge <= 0 {
		return GateEvaluation{}, fmt.Errorf("release gate authorization context is invalid")
	}
	if event.WorkItemID != workItemID {
		return GateEvaluation{}, fmt.Errorf("release gate event belongs to another work item")
	}
	projection, err := ProjectGateEvaluation(event)
	if err != nil {
		return GateEvaluation{}, err
	}
	var decoded gateEvaluationPayload
	if err := json.Unmarshal([]byte(event.PayloadJSON), &decoded); err != nil || decoded.SchemaVersion != releasegate.SchemaVersion {
		return GateEvaluation{}, fmt.Errorf("release gate event payload is invalid")
	}
	recomputed := releasegate.Evaluate(decoded.Input)
	if !reflect.DeepEqual(recomputed, decoded.Decision) {
		return GateEvaluation{}, fmt.Errorf("release gate decision cannot be reproduced")
	}
	if projection.Action != action || projection.State != "allowed" || len(projection.Reasons) != 0 {
		return GateEvaluation{}, fmt.Errorf("release gate did not allow the requested action")
	}
	if decoded.Input.ChangeSetID != workItemID.String() || projection.ChangeSetID != workItemID.String() {
		return GateEvaluation{}, fmt.Errorf("release gate change-set does not match the work item")
	}
	approval := decoded.Input.HumanApproval
	if approval == nil || !approval.Approved || approval.ActorType != "human" || approval.Actor != actor || !strings.EqualFold(approval.SubjectDigest, projection.SubjectDigest) {
		return GateEvaluation{}, fmt.Errorf("release gate human approval does not match the current actor")
	}
	occurredAt := event.OccurredAt.UTC()
	now = now.UTC()
	if occurredAt.After(now.Add(time.Minute)) || now.Sub(occurredAt) > maxAge {
		return GateEvaluation{}, fmt.Errorf("release gate evaluation is stale")
	}
	return projection, nil
}

func canonicalGateInput(input releasegate.Input) releasegate.Input {
	clone := input
	clone.Revisions = append([]releasegate.Revision(nil), input.Revisions...)
	clone.Policy.RequiredTestKinds = append([]string(nil), input.Policy.RequiredTestKinds...)
	clone.Branches = append([]releasegate.BranchEvidence(nil), input.Branches...)
	clone.Checks = append([]releasegate.CheckEvidence(nil), input.Checks...)
	clone.Reviews = append([]releasegate.ReviewEvidence(nil), input.Reviews...)
	clone.Vault = append([]releasegate.VaultEvidence(nil), input.Vault...)
	clone.Tests = append([]releasegate.TestEvidence(nil), input.Tests...)
	clone.Security = append([]releasegate.SecurityEvidence(nil), input.Security...)
	for index := range clone.Branches {
		clone.Branches[index].RequiredChecks = append([]string(nil), clone.Branches[index].RequiredChecks...)
		sort.Slice(clone.Branches[index].RequiredChecks, func(left, right int) bool {
			return strings.ToLower(clone.Branches[index].RequiredChecks[left]) < strings.ToLower(clone.Branches[index].RequiredChecks[right])
		})
	}
	sort.Slice(clone.Policy.RequiredTestKinds, func(left, right int) bool {
		return strings.ToLower(clone.Policy.RequiredTestKinds[left]) < strings.ToLower(clone.Policy.RequiredTestKinds[right])
	})
	sort.Slice(clone.Revisions, func(left, right int) bool {
		return strings.ToLower(clone.Revisions[left].Repository) < strings.ToLower(clone.Revisions[right].Repository)
	})
	sort.Slice(clone.Branches, func(left, right int) bool {
		return strings.ToLower(clone.Branches[left].Repository) < strings.ToLower(clone.Branches[right].Repository)
	})
	sort.Slice(clone.Checks, func(left, right int) bool {
		return strings.ToLower(clone.Checks[left].Repository+"\x00"+clone.Checks[left].Name) < strings.ToLower(clone.Checks[right].Repository+"\x00"+clone.Checks[right].Name)
	})
	sort.Slice(clone.Reviews, func(left, right int) bool {
		return strings.ToLower(clone.Reviews[left].Repository+"\x00"+clone.Reviews[left].ReviewerActor) < strings.ToLower(clone.Reviews[right].Repository+"\x00"+clone.Reviews[right].ReviewerActor)
	})
	sort.Slice(clone.Vault, func(left, right int) bool {
		return strings.ToLower(clone.Vault[left].Repository) < strings.ToLower(clone.Vault[right].Repository)
	})
	sort.Slice(clone.Tests, func(left, right int) bool {
		return strings.ToLower(clone.Tests[left].Kind) < strings.ToLower(clone.Tests[right].Kind)
	})
	sort.Slice(clone.Security, func(left, right int) bool {
		return strings.ToLower(clone.Security[left].Repository) < strings.ToLower(clone.Security[right].Repository)
	})
	return clone
}
