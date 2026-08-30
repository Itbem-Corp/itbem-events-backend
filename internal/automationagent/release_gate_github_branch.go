package automationagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"events-stocks/internal/releasegate"
)

const maxGitHubReleaseChecks = 100

type githubReleaseBranchEvidence struct {
	ProtectionEvaluated bool
	Protected           bool
	RequiredChecks      []releasegate.RequiredCheck
	Checks              []releasegate.CheckEvidence
}

// ReadGitHubReleaseBranchEvidence reads active classic branch protection,
// repository/organization rulesets, check runs, and legacy commit statuses.
// Every response is bounded and tied to the exact PR base branch and head SHA.
func ReadGitHubReleaseBranchEvidence(ctx context.Context, config GitHubAppConfig, token, repository, branch, headSHA string) (githubReleaseBranchEvidence, error) {
	repository = strings.ToLower(strings.TrimSpace(repository))
	branch = strings.TrimSpace(branch)
	headSHA = strings.ToLower(strings.TrimSpace(headSHA))
	if !githubRepositoryNamePattern.MatchString(repository) || branch == "" || len(branch) > 240 || !validGitHubCommitSHA(headSHA) || strings.TrimSpace(token) == "" {
		return githubReleaseBranchEvidence{}, fmt.Errorf("release Gatekeeper GitHub branch lookup is invalid")
	}
	parts := strings.Split(repository, "/")
	baseURL := strings.TrimRight(config.APIBaseURL, "/") + "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
	client := &http.Client{Timeout: 15 * time.Second}

	protected, classic, err := readGitHubReleaseBranchProtection(ctx, client, token, baseURL+"/branches/"+url.PathEscape(branch), branch)
	if err != nil {
		return githubReleaseBranchEvidence{}, err
	}
	ruleset, err := readGitHubReleaseRules(ctx, client, token, baseURL+"/rules/branches/"+url.PathEscape(branch)+"?per_page=100")
	if err != nil {
		return githubReleaseBranchEvidence{}, err
	}
	required, err := mergeGitHubRequiredChecks(classic, ruleset)
	if err != nil {
		return githubReleaseBranchEvidence{}, err
	}
	checkRuns, err := readGitHubReleaseCheckRuns(ctx, client, token, baseURL+"/commits/"+url.PathEscape(headSHA)+"/check-runs?filter=latest&per_page=100", repository, headSHA)
	if err != nil {
		return githubReleaseBranchEvidence{}, err
	}
	statuses, err := readGitHubReleaseCommitStatuses(ctx, client, token, baseURL+"/commits/"+url.PathEscape(headSHA)+"/status?per_page=100", repository, headSHA)
	if err != nil {
		return githubReleaseBranchEvidence{}, err
	}
	checks := append(checkRuns, statuses...)
	if len(checks) > maxGitHubReleaseChecks {
		return githubReleaseBranchEvidence{}, fmt.Errorf("release Gatekeeper combined checks exceed the bounded limit")
	}
	sort.Slice(checks, func(left, right int) bool {
		if !strings.EqualFold(checks[left].Name, checks[right].Name) {
			return strings.ToLower(checks[left].Name) < strings.ToLower(checks[right].Name)
		}
		return checks[left].IntegrationID < checks[right].IntegrationID
	})
	return githubReleaseBranchEvidence{ProtectionEvaluated: true, Protected: protected, RequiredChecks: required, Checks: checks}, nil
}

func readGitHubReleaseBranchProtection(ctx context.Context, client *http.Client, token, endpoint, expectedBranch string) (bool, []releasegate.RequiredCheck, error) {
	request, err := githubAppRequest(ctx, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return false, nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return false, nil, fmt.Errorf("read release Gatekeeper branch protection")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, nil, fmt.Errorf("release Gatekeeper branch protection lookup was rejected (%d)", response.StatusCode)
	}
	var payload struct {
		Name       string `json:"name"`
		Protected  bool   `json:"protected"`
		Protection struct {
			RequiredStatusChecks struct {
				Contexts []string `json:"contexts"`
				Checks   []struct {
					Context string `json:"context"`
					AppID   *int64 `json:"app_id"`
				} `json:"checks"`
			} `json:"required_status_checks"`
		} `json:"protection"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&payload); err != nil || payload.Name != expectedBranch {
		return false, nil, fmt.Errorf("release Gatekeeper branch protection response is invalid")
	}
	checks := make([]releasegate.RequiredCheck, 0, len(payload.Protection.RequiredStatusChecks.Contexts)+len(payload.Protection.RequiredStatusChecks.Checks))
	contextsWithIdentity := map[string]struct{}{}
	for _, check := range payload.Protection.RequiredStatusChecks.Checks {
		integrationID := int64(0)
		if check.AppID != nil {
			integrationID = *check.AppID
		}
		checks = append(checks, releasegate.RequiredCheck{Name: check.Context, IntegrationID: integrationID})
		contextsWithIdentity[strings.ToLower(strings.TrimSpace(check.Context))] = struct{}{}
	}
	for _, contextName := range payload.Protection.RequiredStatusChecks.Contexts {
		if _, represented := contextsWithIdentity[strings.ToLower(strings.TrimSpace(contextName))]; !represented {
			checks = append(checks, releasegate.RequiredCheck{Name: contextName})
		}
	}
	return payload.Protected, checks, nil
}

func readGitHubReleaseRules(ctx context.Context, client *http.Client, token, endpoint string) ([]releasegate.RequiredCheck, error) {
	request, err := githubAppRequest(ctx, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("read release Gatekeeper branch rules")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || hasGitHubNextPage(response.Header.Get("Link")) {
		return nil, fmt.Errorf("release Gatekeeper branch rules are unavailable or exceed the bounded page")
	}
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			RequiredStatusChecks []struct {
				Context       string `json:"context"`
				IntegrationID *int64 `json:"integration_id"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&rules); err != nil || len(rules) > 100 {
		return nil, fmt.Errorf("release Gatekeeper branch rules response is invalid")
	}
	checks := []releasegate.RequiredCheck{}
	for _, rule := range rules {
		if rule.Type != "required_status_checks" {
			continue
		}
		for _, check := range rule.Parameters.RequiredStatusChecks {
			integrationID := int64(0)
			if check.IntegrationID != nil {
				integrationID = *check.IntegrationID
			}
			checks = append(checks, releasegate.RequiredCheck{Name: check.Context, IntegrationID: integrationID})
		}
	}
	return checks, nil
}

func mergeGitHubRequiredChecks(groups ...[]releasegate.RequiredCheck) ([]releasegate.RequiredCheck, error) {
	result := []releasegate.RequiredCheck{}
	seen := map[string]struct{}{}
	for _, checks := range groups {
		for _, check := range checks {
			if !validGitHubReleaseEvidenceName(check.Name) || check.IntegrationID < 0 {
				return nil, fmt.Errorf("release Gatekeeper branch required checks are invalid")
			}
			key := strings.ToLower(check.Name) + "\x00" + fmt.Sprint(check.IntegrationID)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, check)
			if len(result) > maxGitHubReleaseChecks {
				return nil, fmt.Errorf("release Gatekeeper branch required checks exceed the bounded limit")
			}
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if !strings.EqualFold(result[left].Name, result[right].Name) {
			return strings.ToLower(result[left].Name) < strings.ToLower(result[right].Name)
		}
		return result[left].IntegrationID < result[right].IntegrationID
	})
	return result, nil
}

func readGitHubReleaseCheckRuns(ctx context.Context, client *http.Client, token, endpoint, repository, headSHA string) ([]releasegate.CheckEvidence, error) {
	request, err := githubAppRequest(ctx, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("read release Gatekeeper check runs")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || hasGitHubNextPage(response.Header.Get("Link")) {
		return nil, fmt.Errorf("release Gatekeeper check runs are unavailable or exceed the bounded page")
	}
	var payload struct {
		TotalCount int `json:"total_count"`
		CheckRuns  []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			HeadSHA    string `json:"head_sha"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			App        *struct {
				ID int64 `json:"id"`
			} `json:"app"`
		} `json:"check_runs"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 512<<10)).Decode(&payload); err != nil || payload.TotalCount < 0 || payload.TotalCount > maxGitHubReleaseChecks || len(payload.CheckRuns) > maxGitHubReleaseChecks || payload.TotalCount > len(payload.CheckRuns) {
		return nil, fmt.Errorf("release Gatekeeper check runs response is invalid or truncated")
	}
	type latestCheck struct {
		ID       int64
		Evidence releasegate.CheckEvidence
	}
	latest := map[string]latestCheck{}
	for _, check := range payload.CheckRuns {
		name := strings.TrimSpace(check.Name)
		actualSHA := strings.ToLower(strings.TrimSpace(check.HeadSHA))
		if check.ID < 1 || !validGitHubReleaseEvidenceName(check.Name) || name != check.Name || actualSHA != headSHA || check.App == nil || check.App.ID < 1 {
			return nil, fmt.Errorf("release Gatekeeper check runs response is invalid")
		}
		status := releasegate.StatusFailed
		conclusion := strings.ToLower(strings.TrimSpace(check.Conclusion))
		if strings.EqualFold(strings.TrimSpace(check.Status), "completed") && (conclusion == "success" || conclusion == "neutral" || conclusion == "skipped") {
			status = releasegate.StatusPassed
		}
		evidence := releasegate.CheckEvidence{Repository: repository, Name: name, IntegrationID: check.App.ID, HeadSHA: headSHA, Status: status}
		key := strings.ToLower(name) + "\x00" + fmt.Sprint(check.App.ID)
		if previous, ok := latest[key]; !ok || check.ID > previous.ID {
			latest[key] = latestCheck{ID: check.ID, Evidence: evidence}
		}
	}
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]releasegate.CheckEvidence, 0, len(keys))
	for _, key := range keys {
		result = append(result, latest[key].Evidence)
	}
	return result, nil
}

func readGitHubReleaseCommitStatuses(ctx context.Context, client *http.Client, token, endpoint, repository, headSHA string) ([]releasegate.CheckEvidence, error) {
	request, err := githubAppRequest(ctx, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("read release Gatekeeper commit statuses")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || hasGitHubNextPage(response.Header.Get("Link")) {
		return nil, fmt.Errorf("release Gatekeeper commit statuses are unavailable or exceed the bounded page")
	}
	var payload struct {
		SHA      string `json:"sha"`
		Statuses []struct {
			ID      int64  `json:"id"`
			State   string `json:"state"`
			Context string `json:"context"`
		} `json:"statuses"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 512<<10)).Decode(&payload); err != nil || strings.ToLower(strings.TrimSpace(payload.SHA)) != headSHA || len(payload.Statuses) > maxGitHubReleaseChecks {
		return nil, fmt.Errorf("release Gatekeeper commit statuses response is invalid")
	}
	type latestStatus struct {
		ID       int64
		Evidence releasegate.CheckEvidence
	}
	latest := map[string]latestStatus{}
	for _, status := range payload.Statuses {
		name := strings.TrimSpace(status.Context)
		if status.ID < 1 || !validGitHubReleaseEvidenceName(status.Context) || name != status.Context {
			return nil, fmt.Errorf("release Gatekeeper commit statuses response is invalid")
		}
		state := releasegate.StatusFailed
		if strings.EqualFold(strings.TrimSpace(status.State), "success") {
			state = releasegate.StatusPassed
		}
		evidence := releasegate.CheckEvidence{Repository: repository, Name: name, HeadSHA: headSHA, Status: state}
		key := strings.ToLower(name)
		if previous, ok := latest[key]; !ok || status.ID > previous.ID {
			latest[key] = latestStatus{ID: status.ID, Evidence: evidence}
		}
	}
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]releasegate.CheckEvidence, 0, len(keys))
	for _, key := range keys {
		result = append(result, latest[key].Evidence)
	}
	return result, nil
}

func hasGitHubNextPage(link string) bool {
	return strings.Contains(strings.ToLower(link), `rel="next"`)
}

func validGitHubReleaseEvidenceName(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 160 {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return character < 0x20 || character == 0x7f
	}) == -1
}
