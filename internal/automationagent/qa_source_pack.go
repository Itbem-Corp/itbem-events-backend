package automationagent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxQASourcePackBytes = 64 << 20
	maxQASourceTreeBytes = 128 << 20
	maxQASourceTreeFiles = 20000
)

// Consumes a server-authenticated pack under the frozen published SHA. The
// production source endpoint must establish that authorization before calling
// this primitive; the worker never receives a GitHub credential here.
func materializePublishedQASourcePack(ctx context.Context, workspace Workspace, branch, commit, packDigest string, pack []byte) (string, error) {
	if !strings.HasPrefix(branch, "itbem-agent/") || !taskIDPattern.MatchString(strings.TrimPrefix(branch, "itbem-agent/")) || !gitCommitPattern.MatchString(commit) || !sha256DigestPattern.MatchString(packDigest) {
		return "", fmt.Errorf("QA source identity invalid")
	}
	if !validQASourcePackEnvelope(packDigest, pack) {
		return "", fmt.Errorf("QA source pack integrity or bound invalid")
	}
	_, remote, required, err := gitHubSourceWorkspaceRemote(workspace)
	if err != nil || !required {
		return "", fmt.Errorf("QA source requires an operator-registered GitHub repository")
	}
	resolved, err := filepath.EvalSymlinks(workspace.Root)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(workspace.Root) {
		return "", fmt.Errorf("QA source workspace must be a real registered directory")
	}
	parent := filepath.Join(workspace.Root, ".itbem-agent-worktrees")
	if err := os.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("QA source parent must be a real directory")
	}
	name := strings.TrimPrefix(branch, "itbem-agent/")
	lockPath := filepath.Join(parent, ".qa-source-lock-"+name)
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("QA source materialization is already locked")
	}
	defer func() { _ = lock.Close(); _ = os.Remove(lockPath) }()
	target := filepath.Join(parent, name)
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("QA source target must be a real directory")
		}
		if err := verifyQASourceLocalConfig(ctx, target); err != nil {
			return "", err
		}
		if err := verifyQASourceOrigin(ctx, target, remote); err != nil {
			return "", err
		}
		if err := verifyQASourceRevision(ctx, target, branch, commit); err != nil {
			return "", err
		}
		if _, err := sandboxWorktreeDigest(target); err != nil {
			return "", err
		}
		return target, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	stage, err := os.MkdirTemp(parent, ".qa-source-stage-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := qaSourceGit(ctx, stage, nil, "init", "--template=", "--initial-branch=qa-source"); err != nil {
		return "", err
	}
	// Git's shallow boundary preserves the original commit object and tree; no
	// replacement commit is created merely to reproduce the source files.
	if err := os.WriteFile(filepath.Join(stage, ".git", "shallow"), []byte(commit+"\n"), 0600); err != nil {
		return "", err
	}
	if err := qaSourceGit(ctx, stage, bytes.NewReader(pack), "index-pack", "--stdin", "--strict", "--threads=1", "--max-input-size="+strconv.Itoa(maxQASourcePackBytes)); err != nil {
		return "", err
	}
	if err := qaSourceGit(ctx, stage, nil, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return "", err
	}
	if err := validateQASourceTree(ctx, stage, commit); err != nil {
		return "", err
	}
	for _, args := range [][]string{{"symbolic-ref", "HEAD", "refs/heads/" + branch}, {"update-ref", "refs/heads/" + branch, commit}, {"remote", "add", "origin", remote}, {"reset", "--hard", commit}, {"fsck", "--strict", "--no-dangling"}} {
		if err := qaSourceGit(ctx, stage, nil, args...); err != nil {
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

func validQASourcePackEnvelope(digest string, pack []byte) bool {
	return len(pack) >= 12 && len(pack) <= maxQASourcePackBytes && string(pack[:4]) == "PACK" && binary.BigEndian.Uint32(pack[4:8]) == 2 && binary.BigEndian.Uint32(pack[8:12]) >= 1 && binary.BigEndian.Uint32(pack[8:12]) <= 3*maxQASourceTreeFiles && sha256DigestPattern.MatchString(digest) && fmt.Sprintf("%x", sha256.Sum256(pack)) == digest
}

func verifyQASourceRevision(ctx context.Context, root, branch, commit string) error {
	for _, check := range []struct {
		args     []string
		expected string
	}{
		{[]string{"branch", "--show-current"}, branch},
		{[]string{"rev-parse", "HEAD"}, commit},
		{[]string{"diff", "--no-ext-diff", "--no-textconv", "--quiet", "HEAD"}, ""},
		{[]string{"ls-files", "--others"}, ""},
	} {
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		cmd := qaSourceGitCommand(bounded, root, check.args...)
		var stdout, stderr boundedCommandBuffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		cancel()
		if err != nil || strings.TrimSpace(stdout.String()) != check.expected {
			return fmt.Errorf("QA source does not match its exact clean commit")
		}
	}
	return nil
}

// Imported source repositories have no operator customization. Fail closed
// on extra local configuration before commands that could consult filters,
// monitors, include files, or alternate Git authorities.
func verifyQASourceLocalConfig(ctx context.Context, root string) error {
	info, err := os.Lstat(filepath.Join(root, ".git"))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("QA source Git directory must be private and real")
	}
	entries := 0
	if err := filepath.WalkDir(filepath.Join(root, ".git"), func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > 100000 || entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("QA source Git authority contains unsupported filesystem entries")
		}
		relative, err := filepath.Rel(filepath.Join(root, ".git"), name)
		if err != nil {
			return err
		}
		switch filepath.ToSlash(relative) {
		case "objects/info/alternates", "objects/info/http-alternates":
			return fmt.Errorf("QA source Git authority cannot reference external objects")
		}
		return ctx.Err()
	}); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := qaSourceGitCommand(bounded, root, "config", "--local", "--no-includes", "--name-only", "--list")
	var stdout, stderr boundedCommandBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("QA source local configuration cannot be verified")
	}
	allowed := map[string]bool{"core.repositoryformatversion": true, "core.filemode": true, "core.bare": true, "core.logallrefupdates": true, "core.ignorecase": true, "core.precomposeunicode": true, "remote.origin.url": true, "remote.origin.fetch": true}
	for _, key := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if !allowed[strings.ToLower(strings.TrimSpace(key))] {
			return fmt.Errorf("QA source local configuration contains unsupported authority")
		}
	}
	return nil
}

func verifyQASourceOrigin(ctx context.Context, root, expected string) error {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := qaSourceGitCommand(bounded, root, "config", "--local", "--get-all", "remote.origin.url")
	var stdout, stderr boundedCommandBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil || strings.TrimSpace(stdout.String()) != expected {
		return fmt.Errorf("QA source origin differs from the registered repository")
	}
	return nil
}

func qaSourceGitCommand(ctx context.Context, root string, args ...string) *exec.Cmd {
	options := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.autocrlf=false", "-c", "core.protectHFS=true", "-c", "core.protectNTFS=true", "-c", "credential.helper=", "-c", "protocol.allow=never"}
	executable, arguments := qaSourceCommandArguments(ctx, append(options, args...))
	cmd := exec.CommandContext(ctx, executable, arguments...)
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=", "GIT_ATTR_NOSYSTEM=1", "LANG=C"}
	if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
		cmd.Env = append(cmd.Env, "SystemRoot="+systemRoot)
	}
	stop := configureCommandProcessGroup(cmd)
	cmd.Cancel = func() error { stop(); return nil }
	cmd.WaitDelay = 750 * time.Millisecond
	return cmd
}

func qaSourceGit(ctx context.Context, root string, input io.Reader, args ...string) error {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := qaSourceGitCommand(bounded, root, args...)
	cmd.Stdin = input
	var stdout, stderr boundedCommandBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("QA source Git verification failed")
	}
	return nil
}

func validateQASourceTree(ctx context.Context, root, commit string) error {
	dependencies, _ := ctx.Value(qaSourceDependencyBoundaryKey{}).(map[string]string)
	return validateQASourceTreeWithDependencies(ctx, root, commit, dependencies)
}

func validateQASourceTreeWithDependencies(ctx context.Context, root, commit string, dependencies map[string]string) error {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := qaSourceGitCommand(bounded, root, "ls-tree", "-r", "-z", "-l", commit)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr boundedCommandBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(stdout, 8192)
	var total int64
	files := 0
	seenDependencies := map[string]bool{}
	for {
		record, readErr := reader.ReadSlice(0)
		if readErr == io.EOF && len(record) == 0 {
			break
		}
		if readErr != nil {
			cancel()
			_ = cmd.Wait()
			return fmt.Errorf("QA source tree record exceeds its boundary")
		}
		header, name, found := strings.Cut(string(record[:len(record)-1]), "\t")
		fields := strings.Fields(header)
		if found && len(fields) == 4 && fields[0] == "160000" && fields[1] == "commit" && safeQADependencyPath(name) && gitCommitPattern.MatchString(fields[2]) && dependencies[name] == fields[2] && !seenDependencies[name] {
			seenDependencies[name] = true
			continue
		}
		if !found || len(fields) != 4 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" || !utf8.ValidString(name) || name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\r\n") {
			cancel()
			_ = cmd.Wait()
			return fmt.Errorf("QA source tree contains unsupported or unsafe entries")
		}
		for _, part := range strings.Split(name, "/") {
			if strings.EqualFold(part, ".git") || part == ".." {
				cancel()
				_ = cmd.Wait()
				return fmt.Errorf("QA source tree contains Git authority paths")
			}
		}
		size, sizeErr := strconv.ParseInt(fields[3], 10, 64)
		files++
		total += size
		if sizeErr != nil || size < 0 || size > 32<<20 || files > maxQASourceTreeFiles || total > maxQASourceTreeBytes {
			cancel()
			_ = cmd.Wait()
			return fmt.Errorf("QA source expanded tree exceeds its boundary")
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("QA source tree verification failed")
	}
	if len(seenDependencies) != len(dependencies) {
		return fmt.Errorf("QA source pinned dependency matrix incomplete")
	}
	if budget, ok := ctx.Value(qaSourceBundleBudgetKey{}).(*qaSourceBundleBudget); ok {
		if total > budget.bytes || files > budget.files {
			return fmt.Errorf("QA source bundle expanded tree exceeds its boundary")
		}
		budget.bytes -= total
		budget.files -= files
	}
	return nil
}
