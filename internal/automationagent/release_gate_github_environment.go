package automationagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxGitHubEnvironmentReferences = 64

var (
	githubWorkflowPathPattern         = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9][A-Za-z0-9_.-]{0,119}\.ya?ml$`)
	githubEnvironmentReferencePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
)

// GitHubEnvironmentRequirement is an operator-approved, value-free release
// contract. It can safely cross the worker boundary because it contains names
// only; the backend must still compare it with current effective policy before
// accepting the resulting observation.
type GitHubEnvironmentRequirement struct {
	Repository                 string   `json:"repository"`
	HeadSHA                    string   `json:"head_sha"`
	Workflow                   string   `json:"workflow"`
	Environment                string   `json:"environment"`
	RequiredSecretReferences   []string `json:"required_secret_references"`
	RequiredVariableReferences []string `json:"required_variable_references"`
}

// GitHubEnvironmentEvidence records only bounded readiness facts and missing
// approved names. It never contains a secret/variable value or the complete
// environment inventory returned by GitHub.
type GitHubEnvironmentEvidence struct {
	GitHubEnvironmentRequirement
	WorkflowExists            bool     `json:"workflow_exists"`
	EnvironmentExists         bool     `json:"environment_exists"`
	MissingSecretReferences   []string `json:"missing_secret_references"`
	MissingVariableReferences []string `json:"missing_variable_references"`
}

func (evidence GitHubEnvironmentEvidence) Ready() bool {
	return evidence.WorkflowExists && evidence.EnvironmentExists && len(evidence.MissingSecretReferences) == 0 && len(evidence.MissingVariableReferences) == 0
}

// ReadGitHubReleaseEnvironmentEvidence verifies the approved release contract
// against GitHub at one exact commit. Contents is read with ?ref=<head SHA>;
// environment endpoints expose names/metadata only and cannot return values.
func ReadGitHubReleaseEnvironmentEvidence(ctx context.Context, config GitHubAppConfig, token string, requirement GitHubEnvironmentRequirement) (GitHubEnvironmentEvidence, error) {
	requirement, err := canonicalGitHubEnvironmentRequirement(requirement)
	if err != nil || strings.TrimSpace(token) == "" {
		return GitHubEnvironmentEvidence{}, fmt.Errorf("release environment requirement is invalid")
	}
	parts := strings.Split(requirement.Repository, "/")
	baseURL := strings.TrimRight(config.APIBaseURL, "/") + "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
	client := &http.Client{Timeout: 20 * time.Second}
	evidence := GitHubEnvironmentEvidence{
		GitHubEnvironmentRequirement: requirement,
		MissingSecretReferences:      []string{}, MissingVariableReferences: []string{},
	}

	workflowURL := baseURL + "/contents/" + escapeGitHubRepositoryContentPath(requirement.Workflow) + "?ref=" + url.QueryEscape(requirement.HeadSHA)
	request, err := githubAppRequest(ctx, http.MethodGet, workflowURL, token, nil)
	if err != nil {
		return GitHubEnvironmentEvidence{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return GitHubEnvironmentEvidence{}, fmt.Errorf("read release workflow")
	}
	var workflow struct {
		Type string `json:"type"`
		Path string `json:"path"`
		SHA  string `json:"sha"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&workflow)
	status := response.StatusCode
	response.Body.Close()
	if status != http.StatusOK || decodeErr != nil || workflow.Type != "file" || workflow.Path != requirement.Workflow || !validGitHubCommitSHA(strings.ToLower(strings.TrimSpace(workflow.SHA))) {
		return GitHubEnvironmentEvidence{}, fmt.Errorf("release workflow is unavailable at the exact revision")
	}
	evidence.WorkflowExists = true

	environmentURL := baseURL + "/environments/" + url.PathEscape(requirement.Environment)
	request, err = githubAppRequest(ctx, http.MethodGet, environmentURL, token, nil)
	if err != nil {
		return GitHubEnvironmentEvidence{}, err
	}
	response, err = client.Do(request)
	if err != nil {
		return GitHubEnvironmentEvidence{}, fmt.Errorf("read release environment")
	}
	var environment struct {
		Name string `json:"name"`
	}
	decodeErr = json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&environment)
	status = response.StatusCode
	response.Body.Close()
	if status != http.StatusOK || decodeErr != nil || environment.Name != requirement.Environment {
		return GitHubEnvironmentEvidence{}, fmt.Errorf("release environment is unavailable")
	}
	evidence.EnvironmentExists = true

	if len(requirement.RequiredSecretReferences) > 0 {
		available, readErr := readGitHubEnvironmentReferenceNames(ctx, client, config, token, environmentURL+"/secrets?per_page=100", "secrets")
		if readErr != nil {
			return GitHubEnvironmentEvidence{}, readErr
		}
		evidence.MissingSecretReferences = missingGitHubEnvironmentReferences(requirement.RequiredSecretReferences, available)
	}
	if len(requirement.RequiredVariableReferences) > 0 {
		available, readErr := readGitHubEnvironmentReferenceNames(ctx, client, config, token, environmentURL+"/variables?per_page=100", "variables")
		if readErr != nil {
			return GitHubEnvironmentEvidence{}, readErr
		}
		evidence.MissingVariableReferences = missingGitHubEnvironmentReferences(requirement.RequiredVariableReferences, available)
	}
	return evidence, nil
}

func readGitHubEnvironmentReferenceNames(ctx context.Context, client *http.Client, config GitHubAppConfig, token, endpoint, kind string) (map[string]struct{}, error) {
	request, err := githubAppRequest(ctx, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("read release environment %s", kind)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || hasGitHubNextPage(response.Header.Get("Link")) {
		return nil, fmt.Errorf("release environment %s are unavailable or exceed the bounded page", kind)
	}
	var payload struct {
		TotalCount int `json:"total_count"`
		Secrets    []struct {
			Name string `json:"name"`
		} `json:"secrets"`
		Variables []struct {
			Name string `json:"name"`
		} `json:"variables"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&payload); err != nil || payload.TotalCount < 0 || payload.TotalCount > 100 {
		return nil, fmt.Errorf("release environment %s response is invalid", kind)
	}
	values := payload.Secrets
	if kind == "variables" {
		values = payload.Variables
	}
	if len(values) != payload.TotalCount {
		return nil, fmt.Errorf("release environment %s response is incomplete", kind)
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		name := strings.ToUpper(strings.TrimSpace(value.Name))
		if !githubEnvironmentReferencePattern.MatchString(name) || strings.HasPrefix(name, "GITHUB_") {
			return nil, fmt.Errorf("release environment %s response is invalid", kind)
		}
		if _, duplicate := result[name]; duplicate {
			return nil, fmt.Errorf("release environment %s response is ambiguous", kind)
		}
		result[name] = struct{}{}
	}
	return result, nil
}

func canonicalGitHubEnvironmentRequirement(value GitHubEnvironmentRequirement) (GitHubEnvironmentRequirement, error) {
	value.Repository = strings.ToLower(strings.TrimSpace(value.Repository))
	value.HeadSHA = strings.ToLower(strings.TrimSpace(value.HeadSHA))
	value.Workflow = strings.TrimSpace(value.Workflow)
	value.Environment = strings.TrimSpace(value.Environment)
	value.RequiredSecretReferences = canonicalGitHubEnvironmentReferences(value.RequiredSecretReferences)
	value.RequiredVariableReferences = canonicalGitHubEnvironmentReferences(value.RequiredVariableReferences)
	if !githubRepositoryNamePattern.MatchString(value.Repository) || !validGitHubCommitSHA(value.HeadSHA) || !githubWorkflowPathPattern.MatchString(value.Workflow) || value.Environment == "" || len(value.Environment) > 128 || strings.IndexFunc(value.Environment, func(character rune) bool { return character < 0x20 || character == 0x7f }) != -1 || !validGitHubEnvironmentReferences(value.RequiredSecretReferences) || !validGitHubEnvironmentReferences(value.RequiredVariableReferences) {
		return GitHubEnvironmentRequirement{}, fmt.Errorf("release environment requirement is invalid")
	}
	return value, nil
}

func canonicalGitHubEnvironmentReferences(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.ToUpper(strings.TrimSpace(value))
	}
	sort.Strings(result)
	return result
}

func validGitHubEnvironmentReferences(values []string) bool {
	if len(values) > maxGitHubEnvironmentReferences {
		return false
	}
	for index, value := range values {
		if !githubEnvironmentReferencePattern.MatchString(value) || strings.HasPrefix(value, "GITHUB_") || (index > 0 && values[index-1] == value) {
			return false
		}
	}
	return true
}

func missingGitHubEnvironmentReferences(required []string, available map[string]struct{}) []string {
	missing := make([]string, 0)
	for _, name := range required {
		if _, ok := available[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}
