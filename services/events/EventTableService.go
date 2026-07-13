package events

import (
	"context"
	"fmt"

	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"

	"github.com/gofrs/uuid"
)

type EventTableService struct {
	repo  ports.EventTableRepository
	cache ports.CacheRepository
}

func NewEventTableService(repo ports.EventTableRepository, cache ports.CacheRepository) *EventTableService {
	return &EventTableService{repo: repo, cache: cache}
}

func (s *EventTableService) CreateTable(table *models.EventTable) error {
	if err := s.repo.CreateEventTable(table); err != nil {
		return err
	}
	return s.invalidate(table.EventID)
}

func (s *EventTableService) UpdateTable(table *models.EventTable) error {
	if err := s.repo.UpdateEventTable(table); err != nil {
		return err
	}
	return s.invalidate(table.EventID)
}

func (s *EventTableService) DeleteTable(table *models.EventTable) error {
	if err := s.repo.DeleteEventTable(table.ID); err != nil {
		return err
	}
	return s.invalidate(table.EventID)
}

func (s *EventTableService) GetTableByID(id uuid.UUID) (*models.EventTable, error) {
	return s.repo.GetEventTableByID(id)
}

func (s *EventTableService) ListByEventID(eventID uuid.UUID) ([]models.EventTable, error) {
	return s.repo.ListEventTablesByEventID(eventID)
}

func (s *EventTableService) AssignGuests(eventID uuid.UUID, assignments map[uuid.UUID]*uuid.UUID) error {
	if len(assignments) == 0 {
		return nil
	}
	if err := s.repo.AssignGuestsToTables(eventID, assignments); err != nil {
		return err
	}
	return s.invalidate(eventID)
}

func (s *EventTableService) SaveSeatingPlan(eventID uuid.UUID, plan dtos.SeatingPlanSaveRequest) ([]models.EventTable, error) {
	tables, err := s.repo.SaveSeatingPlan(eventID, plan)
	if err != nil {
		return nil, err
	}
	return tables, s.invalidate(eventID)
}

func (s *EventTableService) invalidate(eventID uuid.UUID) error {
	if s.cache == nil {
		return nil
	}
	ctx := context.Background()
	_ = s.cache.DeleteKeysByPattern(ctx, fmt.Sprintf("all:%s:guests", eventID.String()))
	_ = s.cache.DeleteKeysByPattern(ctx, fmt.Sprintf("event_tables:%s:*", eventID.String()))
	return nil
}
