package tablerepository

import (
	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

type TableRepo struct{}

func NewTableRepo() *TableRepo { return &TableRepo{} }

func (r *TableRepo) ListByEventID(eventID uuid.UUID) ([]models.Table, error) {
	var tables []models.Table
	err := configuration.DB.
		Where("event_id = ?", eventID).
		Order("sort_order ASC, created_at ASC").
		Find(&tables).Error
	return tables, err
}

func (r *TableRepo) Create(table *models.Table) error {
	return configuration.DB.Create(table).Error
}

func (r *TableRepo) Update(table *models.Table) error {
	return configuration.DB.
		Model(table).
		Where("id = ?", table.ID).
		Select("name", "capacity", "sort_order").
		Updates(table).Error
}

func (r *TableRepo) Delete(id uuid.UUID) error {
	// Unassign guests first (SET NULL), then soft-delete table
	if err := configuration.DB.
		Model(&models.Guest{}).
		Where("table_id = ?", id).
		Update("table_id", nil).Error; err != nil {
		return err
	}
	return configuration.DB.Where("id = ?", id).Delete(&models.Table{}).Error
}

func (r *TableRepo) GetByID(id uuid.UUID) (*models.Table, error) {
	var table models.Table
	if err := configuration.DB.Where("id = ?", id).First(&table).Error; err != nil {
		return nil, err
	}
	return &table, nil
}

func (r *TableRepo) BatchAssign(assignments []dtos.SeatAssignment) error {
	return configuration.DB.Transaction(func(tx *gorm.DB) error {
		for _, a := range assignments {
			guestID, err := uuid.FromString(a.GuestID)
			if err != nil {
				continue
			}
			q := tx.Model(&models.Guest{}).Where("id = ?", guestID)
			if a.TableID == nil {
				q = q.Update("table_id", nil)
			} else {
				tableID, err := uuid.FromString(*a.TableID)
				if err != nil {
					continue
				}
				q = q.Update("table_id", tableID)
			}
			if err := q.Error; err != nil {
				return err
			}
		}
		return nil
	})
}
