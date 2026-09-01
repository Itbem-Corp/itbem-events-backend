package automationagent

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPublishGitHubCodeReviewIsExactSHAAndRetrySafe(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	key := testGitHubAppKey(t)
	posts := 0
	var published githubRemoteReview
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/app":
			_ = json.NewEncoder(response).Encode(map[string]string{"slug": "bema-review-bot"})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/itbem/example/installation":
			_ = json.NewEncoder(response).Encode(map[string]int64{"id": 67890})
		case request.Method == http.MethodPost && request.URL.Path == "/app/installations/67890/access_tokens":
			var scope map[string][]string
			_ = json.NewDecoder(request.Body).Decode(&scope)
			if len(scope["repositories"]) != 1 || scope["repositories"][0] != "example" {
				t.Fatalf("review token was not repository-scoped: %#v", scope)
			}
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(map[string]any{"token": "review-token", "expires_at": now.Add(time.Hour)})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/itbem/example/pulls/42":
			_ = json.NewEncoder(response).Encode(map[string]any{"state": "open", "head": map[string]string{"sha": strings.Repeat("b", 40)}, "user": map[string]string{"login": "engineer-bot[bot]"}})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/itbem/example/pulls/42/reviews":
			if published.ID == 0 {
				_ = json.NewEncoder(response).Encode([]any{})
			} else {
				_ = json.NewEncoder(response).Encode([]githubRemoteReview{published})
			}
		case request.Method == http.MethodPost && request.URL.Path == "/repos/itbem/example/pulls/42/reviews":
			posts++
			var payload githubReviewCreatePayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Event != "REQUEST_CHANGES" || payload.CommitID != strings.Repeat("b", 40) || len(payload.Comments) != 1 || payload.Comments[0].Side != "RIGHT" {
				t.Fatalf("unexpected review publication: %#v", payload)
			}
			published = githubRemoteReview{ID: 77, State: "CHANGES_REQUESTED", Body: payload.Body, CommitID: payload.CommitID, HTMLURL: "https://github.com/itbem/example/pull/42#pullrequestreview-77", Submitted: now.Format(time.RFC3339)}
			published.User.Login = "bema-review-bot[bot]"
			_ = json.NewEncoder(response).Encode(published)
		default:
			t.Fatalf("unexpected GitHub request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()

	boundary, err := ParseCodeReviewInput(validCodeReviewInput())
	if err != nil {
		t.Fatal(err)
	}
	boundary, err = BindCodeReviewRemoteTarget(boundary, 42, 67890)
	if err != nil {
		t.Fatal(err)
	}
	review, err := ParseCodeReview(validCodeReview())
	if err != nil {
		t.Fatal(err)
	}
	lookup := githubReviewTestLookup(t, key, server.URL)
	first, err := PublishGitHubCodeReview(context.Background(), boundary, review, lookup)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PublishGitHubCodeReview(context.Background(), boundary, review, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if posts != 1 || first.Reused || !second.Reused || first.SubjectSHA256 != second.SubjectSHA256 || first.ReviewerActor != "bema-review-bot[bot]" {
		t.Fatalf("review publication was not retry safe: posts=%d first=%#v second=%#v", posts, first, second)
	}
	published.User.Login = "untrusted-actor"
	if _, err := PublishGitHubCodeReview(context.Background(), boundary, review, lookup); err == nil || !strings.Contains(err.Error(), "different identity") {
		t.Fatalf("a forged idempotency marker was accepted: %v", err)
	}
	if posts != 1 {
		t.Fatalf("identity conflict produced another external effect: posts=%d", posts)
	}
}

func TestPublishGitHubCodeReviewNeverSelfApproves(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	key := testGitHubAppKey(t)
	patch := "diff --git a/src/a.go b/src/a.go\n--- a/src/a.go\n+++ b/src/a.go\n@@ -1 +1 @@\n-old\n+new\ndiff --git a/src/a_test.go b/src/a_test.go\n--- a/src/a_test.go\n+++ b/src/a_test.go\n@@ -1 +1 @@\n-old test\n+new test\n"
	boundary, err := NewCodeReviewInput("github://itbem/example", strings.Repeat("a", 40), strings.Repeat("b", 40), patch)
	if err != nil {
		t.Fatal(err)
	}
	boundary, err = BindCodeReviewRemoteTarget(boundary, 42, 67890)
	if err != nil {
		t.Fatal(err)
	}
	review, err := ParseCodeReview(`{"summary":"The frozen diff is consistent.","verdict":"approve","review_scope":["implementation and tests"],"findings":[],"test_plan":["Run the repository test suite."],"coverage_gaps":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/app":
			_ = json.NewEncoder(response).Encode(map[string]string{"slug": "bema-review-bot"})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/itbem/example/installation":
			_ = json.NewEncoder(response).Encode(map[string]int64{"id": 67890})
		case request.Method == http.MethodPost && request.URL.Path == "/app/installations/67890/access_tokens":
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(map[string]any{"token": "review-token", "expires_at": now.Add(time.Hour)})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/itbem/example/pulls/42":
			_ = json.NewEncoder(response).Encode(map[string]any{"state": "open", "head": map[string]string{"sha": boundary.HeadSHA}, "user": map[string]string{"login": "bema-review-bot[bot]"}})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/itbem/example/pulls/42/reviews":
			_ = json.NewEncoder(response).Encode([]any{})
		case request.Method == http.MethodPost && request.URL.Path == "/repos/itbem/example/pulls/42/reviews":
			var payload githubReviewCreatePayload
			_ = json.NewDecoder(request.Body).Decode(&payload)
			if payload.Event != "COMMENT" || !strings.Contains(payload.Body, "Independent approval remains required") {
				t.Fatalf("self approval was not downgraded: %#v", payload)
			}
			published := githubRemoteReview{ID: 78, State: "COMMENTED", Body: payload.Body, CommitID: payload.CommitID, HTMLURL: "https://github.com/itbem/example/pull/42#pullrequestreview-78", Submitted: now.Format(time.RFC3339)}
			published.User.Login = "bema-review-bot[bot]"
			_ = json.NewEncoder(response).Encode(published)
		default:
			t.Fatalf("unexpected GitHub request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()
	publication, err := PublishGitHubCodeReview(context.Background(), boundary, review, githubReviewTestLookup(t, key, server.URL))
	if err != nil || publication.Event != "COMMENT" || publication.Verdict != "approve" || publication.AuthorActor != publication.ReviewerActor {
		t.Fatalf("self approval did not remain non-approving: %#v / %v", publication, err)
	}
}

func TestFindGitHubCodeReviewSearchesBoundedPagination(t *testing.T) {
	subject, payload, head := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 40)
	marker := githubReviewMarker(subject, payload)
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		pages++
		if request.URL.Query().Get("per_page") != "100" || request.URL.Query().Get("page") != strconv.Itoa(pages) {
			t.Fatalf("unexpected pagination: %s", request.URL.RawQuery)
		}
		if pages == 1 {
			reviews := make([]githubRemoteReview, 100)
			_ = json.NewEncoder(response).Encode(reviews)
			return
		}
		review := githubRemoteReview{ID: 91, State: "APPROVED", Body: marker, CommitID: head}
		_ = json.NewEncoder(response).Encode([]githubRemoteReview{review})
	}))
	defer server.Close()
	review, found, err := findGitHubCodeReview(context.Background(), server.Client(), "token", server.URL+"/reviews", subject, payload, head, "APPROVE")
	if err != nil || !found || review.ID != 91 || pages != 2 {
		t.Fatalf("paginated review was not reconciled: %#v found=%v pages=%d err=%v", review, found, pages, err)
	}
}

func githubReviewTestLookup(t *testing.T, key *rsa.PrivateKey, serverURL string) func(string) string {
	t.Helper()
	pemKey := testGitHubAppPEM(t, key)
	return func(name string) string {
		return map[string]string{
			"ITBEM_GITHUB_APP_ID": "12345", "ITBEM_GITHUB_INSTALLATION_IDS": "67890",
			"ITBEM_GITHUB_APP_PRIVATE_KEY": pemKey, "ITBEM_GITHUB_API_BASE_URL": serverURL,
		}[name]
	}
}
