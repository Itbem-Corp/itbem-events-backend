package automationagent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// A missing observation is not a passing observation. Keep this gate outside
// model prose so a confidently phrased answer cannot manufacture QA success.
func validateQASuccessEvidence(report, execution map[string]any) error {
	for _, field := range []string{"defects", "coverage_gaps"} {
		entries, ok := report[field].([]any)
		if !ok || len(entries) > 0 {
			return fmt.Errorf("QA cannot pass with missing or unresolved %s", field)
		}
	}
	checks, ok := report["checks"].([]any)
	if !ok || len(checks) == 0 {
		return fmt.Errorf("QA cannot pass without reported checks")
	}
	for _, raw := range checks {
		check, ok := raw.(map[string]any)
		if !ok || check["status"] != "passed" {
			return fmt.Errorf("QA cannot pass with failed, skipped or unknown reported checks")
		}
	}
	preview, ok := execution["preview"].(map[string]any)
	if !ok || preview["passed"] != true {
		return fmt.Errorf("QA requires a positively observed preview")
	}
	runs, ok := execution["repository_runs"].([]any)
	if !ok || len(runs) == 0 {
		return fmt.Errorf("QA requires observed repository execution")
	}
	for _, raw := range runs {
		run, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("QA repository execution is malformed")
		}
		commands, ok := run["commands"].([]any)
		if !ok {
			return fmt.Errorf("QA command evidence is missing")
		}
		if contract, ok := run["execution_contract"].(map[string]any); ok && len(commands) == 0 && (contract["run_validation"] == true || contract["run_qa"] == true) {
			return fmt.Errorf("QA omitted required commands")
		}
		for _, raw := range commands {
			command, ok := raw.(map[string]any)
			if !ok || command["passed"] != true {
				return fmt.Errorf("QA command did not positively pass")
			}
		}
	}
	for _, field := range []string{"semantic", "screenshot"} {
		if raw, present := execution[field]; present {
			evidence, ok := raw.(map[string]any)
			if !ok || evidence["passed"] != true {
				return fmt.Errorf("QA %s evidence is incomplete", field)
			}
		}
	}
	if raw, present := execution["screenshots"]; present {
		captures, ok := raw.([]any)
		if !ok || len(captures) == 0 {
			return fmt.Errorf("QA screenshot evidence is incomplete")
		}
		for _, raw := range captures {
			capture, ok := raw.(map[string]any)
			if !ok || capture["passed"] != true {
				return fmt.Errorf("QA screenshot did not positively pass")
			}
		}
	}
	return nil
}

var evidenceIDPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

// Ground the release draft in the immutable evidence IDs supplied by the
// control plane. This proves reference membership, not semantic correctness;
// an independent reviewer still evaluates whether a cited fact follows.
func ValidateDeliverySummaryEvidence(summary map[string]any, delivery json.RawMessage) error {
	var input struct {
		Gates    []json.RawMessage `json:"gates"`
		Evidence []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"evidence"`
	}
	if json.Unmarshal(delivery, &input) != nil {
		return fmt.Errorf("delivery summary evidence context is invalid")
	}
	allowed := map[string]string{}
	for _, item := range input.Evidence {
		if evidenceIDPattern.FindString(item.ID) == item.ID && item.ID != "" && strings.TrimSpace(item.Title) != "" {
			allowed[strings.ToLower(item.ID)] = item.Title
		}
	}
	technical, ok := summary["technical"].(map[string]any)
	if !ok {
		return fmt.Errorf("delivery summary technical evidence is required")
	}
	decisions, ok := technical["decisions"].([]any)
	if !ok || (len(input.Gates) > 0 && len(decisions) == 0) {
		return fmt.Errorf("delivery summary must acknowledge recorded human decisions")
	}
	if len(input.Gates) == 0 && len(decisions) > 0 {
		return fmt.Errorf("delivery summary cannot invent human decisions when no gates are recorded")
	}
	if len(input.Gates) > 0 {
		decisionText := make([]string, 0, len(decisions))
		for _, raw := range decisions {
			text, ok := raw.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return fmt.Errorf("delivery summary human decisions must be non-empty strings")
			}
			decisionText = append(decisionText, strings.ToLower(strings.TrimSpace(text)))
		}
		joinedDecisions := strings.Join(decisionText, " ")
		for _, rawGate := range input.Gates {
			var gate struct {
				Kind     string `json:"kind"`
				Decision string `json:"decision"`
			}
			if err := json.Unmarshal(rawGate, &gate); err != nil || strings.TrimSpace(gate.Kind) == "" || strings.TrimSpace(gate.Decision) == "" {
				return fmt.Errorf("recorded human gate is incomplete")
			}
			if !summaryMentionsGateKind(joinedDecisions, gate.Kind) || !summaryMentionsGateDecision(joinedDecisions, gate.Decision) {
				return fmt.Errorf("delivery summary human decisions are not grounded in the recorded %s gate", gate.Kind)
			}
		}
	}
	entries, ok := technical["evidence"].([]any)
	if !ok || len(entries) == 0 || len(allowed) == 0 {
		return fmt.Errorf("delivery summary requires recorded evidence before release review")
	}
	for _, entry := range entries {
		text, ok := entry.(string)
		if !ok {
			return fmt.Errorf("delivery summary evidence entry is invalid")
		}
		references := evidenceIDPattern.FindAllString(text, -1)
		if len(references) == 0 {
			return fmt.Errorf("each delivery summary evidence entry must cite a recorded evidence ID")
		}
		for _, id := range references {
			title, exists := allowed[strings.ToLower(id)]
			if !exists || !strings.Contains(text, title) {
				return fmt.Errorf("delivery summary cites an unknown evidence ID or mismatched title")
			}
		}
	}
	return nil
}

func normalizedSummaryToken(value string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(strings.ToLower(strings.TrimSpace(value)))), " ")
}

func summaryMentionsGateKind(decisions, kind string) bool {
	decisions = normalizedSummaryToken(decisions)
	kind = normalizedSummaryToken(kind)
	if kind == "" {
		return false
	}
	if strings.Contains(decisions, kind) {
		return true
	}
	// The UI and model use the human labels while the durable contract uses
	// compact gate keys. Keep aliases deterministic and bounded.
	switch kind {
	case "code review":
		return strings.Contains(decisions, "implementation") || strings.Contains(decisions, "code review")
	case "qa review":
		return strings.Contains(decisions, "qa")
	case "release review":
		return strings.Contains(decisions, "release")
	default:
		return false
	}
}

func summaryMentionsGateDecision(decisions, decision string) bool {
	decisions = normalizedSummaryToken(decisions)
	decision = normalizedSummaryToken(decision)
	if strings.Contains(decisions, decision) {
		return true
	}
	switch decision {
	case "approved", "approve":
		return strings.Contains(decisions, "approval")
	case "request changes", "changes requested":
		return strings.Contains(decisions, "rework") || strings.Contains(decisions, "changes requested") || strings.Contains(decisions, "request changes")
	case "rejected", "reject":
		return strings.Contains(decisions, "denied") || strings.Contains(decisions, "declined")
	default:
		return false
	}
}
