package automationagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"events-stocks/internal/releasegate"
)

type releaseGateChangeSet struct {
	CommitSHA      string `json:"commit_sha"`
	ReviewType     string `json:"review_type"`
	PullRequestURL string `json:"pull_request_url"`
}

type githubPullRequestEvidence struct {
	HeadSHA      string
	BaseBranch   string
	AuthorActor  string
	Mergeable    bool
	ConflictFree bool
	Reviews      []releasegate.ReviewEvidence
}

// RunReleaseGateWithGitHub replaces every mutable GitHub claim in the
// control-plane candidate with a fresh repository-scoped API read. This first
// adapter intentionally leaves branch protection and checks unresolved, so PR
// metadata and reviews alone can never authorize a release.
func RunReleaseGateWithGitHub(ctx context.Context, delivery json.RawMessage, lookup func(string) string) (releasegate.Input, error) {
	input, err := RunReleaseGate(delivery)
	if err != nil {
		return releasegate.Input{}, err
	}
	var envelope struct {
		ChangeSets []releaseGateChangeSet `json:"change_sets"`
	}
	if err := json.Unmarshal(delivery, &envelope); err != nil {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper change-set envelope is invalid")
	}
	changes, err := releaseGateChangesByRepository(envelope.ChangeSets, input.Revisions)
	if err != nil {
		return releasegate.Input{}, err
	}
	config, err := LoadGitHubAppConfig(lookup)
	if err != nil {
		return releasegate.Input{}, err
	}
	input.Branches = nil
	input.Checks = nil
	input.Reviews = nil
	for index := range input.Revisions {
		revision := &input.Revisions[index]
		repository := strings.ToLower(strings.TrimSpace(revision.Repository))
		change := changes[repository]
		prNumber, err := releaseGatePullRequestNumber(change.PullRequestURL, repository)
		if err != nil {
			return releasegate.Input{}, err
		}
		token, err := MintGitHubRepositoryToken(ctx, config, nil, time.Now().UTC(), repository)
		if err != nil {
			return releasegate.Input{}, fmt.Errorf("release Gatekeeper could not authenticate the repository: %w", err)
		}
		evidence, err := ReadGitHubReleasePullRequestEvidence(ctx, config, token.Token, repository, prNumber)
		if err != nil {
			return releasegate.Input{}, err
		}
		if !strings.EqualFold(evidence.HeadSHA, revision.SHA) || !strings.EqualFold(change.CommitSHA, revision.SHA) {
			return releasegate.Input{}, fmt.Errorf("release Gatekeeper pull request head changed after publication")
		}
		revision.Branch = evidence.BaseBranch
		input.Branches = append(input.Branches, releasegate.BranchEvidence{
			Repository: repository, HeadSHA: evidence.HeadSHA,
			Mergeable: evidence.Mergeable, ConflictFree: evidence.ConflictFree,
			ProtectionEvaluated: false, RequiredChecks: []string{},
		})
		input.Reviews = append(input.Reviews, evidence.Reviews...)
	}
	if _, err := releasegate.RevisionMatrixDigest(input.Revisions); err != nil {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper GitHub revision matrix is invalid")
	}
	return input, nil
}

func releaseGateChangesByRepository(changes []releaseGateChangeSet, revisions []releasegate.Revision) (map[string]releaseGateChangeSet, error) {
	required := make(map[string]string, len(revisions))
	for _, revision := range revisions {
		repository := strings.ToLower(strings.TrimSpace(revision.Repository))
		required[repository] = strings.ToLower(strings.TrimSpace(revision.SHA))
	}
	result := make(map[string]releaseGateChangeSet, len(required))
	for _, change := range changes {
		if !strings.EqualFold(strings.TrimSpace(change.ReviewType), "pull_request") {
			continue
		}
		repository, _, err := parseReleaseGatePullRequestURL(change.PullRequestURL)
		if err != nil {
			continue
		}
		expectedSHA, needed := required[repository]
		if !needed {
			continue
		}
		if _, duplicate := result[repository]; duplicate || !strings.EqualFold(strings.TrimSpace(change.CommitSHA), expectedSHA) {
			return nil, fmt.Errorf("release Gatekeeper published pull request matrix is ambiguous")
		}
		change.CommitSHA = strings.ToLower(strings.TrimSpace(change.CommitSHA))
		result[repository] = change
	}
	if len(result) != len(required) {
		return nil, fmt.Errorf("release Gatekeeper requires one published pull request per repository")
	}
	return result, nil
}

func releaseGatePullRequestNumber(value, repository string) (int, error) {
	actualRepository, number, err := parseReleaseGatePullRequestURL(value)
	if err != nil || actualRepository != repository {
		return 0, fmt.Errorf("release Gatekeeper pull request URL does not match its repository")
	}
	return number, nil
}

func parseReleaseGatePullRequestURL(value string) (string, int, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", 0, fmt.Errorf("release Gatekeeper pull request URL is invalid")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return "", 0, fmt.Errorf("release Gatekeeper pull request URL is invalid")
	}
	repository := strings.ToLower(parts[0] + "/" + parts[1])
	number, err := strconv.Atoi(parts[3])
	if !githubRepositoryNamePattern.MatchString(repository) || err != nil || number < 1 {
		return "", 0, fmt.Errorf("release Gatekeeper pull request URL is invalid")
	}
	return repository, number, nil
}

// ReadGitHubReleasePullRequestEvidence reads only bounded PR/review metadata.
// Required checks and protection rules are deliberately owned by a separate
// adapter so an incomplete API response cannot be mistaken for no policy.
func ReadGitHubReleasePullRequestEvidence(ctx context.Context, config GitHubAppConfig, token, repository string, number int) (githubPullRequestEvidence, error) {
	repository = strings.ToLower(strings.TrimSpace(repository))
	if !githubRepositoryNamePattern.MatchString(repository) || number < 1 || strings.TrimSpace(token) == "" {
		return githubPullRequestEvidence{}, fmt.Errorf("release Gatekeeper GitHub PR lookup is invalid")
	}
	parts := strings.Split(repository, "/")
	baseURL := strings.TrimRight(config.APIBaseURL, "/") + "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/pulls/" + strconv.Itoa(number)
	request, err := githubAppRequest(ctx, http.MethodGet, baseURL, token, nil)
	if err != nil {
		return githubPullRequestEvidence{}, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return githubPullRequestEvidence{}, fmt.Errorf("read release Gatekeeper pull request")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return githubPullRequestEvidence{}, fmt.Errorf("release Gatekeeper pull request lookup was rejected (%d)", response.StatusCode)
	}
	var pull struct {
		State          string `json:"state"`
		Draft          bool   `json:"draft"`
		Merged         bool   `json:"merged"`
		Mergeable      *bool  `json:"mergeable"`
		MergeableState string `json:"mergeable_state"`
		Head           struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&pull); err != nil {
		return githubPullRequestEvidence{}, fmt.Errorf("release Gatekeeper pull request response is invalid")
	}
	headSHA := strings.ToLower(strings.TrimSpace(pull.Head.SHA))
	baseBranch, author := strings.TrimSpace(pull.Base.Ref), strings.TrimSpace(pull.User.Login)
	if !strings.EqualFold(strings.TrimSpace(pull.State), "open") || pull.Draft || pull.Merged || !validGitHubCommitSHA(headSHA) || baseBranch == "" || len(baseBranch) > 240 || author == "" || len(author) > 100 {
		return githubPullRequestEvidence{}, fmt.Errorf("release Gatekeeper pull request is not an open reviewable exact revision")
	}

	reviews, err := readGitHubReleaseReviews(ctx, client, config, token, baseURL+"/reviews?per_page=100", repository, headSHA, author)
	if err != nil {
		return githubPullRequestEvidence{}, err
	}
	mergeable := pull.Mergeable != nil && *pull.Mergeable
	return githubPullRequestEvidence{
		HeadSHA: headSHA, BaseBranch: baseBranch, AuthorActor: author,
		Mergeable: mergeable, ConflictFree: mergeable && !strings.EqualFold(strings.TrimSpace(pull.MergeableState), "dirty"),
		Reviews: reviews,
	}, nil
}

func readGitHubReleaseReviews(ctx context.Context, client *http.Client, config GitHubAppConfig, token, endpoint, repository, headSHA, author string) ([]releasegate.ReviewEvidence, error) {
	request, err := githubAppRequest(ctx, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("read release Gatekeeper reviews")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || strings.Contains(response.Header.Get("Link"), `rel="next"`) {
		return nil, fmt.Errorf("release Gatekeeper review history is unavailable or exceeds the bounded page")
	}
	var payload []struct {
		ID       int64  `json:"id"`
		State    string `json:"state"`
		CommitID string `json:"commit_id"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&payload); err != nil || len(payload) > 100 {
		return nil, fmt.Errorf("release Gatekeeper review history is invalid")
	}
	type decisiveReview struct {
		ID                     int64
		State, CommitID, Actor string
	}
	latest := map[string]decisiveReview{}
	for _, review := range payload {
		state := strings.ToUpper(strings.TrimSpace(review.State))
		if state != "APPROVED" && state != "CHANGES_REQUESTED" && state != "DISMISSED" {
			continue
		}
		actor := strings.TrimSpace(review.User.Login)
		if review.ID < 1 || actor == "" || len(actor) > 100 {
			return nil, fmt.Errorf("release Gatekeeper review history is invalid")
		}
		key := strings.ToLower(actor)
		if previous, ok := latest[key]; !ok || review.ID > previous.ID {
			latest[key] = decisiveReview{ID: review.ID, State: state, CommitID: strings.ToLower(strings.TrimSpace(review.CommitID)), Actor: actor}
		}
	}
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]releasegate.ReviewEvidence, 0, len(keys))
	for _, key := range keys {
		review := latest[key]
		if review.State == "DISMISSED" {
			continue
		}
		reviewSHA := review.CommitID
		blocking := 0
		if review.State == "CHANGES_REQUESTED" {
			reviewSHA, blocking = headSHA, 1
		}
		if !validGitHubCommitSHA(reviewSHA) {
			continue
		}
		result = append(result, releasegate.ReviewEvidence{Repository: repository, HeadSHA: reviewSHA, AuthorActor: author, ReviewerActor: review.Actor, Approved: review.State == "APPROVED", BlockingChangeRequests: blocking})
	}
	return result, nil
}
