package automationagent

import (
	"fmt"
	"strings"
)

// ParseReadOnlyAssessment deliberately accepts a small report schema. It is
// not a change proposal and cannot be repurposed into a patch or publication.
func ParseReadOnlyAssessment(content string) (map[string]any, error) {
	value, ok := decodeJSONObject(strings.TrimSpace(content))
	if !ok {
		return nil, fmt.Errorf("read-only assessment response must be JSON")
	}
	summary := firstProposalString(value, "summary")
	verdict := strings.ToLower(strings.TrimSpace(firstProposalString(value, "verdict")))
	if summary == "" || len(summary) > maxCommandOutput || (verdict != "assessed" && verdict != "blocked") {
		return nil, fmt.Errorf("read-only assessment response is invalid")
	}
	for _, key := range []string{"evidence", "risks", "limitations", "recommended_next_steps"} {
		raw, present := value[key]
		entries, isList := raw.([]any)
		if !present || !isList || len(entries) > 12 {
			return nil, fmt.Errorf("read-only assessment %s is invalid", key)
		}
		for _, entry := range entries {
			text, isText := entry.(string)
			if !isText || strings.TrimSpace(text) == "" || len(text) > 1200 {
				return nil, fmt.Errorf("read-only assessment %s is invalid", key)
			}
		}
	}
	if _, hasPatch := value["patch"]; hasPatch {
		return nil, fmt.Errorf("read-only assessment must not contain a patch")
	}
	if _, hasPatches := value["patches"]; hasPatches {
		return nil, fmt.Errorf("read-only assessment must not contain patches")
	}
	return value, nil
}

func readOnlyAssessmentHandoff() map[string]any {
	return map[string]any{
		"mode":                 "read_only_assessment",
		"changed_repositories": []string{},
		"remote_actions":       []string{},
	}
}
