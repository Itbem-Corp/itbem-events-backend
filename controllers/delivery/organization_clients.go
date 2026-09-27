package delivery

import (
	"events-stocks/configuration"
	"events-stocks/internal/organizationscope"

	"github.com/gofrs/uuid"
)

var errDeliveryOrganizationNotFound = organizationscope.ErrOrganizationNotFound

// deliveryOrganizationClientIDs returns the selected organization client and
// every active descendant in the Client.ParentID tree. UNION deduplicates IDs,
// which also makes the traversal terminate if malformed data contains a cycle.
func deliveryOrganizationClientIDs(organizationID uuid.UUID) ([]uuid.UUID, error) {
	return organizationscope.ClientIDs(configuration.DB, organizationID)
}

func deliveryOrganizationContainsClient(clientIDs []uuid.UUID, clientID uuid.UUID) bool {
	for _, candidate := range clientIDs {
		if candidate == clientID {
			return true
		}
	}
	return false
}
