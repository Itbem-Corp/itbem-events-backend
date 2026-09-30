package automationagent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInspectionRemovesCredentialAndPrivateAnalysis(t *testing.T) {
	input, count := InspectionContent("Authorization: Bearer synthetic-secret-value", false)
	if count == 0 || strings.Contains(input, "synthetic-secret-value") {
		t.Fatal("credential retained")
	}
	output, _ := InspectionContent(`{"answer":"visible answer","reasoning":"private hidden reasoning","analysis":"private analysis"}`, true)
	if !strings.Contains(output, "visible answer") || strings.Contains(output, "private hidden reasoning") || strings.Contains(output, "private analysis") {
		t.Fatalf("unsafe response projection: %s", output)
	}
	err := &RetryableError{Message: "provider network request failed", Cause: context.DeadlineExceeded}
	if InferenceFailureCode(err) != "provider_timeout" || strings.Contains(err.Error(), "deadline") {
		t.Fatal("unsafe or unclassified timeout")
	}
	if SafeInferenceFailureCode("provider_timeout") != "provider_timeout" {
		t.Fatal("timeout omitted from safe header")
	}
}

func TestProviderBodyDeadlineRemainsTerminalAndClassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"choices":`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	config, err := NewProviderConfig(ProviderDeepSeek, "deepseek-flash", server.URL, "synthetic-not-a-credential")
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Timeout = 50 * time.Millisecond
	_, err = newProviderTestClient(config, client).Complete(context.Background(), []Message{{Role: "user", Content: "synthetic"}}, 4096)
	if InferenceFailureCode(err) != "provider_timeout" {
		t.Fatalf("body deadline not classified: %v", err)
	}
	var retryable *RetryableError
	if errors.As(err, &retryable) {
		t.Fatal("partial response became automatically retryable")
	}
	if strings.Contains(err.Error(), server.URL) {
		t.Fatal("endpoint leaked")
	}
}
