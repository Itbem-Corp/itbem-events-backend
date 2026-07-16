package seeds

import (
	"events-stocks/models"
	"log/slog"

	"gorm.io/gorm"
)

// SeedClientEventiAppSeed guarantees the stable first-party organizations.
// FirstOrCreate is intentionally additive: an existing row keeps its branding,
// type, and hierarchy instead of being silently rewritten during a deployment.
func SeedClientEventiAppSeed(db *gorm.DB) {
	var platformType models.ClientType
	if err := db.Where("code = ?", models.ClientTypeCodePlatform).First(&platformType).Error; err != nil {
		slog.Error("platform client type unavailable", "error", err)
		return
	}

	root := models.Client{
		Name: "EventiApp", Code: "eventiapp", ClientTypeID: platformType.ID, IsActive: true,
	}
	if err := db.Where(models.Client{Code: root.Code}).FirstOrCreate(&root).Error; err != nil {
		slog.Error("failed to seed EventiApp organization", "error", err)
		return
	}

	var agencyType models.ClientType
	if err := db.Where("code = ?", models.ClientTypeCodeAgency).First(&agencyType).Error; err != nil {
		slog.Error("agency client type unavailable", "error", err)
		return
	}
	itbem := models.Client{
		Name: "ITBEM", Code: "itbem", ClientTypeID: agencyType.ID, ParentID: &root.ID, IsActive: true,
	}
	if err := db.Where(models.Client{Code: itbem.Code}).FirstOrCreate(&itbem).Error; err != nil {
		slog.Error("failed to seed ITBEM organization", "error", err)
	}
	cafettonhouse := models.Client{
		Name: "Cafetton House", Code: "cafettonhouse", ClientTypeID: agencyType.ID, ParentID: &root.ID, IsActive: true,
	}
	if err := db.Where(models.Client{Code: cafettonhouse.Code}).FirstOrCreate(&cafettonhouse).Error; err != nil {
		slog.Error("failed to seed Cafetton House organization", "error", err)
	}
}
