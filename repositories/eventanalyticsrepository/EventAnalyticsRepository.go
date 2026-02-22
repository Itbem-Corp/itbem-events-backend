package eventanalyticsrepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

func CreateEventAnalytics(m *models.EventAnalytics) error {
	return gormrepository.Insert(m)
}

func UpdateEventAnalytics(m *models.EventAnalytics) error {
	return gormrepository.Update(m, m.ID)
}

func DeleteEventAnalytics(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.EventAnalytics{})
}

func GetEventAnalyticsByID(id uuid.UUID) (*models.EventAnalytics, error) {
	var model models.EventAnalytics
	err := gormrepository.GetByID(&model, id)
	return &model, err
}

func ListEventAnalyticss() ([]models.EventAnalytics, error) {
	var list []models.EventAnalytics
	err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
	return list, err
}

func GetEventAnalyticsByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	var m models.EventAnalytics
	if err := gormrepository.GetFirst(&m, gormrepository.QueryOptions{
		Filters: map[string]interface{}{"event_id": eventID},
	}); err != nil {
		return nil, err
	}
	return &m, nil
}
