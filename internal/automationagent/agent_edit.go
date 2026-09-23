package automationagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// A bounded whole-file replacement avoids asking a model to calculate diff
// hunks. The generated diff still passes the same approved-path and Git gates.
func agentFileReplacement(ctx context.Context, taskID string, delivery json.RawMessage, reference, path, content string) (string, error) {
	if len(content) > maxCommandOutput || strings.ContainsAny(path, "\r\n\t \"") {
		return "", fmt.Errorf("edit exceeds the file budget or uses an unsupported path")
	}
	old, err := readAgentFile(ctx, taskID, delivery, reference, path)
	if err != nil {
		return "", err
	}
	if old == content {
		return "", fmt.Errorf("edit made no change")
	}
	lines := func(text string) []string {
		if text == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	}
	before, after := lines(old), lines(content)
	var diff strings.Builder
	fmt.Fprintf(&diff, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -%d,%d +%d,%d @@\n", path, path, path, path, min(1, len(before)), len(before), min(1, len(after)), len(after))
	for _, part := range []struct {
		prefix   string
		lines    []string
		original string
	}{{"-", before, old}, {"+", after, content}} {
		for _, line := range part.lines {
			diff.WriteString(part.prefix + line + "\n")
		}
		if len(part.lines) > 0 && !strings.HasSuffix(part.original, "\n") {
			diff.WriteString("\\ No newline at end of file\n")
		}
	}
	proposal := map[string]any{"summary": "Bounded file replacement", "patches": []any{map[string]string{"repository_ref": reference, "patch": diff.String()}}}
	raw, err := json.Marshal(proposal)
	return string(raw), err
}
