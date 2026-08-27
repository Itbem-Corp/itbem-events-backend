package automationagent

import (
	"fmt"
	"strings"
)

const maxProductIdeaText = 1200

// ParseProductIdeation turns a provider answer into a reviewable product
// decision brief. It is intentionally non-mutating: the result can inform a
// human request, but it cannot create work items or unlock a Delivery phase.
func ParseProductIdeation(content string) (map[string]any, error) {
	brief, ok := decodeJSONObject(content)
	if !ok {
		return nil, fmt.Errorf("product ideation must be one JSON object")
	}
	if !boundedProductIdeaText(brief["summary"]) {
		return nil, fmt.Errorf("product ideation requires a bounded summary")
	}
	directions, ok := brief["directions"].([]any)
	if !ok || len(directions) < 2 || len(directions) > 3 {
		return nil, fmt.Errorf("product ideation requires two or three directions")
	}
	names := map[string]struct{}{}
	for _, raw := range directions {
		direction, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("product ideation directions must be objects")
		}
		for _, field := range []string{"name", "user_outcome", "smallest_slice", "trade_off", "risk", "success_signal"} {
			if !boundedProductIdeaText(direction[field]) {
				return nil, fmt.Errorf("product ideation direction %s is required", field)
			}
		}
		name := strings.TrimSpace(stringAny(direction["name"]))
		if _, duplicate := names[name]; duplicate {
			return nil, fmt.Errorf("product ideation directions must have unique names")
		}
		names[name] = struct{}{}
	}
	recommendation, ok := brief["recommendation"].(map[string]any)
	if !ok || !boundedProductIdeaText(recommendation["direction"]) || !boundedProductIdeaText(recommendation["rationale"]) || !boundedProductIdeaText(recommendation["first_experiment"]) {
		return nil, fmt.Errorf("product ideation requires a bounded recommendation")
	}
	if _, exists := names[strings.TrimSpace(stringAny(recommendation["direction"]))]; !exists {
		return nil, fmt.Errorf("product ideation recommendation must name a proposed direction")
	}
	if questions, present := brief["open_questions"]; present {
		items, ok := questions.([]any)
		if !ok || len(items) > 6 {
			return nil, fmt.Errorf("product ideation open_questions must be a bounded list")
		}
		for _, item := range items {
			if !boundedProductIdeaText(item) {
				return nil, fmt.Errorf("product ideation open_questions must contain bounded strings")
			}
		}
	}
	return brief, nil
}

func boundedProductIdeaText(value any) bool {
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	return ok && text != "" && len(text) <= maxProductIdeaText
}
