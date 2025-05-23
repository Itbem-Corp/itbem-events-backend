package seeds

import (
	"log"
	"time"

	"events-stocks/models"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

func SeedResourceTypes(db *gorm.DB) {
	items := []struct {
		Code  string
		Label string
	}{
		{"image", "Image"},
		{"video", "Video"},
		{"audio", "Audio"},
		{"file", "File"},
		{"font", "Font"},
	}

	for _, item := range items {
		var existing models.ResourceType
		if err := db.Where("code = ?", item.Code).First(&existing).Error; err == gorm.ErrRecordNotFound {
			entry := models.ResourceType{
				ID:        uuid.Must(uuid.NewV4()),
				Code:      item.Code,
				Label:     item.Label,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			if err := db.Create(&entry).Error; err != nil {
				log.Printf("Error seeding ResourceType '%s': %v", item.Code, err)
			}
		}
	}
}
