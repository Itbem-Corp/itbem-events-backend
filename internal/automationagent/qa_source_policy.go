package automationagent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Operator policy is keyed by the parent GitHub repository, then relative
// gitlink path. A missing policy grants no child reads; no implicit default.
func LoadQASourceDependencyPolicy(lookup func(string) string, parent string) (map[string]string, error) {
	if !githubRepositoryNamePattern.MatchString(strings.ToLower(parent)) {
		return nil, fmt.Errorf("QA source policy parent invalid")
	}
	raw := strings.TrimSpace(lookup("ITBEM_QA_SOURCE_DEPENDENCIES_JSON"))
	if raw == "" {
		return map[string]string{}, nil
	}
	var all map[string]map[string]string
	if len(raw) > 32<<10 || json.Unmarshal([]byte(raw), &all) != nil || len(all) > 32 {
		return nil, fmt.Errorf("QA source dependency policy invalid")
	}
	result := map[string]string{}
	seen := map[string]bool{}
	for repository, children := range all {
		repository = strings.ToLower(repository)
		if !githubRepositoryNamePattern.MatchString(repository) || seen[repository] || len(children) > 16 {
			return nil, fmt.Errorf("QA source dependency policy invalid")
		}
		seen[repository] = true
		for path, child := range children {
			if !safeQADependencyPath(path) || !githubRepositoryNamePattern.MatchString(strings.ToLower(child)) {
				return nil, fmt.Errorf("QA source dependency policy invalid")
			}
			if repository == strings.ToLower(parent) {
				result[path] = strings.ToLower(child)
			}
		}
	}
	return result, nil
}
