package services

import (
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/cacheloaderrepository"
	"events-stocks/repositories/resourcerepository"
	"events-stocks/utils"
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
