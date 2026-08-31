package automationagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	codeReviewContextRadius   = 24
	maxCodeReviewSourceBytes  = 256 << 10
	maxCodeReviewExcerptBytes = 12 << 10
)

type codeReviewContextWindow struct {
	file  string
	side  string
	start int
	end   int
}

// ReadGitHubCodeReviewContext reads bounded source windows at the exact base
// and head SHAs already sealed by the PR patch. Paths come only from the
// trusted patch manifest; repository text remains untrusted and is redacted.
func ReadGitHubCodeReviewContext(ctx context.Context, config GitHubAppConfig, token string, review CodeReviewInput) ([]CodeReviewContextExcerpt, error) {
	repository, err := parseGitHubRepositoryReference(review.RepositoryRef)
	if err != nil || strings.TrimSpace(token) == "" || !validReviewSHA(review.BaseSHA) || !validReviewSHA(review.HeadSHA) {
		return nil, fmt.Errorf("GitHub code review context requires an immutable review and installation token")
	}
	windows := codeReviewContextWindows(review.ChangedLines)
	if len(windows) == 0 {
		return nil, fmt.Errorf("GitHub code review context has no changed source windows")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	baseURL := strings.TrimRight(config.APIBaseURL, "/") + "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name) + "/contents/"
	result := make([]CodeReviewContextExcerpt, 0, min(len(windows), maxCodeReviewExcerpts))
	total := 0
	for _, window := range windows {
		if len(result) == maxCodeReviewExcerpts || total >= maxCodeReviewContextBytes {
			break
		}
		revision := review.HeadSHA
		if window.side == "base" {
			revision = review.BaseSHA
		}
		request, requestErr := githubAppRequest(ctx, http.MethodGet, baseURL+escapeGitHubRepositoryContentPath(window.file)+"?ref="+url.QueryEscape(revision), token, nil)
		if requestErr != nil {
			return nil, requestErr
		}
		response, requestErr := client.Do(request)
		if requestErr != nil {
			return nil, fmt.Errorf("read GitHub code review source")
		}
		var payload struct {
			Type     string `json:"type"`
			Encoding string `json:"encoding"`
			Content  string `json:"content"`
			Size     int    `json:"size"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, maxCodeReviewSourceBytes*2)).Decode(&payload)
		status := response.StatusCode
		response.Body.Close()
		if status == http.StatusNotFound {
			continue
		}
		if status != http.StatusOK || decodeErr != nil {
			return nil, fmt.Errorf("GitHub code review source was rejected or invalid")
		}
		if payload.Type != "file" || !strings.EqualFold(payload.Encoding, "base64") || payload.Size < 0 || payload.Size > maxCodeReviewSourceBytes {
			continue
		}
		decoded, decodeErr := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
		if decodeErr != nil || len(decoded) == 0 || len(decoded) > maxCodeReviewSourceBytes || !validText(decoded) {
			continue
		}
		lines := strings.Split(strings.ReplaceAll(string(decoded), "\r\n", "\n"), "\n")
		start := max(1, window.start-codeReviewContextRadius)
		end := min(len(lines), window.end+codeReviewContextRadius)
		if start > end {
			continue
		}
		content := strings.Join(lines[start-1:end], "\n")
		if len(content) > maxCodeReviewExcerptBytes {
			content = content[:maxCodeReviewExcerptBytes]
		}
		if total+len(content) > maxCodeReviewContextBytes {
			content = content[:maxCodeReviewContextBytes-total]
		}
		sanitized, _ := redactWorkspaceExcerpt(content)
		if strings.TrimSpace(sanitized) == "" {
			continue
		}
		// Truncation may stop before the originally selected end line. Keep the
		// published bounds truthful to the actual excerpt delivered to the model.
		end = min(end, start+strings.Count(sanitized, "\n"))
		result = append(result, CodeReviewContextExcerpt{File: window.file, Side: window.side, Start: start, End: end, Content: sanitized})
		total += len(sanitized)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("GitHub code review context could not read any changed source")
	}
	return result, nil
}

func codeReviewContextWindows(ranges []CodeReviewChangedLineRange) []codeReviewContextWindow {
	windows := make([]codeReviewContextWindow, 0, len(ranges))
	for _, item := range ranges {
		windows = append(windows, codeReviewContextWindow{file: item.File, side: item.Side, start: item.Start, end: item.End})
	}
	sort.Slice(windows, func(i, j int) bool {
		if windows[i].file != windows[j].file {
			return windows[i].file < windows[j].file
		}
		if windows[i].side != windows[j].side {
			return windows[i].side < windows[j].side
		}
		return windows[i].start < windows[j].start
	})
	merged := make([]codeReviewContextWindow, 0, len(windows))
	for _, item := range windows {
		if len(merged) > 0 {
			last := &merged[len(merged)-1]
			if last.file == item.file && last.side == item.side && item.start <= last.end+codeReviewContextRadius*2 {
				if item.end > last.end {
					last.end = item.end
				}
				continue
			}
		}
		merged = append(merged, item)
	}
	return merged
}
