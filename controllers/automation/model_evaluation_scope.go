package automation

import (
	"errors"
	"time"

	"events-stocks/internal/modelevaluation"
	"events-stocks/models"
	"events-stocks/services/automationcost"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errEvaluationAdmission = errors.New("evaluation admission rejected")

func evaluationCallForTask(tx *gorm.DB, task models.AutomationTask) (models.AutomationModelEvaluationCall, models.AutomationAIActionRoute, error) {
	var call models.AutomationModelEvaluationCall
	if task.ModelEvaluationID == nil || task.Operation != "ai.chat" || task.DeliveryWorkItemID != nil || task.DeliveryOnboardingID != nil || task.MaxCompletionTokens != modelevaluation.MaxCompletionTokens {
		return call, models.AutomationAIActionRoute{}, errEvaluationAdmission
	}
	if err := tx.Where("automation_task_id = ? AND evaluation_id = ?", task.ID, *task.ModelEvaluationID).Take(&call).Error; err != nil {
		return call, models.AutomationAIActionRoute{}, err
	}
	route, err := modelevaluation.Route(modelevaluation.Candidate(call.Candidate))
	if err != nil || call.Sequence < 1 || call.Sequence > modelevaluation.MaxCalls || call.ReservationMicros <= 0 || call.ReservationMicros > modelevaluation.MaxBudgetMicros {
		return call, route, errEvaluationAdmission
	}
	_, hash, err := canonicalInferenceRoutes([]models.AutomationAIActionRoute{route})
	if err != nil || hash != call.RouteHash {
		return call, route, errEvaluationAdmission
	}
	return call, route, nil
}

// validateEvaluationInference runs inside the normal live-lease transaction.
// The task row is already locked. Batch locking serializes all call admission;
// every receipt state counts across run IDs, including ambiguous paid outcomes.
func validateEvaluationInference(tx *gorm.DB, task models.AutomationTask, request inferenceRequest, snapshot models.AutomationInferenceAttemptPolicy, scope *gatewayInferenceScope) error {
	call, route, err := evaluationCallForTask(tx, task)
	if err != nil {
		return err
	}
	var batch models.AutomationModelEvaluation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&batch, "id = ?", *task.ModelEvaluationID).Error; err != nil {
		return err
	}
	expectedCalls, err := modelevaluation.ExpectedCalls(batch.CorpusVersion)
	if err != nil || call.Sequence > expectedCalls {
		return errEvaluationAdmission
	}
	if batch.CorpusVersion == modelevaluation.ImplementationPilotVersion {
		candidates := []modelevaluation.Candidate{modelevaluation.MiniMax, modelevaluation.DeepSeek, modelevaluation.Luna}
		if call.CaseID != "pagination-v1" || call.Candidate != string(candidates[call.Sequence-1]) {
			return errEvaluationAdmission
		}
	}
	if batch.Status != "active" || batch.BudgetMicros <= 0 || batch.BudgetMicros > modelevaluation.MaxBudgetMicros || batch.ReservationMicros <= 0 || batch.ReservationMicros > batch.BudgetMicros || task.BudgetReservationExpiresAt == nil || !task.BudgetReservationExpiresAt.After(time.Now().UTC()) {
		return errEvaluationAdmission
	}
	hash, err := modelevaluation.MessageDigest(request.Messages)
	if err != nil || hash != call.MessagesHash || request.MaxCompletionTokens != modelevaluation.MaxCompletionTokens || snapshot.MaxInferenceCalls != 1 || snapshot.ProjectID != nil || snapshot.RoutesHash != call.RouteHash {
		return errEvaluationAdmission
	}
	routes, _, valid := validateAutomationInferenceAttemptPolicy(snapshot)
	if !valid || len(routes) != 1 || routes[0] != route {
		return errEvaluationAdmission
	}
	var used int64
	if err := tx.Model(&models.AutomationInferenceReceipt{}).Where("automation_task_id = ?", task.ID).Count(&used).Error; err != nil {
		return err
	}
	if used != 0 {
		return errInferenceRunQuotaExceeded
	}
	// No other evaluation call can be active or ambiguous, even after a lease
	// expires. An ambiguous outcome requires operator resolution, never retry.
	if err := tx.Model(&models.AutomationInferenceReceipt{}).
		Joins("JOIN automation_model_evaluation_calls AS evaluation_call ON evaluation_call.automation_task_id = automation_inference_receipts.automation_task_id").
		Where("evaluation_call.evaluation_id = ? AND automation_inference_receipts.status IN ?", batch.ID, []string{"reserved", "ambiguous", "rejected"}).Count(&used).Error; err != nil {
		return err
	}
	if used != 0 {
		return errEvaluationAdmission
	}
	messageBytes := 0
	for _, message := range request.Messages {
		messageBytes += len(message.Role) + len(message.Content) + 64
	}
	bound, err := automationcost.EstimateUpperBound(route.Provider, route.Model, messageBytes, request.MaxCompletionTokens, batch.PricingJSON)
	if err != nil || bound <= 0 || bound > call.ReservationMicros {
		return errEvaluationAdmission
	}
	scope.EvaluationID = task.ModelEvaluationID
	scope.EvaluationPricingJSON = batch.PricingJSON
	scope.EvaluationReservationMicros = call.ReservationMicros
	return nil
}
