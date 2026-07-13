package seeds

import (
	"events-stocks/models"
	"log/slog"

	"gorm.io/gorm"
)

// SeedClientRoles defines the fixed organization roles used by authorization.
// Hierarchy is intentionally monotonic: a role can only delegate to a role
// with a greater hierarchy number.
func SeedClientRoles(db *gorm.DB) {
	roles := []models.ClientRole{
		{Name: "Owner", Code: "Owner", Hierarchy: 1, Description: "Owns the organization and its hierarchy."},
		{Name: "Administrator", Code: "Admin", Hierarchy: 2, Description: "Manages members and organization operations."},
		{Name: "Event manager", Code: "EVENT_MANAGER", Hierarchy: 3, Description: "Creates and operates events."},
		{Name: "Editor", Code: "EDITOR", Hierarchy: 4, Description: "Edits event and guest content."},
		{Name: "Check-in", Code: "CHECKIN", Hierarchy: 5, Description: "Runs guest check-in and RSVP operations only."},
		{Name: "Analyst", Code: "ANALYST", Hierarchy: 6, Description: "Views event and guest analytics without changing operational data."},
		{Name: "Member", Code: "Member", Hierarchy: 7, Description: "Standard event collaborator."},
		{Name: "Viewer", Code: "Guest", Hierarchy: 8, Description: "Read-only access."},
	}

	for _, role := range roles {
		var count int64
		db.Model(&models.ClientRole{}).Where("code = ?", role.Code).Count(&count)
		if count == 0 {
			db.Create(&role)
			slog.Info("role seeded", "code", role.Code)
		}
	}
}
