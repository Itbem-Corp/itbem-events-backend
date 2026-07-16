package seeds

import (
	"events-stocks/models"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var firstPartyApplications = []models.Application{
	{
		Code: "eventiapp", Name: "EventiApp", ProductLabel: "Event operations",
		Modules:             models.StringList{"home", "events"},
		AllowsPlatformAdmin: true, IsActive: true,
	},
	{
		Code: "itbem", Name: "ITBEM", ProductLabel: "Platform control plane",
		Modules:             models.StringList{"home", "users", "organizations"},
		AllowsPlatformAdmin: true, IsActive: true,
	},
	{
		Code: "cafettonhouse", Name: "Cafetton House", ProductLabel: "Client operations",
		Modules:             models.StringList{"home", "users", "organizations"},
		AllowsPlatformAdmin: false, IsActive: true,
	},
}

// SeedApplications publishes the product catalog and reconciles the current
// organization memberships into explicit per-application entitlements.
func SeedApplications(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		for _, definition := range firstPartyApplications {
			application := definition
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "code"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"name", "product_label", "modules", "allows_platform_admin", "is_active", "updated_at",
				}),
			}).Create(&application).Error; err != nil {
				return fmt.Errorf("upsert application %s: %w", definition.Code, err)
			}

			var root models.Client
			if err := tx.Where("LOWER(code) = ?", strings.ToLower(definition.Code)).First(&root).Error; err != nil {
				return fmt.Errorf("load application root %s: %w", definition.Code, err)
			}
			assignment := models.ClientApplication{
				ClientID: root.ID, ApplicationID: application.ID,
				Modules: definition.Modules, IsActive: true,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "client_id"}, {Name: "application_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"modules", "is_active", "updated_at"}),
			}).Create(&assignment).Error; err != nil {
				return fmt.Errorf("enable application %s: %w", definition.Code, err)
			}
		}

		// Pick the nearest branded ancestor for every existing membership. This
		// keeps Cafetton/ITBEM members inside their portal while normal customer
		// organizations under the platform remain EventiApp members.
		if err := tx.Exec(`
			WITH RECURSIVE ancestry AS (
				SELECT cm.id AS member_id, c.id AS client_id, c.parent_id, 0 AS depth
				FROM client_members cm
				JOIN clients c ON c.id = cm.client_id
				WHERE cm.is_active = true AND c.deleted_at IS NULL
				UNION ALL
				SELECT ancestry.member_id, parent.id, parent.parent_id, ancestry.depth + 1
				FROM ancestry
				JOIN clients parent ON parent.id = ancestry.parent_id
				WHERE parent.deleted_at IS NULL AND ancestry.depth < 31
			),
			resolved AS (
				SELECT DISTINCT ON (ancestry.member_id)
					ancestry.member_id,
					applications.id AS application_id
				FROM ancestry
				JOIN clients ON clients.id = ancestry.client_id
				JOIN applications ON LOWER(applications.code) = LOWER(clients.code)
				WHERE applications.is_active = true
				ORDER BY ancestry.member_id, ancestry.depth ASC
			)
			INSERT INTO client_member_applications
				(id, client_member_id, application_id, is_active, created_at, updated_at)
			SELECT uuid_generate_v4(), member_id, application_id, true, NOW(), NOW()
			FROM resolved
			ON CONFLICT (client_member_id, application_id)
			DO UPDATE SET is_active = true, updated_at = NOW()
		`).Error; err != nil {
			return fmt.Errorf("backfill application memberships: %w", err)
		}
		return nil
	})
}
