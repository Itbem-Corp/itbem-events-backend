package automationagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type qaSourceDependencyBoundaryKey struct{}
type qaSourceBundleBudgetKey struct{}
type qaSourceBundleBudget struct {
	bytes int64
	files int
}

type packedQASourceDependency struct {
	pinnedQASourceDependency
	PackSHA256 string
	Pack       []byte
}

// Private composition primitive. It publishes the parent checkout only after
// every separately approved child has its original pinned commit and origin.
// Transport must authenticate the full descriptor set before calling it.
func materializePublishedQASourceBundle(ctx context.Context, workspace Workspace, branch, commit, digest string, pack []byte, approved map[string]string, children []packedQASourceDependency) (string, error) {
	if len(children) > 16 || len(approved) > 16 || !gitCommitPattern.MatchString(commit) || !validQASourcePackEnvelope(digest, pack) {
		return "", fmt.Errorf("QA source dependency count exceeds its boundary")
	}
	expected := map[string]string{}
	var total int64 = int64(len(pack))
	for _, child := range children {
		if !validQASourcePackEnvelope(child.PackSHA256, child.Pack) {
			return "", fmt.Errorf("QA source child package integrity invalid")
		}
		if !safeQADependencyPath(child.Path) || expected[child.Path] != "" || !gitCommitPattern.MatchString(child.CommitSHA) || !githubRepositoryNamePattern.MatchString(child.Repository) || !strings.EqualFold(approved[child.Path], child.Repository) {
			return "", fmt.Errorf("QA source dependency is not explicitly approved")
		}
		expected[child.Path] = child.CommitSHA
		total += int64(len(child.Pack))
	}
	if total > maxQASourcePackBytes {
		return "", fmt.Errorf("QA source bundle exceeds its package boundary")
	}
	resolved, err := filepath.EvalSymlinks(workspace.Root)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(workspace.Root) {
		return "", fmt.Errorf("QA source workspace must be real")
	}
	parent := filepath.Join(workspace.Root, ".itbem-agent-worktrees")
	if err := os.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("QA source parent must be real")
	}
	if !strings.HasPrefix(branch, "itbem-agent/") || !taskIDPattern.MatchString(strings.TrimPrefix(branch, "itbem-agent/")) {
		return "", fmt.Errorf("QA source branch invalid")
	}
	target := filepath.Join(parent, strings.TrimPrefix(branch, "itbem-agent/"))
	lockPath := filepath.Join(parent, ".qa-source-lock-"+strings.TrimPrefix(branch, "itbem-agent/"))
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("QA source bundle is already locked")
	}
	defer func() { _ = lock.Close(); _ = os.Remove(lockPath) }()
	ctx = context.WithValue(ctx, qaSourceBundleBudgetKey{}, &qaSourceBundleBudget{bytes: maxQASourceTreeBytes, files: maxQASourceTreeFiles})
	// Existing bundles are never replaced or partially repaired.
	if _, err := os.Lstat(target); err == nil {
		if err := verifyQASourceBundleCheckout(ctx, workspace, target, branch, commit, approved, expected, children); err != nil {
			return "", err
		}
		return target, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	holder, err := os.MkdirTemp(parent, ".qa-bundle-stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(holder)
	stagedWorkspace := workspace
	stagedWorkspace.Root = holder
	rootCtx := context.WithValue(ctx, qaSourceDependencyBoundaryKey{}, expected)
	stage, err := materializePublishedQASourcePack(rootCtx, stagedWorkspace, branch, commit, digest, pack)
	if err != nil {
		return "", err
	}
	discovered, err := readPinnedQASourceDependencies(ctx, stage, commit, approved)
	if err != nil || len(discovered) != len(children) {
		return "", fmt.Errorf("QA source dependency declarations differ from the bundle")
	}
	for _, child := range children {
		matched := false
		for _, dependency := range discovered {
			if dependency == child.pinnedQASourceDependency {
				matched = true
			}
		}
		if !matched {
			return "", fmt.Errorf("QA source child differs from the original gitlink")
		}
		childHolder, err := os.MkdirTemp(holder, "child-")
		if err != nil {
			return "", err
		}
		childWorkspace := Workspace{Root: childHolder, Config: WorkspaceConfig{RepositoryURL: "https://github.com/" + child.Repository + ".git", BaseBranch: "main"}}
		childCtx := context.WithValue(ctx, qaSourceDependencyBoundaryKey{}, map[string]string(nil))
		checkout, err := materializePublishedQASourcePack(childCtx, childWorkspace, branch, child.CommitSHA, child.PackSHA256, child.Pack)
		if err != nil {
			return "", err
		}
		destination := filepath.Join(stage, filepath.FromSlash(child.Path))
		if info, err := os.Lstat(destination); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("QA source dependency destination invalid")
			}
			if err := os.Remove(destination); err != nil {
				return "", err
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return "", err
		}
		if err := os.Rename(checkout, destination); err != nil {
			return "", err
		}
	}
	if err := verifyQASourceRevision(ctx, stage, branch, commit); err != nil {
		return "", err
	}
	if _, err := sandboxWorktreeDigest(stage); err != nil {
		return "", err
	}
	if err := os.Rename(stage, target); err != nil {
		return "", err
	}
	return target, nil
}

func verifyQASourceBundleCheckout(ctx context.Context, workspace Workspace, root, branch, commit string, approved, expected map[string]string, children []packedQASourceDependency) error {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(root) {
		return fmt.Errorf("QA source bundle checkout must be real")
	}
	if err := verifyQASourceLocalConfig(ctx, root); err != nil {
		return err
	}
	_, remote, required, err := gitHubSourceWorkspaceRemote(workspace)
	if err != nil || !required {
		return fmt.Errorf("QA source bundle requires its registered repository")
	}
	if err := verifyQASourceOrigin(ctx, root, remote); err != nil {
		return err
	}
	if err := validateQASourceTreeWithDependencies(ctx, root, commit, expected); err != nil {
		return err
	}
	discovered, err := readPinnedQASourceDependencies(ctx, root, commit, approved)
	if err != nil || len(discovered) != len(children) {
		return fmt.Errorf("QA source bundle dependency manifest differs")
	}
	for _, child := range children {
		matched := false
		for _, dependency := range discovered {
			if dependency == child.pinnedQASourceDependency {
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("QA source bundle child does not match the frozen gitlink")
		}
		childRoot := filepath.Join(root, filepath.FromSlash(child.Path))
		resolved, err := filepath.EvalSymlinks(childRoot)
		if err != nil || filepath.Clean(resolved) != filepath.Clean(childRoot) {
			return fmt.Errorf("QA source dependency checkout must be real")
		}
		if err := verifyQASourceLocalConfig(ctx, childRoot); err != nil {
			return err
		}
		if err := verifyQASourceOrigin(ctx, childRoot, "https://github.com/"+child.Repository+".git"); err != nil {
			return err
		}
		if err := validateQASourceTreeWithDependencies(ctx, childRoot, child.CommitSHA, nil); err != nil {
			return err
		}
		if err := verifyQASourceRevision(ctx, childRoot, branch, child.CommitSHA); err != nil {
			return err
		}
	}
	if err := verifyQASourceRevision(ctx, root, branch, commit); err != nil {
		return err
	}
	_, err = sandboxWorktreeDigest(root)
	return err
}
