package eventsrepository

import (
	"context"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func GetEventByID(id uuid.UUID) (*models.Event, error) {
	var event models.Event
	err := gormrepository.GetByID(&event, id)
	return &event, err
}

func CreateEvent(event *models.Event) error {
	err := gormrepository.Insert(event)
	if err != nil {
		return ValidateError(err)
	}

	pattern := "*" + utils.RedisServiceEventsKey + "*"
	if delErr := redisrepository.DeleteKeysByPattern(context.Background(), pattern); delErr != nil {
		return delErr
	}

	return nil
}

func UpdateEvent(event *models.Event) error {
	err := gormrepository.Update(event, event.ID)
	if err == nil {
		pattern := "*" + utils.RedisServiceEventsKey + "*"
		if delErr := redisrepository.DeleteKeysByPattern(context.Background(), pattern); delErr != nil {
			return delErr
		}
	}
	return err
}

func DeleteEvent(id uuid.UUID) error {
	err := gormrepository.Delete(id, &models.Event{})
	if err == nil {
		pattern := "*" + utils.RedisServiceEventsKey + "*"
		if delErr := redisrepository.DeleteKeysByPattern(context.Background(), pattern); delErr != nil {
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

	opts := gormrepository.QueryOptions{
		Filters:  filters,
		OrderBy:  "id",
		OrderDir: "desc",
	}

	if pageSize > 0 {
		opts.Limit = pageSize
		opts.Offset = (page - 1) * pageSize
	}

	err := gormrepository.GetList(&events, opts)
	return events, err
}
