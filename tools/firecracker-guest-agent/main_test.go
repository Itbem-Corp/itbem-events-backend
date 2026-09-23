//go:build linux

package main

import "testing"

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
