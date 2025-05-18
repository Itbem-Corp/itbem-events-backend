package seeds

import (
	"log"
	"time"

	"events-stocks/models"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

func SeedEventType(db *gorm.DB) {
	items := []string{"wedding", "graduation", "birthday"}

	for _, name := range items {
		var existing models.EventType
		if err := db.Where("name = ?", name).First(&existing).Error; err == gorm.ErrRecordNotFound {
			entry := models.EventType{
				ID:        uuid.Must(uuid.NewV4()),
				Name:      name,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			if err := db.Create(&entry).Error; err != nil {
				log.Printf("Error seeding EventType '%s': %v", name, err)
			}
		}
	}
}
