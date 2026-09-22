// Package deliveryledger keeps immutable, tamper-evident delivery evidence.
package deliveryledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"events-stocks/internal/deliverypolicy"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EventTypeAutonomySnapshot is recorded once, before a work item can use a
// delegated gate. It is intentionally separate from live policy rows: later
// configuration changes must never alter an in-flight delivery's authority.
const EventTypeAutonomySnapshot = "delivery.autonomy.snapshot.v1"

var autonomyDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var autonomySHApattern = regexp.MustCompile(`^[a-f0-9]{40,64}$`)

// AutonomyRepository freezes one repository boundary. Policy is retained in
// the private ledger so a gatekeeper can reconstruct the exact configuration;
// browser projections expose only its digests and approval mode.
type AutonomyRepository struct {
	Repository      string                        `json:"repository"`
	SourceReference string                        `json:"source_reference"`
	SourceRevision  string                        `json:"source_revision"`
	VaultRevisionID string                        `json:"vault_revision_id"`
	VaultVersion    int64                         `json:"vault_version"`
	VaultRevision   string                        `json:"vault_revision"`
	VaultDigest     string                        `json:"vault_digest"`
	Policy          deliverypolicy.ResolvedPolicy `json:"policy"`
}

// AutonomySnapshotInput is deliberately task-scoped. The caller obtains the
// policy only from approved policy revisions and the Vault only from the
// frozen repository source; this package seals that already-authorized input.
type AutonomySnapshotInput struct {
	ProjectID    uuid.UUID            `json:"project_id"`
	ChangeSetID  string               `json:"change_set_id"`
	Repositories []AutonomyRepository `json:"repositories"`
}

type autonomySnapshotPayload struct {
	SchemaVersion int                   `json:"schema_version"`
	Input         AutonomySnapshotInput `json:"input"`
}

// AutonomyRepositorySnapshot is the safe read model for a frozen authority
// boundary. It intentionally omits policy approver identities and content.
type AutonomyRepositorySnapshot struct {
	Repository       string                          `json:"repository"`
	SourceReference  string                          `json:"source_reference"`
	SourceRevision   string                          `json:"source_revision"`
	VaultRevisionID  string                          `json:"vault_revision_id"`
	VaultVersion     int64                           `json:"vault_version"`
	VaultRevision    string                          `json:"vault_revision"`
	VaultDigest      string                          `json:"vault_digest"`
	PolicyDigest     string                          `json:"policy_digest"`
	GateApprovalMode deliverypolicy.GateApprovalMode `json:"gate_approval_mode"`
}

// AutonomySnapshot is a presentation-safe verified projection. Delegated is
// true only when every affected repository explicitly opted into it.
type AutonomySnapshot struct {
	EventID      uuid.UUID                    `json:"event_id"`
	Sequence     int64                        `json:"sequence"`
	ProjectID    uuid.UUID                    `json:"project_id"`
	ChangeSetID  string                       `json:"change_set_id"`
	Repositories []AutonomyRepositorySnapshot `json:"repositories"`
	Delegated    bool                         `json:"delegated"`
	OccurredAt   time.Time                    `json:"occurred_at"`
}

// RecordAutonomySnapshot appends the first valid snapshot for a work item.
// It is idempotent for an identical retry and fails closed if a retry tries to
// substitute policy, Vault, or source SHA after the work item has started.
func RecordAutonomySnapshot(db *gorm.DB, workItemID uuid.UUID, input AutonomySnapshotInput, occurredAt time.Time) (models.DeliveryEvent, bool, error) {
	if db == nil || workItemID == uuid.Nil || occurredAt.IsZero() {
		return models.DeliveryEvent{}, false, fmt.Errorf("delivery autonomy snapshot persistence input is invalid")
	}
	event, err := newAutonomySnapshotEvent(workItemID, input, occurredAt)
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
		findErr := tx.Where("work_item_id = ? AND event_type = ?", workItemID, EventTypeAutonomySnapshot).Order("sequence ASC").First(&existing).Error
		if findErr == nil {
			if !strings.EqualFold(existing.PayloadDigest, event.PayloadDigest) || !strings.EqualFold(existing.SubjectDigest, event.SubjectDigest) {
				return fmt.Errorf("delivery autonomy snapshot is already frozen for this work item")
			}
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
		return models.DeliveryEvent{}, false, fmt.Errorf("append delivery autonomy snapshot: %w", err)
	}
	return event, created, nil
}

func newAutonomySnapshotEvent(workItemID uuid.UUID, input AutonomySnapshotInput, occurredAt time.Time) (models.DeliveryEvent, error) {
	canonical, err := canonicalAutonomySnapshotInput(input)
	if workItemID == uuid.Nil || occurredAt.IsZero() || err != nil {
		return models.DeliveryEvent{}, fmt.Errorf("delivery autonomy snapshot input is invalid")
	}
	payload, err := json.Marshal(autonomySnapshotPayload{SchemaVersion: 1, Input: canonical})
	if err != nil {
		return models.DeliveryEvent{}, fmt.Errorf("encode delivery autonomy snapshot: %w", err)
	}
	digest := sha256.Sum256(payload)
	payloadDigest := hex.EncodeToString(digest[:])
	return models.DeliveryEvent{
		WorkItemID: workItemID, EventType: EventTypeAutonomySnapshot,
		DedupeKey: workItemID.String() + ":autonomy-snapshot-v1", SubjectDigest: autonomySnapshotSubjectDigest(canonical),
		PayloadJSON: string(payload), PayloadDigest: payloadDigest,
		ActorType: "system", ActorID: "delivery-autonomy/v1", OccurredAt: occurredAt.UTC(), CreatedAt: occurredAt.UTC(),
	}, nil
}

// ProjectAutonomySnapshot verifies the payload seal before exposing the
// minimal evidence used by the coordinator and UI.
func ProjectAutonomySnapshot(event models.DeliveryEvent) (AutonomySnapshot, error) {
	if event.ID == uuid.Nil || event.WorkItemID == uuid.Nil || event.Sequence < 1 || event.EventType != EventTypeAutonomySnapshot || event.OccurredAt.IsZero() {
		return AutonomySnapshot{}, fmt.Errorf("delivery autonomy snapshot event envelope is invalid")
	}
	payload := []byte(event.PayloadJSON)
	digest := sha256.Sum256(payload)
	if !strings.EqualFold(event.PayloadDigest, hex.EncodeToString(digest[:])) {
		return AutonomySnapshot{}, fmt.Errorf("delivery autonomy snapshot payload digest does not match")
	}
	var decoded autonomySnapshotPayload
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.SchemaVersion != 1 {
		return AutonomySnapshot{}, fmt.Errorf("delivery autonomy snapshot payload is invalid")
	}
	canonical, err := canonicalAutonomySnapshotInput(decoded.Input)
	if err != nil || !sameAutonomySnapshotInput(canonical, decoded.Input) || !strings.EqualFold(event.SubjectDigest, autonomySnapshotSubjectDigest(canonical)) {
		return AutonomySnapshot{}, fmt.Errorf("delivery autonomy snapshot payload is not canonical")
	}
	result := AutonomySnapshot{EventID: event.ID, Sequence: event.Sequence, ProjectID: canonical.ProjectID, ChangeSetID: canonical.ChangeSetID, Repositories: make([]AutonomyRepositorySnapshot, 0, len(canonical.Repositories)), OccurredAt: event.OccurredAt.UTC()}
	result.Delegated = len(canonical.Repositories) > 0
	for _, repository := range canonical.Repositories {
		result.Repositories = append(result.Repositories, AutonomyRepositorySnapshot{
			Repository: repository.Repository, SourceReference: repository.SourceReference, SourceRevision: repository.SourceRevision,
			VaultRevisionID: repository.VaultRevisionID, VaultVersion: repository.VaultVersion, VaultRevision: repository.VaultRevision, VaultDigest: repository.VaultDigest,
			PolicyDigest: repository.Policy.Digest, GateApprovalMode: repository.Policy.GateApprovalMode,
		})
		if repository.Policy.GateApprovalMode != deliverypolicy.GateApprovalDelegated {
			result.Delegated = false
		}
	}
	return result, nil
}

func canonicalAutonomySnapshotInput(input AutonomySnapshotInput) (AutonomySnapshotInput, error) {
	if input.ProjectID == uuid.Nil || strings.TrimSpace(input.ChangeSetID) == "" || len(input.Repositories) == 0 || len(input.Repositories) > 32 {
		return AutonomySnapshotInput{}, fmt.Errorf("missing required autonomy snapshot scope")
	}
	input.ChangeSetID = strings.TrimSpace(input.ChangeSetID)
	result := AutonomySnapshotInput{ProjectID: input.ProjectID, ChangeSetID: input.ChangeSetID, Repositories: append([]AutonomyRepository(nil), input.Repositories...)}
	seen := make(map[string]struct{}, len(result.Repositories))
	for index := range result.Repositories {
		repository := &result.Repositories[index]
		repository.Repository = strings.TrimSpace(repository.Repository)
		repository.SourceReference = strings.TrimSpace(repository.SourceReference)
		repository.SourceRevision = strings.ToLower(strings.TrimSpace(repository.SourceRevision))
		repository.VaultRevisionID = strings.TrimSpace(repository.VaultRevisionID)
		repository.VaultRevision = strings.ToLower(strings.TrimSpace(repository.VaultRevision))
		repository.VaultDigest = strings.ToLower(strings.TrimSpace(repository.VaultDigest))
		if repository.Repository == "" || repository.SourceReference == "" || !autonomySHApattern.MatchString(repository.SourceRevision) || repository.VaultRevisionID == "" || repository.VaultVersion < 1 || !autonomySHApattern.MatchString(repository.VaultRevision) || !autonomyDigestPattern.MatchString(repository.VaultDigest) {
			return AutonomySnapshotInput{}, fmt.Errorf("repository autonomy boundary is invalid")
		}
		policy := repository.Policy
		if policy.Context.ProjectID != input.ProjectID.String() || !strings.EqualFold(strings.TrimSpace(policy.Context.ChangeSetID), input.ChangeSetID) || !strings.EqualFold(strings.TrimSpace(policy.Context.Repository), repository.Repository) || !policy.Resolved || !autonomyDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(policy.Digest))) || (policy.GateApprovalMode != deliverypolicy.GateApprovalHuman && policy.GateApprovalMode != deliverypolicy.GateApprovalDelegated) {
			return AutonomySnapshotInput{}, fmt.Errorf("repository policy is not a resolved immutable authority")
		}
		if _, duplicate := seen[strings.ToLower(repository.Repository)]; duplicate {
			return AutonomySnapshotInput{}, fmt.Errorf("repository autonomy boundary is duplicated")
		}
		seen[strings.ToLower(repository.Repository)] = struct{}{}
	}
	sort.Slice(result.Repositories, func(left, right int) bool {
		return strings.ToLower(result.Repositories[left].Repository) < strings.ToLower(result.Repositories[right].Repository)
	})
	return result, nil
}

func autonomySnapshotSubjectDigest(input AutonomySnapshotInput) string {
	values := make([]string, 0, len(input.Repositories))
	for _, repository := range input.Repositories {
		values = append(values, strings.ToLower(repository.Repository)+"|"+repository.SourceRevision+"|"+repository.VaultDigest+"|"+strings.ToLower(repository.Policy.Digest))
	}
	sum := sha256.Sum256([]byte(strings.Join(values, "\n")))
	return hex.EncodeToString(sum[:])
}

func sameAutonomySnapshotInput(left, right AutonomySnapshotInput) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}
