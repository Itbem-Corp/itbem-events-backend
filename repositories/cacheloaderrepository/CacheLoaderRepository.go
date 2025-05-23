package cacheloaderrepository

import (
	"context"
	"events-stocks/configuration"
	"events-stocks/configuration/constants"
	"events-stocks/dtos"
	"events-stocks/repositories/bucketrepository"
	"events-stocks/repositories/colorrepository"
	"events-stocks/repositories/eventsrepository"
	"events-stocks/repositories/fontrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/repositories/resourcerepository"
	"events-stocks/utils"
	"fmt"
	"github.com/gofrs/uuid"
	"strings"
	"time"
)

func GetLoader(resource string, key string) (func() (string, error), bool) {
	switch resource {
	case "events":
		if key == "all" {
			return ListAllEvents, true
		}
	case "fontsets":
		if key == "all" {
			return ListFontSets, true
		}
	case "colorpalettes":
		if key == "all" {
			return ListColorPalettes, true
		}
	case "resourcetypes":
		if key == "all" {
			return ListResourceTypesRaw, true
		}
	case "resources":
		return func() (string, error) {
			id, _ := uuid.FromString(key)
			return ListResourcesBySectionIdRaw(&id)
		}, true
	}

	return nil, false
}

func CacheOrLoad(resource string, key string, ttl time.Duration, loader func() (string, error)) (string, error) {
	ctx := context.Background()
	redisKey := key + ":" + resource

	// Revisa si existe en cache
	data, err := redisrepository.GetKey(ctx, redisKey)
	if err == nil && data != "" {
		return data, nil
	}

	// Ejecuta el loader
	data, err = loader()
	if err != nil {
		return "", err
	}
	// Guarda en cache
	_ = redisrepository.SaveKey(ctx, redisKey, data, ttl)

	return data, nil
}

func CacheOrLoadAuto(resource string, key string) (string, error) {
	loaderFunc, exists := GetLoader(resource, key)
	if !exists {
		return "", fmt.Errorf("no loader found for %s:%s", resource, key)
	}

	ttl := utils.CacheTTLs[resource]
	if ttl == 0 {
		ttl = time.Minute * 5 // fallback TTL
	}

	return CacheOrLoad(resource, key, ttl, loaderFunc)
}

func ListAllEvents() (string, error) {
	data, err := eventsrepository.ListEvents(1, 0, "")
	return utils.MarshallData(data, err)
}

func ListFontSets() (string, error) {
	data, err := fontrepository.ListFontSets(1, 0, "")
	return utils.MarshallData(data, err)
}

func ListColorPalettes() (string, error) {
	data, err := colorrepository.ListColorPalettes()
	return utils.MarshallData(data, err)
}

func ListResourceTypesRaw() (string, error) {
	data, err := resourcerepository.ListResourceTypesRaw()
	return utils.MarshallData(data, err)
}

func ListResourcesBySectionIdRaw(id *uuid.UUID) (string, error) {
	data, err := GetResourcesBySectionID(id, "", "", "")
	return utils.MarshallData(data, err)
}

func GetResourcesBySectionID(sectionID *uuid.UUID, uploadPath string, bucket string, provider string) ([]dtos.ResourceResponse, error) {
	resources, err := resourcerepository.ListResourcesBySection(sectionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}

	if provider == "" {
		provider = constants.DefaultCloudProvider
	}

	if bucket == "" {
		env := configuration.LoadConfig()
		bucket = env.AwsBucketName
	}

	if uploadPath == "" {
		uploadPath = constants.EventsBucketFolder
	}

	var result []dtos.ResourceResponse

	for _, r := range resources {
		if strings.TrimSpace(r.Path) == "" {
			continue
		}

		parts := strings.Split(r.Path, "/")
		filename := parts[len(parts)-1]

		exists, _, err := bucketrepository.FileExists(filename, uploadPath, bucket, provider)
		if err != nil || !exists {
			continue
		}

		viewURL, err := bucketrepository.GetPresignedFileURL(filename, uploadPath, bucket, provider, 60)
		if err != nil {
			continue
		}

		var sectionID uuid.UUID
		if r.EventSectionID != nil {
			sectionID = *r.EventSectionID
		}

		var position int
		if r.Position != nil {
			position = *r.Position
		}

		result = append(result, dtos.ResourceResponse{
			ID:             r.ID,
			EventSectionID: sectionID,
			ResourceTypeID: r.ResourceTypeID,
			AltText:        r.AltText,
			Title:          r.Title,
			Position:       position,
			ViewURL:        viewURL,
			CreatedAt:      r.CreatedAt,
		})
	}

	return result, nil
}
