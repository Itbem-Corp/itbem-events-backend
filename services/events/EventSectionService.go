package events

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/eventsectionrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListEventSections() ([]models.EventSection, error) {
	cacheKey := "all:events"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.EventSection
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := eventsectionrepository.ListEventSections()
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["events"])

	return data, nil
}

func GetEventSectionByID(id uuid.UUID) (*models.EventSection, error) {
	return eventsectionrepository.GetEventSectionByID(id)
}

func CreateEventSection(obj *models.EventSection) error {
	if err := eventsectionrepository.CreateEventSection(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}

func UpdateEventSection(obj *models.EventSection) error {
	if err := eventsectionrepository.UpdateEventSection(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}

func DeleteEventSection(id uuid.UUID) error {
	if err := eventsectionrepository.DeleteEventSection(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}
