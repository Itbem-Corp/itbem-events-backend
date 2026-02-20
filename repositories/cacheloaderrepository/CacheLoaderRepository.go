package cacheloaderrepository

import (
	"context"
	"events-stocks/configuration"
	"events-stocks/configuration/constants"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/bucketrepository"
	"events-stocks/repositories/colorrepository"
	"events-stocks/repositories/eventsrepository"
	"events-stocks/repositories/fontrepository"
	"events-stocks/repositories/guestrepository"
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
		} else {
			return func() (string, error) {
				if id, err := uuid.FromString(key); err == nil {
					return GetEventByID(id, err)
				}
				return "", fmt.Errorf("invalid UUID for event key: %s", key)
			}, true
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
	case "guests":
		if strings.HasPrefix(key, "all:") {
			eventIDStr := strings.TrimPrefix(key, "all:")
			if id, err := uuid.FromString(eventIDStr); err == nil {
				return func() (string, error) {
					return ListGuestsByEventID(id)
				}, true
			}
			return nil, false
		} else {
			// Guest por ID
			return func() (string, error) {
				if id, err := uuid.FromString(key); err == nil {
					return GetGuestByID(id)
				}
				return "", fmt.Errorf("invalid UUID for guest: %s", key)
			}, true
		}

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

func ListGuestsByEventID(eventID uuid.UUID) (string, error) {
	guests, err := guestrepository.ListGuestsByEventID(eventID)
	if err != nil {
		return "", err
	}
	return utils.MarshallData(guests, nil)
}

func GetGuestByID(id uuid.UUID) (string, error) {
	guest, err := guestrepository.GetGuestByID(id)
	if err != nil {
		return "", err
	}
	return utils.MarshallData([]*models.Guest{guest}, nil)
}

func GetEventByID(id uuid.UUID, error error) (string, error) {
	event, err := eventsrepository.GetEventByID(id)
	if err != nil {
		return "", err
	}
	return event, nil
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

		// Path no vacío indica que el archivo fue subido correctamente.
		// Omitimos FileExists (S3 HeadObject) para evitar N+1 API calls.
		viewURL, err := bucketrepository.GetPresignedFileURL(filename, uploadPath, bucket, provider, 720)
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
