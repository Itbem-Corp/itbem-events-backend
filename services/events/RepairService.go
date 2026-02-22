package events

import (
	"fmt"
	"time"

	"events-stocks/models"
	"events-stocks/repositories/eventsrepository"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

type RepairResult struct {
	Repaired bool     `json:"repaired"`
	Fixes    []string `json:"fixes"`
	Warnings []string `json:"warnings"`
}

func RepairEvent(db *gorm.DB, eventID uuid.UUID) (*RepairResult, error) {
	result := &RepairResult{}

	// Load event with preloads
	var event models.Event
	if err := db.Preload("EventType").Preload("EventConfig").Preload("EventConfig.DesignTemplate").
		First(&event, "id = ?", eventID).Error; err != nil {
		return nil, fmt.Errorf("event not found: %w", err)
	}

	tx := db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// ── Tier 1: Missing records ──────────────────────────────────────────

	// 1a. EventConfig missing
	var configCount int64
	tx.Model(&models.EventConfig{}).Where("id = ?", eventID).Count(&configCount)
	if configCount == 0 {
		cfg := &models.EventConfig{ID: eventID}
		if err := tx.Create(cfg).Error; err == nil {
			result.Fixes = append(result.Fixes, "created missing event_config")
		}
	}

	// 1b. EventAnalytics missing
	var analyticsCount int64
	tx.Model(&models.EventAnalytics{}).Where("event_id = ?", eventID).Count(&analyticsCount)
	if analyticsCount == 0 {
		a := &models.EventAnalytics{EventID: eventID}
		if err := tx.Create(a).Error; err == nil {
			result.Fixes = append(result.Fixes, "created missing event_analytics")
		}
	}

	// 1c. EventType FK invalid
	if event.EventType.ID == uuid.Nil {
		var firstType models.EventType
		if err := tx.First(&firstType).Error; err == nil {
			tx.Model(&event).Update("event_type_id", firstType.ID)
			result.Fixes = append(result.Fixes, fmt.Sprintf("reassigned event_type to '%s'", firstType.Name))
		}
	}

	// ── Tier 2: Invalid field values ─────────────────────────────────────

	updates := map[string]interface{}{}

	if event.Identifier == "" {
		if event.Name != "" {
			newSlug := utils.Slugify(event.Name)
			if newSlug == "" {
				newSlug = "event"
			}
			candidate := newSlug
			for i := 2; eventsrepository.IdentifierExists(candidate); i++ {
				candidate = fmt.Sprintf("%s-%d", newSlug, i)
			}
			updates["identifier"] = candidate
			result.Fixes = append(result.Fixes, fmt.Sprintf("regenerated identifier: '%s'", candidate))
		} else {
			result.Warnings = append(result.Warnings, "event name is empty — cannot generate identifier")
		}
	}

	if event.Name == "" {
		result.Warnings = append(result.Warnings, "event name is empty — cannot auto-repair")
	}

	if event.Timezone == "" {
		updates["timezone"] = "America/Mexico_City"
		result.Fixes = append(result.Fixes, "set timezone to 'America/Mexico_City'")
	}

	if event.EventDateTime.IsZero() || event.EventDateTime.Year() <= 1970 {
		future := time.Now().AddDate(0, 0, 30)
		updates["event_date_time"] = future
		result.Fixes = append(result.Fixes, "set event_date_time to now+30d placeholder")
	}

	if event.Language == "" {
		updates["language"] = "es"
		result.Fixes = append(result.Fixes, "set language to 'es'")
	}

	if len(updates) > 0 {
		tx.Model(&event).Updates(updates)
	}

	// ── Tier 3: Inconsistent relations ───────────────────────────────────

	// 3a. Config design_template_id points to nonexistent template
	var config models.EventConfig
	if err := tx.Preload("DesignTemplate").First(&config, "id = ?", eventID).Error; err == nil {
		if config.DesignTemplateID != nil && *config.DesignTemplateID != uuid.Nil {
			var tplCount int64
			tx.Model(&models.DesignTemplate{}).Where("id = ?", *config.DesignTemplateID).Count(&tplCount)
			if tplCount == 0 {
				tx.Model(&config).Update("design_template_id", nil)
				result.Fixes = append(result.Fixes, "cleared orphaned design_template_id")
			}
		}
	}

	// 3b. Guests with zero-UUID guest_status_id
	var pendingStatus models.GuestStatus
	if err := tx.Where("UPPER(code) = ?", "PENDING").First(&pendingStatus).Error; err == nil {
		zeroUUID := uuid.Nil
		res := tx.Model(&models.Guest{}).
			Where("event_id = ? AND guest_status_id = ?", eventID, zeroUUID).
			Update("guest_status_id", pendingStatus.ID)
		if res.RowsAffected > 0 {
			result.Fixes = append(result.Fixes, fmt.Sprintf("fixed %d guests with zero guest_status_id", res.RowsAffected))
		}
	}

	// 3c. Invitations with max_guests = 0
	res := tx.Model(&models.Invitation{}).
		Where("event_id = ? AND max_guests = 0", eventID).
		Update("max_guests", 1)
	if res.RowsAffected > 0 {
		result.Fixes = append(result.Fixes, fmt.Sprintf("set max_guests=1 on %d invitations", res.RowsAffected))
	}

	// ── Tier 4: Stuck moments ────────────────────────────────────────────

	thirtyMinAgo := time.Now().Add(-30 * time.Minute)
	oneHourAgo := time.Now().Add(-1 * time.Hour)

	res = tx.Model(&models.Moment{}).
		Where("event_id = ? AND processing_status = ? AND updated_at < ?", eventID, "processing", thirtyMinAgo).
		Update("processing_status", "failed")
	if res.RowsAffected > 0 {
		result.Fixes = append(result.Fixes, fmt.Sprintf("marked %d stuck 'processing' moments as 'failed'", res.RowsAffected))
	}

	res = tx.Model(&models.Moment{}).
		Where("event_id = ? AND processing_status = ? AND created_at < ?", eventID, "pending", oneHourAgo).
		Update("processing_status", "failed")
	if res.RowsAffected > 0 {
		result.Fixes = append(result.Fixes, fmt.Sprintf("marked %d stuck 'pending' moments as 'failed'", res.RowsAffected))
	}

	// ── Commit ───────────────────────────────────────────────────────────

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("repair transaction failed: %w", err)
	}

	result.Repaired = len(result.Fixes) > 0
	return result, nil
}
