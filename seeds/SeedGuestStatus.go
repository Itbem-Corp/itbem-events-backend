package seeds

import (
	"log"
	"time"

	"events-stocks/models"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

func SeedGuestStatus(db *gorm.DB) {
	statuses := []struct {
		Code  string
		Label string
		Color string
		Order int
	}{
		{"pending", "Pending", "#facc15", 1},     // amarillo
		{"confirmed", "Confirmed", "#22c55e", 2}, // verde
		{"declined", "Declined", "#ef4444", 3},   // rojo
	}

	for _, s := range statuses {
		var existing models.GuestStatus
		if err := db.Where("code = ?", s.Code).First(&existing).Error; err == gorm.ErrRecordNotFound {
			entry := models.GuestStatus{
				ID:        uuid.Must(uuid.NewV4()),
				Code:      s.Code,
				Label:     s.Label,
				Color:     s.Color,
				Order:     s.Order,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			if err := db.Create(&entry).Error; err != nil {
				log.Printf("Error seeding GuestStatus '%s': %v", s.Code, err)
			}
		}
	}
}
