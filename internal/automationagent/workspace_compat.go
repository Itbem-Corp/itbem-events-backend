package automationagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const managedWorkspaceRuntimeExclude = ".itbem-agent-worktrees/"

func ensureManagedWorkspaceRuntimeExclude(ctx context.Context, root string) error {
	resolved, err := runLocal(ctx, root, 20*time.Second, "", "git", "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil || resolved.ExitCode != 0 {
		return fmt.Errorf("resolve managed workspace runtime exclude")
	}
	path := filepath.Clean(strings.TrimSpace(resolved.Output))
	if path == "." || !filepath.IsAbs(path) {
		return fmt.Errorf("managed workspace runtime exclude path is invalid")
	}
	common, commonErr := runLocal(ctx, root, 20*time.Second, "", "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if commonErr != nil || common.ExitCode != 0 {
		return fmt.Errorf("resolve managed workspace git directory")
	}
	gitDirectory := filepath.Clean(strings.TrimSpace(common.Output))
	relative, relativeErr := filepath.Rel(gitDirectory, path)
	if relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("managed workspace runtime exclude is outside its Git directory")
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("managed workspace runtime exclude is not a regular file")
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect managed workspace runtime exclude")
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read managed workspace runtime exclude")
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSpace(line) == managedWorkspaceRuntimeExclude {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("prepare managed workspace runtime exclude")
	}
	entry := string(contents)
	if entry != "" && !strings.HasSuffix(entry, "\n") {
		entry += "\n"
	}
	entry += managedWorkspaceRuntimeExclude + "\n"
	if err := os.WriteFile(path, []byte(entry), 0600); err != nil {
		return fmt.Errorf("write managed workspace runtime exclude")
	}
	return nil
}

func gitWorkspaceEnvironment(environment map[string]string) map[string]string {
	if len(environment) == 0 {
		return map[string]string{"GIT_TERMINAL_PROMPT": "0"}
	}
	result := make(map[string]string, len(environment)+1)
	for key, value := range environment {
		result[key] = value
	}
	result["GIT_TERMINAL_PROMPT"] = "0"
	return result
}

func gitWorkspaceUsesInstallationToken(environment map[string]string) bool {
	return strings.TrimSpace(environment["ITBEM_GITHUB_INSTALLATION_TOKEN"]) != ""
}

func gitWorkspaceCloneArguments(branch, remote, directory string, suppressCredentialHelper bool) []string {
	args := []string{"clone", "--origin", "origin", "--branch", branch, "--no-recurse-submodules", remote, directory}
	if suppressCredentialHelper {
		args = append(gitHubInstallationGitConfigArguments(), args...)
	}
	return args
}

func gitWorkspaceFetchArguments(remote string, suppressCredentialHelper bool) []string {
	args := []string{"fetch", "--prune", "--tags", "--no-recurse-submodules", remote}
	if suppressCredentialHelper {
		args = append(gitHubInstallationGitConfigArguments(), args...)
	}
	if remote != "origin" {
		args = append(args, "+refs/heads/*:refs/remotes/origin/*")
	}
	return args
}

func gitHubInstallationGitConfigArguments() []string {
	return []string{"-c", "credential.helper=", "-c", "http.proxy=", "-c", "http.sslVerify=true", "-c", "http.extraHeader="}
}

func validateCommandKinds(validationCommands [][]string, validationKinds []string, qaCommands [][]string, qaKinds []string) error {
	for _, pair := range []struct {
		name     string
		commands [][]string
		kinds    []string
	}{{"validation_command_kinds", validationCommands, validationKinds}, {"qa_command_kinds", qaCommands, qaKinds}} {
		if len(pair.kinds) != 0 && len(pair.kinds) != len(pair.commands) {
			return fmt.Errorf("%s must be empty or contain one identity per command", pair.name)
		}
	}
	seen := make(map[string]struct{}, len(validationKinds)+len(qaKinds))
	for _, kind := range append(append([]string(nil), validationKinds...), qaKinds...) {
		if kind != strings.TrimSpace(kind) || !workspaceTestKind.MatchString(kind) {
			return fmt.Errorf("test command identity %q is invalid", kind)
		}
		key := strings.ToLower(kind)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("test command identity %q is duplicated", kind)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func configuredCommandKind(kinds []string, index int) string {
	if index < 0 || index >= len(kinds) {
		return ""
	}
	return kinds[index]
}

func validateReadOnlyFixturePaths(paths []string) error {
	if len(paths) > maxReadOnlyFixturePaths {
		return fmt.Errorf("read_only_fixture_paths may contain at most %d entries", maxReadOnlyFixturePaths)
	}
	seen := make(map[string]struct{}, len(paths))
	for index, configured := range paths {
		configured = strings.TrimSpace(configured)
		if configured == "" || filepath.IsAbs(configured) || filepath.VolumeName(configured) != "" || strings.ContainsAny(configured, "\x00\r\n") {
			return fmt.Errorf("read_only_fixture_paths[%d] is invalid", index)
		}
		clean := filepath.Clean(configured)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("read_only_fixture_paths[%d] must remain inside the workspace", index)
		}
		for _, segment := range strings.FieldsFunc(filepath.ToSlash(clean), func(r rune) bool { return r == '/' }) {
			if excludedDirectory(segment) {
				return fmt.Errorf("read_only_fixture_paths[%d] targets an excluded directory", index)
			}
		}
		if !safeContextFile(clean) {
			return fmt.Errorf("read_only_fixture_paths[%d] may contain credentials", index)
		}
		key := strings.ToLower(filepath.ToSlash(clean))
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("read_only_fixture_paths[%d] is duplicated", index)
		}
		seen[key] = struct{}{}
		paths[index] = clean
	}
	return nil
}

// copyReadOnlyWorkspaceFixtures projects only explicitly operator-approved
// fixture paths. It excludes Git metadata, symlinks, special files, and secret-
// shaped paths, and enforces an aggregate size/file budget.
func copyReadOnlyWorkspaceFixtures(workspace Workspace, worktreeRoot string) error {
	if err := validateReadOnlyFixturePaths(workspace.Config.ReadOnlyFixturePaths); err != nil {
		return err
	}
	files, totalBytes := 0, int64(0)
	for _, relativeRoot := range workspace.Config.ReadOnlyFixturePaths {
		source := filepath.Join(workspace.Root, relativeRoot)
		destination := filepath.Join(worktreeRoot, relativeRoot)
		info, err := os.Lstat(source)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("configured read-only fixture %q is unavailable or unsafe", filepath.ToSlash(relativeRoot))
		}
		if err := os.RemoveAll(destination); err != nil {
			return fmt.Errorf("refresh read-only fixture %q", filepath.ToSlash(relativeRoot))
		}
		err = filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, relErr := filepath.Rel(source, path)
			if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return fmt.Errorf("invalid fixture path")
			}
			if entry.Name() == ".git" {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() && !entry.Type().IsRegular() {
				return fmt.Errorf("read-only fixture contains a link or special file")
			}
			projected := filepath.Join(relativeRoot, relative)
			if !safeContextFile(projected) {
				return fmt.Errorf("read-only fixture contains a credential-like path")
			}
			target := filepath.Join(destination, relative)
			if entry.IsDir() {
				return os.MkdirAll(target, 0700)
			}
			files++
			entryInfo, err := entry.Info()
			if err != nil {
				return err
			}
			totalBytes += entryInfo.Size()
			if files > maxReadOnlyFixtureFiles || totalBytes > maxReadOnlyFixtureBytes {
				return fmt.Errorf("read-only fixtures exceed the safe copy budget")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			input, err := os.Open(path)
			if err != nil {
				return err
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
			if err != nil {
				_ = input.Close()
				return err
			}
			_, copyErr := io.Copy(output, input)
			inputErr, outputErr := input.Close(), output.Close()
			if copyErr != nil {
				return copyErr
			}
			if inputErr != nil {
				return inputErr
			}
			return outputErr
		})
		if err != nil {
			return fmt.Errorf("copy read-only fixture %q: %w", filepath.ToSlash(relativeRoot), err)
		}
	}
	return copyPinnedContractFixture(workspace.Root, worktreeRoot)
}

func isolatedWorktreeAt(ctx context.Context, workspace Workspace, taskID, expectedRevision string) (string, string, error) {
	if !taskIDPattern.MatchString(strings.ToLower(strings.TrimSpace(taskID))) {
		return "", "", fmt.Errorf("isolated worktree task ID is invalid")
	}
	inside, err := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "rev-parse", "--is-inside-work-tree")
	if err != nil || inside.ExitCode != 0 || strings.TrimSpace(inside.Output) != "true" {
		return "", "", fmt.Errorf("registered workspace must be a Git worktree")
	}
	expectedRevision = strings.ToLower(strings.TrimSpace(expectedRevision))
	if expectedRevision != "" {
		if !gitCommitPattern.MatchString(expectedRevision) {
			return "", "", fmt.Errorf("isolated worktree expected revision is invalid")
		}
		known, knownErr := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "rev-parse", "--verify", "--quiet", expectedRevision+"^{commit}")
		if knownErr != nil || known.ExitCode != 0 || !strings.EqualFold(strings.TrimSpace(known.Output), expectedRevision) {
			return "", "", fmt.Errorf("isolated worktree expected revision is unavailable locally")
		}
	}
	if err := workspace.RequireCapability(WorkspaceCapabilityCreateWorktree); err != nil {
		return "", "", err
	}
	release, err := lockManagedWorkspace(ctx, workspace.Root)
	if err != nil {
		return "", "", err
	}
	defer release()
	branch := "itbem-agent/" + taskID
	directory := filepath.Join(workspace.Root, ".itbem-agent-worktrees", taskID)
	revision := expectedRevision
	if revision == "" {
		revision = "HEAD"
	}
	if info, statErr := os.Stat(directory); statErr == nil && info.IsDir() {
		actual, evalErr := filepath.EvalSymlinks(directory)
		if evalErr != nil || !strings.EqualFold(filepath.Clean(actual), filepath.Clean(directory)) {
			return "", "", fmt.Errorf("isolated worktree cannot be a linked directory")
		}
		current, currentErr := runLocal(ctx, directory, 20*time.Second, "", "git", "branch", "--show-current")
		if currentErr != nil || current.ExitCode != 0 || strings.TrimSpace(current.Output) != branch {
			return "", "", fmt.Errorf("isolated worktree branch no longer matches the task")
		}
		if expectedRevision != "" {
			head, headErr := runLocal(ctx, directory, 20*time.Second, "", "git", "rev-parse", "HEAD")
			if headErr != nil || head.ExitCode != 0 || !strings.EqualFold(strings.TrimSpace(head.Output), expectedRevision) {
				return "", "", fmt.Errorf("existing isolated worktree does not match the frozen revision")
			}
		}
		if err := copyReadOnlyWorkspaceFixtures(workspace, directory); err != nil {
			return "", "", err
		}
		return directory, branch, nil
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", "", fmt.Errorf("isolated worktree path is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
		return "", "", fmt.Errorf("prepare isolated worktree")
	}
	created, err := runLocal(ctx, workspace.Root, 90*time.Second, "", "git", "worktree", "add", "-b", branch, directory, revision)
	if err != nil || created.ExitCode != 0 {
		return "", "", fmt.Errorf("could not create isolated local worktree")
	}
	if err := copyReadOnlyWorkspaceFixtures(workspace, directory); err != nil {
		return "", "", err
	}
	return directory, branch, nil
}

func gitHubSourceWorkspaceRemote(workspace Workspace) (githubRepository, string, bool, error) {
	registered := strings.TrimSpace(workspace.Config.RepositoryURL)
	if registered == "" {
		return githubRepository{}, "", false, nil
	}
	lower := strings.ToLower(registered)
	if !strings.HasPrefix(lower, "https://github.com/") && !strings.HasPrefix(lower, "git@github.com:") {
		return githubRepository{}, "", false, nil
	}
	repository, err := parseGitHubRemote(registered)
	if err != nil {
		return githubRepository{}, "", true, fmt.Errorf("workspace %s has an invalid GitHub repository_url", workspace.ID)
	}
	return repository, "https://github.com/" + repository.Owner + "/" + repository.Name + ".git", true, nil
}

func ensureManagedWorkspaceOrigin(ctx context.Context, workspace Workspace) error {
	if err := workspace.RequireCapability(WorkspaceCapabilityFetchRemote); err != nil {
		return err
	}
	state := workspaceGitState(workspace.Root)
	if !state.Available || strings.TrimSpace(state.HeadSHA) == "" {
		return fmt.Errorf("workspace is not a readable Git repository")
	}
	origin, err := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "config", "--get", "remote.origin.url")
	if err != nil || origin.ExitCode != 0 || !sameManagedRemote(strings.TrimSpace(origin.Output), workspace.Config.RepositoryURL) {
		return fmt.Errorf("workspace origin does not match its registered repository_url")
	}
	return nil
}

func verifyDeliveryWorkspaceBinding(workspace Workspace, state WorkspaceGitState, metadata map[string]any) error {
	rawExpected, declared := metadata["github_repository"].(string)
	if !declared || strings.TrimSpace(rawExpected) == "" {
		return nil
	}
	expected := strings.Trim(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(rawExpected)), "github://"), "/")
	actual := strings.Trim(strings.ToLower(strings.TrimSpace(state.GitHubRepository)), "/")
	if actual == "" || actual != expected {
		return fmt.Errorf("workspace %s GitHub identity does not match the frozen Delivery context", workspace.ID)
	}
	return nil
}

// PrepareDeliveryWorkspaces refreshes operator-managed workspaces before any
// inference and proves the local tree still matches each immutable checkpoint.
// It never accepts a path or remote from the task payload.
func PrepareDeliveryWorkspaces(ctx context.Context, delivery json.RawMessage, lookup func(string) string) error {
	var value struct {
		ContextSources []struct {
			Kind      string         `json:"kind"`
			Reference string         `json:"reference"`
			Revision  string         `json:"revision"`
			Metadata  map[string]any `json:"metadata"`
		} `json:"context_sources"`
	}
	if err := json.Unmarshal(delivery, &value); err != nil {
		return fmt.Errorf("delivery input must be a JSON object")
	}
	seen := make(map[string]struct{}, len(value.ContextSources))
	registry, err := LoadWorkspaceRegistry(lookup("ITBEM_AI_WORKSPACES_JSON"))
	if err != nil {
		return err
	}
	for _, source := range value.ContextSources {
		if source.Kind != "repository" || !strings.HasPrefix(strings.TrimSpace(source.Reference), "workspace://") {
			continue
		}
		reference := strings.TrimSpace(source.Reference)
		if _, duplicate := seen[reference]; duplicate {
			continue
		}
		seen[reference] = struct{}{}
		id := strings.TrimPrefix(reference, "workspace://")
		workspace, registered := registry[id]
		if !registered {
			return fmt.Errorf("workspace is not registered locally: %s", id)
		}
		var err error
		expected := strings.ToLower(strings.TrimSpace(source.Revision))
		if expected != "" && !gitCommitPattern.MatchString(expected) {
			return fmt.Errorf("workspace %s managed Delivery source has an invalid immutable revision", workspace.ID)
		}
		var state WorkspaceGitState
		if strings.TrimSpace(workspace.Config.RepositoryURL) != "" {
			if expected == "" {
				return fmt.Errorf("workspace %s managed Delivery source has no immutable frozen revision", workspace.ID)
			}
			state, err = SyncAuthorizedManagedWorkspace(ctx, workspace, lookup)
			if err != nil {
				return fmt.Errorf("workspace %s could not synchronize its managed base before Delivery: %w", workspace.ID, err)
			}
		} else {
			state = ReadWorkspaceGitState(workspace)
			if !state.Available || state.HasLocalChanges {
				return fmt.Errorf("workspace %s must be a clean readable Git checkout", workspace.ID)
			}
		}
		if err := verifyDeliveryWorkspaceBinding(workspace, state, source.Metadata); err != nil {
			return err
		}
		if expected != "" {
			known, knownErr := runLocal(ctx, workspace.Root, 20*time.Second, "", "git", "rev-parse", "--verify", "--quiet", expected+"^{commit}")
			if knownErr != nil || known.ExitCode != 0 || !strings.EqualFold(strings.TrimSpace(known.Output), expected) {
				return fmt.Errorf("workspace %s frozen context revision is unavailable locally; refresh the project checkpoint and replan", workspace.ID)
			}
		}
		if state.RemoteAhead > 0 {
			return fmt.Errorf("workspace %s is %d known commits behind its tracking branch; refresh the project checkpoint and replan", workspace.ID, state.RemoteAhead)
		}
	}
	return nil
}

func GitHubSourceAccessRequired(lookup func(string) string) (bool, error) {
	workspaces, err := LoadWorkspaceRegistry(lookup("ITBEM_AI_WORKSPACES_JSON"))
	if err != nil {
		return false, err
	}
	for _, workspace := range workspaces {
		_, _, required, sourceErr := gitHubSourceWorkspaceRemote(workspace)
		if sourceErr != nil {
			return false, sourceErr
		}
		if required {
			return true, nil
		}
	}
	return false, nil
}

func FetchAuthorizedWorkspaceRemote(ctx context.Context, workspace Workspace, lookup func(string) string) (WorkspaceGitState, error) {
	repository, remote, required, err := gitHubSourceWorkspaceRemote(workspace)
	if err != nil {
		return WorkspaceGitState{}, err
	}
	if !required {
		return FetchWorkspaceRemote(ctx, workspace)
	}
	config, err := LoadGitHubSourceAppConfig(lookup)
	if err != nil {
		return WorkspaceGitState{}, fmt.Errorf("GitHub source App is required for workspace %s: %w", workspace.ID, err)
	}
	return FetchWorkspaceRemoteWithGitHubApp(ctx, workspace, repository, remote, config, nil, time.Now().UTC())
}

func FetchWorkspaceRemoteWithGitHubApp(ctx context.Context, workspace Workspace, repository githubRepository, remote string, config GitHubAppConfig, client *http.Client, now time.Time) (WorkspaceGitState, error) {
	if err := ensureManagedWorkspaceOrigin(ctx, workspace); err != nil {
		return WorkspaceGitState{}, err
	}
	registeredRepository, registeredRemote, required, err := gitHubSourceWorkspaceRemote(workspace)
	if err != nil || !required || !strings.EqualFold(registeredRepository.Owner+"/"+registeredRepository.Name, repository.Owner+"/"+repository.Name) || !sameManagedRemote(registeredRemote, remote) {
		return WorkspaceGitState{}, fmt.Errorf("GitHub source fetch target does not match the registered workspace")
	}
	token, err := MintGitHubRepositoryToken(ctx, config, client, now, repository.Owner+"/"+repository.Name)
	if err != nil {
		return WorkspaceGitState{}, fmt.Errorf("GitHub source App could not authenticate the registered repository")
	}
	environment, cleanup, err := gitHubInstallationTokenEnvironment(token.Token)
	if err != nil {
		return WorkspaceGitState{}, err
	}
	defer cleanup()
	// Fetch through the already-verified origin remote name so Git applies its
	// configured remote-tracking refspecs. Passing only the URL would fetch an
	// implicit remote HEAD and fail on repositories whose bare test/server does
	// not advertise HEAD.
	args := []string{"-c", "credential.helper=", "-c", "http.proxy=", "-c", "http.sslVerify=true", "-c", "http.extraHeader=", "fetch", "--prune", "--tags", "--no-recurse-submodules", "origin"}
	fetched, runErr := runLocalWithEnv(ctx, workspace.Root, 60*time.Second, "", environment, "git", args...)
	if runErr != nil || fetched.ExitCode != 0 {
		return WorkspaceGitState{}, fmt.Errorf("authorized GitHub workspace fetch could not complete")
	}
	return workspaceGitState(workspace.Root), nil
}
