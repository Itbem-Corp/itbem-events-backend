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

	"events-stocks/internal/environmentevidence"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const EventTypeEnvironmentObserved = "delivery.environment.observed.v1"

type environmentObservationPayload struct {
	SchemaVersion int                             `json:"schema_version"`
	Observation   environmentevidence.Observation `json:"observation"`
}

type EnvironmentObservation struct {
	EventID     uuid.UUID                       `json:"event_id"`
	Sequence    int64                           `json:"sequence"`
	Observation environmentevidence.Observation `json:"observation"`
	OccurredAt  time.Time                       `json:"occurred_at"`
}

// RecordEnvironmentObservation appends one value-free exact-matrix release
// readiness observation. A callback retry is idempotent only when its complete
// canonical payload is identical to the first accepted event.
func RecordEnvironmentObservation(db *gorm.DB, workItemID uuid.UUID, observation environmentevidence.Observation, occurredAt time.Time) (models.DeliveryEvent, bool, error) {
	if db == nil || workItemID == uuid.Nil || occurredAt.IsZero() {
		return models.DeliveryEvent{}, false, fmt.Errorf("delivery environment observation persistence input is invalid")
	}
	event, err := newEnvironmentObservationEvent(workItemID, observation, occurredAt)
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
				return fmt.Errorf("environment task already recorded different evidence")
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
		return models.DeliveryEvent{}, false, fmt.Errorf("append delivery environment observation: %w", err)
	}
	return event, created, nil
}

func newEnvironmentObservationEvent(workItemID uuid.UUID, observation environmentevidence.Observation, occurredAt time.Time) (models.DeliveryEvent, error) {
	canonical, err := environmentevidence.Canonical(observation)
	if workItemID == uuid.Nil || occurredAt.IsZero() || err != nil {
		return models.DeliveryEvent{}, fmt.Errorf("delivery environment observation event input is invalid")
	}
	payload, err := json.Marshal(environmentObservationPayload{SchemaVersion: environmentevidence.SchemaVersion, Observation: canonical})
	if err != nil {
		return models.DeliveryEvent{}, fmt.Errorf("encode delivery environment observation: %w", err)
	}
	digest := sha256.Sum256(payload)
	return models.DeliveryEvent{
		WorkItemID: workItemID, EventType: EventTypeEnvironmentObserved,
		DedupeKey:     workItemID.String() + ":environment-observation-v1:" + canonical.TaskID,
		SubjectDigest: canonical.MatrixDigest, PayloadJSON: string(payload), PayloadDigest: hex.EncodeToString(digest[:]),
		ActorType: "system", ActorID: "release-runner/github-environment/v1", OccurredAt: occurredAt.UTC(), CreatedAt: occurredAt.UTC(),
	}, nil
}

func ProjectEnvironmentObservation(event models.DeliveryEvent) (EnvironmentObservation, error) {
	if event.ID == uuid.Nil || event.WorkItemID == uuid.Nil || event.Sequence < 1 || event.EventType != EventTypeEnvironmentObserved || event.OccurredAt.IsZero() {
		return EnvironmentObservation{}, fmt.Errorf("delivery environment event envelope is invalid")
	}
	payload := []byte(event.PayloadJSON)
	digest := sha256.Sum256(payload)
	if !strings.EqualFold(event.PayloadDigest, hex.EncodeToString(digest[:])) {
		return EnvironmentObservation{}, fmt.Errorf("delivery environment event payload digest does not match")
	}
	var envelope struct {
		SchemaVersion int             `json:"schema_version"`
		Observation   json.RawMessage `json:"observation"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.SchemaVersion != environmentevidence.SchemaVersion {
		return EnvironmentObservation{}, fmt.Errorf("delivery environment event payload is invalid")
	}
	observation, err := environmentevidence.Decode(envelope.Observation)
	if err != nil || !strings.EqualFold(observation.MatrixDigest, event.SubjectDigest) {
		return EnvironmentObservation{}, fmt.Errorf("delivery environment event payload is invalid")
	}
	return EnvironmentObservation{EventID: event.ID, Sequence: event.Sequence, Observation: observation, OccurredAt: event.OccurredAt.UTC()}, nil
}
