package automationagent

import (
	"strings"
	"unicode/utf8"
)

// safePublicErrorMessage prepares diagnostics for callbacks and persisted
// result envelopes. Tool/provider errors can echo command output, which may
// contain credentials; never fall back to the raw message if sanitization
// produces an unusable value.
func safePublicErrorMessage(message string) string {
	if strings.TrimSpace(message) == "" {
		return ""
	}

	safe, _ := redactWorkspaceExcerpt(message)
	safe = strings.TrimSpace(safe)
	if safe == "" {
		return "automation task failed; inspect private execution evidence"
	}
	if len(safe) > maxErrorMessageLen {
		safe = safe[:maxErrorMessageLen]
		for !utf8.ValidString(safe) {
			safe = safe[:len(safe)-1]
		}
	}
	return safe
}

// RedactPublicError exposes the same final redaction boundary to the worker
// executable when it must write a diagnostic to stderr or structured logs.
func RedactPublicError(message string) string {
	return safePublicErrorMessage(message)
}
