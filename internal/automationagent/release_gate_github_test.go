package automationagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/releasegate"
)

func TestReadGitHubReleasePullRequestEvidencePinsHeadBaseAndDecisiveReviews(t *testing.T) {
	head, old := strings.Repeat("b", 40), strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer repository-token" {
			t.Fatal("PR evidence must use only the repository-scoped token")
		}
		switch request.URL.Path {
		case "/repos/example/service/pulls/42":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"state": "open", "draft": false, "merged": false, "mergeable": true, "mergeable_state": "clean",
				"head": map[string]string{"sha": head}, "base": map[string]string{"ref": "release/stable"}, "user": map[string]string{"login": "author"},
			})
		case "/repos/example/service/pulls/42/reviews":
			_ = json.NewEncoder(response).Encode([]map[string]any{
				{"id": 1, "state": "APPROVED", "commit_id": old, "user": map[string]string{"login": "reviewer-a"}},
				{"id": 2, "state": "COMMENTED", "commit_id": head, "user": map[string]string{"login": "reviewer-a"}},
				{"id": 3, "state": "CHANGES_REQUESTED", "commit_id": old, "user": map[string]string{"login": "reviewer-b"}},
				{"id": 4, "state": "APPROVED", "commit_id": head, "user": map[string]string{"login": "reviewer-c"}},
			})
		default:
			t.Fatalf("unexpected GitHub evidence request: %s", request.URL.String())
		}
	}))
	defer server.Close()

	evidence, err := ReadGitHubReleasePullRequestEvidence(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "repository-token", "example/service", 42)
	if err != nil || evidence.HeadSHA != head || evidence.BaseBranch != "release/stable" || evidence.AuthorActor != "author" || !evidence.Mergeable || !evidence.ConflictFree || len(evidence.Reviews) != 3 {
		t.Fatalf("unexpected exact PR evidence: %#v / %v", evidence, err)
	}
	if evidence.Reviews[0].ReviewerActor != "reviewer-a" || evidence.Reviews[0].HeadSHA != old || !evidence.Reviews[0].Approved {
		t.Fatalf("an old approval must remain visibly stale: %#v", evidence.Reviews[0])
	}
	if evidence.Reviews[1].ReviewerActor != "reviewer-b" || evidence.Reviews[1].HeadSHA != head || evidence.Reviews[1].BlockingChangeRequests != 1 {
		t.Fatalf("an unresolved change request must block the current head: %#v", evidence.Reviews[1])
	}
	if evidence.Reviews[2].ReviewerActor != "reviewer-c" || evidence.Reviews[2].HeadSHA != head || !evidence.Reviews[2].Approved {
		t.Fatalf("the exact-head independent approval was lost: %#v", evidence.Reviews[2])
	}
}

func TestRunReleaseGateWithGitHubCollectsProtectedBranchAndExactChecks(t *testing.T) {
	head := strings.Repeat("b", 40)
	key := testGitHubAppKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/installation":
			_ = json.NewEncoder(response).Encode(map[string]int64{"id": 22})
		case request.Method == http.MethodPost && request.URL.Path == "/app/installations/22/access_tokens":
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(map[string]string{"token": "repository-token", "expires_at": time.Now().UTC().Add(45 * time.Minute).Format(time.RFC3339)})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/pulls/42":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"state": "open", "draft": false, "merged": false, "mergeable": true, "mergeable_state": "clean",
				"head": map[string]string{"sha": head}, "base": map[string]string{"ref": "production"}, "user": map[string]string{"login": "author"},
			})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/pulls/42/reviews":
			_ = json.NewEncoder(response).Encode([]map[string]any{{"id": 1, "state": "APPROVED", "commit_id": head, "user": map[string]string{"login": "reviewer"}}})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/branches/production":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"name": "production", "protected": true,
				"protection": map[string]any{"required_status_checks": map[string]any{
					"contexts": []string{"ci"}, "checks": []map[string]any{{"context": "ci", "app_id": 99}},
				}},
			})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/rules/branches/production":
			_ = json.NewEncoder(response).Encode([]any{})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/commits/"+head+"/check-runs":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"total_count": 1,
				"check_runs":  []map[string]any{{"id": 1, "name": "ci", "head_sha": head, "status": "completed", "conclusion": "success", "app": map[string]int64{"id": 99}}},
			})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example/service/commits/"+head+"/status":
			_ = json.NewEncoder(response).Encode(map[string]any{"sha": head, "statuses": []any{}})
		default:
			t.Fatalf("unexpected GitHub Gatekeeper request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()

	candidate := releasegate.Input{
		SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: "change-set:42",
		Revisions: []releasegate.Revision{{Repository: "example/service", Branch: "published-head", SHA: head}},
		Policy:    releasegate.Policy{Resolved: false},
	}
	delivery, err := json.Marshal(map[string]any{
		"gatekeeper":  candidate,
		"change_sets": []map[string]any{{"commit_sha": head, "review_type": "pull_request", "pull_request_url": "https://github.com/example/service/pull/42"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"ITBEM_GITHUB_APP_ID": "12345", "ITBEM_GITHUB_INSTALLATION_IDS": "22",
		"ITBEM_GITHUB_APP_PRIVATE_KEY": testGitHubAppPEM(t, key), "ITBEM_GITHUB_API_BASE_URL": server.URL,
	}
	input, err := RunReleaseGateWithGitHub(context.Background(), delivery, func(name string) string { return values[name] })
	if err != nil || input.Revisions[0].Branch != "production" || len(input.Branches) != 1 || !input.Branches[0].ProtectionEvaluated || !input.Branches[0].Protected || len(input.Branches[0].RequiredChecks) != 1 || len(input.Checks) != 1 || input.Checks[0].IntegrationID != 99 || len(input.Reviews) != 1 || !input.Reviews[0].Approved {
		t.Fatalf("unexpected enriched Gatekeeper candidate: %#v / %v", input, err)
	}
	decision := releasegate.Evaluate(input)
	if decision.State != "blocked" || releaseGateDecisionHasReason(decision, "branch_protection_unknown") || releaseGateDecisionHasReason(decision, "required_check_missing") || !releaseGateDecisionHasReason(decision, "policy_unresolved") {
		t.Fatalf("authoritative GitHub evidence was not projected correctly: %#v", decision)
	}
}

func releaseGateDecisionHasReason(decision releasegate.Decision, code string) bool {
	for _, reason := range decision.Reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}
