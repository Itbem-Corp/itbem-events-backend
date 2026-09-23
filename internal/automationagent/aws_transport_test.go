package automationagent

import "testing"

func TestNormalizeLocalQueueURLUsesLoopbackEndpoint(t *testing.T) {
	got, err := normalizeLocalQueueURL(
		"http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/itbem-ai-local",
		"http://127.0.0.1:4566",
	)
	if err != nil {
		t.Fatalf("normalizeLocalQueueURL: %v", err)
	}
	want := "http://127.0.0.1:4566/000000000000/itbem-ai-local"
	if got != want {
		t.Fatalf("normalized URL = %q, want %q", got, want)
	}
}

func TestNormalizeLocalQueueURLLeavesRemoteURLUntouched(t *testing.T) {
	input := "https://sqs.us-east-1.amazonaws.com/123/queue"
	got, err := normalizeLocalQueueURL(input, "")
	if err != nil {
		t.Fatalf("normalizeLocalQueueURL: %v", err)
	}
	if got != input {
		t.Fatalf("remote URL changed to %q", got)
	}
}
