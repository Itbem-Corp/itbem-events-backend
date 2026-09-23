package automation

import "testing"

func TestLiveProgressAcceptsOnlyBoundedRuntimeMetadata(t *testing.T) {
	if !validAgentProgress("validating", 3) || !validAgentProgress("", 0) {
		t.Fatal("valid progress rejected")
	}
	for _, step := range []string{"upload_secret", "free text", "<script>"} {
		if validAgentProgress(step, 1) {
			t.Fatal("untrusted progress accepted")
		}
	}
	if validAgentProgress("thinking", 7) || validAgentProgress("thinking", -1) {
		t.Fatal("unbounded call progress")
	}
}
