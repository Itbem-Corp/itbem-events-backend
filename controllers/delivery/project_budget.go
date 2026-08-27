package delivery

import (
	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/services/automationcost"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const (
	maxDeliveryMonthlyBudgetMicros   int64 = 1_000_000_000_000
	maxDeliveryTaskBudgetMicros      int64 = 100_000_000_000
	defaultTaskBudgetAlertPercent          = 80
	budgetReservationInitialDuration       = 40 * time.Minute
)

type projectBudgetRequest struct {
	MonthlyBudgetMicros int64 `json:"monthly_budget_microusd"`
	AlertPercent        int   `json:"alert_percent"`
}

type projectBudgetResponse struct {
	MonthlyBudgetMicros int64  `json:"monthly_budget_microusd"`
	AlertPercent        int    `json:"alert_percent"`
	SpentMicros         int64  `json:"spent_microusd"`
	ReservedMicros      int64  `json:"reserved_microusd"`
	AllocatedMicros     int64  `json:"allocated_microusd"`
	RemainingMicros     *int64 `json:"remaining_microusd,omitempty"`
	Enforced            bool   `json:"enforced"`
	MonthStart          string `json:"month_start"`
}

func monthStartUTC(now time.Time) time.Time {
	value := now.UTC()
	return time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func projectMonthlySpend(tx *gorm.DB, projectID uuid.UUID, now time.Time) (int64, error) {
	var spent int64
	err := tx.Table("("+deliveryCostLedgerUnion+") AS execution").
		Select("COALESCE(SUM(execution.total_cost_micros), 0)").
		Joins("JOIN delivery_work_items ON delivery_work_items.id = execution.delivery_work_item_id").
		Where("delivery_work_items.project_id = ? AND execution.completed_at >= ?", projectID, monthStartUTC(now)).
		Scan(&spent).Error
	return spent, err
}

func projectMonthlyReservations(tx *gorm.DB, projectID uuid.UUID, now time.Time) (int64, error) {
	var reserved int64
	err := tx.Model(&models.AutomationTask{}).
		Select("COALESCE(SUM(automation_tasks.budget_reservation_micros), 0)").
		Joins("JOIN delivery_work_items ON delivery_work_items.id = automation_tasks.delivery_work_item_id").
		Where("delivery_work_items.project_id = ? AND automation_tasks.created_at >= ? AND automation_tasks.status IN ? AND (automation_tasks.budget_reservation_expires_at IS NULL OR automation_tasks.budget_reservation_expires_at > ?)", projectID, monthStartUTC(now), []string{"queued", "running", "cancel_requested"}, now.UTC()).
		Scan(&reserved).Error
	return reserved, err
}

func projectBudgetSnapshot(tx *gorm.DB, project models.DeliveryProject, now time.Time) (projectBudgetResponse, error) {
	spent, err := projectMonthlySpend(tx, project.ID, now)
	if err != nil {
		return projectBudgetResponse{}, err
	}
	reserved, err := projectMonthlyReservations(tx, project.ID, now)
	if err != nil {
		return projectBudgetResponse{}, err
	}
	response := projectBudgetResponse{MonthlyBudgetMicros: project.MonthlyBudgetMicros, AlertPercent: project.BudgetAlertPercent, SpentMicros: spent, ReservedMicros: reserved, AllocatedMicros: spent + reserved, Enforced: project.MonthlyBudgetMicros > 0, MonthStart: monthStartUTC(now).Format(time.RFC3339)}
	if response.Enforced {
		remaining := project.MonthlyBudgetMicros - response.AllocatedMicros
		if remaining < 0 {
			remaining = 0
		}
		response.RemainingMicros = &remaining
	}
	return response, nil
}

func GetProjectBudget(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryView); err != nil {
		return err
	}
	var project models.DeliveryProject
	if err := configuration.DB.First(&project, projectID).Error; err != nil {
		return lookup(c, "Delivery project", err)
	}
	snapshot, err := projectBudgetSnapshot(configuration.DB, project, time.Now().UTC())
	if err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery project budget", snapshot)
}

func UpdateProjectBudget(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryManage); err != nil {
		return err
	}
	var request projectBudgetRequest
	if err := c.Bind(&request); err != nil {
		return badRequest(c, "Invalid project budget", err.Error())
	}
	if request.MonthlyBudgetMicros < 0 || request.MonthlyBudgetMicros > maxDeliveryMonthlyBudgetMicros || request.AlertPercent < 50 || request.AlertPercent > 100 {
		return badRequest(c, "Invalid project budget", "budget must be between 0 and 1,000,000 USD; alert percent must be between 50 and 100")
	}
	var project models.DeliveryProject
	if err := configuration.DB.First(&project, projectID).Error; err != nil {
		return lookup(c, "Delivery project", err)
	}
	if err := configuration.DB.Model(&project).Updates(map[string]any{"monthly_budget_micros": request.MonthlyBudgetMicros, "budget_alert_percent": request.AlertPercent}).Error; err != nil {
		return utilsError(c, err)
	}
	project.MonthlyBudgetMicros, project.BudgetAlertPercent = request.MonthlyBudgetMicros, request.AlertPercent
	snapshot, err := projectBudgetSnapshot(configuration.DB, project, time.Now().UTC())
	if err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery project budget updated", snapshot)
}

func rejectRunWhenProjectBudgetReached(tx *gorm.DB, project models.DeliveryProject, now time.Time, reservationMicros int64) error {
	if project.MonthlyBudgetMicros <= 0 {
		return nil
	}
	spent, err := projectMonthlySpend(tx, project.ID, now)
	if err != nil {
		return err
	}
	reserved, err := projectMonthlyReservations(tx, project.ID, now)
	if err != nil {
		return err
	}
	if !budgetAdmissionAllowed(project.MonthlyBudgetMicros, spent, reserved, reservationMicros) {
		return fmt.Errorf("the monthly AI budget cannot reserve this run; wait for active runs to settle or raise the project budget")
	}
	return nil
}

func budgetAdmissionAllowed(budget, spent, reserved, candidate int64) bool {
	return budget > 0 && spent >= 0 && reserved >= 0 && candidate >= 0 && spent+reserved+candidate <= budget
}

func projectBudgetReservation(cfg *models.Config, inputBytes, maxCompletionTokens int) (int64, error) {
	provider, model := "minimax", "MiniMax-M3"
	if cfg != nil {
		if value := strings.TrimSpace(cfg.AutomationBudgetProvider); value != "" {
			provider = value
		}
		if value := strings.TrimSpace(cfg.AutomationBudgetModel); value != "" {
			model = value
		}
	}
	configured := ""
	if cfg != nil {
		configured = cfg.AutomationPricingJSON
	}
	return automationcost.EstimateUpperBound(provider, model, inputBytes, maxCompletionTokens, configured)
}

const (
	defaultQASemanticInputTokenReserve  = 24_000
	defaultQASemanticOutputTokenReserve = 4_096
)

// deliveryRunBudgetReservation includes every model call the requested
// delivery operation is allowed to make. QA has a second, browser-bound
// Stagehand call in addition to the delivery agent summary; reserving it here
// prevents concurrent QA tasks from silently exceeding a project cap.
func deliveryRunBudgetReservation(cfg *models.Config, operation string, inputBytes, maxCompletionTokens int) (int64, error) {
	primary, err := projectBudgetReservation(cfg, inputBytes, maxCompletionTokens)
	if err != nil || operation != "delivery.qa" {
		return primary, err
	}
	semanticInput, semanticOutput := defaultQASemanticInputTokenReserve, defaultQASemanticOutputTokenReserve
	if cfg != nil {
		if cfg.AutomationQASemanticInputTokenReserve > 0 {
			semanticInput = cfg.AutomationQASemanticInputTokenReserve
		}
		if cfg.AutomationQASemanticOutputTokenReserve > 0 {
			semanticOutput = cfg.AutomationQASemanticOutputTokenReserve
		}
	}
	semantic, err := projectBudgetReservation(cfg, semanticInput, semanticOutput)
	if err != nil {
		return 0, err
	}
	if primary > math.MaxInt64-semantic {
		return 0, fmt.Errorf("budget reservation exceeds supported range")
	}
	return primary + semantic, nil
}
