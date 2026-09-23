package delivery

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"events-stocks/models"
)

const deliveryMandateVersion = 1

// deliveryAutonomyMandate is the durable ceiling for one work item. The
// phase policy remains a second, narrower control: a model can never gain a
// capability by asking for a different phase or by changing its prompt.
type deliveryAutonomyMandate struct {
	Version               int      `json:"version"`
	Objective             string   `json:"objective"`
	IncludedScope         []string `json:"included_scope"`
	ExcludedScope         []string `json:"excluded_scope"`
	RepositoryRefs        []string `json:"repository_refs"`
	AllowedTools          []string `json:"allowed_tools"`
	EffectiveAllowedTools []string `json:"effective_allowed_tools,omitempty"`
	MaxConcurrency        int      `json:"max_concurrency"`
	BudgetMicros          int64    `json:"budget_microusd"`
	AutonomyPolicy        string   `json:"autonomy_policy"`
	StopConditions        []string `json:"stop_conditions"`
	HumanActions          []string `json:"human_actions"`
}

var deliveryMandateAllowedTools = map[string]struct{}{
	"context.read":         {},
	"evidence.read":        {},
	"evidence.record":      {},
	"plan.propose":         {},
	"conversation.respond": {},
	"repository.read":      {},
	"worktree.create":      {},
	"patch.apply":          {},
	"test.run":             {},
	"artifact.capture":     {},
	"git.commit":           {},
	"github.pr.create":     {},
	"report.write":         {},
}

var deliveryMandateStopConditions = map[string]struct{}{
	"scope_exceeded":              {},
	"budget_exhausted":            {},
	"evidence_missing":            {},
	"human_gate_required":         {},
	"uncertain_external_effect":   {},
	"context_stale_or_incomplete": {},
	"concurrency_limit_reached":   {},
}

var deliveryMandateHumanActions = map[string]struct{}{
	"approve_plan":          {},
	"approve_code_review":   {},
	"approve_qa":            {},
	"approve_release":       {},
	"authorize_publication": {},
	"resolve_blocker":       {},
}

var deliveryMandatePhaseTools = map[string][]string{
	"plan":           {"context.read", "evidence.read", "plan.propose"},
	"chat":           {"context.read", "evidence.read", "conversation.respond"},
	"implementation": {"context.read", "repository.read", "worktree.create", "patch.apply", "test.run", "evidence.record"},
	"publish":        {"context.read", "evidence.read", "git.commit", "github.pr.create", "evidence.record"},
	"qa":             {"context.read", "repository.read", "test.run", "artifact.capture", "evidence.record"},
	"summary":        {"context.read", "evidence.read", "report.write", "evidence.record"},
}

func defaultDeliveryMandate(item models.DeliveryWorkItem, repositoryRefs []string) deliveryAutonomyMandate {
	objective := strings.TrimSpace(item.ExpectedOutcome)
	if objective == "" {
		objective = strings.TrimSpace(item.Title)
	}
	if objective == "" {
		objective = "bounded delivery task"
	}
	return deliveryAutonomyMandate{
		Version:        deliveryMandateVersion,
		Objective:      objective,
		IncludedScope:  decodeDeliveryStringList(item.IncludedScopeJSON),
		ExcludedScope:  decodeDeliveryStringList(item.ExcludedScopeJSON),
		RepositoryRefs: uniqueDeliveryStrings(repositoryRefs),
		AllowedTools:   sortedDeliveryKeys(deliveryMandateAllowedTools),
		MaxConcurrency: 1,
		BudgetMicros:   item.BudgetMicros,
		AutonomyPolicy: "bounded_autonomy",
		StopConditions: sortedDeliveryKeys(deliveryMandateStopConditions),
		HumanActions:   sortedDeliveryKeys(deliveryMandateHumanActions),
	}
}

func resolveDeliveryMandate(item models.DeliveryWorkItem, snapshots []models.DeliveryContextSnapshot) (deliveryAutonomyMandate, error) {
	raw := strings.TrimSpace(item.MandateJSON)
	if raw == "" || raw == "{}" {
		references := make([]string, 0, len(snapshots))
		for _, snapshot := range snapshots {
			if strings.EqualFold(strings.TrimSpace(snapshot.Kind), "repository") {
				references = append(references, snapshot.Reference)
			}
		}
		return validateDeliveryMandate(defaultDeliveryMandate(item, references))
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var mandate deliveryAutonomyMandate
	if err := decoder.Decode(&mandate); err != nil {
		return deliveryAutonomyMandate{}, fmt.Errorf("stored autonomy mandate is invalid: %w", err)
	}
	return validateDeliveryMandate(mandate)
}

func validateDeliveryMandate(mandate deliveryAutonomyMandate) (deliveryAutonomyMandate, error) {
	if mandate.Version != deliveryMandateVersion {
		return deliveryAutonomyMandate{}, fmt.Errorf("unsupported autonomy mandate version %d", mandate.Version)
	}
	mandate.Objective = strings.TrimSpace(mandate.Objective)
	if mandate.Objective == "" || len(mandate.Objective) > 4000 {
		return deliveryAutonomyMandate{}, fmt.Errorf("autonomy mandate objective is required and bounded")
	}
	if mandate.MaxConcurrency < 1 || mandate.MaxConcurrency > 8 {
		return deliveryAutonomyMandate{}, fmt.Errorf("autonomy mandate max_concurrency must be between 1 and 8")
	}
	if mandate.BudgetMicros < 0 {
		return deliveryAutonomyMandate{}, fmt.Errorf("autonomy mandate budget cannot be negative")
	}
	if mandate.AutonomyPolicy != "bounded_autonomy" {
		return deliveryAutonomyMandate{}, fmt.Errorf("autonomy mandate policy is not allowlisted")
	}
	var err error
	mandate.IncludedScope, err = validateDeliveryMandateStrings(mandate.IncludedScope, "included_scope", 200)
	if err != nil {
		return deliveryAutonomyMandate{}, err
	}
	mandate.ExcludedScope, err = validateDeliveryMandateStrings(mandate.ExcludedScope, "excluded_scope", 200)
	if err != nil {
		return deliveryAutonomyMandate{}, err
	}
	mandate.RepositoryRefs, err = validateDeliveryMandateStrings(mandate.RepositoryRefs, "repository_refs", 100)
	if err != nil {
		return deliveryAutonomyMandate{}, err
	}
	for _, reference := range mandate.RepositoryRefs {
		if !isDeliveryRepositoryReference(reference) {
			return deliveryAutonomyMandate{}, fmt.Errorf("autonomy mandate repository reference is invalid")
		}
	}
	mandate.AllowedTools, err = validateDeliveryMandateEnumList(mandate.AllowedTools, deliveryMandateAllowedTools, "allowed_tools")
	if err != nil {
		return deliveryAutonomyMandate{}, err
	}
	mandate.StopConditions, err = validateDeliveryMandateEnumList(mandate.StopConditions, deliveryMandateStopConditions, "stop_conditions")
	if err != nil {
		return deliveryAutonomyMandate{}, err
	}
	mandate.HumanActions, err = validateDeliveryMandateEnumList(mandate.HumanActions, deliveryMandateHumanActions, "human_actions")
	if err != nil {
		return deliveryAutonomyMandate{}, err
	}
	if len(mandate.StopConditions) == 0 || len(mandate.HumanActions) == 0 {
		return deliveryAutonomyMandate{}, fmt.Errorf("autonomy mandate must define stop conditions and human actions")
	}
	return mandate, nil
}

func effectiveDeliveryMandate(mandate deliveryAutonomyMandate, phase string) deliveryAutonomyMandate {
	mandate.EffectiveAllowedTools = append([]string(nil), deliveryMandatePhaseTools[phase]...)
	if len(mandate.EffectiveAllowedTools) == 0 {
		mandate.EffectiveAllowedTools = []string{"context.read", "evidence.read"}
	}
	allowed := make(map[string]struct{}, len(mandate.AllowedTools))
	for _, tool := range mandate.AllowedTools {
		allowed[tool] = struct{}{}
	}
	filtered := mandate.EffectiveAllowedTools[:0]
	for _, tool := range mandate.EffectiveAllowedTools {
		if _, ok := allowed[tool]; ok {
			filtered = append(filtered, tool)
		}
	}
	mandate.EffectiveAllowedTools = filtered
	return mandate
}

func marshalDeliveryMandate(mandate deliveryAutonomyMandate) (string, error) {
	validated, err := validateDeliveryMandate(mandate)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(validated)
	if err != nil {
		return "", fmt.Errorf("encode autonomy mandate: %w", err)
	}
	return string(encoded), nil
}

func deliveryWorkItemMandateJSON(item models.DeliveryWorkItem) json.RawMessage {
	raw := strings.TrimSpace(item.MandateJSON)
	if raw == "" || raw == "{}" {
		mandate, err := resolveDeliveryMandate(item, nil)
		if err != nil {
			return json.RawMessage(`{}`)
		}
		encoded, err := json.Marshal(mandate)
		if err != nil {
			return json.RawMessage(`{}`)
		}
		return json.RawMessage(encoded)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil || object == nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func decodeDeliveryStringList(raw string) []string {
	var values []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &values); err != nil {
		return nil
	}
	return uniqueDeliveryStrings(values)
}

func validateDeliveryMandateStrings(values []string, field string, max int) ([]string, error) {
	if len(values) > max {
		return nil, fmt.Errorf("autonomy mandate %s is too large", field)
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 500 {
			return nil, fmt.Errorf("autonomy mandate %s contains an invalid value", field)
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result, nil
}

func validateDeliveryMandateEnumList(values []string, allowlist map[string]struct{}, field string) ([]string, error) {
	result, err := validateDeliveryMandateStrings(values, field, 32)
	if err != nil {
		return nil, err
	}
	for _, value := range result {
		if _, ok := allowlist[value]; !ok {
			return nil, fmt.Errorf("autonomy mandate %s contains an unallowlisted value", field)
		}
	}
	sort.Strings(result)
	return result, nil
}

func uniqueDeliveryStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func sortedDeliveryKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
