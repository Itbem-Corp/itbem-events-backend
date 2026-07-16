package outboxrepository

import (
	"time"

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
	if event.State == "" {
		event.State = StatePending
	}
	if event.AvailableAt.IsZero() {
		event.AvailableAt = time.Now().UTC()
	}
	result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(event)
	return result.RowsAffected == 1, result.Error
}

// ClaimBatch leases ready rows using SKIP LOCKED, allowing multiple API
// instances to dispatch concurrently without selecting the same row.
func ClaimBatch(db *gorm.DB, limit int, lease time.Duration) ([]models.OutboxEvent, error) {
	if limit <= 0 {
		limit = 10
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(lease)
	var events []models.OutboxEvent
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where(
				"(state = ? AND available_at <= ?) OR (state = ? AND lease_until < ?)",
				StatePending, now, StateProcessing, now,
			).
			Order("created_at ASC").
			Limit(limit).
			Find(&events).Error; err != nil {
			return err
		}
		for index := range events {
			result := tx.Model(&models.OutboxEvent{}).
				Where("id = ?", events[index].ID).
				Updates(map[string]interface{}{
					"state":       StateProcessing,
					"lease_until": leaseUntil,
					"attempts":    gorm.Expr("attempts + 1"),
					"last_error":  "",
				})
			if result.Error != nil {
				return result.Error
			}
			events[index].State = StateProcessing
			events[index].LeaseUntil = &leaseUntil
			events[index].Attempts++
		}
		return nil
	})
	return events, err
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
		message = cause.Error()
		if len(message) > 1024 {
			message = message[:1024]
		}
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
