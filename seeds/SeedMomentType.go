package seeds

import (
	"log/slog"
	"time"

	"events-stocks/models"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

func SeedMomentType(db *gorm.DB) {
	items := []string{"message", "photo", "video"}

	for _, name := range items {
		var existing models.MomentType
		if err := db.Where("name = ?", name).First(&existing).Error; err == gorm.ErrRecordNotFound {
			entry := models.MomentType{
				ID:        uuid.Must(uuid.NewV4()),
				Name:      name,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			if err := db.Create(&entry).Error; err != nil {
				slog.Error("error seeding moment type", "name", name, "error", err)
			}
		}
	}
}
