package delivery

import (
	"errors"
	"events-stocks/configuration"
	"events-stocks/models"
	"fmt"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

var errTaskBudgetAdmission = errors.New("the task AI budget cannot reserve this run; raise the task budget or wait for active runs to settle")
var errProjectBudgetAdmission = errors.New("the monthly AI budget cannot reserve this run; wait for active runs to settle or raise the project budget")
var errAutomationGlobalAdmission = errors.New("the automation global concurrency limit is saturated; wait for active runs to settle")
var errAutomationProjectAdmission = errors.New("the automation project concurrency limit is saturated; wait for active runs to settle")
var errAutomationQueueAdmission = errors.New("the automation queue depth limit is saturated; wait for active runs to settle")

var activeAutomationStatuses = []string{"queued", "running", "cancel_requested"}

// rejectAutomationAdmission is a deterministic backpressure gate. It runs
// inside the same transaction as task creation and serializes configured
// environments with a PostgreSQL advisory lock, preventing two projects from
// both observing the same last global slot. Zero means an environment has not
// opted into that particular ceiling yet, which keeps older deployments
// compatible while making the policy explicit in the config surface.
func rejectAutomationAdmission(tx *gorm.DB, cfg *models.Config, projectID uuid.UUID) error {
	if cfg == nil || (cfg.AutomationGlobalActiveLimit <= 0 && cfg.AutomationProjectActiveLimit <= 0 && cfg.AutomationQueueDepthLimit <= 0) {
		return nil
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "itbem:automation:admission").Error; err != nil {
		return fmt.Errorf("lock automation admission: %w", err)
	}
	if cfg.AutomationQueueDepthLimit > 0 {
		var queued int64
		if err := tx.Model(&models.AutomationTask{}).Where("status IN ?", activeAutomationStatuses).Count(&queued).Error; err != nil {
			return err
		}
		if queued >= int64(cfg.AutomationQueueDepthLimit) {
			return errAutomationQueueAdmission
		}
	}
	if cfg.AutomationGlobalActiveLimit > 0 {
		var active int64
		if err := tx.Model(&models.AutomationTask{}).Where("status IN ?", activeAutomationStatuses).Count(&active).Error; err != nil {
			return err
		}
		if active >= int64(cfg.AutomationGlobalActiveLimit) {
			return errAutomationGlobalAdmission
		}
	}
	if cfg.AutomationProjectActiveLimit > 0 {
		var active int64
		if err := tx.Model(&models.AutomationTask{}).
			Joins("JOIN delivery_work_items ON delivery_work_items.id = automation_tasks.delivery_work_item_id").
			Where("delivery_work_items.project_id = ? AND automation_tasks.status IN ?", projectID, activeAutomationStatuses).
			Count(&active).Error; err != nil {
			return err
		}
		if active >= int64(cfg.AutomationProjectActiveLimit) {
			return errAutomationProjectAdmission
		}
	}
	return nil
}

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
		return errTaskBudgetAdmission
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
