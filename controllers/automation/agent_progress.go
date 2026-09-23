package automation

import "events-stocks/internal/automationagent"

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
	return false
}
