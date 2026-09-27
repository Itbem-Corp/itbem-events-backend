package organizationscope

import (
	"errors"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

// ErrOrganizationNotFound indicates that the selected organization does not
// resolve to a non-deleted client record.
var ErrOrganizationNotFound = errors.New("organization not found")

// ClientIDs returns the selected organization client and every non-deleted
// client below it in the Client.ParentID hierarchy. UNION deduplicates IDs, which
// also makes traversal terminate if malformed data contains a cycle.
func ClientIDs(db *gorm.DB, organizationID uuid.UUID) ([]uuid.UUID, error) {
	if organizationID == uuid.Nil {
		return nil, ErrOrganizationNotFound
	}
	if db == nil {
		return nil, errors.New("organization scope database is unavailable")
	}
	var clientIDs []uuid.UUID
	err := db.Raw(`
		WITH RECURSIVE organization_clients(id) AS (
			SELECT clients.id
			FROM clients
			WHERE clients.id = ? AND clients.deleted_at IS NULL
			UNION
			SELECT child.id
			FROM clients AS child
			JOIN organization_clients AS parent ON child.parent_id = parent.id
			WHERE child.deleted_at IS NULL
		)
		SELECT id FROM organization_clients
	`, organizationID).Scan(&clientIDs).Error
	if err != nil {
		return nil, err
	}
	if len(clientIDs) == 0 {
		return nil, ErrOrganizationNotFound
	}
	return clientIDs, nil
}
