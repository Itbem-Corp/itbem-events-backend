package delivery

import (
	"events-stocks/configuration"
	"events-stocks/models"
	"fmt"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

type workItemBudgetRequest struct {
	BudgetMicros int64 `json:"budget_microusd"`
	AlertPercent int   `json:"alert_percent"`
}

type workItemBudgetResponse struct {
	BudgetMicros    int64  `json:"budget_microusd"`
	AlertPercent    int    `json:"alert_percent"`
	SpentMicros     int64  `json:"spent_microusd"`
	ReservedMicros  int64  `json:"reserved_microusd"`
	AllocatedMicros int64  `json:"allocated_microusd"`
	RemainingMicros *int64 `json:"remaining_microusd,omitempty"`
	Enforced        bool   `json:"enforced"`
}

func workItemSpend(tx *gorm.DB, workItemID uuid.UUID) (int64, error) {
	var spent int64
	err := tx.Table("("+deliveryCostLedgerUnion+") AS execution").
		Select("COALESCE(SUM(execution.total_cost_micros), 0)").
		Where("execution.delivery_work_item_id = ?", workItemID).
		Scan(&spent).Error
	return spent, err
}

func workItemReservations(tx *gorm.DB, workItemID uuid.UUID, now time.Time) (int64, error) {
	var reserved int64
	err := tx.Model(&models.AutomationTask{}).
		Select("COALESCE(SUM(budget_reservation_micros), 0)").
		Where("delivery_work_item_id = ? AND status IN ? AND (budget_reservation_expires_at IS NULL OR budget_reservation_expires_at > ?)", workItemID, []string{"queued", "running", "cancel_requested"}, now.UTC()).
		Scan(&reserved).Error
	return reserved, err
}

func workItemBudgetSnapshot(tx *gorm.DB, item models.DeliveryWorkItem, now time.Time) (workItemBudgetResponse, error) {
	spent, err := workItemSpend(tx, item.ID)
	if err != nil {
		return workItemBudgetResponse{}, err
	}
	reserved, err := workItemReservations(tx, item.ID, now)
	if err != nil {
		return workItemBudgetResponse{}, err
	}
	response := workItemBudgetResponse{BudgetMicros: item.BudgetMicros, AlertPercent: item.BudgetAlertPercent, SpentMicros: spent, ReservedMicros: reserved, AllocatedMicros: spent + reserved, Enforced: item.BudgetMicros > 0}
	if response.Enforced {
		remaining := item.BudgetMicros - response.AllocatedMicros
		if remaining < 0 {
			remaining = 0
		}
		response.RemainingMicros = &remaining
	}
	return response, nil
}

func rejectRunWhenWorkItemBudgetReached(tx *gorm.DB, item models.DeliveryWorkItem, now time.Time, reservationMicros int64) error {
	if item.BudgetMicros <= 0 {
		return nil
	}
	spent, err := workItemSpend(tx, item.ID)
	if err != nil {
		return err
	}
	reserved, err := workItemReservations(tx, item.ID, now)
	if err != nil {
		return err
	}
	if !budgetAdmissionAllowed(item.BudgetMicros, spent, reserved, reservationMicros) {
		return fmt.Errorf("the task AI budget cannot reserve this run; raise the task budget or wait for active runs to settle")
	}
	return nil
}

func GetWorkItemBudget(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	if _, _, err := workItemActor(c, workItemID, deliveryView); err != nil {
		return err
	}
	var item models.DeliveryWorkItem
	if err := configuration.DB.First(&item, workItemID).Error; err != nil {
		return lookup(c, "Delivery work item", err)
	}
	snapshot, snapshotErr := workItemBudgetSnapshot(configuration.DB, item, time.Now().UTC())
	if snapshotErr != nil {
		return utilsError(c, snapshotErr)
	}
	return success(c, "Delivery task budget", snapshot)
}

func UpdateWorkItemBudget(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	if _, _, err := workItemActor(c, workItemID, deliveryManage); err != nil {
		return err
	}
	var request workItemBudgetRequest
	if err := c.Bind(&request); err != nil {
		return badRequest(c, "Invalid delivery task budget", err.Error())
	}
	if request.BudgetMicros < 0 || request.BudgetMicros > maxDeliveryTaskBudgetMicros || request.AlertPercent < 50 || request.AlertPercent > 100 {
		return badRequest(c, "Invalid delivery task budget", "budget must be between 0 and 100,000 USD; alert percent must be between 50 and 100")
	}
	var item models.DeliveryWorkItem
	if err := configuration.DB.First(&item, workItemID).Error; err != nil {
		return lookup(c, "Delivery work item", err)
	}
	if err := configuration.DB.Model(&item).Updates(map[string]any{"budget_micros": request.BudgetMicros, "budget_alert_percent": request.AlertPercent}).Error; err != nil {
		return utilsError(c, err)
	}
	item.BudgetMicros, item.BudgetAlertPercent = request.BudgetMicros, request.AlertPercent
	snapshot, err := workItemBudgetSnapshot(configuration.DB, item, time.Now().UTC())
	if err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery task budget updated", snapshot)
}
