package deliveryplansteps

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
)

var validationPlanID = uuid.Must(uuid.FromString("00000000-0000-4000-8000-000000000001"))

// InputsFromStructuredPlan accepts the current explicit execution DAG and
// safely upgrades older plans that contain only implementation_steps strings.
// Legacy ordered steps are chained sequentially; new plans can declare true
// parallel branches explicitly via execution_steps.depends_on.
func InputsFromStructuredPlan(structured map[string]any) ([]StepInput, error) {
	if raw, present := structured["execution_steps"]; present {
		items, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("execution_steps must be a list of step objects")
		}
		if len(items) > MaxStepsPerPlan {
			return nil, fmt.Errorf("execution_steps may contain at most %d steps", MaxStepsPerPlan)
		}
		inputs := make([]StepInput, 0, len(items))
		for index, item := range items {
			entry, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("execution_steps[%d] must be an object", index)
			}
			order, ok := integerValue(entry["order"])
			if !ok {
				return nil, fmt.Errorf("execution_steps[%d].order must be an integer", index)
			}
			criteria, err := stringList(entry["acceptance_criteria"], fmt.Sprintf("execution_steps[%d].acceptance_criteria", index))
			if err != nil {
				return nil, err
			}
			dependencies, err := optionalStringList(entry["depends_on"], fmt.Sprintf("execution_steps[%d].depends_on", index))
			if err != nil {
				return nil, err
			}
			evidenceRequirements, err := parseEvidenceRequirementsValue(entry["evidence_requirements"], fmt.Sprintf("execution_steps[%d].evidence_requirements", index))
			if err != nil {
				return nil, err
			}
			input := StepInput{
				Key:                  stringValue(entry["step_key"]),
				Role:                 stringValue(entry["role"]),
				Order:                order,
				Title:                stringValue(entry["title"]),
				Objective:            stringValue(entry["objective"]),
				AcceptanceCriteria:   criteria,
				EvidenceRequirements: evidenceRequirements,
				DependsOn:            dependencies,
				IdempotencyKey:       stringValue(entry["idempotency_key"]),
			}
			inputs = append(inputs, input)
		}
		if err := ValidateInputs(inputs); err != nil {
			return nil, err
		}
		workItemCriteria, err := stringList(structured["acceptance_criteria"], "acceptance_criteria")
		if err != nil {
			return nil, err
		}
		if err := validateIntegrationCriteria(inputs, workItemCriteria); err != nil {
			return nil, err
		}
		return inputs, nil
	}

	implementationSteps, err := stringList(structured["implementation_steps"], "implementation_steps")
	if err != nil {
		return nil, err
	}
	if len(implementationSteps) == 0 {
		return []StepInput{}, nil
	}
	return nil, fmt.Errorf("legacy implementation_steps cannot be executed; an explicit role-bound execution_steps DAG with an integration node is required")
}

func parseEvidenceRequirementsValue(value any, field string) ([]EvidenceRequirement, error) {
	if value == nil {
		return []EvidenceRequirement{}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%s must be a list of typed requirement objects", field)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var requirements []EvidenceRequirement
	if err := decoder.Decode(&requirements); err != nil {
		return nil, fmt.Errorf("%s must be a list of typed requirement objects", field)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("%s contains trailing data", field)
	}
	normalized, err := normalizeEvidenceRequirements(requirements)
	if err != nil {
		return nil, fmt.Errorf("%s is invalid: %w", field, err)
	}
	return normalized, nil
}

func validateIntegrationCriteria(inputs []StepInput, workItemCriteria []string) error {
	if len(inputs) == 0 {
		if len(workItemCriteria) == 0 {
			return nil
		}
		return fmt.Errorf("work-item acceptance criteria require an explicit execution DAG")
	}
	workItem := make(map[string]struct{}, len(workItemCriteria))
	for _, criterion := range workItemCriteria {
		criterion = strings.TrimSpace(criterion)
		if criterion == "" {
			return fmt.Errorf("acceptance_criteria must contain non-empty criteria")
		}
		if _, duplicate := workItem[criterion]; duplicate {
			return fmt.Errorf("acceptance_criteria contains a duplicate criterion")
		}
		workItem[criterion] = struct{}{}
	}
	if len(workItem) == 0 || len(workItem) > MaxAcceptanceCriteria {
		return fmt.Errorf("an execution DAG requires between 1 and %d work-item acceptance criteria", MaxAcceptanceCriteria)
	}
	producerCriteria := make(map[string]struct{})
	var integrationCriteria []string
	for _, step := range inputs {
		if step.Role == StepRoleIntegration {
			integrationCriteria = step.AcceptanceCriteria
			continue
		}
		for _, criterion := range step.AcceptanceCriteria {
			producerCriteria[criterion] = struct{}{}
		}
	}
	if len(producerCriteria) != len(workItem) {
		return fmt.Errorf("implementation criteria must uniquely cover work-item acceptance criteria")
	}
	for criterion := range workItem {
		if _, found := producerCriteria[criterion]; !found {
			return fmt.Errorf("implementation criteria must uniquely cover work-item acceptance criteria")
		}
	}
	integrationSet := make(map[string]struct{}, len(integrationCriteria))
	for _, criterion := range integrationCriteria {
		integrationSet[criterion] = struct{}{}
	}
	if len(integrationSet) != len(workItem) {
		return fmt.Errorf("integration criteria must exactly match work-item acceptance criteria")
	}
	for criterion := range workItem {
		if _, found := integrationSet[criterion]; !found {
			return fmt.Errorf("integration criteria must exactly match work-item acceptance criteria")
		}
	}
	return nil
}

// ValidateInputs applies the same key, bounds, duplicate and DAG checks used
// during persistence without requiring a database-backed plan.
func ValidateInputs(inputs []StepInput) error {
	_, err := Normalize(models.DeliveryPlan{ID: validationPlanID, WorkItemID: validationPlanID, Version: 1}, "", inputs, time.Time{})
	return err
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func integerValue(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int64:
		if int64(int(number)) != number {
			return 0, false
		}
		return int(number), true
	case float64:
		if number != float64(int(number)) {
			return 0, false
		}
		return int(number), true
	default:
		return 0, false
	}
}

func optionalStringList(value any, field string) ([]string, error) {
	if value == nil {
		return []string{}, nil
	}
	return stringList(value, field)
}

func stringList(value any, field string) ([]string, error) {
	var raw []any
	switch typed := value.(type) {
	case []any:
		raw = typed
	case []string:
		result := make([]string, len(typed))
		for index, item := range typed {
			result[index] = strings.TrimSpace(item)
			if result[index] == "" {
				return nil, fmt.Errorf("%s must contain non-empty strings", field)
			}
		}
		return result, nil
	default:
		return nil, fmt.Errorf("%s must be a list of strings", field)
	}
	result := make([]string, len(raw))
	for index, item := range raw {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("%s must contain non-empty strings", field)
		}
		result[index] = strings.TrimSpace(text)
	}
	return result, nil
}
