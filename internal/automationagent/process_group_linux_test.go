//go:build linux

package automationagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	// SIGKILL has been sent to the entire group, but observing the shell's
	// exit does not synchronize with the child's final scheduler transition.
	// Require the child to disappear or become a zombie within a bounded
	// interval; a leaked sleep remains executable for 30 seconds and fails.
	deadline := time.Now().Add(time.Second)
	for {
		stat, statErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		// procfs can open the entry before exit and then report ESRCH on
		// read. Both ENOENT and ESRCH mean this child no longer exists.
		if os.IsNotExist(statErr) || errors.Is(statErr, syscall.ESRCH) {
			break
		}
		if statErr != nil {
			t.Fatal(statErr)
		}
		fields := strings.Fields(string(stat))
		if len(fields) >= 3 && fields[2] == "Z" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child remained executable after bounded revocation cleanup: %s", stat)
		}
		time.Sleep(10 * time.Millisecond)
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
