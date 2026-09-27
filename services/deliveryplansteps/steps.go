// Package deliveryplansteps owns the normalized, version-scoped step graph
// for DeliveryPlan. It intentionally stores only approved plan facts, not the
// private prompt, model reasoning, or execution result blobs.
package deliveryplansteps

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	MaxStepsPerPlan                        = 100
	MaxDependenciesPerPlan                 = 500
	MaxAcceptanceCriteria                  = 12
	MaxEvidenceRequirements                = 8
	MaxEvidenceTypesPerRequirement         = 6
	MaxEvidenceRequirementKeyBytes         = 48
	MaxEvidenceRequirementTitleBytes       = 120
	MaxEvidenceRequirementDescriptionBytes = 400
	MaxStepEvidenceBytes                   = 1 << 20
)

const (
	StepRoleImplementation = models.DeliveryPlanStepRoleImplementation
	StepRoleIntegration    = models.DeliveryPlanStepRoleIntegration
)

var (
	stepKeyPattern                = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	evidenceRequirementKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)
	idempotencyKeyPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	ErrPlanStepsConflict          = errors.New("normalized steps already exist with different plan content; create a new plan version")
)

var allowedStepEvidenceContentTypes = map[string]struct{}{
	"application/json": {},
	"image/jpeg":       {},
	"image/png":        {},
	"text/csv":         {},
	"text/markdown":    {},
	"text/plain":       {},
}

// EvidenceRequirement defines one bounded, reviewable artifact type for a
// plan step. It intentionally accepts only a small MIME allow-list; uploads
// remain capped by the signed callback's 1 MiB body limit.
type EvidenceRequirement struct {
	Key          string   `json:"key"`
	Title        string   `json:"title"`
	Description  string   `json:"description,omitempty"`
	Required     bool     `json:"required"`
	ContentTypes []string `json:"content_types"`
	MaxBytes     int64    `json:"max_bytes"`
}

// StepInput contains only the reviewable plan facts needed to create a step.
// It deliberately has no prompt, private reasoning, provider response, or
// arbitrary JSON/blob field.
type StepInput struct {
	Key                  string                `json:"step_key"`
	Role                 string                `json:"role"`
	Order                int                   `json:"order"`
	Title                string                `json:"title"`
	Objective            string                `json:"objective"`
	AcceptanceCriteria   []string              `json:"acceptance_criteria"`
	EvidenceRequirements []EvidenceRequirement `json:"evidence_requirements,omitempty"`
	DependsOn            []string              `json:"depends_on,omitempty"`
	IdempotencyKey       string                `json:"idempotency_key,omitempty"`
}

// StepDTO is an explicit safe projection. Worker ID, machine ID, run lease,
// and model evidence references remain server-side; consumers use the agent
// profile and related task/ledger API for authorized execution detail.
type StepDTO struct {
	ID                    string                `json:"id"`
	PlanID                string                `json:"plan_id"`
	PlanVersion           int                   `json:"plan_version"`
	StepKey               string                `json:"step_key"`
	Role                  string                `json:"role"`
	Order                 int                   `json:"order"`
	Title                 string                `json:"title"`
	Objective             string                `json:"objective"`
	AcceptanceCriteria    []string              `json:"acceptance_criteria"`
	EvidenceRequirements  []EvidenceRequirement `json:"evidence_requirements"`
	DependsOn             []string              `json:"depends_on"`
	Status                string                `json:"status"`
	AgentKey              string                `json:"agent_key,omitempty"`
	AutomationTaskID      string                `json:"automation_task_id,omitempty"`
	AutomationExecutionID string                `json:"automation_execution_id,omitempty"`
	StartedAt             *time.Time            `json:"started_at,omitempty"`
	CompletedAt           *time.Time            `json:"completed_at,omitempty"`
	CreatedAt             time.Time             `json:"created_at"`
	UpdatedAt             time.Time             `json:"updated_at"`
}

type NormalizedPlan struct {
	Steps        []models.DeliveryPlanStep
	Dependencies []models.DeliveryPlanStepDependency
}

func normalizeEvidenceRequirements(requirements []EvidenceRequirement) ([]EvidenceRequirement, error) {
	if len(requirements) > MaxEvidenceRequirements {
		return nil, fmt.Errorf("a step may have at most %d evidence requirements", MaxEvidenceRequirements)
	}
	result := make([]EvidenceRequirement, len(requirements))
	seenKeys := make(map[string]struct{}, len(requirements))
	for index, requirement := range requirements {
		requirement.Key = strings.TrimSpace(requirement.Key)
		requirement.Title = strings.TrimSpace(requirement.Title)
		requirement.Description = strings.TrimSpace(requirement.Description)
		if !evidenceRequirementKeyPattern.MatchString(requirement.Key) || len(requirement.Key) > MaxEvidenceRequirementKeyBytes {
			return nil, fmt.Errorf("requirement key must be a stable lowercase slug")
		}
		if _, duplicate := seenKeys[requirement.Key]; duplicate {
			return nil, fmt.Errorf("requirement keys must be unique")
		}
		seenKeys[requirement.Key] = struct{}{}
		if requirement.Title == "" || len(requirement.Title) > MaxEvidenceRequirementTitleBytes || len(requirement.Description) > MaxEvidenceRequirementDescriptionBytes {
			return nil, fmt.Errorf("requirement title or description exceeds its bound")
		}
		if requirement.MaxBytes < 1 || requirement.MaxBytes > MaxStepEvidenceBytes {
			return nil, fmt.Errorf("max_bytes must be between 1 and %d", MaxStepEvidenceBytes)
		}
		if len(requirement.ContentTypes) == 0 || len(requirement.ContentTypes) > MaxEvidenceTypesPerRequirement {
			return nil, fmt.Errorf("content_types must contain between 1 and %d allowed MIME types", MaxEvidenceTypesPerRequirement)
		}
		seenTypes := make(map[string]struct{}, len(requirement.ContentTypes))
		contentTypes := make([]string, 0, len(requirement.ContentTypes))
		for _, contentType := range requirement.ContentTypes {
			contentType = strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
			if _, allowed := allowedStepEvidenceContentTypes[contentType]; !allowed {
				return nil, fmt.Errorf("content type %q is not allowed", contentType)
			}
			if _, duplicate := seenTypes[contentType]; duplicate {
				return nil, fmt.Errorf("content_types must be unique")
			}
			seenTypes[contentType] = struct{}{}
			contentTypes = append(contentTypes, contentType)
		}
		sort.Strings(contentTypes)
		requirement.ContentTypes = contentTypes
		result[index] = requirement
	}
	return result, nil
}

func parseStoredEvidenceRequirements(raw string) ([]EvidenceRequirement, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" {
		return []EvidenceRequirement{}, nil
	}
	var requirements []EvidenceRequirement
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&requirements); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("trailing evidence requirement data")
	}
	return normalizeEvidenceRequirements(requirements)
}

// EvidenceRequirementsFromJSON exposes the strict, bounded parser for
// execution controllers that must validate a worker's requested requirement.
func EvidenceRequirementsFromJSON(raw string) ([]EvidenceRequirement, error) {
	return parseStoredEvidenceRequirements(raw)
}

func canonicalStoredEvidenceRequirements(raw string) string {
	requirements, err := parseStoredEvidenceRequirements(raw)
	if err != nil {
		return "<invalid>"
	}
	encoded, err := json.Marshal(requirements)
	if err != nil {
		return "<invalid>"
	}
	return string(encoded)
}

// Normalize validates keys, order and a same-plan dependency DAG, then builds
// deterministic idempotency keys scoped by the plan version. IDs are generated
// before edges so dependency rows can be written atomically with their steps.
func Normalize(plan models.DeliveryPlan, createdBy string, inputs []StepInput, now time.Time) (NormalizedPlan, error) {
	if plan.ID == uuid.Nil || plan.WorkItemID == uuid.Nil || plan.Version < 1 {
		return NormalizedPlan{}, fmt.Errorf("a persisted versioned delivery plan is required")
	}
	if len(inputs) > MaxStepsPerPlan {
		return NormalizedPlan{}, fmt.Errorf("a plan may contain at most %d steps", MaxStepsPerPlan)
	}
	createdBy = strings.TrimSpace(createdBy)
	if len(createdBy) > 128 {
		return NormalizedPlan{}, fmt.Errorf("created_by exceeds 128 characters")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	byKey := make(map[string]StepInput, len(inputs))
	byOrder := make(map[int]struct{}, len(inputs))
	byIdempotencyKey := make(map[string]string, len(inputs))
	dependencyCount := 0
	for index := range inputs {
		input := &inputs[index]
		input.Key = strings.TrimSpace(input.Key)
		input.Role = strings.TrimSpace(strings.ToLower(input.Role))
		input.Title = strings.TrimSpace(input.Title)
		input.Objective = strings.TrimSpace(input.Objective)
		if !stepKeyPattern.MatchString(input.Key) {
			return NormalizedPlan{}, fmt.Errorf("step_key must be a stable lowercase key")
		}
		if input.Role != StepRoleImplementation && input.Role != StepRoleIntegration {
			return NormalizedPlan{}, fmt.Errorf("step %q role must be implementation or integration", input.Key)
		}
		if input.Title == "" || len(input.Title) > 240 || len(input.Objective) > 5000 {
			return NormalizedPlan{}, fmt.Errorf("each step requires a title up to 240 characters and an objective up to 5,000 characters")
		}
		if input.Order < 0 {
			return NormalizedPlan{}, fmt.Errorf("step order must be non-negative")
		}
		if _, duplicate := byKey[input.Key]; duplicate {
			return NormalizedPlan{}, fmt.Errorf("step_key %q is duplicated", input.Key)
		}
		if _, duplicate := byOrder[input.Order]; duplicate {
			return NormalizedPlan{}, fmt.Errorf("step order %d is duplicated", input.Order)
		}
		byOrder[input.Order] = struct{}{}
		input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
		if input.IdempotencyKey == "" {
			input.IdempotencyKey = "step:" + input.Key
		}
		if !idempotencyKeyPattern.MatchString(input.IdempotencyKey) {
			return NormalizedPlan{}, fmt.Errorf("idempotency_key has an invalid format")
		}
		if previous, duplicate := byIdempotencyKey[input.IdempotencyKey]; duplicate && previous != input.Key {
			return NormalizedPlan{}, fmt.Errorf("idempotency_key is repeated across different steps")
		}
		byIdempotencyKey[input.IdempotencyKey] = input.Key
		if len(input.AcceptanceCriteria) == 0 || len(input.AcceptanceCriteria) > MaxAcceptanceCriteria {
			return NormalizedPlan{}, fmt.Errorf("step %q must have between 1 and %d acceptance criteria", input.Key, MaxAcceptanceCriteria)
		}
		normalizedRequirements, err := normalizeEvidenceRequirements(input.EvidenceRequirements)
		if err != nil {
			return NormalizedPlan{}, fmt.Errorf("step %q evidence requirements: %w", input.Key, err)
		}
		input.EvidenceRequirements = normalizedRequirements
		seenCriteria := make(map[string]struct{}, len(input.AcceptanceCriteria))
		for criteriaIndex := range input.AcceptanceCriteria {
			input.AcceptanceCriteria[criteriaIndex] = strings.TrimSpace(input.AcceptanceCriteria[criteriaIndex])
			if input.AcceptanceCriteria[criteriaIndex] == "" || len(input.AcceptanceCriteria[criteriaIndex]) > 400 {
				return NormalizedPlan{}, fmt.Errorf("step %q has an empty or oversized acceptance criterion", input.Key)
			}
			if _, duplicate := seenCriteria[input.AcceptanceCriteria[criteriaIndex]]; duplicate {
				return NormalizedPlan{}, fmt.Errorf("step %q repeats an acceptance criterion", input.Key)
			}
			seenCriteria[input.AcceptanceCriteria[criteriaIndex]] = struct{}{}
		}
		seenDependencies := make(map[string]struct{}, len(input.DependsOn))
		for dependencyIndex := range input.DependsOn {
			input.DependsOn[dependencyIndex] = strings.TrimSpace(input.DependsOn[dependencyIndex])
			dependencyKey := input.DependsOn[dependencyIndex]
			if _, duplicate := seenDependencies[dependencyKey]; duplicate {
				return NormalizedPlan{}, fmt.Errorf("step %q repeats dependency %q", input.Key, dependencyKey)
			}
			seenDependencies[dependencyKey] = struct{}{}
		}
		byKey[input.Key] = *input
		dependencyCount += len(input.DependsOn)
	}
	if dependencyCount > MaxDependenciesPerPlan {
		return NormalizedPlan{}, fmt.Errorf("a plan may contain at most %d step dependencies", MaxDependenciesPerPlan)
	}
	if err := validateDAG(byKey); err != nil {
		return NormalizedPlan{}, err
	}
	if err := validateIntegrationGraph(byKey); err != nil {
		return NormalizedPlan{}, err
	}

	ordered := append([]StepInput(nil), inputs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Order < ordered[j].Order })
	idsByKey := make(map[string]uuid.UUID, len(ordered))
	steps := make([]models.DeliveryPlanStep, 0, len(ordered))
	for _, input := range ordered {
		id, err := uuid.NewV4()
		if err != nil {
			return NormalizedPlan{}, fmt.Errorf("generate plan step identity: %w", err)
		}
		criteriaJSON, err := json.Marshal(input.AcceptanceCriteria)
		if err != nil {
			return NormalizedPlan{}, fmt.Errorf("encode acceptance criteria for %q: %w", input.Key, err)
		}
		evidenceRequirementsJSON, err := json.Marshal(input.EvidenceRequirements)
		if err != nil {
			return NormalizedPlan{}, fmt.Errorf("encode evidence requirements for %q: %w", input.Key, err)
		}
		steps = append(steps, models.DeliveryPlanStep{
			ID: id, PlanID: plan.ID, StepKey: input.Key, IdempotencyKey: input.IdempotencyKey,
			Role:         input.Role,
			DisplayOrder: input.Order, Title: input.Title, Objective: input.Objective,
			AcceptanceCriteriaJSON: string(criteriaJSON), EvidenceRequirementsJSON: string(evidenceRequirementsJSON), Status: models.DeliveryPlanStepPlanned,
			CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now,
		})
		idsByKey[input.Key] = id
	}
	dependencies := make([]models.DeliveryPlanStepDependency, 0, dependencyCount)
	for _, input := range ordered {
		dependsOn := append([]string(nil), input.DependsOn...)
		sort.Strings(dependsOn)
		for _, key := range dependsOn {
			id, err := uuid.NewV4()
			if err != nil {
				return NormalizedPlan{}, fmt.Errorf("generate plan dependency identity: %w", err)
			}
			dependencies = append(dependencies, models.DeliveryPlanStepDependency{
				ID: id, PlanID: plan.ID, StepID: idsByKey[input.Key], DependsOnStepID: idsByKey[key], CreatedAt: now,
			})
		}
	}
	return NormalizedPlan{Steps: steps, Dependencies: dependencies}, nil
}

// validateIntegrationGraph makes the merge/verification node part of the
// approved DAG instead of synthesizing it after approval. Producer criteria
// have one owner each; the integration node repeats their union only as an
// explicit final-verification role and must directly depend on every leaf.
func validateIntegrationGraph(byKey map[string]StepInput) error {
	if len(byKey) == 0 {
		return nil
	}
	var integration *StepInput
	producerCriteria := make(map[string]struct{})
	maxProducerOrder := -1
	successors := make(map[string]bool, len(byKey))
	for key, step := range byKey {
		if step.Role == StepRoleIntegration {
			if integration != nil {
				return fmt.Errorf("an executable plan must have exactly one integration step")
			}
			copy := step
			integration = &copy
			continue
		}
		if step.Role != StepRoleImplementation {
			return fmt.Errorf("step %q has an unsupported role", key)
		}
		if step.Order > maxProducerOrder {
			maxProducerOrder = step.Order
		}
		for _, criterion := range step.AcceptanceCriteria {
			if _, duplicate := producerCriteria[criterion]; duplicate {
				return fmt.Errorf("implementation acceptance criteria must have one producer owner")
			}
			producerCriteria[criterion] = struct{}{}
		}
		for _, dependency := range step.DependsOn {
			if byKey[dependency].Role == StepRoleIntegration {
				return fmt.Errorf("implementation steps cannot depend on the integration step")
			}
			successors[dependency] = true
		}
	}
	if integration == nil || len(producerCriteria) == 0 {
		return fmt.Errorf("an executable plan requires implementation steps and one explicit integration step")
	}
	if integration.Order <= maxProducerOrder {
		return fmt.Errorf("integration step must appear after implementation steps")
	}
	integrationCriteria := make(map[string]struct{}, len(integration.AcceptanceCriteria))
	for _, criterion := range integration.AcceptanceCriteria {
		integrationCriteria[criterion] = struct{}{}
	}
	if len(integrationCriteria) != len(producerCriteria) {
		return fmt.Errorf("integration final-verification criteria must exactly cover implementation criteria")
	}
	for criterion := range producerCriteria {
		if _, covered := integrationCriteria[criterion]; !covered {
			return fmt.Errorf("integration final-verification criteria must exactly cover implementation criteria")
		}
	}
	terminalBranches := make(map[string]struct{})
	for key, step := range byKey {
		if step.Role == StepRoleImplementation && !successors[key] {
			terminalBranches[key] = struct{}{}
		}
	}
	dependencies := make(map[string]struct{}, len(integration.DependsOn))
	for _, key := range integration.DependsOn {
		if byKey[key].Role != StepRoleImplementation {
			return fmt.Errorf("integration step may depend only on implementation branches")
		}
		dependencies[key] = struct{}{}
	}
	if len(dependencies) < len(terminalBranches) {
		return fmt.Errorf("integration step must directly depend on every terminal implementation branch")
	}
	for key := range terminalBranches {
		if _, found := dependencies[key]; !found {
			return fmt.Errorf("integration step must directly depend on every terminal implementation branch")
		}
	}
	return nil
}

func validateDAG(byKey map[string]StepInput) error {
	state := make(map[string]uint8, len(byKey))
	seenEdges := make(map[string]struct{})
	var visit func(string) error
	visit = func(key string) error {
		if state[key] == 1 {
			return fmt.Errorf("step dependencies contain a cycle")
		}
		if state[key] == 2 {
			return nil
		}
		state[key] = 1
		for _, dependency := range byKey[key].DependsOn {
			if _, exists := byKey[dependency]; !exists {
				return fmt.Errorf("step %q depends on unknown step %q", key, dependency)
			}
			if dependency == key {
				return fmt.Errorf("a step cannot depend on itself")
			}
			edgeKey := key + "\x00" + dependency
			if _, duplicate := seenEdges[edgeKey]; duplicate {
				return fmt.Errorf("step %q repeats dependency %q", key, dependency)
			}
			seenEdges[edgeKey] = struct{}{}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[key] = 2
		return nil
	}
	for key := range byKey {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}

// CanTransitionStatus centralizes the step lifecycle for the controller that
// will wire status updates later. Repeating the current valid state is
// idempotent; terminal steps cannot be reopened on the same plan version.
func CanTransitionStatus(current, next string) bool {
	current = strings.TrimSpace(strings.ToLower(current))
	next = strings.TrimSpace(strings.ToLower(next))
	valid := map[string]map[string]struct{}{
		models.DeliveryPlanStepPlanned: {
			models.DeliveryPlanStepPlanned: {}, models.DeliveryPlanStepReady: {}, models.DeliveryPlanStepBlocked: {}, models.DeliveryPlanStepSkipped: {},
		},
		models.DeliveryPlanStepReady: {
			models.DeliveryPlanStepReady: {}, models.DeliveryPlanStepRunning: {}, models.DeliveryPlanStepBlocked: {}, models.DeliveryPlanStepSkipped: {},
		},
		models.DeliveryPlanStepRunning: {
			models.DeliveryPlanStepRunning: {}, models.DeliveryPlanStepCompleted: {}, models.DeliveryPlanStepBlocked: {}, models.DeliveryPlanStepFailed: {},
		},
		models.DeliveryPlanStepBlocked: {
			models.DeliveryPlanStepBlocked: {}, models.DeliveryPlanStepReady: {}, models.DeliveryPlanStepSkipped: {},
		},
		models.DeliveryPlanStepFailed: {
			models.DeliveryPlanStepFailed: {}, models.DeliveryPlanStepReady: {}, models.DeliveryPlanStepSkipped: {},
		},
		models.DeliveryPlanStepCompleted: {models.DeliveryPlanStepCompleted: {}},
		models.DeliveryPlanStepSkipped:   {models.DeliveryPlanStepSkipped: {}},
	}
	_, allowed := valid[current][next]
	return allowed
}

// Ensure materializes one immutable step graph per DeliveryPlan version.
// Retrying the same normalized request returns the existing rows with
// created=false; changing content requires a new DeliveryPlan version.
func Ensure(db *gorm.DB, planID uuid.UUID, actor string, inputs []StepInput, now time.Time) ([]models.DeliveryPlanStep, bool, error) {
	if db == nil {
		return nil, false, fmt.Errorf("delivery plan database is unavailable")
	}
	var result []models.DeliveryPlanStep
	created := false
	err := db.Transaction(func(tx *gorm.DB) error {
		var ensureErr error
		result, created, ensureErr = EnsureInTransaction(tx, planID, actor, inputs, now)
		return ensureErr
	})
	return result, created, err
}

// EnsureInTransaction materializes one immutable graph while joining the
// caller's write transaction. This lets plan creation and its normalized steps
// commit atomically.
func EnsureInTransaction(tx *gorm.DB, planID uuid.UUID, actor string, inputs []StepInput, now time.Time) ([]models.DeliveryPlanStep, bool, error) {
	if tx == nil {
		return nil, false, fmt.Errorf("delivery plan transaction is unavailable")
	}
	var result []models.DeliveryPlanStep
	created := false
	var plan models.DeliveryPlan
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", planID).Error; err != nil {
		return nil, false, err
	}
	normalized, err := Normalize(plan, actor, inputs, now)
	if err != nil {
		return nil, false, err
	}
	var existing []models.DeliveryPlanStep
	if err := tx.Where("plan_id = ?", plan.ID).Order("display_order ASC").Find(&existing).Error; err != nil {
		return nil, false, err
	}
	if len(existing) > 0 {
		var existingDependencies []models.DeliveryPlanStepDependency
		if err := tx.Where("plan_id = ?", plan.ID).Find(&existingDependencies).Error; err != nil {
			return nil, false, err
		}
		if !samePlanContent(normalized, existing, existingDependencies) {
			return nil, false, ErrPlanStepsConflict
		}
		return existing, false, nil
	}
	if len(normalized.Steps) == 0 {
		return []models.DeliveryPlanStep{}, false, nil
	}
	if err := tx.Create(&normalized.Steps).Error; err != nil {
		return nil, false, err
	}
	if len(normalized.Dependencies) > 0 {
		if err := tx.Create(&normalized.Dependencies).Error; err != nil {
			return nil, false, err
		}
	}
	result = normalized.Steps
	created = true
	return result, created, nil
}

func samePlanContent(requested NormalizedPlan, existing []models.DeliveryPlanStep, existingDependencies []models.DeliveryPlanStepDependency) bool {
	if len(requested.Steps) != len(existing) || len(requested.Dependencies) != len(existingDependencies) {
		return false
	}
	requestedByKey := make(map[string]models.DeliveryPlanStep, len(requested.Steps))
	existingByKey := make(map[string]models.DeliveryPlanStep, len(existing))
	requestedKeyByID := make(map[uuid.UUID]string, len(requested.Steps))
	existingKeyByID := make(map[uuid.UUID]string, len(existing))
	for _, step := range requested.Steps {
		requestedByKey[step.StepKey] = step
		requestedKeyByID[step.ID] = step.StepKey
	}
	for _, step := range existing {
		existingByKey[step.StepKey] = step
		existingKeyByID[step.ID] = step.StepKey
	}
	for key, requestedStep := range requestedByKey {
		existingStep, found := existingByKey[key]
		if !found || requestedStep.IdempotencyKey != existingStep.IdempotencyKey || requestedStep.Role != existingStep.Role || requestedStep.DisplayOrder != existingStep.DisplayOrder || requestedStep.Title != existingStep.Title || requestedStep.Objective != existingStep.Objective || requestedStep.AcceptanceCriteriaJSON != existingStep.AcceptanceCriteriaJSON || requestedStep.EvidenceRequirementsJSON != canonicalStoredEvidenceRequirements(existingStep.EvidenceRequirementsJSON) {
			return false
		}
	}
	requestedEdges := make([]string, 0, len(requested.Dependencies))
	for _, edge := range requested.Dependencies {
		requestedEdges = append(requestedEdges, requestedKeyByID[edge.StepID]+"\x00"+requestedKeyByID[edge.DependsOnStepID])
	}
	existingEdges := make([]string, 0, len(existingDependencies))
	for _, edge := range existingDependencies {
		stepKey, stepOK := existingKeyByID[edge.StepID]
		dependsOnKey, dependencyOK := existingKeyByID[edge.DependsOnStepID]
		if !stepOK || !dependencyOK || edge.PlanID != requested.Steps[0].PlanID {
			return false
		}
		existingEdges = append(existingEdges, stepKey+"\x00"+dependsOnKey)
	}
	sort.Strings(requestedEdges)
	sort.Strings(existingEdges)
	for index := range requestedEdges {
		if requestedEdges[index] != existingEdges[index] {
			return false
		}
	}
	return true
}

// DTOs converts normalized rows into a stable project-safe response and drops
// machine identity, worker id, run lease, model evidence and private columns.
func DTOs(steps []models.DeliveryPlanStep, dependencies []models.DeliveryPlanStepDependency, planVersion int) ([]StepDTO, error) {
	if planVersion < 1 {
		return nil, fmt.Errorf("a persisted plan version is required")
	}
	planID := stepsPlanID(steps)
	keyByID := make(map[uuid.UUID]string, len(steps))
	stepIndex := make(map[uuid.UUID]int, len(steps))
	result := make([]StepDTO, 0, len(steps))
	for _, step := range steps {
		if step.ID == uuid.Nil || step.PlanID == uuid.Nil || (planID != uuid.Nil && step.PlanID != planID) {
			return nil, fmt.Errorf("steps do not belong to one persisted plan graph")
		}
		var criteria []string
		if err := json.Unmarshal([]byte(step.AcceptanceCriteriaJSON), &criteria); err != nil || criteria == nil {
			return nil, fmt.Errorf("step %q acceptance criteria are invalid", step.StepKey)
		}
		evidenceRequirements, err := parseStoredEvidenceRequirements(step.EvidenceRequirementsJSON)
		if err != nil {
			return nil, fmt.Errorf("step %q evidence requirements are invalid", step.StepKey)
		}
		result = append(result, StepDTO{
			ID: step.ID.String(), PlanID: step.PlanID.String(), PlanVersion: planVersion, StepKey: step.StepKey, Role: step.Role,
			Order: step.DisplayOrder, Title: step.Title, Objective: step.Objective, AcceptanceCriteria: criteria, EvidenceRequirements: evidenceRequirements,
			DependsOn: []string{}, Status: step.Status, AgentKey: strings.TrimSpace(step.AgentKey),
			AutomationTaskID: uuidPointerString(step.AutomationTaskID), AutomationExecutionID: uuidPointerString(step.AutomationExecutionID),
			StartedAt: step.StartedAt, CompletedAt: step.CompletedAt, CreatedAt: step.CreatedAt, UpdatedAt: step.UpdatedAt,
		})
		keyByID[step.ID] = step.StepKey
		stepIndex[step.ID] = len(result) - 1
	}
	for _, dependency := range dependencies {
		dependentIndex, dependentExists := stepIndex[dependency.StepID]
		dependencyKey, dependencyExists := keyByID[dependency.DependsOnStepID]
		if !dependentExists || !dependencyExists || planID == uuid.Nil || dependency.PlanID != planID {
			return nil, fmt.Errorf("step dependency does not belong to the supplied plan graph")
		}
		result[dependentIndex].DependsOn = append(result[dependentIndex].DependsOn, dependencyKey)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Order < result[j].Order })
	for index := range result {
		sort.Strings(result[index].DependsOn)
	}
	return result, nil
}

func uuidPointerString(value *uuid.UUID) string {
	if value == nil || *value == uuid.Nil {
		return ""
	}
	return value.String()
}

func stepsPlanID(steps []models.DeliveryPlanStep) uuid.UUID {
	if len(steps) == 0 {
		return uuid.Nil
	}
	return steps[0].PlanID
}
