package automationagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestReadGitHubReleaseEnvironmentEvidencePinsWorkflowAndChecksOnlyRequiredNames(t *testing.T) {
	head := strings.Repeat("a", 40)
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.RequestURI())
		if request.Header.Get("Authorization") != "Bearer repository-token" {
			t.Fatalf("installation token was not used")
		}
		switch request.URL.Path {
		case "/repos/example/service/contents/.github/workflows/deploy.yml":
			if request.URL.Query().Get("ref") != head {
				t.Fatalf("workflow was not pinned to exact SHA: %s", request.URL.RawQuery)
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"type": "file", "path": ".github/workflows/deploy.yml", "sha": strings.Repeat("b", 40)})
		case "/repos/example/service/environments/production":
			_ = json.NewEncoder(response).Encode(map[string]any{"name": "production"})
		case "/repos/example/service/environments/production/secrets":
			_ = json.NewEncoder(response).Encode(map[string]any{"total_count": 2, "secrets": []map[string]string{{"name": "DATABASE_URL"}, {"name": "UNRELATED_SECRET"}}})
		case "/repos/example/service/environments/production/variables":
			_ = json.NewEncoder(response).Encode(map[string]any{"total_count": 1, "variables": []map[string]string{{"name": "AWS_ROLE_ARN"}}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	evidence, err := ReadGitHubReleaseEnvironmentEvidence(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "repository-token", GitHubEnvironmentRequirement{
		Repository: "Example/Service", HeadSHA: head, Workflow: ".github/workflows/deploy.yml", Environment: "production",
		RequiredSecretReferences: []string{"database_url"}, RequiredVariableReferences: []string{"aws_role_arn", "public_origin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Ready() || !evidence.WorkflowExists || !evidence.EnvironmentExists || !reflect.DeepEqual(evidence.MissingVariableReferences, []string{"PUBLIC_ORIGIN"}) || len(evidence.MissingSecretReferences) != 0 {
		t.Fatalf("unexpected environment evidence: %#v", evidence)
	}
	if len(requests) != 4 {
		t.Fatalf("unexpected GitHub API request count: %d", len(requests))
	}
}

func TestReadGitHubReleaseEnvironmentEvidenceDoesNotRequestEmptyReferenceInventories(t *testing.T) {
	head := strings.Repeat("a", 40)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		switch request.URL.Path {
		case "/repos/example/service/contents/.github/workflows/deploy.yml":
			_ = json.NewEncoder(response).Encode(map[string]any{"type": "file", "path": ".github/workflows/deploy.yml", "sha": strings.Repeat("b", 40)})
		case "/repos/example/service/environments/production":
			_ = json.NewEncoder(response).Encode(map[string]any{"name": "production"})
		default:
			t.Fatalf("empty approved references caused an inventory read: %s", request.URL.Path)
		}
	}))
	defer server.Close()
	evidence, err := ReadGitHubReleaseEnvironmentEvidence(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "token", GitHubEnvironmentRequirement{
		Repository: "example/service", HeadSHA: head, Workflow: ".github/workflows/deploy.yml", Environment: "production",
		RequiredSecretReferences: []string{}, RequiredVariableReferences: []string{},
	})
	if err != nil || !evidence.Ready() || requests != 2 {
		t.Fatalf("explicit empty reference contract failed: %#v / %v / requests=%d", evidence, err, requests)
	}
}

func TestReadGitHubReleaseEnvironmentEvidenceRejectsUnsafeOrTruncatedInputs(t *testing.T) {
	head := strings.Repeat("a", 40)
	for name, requirement := range map[string]GitHubEnvironmentRequirement{
		"mutable ref":     {Repository: "example/service", HeadSHA: "main", Workflow: ".github/workflows/deploy.yml", Environment: "production"},
		"unsafe workflow": {Repository: "example/service", HeadSHA: head, Workflow: ".github/workflows/../deploy.yml", Environment: "production"},
		"reserved secret": {Repository: "example/service", HeadSHA: head, Workflow: ".github/workflows/deploy.yml", Environment: "production", RequiredSecretReferences: []string{"GITHUB_TOKEN"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadGitHubReleaseEnvironmentEvidence(context.Background(), GitHubAppConfig{APIBaseURL: "https://api.github.invalid"}, "token", requirement); err == nil {
				t.Fatalf("unsafe requirement was accepted: %#v", requirement)
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.Contains(request.URL.Path, "/contents/"):
			_ = json.NewEncoder(response).Encode(map[string]any{"type": "file", "path": ".github/workflows/deploy.yml", "sha": strings.Repeat("b", 40)})
		case strings.HasSuffix(request.URL.Path, "/production"):
			_ = json.NewEncoder(response).Encode(map[string]any{"name": "production"})
		default:
			response.Header().Set("Link", `<https://api.github.test/next>; rel="next"`)
			_ = json.NewEncoder(response).Encode(map[string]any{"total_count": 101, "secrets": []any{}})
		}
	}))
	defer server.Close()
	if _, err := ReadGitHubReleaseEnvironmentEvidence(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "token", GitHubEnvironmentRequirement{
		Repository: "example/service", HeadSHA: head, Workflow: ".github/workflows/deploy.yml", Environment: "production", RequiredSecretReferences: []string{"DATABASE_URL"},
	}); err == nil {
		t.Fatal("paginated environment inventory was accepted as complete")
	}
}
