package eventService

import (
	"context"
	"events-stocks/models"
	"events-stocks/services/gormService"
	"events-stocks/services/redisService"
	"github.com/gofrs/uuid"
)

const RedisServiceEventsKey = "events"

func GetEventByID(id uuid.UUID) (*models.Event, error) {
	var event models.Event
	err := gormService.GetByID(&event, id)
	return &event, err
}

func CreateEvent(event *models.Event) error {
	err := gormService.Insert(event)
	if err != nil {
		return ValidateError(err)
	}

	pattern := "*" + RedisServiceEventsKey + "*"
	if delErr := redisService.DeleteKeysByPattern(context.Background(), pattern); delErr != nil {
		return delErr
	}

	return nil
}

func UpdateEvent(event *models.Event) error {
	err := gormService.Update(event, event.ID)
	if err == nil {
		pattern := "*" + RedisServiceEventsKey + "*"
		if delErr := redisService.DeleteKeysByPattern(context.Background(), pattern); delErr != nil {
			return delErr
		}
	}
	return err
}

func DeleteEvent(id uuid.UUID) error {
	err := gormService.Delete(id, &models.Event{})
	if err == nil {
		pattern := "*" + RedisServiceEventsKey + "*"
		if delErr := redisService.DeleteKeysByPattern(context.Background(), pattern); delErr != nil {
			return delErr
		}
	}
	return err
}

func ListEvents(page int, pageSize int, name string) ([]models.Event, error) {
	var events []models.Event

	filters := map[string]interface{}{}
	if name != "" {
		filters["name"] = name
	}

	opts := gormService.QueryOptions{
		Filters:  filters,
		OrderBy:  "id",
		OrderDir: "desc",
	}

	if pageSize > 0 {
		opts.Limit = pageSize
		opts.Offset = (page - 1) * pageSize
	}

	err := gormService.GetList(&events, opts)
	return events, err
}
