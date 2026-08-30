package automationagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/releasegate"
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

func TestRunReleaseEnvironmentWithGitHubBindsTaskAndExactMatrix(t *testing.T) {
	head := strings.Repeat("a", 40)
	key := testGitHubAppKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/example/service/installation":
			_ = json.NewEncoder(response).Encode(map[string]int64{"id": 22})
		case "/app/installations/22/access_tokens":
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(map[string]string{"token": "repository-token", "expires_at": time.Now().UTC().Add(45 * time.Minute).Format(time.RFC3339)})
		case "/repos/example/service/contents/.github/workflows/deploy.yml":
			if request.URL.Query().Get("ref") != head {
				t.Fatal("release workflow read was not exact-SHA")
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"type": "file", "path": ".github/workflows/deploy.yml", "sha": strings.Repeat("b", 40)})
		case "/repos/example/service/environments/production":
			_ = json.NewEncoder(response).Encode(map[string]any{"name": "production"})
		default:
			t.Fatalf("unexpected environment request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()
	input := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: "change-set:42", Revisions: []releasegate.Revision{{Repository: "example/service", Branch: "production", SHA: head}}}
	delivery, _ := json.Marshal(map[string]any{"release_environment": []GitHubEnvironmentRequirement{{Repository: "example/service", HeadSHA: head, Workflow: ".github/workflows/deploy.yml", Environment: "production", RequiredSecretReferences: []string{}, RequiredVariableReferences: []string{}}}})
	values := map[string]string{"ITBEM_GITHUB_APP_ID": "12345", "ITBEM_GITHUB_INSTALLATION_IDS": "22", "ITBEM_GITHUB_APP_PRIVATE_KEY": testGitHubAppPEM(t, key), "ITBEM_GITHUB_API_BASE_URL": server.URL}
	taskID := "11111111-1111-4111-8111-111111111111"
	observation, err := RunReleaseEnvironmentWithGitHub(context.Background(), delivery, input, taskID, func(name string) string { return values[name] })
	if err != nil || observation.TaskID != taskID || len(observation.Repositories) != 1 || !observation.Repositories[0].Ready() {
		t.Fatalf("exact environment observation failed: %#v / %v", observation, err)
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
