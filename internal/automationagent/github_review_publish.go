package automationagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var githubAppSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

const (
	maxGitHubReviewLookupPages = 10
	githubExactSHAReviewCheck  = "Bema Review / exact-sha"
)

// GitHubCodeReviewPublication is the credential-free, exact-SHA handoff from
// the Reviewer lane. GitHub remains the authority for the review itself.
type GitHubCodeReviewPublication struct {
	SchemaVersion int    `json:"schema_version"`
	Repository    string `json:"repository"`
	PullRequest   int    `json:"pull_request"`
	HeadSHA       string `json:"head_sha"`
	PatchSHA256   string `json:"patch_sha256"`
	SubjectSHA256 string `json:"subject_sha256"`
	PayloadSHA256 string `json:"payload_sha256"`
	Verdict       string `json:"verdict"`
	Event         string `json:"event"`
	// ReviewGatePassed is calculated from the already parsed, boundary-checked
	// verdict before this record is published. It is never model-provided: only
	// an independent approval, or an explicitly non-blocking maintainability
	// note, may pass the exact-SHA review gate.
	ReviewGatePassed bool      `json:"review_gate_passed"`
	ReviewID         int64     `json:"review_id"`
	ReviewURL        string    `json:"review_url"`
	ReviewerActor    string    `json:"reviewer_actor"`
	AuthorActor      string    `json:"author_actor"`
	Reused           bool      `json:"reused"`
	CheckRunID       int64     `json:"check_run_id"`
	CheckRunURL      string    `json:"check_run_url"`
	CheckName        string    `json:"check_name"`
	CheckConclusion  string    `json:"check_conclusion"`
	CheckReused      bool      `json:"check_reused"`
	PublishedAt      time.Time `json:"published_at"`
}

type githubReviewComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
}

type githubReviewCreatePayload struct {
	CommitID string                `json:"commit_id"`
	Body     string                `json:"body"`
	Event    string                `json:"event"`
	Comments []githubReviewComment `json:"comments,omitempty"`
}

type githubRemoteReview struct {
	ID        int64  `json:"id"`
	State     string `json:"state"`
	Body      string `json:"body"`
	CommitID  string `json:"commit_id"`
	HTMLURL   string `json:"html_url"`
	Submitted string `json:"submitted_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

type githubCheckRunPayload struct {
	Name       string               `json:"name"`
	HeadSHA    string               `json:"head_sha,omitempty"`
	ExternalID string               `json:"external_id"`
	Status     string               `json:"status"`
	Conclusion string               `json:"conclusion"`
	DetailsURL string               `json:"details_url"`
	Output     githubCheckRunOutput `json:"output"`
}

type githubCheckRunOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Text    string `json:"text"`
}

type githubRemoteCheckRun struct {
	ID         int64                `json:"id"`
	Name       string               `json:"name"`
	HeadSHA    string               `json:"head_sha"`
	ExternalID string               `json:"external_id"`
	Status     string               `json:"status"`
	Conclusion string               `json:"conclusion"`
	HTMLURL    string               `json:"html_url"`
	DetailsURL string               `json:"details_url"`
	Output     githubCheckRunOutput `json:"output"`
	App        struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	} `json:"app"`
}

func CodeReviewPublicationHandoff(publication GitHubCodeReviewPublication) map[string]any {
	return map[string]any{
		"schema_version": publication.SchemaVersion, "repository": publication.Repository,
		"pull_request": publication.PullRequest, "head_sha": publication.HeadSHA, "patch_sha256": publication.PatchSHA256,
		"subject_sha256": publication.SubjectSHA256, "payload_sha256": publication.PayloadSHA256,
		"verdict": publication.Verdict, "event": publication.Event, "review_gate_passed": publication.ReviewGatePassed, "review_id": publication.ReviewID,
		"review_url": publication.ReviewURL, "reviewer_actor": publication.ReviewerActor, "author_actor": publication.AuthorActor,
		"reused": publication.Reused, "check_run_id": publication.CheckRunID, "check_run_url": publication.CheckRunURL,
		"check_name": publication.CheckName, "check_conclusion": publication.CheckConclusion, "check_reused": publication.CheckReused,
		"published_at": publication.PublishedAt.UTC().Format(time.RFC3339),
	}
}

// PublishGitHubCodeReview relays only a validated Reviewer verdict. It uses a
// repository-scoped token minted from the Review lane's own App identity. A
// subject marker makes ordinary delivery recovery idempotent and rejects a
// second, conflicting model result for the same frozen pull-request head. An
// explicitly authorized retry is the narrow exception: it may supersede this
// App's prior failed check for the same immutable subject after a new,
// validated verdict has been published.
func PublishGitHubCodeReview(ctx context.Context, boundary CodeReviewInput, review map[string]any, lookup func(string) string, allowFailedCheckSupersede bool) (GitHubCodeReviewPublication, error) {
	if boundary.Remote == nil {
		return GitHubCodeReviewPublication{}, fmt.Errorf("remote code review target is required")
	}
	if err := ValidateCodeReviewBoundary(review, boundary); err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	repository, err := parseGitHubRepositoryReference(boundary.RepositoryRef)
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	config, err := LoadGitHubAppConfig(lookup)
	if err != nil {
		return GitHubCodeReviewPublication{}, fmt.Errorf("reviewer GitHub App is not configured")
	}
	config, err = config.WithInstallationID(boundary.Remote.InstallationID)
	if err != nil {
		return GitHubCodeReviewPublication{}, fmt.Errorf("reviewer GitHub App installation is not authorized")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	actor, err := readGitHubAppActor(ctx, config, client, time.Now().UTC())
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	repositoryName := strings.ToLower(repository.Owner + "/" + repository.Name)
	// Resolve the installation for the exact repository immediately before
	// minting the narrowed token. This both rejects a stale webhook installation
	// after an App reinstall and mirrors GitHub's current repository selection
	// before requesting the least-privilege token.
	resolvedInstallationID, err := readGitHubRepositoryInstallationID(ctx, config, client, time.Now().UTC(), repositoryName, true)
	if err != nil {
		return GitHubCodeReviewPublication{}, fmt.Errorf("resolve Reviewer repository installation: %w", err)
	}
	if resolvedInstallationID != boundary.Remote.InstallationID {
		return GitHubCodeReviewPublication{}, fmt.Errorf("reviewer GitHub App installation changed after the frozen webhook")
	}
	token, err := mintGitHubInstallationToken(ctx, config, client, time.Now().UTC(), true, repository.Name)
	if err != nil {
		return GitHubCodeReviewPublication{}, fmt.Errorf("mint repository-scoped Reviewer token: %w", err)
	}
	state, err := ReadGitHubPullRequestState(ctx, config, token.Token, repositoryName, boundary.Remote.PullRequestNumber)
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	if !state.Open || state.Draft || state.Merged || !strings.EqualFold(state.HeadSHA, boundary.HeadSHA) {
		return GitHubCodeReviewPublication{}, fmt.Errorf("pull request no longer matches the frozen review head")
	}
	if strings.TrimSpace(state.AuthorActor) == "" {
		return GitHubCodeReviewPublication{}, fmt.Errorf("pull request author identity is unavailable")
	}
	verdict := strings.ToLower(strings.TrimSpace(stringAny(review["verdict"])))
	event := githubReviewEvent(verdict)
	if event == "APPROVE" && strings.EqualFold(actor, state.AuthorActor) {
		// GitHub will reject self-approval. Preserve the useful result as a
		// comment while keeping the independent approval gate unsatisfied.
		event = "COMMENT"
	}
	reviewGatePassed := codeReviewPassesExactSHAGate(review, event, actor, state.AuthorActor)
	payload, err := githubReviewPayload(boundary, review, event)
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	subjectSHA256, err := CodeReviewPublicationSubjectSHA256(boundary)
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	payloadSHA256, err := canonicalSHA256(payload)
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	marker := githubReviewMarker(subjectSHA256, payloadSHA256)
	payload.Body += "\n\n" + marker
	baseURL := strings.TrimRight(config.APIBaseURL, "/") + "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name) + "/pulls/" + strconv.Itoa(boundary.Remote.PullRequestNumber)
	existing, found, err := findGitHubCodeReview(ctx, client, token.Token, baseURL+"/reviews", subjectSHA256, payloadSHA256, boundary.HeadSHA, event)
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	if found {
		result, resultErr := githubReviewPublication(boundary, repositoryName, verdict, event, subjectSHA256, payloadSHA256, state.AuthorActor, existing, true)
		if resultErr != nil {
			return GitHubCodeReviewPublication{}, resultErr
		}
		if !strings.EqualFold(result.ReviewerActor, actor) {
			return GitHubCodeReviewPublication{}, fmt.Errorf("existing GitHub review belongs to a different identity")
		}
		result.ReviewGatePassed = reviewGatePassed
		return publishGitHubExactSHAReviewCheck(ctx, client, config, token.Token, repository, result, allowFailedCheckSupersede)
	}
	published, statusCode, err := postGitHubCodeReview(ctx, client, token.Token, baseURL+"/reviews", payload)
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	if statusCode == http.StatusUnprocessableEntity && len(payload.Comments) > 0 {
		// GitHub can reject an otherwise valid exact-SHA review when an inline
		// anchor is no longer representable by its diff API. Preserve every
		// finding in the review body and retry once without inline side effects;
		// the failure check still blocks unsafe code.
		published, statusCode, err = postGitHubCodeReview(ctx, client, token.Token, baseURL+"/reviews", githubReviewBodyFallback(payload))
		if err != nil {
			return GitHubCodeReviewPublication{}, err
		}
	}
	if statusCode != http.StatusOK {
		return GitHubCodeReviewPublication{}, fmt.Errorf("GitHub pull request review was rejected (%d)", statusCode)
	}
	result, err := githubReviewPublication(boundary, repositoryName, verdict, event, subjectSHA256, payloadSHA256, state.AuthorActor, published, false)
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	if !strings.EqualFold(result.ReviewerActor, actor) || !strings.Contains(published.Body, marker) {
		return GitHubCodeReviewPublication{}, fmt.Errorf("GitHub pull request review identity is invalid")
	}
	result.ReviewGatePassed = reviewGatePassed
	return publishGitHubExactSHAReviewCheck(ctx, client, config, token.Token, repository, result, allowFailedCheckSupersede)
}

// codeReviewPassesExactSHAGate separates a merge-relevant review result from
// an advisory note. The input has already passed ParseCodeReview and
// ValidateCodeReviewBoundary, but this predicate remains deliberately narrow:
// a low security, correctness, reliability, performance or test-coverage
// observation is never silently made merge-eligible. Only an independent
// approval, or a concrete low maintainability comment with no evidence gap,
// can pass. The low comment stays attached to the PR for follow-up.
func codeReviewPassesExactSHAGate(review map[string]any, event, reviewerActor, authorActor string) bool {
	if strings.TrimSpace(reviewerActor) == "" || strings.EqualFold(reviewerActor, authorActor) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(stringAny(review["verdict"]))) {
	case "approve":
		return event == "APPROVE"
	case "comment":
		if event != "COMMENT" {
			return false
		}
		gaps, ok := review["coverage_gaps"].([]any)
		if !ok || len(gaps) != 0 {
			return false
		}
		findings, ok := review["findings"].([]any)
		if !ok || len(findings) == 0 {
			return false
		}
		for _, raw := range findings {
			finding, ok := raw.(map[string]any)
			if !ok || strings.ToLower(strings.TrimSpace(stringAny(finding["severity"]))) != "low" || strings.ToLower(strings.TrimSpace(stringAny(finding["category"]))) != "maintainability" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func postGitHubCodeReview(ctx context.Context, client *http.Client, token, endpoint string, payload githubReviewCreatePayload) (githubRemoteReview, int, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return githubRemoteReview{}, 0, err
	}
	request, err := githubAppRequest(ctx, http.MethodPost, endpoint, token, strings.NewReader(string(encoded)))
	if err != nil {
		return githubRemoteReview{}, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return githubRemoteReview{}, 0, fmt.Errorf("publish GitHub pull request review")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 128<<10))
		return githubRemoteReview{}, response.StatusCode, nil
	}
	var published githubRemoteReview
	if err := json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&published); err != nil {
		return githubRemoteReview{}, response.StatusCode, fmt.Errorf("GitHub pull request review response is invalid")
	}
	return published, response.StatusCode, nil
}

func githubReviewBodyFallback(payload githubReviewCreatePayload) githubReviewCreatePayload {
	var body strings.Builder
	body.WriteString(payload.Body)
	body.WriteString("\n\nInline anchors were unavailable; findings are preserved below:\n")
	for _, comment := range payload.Comments {
		body.WriteString("\n- **")
		body.WriteString(comment.Path)
		body.WriteString(":")
		body.WriteString(strconv.Itoa(comment.Line))
		body.WriteString("** — ")
		body.WriteString(comment.Body)
	}
	payload.Body = body.String()
	if len(payload.Body) > 60_000 {
		payload.Body = payload.Body[:60_000]
	}
	payload.Comments = nil
	return payload
}

func publishGitHubExactSHAReviewCheck(ctx context.Context, client *http.Client, config GitHubAppConfig, token string, repository githubRepository, publication GitHubCodeReviewPublication, allowFailedCheckSupersede bool) (GitHubCodeReviewPublication, error) {
	appID, err := strconv.ParseInt(strings.TrimSpace(config.AppID), 10, 64)
	if err != nil || appID < 1 || publication.ReviewID < 1 || !validGitHubCommitSHA(publication.HeadSHA) {
		return GitHubCodeReviewPublication{}, fmt.Errorf("reviewer check identity is invalid")
	}
	appSlug := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(publication.ReviewerActor)), "[bot]")
	if !githubAppSlugPattern.MatchString(appSlug) {
		return GitHubCodeReviewPublication{}, fmt.Errorf("reviewer check identity is invalid")
	}
	conclusion := "failure"
	if publication.ReviewGatePassed {
		conclusion = "success"
	}
	gateSummary := "Automated review did not pass the exact-SHA merge gate."
	if publication.ReviewGatePassed && publication.Verdict == "comment" {
		gateSummary = "Exact-SHA review passed; only non-blocking low maintainability notes remain on the pull request."
	} else if publication.ReviewGatePassed {
		gateSummary = "Independent exact-SHA approval passed the merge gate."
	}
	marker := githubReviewCheckMarker(publication.SubjectSHA256, publication.PayloadSHA256)
	payload := githubCheckRunPayload{
		Name: githubExactSHAReviewCheck, HeadSHA: publication.HeadSHA, ExternalID: publication.SubjectSHA256,
		Status: "completed", Conclusion: conclusion, DetailsURL: publication.ReviewURL,
		Output: githubCheckRunOutput{Title: "Bema Reviewer: " + conclusion, Summary: gateSummary + " Head: " + publication.HeadSHA + ".", Text: marker},
	}
	baseURL := strings.TrimRight(config.APIBaseURL, "/") + "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name)
	existing, found, exact, err := findGitHubExactSHAReviewCheck(ctx, client, token, baseURL+"/commits/"+url.PathEscape(publication.HeadSHA)+"/check-runs", appID, appSlug, payload)
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	reused := false
	if found {
		reused = exact && existing.Status == payload.Status && existing.Conclusion == payload.Conclusion && existing.DetailsURL == payload.DetailsURL
		if !reused {
			if !exact && (!allowFailedCheckSupersede || existing.Conclusion != "failure") {
				return GitHubCodeReviewPublication{}, fmt.Errorf("a conflicting Reviewer check already exists for the frozen subject")
			}
			patchPayload := payload
			patchPayload.HeadSHA = ""
			existing, err = writeGitHubExactSHAReviewCheck(ctx, client, token, baseURL+"/check-runs/"+strconv.FormatInt(existing.ID, 10), http.MethodPatch, patchPayload)
		}
	} else {
		existing, err = writeGitHubExactSHAReviewCheck(ctx, client, token, baseURL+"/check-runs", http.MethodPost, payload)
	}
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	if err := validateGitHubExactSHAReviewCheck(existing, appID, appSlug, payload); err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	publication.CheckRunID, publication.CheckRunURL = existing.ID, existing.HTMLURL
	publication.CheckName, publication.CheckConclusion, publication.CheckReused = existing.Name, existing.Conclusion, reused
	return publication, nil
}

func githubReviewCheckMarker(subjectSHA256, payloadSHA256 string) string {
	return "itbem-review-check subject=" + subjectSHA256 + " payload=" + payloadSHA256
}

func findGitHubExactSHAReviewCheck(ctx context.Context, client *http.Client, token, endpoint string, appID int64, appSlug string, expected githubCheckRunPayload) (githubRemoteCheckRun, bool, bool, error) {
	request, err := githubAppRequest(ctx, http.MethodGet, endpoint+"?check_name="+url.QueryEscape(expected.Name)+"&filter=all&per_page=100", token, nil)
	if err != nil {
		return githubRemoteCheckRun{}, false, false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return githubRemoteCheckRun{}, false, false, fmt.Errorf("read existing Reviewer checks")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return githubRemoteCheckRun{}, false, false, fmt.Errorf("GitHub Reviewer check lookup was rejected (%d)", response.StatusCode)
	}
	var result struct {
		TotalCount int                    `json:"total_count"`
		CheckRuns  []githubRemoteCheckRun `json:"check_runs"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result); err != nil || result.TotalCount > 100 || len(result.CheckRuns) > 100 || strings.Contains(response.Header.Get("Link"), `rel="next"`) {
		return githubRemoteCheckRun{}, false, false, fmt.Errorf("GitHub Reviewer check lookup response exceeds the bounded history")
	}
	subjectPrefix := "itbem-review-check subject=" + expected.ExternalID + " payload="
	for _, check := range result.CheckRuns {
		if check.App.ID != appID || !strings.EqualFold(check.App.Slug, appSlug) || check.Name != expected.Name || !strings.EqualFold(check.HeadSHA, expected.HeadSHA) || check.ExternalID != expected.ExternalID {
			continue
		}
		if !strings.Contains(check.Output.Text, subjectPrefix) {
			return githubRemoteCheckRun{}, false, false, fmt.Errorf("a conflicting Reviewer check already exists for the frozen subject")
		}
		return check, true, strings.Contains(check.Output.Text, expected.Output.Text), nil
	}
	return githubRemoteCheckRun{}, false, false, nil
}

func writeGitHubExactSHAReviewCheck(ctx context.Context, client *http.Client, token, endpoint, method string, payload githubCheckRunPayload) (githubRemoteCheckRun, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return githubRemoteCheckRun{}, err
	}
	request, err := githubAppRequest(ctx, method, endpoint, token, strings.NewReader(string(encoded)))
	if err != nil {
		return githubRemoteCheckRun{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return githubRemoteCheckRun{}, fmt.Errorf("publish GitHub Reviewer check")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return githubRemoteCheckRun{}, fmt.Errorf("GitHub Reviewer check was rejected (%d)", response.StatusCode)
	}
	var check githubRemoteCheckRun
	if err := json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&check); err != nil {
		return githubRemoteCheckRun{}, fmt.Errorf("GitHub Reviewer check response is invalid")
	}
	return check, nil
}

func validateGitHubExactSHAReviewCheck(check githubRemoteCheckRun, appID int64, appSlug string, expected githubCheckRunPayload) error {
	parsedURL, err := url.Parse(strings.TrimSpace(check.HTMLURL))
	if err != nil || parsedURL.Scheme != "https" || !strings.EqualFold(parsedURL.Hostname(), "github.com") || check.ID < 1 || check.App.ID != appID || !strings.EqualFold(check.App.Slug, appSlug) || check.Name != expected.Name || !strings.EqualFold(check.HeadSHA, expected.HeadSHA) || check.ExternalID != expected.ExternalID || check.Status != "completed" || check.Conclusion != expected.Conclusion || check.DetailsURL != expected.DetailsURL || !strings.Contains(check.Output.Text, expected.Output.Text) {
		return fmt.Errorf("GitHub Reviewer check identity is invalid")
	}
	return nil
}

func githubReviewEvent(verdict string) string {
	switch verdict {
	case "approve":
		return "APPROVE"
	case "request_changes":
		return "REQUEST_CHANGES"
	default:
		return "COMMENT"
	}
}

func githubReviewPayload(boundary CodeReviewInput, review map[string]any, event string) (githubReviewCreatePayload, error) {
	comments := make([]githubReviewComment, 0)
	findings, _ := review["findings"].([]any)
	for _, raw := range findings {
		finding, ok := raw.(map[string]any)
		if !ok {
			return githubReviewCreatePayload{}, fmt.Errorf("code review finding is invalid")
		}
		start, startOK := integralReviewLine(finding["line_start"])
		end, endOK := integralReviewLine(finding["line_end"])
		if !startOK || !endOK || end < start {
			return githubReviewCreatePayload{}, fmt.Errorf("code review finding line range is invalid")
		}
		side := "RIGHT"
		if strings.EqualFold(stringAny(finding["side"]), "base") {
			side = "LEFT"
		}
		comment := githubReviewComment{
			Path: stringAny(finding["file"]), Line: end, Side: side,
			Body: safeGitHubReviewText(fmt.Sprintf("**%s · %s**\n\n%s\n\nRecommendation: %s", strings.ToUpper(stringAny(finding["severity"])), stringAny(finding["title"]), stringAny(finding["evidence"]), stringAny(finding["recommendation"])), 4000),
		}
		if start < end {
			comment.StartLine, comment.StartSide = start, side
		}
		comments = append(comments, comment)
	}
	body := safeGitHubReviewText(stringAny(review["summary"]), 1200)
	body += "\n\nVerdict: **" + strings.ToLower(strings.TrimSpace(stringAny(review["verdict"]))) + "**"
	if event == "COMMENT" && strings.EqualFold(stringAny(review["verdict"]), "approve") {
		body += "\n\nIndependent approval remains required because the Reviewer App is also the pull-request author."
	}
	if plan, ok := review["test_plan"].([]any); ok && len(plan) > 0 {
		body += "\n\nValidation:\n"
		for _, item := range plan {
			body += "- " + safeGitHubReviewText(stringAny(item), 500) + "\n"
		}
	}
	if gaps, ok := review["coverage_gaps"].([]any); ok && len(gaps) > 0 {
		body += "\nCoverage gaps:\n"
		for _, item := range gaps {
			body += "- " + safeGitHubReviewText(stringAny(item), 500) + "\n"
		}
	}
	return githubReviewCreatePayload{CommitID: boundary.HeadSHA, Body: strings.TrimSpace(body), Event: event, Comments: comments}, nil
}

func safeGitHubReviewText(value string, limit int) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "<!--", "&lt;!--")
	value = strings.ReplaceAll(value, "-->", "--&gt;")
	value = strings.ReplaceAll(value, "@", "@\u200b")
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

func githubReviewMarker(subjectSHA256, payloadSHA256 string) string {
	return "<!-- itbem-review subject=" + subjectSHA256 + " payload=" + payloadSHA256 + " -->"
}

func findGitHubCodeReview(ctx context.Context, client *http.Client, token, endpoint, subjectSHA256, payloadSHA256, headSHA, event string) (githubRemoteReview, bool, error) {
	subjectMarker := "<!-- itbem-review subject=" + subjectSHA256 + " payload="
	expectedMarker := githubReviewMarker(subjectSHA256, payloadSHA256)
	expectedState := map[string]string{"APPROVE": "APPROVED", "REQUEST_CHANGES": "CHANGES_REQUESTED", "COMMENT": "COMMENTED"}[event]
	for page := 1; page <= maxGitHubReviewLookupPages; page++ {
		request, err := githubAppRequest(ctx, http.MethodGet, endpoint+"?per_page=100&page="+strconv.Itoa(page), token, nil)
		if err != nil {
			return githubRemoteReview{}, false, err
		}
		response, err := client.Do(request)
		if err != nil {
			return githubRemoteReview{}, false, fmt.Errorf("read existing GitHub reviews")
		}
		var reviews []githubRemoteReview
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&reviews)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return githubRemoteReview{}, false, fmt.Errorf("GitHub review lookup was rejected (%d)", response.StatusCode)
		}
		if decodeErr != nil || len(reviews) > 100 {
			return githubRemoteReview{}, false, fmt.Errorf("GitHub review lookup response is invalid")
		}
		for _, review := range reviews {
			if !strings.Contains(review.Body, subjectMarker) {
				continue
			}
			if !strings.Contains(review.Body, expectedMarker) || !strings.EqualFold(review.CommitID, headSHA) || !strings.EqualFold(review.State, expectedState) {
				return githubRemoteReview{}, false, fmt.Errorf("a conflicting GitHub review already exists for the frozen subject")
			}
			return review, true, nil
		}
		if len(reviews) < 100 {
			return githubRemoteReview{}, false, nil
		}
	}
	return githubRemoteReview{}, false, fmt.Errorf("GitHub review history exceeds the bounded idempotency lookup")
}

func githubReviewPublication(boundary CodeReviewInput, repository, verdict, event, subjectSHA256, payloadSHA256, author string, review githubRemoteReview, reused bool) (GitHubCodeReviewPublication, error) {
	submittedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(review.Submitted))
	if err != nil || review.ID < 1 || !strings.EqualFold(review.CommitID, boundary.HeadSHA) || strings.TrimSpace(review.User.Login) == "" {
		return GitHubCodeReviewPublication{}, fmt.Errorf("GitHub pull request review response is invalid")
	}
	parsedURL, err := url.Parse(strings.TrimSpace(review.HTMLURL))
	if err != nil || parsedURL.Scheme != "https" || !strings.EqualFold(parsedURL.Hostname(), "github.com") {
		return GitHubCodeReviewPublication{}, fmt.Errorf("GitHub pull request review URL is invalid")
	}
	return GitHubCodeReviewPublication{
		SchemaVersion: 2, Repository: repository, PullRequest: boundary.Remote.PullRequestNumber,
		HeadSHA: boundary.HeadSHA, PatchSHA256: boundary.PatchSHA256, SubjectSHA256: subjectSHA256, PayloadSHA256: payloadSHA256,
		Verdict: verdict, Event: event, ReviewID: review.ID, ReviewURL: parsedURL.String(),
		ReviewerActor: strings.ToLower(strings.TrimSpace(review.User.Login)), AuthorActor: strings.ToLower(strings.TrimSpace(author)),
		Reused: reused, PublishedAt: submittedAt.UTC(),
	}, nil
}

func readGitHubAppActor(ctx context.Context, config GitHubAppConfig, client *http.Client, now time.Time) (string, error) {
	assertion, err := signGitHubAppAssertion(config, now)
	if err != nil {
		return "", err
	}
	request, err := githubAppRequest(ctx, http.MethodGet, strings.TrimRight(config.APIBaseURL, "/")+"/app", assertion, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("read Reviewer GitHub App identity")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("reviewer GitHub App identity was rejected (%d)", response.StatusCode)
	}
	var payload struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&payload); err != nil {
		return "", fmt.Errorf("reviewer GitHub App identity is invalid")
	}
	slug := strings.ToLower(strings.TrimSpace(payload.Slug))
	if !githubAppSlugPattern.MatchString(slug) {
		return "", fmt.Errorf("reviewer GitHub App identity is invalid")
	}
	return slug + "[bot]", nil
}
