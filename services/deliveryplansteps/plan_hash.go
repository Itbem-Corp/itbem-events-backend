package deliveryplansteps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"events-stocks/models"

	"github.com/gofrs/uuid"
)

// ApprovedPlanContentHash returns a stable digest of the approved plan's
// executable content. Mutable lifecycle/lease fields are intentionally
// excluded; step identity, ordering, objectives, criteria, dependencies and
// human approval identity are included so child assignments cannot silently
// drift to a different plan graph.
func ApprovedPlanContentHash(plan models.DeliveryPlan, steps []models.DeliveryPlanStep, dependencies []models.DeliveryPlanStepDependency) (string, error) {
	if plan.ID == uuid.Nil || plan.Version < 1 || plan.Status != "approved" || plan.ApprovedGateID == nil || *plan.ApprovedGateID == uuid.Nil {
		return "", invalidStepInput("an approved, gated plan is required to compute its execution hash")
	}
	if len(steps) == 0 || len(steps) > 512 || len(dependencies) > 4096 {
		return "", invalidStepInput("approved plan graph exceeds supported bounds")
	}

	type canonicalStep struct {
		ID                   string                `json:"id"`
		StepKey              string                `json:"step_key"`
		Role                 string                `json:"role"`
		DisplayOrder         int                   `json:"display_order"`
		Title                string                `json:"title"`
		Objective            string                `json:"objective"`
		AcceptanceCriteria   []string              `json:"acceptance_criteria"`
		EvidenceRequirements []EvidenceRequirement `json:"evidence_requirements,omitempty"`
	}
	type canonicalDependency struct {
		StepID      string `json:"step_id"`
		DependsOnID string `json:"depends_on_step_id"`
	}
	type canonicalPlan struct {
		SchemaVersion int                   `json:"schema_version"`
		PlanID        string                `json:"plan_id"`
		Version       int                   `json:"version"`
		ApprovedGate  string                `json:"approved_gate_id"`
		Steps         []canonicalStep       `json:"steps"`
		Dependencies  []canonicalDependency `json:"dependencies"`
	}

	stepIDs := make(map[uuid.UUID]struct{}, len(steps))
	stepKeys := make(map[string]struct{}, len(steps))
	keyByID := make(map[uuid.UUID]string, len(steps))
	inputByKey := make(map[string]StepInput, len(steps))
	canonicalSteps := make([]canonicalStep, 0, len(steps))
	for _, step := range steps {
		if step.ID == uuid.Nil || step.PlanID != plan.ID || strings.TrimSpace(step.StepKey) == "" || step.DisplayOrder < 0 || (step.Role != StepRoleImplementation && step.Role != StepRoleIntegration) {
			return "", invalidStepInput("approved plan contains an invalid step identity")
		}
		if _, exists := stepIDs[step.ID]; exists {
			return "", invalidStepInput("approved plan contains duplicate step identities")
		}
		if _, exists := stepKeys[step.StepKey]; exists {
			return "", invalidStepInput("approved plan contains duplicate step keys")
		}
		var criteria []string
		if err := json.Unmarshal([]byte(step.AcceptanceCriteriaJSON), &criteria); err != nil || criteria == nil || len(criteria) > 32 {
			return "", invalidStepInput("approved plan acceptance criteria are invalid")
		}
		evidenceRequirements, err := parseStoredEvidenceRequirements(step.EvidenceRequirementsJSON)
		if err != nil {
			return "", invalidStepInput("approved plan evidence requirements are invalid")
		}
		stepIDs[step.ID] = struct{}{}
		stepKeys[step.StepKey] = struct{}{}
		keyByID[step.ID] = step.StepKey
		canonicalSteps = append(canonicalSteps, canonicalStep{
			ID: step.ID.String(), StepKey: step.StepKey, Role: step.Role, DisplayOrder: step.DisplayOrder,
			Title: step.Title, Objective: step.Objective, AcceptanceCriteria: criteria, EvidenceRequirements: evidenceRequirements,
		})
		inputByKey[step.StepKey] = StepInput{Key: step.StepKey, Role: step.Role, Order: step.DisplayOrder, Title: step.Title, Objective: step.Objective, AcceptanceCriteria: criteria}
	}
	sort.Slice(canonicalSteps, func(i, j int) bool {
		if canonicalSteps[i].DisplayOrder != canonicalSteps[j].DisplayOrder {
			return canonicalSteps[i].DisplayOrder < canonicalSteps[j].DisplayOrder
		}
		return canonicalSteps[i].ID < canonicalSteps[j].ID
	})

	canonicalDependencies := make([]canonicalDependency, 0, len(dependencies))
	seenEdges := make(map[string]struct{}, len(dependencies))
	for _, edge := range dependencies {
		if edge.PlanID != plan.ID || edge.StepID == uuid.Nil || edge.DependsOnStepID == uuid.Nil || edge.StepID == edge.DependsOnStepID {
			return "", invalidStepInput("approved plan contains an invalid dependency")
		}
		if _, exists := stepIDs[edge.StepID]; !exists {
			return "", invalidStepInput("approved plan dependency references a missing step")
		}
		if _, exists := stepIDs[edge.DependsOnStepID]; !exists {
			return "", invalidStepInput("approved plan dependency references a missing prerequisite")
		}
		key := edge.StepID.String() + "\x00" + edge.DependsOnStepID.String()
		if _, exists := seenEdges[key]; exists {
			return "", invalidStepInput("approved plan contains a duplicate dependency")
		}
		seenEdges[key] = struct{}{}
		dependentKey, dependencyKey := keyByID[edge.StepID], keyByID[edge.DependsOnStepID]
		if dependentKey == "" || dependencyKey == "" {
			return "", invalidStepInput("approved plan dependency references a missing step")
		}
		dependent := inputByKey[dependentKey]
		dependent.DependsOn = append(dependent.DependsOn, dependencyKey)
		inputByKey[dependentKey] = dependent
		canonicalDependencies = append(canonicalDependencies, canonicalDependency{StepID: edge.StepID.String(), DependsOnID: edge.DependsOnStepID.String()})
	}
	if err := validateIntegrationGraph(inputByKey); err != nil {
		return "", invalidStepInput("approved plan integration graph is invalid: " + err.Error())
	}
	sort.Slice(canonicalDependencies, func(i, j int) bool {
		if canonicalDependencies[i].StepID != canonicalDependencies[j].StepID {
			return canonicalDependencies[i].StepID < canonicalDependencies[j].StepID
		}
		return canonicalDependencies[i].DependsOnID < canonicalDependencies[j].DependsOnID
	})

	payload, err := json.Marshal(canonicalPlan{
		SchemaVersion: 2,
		PlanID:        plan.ID.String(), Version: plan.Version, ApprovedGate: plan.ApprovedGateID.String(),
		Steps: canonicalSteps, Dependencies: canonicalDependencies,
	})
	if err != nil {
		return "", fmt.Errorf("encode approved delivery plan identity: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}
