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
	var publishedCheck githubRemoteCheckRun
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
			if payload.Event != "REQUEST_CHANGES" || payload.CommitID != strings.Repeat("b", 40) {
				t.Fatalf("unexpected review publication: %#v", payload)
			}
			if posts == 1 {
				if len(payload.Comments) != 1 || payload.Comments[0].Side != "RIGHT" {
					t.Fatalf("initial publication lost its inline finding: %#v", payload)
				}
				response.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			if len(payload.Comments) != 0 || !strings.Contains(payload.Body, "Inline anchors were unavailable") || !strings.Contains(payload.Body, "controllers/orders.go:45") {
				t.Fatalf("fallback publication lost its summarized finding: %#v", payload)
			}
			published = githubRemoteReview{ID: 77, State: "CHANGES_REQUESTED", Body: payload.Body, CommitID: payload.CommitID, HTMLURL: "https://github.com/itbem/example/pull/42#pullrequestreview-77", Submitted: now.Format(time.RFC3339)}
			published.User.Login = "bema-review-bot[bot]"
			_ = json.NewEncoder(response).Encode(published)
		case request.Method == http.MethodGet && request.URL.Path == "/repos/itbem/example/commits/"+strings.Repeat("b", 40)+"/check-runs":
			checks := []githubRemoteCheckRun{}
			if publishedCheck.ID != 0 {
				checks = append(checks, publishedCheck)
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"total_count": len(checks), "check_runs": checks})
		case request.Method == http.MethodPost && request.URL.Path == "/repos/itbem/example/check-runs":
			var payload githubCheckRunPayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			publishedCheck = remoteCheckFromPayload(88, payload)
			_ = json.NewEncoder(response).Encode(publishedCheck)
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
	first, err := PublishGitHubCodeReview(context.Background(), boundary, review, lookup, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PublishGitHubCodeReview(context.Background(), boundary, review, lookup, false)
	if err != nil {
		t.Fatal(err)
	}
	if posts != 2 || first.Reused || !second.Reused || first.CheckReused || !second.CheckReused || first.CheckConclusion != "failure" || first.SubjectSHA256 != second.SubjectSHA256 || first.ReviewerActor != "bema-review-bot[bot]" {
		t.Fatalf("review publication was not retry safe: posts=%d first=%#v second=%#v", posts, first, second)
	}
	published.User.Login = "untrusted-actor"
	if _, err := PublishGitHubCodeReview(context.Background(), boundary, review, lookup, false); err == nil || !strings.Contains(err.Error(), "different identity") {
		t.Fatalf("a forged idempotency marker was accepted: %v", err)
	}
	if posts != 2 {
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
	var publishedCheck githubRemoteCheckRun
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
		case request.Method == http.MethodGet && request.URL.Path == "/repos/itbem/example/commits/"+boundary.HeadSHA+"/check-runs":
			_ = json.NewEncoder(response).Encode(map[string]any{"total_count": 0, "check_runs": []any{}})
		case request.Method == http.MethodPost && request.URL.Path == "/repos/itbem/example/check-runs":
			var payload githubCheckRunPayload
			_ = json.NewDecoder(request.Body).Decode(&payload)
			publishedCheck = remoteCheckFromPayload(89, payload)
			_ = json.NewEncoder(response).Encode(publishedCheck)
		default:
			t.Fatalf("unexpected GitHub request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()
	publication, err := PublishGitHubCodeReview(context.Background(), boundary, review, githubReviewTestLookup(t, key, server.URL), false)
	if err != nil || publication.Event != "COMMENT" || publication.Verdict != "approve" || publication.CheckConclusion != "failure" || publication.AuthorActor != publication.ReviewerActor {
		t.Fatalf("self approval did not remain non-approving: %#v / %v", publication, err)
	}
}

func remoteCheckFromPayload(id int64, payload githubCheckRunPayload) githubRemoteCheckRun {
	check := githubRemoteCheckRun{
		ID: id, Name: payload.Name, HeadSHA: payload.HeadSHA, ExternalID: payload.ExternalID,
		Status: payload.Status, Conclusion: payload.Conclusion, HTMLURL: "https://github.com/itbem/example/runs/" + strconv.FormatInt(id, 10),
		DetailsURL: payload.DetailsURL, Output: payload.Output,
	}
	check.App.ID, check.App.Slug = 12345, "bema-review-bot"
	return check
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

func TestCodeReviewPassesExactSHAGateAllowsOnlySafeIndependentOutcomes(t *testing.T) {
	lowMaintainability := map[string]any{
		"verdict": "comment", "coverage_gaps": []any{},
		"findings": []any{map[string]any{"severity": "low", "category": "maintainability"}},
	}
	cases := []struct {
		name     string
		review   map[string]any
		event    string
		reviewer string
		author   string
		want     bool
	}{
		{"independent approval", map[string]any{"verdict": "approve"}, "APPROVE", "bema-review-bot[bot]", "engineer-bot[bot]", true},
		{"low maintainability note", lowMaintainability, "COMMENT", "bema-review-bot[bot]", "engineer-bot[bot]", true},
		{"blank reviewer cannot pass", map[string]any{"verdict": "approve"}, "APPROVE", "", "engineer-bot[bot]", false},
		{"author cannot pass own approval", map[string]any{"verdict": "approve"}, "APPROVE", "bema-review-bot[bot]", "bema-review-bot[bot]", false},
		{"low security note remains a gate failure", map[string]any{"verdict": "comment", "coverage_gaps": []any{}, "findings": []any{map[string]any{"severity": "low", "category": "security"}}}, "COMMENT", "bema-review-bot[bot]", "engineer-bot[bot]", false},
		{"low correctness note remains a gate failure", map[string]any{"verdict": "comment", "coverage_gaps": []any{}, "findings": []any{map[string]any{"severity": "low", "category": "correctness"}}}, "COMMENT", "bema-review-bot[bot]", "engineer-bot[bot]", false},
		{"low reliability note remains a gate failure", map[string]any{"verdict": "comment", "coverage_gaps": []any{}, "findings": []any{map[string]any{"severity": "low", "category": "reliability"}}}, "COMMENT", "bema-review-bot[bot]", "engineer-bot[bot]", false},
		{"low performance note remains a gate failure", map[string]any{"verdict": "comment", "coverage_gaps": []any{}, "findings": []any{map[string]any{"severity": "low", "category": "performance"}}}, "COMMENT", "bema-review-bot[bot]", "engineer-bot[bot]", false},
		{"low test coverage note remains a gate failure", map[string]any{"verdict": "comment", "coverage_gaps": []any{}, "findings": []any{map[string]any{"severity": "low", "category": "test_coverage"}}}, "COMMENT", "bema-review-bot[bot]", "engineer-bot[bot]", false},
		{"coverage gap remains a gate failure", map[string]any{"verdict": "comment", "coverage_gaps": []any{"Run a missing regression test."}, "findings": []any{}}, "COMMENT", "bema-review-bot[bot]", "engineer-bot[bot]", false},
		{"requested changes remain a gate failure", map[string]any{"verdict": "request_changes"}, "REQUEST_CHANGES", "bema-review-bot[bot]", "engineer-bot[bot]", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := codeReviewPassesExactSHAGate(testCase.review, testCase.event, testCase.reviewer, testCase.author); got != testCase.want {
				t.Fatalf("gate eligibility = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestPublishGitHubExactSHAReviewCheckSucceedsOnlyForSafeIndependentOutcome(t *testing.T) {
	head := strings.Repeat("b", 40)
	publication := GitHubCodeReviewPublication{
		Repository: "itbem/example", PullRequest: 42, HeadSHA: head, SubjectSHA256: strings.Repeat("a", 64),
		PayloadSHA256: strings.Repeat("c", 64), Verdict: "approve", Event: "APPROVE", ReviewGatePassed: true, ReviewID: 77,
		ReviewURL: "https://github.com/itbem/example/pull/42#pullrequestreview-77", ReviewerActor: "bema-review-bot[bot]", AuthorActor: "engineer-bot[bot]",
	}
	writes := 0
	var check githubRemoteCheckRun
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/commits/"+head+"/check-runs"):
			checks := []githubRemoteCheckRun{}
			if check.ID > 0 {
				checks = append(checks, check)
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"total_count": len(checks), "check_runs": checks})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/check-runs"):
			writes++
			var payload githubCheckRunPayload
			_ = json.NewDecoder(request.Body).Decode(&payload)
			if payload.HeadSHA != head || payload.Conclusion != "success" || payload.ExternalID != publication.SubjectSHA256 {
				t.Fatalf("check was not bound to the independent exact-SHA approval: %#v", payload)
			}
			check = remoteCheckFromPayload(90, payload)
			_ = json.NewEncoder(response).Encode(check)
		case request.Method == http.MethodPatch && strings.HasSuffix(request.URL.Path, "/check-runs/90"):
			writes++
			var payload githubCheckRunPayload
			_ = json.NewDecoder(request.Body).Decode(&payload)
			if payload.HeadSHA != "" || payload.Conclusion != "success" || payload.ExternalID != publication.SubjectSHA256 {
				t.Fatalf("retry did not preserve the check's exact-SHA identity: %#v", payload)
			}
			check = remoteCheckFromPayload(90, payload)
			check.HeadSHA = head
			_ = json.NewEncoder(response).Encode(check)
		default:
			t.Fatalf("unexpected GitHub request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()
	config := GitHubAppConfig{AppID: "12345", APIBaseURL: server.URL}
	repository := githubRepository{Owner: "itbem", Name: "example"}
	first, err := publishGitHubExactSHAReviewCheck(context.Background(), server.Client(), config, "token", repository, publication, false)
	if err != nil || first.CheckConclusion != "success" || first.CheckReused || writes != 1 {
		t.Fatalf("valid check was rejected: %#v writes=%d err=%v", first, writes, err)
	}
	second, err := publishGitHubExactSHAReviewCheck(context.Background(), server.Client(), config, "token", repository, publication, false)
	if err != nil || !second.CheckReused || writes != 1 {
		t.Fatalf("check retry was not idempotent: %#v writes=%d err=%v", second, writes, err)
	}
	check.Output.Text = githubReviewCheckMarker(publication.SubjectSHA256, strings.Repeat("d", 64))
	if _, err := publishGitHubExactSHAReviewCheck(context.Background(), server.Client(), config, "token", repository, publication, false); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflicting check marker was accepted: %v", err)
	}
	if _, err := publishGitHubExactSHAReviewCheck(context.Background(), server.Client(), config, "token", repository, publication, true); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("an explicit retry weakened an existing successful check: %v", err)
	}

	check.Conclusion = "failure"
	retried := publication
	retried.PayloadSHA256 = strings.Repeat("e", 64)
	retried.ReviewURL = "https://github.com/itbem/example/pull/42#pullrequestreview-78"
	updated, err := publishGitHubExactSHAReviewCheck(context.Background(), server.Client(), config, "token", repository, retried, true)
	if err != nil || updated.CheckConclusion != "success" || updated.CheckReused || writes != 2 || !strings.Contains(check.Output.Text, githubReviewCheckMarker(retried.SubjectSHA256, retried.PayloadSHA256)) {
		t.Fatalf("authorized retry did not supersede only the prior failed exact-SHA check: %#v writes=%d check=%#v err=%v", updated, writes, check, err)
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
