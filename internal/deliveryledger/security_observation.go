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

	"events-stocks/internal/securityevidence"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const EventTypeSecurityObserved = "delivery.security.observed.v1"

type securityObservationPayload struct {
	SchemaVersion int                          `json:"schema_version"`
	Observation   securityevidence.Observation `json:"observation"`
}

type SecurityObservation struct {
	EventID     uuid.UUID                    `json:"event_id"`
	Sequence    int64                        `json:"sequence"`
	Observation securityevidence.Observation `json:"observation"`
	OccurredAt  time.Time                    `json:"occurred_at"`
}

// RecordSecurityObservation appends one exact-matrix local security result.
// Retries are idempotent only for an identical task-scoped payload.
func RecordSecurityObservation(db *gorm.DB, workItemID uuid.UUID, observation securityevidence.Observation, occurredAt time.Time) (models.DeliveryEvent, bool, error) {
	if db == nil || workItemID == uuid.Nil || occurredAt.IsZero() {
		return models.DeliveryEvent{}, false, fmt.Errorf("delivery security observation persistence input is invalid")
	}
	event, err := newSecurityObservationEvent(workItemID, observation, occurredAt)
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
				return fmt.Errorf("security task already recorded different evidence")
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
		return models.DeliveryEvent{}, false, fmt.Errorf("append delivery security observation: %w", err)
	}
	return event, created, nil
}

func newSecurityObservationEvent(workItemID uuid.UUID, observation securityevidence.Observation, occurredAt time.Time) (models.DeliveryEvent, error) {
	canonical, err := securityevidence.Canonical(observation)
	if workItemID == uuid.Nil || occurredAt.IsZero() || err != nil {
		return models.DeliveryEvent{}, fmt.Errorf("delivery security observation event input is invalid")
	}
	payload, err := json.Marshal(securityObservationPayload{SchemaVersion: securityevidence.SchemaVersion, Observation: canonical})
	if err != nil {
		return models.DeliveryEvent{}, fmt.Errorf("encode delivery security observation: %w", err)
	}
	digest := sha256.Sum256(payload)
	return models.DeliveryEvent{
		WorkItemID: workItemID, EventType: EventTypeSecurityObserved,
		DedupeKey:     workItemID.String() + ":security-observation-v1:" + canonical.TaskID,
		SubjectDigest: canonical.MatrixDigest, PayloadJSON: string(payload), PayloadDigest: hex.EncodeToString(digest[:]),
		ActorType: "system", ActorID: "qa-runner/local-security/v1", OccurredAt: occurredAt.UTC(), CreatedAt: occurredAt.UTC(),
	}, nil
}

func ProjectSecurityObservation(event models.DeliveryEvent) (SecurityObservation, error) {
	if event.ID == uuid.Nil || event.WorkItemID == uuid.Nil || event.Sequence < 1 || event.EventType != EventTypeSecurityObserved || event.OccurredAt.IsZero() {
		return SecurityObservation{}, fmt.Errorf("delivery security event envelope is invalid")
	}
	payload := []byte(event.PayloadJSON)
	digest := sha256.Sum256(payload)
	if !strings.EqualFold(event.PayloadDigest, hex.EncodeToString(digest[:])) {
		return SecurityObservation{}, fmt.Errorf("delivery security event payload digest does not match")
	}
	var envelope struct {
		SchemaVersion int             `json:"schema_version"`
		Observation   json.RawMessage `json:"observation"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.SchemaVersion != securityevidence.SchemaVersion {
		return SecurityObservation{}, fmt.Errorf("delivery security event payload is invalid")
	}
	observation, err := securityevidence.Decode(envelope.Observation)
	if err != nil || !strings.EqualFold(observation.MatrixDigest, event.SubjectDigest) {
		return SecurityObservation{}, fmt.Errorf("delivery security event payload is invalid")
	}
	return SecurityObservation{EventID: event.ID, Sequence: event.Sequence, Observation: observation, OccurredAt: event.OccurredAt.UTC()}, nil
}
