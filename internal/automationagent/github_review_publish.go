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

const maxGitHubReviewLookupPages = 10

// GitHubCodeReviewPublication is the credential-free, exact-SHA handoff from
// the Reviewer lane. GitHub remains the authority for the review itself.
type GitHubCodeReviewPublication struct {
	SchemaVersion int       `json:"schema_version"`
	Repository    string    `json:"repository"`
	PullRequest   int       `json:"pull_request"`
	HeadSHA       string    `json:"head_sha"`
	PatchSHA256   string    `json:"patch_sha256"`
	SubjectSHA256 string    `json:"subject_sha256"`
	PayloadSHA256 string    `json:"payload_sha256"`
	Verdict       string    `json:"verdict"`
	Event         string    `json:"event"`
	ReviewID      int64     `json:"review_id"`
	ReviewURL     string    `json:"review_url"`
	ReviewerActor string    `json:"reviewer_actor"`
	AuthorActor   string    `json:"author_actor"`
	Reused        bool      `json:"reused"`
	PublishedAt   time.Time `json:"published_at"`
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

func CodeReviewPublicationHandoff(publication GitHubCodeReviewPublication) map[string]any {
	return map[string]any{
		"schema_version": publication.SchemaVersion, "repository": publication.Repository,
		"pull_request": publication.PullRequest, "head_sha": publication.HeadSHA, "patch_sha256": publication.PatchSHA256,
		"subject_sha256": publication.SubjectSHA256, "payload_sha256": publication.PayloadSHA256,
		"verdict": publication.Verdict, "event": publication.Event, "review_id": publication.ReviewID,
		"review_url": publication.ReviewURL, "reviewer_actor": publication.ReviewerActor, "author_actor": publication.AuthorActor,
		"reused": publication.Reused, "published_at": publication.PublishedAt.UTC().Format(time.RFC3339),
	}
}

// PublishGitHubCodeReview relays only a validated Reviewer verdict. It uses a
// repository-scoped token minted from the Review lane's own App identity. A
// subject marker makes crash recovery idempotent and rejects a second,
// conflicting model result for the same frozen pull-request head.
func PublishGitHubCodeReview(ctx context.Context, boundary CodeReviewInput, review map[string]any, lookup func(string) string) (GitHubCodeReviewPublication, error) {
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
		return GitHubCodeReviewPublication{}, fmt.Errorf("Reviewer GitHub App is not configured")
	}
	config, err = config.WithInstallationID(boundary.Remote.InstallationID)
	if err != nil {
		return GitHubCodeReviewPublication{}, fmt.Errorf("Reviewer GitHub App installation is not authorized")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	actor, err := readGitHubAppActor(ctx, config, client, time.Now().UTC())
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	token, err := mintGitHubInstallationToken(ctx, config, client, time.Now().UTC(), true, repository.Name)
	if err != nil {
		return GitHubCodeReviewPublication{}, fmt.Errorf("mint repository-scoped Reviewer token")
	}
	repositoryName := strings.ToLower(repository.Owner + "/" + repository.Name)
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
		return result, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	request, err := githubAppRequest(ctx, http.MethodPost, baseURL+"/reviews", token.Token, strings.NewReader(string(encoded)))
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return GitHubCodeReviewPublication{}, fmt.Errorf("publish GitHub pull request review")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return GitHubCodeReviewPublication{}, fmt.Errorf("GitHub pull request review was rejected (%d)", response.StatusCode)
	}
	var published githubRemoteReview
	if err := json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&published); err != nil {
		return GitHubCodeReviewPublication{}, fmt.Errorf("GitHub pull request review response is invalid")
	}
	result, err := githubReviewPublication(boundary, repositoryName, verdict, event, subjectSHA256, payloadSHA256, state.AuthorActor, published, false)
	if err != nil {
		return GitHubCodeReviewPublication{}, err
	}
	if !strings.EqualFold(result.ReviewerActor, actor) || !strings.Contains(published.Body, marker) {
		return GitHubCodeReviewPublication{}, fmt.Errorf("GitHub pull request review identity is invalid")
	}
	return result, nil
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
		start, end := int(numberAny(finding["line_start"])), int(numberAny(finding["line_end"]))
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
		SchemaVersion: 1, Repository: repository, PullRequest: boundary.Remote.PullRequestNumber,
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
		return "", fmt.Errorf("Reviewer GitHub App identity was rejected (%d)", response.StatusCode)
	}
	var payload struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&payload); err != nil {
		return "", fmt.Errorf("Reviewer GitHub App identity is invalid")
	}
	slug := strings.ToLower(strings.TrimSpace(payload.Slug))
	if !githubAppSlugPattern.MatchString(slug) {
		return "", fmt.Errorf("Reviewer GitHub App identity is invalid")
	}
	return slug + "[bot]", nil
}

func numberAny(value any) float64 {
	result, _ := value.(float64)
	return result
}
