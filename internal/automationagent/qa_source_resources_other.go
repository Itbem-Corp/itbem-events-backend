//go:build !linux

package automationagent

import (
	"context"
	"fmt"
)

func qaSourceScratchRoot() (string, error) {
	return "", fmt.Errorf("server QA source acquisition requires Linux resource isolation")
}
func qaSourceCommandArguments(ctx context.Context, args []string) (string, []string) {
	return "git", args
}
