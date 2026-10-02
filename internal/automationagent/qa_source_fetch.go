package automationagent

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Server-side only. The caller must authorize repository and SHA from the
// immutable task before calling, and revalidate live authority afterwards.
// No endpoint is enabled until the fetch resource boundary is qualified.
func fetchGitHubQASourcePack(ctx context.Context, repository, commit string, config GitHubAppConfig, client *http.Client) ([]byte, string, error) {
	repository = strings.ToLower(repository)
	if !githubRepositoryNamePattern.MatchString(repository) || !gitCommitPattern.MatchString(commit) {
		return nil, "", fmt.Errorf("QA source acquisition identity is invalid")
	}
	token, err := MintGitHubRepositoryToken(ctx, config, client, time.Now().UTC(), repository)
	if err != nil {
		return nil, "", fmt.Errorf("QA source repository authentication unavailable")
	}
	root, err := os.MkdirTemp("", "itbem-qa-source-fetch-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(root)
	if err := qaSourceGit(ctx, root, nil, "init", "--template=", "--initial-branch=qa-source"); err != nil {
		return nil, "", err
	}
	bounded, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := qaSourceFetchCommand(bounded, root, repository, commit, token.Token)
	var stdout, stderr boundedCommandBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("QA source exact commit acquisition failed")
	}
	if err := qaSourceGit(ctx, root, nil, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return nil, "", err
	}
	return buildQASourcePack(ctx, root, commit)
}

func qaSourceFetchCommand(ctx context.Context, root, repository, commit, token string) *exec.Cmd {
	cmd := qaSourceGitCommand(ctx, root,
		"-c", "protocol.https.allow=always", "-c", "http.followRedirects=false",
		"-c", "http.sslVerify=true", "-c", "fetch.fsckObjects=true", "-c", "transfer.fsckObjects=true",
		"fetch", "--no-tags", "--no-recurse-submodules", "--depth=1", "--", "https://github.com/"+repository+".git", commit)
	for index, value := range cmd.Env {
		if strings.HasPrefix(value, "GIT_ALLOW_PROTOCOL=") {
			cmd.Env[index] = "GIT_ALLOW_PROTOCOL=https"
		}
	}
	// Credentials are neither persisted in Git configuration nor supplied in
	// argv/remote URLs. The header is scoped to the sole authorized HTTPS host.
	header := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0="+header)
	return cmd
}
