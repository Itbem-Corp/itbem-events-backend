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
func FetchGitHubQASourcePack(ctx context.Context, repository, commit string, config GitHubAppConfig, client *http.Client) ([]byte, string, error) {
	return fetchGitHubQASource(ctx, repository, commit, config, client, buildQASourcePack)
}

// QASourceBundle carries raw Git packages without credentials or rewritten
// commits. Transport must bind every descriptor to its server receipt.
type QASourceBundle struct {
	Pack         []byte
	PackSHA256   string
	Dependencies []QASourceDependencyPack
}

type QASourceDependencyPack struct {
	Path       string
	Repository string
	CommitSHA  string
	PackSHA256 string
	Pack       []byte
}

// Approved dependencies come from operator policy, never request payloads or
// repository declarations. Each child gets a fresh repository-scoped token.
func FetchGitHubQASourceBundle(ctx context.Context, repository, commit string, approved map[string]string, config GitHubAppConfig, client *http.Client) (QASourceBundle, error) {
	var result QASourceBundle
	if len(approved) > 16 {
		return result, fmt.Errorf("QA dependency approval exceeds its boundary")
	}
	policy := make(map[string]string, len(approved))
	for path, repository := range approved {
		if !safeQADependencyPath(path) || !githubRepositoryNamePattern.MatchString(strings.ToLower(repository)) {
			return result, fmt.Errorf("QA dependency approval is invalid")
		}
		policy[path] = strings.ToLower(repository)
	}
	pack, digest, err := fetchGitHubQASource(ctx, repository, commit, config, client, func(ctx context.Context, root, commit string) ([]byte, string, error) {
		pack, digest, children, err := buildQASourceBundle(ctx, root, commit, policy, func(ctx context.Context, repository, commit string) ([]byte, string, error) {
			return FetchGitHubQASourcePack(ctx, repository, commit, config, client)
		})
		if err != nil {
			return nil, "", err
		}
		for _, child := range children {
			result.Dependencies = append(result.Dependencies, QASourceDependencyPack{Path: child.Path, Repository: child.Repository, CommitSHA: child.CommitSHA, PackSHA256: child.PackSHA256, Pack: child.Pack})
		}
		return pack, digest, nil
	})
	if err != nil {
		return QASourceBundle{}, err
	}
	result.Pack, result.PackSHA256 = pack, digest
	return result, nil
}

func fetchGitHubQASource(ctx context.Context, repository, commit string, config GitHubAppConfig, client *http.Client, consume func(context.Context, string, string) ([]byte, string, error)) ([]byte, string, error) {
	repository = strings.ToLower(repository)
	if !githubRepositoryNamePattern.MatchString(repository) || !gitCommitPattern.MatchString(commit) {
		return nil, "", fmt.Errorf("QA source acquisition identity is invalid")
	}
	scratch, err := qaSourceScratchRoot()
	if err != nil {
		return nil, "", err
	}
	ctx = withQASourceResourceBoundary(ctx)
	token, err := MintGitHubRepositoryToken(ctx, config, client, time.Now().UTC(), repository)
	if err != nil {
		return nil, "", fmt.Errorf("QA source repository authentication unavailable")
	}
	root, err := os.MkdirTemp(scratch, "itbem-qa-source-fetch-")
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
	return consume(ctx, root, commit)
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
