package services

import (
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/cacheloaderrepository"
	"events-stocks/repositories/resourcerepository"
	"events-stocks/utils"
	"fmt"

	"github.com/gofrs/uuid"
)

func ListResourceTypes() ([]models.ResourceType, error) {
	jsonStr, err := cacheloaderrepository.CacheOrLoad(
		utils.RedisResourceTypeKey,
		"all",
		utils.CacheTTLs[utils.RedisResourceTypeKey],
		func() (string, error) {
			data, err := resourcerepository.ListResourceTypesRaw()
			if err != nil {
				return "", err
			}
			return utils.MarshallData(data, nil)
		},
	)

	if err != nil {
		return resourcerepository.ListResourceTypesRaw()
	}

	var result []models.ResourceType
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return resourcerepository.ListResourceTypesRaw()
	}

	return result, nil
}

// ResolveResourceTypeByCode looks up a ResourceType by its code (e.g. "image")
// and returns its UUID. Used when the caller doesn't provide resource_type_id.
func (rs *ResourceService) ResolveResourceTypeByCode(code string) (uuid.UUID, error) {
	types, err := ListResourceTypes()
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
