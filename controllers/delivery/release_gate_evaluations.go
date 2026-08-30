package delivery

import (
	"fmt"

	"events-stocks/configuration"
	"events-stocks/internal/deliveryledger"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

const releaseGateEvaluationLimit = 50

type releaseGateEvaluationSnapshot struct {
	SchemaVersion int                             `json:"schema_version"`
	WorkItemID    uuid.UUID                       `json:"work_item_id"`
	Evaluations   []deliveryledger.GateEvaluation `json:"evaluations"`
	Truncated     bool                            `json:"truncated"`
}

// ListReleaseGateEvaluations exposes only the bounded deterministic decision
// projection. Exact inputs and actor identities remain private ledger data.
func ListReleaseGateEvaluations(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	if _, _, err := workItemActor(c, workItemID, deliveryView); err != nil {
		return err
	}
	var events []models.DeliveryEvent
	if err := configuration.DB.Select("id", "work_item_id", "sequence", "event_type", "subject_digest", "payload_json", "payload_digest", "occurred_at").
		Where("work_item_id = ? AND event_type = ?", workItemID, deliveryledger.EventTypeReleaseGateEvaluated).
		Order("sequence DESC").Limit(releaseGateEvaluationLimit + 1).Find(&events).Error; err != nil {
		return utilsError(c, err)
	}
	snapshot, err := buildReleaseGateEvaluationSnapshot(workItemID, events)
	if err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery release gate evaluations", snapshot)
}

func buildReleaseGateEvaluationSnapshot(workItemID uuid.UUID, events []models.DeliveryEvent) (releaseGateEvaluationSnapshot, error) {
	if workItemID == uuid.Nil {
		return releaseGateEvaluationSnapshot{}, fmt.Errorf("release gate work item is invalid")
	}
	snapshot := releaseGateEvaluationSnapshot{SchemaVersion: 1, WorkItemID: workItemID, Evaluations: []deliveryledger.GateEvaluation{}}
	if len(events) > releaseGateEvaluationLimit {
		snapshot.Truncated = true
		events = events[:releaseGateEvaluationLimit]
	}
	for _, event := range events {
		if event.WorkItemID != workItemID {
			return releaseGateEvaluationSnapshot{}, fmt.Errorf("release gate event belongs to another work item")
		}
		projection, err := deliveryledger.ProjectGateEvaluation(event)
		if err != nil {
			return releaseGateEvaluationSnapshot{}, err
		}
		snapshot.Evaluations = append(snapshot.Evaluations, projection)
	}
	return snapshot, nil
}
