package automationagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadGitHubCodeReviewContextFreezesExactRevisionAndDigest(t *testing.T) {
	baseSHA, headSHA := strings.Repeat("a", 40), strings.Repeat("b", 40)
	patch := "diff --git a/internal/guard.go b/internal/guard.go\n--- a/internal/guard.go\n+++ b/internal/guard.go\n@@ -1,3 +1,3 @@\n package internal\n-func guard(v string) bool { return false }\n+func guard(v string) bool { return strings.Contains(v, \"token\") }\n var stable = true\n"
	review, err := NewCodeReviewInput("github://itbem/example", baseSHA, headSHA, patch)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		revision := request.URL.Query().Get("ref")
		if request.URL.Path != "/repos/itbem/example/contents/internal/guard.go" || (revision != headSHA && revision != baseSHA) || request.Header.Get("Authorization") != "Bearer ephemeral" {
			t.Fatalf("context escaped exact repository revision: %s %s", request.URL.Path, request.URL.RawQuery)
		}
		content := "package internal\nfunc guard(v string) bool { return strings.Contains(v, \"token\") }\nvar stable = true\n"
		if revision == baseSHA {
			content = "package internal\nfunc guard(v string) bool { return false }\nvar stable = true\n"
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content)), "size": len(content)})
	}))
	defer server.Close()

	excerpts, err := ReadGitHubCodeReviewContext(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "ephemeral", review)
	if err != nil || requests != 2 || len(excerpts) != 2 || excerpts[0].File != "internal/guard.go" || excerpts[1].File != "internal/guard.go" || excerpts[0].Side != "base" || excerpts[1].Side != "head" || !strings.Contains(excerpts[1].Content, "strings.Contains") {
		t.Fatalf("exact source context was not frozen: %#v / requests=%d / err=%v", excerpts, requests, err)
	}
	bound, err := BindCodeReviewContext(review, excerpts)
	if err != nil || !validReviewDigest(bound.ContextSHA256) {
		t.Fatalf("source context digest was not sealed: %#v / %v", bound, err)
	}
	bound.Context[0].Content += "tampered"
	raw, _ := json.Marshal(bound)
	if _, err := ParseCodeReviewInput(raw); err == nil || !strings.Contains(err.Error(), "source context does not match") {
		t.Fatalf("tampered source context was accepted: %v", err)
	}
}

func TestCodeReviewContextWindowsMergeNearbyRangesButKeepSidesSeparate(t *testing.T) {
	windows := codeReviewContextWindows([]CodeReviewChangedLineRange{
		{File: "a.go", Side: "head", Start: 10, End: 12},
		{File: "a.go", Side: "head", Start: 30, End: 31},
		{File: "a.go", Side: "base", Start: 10, End: 10},
	})
	if len(windows) != 2 || windows[0].side != "base" || windows[1].side != "head" || windows[1].start != 10 || windows[1].end != 31 {
		t.Fatalf("context windows were not deterministically merged: %#v", windows)
	}
}
