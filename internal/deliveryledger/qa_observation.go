package deliveryledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"events-stocks/internal/qaevidence"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Version two adds operator-owned named command identities. Version-one
// observations remain immutable history but are intentionally ineligible for
// named release gates.
const EventTypeQAObserved = "delivery.qa.observed.v2"

type qaObservationPayload struct {
	SchemaVersion int                    `json:"schema_version"`
	Observation   qaevidence.Observation `json:"observation"`
}

type QAObservation struct {
	EventID     uuid.UUID              `json:"event_id"`
	Sequence    int64                  `json:"sequence"`
	Observation qaevidence.Observation `json:"observation"`
	OccurredAt  time.Time              `json:"occurred_at"`
}

// RecordQAObservation appends one immutable observation for one exact QA task.
// A callback retry is idempotent only when its canonical payload is identical.
func RecordQAObservation(db *gorm.DB, workItemID uuid.UUID, observation qaevidence.Observation, occurredAt time.Time) (models.DeliveryEvent, bool, error) {
	if db == nil || workItemID == uuid.Nil || occurredAt.IsZero() {
		return models.DeliveryEvent{}, false, fmt.Errorf("delivery QA observation persistence input is invalid")
	}
	event, err := newQAObservationEvent(workItemID, observation, occurredAt)
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
			if !strings.EqualFold(existing.PayloadDigest, event.PayloadDigest) || !strings.EqualFold(existing.SubjectDigest, event.SubjectDigest) || existing.EventType != event.EventType {
				return fmt.Errorf("QA task already recorded different evidence")
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
		return models.DeliveryEvent{}, false, fmt.Errorf("append delivery QA observation: %w", err)
	}
	return event, created, nil
}

func newQAObservationEvent(workItemID uuid.UUID, observation qaevidence.Observation, occurredAt time.Time) (models.DeliveryEvent, error) {
	if workItemID == uuid.Nil || occurredAt.IsZero() {
		return models.DeliveryEvent{}, fmt.Errorf("delivery QA observation event input is invalid")
	}
	canonical, err := qaevidence.Canonical(observation)
	if err != nil {
		return models.DeliveryEvent{}, err
	}
	payload, err := json.Marshal(qaObservationPayload{SchemaVersion: qaevidence.SchemaVersion, Observation: canonical})
	if err != nil {
		return models.DeliveryEvent{}, fmt.Errorf("encode delivery QA observation: %w", err)
	}
	digest := sha256.Sum256(payload)
	payloadDigest := hex.EncodeToString(digest[:])
	return models.DeliveryEvent{
		WorkItemID: workItemID, EventType: EventTypeQAObserved,
		DedupeKey:     workItemID.String() + ":qa-observation-v2:" + canonical.TaskID,
		SubjectDigest: canonical.MatrixDigest, PayloadJSON: string(payload), PayloadDigest: payloadDigest,
		ActorType: "system", ActorID: "qa-runner/v2", OccurredAt: occurredAt.UTC(), CreatedAt: occurredAt.UTC(),
	}, nil
}

func ProjectQAObservation(event models.DeliveryEvent) (QAObservation, error) {
	if event.ID == uuid.Nil || event.WorkItemID == uuid.Nil || event.Sequence < 1 || event.EventType != EventTypeQAObserved || event.OccurredAt.IsZero() {
		return QAObservation{}, fmt.Errorf("delivery QA event envelope is invalid")
	}
	payload := []byte(event.PayloadJSON)
	digest := sha256.Sum256(payload)
	if !strings.EqualFold(event.PayloadDigest, hex.EncodeToString(digest[:])) {
		return QAObservation{}, fmt.Errorf("delivery QA event payload digest does not match")
	}
	var envelope struct {
		SchemaVersion int             `json:"schema_version"`
		Observation   json.RawMessage `json:"observation"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.SchemaVersion != qaevidence.SchemaVersion {
		return QAObservation{}, fmt.Errorf("delivery QA event payload is invalid")
	}
	observation, err := qaevidence.Decode(envelope.Observation)
	if err != nil || !strings.EqualFold(observation.MatrixDigest, event.SubjectDigest) {
		return QAObservation{}, fmt.Errorf("delivery QA event payload is invalid")
	}
	return QAObservation{EventID: event.ID, Sequence: event.Sequence, Observation: observation, OccurredAt: event.OccurredAt.UTC()}, nil
}
