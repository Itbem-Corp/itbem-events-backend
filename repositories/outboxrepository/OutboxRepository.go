package outboxrepository

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"events-stocks/internal/observability"
	"events-stocks/internal/runtimeroute"
	"events-stocks/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StatePending    = "pending"
	StateProcessing = "processing"
	StateCompleted  = "completed"
)

func Enqueue(db *gorm.DB, event *models.OutboxEvent) (bool, error) {
	if err := ApplyRoute(event); err != nil {
		return false, err
	}
	// Derive scheduler metadata from the already validated event. Callers may
	// not choose a lane that differs from the project/operation in the durable
	// payload.
	event.FairnessKey = fairnessKey(*event)
	if event.State == "" {
		event.State = StatePending
	}
	if event.AvailableAt.IsZero() {
		event.AvailableAt = time.Now().UTC()
	}
	result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(event)
	return result.RowsAffected == 1, result.Error
}

// ApplyRoute freezes the registered runtime selection into a durable handoff.
// It keeps direct repository enqueues from silently bypassing the dispatcher
// routing policy and rejects a caller that tries to relabel an event.
func ApplyRoute(event *models.OutboxEvent) error {
	if event == nil {
		return fmt.Errorf("outbox event is required")
	}
	route, err := runtimeroute.RouteFor(event.EventType)
	if err != nil {
		return err
	}
	if err := runtimeroute.Validate(route, event.TenantCode); err != nil {
		return err
	}
	if event.TargetRuntime != "" && event.TargetRuntime != string(route.Runtime) {
		return fmt.Errorf("outbox event runtime does not match registered route")
	}
	if event.QueueNamespace != "" && event.QueueNamespace != route.QueueNamespace {
		return fmt.Errorf("outbox event queue namespace does not match registered route")
	}
	event.TargetRuntime = string(route.Runtime)
	event.QueueNamespace = route.QueueNamespace
	return nil
}

// ClaimBatch leases ready rows using SKIP LOCKED. A durable cursor serializes
// only the short scheduling decision across API replicas, while row leases
// still provide at-least-once delivery and crash recovery. The cursor starts
// each batch after the last lane served, so a project cannot starve another
// project simply by having older or more numerous rows.
func ClaimBatch(db *gorm.DB, limit int, lease time.Duration) ([]models.OutboxEvent, error) {
	if limit <= 0 {
		limit = 10
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(lease)
	var events []models.OutboxEvent
	err := db.Transaction(func(tx *gorm.DB) error {
		cursor, err := lockDispatchCursor(tx)
		if err != nil {
			return err
		}
		if err := normalizeLegacyFairnessKeys(tx, now, fairnessNormalizationLimit(limit)); err != nil {
			return err
		}
		keys, err := readyFairnessKeys(tx, now)
		if err != nil {
			return err
		}
		keys = rotateFairnessKeys(keys, cursor.LastFairnessKey)
		events, err = claimFairRows(tx, keys, limit, now, leaseUntil)
		if err != nil {
			return err
		}
		for index := range events {
			// claimFairRows already owns the row lock; this update is the only
			// state mutation before the transaction commits.
			events[index].State = StateProcessing
		}
		if len(events) > 0 {
			cursor.LastFairnessKey = events[len(events)-1].FairnessKey
			if err := tx.Model(&models.OutboxDispatchCursor{}).
				Where("id = ?", cursor.ID).
				Updates(map[string]interface{}{"last_fairness_key": cursor.LastFairnessKey, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

const dispatchCursorKey = "outbox:global"

func lockDispatchCursor(tx *gorm.DB) (*models.OutboxDispatchCursor, error) {
	seed := &models.OutboxDispatchCursor{CursorKey: dispatchCursorKey}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(seed).Error; err != nil {
		return nil, err
	}
	var cursor models.OutboxDispatchCursor
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("cursor_key = ?", dispatchCursorKey).First(&cursor).Error; err != nil {
		return nil, err
	}
	return &cursor, nil
}

func readyOutboxScope(db *gorm.DB, now time.Time) *gorm.DB {
	return db.Where(
		"(state = ? AND available_at <= ?) OR (state = ? AND lease_until < ?)",
		StatePending, now, StateProcessing, now,
	)
}

func readyFairnessKeys(db *gorm.DB, now time.Time) ([]string, error) {
	var keys []string
	err := readyOutboxScope(db.Model(&models.OutboxEvent{}), now).
		Select("fairness_key").Group("fairness_key").Order("fairness_key ASC").Pluck("fairness_key", &keys).Error
	return keys, err
}

// normalizeLegacyFairnessKeys upgrades rows created before the persisted lane
// metadata existed. It is bounded and runs under the same cursor transaction,
// so old rows cannot bypass the fairness policy indefinitely.
func normalizeLegacyFairnessKeys(db *gorm.DB, now time.Time, limit int) error {
	var legacy []models.OutboxEvent
	if err := readyOutboxScope(db.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("fairness_key = ''"), now).
		Order("created_at ASC, id ASC").Limit(limit).Find(&legacy).Error; err != nil {
		return err
	}
	for _, event := range legacy {
		key := fairnessKey(event)
		if err := db.Model(&models.OutboxEvent{}).Where("id = ?", event.ID).Update("fairness_key", key).Error; err != nil {
			return err
		}
	}
	return nil
}

func rotateFairnessKeys(keys []string, last string) []string {
	if len(keys) < 2 || strings.TrimSpace(last) == "" {
		return keys
	}
	// The database returns sorted keys. Keep the helper defensive for tests and
	// callers that provide an in-memory slice.
	sort.Strings(keys)
	start := sort.Search(len(keys), func(index int) bool { return keys[index] > last })
	if start == len(keys) {
		start = 0
	}
	rotated := make([]string, 0, len(keys))
	rotated = append(rotated, keys[start:]...)
	rotated = append(rotated, keys[:start]...)
	return rotated
}

func claimFairRows(tx *gorm.DB, keys []string, limit int, now time.Time, leaseUntil time.Time) ([]models.OutboxEvent, error) {
	if limit <= 0 || len(keys) == 0 {
		return nil, nil
	}
	selected := make([]models.OutboxEvent, 0, minInt(limit, len(keys)))
	for len(selected) < limit {
		progress := false
		for _, key := range keys {
			if len(selected) == limit {
				break
			}
			var event models.OutboxEvent
			result := readyOutboxScope(tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("fairness_key = ?", key), now).
				Order("created_at ASC, id ASC").Limit(1).Find(&event)
			if result.Error != nil {
				return nil, result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			updates := map[string]interface{}{
				"state":       StateProcessing,
				"lease_until": leaseUntil,
				"attempts":    gorm.Expr("attempts + 1"),
				"last_error":  "",
			}
			if err := tx.Model(&models.OutboxEvent{}).Where("id = ?", event.ID).Updates(updates).Error; err != nil {
				return nil, err
			}
			event.State = StateProcessing
			event.LeaseUntil = &leaseUntil
			event.Attempts++
			selected = append(selected, event)
			progress = true
		}
		if !progress {
			break
		}
	}
	return selected, nil
}

// fairnessNormalizationLimit bounds the one-time upgrade of legacy rows that
// predate persisted lane metadata. A dispatcher must remain cheap even when a
// tenant has a very large historical backlog.
func fairnessNormalizationLimit(limit int) int {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	return limit * 4
}

// pickFairBatch performs a deterministic round-robin over project+operation
// lanes. It is intentionally independent of the database so its starvation
// guarantees are testable without a live Postgres instance. The first pass
// gives every lane one slot; subsequent passes preserve FIFO order within a
// lane until the requested batch is full.
func pickFairBatch(candidates []models.OutboxEvent, limit int) []models.OutboxEvent {
	if limit <= 0 || len(candidates) == 0 {
		return nil
	}
	lanes := make(map[string][]models.OutboxEvent)
	order := make([]string, 0)
	for _, event := range candidates {
		key := fairnessKey(event)
		if _, exists := lanes[key]; !exists {
			order = append(order, key)
		}
		lanes[key] = append(lanes[key], event)
	}
	// The query already returns created_at/id order. Sorting the lane names
	// only makes the output stable if this helper is reused with an arbitrary
	// candidate slice in a unit test; the database FIFO order remains the tie
	// breaker inside each lane.
	if len(order) > 1 {
		sort.SliceStable(order, func(i, j int) bool { return order[i] < order[j] })
	}
	selected := make([]models.OutboxEvent, 0, minInt(limit, len(candidates)))
	for len(selected) < limit {
		progress := false
		for _, key := range order {
			queue := lanes[key]
			if len(queue) == 0 {
				continue
			}
			selected = append(selected, queue[0])
			lanes[key] = queue[1:]
			progress = true
			if len(selected) == limit {
				break
			}
		}
		if !progress {
			break
		}
	}
	return selected
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

type automationFairnessPayload struct {
	Payload struct {
		ProjectID string `json:"project_id"`
		Operation string `json:"operation"`
	} `json:"payload"`
}

func fairnessKey(event models.OutboxEvent) string {
	if event.EventType == "automation.ai.local.process" {
		var message automationFairnessPayload
		if json.Unmarshal([]byte(event.Payload), &message) == nil {
			project := strings.TrimSpace(message.Payload.ProjectID)
			operation := strings.TrimSpace(message.Payload.Operation)
			if project != "" || operation != "" {
				return "automation:" + project + ":" + operation
			}
		}
	}
	// Non-automation events and legacy payloads still get a bounded lane,
	// rather than silently creating a lane per job and defeating fairness.
	return "event:" + strings.TrimSpace(event.TenantCode) + ":" + strings.TrimSpace(event.EventType)
}

func MarkCompleted(db *gorm.DB, id interface{}) error {
	now := time.Now().UTC()
	return db.Model(&models.OutboxEvent{}).
		Where("id = ? AND state = ?", id, StateProcessing).
		Updates(map[string]interface{}{
			"state":        StateCompleted,
			"processed_at": now,
			"lease_until":  nil,
			"last_error":   "",
		}).Error
}

func ScheduleRetry(db *gorm.DB, event models.OutboxEvent, cause error) error {
	message := ""
	if cause != nil {
		message = observability.SanitizeError(cause)
	}
	return db.Model(&models.OutboxEvent{}).
		Where("id = ? AND state = ?", event.ID, StateProcessing).
		Updates(map[string]interface{}{
			"state":        StatePending,
			"available_at": time.Now().UTC().Add(retryDelay(event.Attempts)),
			"lease_until":  nil,
			"last_error":   message,
		}).Error
}

func DeleteCompletedBefore(db *gorm.DB, cutoff time.Time) error {
	return db.Where("state = ? AND processed_at < ?", StateCompleted, cutoff.UTC()).
		Delete(&models.OutboxEvent{}).Error
}

func retryDelay(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return 2 * time.Second
	case attempts == 2:
		return 10 * time.Second
	case attempts == 3:
		return 30 * time.Second
	default:
		return 2 * time.Minute
	}
}
