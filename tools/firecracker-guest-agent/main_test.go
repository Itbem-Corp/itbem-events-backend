//go:build linux

package main

import (
	"strings"
	"testing"
	"time"
)

func TestGuestExecutionHasBoundedOutputAndDeadline(t *testing.T) {
	result := execute(request{Command: "/bin/sh", Args: []string{"-c", "printf '%20000s' x"}}, time.Second)
	if result.OK || result.Error != "guest output exceeded limit" || len(result.Stdout) != maxOutputBytes {
		t.Fatalf("excessive output was accepted: ok=%v bytes=%d error=%s", result.OK, len(result.Stdout), result.Error)
	}
	started := time.Now()
	result = execute(request{Command: "/bin/sh", Args: []string{"-c", "sleep 30 & wait"}}, 100*time.Millisecond)
	if result.OK || result.Error != "guest command timed out" || time.Since(started) > 3*time.Second {
		t.Fatalf("command descendants escaped the deadline: %#v", result)
	}
}

func TestGuestExecutionDoesNotInheritHostEnvironment(t *testing.T) {
	t.Setenv("ITBEM_SYNTHETIC_GUEST_CANARY", "fixture-value")
	result := execute(request{Command: "/bin/sh", Args: []string{"-c", "printf '%s' \"${ITBEM_SYNTHETIC_GUEST_CANARY-unset}\""}}, time.Second)
	if !result.OK || result.Stdout != "unset" {
		t.Fatal("guest execution inherited the caller environment")
	}
	result = execute(request{Command: "/bin/cat", Args: []string{strings.Repeat("x", 2049)}}, time.Second)
	if result.OK || result.Error != "command arguments exceed the guest contract" {
		t.Fatal("oversized arguments did not fail before execution")
	}
}

func TestGuestCommandAllowlistIsNarrow(t *testing.T) {
	for _, command := range []string{"/bin/sh", "/bin/sha256sum", "/bin/cat"} {
		if !allowed(command) {
			t.Fatalf("expected %q to be allowed", command)
		}
	}
	for _, command := range []string{"sh", "/usr/bin/env", "/bin/rm", ""} {
		if allowed(command) {
			t.Fatalf("expected %q to be rejected", command)
		}
	}
}
