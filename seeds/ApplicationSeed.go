package seeds

import (
	"events-stocks/internal/products"
	"events-stocks/models"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SeedApplications publishes the product catalog and reconciles the current
// organization memberships into explicit per-application entitlements.
func SeedApplications(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		for _, definition := range products.All() {
			application := models.Application{
				Code:                definition.Code.String(),
				Name:                definition.Name,
				ProductLabel:        definition.ProductLabel,
				Modules:             models.StringList(definition.Modules),
				AllowsPlatformAdmin: definition.AllowsPlatformAuthority,
				IsActive:            true,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "code"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"name", "product_label", "modules", "allows_platform_admin", "is_active", "updated_at",
				}),
			}).Create(&application).Error; err != nil {
				return fmt.Errorf("upsert application %s: %w", definition.Code, err)
			}

			var root models.Client
			if err := tx.Where("LOWER(code) = ?", strings.ToLower(definition.Code.String())).First(&root).Error; err != nil {
				return fmt.Errorf("load application root %s: %w", definition.Code, err)
			}
			// A new brand root is enabled once. An existing row keeps its
			// is_active flag, so a product disabled on purpose stays disabled
			// across restarts; only the published module list is refreshed.
			assignment := models.ClientApplication{
				ClientID: root.ID, ApplicationID: application.ID,
				Modules: definition.Modules, IsActive: true,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "client_id"}, {Name: "application_id"}},
				DoUpdates: clause.AssignmentColumns(brandRootApplicationUpdateColumns),
			}).Create(&assignment).Error; err != nil {
				return fmt.Errorf("enable application %s: %w", definition.Code, err)
			}
		}

		// Pick the nearest branded ancestor for every existing membership. This
		// keeps Cafetton/ITBEM members inside their portal while normal customer
		// organizations under the platform remain EventiApp members. Existing
		// rows are never touched: a disabled entitlement stays disabled.
		if err := tx.Exec(memberApplicationBackfillSQL).Error; err != nil {
			return fmt.Errorf("backfill application memberships: %w", err)
		}
		return nil
	})
}

// brandRootApplicationUpdateColumns deliberately omits is_active: the seed
// runs on every boot and must not re-enable a product an operator disabled.
var brandRootApplicationUpdateColumns = []string{"modules", "updated_at"}

// memberApplicationBackfillSQL only inserts missing entitlements. Updating an
// existing row would re-enable access that was disabled on purpose
// (models.Client AfterCreate still upserts: a new membership is an explicit
// grant).
const memberApplicationBackfillSQL = `
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
			DO NOTHING
`
