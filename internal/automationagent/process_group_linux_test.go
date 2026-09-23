//go:build linux

package automationagent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunLocalTimeoutCleansCommandProcessGroup(t *testing.T) {
	// The shell deliberately leaves a child sleep behind if only the direct
	// process is terminated. The runner must return promptly and clean the
	// whole group instead of leaking that child into the worker host.
	started := time.Now()
	result, err := runLocal(context.Background(), t.TempDir(), 80*time.Millisecond, "", "sh", "-c", "sleep 30")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("runLocal error = %v, result = %#v; want timeout", err, result)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timed out command took %s; process group cleanup was not prompt", elapsed)
	}
}
