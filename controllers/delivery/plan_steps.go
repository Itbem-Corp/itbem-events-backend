package delivery

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/services/deliveryplansteps"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type deliveryPlanStepsResponse struct {
	PlanID      uuid.UUID                   `json:"plan_id"`
	PlanVersion int                         `json:"plan_version"`
	Items       []deliveryplansteps.StepDTO `json:"items"`
	Total       int                         `json:"total"`
}

type deliveryPlanStepsMaterializationResponse struct {
	deliveryPlanStepsResponse
	Created bool `json:"created"`
}

var errDeliveryPlanExecutionContract = errors.New("delivery plan execution contract is invalid")

// ListPlanSteps returns a server-side allow-list projection for a versioned
// plan. Legacy plans without normalized rows return an empty list until their
// owner explicitly materializes the immutable graph or a new agent run does so.
func ListPlanSteps(c echo.Context) error {
	planID, err := id(c, "delivery plan")
	if err != nil {
		return err
	}
	plan, _, err := loadDeliveryPlanForPermission(c, planID, deliveryView)
	if err != nil {
		return err
	}
	if plan == nil {
		// Helpers such as lookup and projectActor write their HTTP response and
		// return the result of Context.JSON, which is nil on a successful write.
		// Preserve that response instead of dereferencing a nil resource.
		return nil
	}
	items, err := loadDeliveryPlanStepDTOs(configuration.DB, *plan)
	if err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery plan steps", deliveryPlanStepsResponse{PlanID: plan.ID, PlanVersion: plan.Version, Items: items, Total: len(items)})
}

// MaterializePlanSteps backfills an immutable normalized step graph from an
// already persisted plan. It is idempotent: a retry with identical content
// returns the same rows; changed content requires a new plan version.
func MaterializePlanSteps(c echo.Context) error {
	planID, err := id(c, "delivery plan")
	if err != nil {
		return err
	}
	plan, actor, err := loadDeliveryPlanForPermission(c, planID, deliveryManage)
	if err != nil {
		return err
	}
	if plan == nil || actor == nil {
		// Authorization and not-found helpers may have already written a
		// response while returning nil from Context.JSON.
		return nil
	}
	var createdSteps []models.DeliveryPlanStep
	graphCreated := false
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		var lockedPlan models.DeliveryPlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedPlan, "id = ?", planID).Error; err != nil {
			return err
		}
		if lockedPlan.WorkItemID != plan.WorkItemID || lockedPlan.Version != plan.Version {
			return errors.New("delivery plan changed during step materialization")
		}
		var ensureErr error
		createdSteps, graphCreated, ensureErr = ensureDeliveryPlanStepsTx(tx, lockedPlan, actor.CognitoSub)
		return ensureErr
	})
	if err != nil {
		if errors.Is(err, deliveryplansteps.ErrPlanStepsConflict) {
			return conflict(c, "Delivery plan steps rejected", "the normalized graph differs from this immutable plan version")
		}
		if errors.Is(err, errDeliveryPlanExecutionContract) {
			return conflict(c, "Delivery plan steps unavailable", "this plan version does not contain an explicit, role-bound integration DAG; create and approve a new plan version before execution")
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return lookup(c, "Delivery plan", err)
		}
		return utilsError(c, err)
	}
	items, err := loadDeliveryPlanStepDTOs(configuration.DB, *plan)
	if err != nil {
		return utilsError(c, err)
	}
	response := deliveryPlanStepsMaterializationResponse{
		deliveryPlanStepsResponse: deliveryPlanStepsResponse{PlanID: plan.ID, PlanVersion: plan.Version, Items: items, Total: len(items)},
		Created:                   graphCreated && len(createdSteps) > 0,
	}
	if response.Created {
		return created(c, "Delivery plan steps materialized", response)
	}
	return success(c, "Delivery plan steps already materialized", response)
}

func loadDeliveryPlanForPermission(c echo.Context, planID uuid.UUID, permission deliveryPermission) (*models.DeliveryPlan, *models.User, error) {
	if configuration.DB == nil {
		return nil, nil, utils.Error(c, http.StatusServiceUnavailable, "Delivery unavailable", "Database is unavailable")
	}
	var plan models.DeliveryPlan
	if err := configuration.DB.First(&plan, "id = ?", planID).Error; err != nil {
		return nil, nil, lookup(c, "Delivery plan", err)
	}
	var item models.DeliveryWorkItem
	if err := configuration.DB.Select("id", "project_id").First(&item, "id = ?", plan.WorkItemID).Error; err != nil {
		return nil, nil, lookup(c, "Delivery work item", err)
	}
	actor, err := projectActor(c, item.ProjectID, permission)
	if err != nil {
		return nil, nil, err
	}
	return &plan, actor, nil
}

func ensureDeliveryPlanStepsTx(tx *gorm.DB, plan models.DeliveryPlan, actor string) ([]models.DeliveryPlanStep, bool, error) {
	var structured map[string]any
	if err := json.Unmarshal([]byte(plan.StructuredJSON), &structured); err != nil || structured == nil {
		return nil, false, errors.New("persisted delivery plan is not valid structured JSON")
	}
	inputs, err := deliveryplansteps.InputsFromStructuredPlan(structured)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", errDeliveryPlanExecutionContract, err)
	}
	return deliveryplansteps.EnsureInTransaction(tx, plan.ID, actor, inputs, time.Now().UTC())
}

func loadDeliveryPlanStepDTOs(db *gorm.DB, plan models.DeliveryPlan) ([]deliveryplansteps.StepDTO, error) {
	if db == nil {
		return nil, errors.New("delivery plan database is unavailable")
	}
	var steps []models.DeliveryPlanStep
	if err := db.Where("plan_id = ?", plan.ID).Order("display_order ASC").Find(&steps).Error; err != nil {
		return nil, err
	}
	var dependencies []models.DeliveryPlanStepDependency
	if len(steps) > 0 {
		if err := db.Where("plan_id = ?", plan.ID).Order("step_id ASC, depends_on_step_id ASC").Find(&dependencies).Error; err != nil {
			return nil, err
		}
	}
	return deliveryplansteps.DTOs(steps, dependencies, plan.Version)
}
