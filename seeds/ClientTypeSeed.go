package seeds

import (
	"events-stocks/models"
	"gorm.io/gorm"
	"log/slog"
)

func SeedClientTypes(db *gorm.DB) {
	types := []models.ClientType{
		{
			Name:        "Platform",
			Code:        models.ClientTypeCodePlatform,
			Level:       1, // 👑 Nivel Supremo
			Description: "System Root",
		},
		{
			Name:        "Agency",
			Code:        models.ClientTypeCodeAgency,
			Level:       2, // 🥈 Intermediario
			Description: "Reseller Root",
		},
		{
			Name:        "Customer",
			Code:        models.ClientTypeCodeCustomer,
			Level:       3, // 🥉 Usuario Final
			Description: "Customer Root",
		},
	}

	for _, t := range types {
		var count int64
		// Buscamos por Code para evitar duplicados
		db.Model(&models.ClientType{}).Where("code = ?", t.Code).Count(&count)
		if count == 0 {
			db.Create(&t)
			slog.Info("client type seeded", "name", t.Name, "level", t.Level)
		}
	}
}
