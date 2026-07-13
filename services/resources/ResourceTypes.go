package services

import (
	"context"
	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/utils"
	"fmt"

	"github.com/gofrs/uuid"
)

func ListResourceTypes() ([]models.ResourceType, error) {
	if _resourceSvc == nil {
		return nil, resourceServiceUnavailable()
	}
	return _resourceSvc.ListResourceTypes()
}

func (rs *ResourceService) ListResourceTypes() ([]models.ResourceType, error) {
	repo, err := rs.requireRepo()
	if err != nil {
		return nil, err
	}
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		rs.cache,
		"all:"+utils.RedisResourceTypeKey,
		utils.CacheTTLs[utils.RedisResourceTypeKey],
		repo.ListResourceTypesRaw,
	)
}

// ResolveResourceTypeByCode looks up a ResourceType by its code (e.g. "image")
// and returns its UUID. Used when the caller doesn't provide resource_type_id.
func (rs *ResourceService) ResolveResourceTypeByCode(code string) (uuid.UUID, error) {
	types, err := rs.ListResourceTypes()
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("failed to list resource types: %w", err)
	}
	for _, t := range types {
		if t.Code == code {
			return t.ID, nil
		}
	}
	return uuid.UUID{}, fmt.Errorf("resource type not found for code: %s", code)
}
