package tables

import (
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"
	"github.com/gofrs/uuid"
)

type TableService struct {
	repo ports.TableRepository
}

func NewTableService(repo ports.TableRepository) *TableService {
	return &TableService{repo: repo}
}

func (s *TableService) ListByEventID(eventID uuid.UUID) ([]models.Table, error) {
	return s.repo.ListByEventID(eventID)
}

func (s *TableService) Create(eventID uuid.UUID, table *models.Table) error {
	table.EventID = eventID
	return s.repo.Create(table)
}

func (s *TableService) Update(table *models.Table) error {
	return s.repo.Update(table)
}

func (s *TableService) Delete(id uuid.UUID) error {
	return s.repo.Delete(id)
}

func (s *TableService) GetByID(id uuid.UUID) (*models.Table, error) {
	return s.repo.GetByID(id)
}

func (s *TableService) BatchAssign(assignments []dtos.SeatAssignment) error {
	return s.repo.BatchAssign(assignments)
}
