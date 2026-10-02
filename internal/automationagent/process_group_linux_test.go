//go:build linux

package automationagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestQARevocationStopsRunningCommandTree(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	_, err := runQACommandWithAuthorityInterval(ctx, func(context.Context) (bool, error) {
		_, err := os.Stat(marker)
		return os.IsNotExist(err), nil
	}, func(commandCtx context.Context) (commandResult, error) {
		return runLocal(commandCtx, root, 30*time.Second, "", "sh", "-c", "sleep 30 & printf '%s' $! > child.pid; wait")
	}, 20*time.Millisecond)
	if !errors.Is(err, errQACapabilityNotAccepted) || time.Since(started) > 3*time.Second {
		t.Fatalf("revocation did not stop running command: %v", err)
	}
	raw, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatal(readErr)
	}
	pid, parseErr := strconv.Atoi(string(raw))
	if parseErr != nil || pid < 1 {
		t.Fatal("synthetic child identity invalid")
	}
	// A reaped child disappears; a killed child may briefly remain a zombie.
	// Neither may keep executing after the worker acknowledges revocation.
	stat, statErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if statErr == nil {
		fields := strings.Fields(string(stat))
		if len(fields) < 3 || fields[2] != "Z" {
			t.Fatalf("child remained executable after revocation: %s", stat)
		}
	} else if !os.IsNotExist(statErr) {
		t.Fatal(statErr)
	}
}

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
