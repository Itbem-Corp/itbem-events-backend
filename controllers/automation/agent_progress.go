package automation

import (
	"regexp"

	"events-stocks/internal/automationagent"
)

// Step keys are intentionally truncated by the agent runtime so this label
// remains within the 32-byte live-progress contract. Only the bounded prefix
// and lifecycle action are exposed; no arbitrary provider text crosses the
// progress channel.
var planStepProgressPattern = regexp.MustCompile(`^plan/[a-z][a-z0-9_-]{0,18}/(claimed|working|thinking|reading|validating)$`)

// Only runtime-owned labels cross the live channel: no chain of thought,
// source code, credentials or arbitrary provider prose.
func validAgentProgress(step string, call int) bool {
	if call < 0 || call > automationagent.AgentMaxCalls {
		return false
	}
	switch step {
	case "", "thinking", "reading", "validating", "repairing", "acceptance":
		return true
	}
	return len(step) <= 32 && planStepProgressPattern.MatchString(step)
}
