// Package deliverydecomposition validates the bounded task graph proposed for
// a human delivery request. It intentionally has no persistence dependency so
// the same validation runs before an agent proposal is shown and before a
// reviewer materializes it into work items.
package deliverydecomposition

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	MaxTasks              = 12
	MaxItemsPerList       = 12
	MaxBudgetMicros int64 = 100_000_000_000
)

var taskKey = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

type Task struct {
	Key                string   `json:"key"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	ExpectedOutcome    string   `json:"expected_outcome"`
	IncludedScope      []string `json:"included_scope"`
	ExcludedScope      []string `json:"excluded_scope"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	ContextReferences  []string `json:"context_references"`
	DependsOn          []string `json:"depends_on"`
	BudgetMicros       int64    `json:"budget_microusd"`
}

type Proposal struct {
	Summary string `json:"summary"`
	Tasks   []Task `json:"tasks"`
}

// Parse rejects ambiguous graphs before any work item exists. Context is
// expressed by stable project references, never agent-selected filesystem
// paths or credentials.
func Parse(raw []byte) (Proposal, error) {
	var proposal Proposal
	if err := json.Unmarshal(raw, &proposal); err != nil {
		return Proposal{}, fmt.Errorf("decomposition must be a JSON object: %w", err)
	}
	proposal.Summary = compact(proposal.Summary)
	if proposal.Summary == "" || len(proposal.Summary) > 1000 {
		return Proposal{}, fmt.Errorf("decomposition summary is required and must be at most 1,000 characters")
	}
	if len(proposal.Tasks) == 0 || len(proposal.Tasks) > MaxTasks {
		return Proposal{}, fmt.Errorf("decomposition must contain between 1 and %d tasks", MaxTasks)
	}

	seen := make(map[string]struct{}, len(proposal.Tasks))
	for index := range proposal.Tasks {
		task := &proposal.Tasks[index]
		task.Key = strings.ToLower(compact(task.Key))
		task.Title, task.Description, task.ExpectedOutcome = compact(task.Title), compact(task.Description), compact(task.ExpectedOutcome)
		if !taskKey.MatchString(task.Key) || task.Title == "" || task.ExpectedOutcome == "" {
			return Proposal{}, fmt.Errorf("task %d requires a stable key, title and expected outcome", index+1)
		}
		if len(task.Title) > 240 || len(task.Description) > 4000 || len(task.ExpectedOutcome) > 2000 {
			return Proposal{}, fmt.Errorf("task %s contains an oversized text field", task.Key)
		}
		if task.BudgetMicros < 0 || task.BudgetMicros > MaxBudgetMicros {
			return Proposal{}, fmt.Errorf("task %s has an invalid budget", task.Key)
		}
		if _, exists := seen[task.Key]; exists {
			return Proposal{}, fmt.Errorf("task key %s is duplicated", task.Key)
		}
		seen[task.Key] = struct{}{}
		var err error
		if task.IncludedScope, err = list(task.IncludedScope, false); err != nil {
			return Proposal{}, fmt.Errorf("task %s included_scope: %w", task.Key, err)
		}
		if task.ExcludedScope, err = list(task.ExcludedScope, true); err != nil {
			return Proposal{}, fmt.Errorf("task %s excluded_scope: %w", task.Key, err)
		}
		if task.AcceptanceCriteria, err = list(task.AcceptanceCriteria, false); err != nil {
			return Proposal{}, fmt.Errorf("task %s acceptance_criteria: %w", task.Key, err)
		}
		if task.ContextReferences, err = references(task.ContextReferences); err != nil {
			return Proposal{}, fmt.Errorf("task %s context_references: %w", task.Key, err)
		}
		if task.DependsOn, err = dependencies(task.DependsOn, task.Key); err != nil {
			return Proposal{}, fmt.Errorf("task %s depends_on: %w", task.Key, err)
		}
	}
	for _, task := range proposal.Tasks {
		for _, dependency := range task.DependsOn {
			if _, exists := seen[dependency]; !exists {
				return Proposal{}, fmt.Errorf("task %s depends on unknown task %s", task.Key, dependency)
			}
		}
	}
	if hasCycle(proposal.Tasks) {
		return Proposal{}, fmt.Errorf("decomposition dependencies contain a cycle")
	}
	return proposal, nil
}

func compact(value string) string { return strings.Join(strings.Fields(strings.TrimSpace(value)), " ") }

func list(values []string, emptyAllowed bool) ([]string, error) {
	if len(values) > MaxItemsPerList {
		return nil, fmt.Errorf("contains too many items")
	}
	result, seen := make([]string, 0, len(values)), map[string]struct{}{}
	for _, value := range values {
		value = compact(value)
		if value == "" || len(value) > 500 {
			return nil, fmt.Errorf("contains an invalid item")
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	if !emptyAllowed && len(result) == 0 {
		return nil, fmt.Errorf("must not be empty")
	}
	return result, nil
}

func references(values []string) ([]string, error) {
	result, err := list(values, false)
	if err != nil {
		return nil, err
	}
	for _, value := range result {
		if !strings.HasPrefix(value, "workspace://") && !strings.HasPrefix(value, "github://") {
			return nil, fmt.Errorf("must use a project workspace:// or github:// reference")
		}
	}
	return result, nil
}

func dependencies(values []string, ownKey string) ([]string, error) {
	if len(values) > MaxTasks {
		return nil, fmt.Errorf("contains too many items")
	}
	result, seen := make([]string, 0, len(values)), map[string]struct{}{}
	for _, value := range values {
		value = strings.ToLower(compact(value))
		if !taskKey.MatchString(value) || value == ownKey {
			return nil, fmt.Errorf("contains an invalid task key")
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func hasCycle(tasks []Task) bool {
	byKey, state := make(map[string]Task, len(tasks)), map[string]uint8{}
	for _, task := range tasks {
		byKey[task.Key] = task
	}
	var visit func(string) bool
	visit = func(key string) bool {
		switch state[key] {
		case 1:
			return true
		case 2:
			return false
		}
		state[key] = 1
		for _, dependency := range byKey[key].DependsOn {
			if visit(dependency) {
				return true
			}
		}
		state[key] = 2
		return false
	}
	for _, task := range tasks {
		if visit(task.Key) {
			return true
		}
	}
	return false
}
