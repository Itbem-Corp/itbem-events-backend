package automation

import "testing"

func TestLiveProgressAcceptsOnlyBoundedRuntimeMetadata(t *testing.T) {
	if !validAgentProgress("validating", 3) || !validAgentProgress("", 0) {
		t.Fatal("valid progress rejected")
	}
	for _, step := range []string{
		"plan/implement/claimed",
		"plan/abc_foo/working",
		"plan/step-001/thinking",
		"plan/step-001/reading",
		"plan/step-001/validating",
		"plan/abcdefghijklmnopqrs/working", // exactly 32 bytes
	} {
		if !validAgentProgress(step, 1) {
			t.Fatalf("valid plan-step progress rejected: %q", step)
		}
	}
	for _, step := range []string{"upload_secret", "free text", "<script>"} {
		if validAgentProgress(step, 1) {
			t.Fatal("untrusted progress accepted")
		}
	}
	for _, step := range []string{
		"plan/Implement/claimed",
		"plan/-invalid/working",
		"plan/implement/complete",
		"plan/abcdefghijklmnopqrst/working", // exceeds the 32-byte bound
		"plan/implement/working provider supplied prose",
	} {
		if validAgentProgress(step, 1) {
			t.Fatalf("malformed plan-step progress accepted: %q", step)
		}
	}
	if validAgentProgress("thinking", 7) || validAgentProgress("thinking", -1) {
		t.Fatal("unbounded call progress")
	}
}
