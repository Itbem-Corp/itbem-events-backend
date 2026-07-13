package seeds

import (
	"fmt"

	"events-stocks/internal/phrasecatalog"
	"events-stocks/models"
	"gorm.io/gorm"
)

// SeedEventPhrases is additive: it preserves every production row and inserts
// only entries missing from the versioned wedding catalog.
func SeedEventPhrases(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("event phrase database is not initialized")
	}
	var existing []string
	if err := db.Model(&models.EventPhrase{}).
		Where("event_type = ?", "WEDDING").
		Pluck("phrase", &existing).Error; err != nil {
		return fmt.Errorf("load existing wedding phrases: %w", err)
	}

	seen := make(map[string]struct{}, len(existing))
	for _, phrase := range existing {
		seen[phrase] = struct{}{}
	}
	missing := make([]models.EventPhrase, 0)
	for _, phrase := range phrasecatalog.Wedding() {
		if _, ok := seen[phrase]; ok {
			continue
		}
		missing = append(missing, models.EventPhrase{EventType: "WEDDING", Phrase: phrase})
	}
	if len(missing) == 0 {
		return nil
	}
	if err := db.CreateInBatches(&missing, 100).Error; err != nil {
		return fmt.Errorf("insert %d missing wedding phrases: %w", len(missing), err)
	}
	return nil
}
