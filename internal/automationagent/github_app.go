package automationagent

import (
	"context"
	"crypto/rsa"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrGitHubAppNotConfigured is deliberately distinct from an authentication
// failure: it lets delivery remain local-only until an operator configures a
// GitHub App without falling back to a user's SSH key or personal token.
var ErrGitHubAppNotConfigured = errors.New("GitHub App installation credentials are not configured")

var githubRepositoryNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_.-]*$`)

type GitHubAppConfig struct {
	AppID          string
	InstallationID string
	// InstallationIDs is the explicit allow-list for webhook deliveries. The
	// selected InstallationID is retained for the API calls that require one
	// concrete installation token.
	InstallationIDs []string
	PrivateKey      *rsa.PrivateKey
	APIBaseURL      string
}

// GitHubRepositorySnapshot is a small, immutable remote checkpoint. It is
// safe to freeze into Delivery context: it contains no source body, token, or
// account identity beyond the repository that the human explicitly registered.
type GitHubRepositorySnapshot struct {
	Reference     string `json:"reference"`
	Revision      string `json:"revision"`
	DefaultBranch string `json:"default_branch"`
}

const (
	maxGitHubRepositoryMapFiles = 250
	// Keep the inventory well below the context-source metadata budget even
	// when a repository uses unusually long paths. This is an orientation aid,
	// never a substitute for a checked-out workspace or source contents.
	maxGitHubRepositoryMapBytes = 12 << 10
	// Remote excerpts are intentionally much smaller than local workspace
	// context. They orient a plan across registered repositories without
	// turning a GitHub App read grant into an unbounded source exfiltration
	// channel.
	maxGitHubRepositoryExcerpts     = 8
	maxGitHubRepositoryExcerptBytes = 3 << 10
	maxGitHubRepositoryContextBytes = 16 << 10
)

const maxGitHubPullRequestPatchBytes = 512 << 10

// GitHubPullRequestState is the small mutable PR checkpoint used solely to
// reject obsolete webhook deliveries. It carries no source or account data.
type GitHubPullRequestState struct {
	HeadSHA string
	Open    bool
	Draft   bool
	Merged  bool
}

// ReadGitHubPullRequestState confirms the mutable PR state immediately before
// a webhook can supersede queued work. The actual review still uses the exact
// immutable base/head comparison from the signed delivery. This read only
// rejects delayed deliveries; it never follows a newer SHA implicitly.
func ReadGitHubPullRequestState(ctx context.Context, config GitHubAppConfig, token, repository string, number int) (GitHubPullRequestState, error) {
	repository = strings.ToLower(strings.TrimSpace(repository))
	if !githubRepositoryNamePattern.MatchString(repository) || number < 1 || strings.TrimSpace(token) == "" {
		return GitHubPullRequestState{}, fmt.Errorf("GitHub pull request lookup is invalid")
	}
	parts := strings.Split(repository, "/")
	endpoint := strings.TrimRight(config.APIBaseURL, "/") + "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/pulls/" + strconv.Itoa(number)
	request, err := githubAppRequest(ctx, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return GitHubPullRequestState{}, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return GitHubPullRequestState{}, fmt.Errorf("read GitHub pull request state")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return GitHubPullRequestState{}, fmt.Errorf("GitHub pull request lookup was rejected (%d)", response.StatusCode)
	}
	var payload struct {
		State  string `json:"state"`
		Draft  bool   `json:"draft"`
		Merged bool   `json:"merged"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&payload); err != nil || !validGitHubCommitSHA(strings.ToLower(strings.TrimSpace(payload.Head.SHA))) {
		return GitHubPullRequestState{}, fmt.Errorf("GitHub pull request state is invalid")
	}
	return GitHubPullRequestState{HeadSHA: strings.ToLower(strings.TrimSpace(payload.Head.SHA)), Open: strings.EqualFold(strings.TrimSpace(payload.State), "open"), Draft: payload.Draft, Merged: payload.Merged}, nil
}

// ReadGitHubPullRequestPatch downloads the immutable compare diff between the
// exact webhook base/head commits. It never asks GitHub for the mutable current
// PR head, so a synchronize event cannot accidentally be reviewed as a later
// push that arrived while the webhook was being processed.
func ReadGitHubPullRequestPatch(ctx context.Context, config GitHubAppConfig, token, repository, baseSHA, headSHA string) (string, error) {
	repository = strings.ToLower(strings.TrimSpace(repository))
	baseSHA, headSHA = strings.ToLower(strings.TrimSpace(baseSHA)), strings.ToLower(strings.TrimSpace(headSHA))
	if !githubRepositoryNamePattern.MatchString(repository) || !validGitHubCommitSHA(baseSHA) || !validGitHubCommitSHA(headSHA) || baseSHA == headSHA || strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("GitHub pull request comparison is invalid")
	}
	parts := strings.Split(repository, "/")
	endpoint := strings.TrimRight(config.APIBaseURL, "/") + "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/compare/" + url.PathEscape(baseSHA) + "..." + url.PathEscape(headSHA)
	request, err := githubAppRequest(ctx, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github.v3.diff")
	client := &http.Client{Timeout: 25 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("read GitHub pull request comparison")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub pull request comparison was rejected (%d)", response.StatusCode)
	}
	patch, err := io.ReadAll(io.LimitReader(response.Body, maxGitHubPullRequestPatchBytes+1))
	if err != nil || len(patch) == 0 || len(patch) > maxGitHubPullRequestPatchBytes {
		return "", fmt.Errorf("GitHub pull request comparison is invalid or exceeds the review limit")
	}
	return string(patch), nil
}

// GitHubRepositoryMap is a bounded, credential-free inventory of the frozen
// remote revision. It gives a delivery planner architectural orientation when
// a project references several GitHub repositories without pretending that the
// local agent has their source code or permission to modify them.
type GitHubRepositoryMap struct {
	Revision           string   `json:"revision"`
	FileCount          int      `json:"file_count"`
	InventoryTruncated bool     `json:"inventory_truncated"`
	Files              []string `json:"files"`
}

// GitHubRepositoryExcerpt is a redacted, revision-pinned source fragment from
// an explicitly registered remote repository. It is read-only planning context
// and never makes the remote repository an implementation target.
type GitHubRepositoryExcerpt struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// GitHubRepositorySourceContext is the bounded source complement to a remote
// inventory. RedactedValues gives the control plane and the reviewer a compact
// signal that useful structure was retained while credential-shaped content was
// removed before it could become model context.
type GitHubRepositorySourceContext struct {
	Revision         string                    `json:"revision"`
	Excerpts         []GitHubRepositoryExcerpt `json:"excerpts"`
	RedactedValues   int                       `json:"redacted_values"`
	ContextTruncated bool                      `json:"context_truncated"`
}

// ReadGitHubRepositorySnapshot resolves the current default-branch commit for
// an explicitly registered github://owner/repository source. It is read-only:
// no clone, fetch, branch, PR or repository setting is modified.
func ReadGitHubRepositorySnapshot(ctx context.Context, config GitHubAppConfig, token, reference string) (GitHubRepositorySnapshot, error) {
	repository, err := parseGitHubRepositoryReference(reference)
	if err != nil {
		return GitHubRepositorySnapshot{}, err
	}
	if strings.TrimSpace(token) == "" {
		return GitHubRepositorySnapshot{}, fmt.Errorf("GitHub installation token is required")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	baseURL := strings.TrimRight(config.APIBaseURL, "/") + "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name)
	request, err := githubAppRequest(ctx, http.MethodGet, baseURL, token, nil)
	if err != nil {
		return GitHubRepositorySnapshot{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return GitHubRepositorySnapshot{}, fmt.Errorf("read GitHub repository metadata")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return GitHubRepositorySnapshot{}, fmt.Errorf("GitHub repository metadata was rejected (%d)", response.StatusCode)
	}
	var metadata struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil || strings.TrimSpace(metadata.DefaultBranch) == "" {
		return GitHubRepositorySnapshot{}, fmt.Errorf("GitHub repository metadata is invalid")
	}
	commitURL := baseURL + "/commits/" + url.PathEscape(metadata.DefaultBranch)
	request, err = githubAppRequest(ctx, http.MethodGet, commitURL, token, nil)
	if err != nil {
		return GitHubRepositorySnapshot{}, err
	}
	response, err = client.Do(request)
	if err != nil {
		return GitHubRepositorySnapshot{}, fmt.Errorf("read GitHub default branch")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return GitHubRepositorySnapshot{}, fmt.Errorf("GitHub default branch was rejected (%d)", response.StatusCode)
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(response.Body).Decode(&commit); err != nil || !validGitHubCommitSHA(commit.SHA) {
		return GitHubRepositorySnapshot{}, fmt.Errorf("GitHub default branch revision is invalid")
	}
	return GitHubRepositorySnapshot{Reference: "github://" + repository.Owner + "/" + repository.Name, Revision: strings.ToLower(commit.SHA), DefaultBranch: strings.TrimSpace(metadata.DefaultBranch)}, nil
}

// ReadGitHubRepositoryMap reads the Git tree for one already-frozen GitHub
// revision. It accepts neither arbitrary URLs nor paths, excludes secret-like
// and generated paths using the same policy as local workspace context, and
// never reads file bodies. Source changes still require a registered local
// workspace checkpoint.
func ReadGitHubRepositoryMap(ctx context.Context, config GitHubAppConfig, token string, snapshot GitHubRepositorySnapshot) (GitHubRepositoryMap, error) {
	repository, err := parseGitHubRepositoryReference(snapshot.Reference)
	if err != nil {
		return GitHubRepositoryMap{}, err
	}
	revision := strings.ToLower(strings.TrimSpace(snapshot.Revision))
	if !validGitHubCommitSHA(revision) || strings.TrimSpace(token) == "" {
		return GitHubRepositoryMap{}, fmt.Errorf("GitHub repository map requires a frozen revision and installation token")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	baseURL := strings.TrimRight(config.APIBaseURL, "/") + "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name)
	request, err := githubAppRequest(ctx, http.MethodGet, baseURL+"/git/trees/"+url.PathEscape(revision)+"?recursive=1", token, nil)
	if err != nil {
		return GitHubRepositoryMap{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return GitHubRepositoryMap{}, fmt.Errorf("read GitHub repository tree")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return GitHubRepositoryMap{}, fmt.Errorf("GitHub repository tree was rejected (%d)", response.StatusCode)
	}
	var payload struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		return GitHubRepositoryMap{}, fmt.Errorf("GitHub repository tree is invalid")
	}
	files := make([]string, 0, min(len(payload.Tree), maxGitHubRepositoryMapFiles))
	fileCount := 0
	inventoryBytes := 0
	for _, entry := range payload.Tree {
		if entry.Type != "blob" {
			continue
		}
		path := strings.Trim(strings.TrimSpace(entry.Path), "/")
		if path == "" || len(path) > 240 || strings.Contains(path, "../") || !safeGitHubRepositoryMapPath(path) {
			continue
		}
		fileCount++
		if len(files) < maxGitHubRepositoryMapFiles && inventoryBytes+len(path)+3 <= maxGitHubRepositoryMapBytes {
			files = append(files, path)
			inventoryBytes += len(path) + 3
		}
	}
	sort.Strings(files)
	return GitHubRepositoryMap{Revision: revision, FileCount: fileCount, InventoryTruncated: payload.Truncated || fileCount > len(files), Files: files}, nil
}

// ReadGitHubRepositorySourceContext reads a small set of high-signal files
// from the exact remote revision after the repository tree has already been
// validated. It accepts no caller-selected paths, skips unsafe/binary/large
// content, and redacts values with the same policy used for local workspaces.
// A missing Contents permission is non-fatal to the caller: it can retain the
// safer inventory-only checkpoint instead.
func ReadGitHubRepositorySourceContext(ctx context.Context, config GitHubAppConfig, token string, snapshot GitHubRepositorySnapshot, inventory GitHubRepositoryMap) (GitHubRepositorySourceContext, error) {
	repository, err := parseGitHubRepositoryReference(snapshot.Reference)
	if err != nil {
		return GitHubRepositorySourceContext{}, err
	}
	revision := strings.ToLower(strings.TrimSpace(snapshot.Revision))
	if !validGitHubCommitSHA(revision) || strings.TrimSpace(token) == "" || !strings.EqualFold(revision, strings.TrimSpace(inventory.Revision)) {
		return GitHubRepositorySourceContext{}, fmt.Errorf("GitHub repository source context requires the matching frozen revision and installation token")
	}
	paths := selectedGitHubRepositoryContextPaths(inventory.Files)
	if len(paths) == 0 {
		return GitHubRepositorySourceContext{Revision: revision}, nil
	}
	client := &http.Client{Timeout: 20 * time.Second}
	baseURL := strings.TrimRight(config.APIBaseURL, "/") + "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name) + "/contents/"
	remaining := maxGitHubRepositoryContextBytes
	result := GitHubRepositorySourceContext{Revision: revision, Excerpts: make([]GitHubRepositoryExcerpt, 0, len(paths))}
	for _, sourcePath := range paths {
		if remaining <= 0 {
			result.ContextTruncated = true
			break
		}
		request, requestErr := githubAppRequest(ctx, http.MethodGet, baseURL+escapeGitHubRepositoryContentPath(sourcePath)+"?ref="+url.QueryEscape(revision), token, nil)
		if requestErr != nil {
			return GitHubRepositorySourceContext{}, requestErr
		}
		response, requestErr := client.Do(request)
		if requestErr != nil {
			return GitHubRepositorySourceContext{}, fmt.Errorf("read GitHub repository source")
		}
		var payload struct {
			Type     string `json:"type"`
			Encoding string `json:"encoding"`
			Content  string `json:"content"`
			Size     int    `json:"size"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload)
		status := response.StatusCode
		response.Body.Close()
		if status != http.StatusOK || decodeErr != nil || payload.Type != "file" || !strings.EqualFold(payload.Encoding, "base64") || payload.Size < 0 || payload.Size > maxGitHubRepositoryExcerptBytes {
			continue
		}
		decoded, decodeErr := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
		if decodeErr != nil || len(decoded) == 0 || len(decoded) > maxGitHubRepositoryExcerptBytes || !validText(decoded) {
			continue
		}
		if len(decoded) > remaining {
			decoded = decoded[:remaining]
			result.ContextTruncated = true
		}
		sanitized, redactions := redactWorkspaceExcerpt(string(decoded))
		result.RedactedValues += redactions
		result.Excerpts = append(result.Excerpts, GitHubRepositoryExcerpt{Path: sourcePath, Content: sanitized})
		remaining -= len(decoded)
	}
	return result, nil
}

func selectedGitHubRepositoryContextPaths(files []string) []string {
	candidates := make([]string, 0, len(files))
	for _, file := range files {
		file = strings.Trim(strings.TrimSpace(file), "/")
		if file == "" || !safeGitHubRepositoryMapPath(file) || githubRepositoryContextPriority(file) > 2 {
			continue
		}
		candidates = append(candidates, file)
	}
	sort.Slice(candidates, func(left, right int) bool {
		leftPriority, rightPriority := githubRepositoryContextPriority(candidates[left]), githubRepositoryContextPriority(candidates[right])
		if leftPriority == rightPriority {
			return candidates[left] < candidates[right]
		}
		return leftPriority < rightPriority
	})
	if len(candidates) > maxGitHubRepositoryExcerpts {
		candidates = candidates[:maxGitHubRepositoryExcerpts]
	}
	return candidates
}

func githubRepositoryContextPriority(value string) int {
	lower := strings.ToLower(strings.TrimSpace(value))
	base := lower[strings.LastIndex(lower, "/")+1:]
	if base == "readme.md" || base == "architecture.md" || base == "code_index.md" {
		return 0
	}
	if base == "go.mod" || base == "cargo.toml" || base == "package.json" || base == "serverless.yml" || base == "serverless.yaml" ||
		strings.HasSuffix(lower, "/main.go") || strings.HasSuffix(lower, "/main.rs") || strings.HasSuffix(lower, "/main.ts") || strings.HasSuffix(lower, "/main.tsx") || strings.HasSuffix(lower, "/main.js") || strings.HasSuffix(lower, "/index.ts") || strings.HasSuffix(lower, "/index.tsx") || strings.HasSuffix(lower, "/handler.ts") || strings.HasSuffix(lower, "/handler.js") {
		return 1
	}
	if strings.HasPrefix(lower, "docs/") || strings.Contains(lower, "/docs/") || strings.Contains(lower, "/config/") {
		return 2
	}
	return 3
}

// escapeGitHubRepositoryContentPath leaves the already-validated directory
// separators intact while escaping every segment. The Contents endpoint uses
// path segments, so escaping the complete path would turn a nested entrypoint
// into one opaque name on some GitHub-compatible API gateways.
func escapeGitHubRepositoryContentPath(value string) string {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func safeGitHubRepositoryMapPath(value string) bool {
	if !safeContextFile(value) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if excludedDirectory(segment) {
			return false
		}
	}
	return true
}

func parseGitHubRepositoryReference(value string) (githubRepository, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || !strings.EqualFold(parsed.Scheme, "github") || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return githubRepository{}, fmt.Errorf("repository reference must use github://owner/repository")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 1 || parts[0] == "" || strings.ContainsAny(parsed.Host, " /\\") || strings.ContainsAny(parts[0], " /\\") {
		return githubRepository{}, fmt.Errorf("repository reference must use github://owner/repository")
	}
	return githubRepository{Owner: parsed.Host, Name: parts[0]}, nil
}

func validGitHubCommitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if !isGitHubHex(character) {
			return false
		}
	}
	return true
}

func isGitHubHex(character rune) bool {
	return (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')
}

func githubAppRequest(ctx context.Context, method, endpoint, token string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("create GitHub API request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return request, nil
}

// createGitHubPullRequest uses the installation token only in an Authorization
// header. It deliberately never auto-merges or updates repository settings.
// A 422 response is handled by locating the already-open branch PR, making a
// safe retry idempotent after a callback or network interruption.
func createGitHubPullRequest(ctx context.Context, config GitHubAppConfig, token string, repository githubRepository, branch, title string) (string, bool, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	baseURL := strings.TrimRight(config.APIBaseURL, "/") + "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return "", false, fmt.Errorf("create GitHub repository request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := client.Do(request)
	if err != nil {
		return "", false, fmt.Errorf("read GitHub repository metadata")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("GitHub repository metadata was rejected (%d)", response.StatusCode)
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(response.Body).Decode(&repo); err != nil || strings.TrimSpace(repo.DefaultBranch) == "" {
		return "", false, fmt.Errorf("GitHub repository metadata is invalid")
	}
	payload, _ := json.Marshal(map[string]string{"title": safeCommitTitle(title), "head": branch, "base": repo.DefaultBranch, "body": "Created by ITBEM Delivery after human code-review approval. It requires the normal human pull-request review and merge process."})
	request, err = http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/pulls", strings.NewReader(string(payload)))
	if err != nil {
		return "", false, fmt.Errorf("create GitHub pull request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("Content-Type", "application/json")
	response, err = client.Do(request)
	if err != nil {
		return "", false, fmt.Errorf("create GitHub pull request")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusCreated {
		var pr struct {
			HTMLURL string `json:"html_url"`
		}
		if err := json.NewDecoder(response.Body).Decode(&pr); err != nil || !validGitHubPullURL(pr.HTMLURL, repository) {
			return "", false, fmt.Errorf("GitHub pull request response is invalid")
		}
		return pr.HTMLURL, true, nil
	}
	if response.StatusCode != http.StatusUnprocessableEntity {
		return "", false, fmt.Errorf("GitHub pull request was rejected (%d)", response.StatusCode)
	}
	// The only supported recovery from 422 is a previously created open PR for
	// this exact branch. Do not guess a PR from arbitrary error text.
	listURL := baseURL + "/pulls?state=open&head=" + url.QueryEscape(repository.Owner+":"+branch)
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return "", false, fmt.Errorf("read existing GitHub pull request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err = client.Do(request)
	if err != nil {
		return "", false, fmt.Errorf("read existing GitHub pull request")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("GitHub pull request lookup was rejected (%d)", response.StatusCode)
	}
	var pulls []struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(response.Body).Decode(&pulls); err != nil || len(pulls) != 1 || !validGitHubPullURL(pulls[0].HTMLURL, repository) {
		return "", false, fmt.Errorf("no unique existing pull request was found for the approved branch")
	}
	return pulls[0].HTMLURL, false, nil
}

func validGitHubPullURL(value string, repository githubRepository) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return false
	}
	prefix := "/" + repository.Owner + "/" + repository.Name + "/pull/"
	return strings.HasPrefix(parsed.Path, prefix) && strings.TrimPrefix(parsed.Path, prefix) != ""
}

type GitHubInstallationToken struct {
	Token     string
	ExpiresAt time.Time
}

// VerifyGitHubAppInstallation proves that a syntactically valid App
// configuration can mint an installation token and use it for one bounded,
// read-only GitHub endpoint. The token is retained only in this stack frame;
// callers receive no credential, repository listing, or account metadata.
func VerifyGitHubAppInstallation(ctx context.Context, config GitHubAppConfig, client *http.Client, now time.Time) error {
	installation, err := MintGitHubInstallationToken(ctx, config, client, now)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	endpoint := strings.TrimRight(config.APIBaseURL, "/") + "/installation/repositories?per_page=1"
	request, err := githubAppRequest(ctx, http.MethodGet, endpoint, installation.Token, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("verify GitHub App installation access")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub App installation repository access was rejected (%d)", response.StatusCode)
	}
	// Decode only enough to reject an invalid API response. The repository
	// names intentionally stay outside logs, responses and task context.
	var payload struct {
		TotalCount *int `json:"total_count"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&payload); err != nil || payload.TotalCount == nil || *payload.TotalCount < 0 {
		return fmt.Errorf("GitHub App installation verification response is invalid")
	}
	return nil
}

// LoadGitHubAppConfig reads only process-owned environment values. In
// deployment these should come from a secrets manager; local development may
// use .env.ai.local, which is explicitly excluded from agent context.
func LoadGitHubAppConfig(lookup func(string) string) (GitHubAppConfig, error) {
	appID := strings.TrimSpace(lookup("ITBEM_GITHUB_APP_ID"))
	installationIDs, err := parseGitHubInstallationIDs(lookup("ITBEM_GITHUB_INSTALLATION_IDS"))
	if err != nil {
		return GitHubAppConfig{}, err
	}
	if len(installationIDs) == 0 {
		installationIDs, err = parseGitHubInstallationIDs(lookup("ITBEM_GITHUB_INSTALLATION_ID"))
		if err != nil {
			return GitHubAppConfig{}, err
		}
	}
	privatePEM := strings.TrimSpace(lookup("ITBEM_GITHUB_APP_PRIVATE_KEY"))
	if privatePEM == "" {
		privateKeyFile := strings.TrimSpace(lookup("ITBEM_GITHUB_APP_PRIVATE_KEY_FILE"))
		if privateKeyFile != "" {
			contents, readErr := os.ReadFile(privateKeyFile)
			if readErr != nil {
				return GitHubAppConfig{}, fmt.Errorf("ITBEM_GITHUB_APP_PRIVATE_KEY_FILE is unreadable")
			}
			privatePEM = strings.TrimSpace(string(contents))
		}
	}
	if appID == "" || len(installationIDs) == 0 || privatePEM == "" {
		return GitHubAppConfig{}, ErrGitHubAppNotConfigured
	}
	if _, err := strconv.ParseInt(appID, 10, 64); err != nil {
		return GitHubAppConfig{}, fmt.Errorf("ITBEM_GITHUB_APP_ID is invalid")
	}
	privateKey, err := parseGitHubAppPrivateKey(privatePEM)
	if err != nil {
		return GitHubAppConfig{}, err
	}
	baseURL, err := normalizeGitHubAPIBaseURL(lookup("ITBEM_GITHUB_API_BASE_URL"))
	if err != nil {
		return GitHubAppConfig{}, err
	}
	return GitHubAppConfig{AppID: appID, InstallationID: installationIDs[0], InstallationIDs: installationIDs, PrivateKey: privateKey, APIBaseURL: baseURL}, nil
}

func parseGitHubInstallationIDs(value string) ([]string, error) {
	seen := map[string]struct{}{}
	result := make([]string, 0, 3)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, err := strconv.ParseInt(item, 10, 64); err != nil {
			return nil, fmt.Errorf("ITBEM_GITHUB_INSTALLATION_ID is invalid")
		}
		if _, exists := seen[item]; !exists {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result, nil
}

// AllowsInstallationID keeps webhook admission bound to a configured GitHub
// App installation rather than accepting every organization that installs it.
func (config GitHubAppConfig) AllowsInstallationID(id int64) bool {
	if id < 1 {
		return false
	}
	value := strconv.FormatInt(id, 10)
	if len(config.InstallationIDs) == 0 {
		return subtle.ConstantTimeCompare([]byte(value), []byte(config.InstallationID)) == 1
	}
	matched := 0
	for _, allowed := range config.InstallationIDs {
		matched |= subtle.ConstantTimeCompare([]byte(value), []byte(allowed))
	}
	return matched == 1
}

// WithInstallationID returns a token-minting configuration only for a member
// of the configured allow-list. It never broadens the set of accepted events.
func (config GitHubAppConfig) WithInstallationID(id int64) (GitHubAppConfig, error) {
	if !config.AllowsInstallationID(id) {
		return GitHubAppConfig{}, fmt.Errorf("GitHub App installation is not allowed")
	}
	config.InstallationID = strconv.FormatInt(id, 10)
	return config, nil
}

func parseGitHubAppPrivateKey(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.ReplaceAll(value, `\n`, "\n")))
	if block == nil {
		return nil, fmt.Errorf("ITBEM_GITHUB_APP_PRIVATE_KEY is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ITBEM_GITHUB_APP_PRIVATE_KEY is invalid")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("ITBEM_GITHUB_APP_PRIVATE_KEY must be an RSA key")
	}
	return key, nil
}

func normalizeGitHubAPIBaseURL(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "https://api.github.com", nil
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("ITBEM_GITHUB_API_BASE_URL is invalid")
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isGitHubAPILoopbackHost(parsed.Hostname())) {
		return "", fmt.Errorf("ITBEM_GITHUB_API_BASE_URL must use HTTPS")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func isGitHubAPILoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// MintGitHubInstallationToken mints a new short-lived installation token.
// The returned token is intentionally ephemeral and callers must keep it out
// of result payloads, logs, evidence and storage.
func MintGitHubInstallationToken(ctx context.Context, config GitHubAppConfig, client *http.Client, now time.Time) (GitHubInstallationToken, error) {
	return mintGitHubInstallationToken(ctx, config, client, now, true)
}

func mintGitHubInstallationToken(ctx context.Context, config GitHubAppConfig, client *http.Client, now time.Time, allowClockCorrection bool) (GitHubInstallationToken, error) {
	if config.PrivateKey == nil || strings.TrimSpace(config.AppID) == "" || strings.TrimSpace(config.InstallationID) == "" || strings.TrimSpace(config.APIBaseURL) == "" {
		return GitHubInstallationToken{}, ErrGitHubAppNotConfigured
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	issuedAt := now.UTC().Add(-60 * time.Second)
	claims := jwt.RegisteredClaims{
		Issuer:    config.AppID,
		IssuedAt:  jwt.NewNumericDate(issuedAt),
		ExpiresAt: jwt.NewNumericDate(issuedAt.Add(9 * time.Minute)),
	}
	assertion, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(config.PrivateKey)
	if err != nil {
		return GitHubInstallationToken{}, fmt.Errorf("sign GitHub App assertion: %w", err)
	}
	endpoint := strings.TrimRight(config.APIBaseURL, "/") + "/app/installations/" + url.PathEscape(config.InstallationID) + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return GitHubInstallationToken{}, fmt.Errorf("create GitHub App token request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+assertion)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := client.Do(req)
	if err != nil {
		return GitHubInstallationToken{}, fmt.Errorf("request GitHub App installation token: %w", err)
	}
	if response.StatusCode == http.StatusUnauthorized && allowClockCorrection {
		serverTime, parseErr := http.ParseTime(response.Header.Get("Date"))
		response.Body.Close()
		if parseErr == nil && serverTime.Sub(now.UTC()).Abs() > 2*time.Minute {
			return mintGitHubInstallationToken(ctx, config, client, serverTime, false)
		}
		return GitHubInstallationToken{}, fmt.Errorf("GitHub App installation token request was rejected (%d)", response.StatusCode)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return GitHubInstallationToken{}, fmt.Errorf("GitHub App installation token request was rejected (%d)", response.StatusCode)
	}
	var payload struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return GitHubInstallationToken{}, fmt.Errorf("decode GitHub App installation token response: %w", err)
	}
	if strings.TrimSpace(payload.Token) == "" || payload.ExpiresAt.IsZero() || !payload.ExpiresAt.After(now.UTC().Add(time.Minute)) {
		return GitHubInstallationToken{}, fmt.Errorf("GitHub App installation token response is invalid")
	}
	return GitHubInstallationToken{Token: payload.Token, ExpiresAt: payload.ExpiresAt.UTC()}, nil
}
