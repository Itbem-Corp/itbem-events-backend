package eventtablerepository

import (
	"fmt"
	"strings"

	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

type EventTableRepo struct{}

func NewEventTableRepo() *EventTableRepo { return &EventTableRepo{} }

func CreateEventTable(table *models.EventTable) error {
	return gormrepository.Insert(table)
}

func UpdateEventTable(table *models.EventTable) error {
	return gormrepository.Update(table, table.ID)
}

func DeleteEventTable(id uuid.UUID) error {
	return configuration.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Guest{}).
			Where("table_id = ?", id).
			Update("table_id", nil).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&models.EventTable{}).Error
	})
}

func GetEventTableByID(id uuid.UUID) (*models.EventTable, error) {
	var table models.EventTable
	err := gormrepository.GetByID(&table, id)
	if err != nil {
		return nil, err
	}
	return &table, nil
}

func ListEventTablesByEventID(eventID uuid.UUID) ([]models.EventTable, error) {
	var list []models.EventTable
	err := configuration.DB.
		Where("event_id = ?", eventID).
		Order("sort_order ASC").
		Order("created_at ASC").
		Find(&list).Error
	return list, err
}

func AssignGuestsToTables(eventID uuid.UUID, assignments map[uuid.UUID]*uuid.UUID) error {
	return configuration.DB.Transaction(func(tx *gorm.DB) error {
		tableIDs := make([]uuid.UUID, 0)
		seenTables := make(map[uuid.UUID]struct{})
		for _, tableID := range assignments {
			if tableID == nil {
				continue
			}
			if _, ok := seenTables[*tableID]; ok {
				continue
			}
			seenTables[*tableID] = struct{}{}
			tableIDs = append(tableIDs, *tableID)
		}
		if len(tableIDs) > 0 {
			var count int64
			if err := tx.Model(&models.EventTable{}).
				Where("event_id = ? AND id IN ?", eventID, tableIDs).
				Count(&count).Error; err != nil {
				return err
			}
			if count != int64(len(tableIDs)) {
				return fmt.Errorf("one or more tables do not belong to event")
			}
		}

		for guestID, tableID := range assignments {
			result := tx.Model(&models.Guest{}).
				Where("id = ? AND event_id = ?", guestID, eventID).
				Update("table_id", tableID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return fmt.Errorf("guest %s does not belong to event", guestID)
			}
		}
		return nil
	})
}

func SaveSeatingPlan(eventID uuid.UUID, plan dtos.SeatingPlanSaveRequest) ([]models.EventTable, error) {
	var saved []models.EventTable
	err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		tempIDs := make(map[string]uuid.UUID, len(plan.Created))
		for _, item := range plan.Created {
			tempID := strings.TrimSpace(item.TempID)
			name := strings.TrimSpace(item.Name)
			if tempID == "" || name == "" || item.Capacity <= 0 {
				return fmt.Errorf("created tables require temp_id, name, and positive capacity")
			}
			if _, exists := tempIDs[tempID]; exists {
				return fmt.Errorf("duplicate temporary table id %s", tempID)
			}
			table := models.EventTable{EventID: eventID, Name: name, Capacity: item.Capacity, SortOrder: item.SortOrder}
			if err := tx.Create(&table).Error; err != nil {
				return err
			}
			tempIDs[tempID] = table.ID
		}

		for _, item := range plan.Updated {
			tableID, err := uuid.FromString(strings.TrimSpace(item.ID))
			if err != nil {
				return fmt.Errorf("invalid updated table id: %w", err)
			}
			name := strings.TrimSpace(item.Name)
			if name == "" || item.Capacity <= 0 {
				return fmt.Errorf("updated tables require name and positive capacity")
			}
			result := tx.Model(&models.EventTable{}).
				Where("id = ? AND event_id = ?", tableID, eventID).
				Updates(map[string]interface{}{"name": name, "capacity": item.Capacity, "sort_order": item.SortOrder})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return fmt.Errorf("table %s does not belong to event", tableID)
			}
		}

		deletedIDs := make([]uuid.UUID, 0, len(plan.DeletedIDs))
		deletedSet := make(map[uuid.UUID]struct{}, len(plan.DeletedIDs))
		for _, rawID := range plan.DeletedIDs {
			id, err := uuid.FromString(strings.TrimSpace(rawID))
			if err != nil {
				return fmt.Errorf("invalid deleted table id: %w", err)
			}
			deletedIDs = append(deletedIDs, id)
			deletedSet[id] = struct{}{}
		}

		type resolvedAssignment struct {
			guestID uuid.UUID
			tableID *uuid.UUID
		}
		resolvedAssignments := make([]resolvedAssignment, 0, len(plan.Assignments))
		assignmentTableIDs := make([]uuid.UUID, 0)
		seenAssignmentTables := make(map[uuid.UUID]struct{})
		seenGuests := make(map[uuid.UUID]struct{}, len(plan.Assignments))
		for _, assignment := range plan.Assignments {
			guestID, err := uuid.FromString(strings.TrimSpace(assignment.GuestID))
			if err != nil {
				return fmt.Errorf("invalid guest id: %w", err)
			}
			if _, duplicate := seenGuests[guestID]; duplicate {
				return fmt.Errorf("duplicate guest assignment %s", guestID)
			}
			seenGuests[guestID] = struct{}{}
			var tableID *uuid.UUID
			if assignment.TableID != nil && strings.TrimSpace(*assignment.TableID) != "" {
				reference := strings.TrimSpace(*assignment.TableID)
				resolved, found := tempIDs[reference]
				if !found {
					resolved, err = uuid.FromString(reference)
					if err != nil {
						return fmt.Errorf("invalid assignment table id: %w", err)
					}
				}
				if _, deleted := deletedSet[resolved]; deleted {
					return fmt.Errorf("cannot assign guest to a deleted table")
				}
				if _, seen := seenAssignmentTables[resolved]; !seen {
					seenAssignmentTables[resolved] = struct{}{}
					assignmentTableIDs = append(assignmentTableIDs, resolved)
				}
				tableID = &resolved
			}
			resolvedAssignments = append(resolvedAssignments, resolvedAssignment{guestID: guestID, tableID: tableID})
		}
		if len(assignmentTableIDs) > 0 {
			var count int64
			if err := tx.Model(&models.EventTable{}).Where("event_id = ? AND id IN ?", eventID, assignmentTableIDs).Count(&count).Error; err != nil {
				return err
			}
			if count != int64(len(assignmentTableIDs)) {
				return fmt.Errorf("one or more assignment tables do not belong to event")
			}
		}
		if len(resolvedAssignments) > 0 {
			guestIDs := make([]uuid.UUID, 0, len(resolvedAssignments))
			values := make([]string, 0, len(resolvedAssignments))
			args := make([]interface{}, 0, len(resolvedAssignments)*2+1)
			for _, assignment := range resolvedAssignments {
				guestIDs = append(guestIDs, assignment.guestID)
				values = append(values, "(?::uuid, ?::uuid)")
				var tableValue interface{}
				if assignment.tableID != nil {
					tableValue = *assignment.tableID
				}
				args = append(args, assignment.guestID, tableValue)
			}
			var guestCount int64
			if err := tx.Model(&models.Guest{}).Where("event_id = ? AND id IN ?", eventID, guestIDs).Count(&guestCount).Error; err != nil {
				return err
			}
			if guestCount != int64(len(guestIDs)) {
				return fmt.Errorf("one or more guests do not belong to event")
			}
			args = append(args, eventID)
			query := `UPDATE guests AS guest
				SET table_id = requested.table_id, updated_at = NOW()
				FROM (VALUES ` + strings.Join(values, ",") + `) AS requested(id, table_id)
				WHERE guest.id = requested.id AND guest.event_id = ? AND guest.deleted_at IS NULL`
			if err := tx.Exec(query, args...).Error; err != nil {
				return err
			}
		}

		if len(deletedIDs) > 0 {
			if err := tx.Model(&models.Guest{}).Where("event_id = ? AND table_id IN ?", eventID, deletedIDs).Update("table_id", nil).Error; err != nil {
				return err
			}
			result := tx.Where("event_id = ? AND id IN ?", eventID, deletedIDs).Delete(&models.EventTable{})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != int64(len(deletedIDs)) {
				return fmt.Errorf("one or more deleted tables do not belong to event")
			}
		}

		return tx.Where("event_id = ?", eventID).Order("sort_order ASC").Order("created_at ASC").Find(&saved).Error
	})
	return saved, err
}

func (r *EventTableRepo) CreateEventTable(table *models.EventTable) error {
	return CreateEventTable(table)
}

func (r *EventTableRepo) UpdateEventTable(table *models.EventTable) error {
	return UpdateEventTable(table)
}

func (r *EventTableRepo) DeleteEventTable(id uuid.UUID) error {
	return DeleteEventTable(id)
}

func (r *EventTableRepo) GetEventTableByID(id uuid.UUID) (*models.EventTable, error) {
	return GetEventTableByID(id)
}

func (r *EventTableRepo) ListEventTablesByEventID(eventID uuid.UUID) ([]models.EventTable, error) {
	return ListEventTablesByEventID(eventID)
}

func (r *EventTableRepo) AssignGuestsToTables(eventID uuid.UUID, assignments map[uuid.UUID]*uuid.UUID) error {
	return AssignGuestsToTables(eventID, assignments)
}

func (r *EventTableRepo) SaveSeatingPlan(eventID uuid.UUID, plan dtos.SeatingPlanSaveRequest) ([]models.EventTable, error) {
	return SaveSeatingPlan(eventID, plan)
}
