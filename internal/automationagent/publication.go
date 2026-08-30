package automationagent

// The publication adapter intentionally contains no model call. A human has
// already approved code review, and the worker only carries out the exact
// scoped grant using an ephemeral GitHub App installation token.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var sha256DigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var githubRepositoryPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_.-]*$`)

type publicationInput struct {
	ContextSources []struct {
		Kind      string         `json:"kind"`
		Reference string         `json:"reference"`
		Metadata  map[string]any `json:"metadata"`
	} `json:"context_sources"`
	WorkItem struct {
		Title string `json:"title"`
	} `json:"work_item"`
	Publication *publicationAuthorization `json:"publication"`
}

type publicationAuthorization struct {
	GrantID          string   `json:"grant_id"`
	RepositoryRef    string   `json:"repository_ref"`
	BaseSHA          string   `json:"base_sha"`
	GitHubRepository string   `json:"github_repository"`
	ReviewDiffSHA256 string   `json:"review_diff_sha256"`
	Branch           string   `json:"branch"`
	Capabilities     []string `json:"capabilities"`
	ExpiresAt        string   `json:"expires_at"`
}

type githubRepository struct {
	Owner string
	Name  string
}

func publicationRepositoryMatches(remote githubRepository, expected string) bool {
	return strings.EqualFold(remote.Owner+"/"+remote.Name, strings.TrimSpace(expected))
}

// validatePublicationRemoteBase proves that the grant's immutable review base
// is still the repository default branch checkpoint. It is intentionally
// strict: rebasing, merging or resolving a stale base are new code changes
// and therefore require a new reviewed diff and a new human grant.
func validatePublicationRemoteBase(checkpoint GitHubRepositorySnapshot, remote githubRepository, auth *publicationAuthorization) error {
	if auth == nil || !publicationRepositoryMatches(remote, auth.GitHubRepository) {
		return fmt.Errorf("reviewed worktree origin no longer matches the human publication grant")
	}
	expectedReference := "github://" + remote.Owner + "/" + remote.Name
	if !strings.EqualFold(strings.TrimSpace(checkpoint.Reference), expectedReference) {
		return fmt.Errorf("GitHub default branch checkpoint does not match the reviewed repository")
	}
	if !gitCommitPattern.MatchString(strings.ToLower(strings.TrimSpace(checkpoint.Revision))) {
		return fmt.Errorf("GitHub default branch checkpoint is invalid")
	}
	if !strings.EqualFold(strings.TrimSpace(checkpoint.Revision), strings.TrimSpace(auth.BaseSHA)) {
		return fmt.Errorf("default branch changed after human code review; refresh, rebase and review the worktree again before publication")
	}
	return nil
}

// RunPublication commits and publishes only the exact locally reviewed
// worktree authorized by the control plane. It never merges, deploys, reads a
// personal Git credential or converts a review grant into broader access.
func RunPublication(ctx context.Context, delivery json.RawMessage, lookup func(string) string) (map[string]any, error) {
	var input publicationInput
	if err := json.Unmarshal(delivery, &input); err != nil || input.Publication == nil {
		return nil, fmt.Errorf("publication input must contain a human authorization")
	}
	auth := input.Publication
	if err := validatePublicationAuthorization(auth); err != nil {
		return nil, err
	}
	workspace, err := publicationWorkspace(input.ContextSources, auth.RepositoryRef, lookup)
	if err != nil {
		return nil, err
	}
	for _, capability := range []string{WorkspaceCapabilityStageCommit, WorkspaceCapabilityPublishBranch} {
		if err := workspace.RequireCapability(capability); err != nil {
			return nil, err
		}
	}
	config, err := LoadGitHubAppConfig(lookup)
	if err != nil {
		return nil, err
	}
	// Authentication is checked before the local worktree is mutated. A missing
	// or invalid app therefore fails closed without producing a stray commit.
	token, err := MintGitHubRepositoryToken(ctx, config, nil, time.Now(), auth.GitHubRepository)
	if err != nil {
		return nil, err
	}
	worktree, err := reviewedWorktree(workspace, auth.Branch)
	if err != nil {
		return nil, err
	}
	if err := verifyReviewedWorktree(ctx, worktree, auth); err != nil {
		return nil, err
	}
	remote, err := gitHubOrigin(ctx, worktree)
	if err != nil {
		return nil, err
	}
	if !publicationRepositoryMatches(remote, auth.GitHubRepository) {
		return nil, fmt.Errorf("reviewed worktree origin no longer matches the human publication grant")
	}
	// The human reviewed a diff against auth.BaseSHA. Publishing it after the
	// default branch has moved would create a PR from stale assumptions and can
	// invalidate the QA plan. This read-only check happens before staging or
	// committing, so a stale base never mutates the reviewed worktree.
	checkpoint, err := ReadGitHubRepositorySnapshot(ctx, config, token.Token, "github://"+remote.Owner+"/"+remote.Name)
	if err != nil {
		return nil, fmt.Errorf("could not verify the reviewed default branch before publication: %w", err)
	}
	if err := validatePublicationRemoteBase(checkpoint, remote, auth); err != nil {
		return nil, err
	}
	commitSHA, committed, err := stageAndCommitPublication(ctx, worktree, auth, input.WorkItem.Title)
	if err != nil {
		return nil, err
	}
	if err := pushGitHubBranch(ctx, worktree, remote, auth.Branch, token.Token); err != nil {
		return nil, err
	}
	result := map[string]any{
		"grant_id": auth.GrantID, "workspace": "workspace://" + workspace.ID, "worktree": "workspace://" + workspace.ID + "#" + auth.Branch,
		"repository_ref": auth.RepositoryRef, "branch": auth.Branch, "base_sha": strings.ToLower(auth.BaseSHA), "commit_sha": commitSHA,
		"remote_repository": remote.Owner + "/" + remote.Name, "branch_published": true, "commit_created": committed,
		"deployment": "not attempted; a human preview/release workflow remains required",
	}
	if hasPublicationCapability(auth.Capabilities, WorkspaceCapabilityCreatePullReq) {
		if err := workspace.RequireCapability(WorkspaceCapabilityCreatePullReq); err != nil {
			return nil, err
		}
		prURL, created, err := createGitHubPullRequest(ctx, config, token.Token, remote, auth.Branch, input.WorkItem.Title)
		if err != nil {
			return nil, err
		}
		result["pull_request_url"], result["pull_request_created"] = prURL, created
	}
	return result, nil
}

func validatePublicationAuthorization(auth *publicationAuthorization) error {
	if auth == nil || !taskIDPattern.MatchString(strings.ToLower(strings.TrimSpace(auth.GrantID))) || !gitCommitPattern.MatchString(strings.ToLower(strings.TrimSpace(auth.BaseSHA))) || !githubRepositoryPattern.MatchString(strings.ToLower(strings.TrimSpace(auth.GitHubRepository))) || !sha256DigestPattern.MatchString(strings.ToLower(strings.TrimSpace(auth.ReviewDiffSHA256))) || !agentPublicationBranch(auth.Branch) || !strings.HasPrefix(strings.TrimSpace(auth.RepositoryRef), "workspace://") {
		return fmt.Errorf("publication authorization is invalid")
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(auth.ExpiresAt))
	if err != nil || !expiresAt.After(time.Now().UTC()) {
		return fmt.Errorf("publication authorization is expired")
	}
	if !hasPublicationCapability(auth.Capabilities, WorkspaceCapabilityStageCommit) || !hasPublicationCapability(auth.Capabilities, WorkspaceCapabilityPublishBranch) {
		return fmt.Errorf("publication authorization does not permit commit and branch publication")
	}
	for _, capability := range auth.Capabilities {
		if capability != WorkspaceCapabilityStageCommit && capability != WorkspaceCapabilityPublishBranch && capability != WorkspaceCapabilityCreatePullReq {
			return fmt.Errorf("publication authorization contains an unsupported capability")
		}
	}
	return nil
}

func hasPublicationCapability(capabilities []string, expected string) bool {
	for _, capability := range capabilities {
		if strings.TrimSpace(capability) == expected {
			return true
		}
	}
	return false
}

func agentPublicationBranch(branch string) bool {
	parts := strings.Split(strings.TrimSpace(branch), "/")
	return len(parts) == 2 && parts[0] == "itbem-agent" && taskIDPattern.MatchString(strings.ToLower(parts[1]))
}

func publicationWorkspace(sources []struct {
	Kind      string         `json:"kind"`
	Reference string         `json:"reference"`
	Metadata  map[string]any `json:"metadata"`
}, reference string, lookup func(string) string) (Workspace, error) {
	for _, source := range sources {
		if source.Kind == "repository" && strings.TrimSpace(source.Reference) == reference {
			return RegisteredWorkspace(reference, lookup)
		}
	}
	return Workspace{}, fmt.Errorf("publication repository is not a frozen delivery context source")
}

func reviewedWorktree(workspace Workspace, branch string) (string, error) {
	parts := strings.Split(branch, "/")
	if !agentPublicationBranch(branch) || len(parts) != 2 {
		return "", fmt.Errorf("publication branch is invalid")
	}
	path := filepath.Join(workspace.Root, ".itbem-agent-worktrees", parts[1])
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("reviewed isolated worktree is not available locally")
	}
	return path, nil
}

func verifyReviewedWorktree(ctx context.Context, worktree string, auth *publicationAuthorization) error {
	branch, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "branch", "--show-current")
	if err != nil || branch.ExitCode != 0 || strings.TrimSpace(branch.Output) != auth.Branch {
		return fmt.Errorf("isolated worktree branch no longer matches its reviewed authorization")
	}
	base, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "merge-base", "--is-ancestor", strings.ToLower(auth.BaseSHA), "HEAD")
	if err != nil || base.ExitCode != 0 {
		return fmt.Errorf("reviewed base revision is not an ancestor of the publication worktree")
	}
	digest, err := worktreeDiffSHA256(ctx, worktree, auth.BaseSHA, false)
	if err != nil || digest != strings.ToLower(strings.TrimSpace(auth.ReviewDiffSHA256)) {
		return fmt.Errorf("reviewed worktree changed after human code review")
	}
	return nil
}

func gitHubOrigin(ctx context.Context, worktree string) (githubRepository, error) {
	result, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "remote", "get-url", "origin")
	if err != nil || result.ExitCode != 0 {
		return githubRepository{}, fmt.Errorf("reviewed worktree does not have a readable origin remote")
	}
	return parseGitHubRemote(result.Output)
}

func parseGitHubRemote(value string) (githubRepository, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "git@github.com:") {
		value = "https://github.com/" + strings.TrimPrefix(value, "git@github.com:")
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil {
		return githubRepository{}, fmt.Errorf("origin must be a credential-free github.com repository")
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return githubRepository{}, fmt.Errorf("origin must identify one GitHub owner and repository")
	}
	return githubRepository{Owner: parts[0], Name: parts[1]}, nil
}

func stageAndCommitPublication(ctx context.Context, worktree string, auth *publicationAuthorization, title string) (string, bool, error) {
	// Do not adopt an existing index. A reviewer approved the worktree diff,
	// not arbitrary previously-staged files.
	preStaged, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "diff", "--cached", "--quiet")
	if err != nil || (preStaged.ExitCode != 0 && preStaged.ExitCode != 1) {
		return "", false, fmt.Errorf("could not inspect reviewed worktree index")
	}
	if preStaged.ExitCode != 0 {
		return "", false, fmt.Errorf("reviewed worktree has unexpected staged changes")
	}
	status, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "status", "--porcelain")
	if err != nil || status.ExitCode != 0 {
		return "", false, fmt.Errorf("could not inspect reviewed worktree changes")
	}
	if strings.TrimSpace(status.Output) != "" {
		staged, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "add", "--all")
		if err != nil || staged.ExitCode != 0 {
			return "", false, fmt.Errorf("could not stage reviewed worktree changes")
		}
		stagedDigest, digestErr := worktreeDiffSHA256(ctx, worktree, auth.BaseSHA, true)
		if digestErr != nil || stagedDigest != strings.ToLower(strings.TrimSpace(auth.ReviewDiffSHA256)) {
			_, _ = runLocal(ctx, worktree, 30*time.Second, "", "git", "reset")
			return "", false, fmt.Errorf("staged worktree no longer matches the human-reviewed diff")
		}
		files, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "diff", "--cached", "--name-only")
		if err != nil || files.ExitCode != 0 || !safePublicationFiles(files.Output) {
			_, _ = runLocal(ctx, worktree, 30*time.Second, "", "git", "reset")
			return "", false, fmt.Errorf("reviewed worktree contains an unsafe file for publication")
		}
		message := "ITBEM delivery: " + safeCommitTitle(title)
		// Repository hooks execute arbitrary code. The worker has already run
		// the declared validation suite and must not execute an unreviewed
		// pre-commit/commit-msg hook while publishing an approved diff.
		committed, err := runLocal(ctx, worktree, 60*time.Second, "", "git", "-c", "user.name=ITBEM Delivery Agent", "-c", "user.email=delivery-agent@itbem.invalid", "commit", "--no-verify", "-m", message)
		if err != nil || committed.ExitCode != 0 {
			return "", false, fmt.Errorf("could not commit reviewed worktree changes")
		}
	}
	head, err := runLocal(ctx, worktree, 30*time.Second, "", "git", "rev-parse", "HEAD")
	if err != nil || head.ExitCode != 0 || !gitCommitPattern.MatchString(strings.ToLower(strings.TrimSpace(head.Output))) {
		return "", false, fmt.Errorf("could not identify publication commit")
	}
	return strings.ToLower(strings.TrimSpace(head.Output)), strings.TrimSpace(status.Output) != "", nil
}

func safePublicationFiles(output string) bool {
	for _, path := range strings.Split(strings.TrimSpace(output), "\n") {
		path = strings.TrimSpace(filepath.ToSlash(path))
		if path == "" || strings.HasPrefix(path, ".itbem-agent-") || strings.HasPrefix(path, ".contracts/") || unsafePatchPath("a/"+path) {
			return false
		}
	}
	return true
}

func safeCommitTitle(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "approved implementation"
	}
	if len(value) > 120 {
		return value[:120]
	}
	return value
}

func pushGitHubBranch(ctx context.Context, worktree string, repository githubRepository, branch, token string) error {
	askpass, cleanup, err := gitAskPass(token)
	if err != nil {
		return err
	}
	defer cleanup()
	remote := "https://github.com/" + repository.Owner + "/" + repository.Name + ".git"
	// Do not let Git Credential Manager, global Git config, SSH, or a pre-push
	// hook substitute a developer identity for the ephemeral GitHub App token.
	// The only credential reaching this child is the short-lived token consumed
	// by our temporary askpass helper.
	environment := map[string]string{
		"GIT_TERMINAL_PROMPT":             "0",
		"GIT_ASKPASS":                     askpass,
		"GIT_ASKPASS_REQUIRE":             "force",
		"GIT_CONFIG_NOSYSTEM":             "1",
		"GIT_CONFIG_GLOBAL":               os.DevNull,
		"ITBEM_GITHUB_INSTALLATION_TOKEN": token,
	}
	result, err := runLocalWithEnv(ctx, worktree, 2*time.Minute, "", environment, "git", publicationPushArguments(remote, branch)...)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("could not publish the approved branch")
	}
	return nil
}

func publicationPushArguments(remote, branch string) []string {
	return []string{"-c", "credential.helper=", "push", "--no-verify", remote, "HEAD:refs/heads/" + branch}
}

func gitAskPass(_ string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "itbem-git-askpass-")
	if err != nil {
		return "", nil, fmt.Errorf("prepare GitHub authentication helper")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "askpass.cmd")
		body := "@echo off\r\necho %~1 | findstr /I /C:\"Username\" >nul && (echo x-access-token) || (echo %ITBEM_GITHUB_INSTALLATION_TOKEN%)\r\n"
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("prepare GitHub authentication helper")
		}
		return path, cleanup, nil
	}
	path := filepath.Join(dir, "askpass.sh")
	body := "#!/bin/sh\ncase \"$1\" in *Username*) echo x-access-token ;; *) printf '%s\\n' \"$ITBEM_GITHUB_INSTALLATION_TOKEN\" ;; esac\n"
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("prepare GitHub authentication helper")
	}
	return path, cleanup, nil
}
