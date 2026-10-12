package clientrepository

import (
	"strings"

	"events-stocks/repositories/gormrepository"

	"github.com/gofrs/uuid"
)

// CheckAccessRecursiveE resolves the caller's direct or inherited role on
// targetClientID with one recursive CTE and returns the scan error instead of
// swallowing it. Only Owner/Admin propagate down the organization tree.
func CheckAccessRecursiveE(userID, targetClientID uuid.UUID) (bool, string, error) {
	type result struct {
		RoleCode string
		Level    int
	}
	var row result

	err := gormrepository.DB().Raw(`
		WITH RECURSIVE anc AS (
			SELECT id, parent_id, 0 AS level
			FROM clients
			WHERE id = ? AND deleted_at IS NULL
			UNION ALL
			SELECT c.id, c.parent_id, a.level + 1
			FROM clients c
			INNER JOIN anc a ON c.id = a.parent_id
			WHERE c.deleted_at IS NULL AND a.level < 31
		)
		SELECT cr.code AS role_code, anc.level
		FROM anc
		JOIN client_members cm ON cm.client_id = anc.id
			AND cm.user_id = ?
			AND cm.is_active = true
		JOIN client_roles cr ON cr.id = cm.client_role_id
		ORDER BY anc.level ASC
		LIMIT 1
	`, targetClientID, userID).Scan(&row).Error
	if err != nil {
		return false, "", err
	}
	if row.RoleCode == "" {
		return false, "", nil
	}
	if row.Level == 0 {
		return true, row.RoleCode, nil
	}
	roleUpper := strings.ToUpper(row.RoleCode)
	if roleUpper == "OWNER" || roleUpper == "ADMIN" {
		return true, "INHERITED_" + roleUpper, nil
	}
	return false, "", nil
}

// ResolveClientProduct returns the lower-case application code of the nearest
// organization in the ancestry (itself included) whose code names an active
// application: the nearest-branded rule of models.ClientMember.AfterCreate.
// It returns "" when no ancestor is branded.
func ResolveClientProduct(clientID uuid.UUID) (string, error) {
	var code string
	err := gormrepository.DB().Raw(`
		WITH RECURSIVE ancestry AS (
			SELECT id, parent_id, code, 0 AS depth
			FROM clients
			WHERE id = ? AND deleted_at IS NULL
			UNION ALL
			SELECT parent.id, parent.parent_id, parent.code, ancestry.depth + 1
			FROM ancestry
			JOIN clients parent ON parent.id = ancestry.parent_id
			WHERE parent.deleted_at IS NULL AND ancestry.depth < 31
		)
		SELECT COALESCE((
			SELECT LOWER(applications.code)
			FROM ancestry
			JOIN applications ON LOWER(applications.code) = LOWER(ancestry.code)
			WHERE applications.is_active = true
			ORDER BY ancestry.depth ASC
			LIMIT 1
		), '')
	`, clientID).Row().Scan(&code)
	if err != nil {
		return "", err
	}
	return code, nil
}
