package automationagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadGitHubReleaseBranchEvidenceCombinesClassicRulesetsAndExactChecks(t *testing.T) {
	head := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer repository-token" {
			t.Fatal("branch evidence must use only the repository-scoped token")
		}
		switch request.URL.Path {
		case "/repos/example/service/branches/release/stable":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"name": "release/stable", "protected": true,
				"protection": map[string]any{"required_status_checks": map[string]any{
					"contexts": []string{"classic-ci", "legacy-ci"},
					"checks":   []map[string]any{{"context": "classic-ci", "app_id": 11}},
				}},
			})
		case "/repos/example/service/rules/branches/release/stable":
			_ = json.NewEncoder(response).Encode([]map[string]any{
				{"type": "required_status_checks", "parameters": map[string]any{"required_status_checks": []map[string]any{
					{"context": "ruleset-ci", "integration_id": 22}, {"context": "any-source"},
				}}},
			})
		case "/repos/example/service/commits/" + head + "/check-runs":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"total_count": 5,
				"check_runs": []map[string]any{
					{"id": 1, "name": "classic-ci", "head_sha": head, "status": "completed", "conclusion": "failure", "app": map[string]int64{"id": 11}},
					{"id": 2, "name": "classic-ci", "head_sha": head, "status": "completed", "conclusion": "success", "app": map[string]int64{"id": 11}},
					{"id": 3, "name": "ruleset-ci", "head_sha": head, "status": "completed", "conclusion": "neutral", "app": map[string]int64{"id": 22}},
					{"id": 4, "name": "any-source", "head_sha": head, "status": "completed", "conclusion": "skipped", "app": map[string]int64{"id": 33}},
					{"id": 5, "name": "pending-ci", "head_sha": head, "status": "in_progress", "app": map[string]int64{"id": 44}},
				},
			})
		case "/repos/example/service/commits/" + head + "/status":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"sha": head,
				"statuses": []map[string]any{
					{"id": 8, "context": "legacy-ci", "state": "failure"},
					{"id": 9, "context": "legacy-ci", "state": "success"},
				},
			})
		default:
			t.Fatalf("unexpected branch evidence request: %s", request.URL.String())
		}
	}))
	defer server.Close()

	evidence, err := ReadGitHubReleaseBranchEvidence(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "repository-token", "example/service", "release/stable", head)
	if err != nil || !evidence.ProtectionEvaluated || !evidence.Protected || len(evidence.RequiredChecks) != 4 || len(evidence.Checks) != 5 {
		t.Fatalf("unexpected branch evidence: %#v / %v", evidence, err)
	}
	wantRequired := map[string]int64{"any-source": 0, "classic-ci": 11, "legacy-ci": 0, "ruleset-ci": 22}
	for _, check := range evidence.RequiredChecks {
		if integration, ok := wantRequired[check.Name]; !ok || integration != check.IntegrationID {
			t.Fatalf("unexpected required check identity: %#v", check)
		}
	}
	states := map[string]string{}
	for _, check := range evidence.Checks {
		states[check.Name] = string(check.Status)
	}
	if states["classic-ci"] != "passed" || states["ruleset-ci"] != "passed" || states["any-source"] != "passed" || states["legacy-ci"] != "passed" || states["pending-ci"] != "failed" {
		t.Fatalf("GitHub result mapping is wrong: %#v", evidence.Checks)
	}
}

func TestReadGitHubReleaseBranchEvidenceRejectsTruncatedRules(t *testing.T) {
	head := strings.Repeat("b", 40)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.Contains(request.URL.Path, "/branches/"):
			_ = json.NewEncoder(response).Encode(map[string]any{"name": "main", "protected": true, "protection": map[string]any{}})
		case strings.Contains(request.URL.Path, "/rules/branches/"):
			response.Header().Set("Link", `<https://api.github.test/next>; rel="next"`)
			_ = json.NewEncoder(response).Encode([]any{})
		default:
			t.Fatalf("truncated rules must fail before checks: %s", request.URL.String())
		}
	}))
	defer server.Close()

	if _, err := ReadGitHubReleaseBranchEvidence(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "repository-token", "example/service", "main", head); err == nil {
		t.Fatal("paginated branch rules were treated as complete")
	}
}

func TestReadGitHubReleaseBranchEvidenceRejectsTruncatedCheckRuns(t *testing.T) {
	head := strings.Repeat("c", 40)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.Contains(request.URL.Path, "/branches/") && !strings.Contains(request.URL.Path, "/rules/"):
			_ = json.NewEncoder(response).Encode(map[string]any{"name": "main", "protected": true, "protection": map[string]any{}})
		case strings.Contains(request.URL.Path, "/rules/branches/"):
			_ = json.NewEncoder(response).Encode([]any{})
		case strings.HasSuffix(request.URL.Path, "/check-runs"):
			_ = json.NewEncoder(response).Encode(map[string]any{"total_count": 101, "check_runs": []any{}})
		default:
			t.Fatalf("truncated checks must fail before statuses: %s", request.URL.String())
		}
	}))
	defer server.Close()

	if _, err := ReadGitHubReleaseBranchEvidence(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "repository-token", "example/service", "main", head); err == nil {
		t.Fatal("truncated check runs were treated as complete")
	}
}
