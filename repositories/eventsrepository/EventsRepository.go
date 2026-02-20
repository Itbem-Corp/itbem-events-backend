package eventsrepository

import (
	"context"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func GetEventByID(id uuid.UUID) (string, error) {
	var event models.Event
	err := gormrepository.GetByID(&event, id)
	if err != nil {
		return "", err
	}
	return utils.MarshallData([]*models.Event{&event}, nil)
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

// EventsRepo implements ports.EventsRepository.
type EventsRepo struct{}

func NewEventsRepo() *EventsRepo { return &EventsRepo{} }

func (r *EventsRepo) CreateEvent(event *models.Event) error           { return CreateEvent(event) }
func (r *EventsRepo) UpdateEvent(event *models.Event) error           { return UpdateEvent(event) }
func (r *EventsRepo) DeleteEvent(id uuid.UUID) error                  { return DeleteEvent(id) }
func (r *EventsRepo) ListEvents(page, pageSize int, name string) ([]models.Event, error) {
	return ListEvents(page, pageSize, name)
}
func (r *EventsRepo) GetEventByID(id uuid.UUID) (string, error) { return GetEventByID(id) }

// GetEventByIDRaw returns a bare Event record by ID without any preloads.
func GetEventByIDRaw(id uuid.UUID) (*models.Event, error) {
	var event models.Event
	err := gormrepository.GetByID(&event, id)
	if err != nil {
		return nil, err
	}
	return &event, nil
}
