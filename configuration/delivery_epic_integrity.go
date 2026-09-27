package configuration

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// migrateDeliveryEpicMembershipIntegrity adds database-enforced project
// affinity and one-active-epic-per-work-item semantics. It deliberately
// refuses to repair legacy rows: operators must review and reconcile any
// reported IDs before retrying the migration.
func migrateDeliveryEpicMembershipIntegrity(tx *gorm.DB) error {
	// Serialize with the delivery API's epic -> work item -> membership write
	// order. The migration transaction already sets a short lock_timeout, so a
	// busy deployment aborts instead of holding API writes indefinitely.
	if err := tx.Exec(`LOCK TABLE delivery_epics, delivery_work_items, delivery_epic_work_items IN SHARE ROW EXCLUSIVE MODE`).Error; err != nil {
		return fmt.Errorf("lock delivery epic membership tables: %w", err)
	}
	if err := validateDeliveryEpicMembershipLegacyData(tx); err != nil {
		return err
	}

	indexes := []struct {
		name      string
		statement string
		fragment  string
	}{
		{
			name:      "uidx_delivery_epics_project_id_id",
			statement: `CREATE UNIQUE INDEX IF NOT EXISTS uidx_delivery_epics_project_id_id ON public.delivery_epics (project_id, id)`,
			fragment:  "on public.delivery_epics using btree (project_id, id)",
		},
		{
			name:      "uidx_delivery_work_items_project_id_id",
			statement: `CREATE UNIQUE INDEX IF NOT EXISTS uidx_delivery_work_items_project_id_id ON public.delivery_work_items (project_id, id)`,
			fragment:  "on public.delivery_work_items using btree (project_id, id)",
		},
		{
			name:      "uidx_delivery_epic_work_items_active_work_item",
			statement: `CREATE UNIQUE INDEX IF NOT EXISTS uidx_delivery_epic_work_items_active_work_item ON public.delivery_epic_work_items (work_item_id) WHERE deleted_at IS NULL`,
			fragment:  "on public.delivery_epic_work_items using btree (work_item_id) where (deleted_at is null)",
		},
	}
	for _, index := range indexes {
		if err := ensureDeliveryEpicIntegrityIndex(tx, index.name, index.statement, index.fragment); err != nil {
			return err
		}
	}

	foreignKeys := []struct {
		table     string
		name      string
		expected  string
		statement string
	}{
		{
			table:     "delivery_epics",
			name:      "fk_delivery_epics_project",
			expected:  "foreign key (project_id) references delivery_projects(id) on update cascade on delete restrict",
			statement: `ALTER TABLE public.delivery_epics ADD CONSTRAINT fk_delivery_epics_project FOREIGN KEY (project_id) REFERENCES public.delivery_projects(id) ON UPDATE CASCADE ON DELETE RESTRICT`,
		},
		{
			table:     "delivery_epic_work_items",
			name:      "fk_delivery_epic_work_items_project_epic",
			expected:  "foreign key (project_id, epic_id) references delivery_epics(project_id, id) on update cascade on delete restrict",
			statement: `ALTER TABLE public.delivery_epic_work_items ADD CONSTRAINT fk_delivery_epic_work_items_project_epic FOREIGN KEY (project_id, epic_id) REFERENCES public.delivery_epics(project_id, id) ON UPDATE CASCADE ON DELETE RESTRICT`,
		},
		{
			table:     "delivery_epic_work_items",
			name:      "fk_delivery_epic_work_items_project_work_item",
			expected:  "foreign key (project_id, work_item_id) references delivery_work_items(project_id, id) on update cascade on delete restrict",
			statement: `ALTER TABLE public.delivery_epic_work_items ADD CONSTRAINT fk_delivery_epic_work_items_project_work_item FOREIGN KEY (project_id, work_item_id) REFERENCES public.delivery_work_items(project_id, id) ON UPDATE CASCADE ON DELETE RESTRICT`,
		},
	}
	for _, foreignKey := range foreignKeys {
		if err := ensureDeliveryEpicIntegrityForeignKey(tx, foreignKey.table, foreignKey.name, foreignKey.expected, foreignKey.statement); err != nil {
			return err
		}
	}
	return nil
}

func validateDeliveryEpicMembershipLegacyData(tx *gorm.DB) error {
	var epicIssue struct {
		EpicID    string `gorm:"column:epic_id"`
		ProjectID string `gorm:"column:project_id"`
	}
	result := tx.Raw(`
		SELECT epic.id::text AS epic_id, epic.project_id::text AS project_id
		FROM delivery_epics AS epic
		LEFT JOIN delivery_projects AS project ON project.id = epic.project_id
		WHERE project.id IS NULL
		ORDER BY epic.id
		LIMIT 1`).Scan(&epicIssue)
	if result.Error != nil {
		return fmt.Errorf("check legacy epic projects: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return fmt.Errorf("legacy delivery epic membership integrity violation: epic %s references missing project %s; reconcile legacy data before retrying (no rows were changed)", epicIssue.EpicID, epicIssue.ProjectID)
	}

	var membershipIssue struct {
		MembershipID string `gorm:"column:membership_id"`
		ProjectID    string `gorm:"column:project_id"`
		EpicID       string `gorm:"column:epic_id"`
		WorkItemID   string `gorm:"column:work_item_id"`
	}
	result = tx.Raw(`
		SELECT membership.id::text AS membership_id,
		       membership.project_id::text AS project_id,
		       membership.epic_id::text AS epic_id,
		       membership.work_item_id::text AS work_item_id
		FROM delivery_epic_work_items AS membership
		LEFT JOIN delivery_epics AS epic ON epic.id = membership.epic_id
		LEFT JOIN delivery_work_items AS work_item ON work_item.id = membership.work_item_id
		WHERE epic.id IS NULL
		   OR work_item.id IS NULL
		   OR epic.project_id IS DISTINCT FROM membership.project_id
		   OR work_item.project_id IS DISTINCT FROM membership.project_id
		ORDER BY membership.id
		LIMIT 1`).Scan(&membershipIssue)
	if result.Error != nil {
		return fmt.Errorf("check legacy epic memberships: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return fmt.Errorf("legacy delivery epic membership integrity violation: membership %s has project=%s, epic=%s, work_item=%s with a missing or mismatched project; reconcile legacy data before retrying (no rows were changed)", membershipIssue.MembershipID, membershipIssue.ProjectID, membershipIssue.EpicID, membershipIssue.WorkItemID)
	}

	var duplicate struct {
		WorkItemID string `gorm:"column:work_item_id"`
		Count      int64  `gorm:"column:membership_count"`
	}
	result = tx.Raw(`
		SELECT work_item_id::text AS work_item_id, count(*) AS membership_count
		FROM delivery_epic_work_items
		WHERE deleted_at IS NULL
		GROUP BY work_item_id
		HAVING count(*) > 1
		ORDER BY work_item_id
		LIMIT 1`).Scan(&duplicate)
	if result.Error != nil {
		return fmt.Errorf("check duplicate active epic memberships: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return fmt.Errorf("legacy delivery epic membership integrity violation: work item %s has %d active epic memberships; reconcile legacy data before retrying (no rows were changed)", duplicate.WorkItemID, duplicate.Count)
	}
	return nil
}

func ensureDeliveryEpicIntegrityIndex(tx *gorm.DB, name, statement, expectedFragment string) error {
	if err := tx.Exec(statement).Error; err != nil {
		return fmt.Errorf("create delivery epic integrity index %s: %w", name, err)
	}
	var definition string
	err := tx.Raw(`
		SELECT pg_get_indexdef(index_relation.oid)
		FROM pg_class AS index_relation
		JOIN pg_namespace AS index_schema ON index_schema.oid = index_relation.relnamespace
		WHERE index_schema.nspname = 'public'
		  AND index_relation.relname = ?
		  AND index_relation.relkind = 'i'`, name).Row().Scan(&definition)
	if err != nil {
		return fmt.Errorf("verify delivery epic integrity index %s: %w", name, err)
	}
	normalized := normalizeDeliveryEpicDDL(definition)
	if !strings.Contains(normalized, "create unique index "+strings.ToLower(name)+" ") || !strings.Contains(normalized, expectedFragment) {
		return fmt.Errorf("delivery epic integrity index %s already exists with an unexpected definition", name)
	}
	return nil
}

func ensureDeliveryEpicIntegrityForeignKey(tx *gorm.DB, table, name, expected, statement string) error {
	definition, validated, found, err := deliveryEpicForeignKeyDefinition(tx, table, name)
	if err != nil {
		return fmt.Errorf("inspect delivery epic integrity constraint %s: %w", name, err)
	}
	if !found {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("create delivery epic integrity constraint %s: %w", name, err)
		}
		definition, validated, found, err = deliveryEpicForeignKeyDefinition(tx, table, name)
		if err != nil {
			return fmt.Errorf("verify delivery epic integrity constraint %s: %w", name, err)
		}
	}
	if !found || !validated || normalizeDeliveryEpicDDL(definition) != expected {
		return fmt.Errorf("delivery epic integrity constraint %s exists with an unexpected or unvalidated definition", name)
	}
	return nil
}

func deliveryEpicForeignKeyDefinition(tx *gorm.DB, table, name string) (definition string, validated, found bool, err error) {
	err = tx.Raw(`
		SELECT pg_get_constraintdef(oid), convalidated
		FROM pg_constraint
		WHERE conrelid = to_regclass(?) AND conname = ? AND contype = 'f'`, "public."+table, name).Row().Scan(&definition, &validated)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	return definition, validated, true, nil
}

func normalizeDeliveryEpicDDL(definition string) string {
	return strings.ToLower(strings.Join(strings.Fields(definition), " "))
}
