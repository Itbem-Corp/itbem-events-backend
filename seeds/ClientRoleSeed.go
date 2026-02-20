package seeds

import (
	"events-stocks/models"
	"gorm.io/gorm"
	"log/slog"
)

func SeedClientRoles(db *gorm.DB) {
	roles := []models.ClientRole{
		{
			Name: "Owner", Code: "Owner", Hierarchy: 1, // 👑 Manda a todos
			Description: "Total Access...",
		},
		{
			Name: "Administrator", Code: "Admin", Hierarchy: 2, // 🥈 Debajo del Owner
			Description: "Operative Management...",
		},
		{
			Name: "Member", Code: "Member", Hierarchy: 3, // 🥉
			Description: "Standard Access...",
		},
		{
			Name: "Guest", Code: "Guest", Hierarchy: 4, // 🥔
			Description: "Read Only...",
		},
	}

	for _, r := range roles {
		var count int64
		db.Model(&models.ClientRole{}).Where("code = ?", r.Code).Count(&count)
		if count == 0 {
			db.Create(&r)
			slog.Info("role seeded", "code", r.Code)
		}
	}
}
