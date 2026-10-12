package clientrepository

import (
	"events-stocks/repositories/gormrepository"

	"github.com/gofrs/uuid"
)

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
