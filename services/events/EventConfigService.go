package events

import (
	"events-stocks/models"
	"events-stocks/repositories/eventconfigrepository"
	"events-stocks/repositories/redisrepository"
	"github.com/gofrs/uuid"
)

func GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	return eventconfigrepository.GetEventConfigByID(id)
}

func CreateEventConfig(obj *models.EventConfig) error {
	if err := eventconfigrepository.CreateEventConfig(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}

func UpdateEventConfig(obj *models.EventConfig) error {
	if err := eventconfigrepository.UpdateEventConfig(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}

func DeleteEventConfig(id uuid.UUID) error {
	if err := eventconfigrepository.DeleteEventConfig(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}
