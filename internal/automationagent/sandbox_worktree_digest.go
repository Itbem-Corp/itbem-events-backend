package automationagent

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Versioned source manifest shared with scripts/sandbox_worktree.py. Git
// metadata and installed dependencies are outside this source binding. They
// require their own immutable dependency evidence, not a workspace path hash.
func sandboxWorktreeDigest(root string) ([32]byte, error) {
	var empty [32]byte
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return empty, fmt.Errorf("worktree root must be a real directory")
	}
	confined, err := os.OpenRoot(root)
	if err != nil {
		return empty, err
	}
	defer confined.Close()
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if sandboxWorktreeExcluded(entry.Name()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if sandboxWorktreeCredential(entry.Name()) {
			return fmt.Errorf("credential material is not allowed in a sandbox worktree")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("worktree contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("worktree contains a non-regular file")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !utf8.ValidString(relative) || len(strings.Split(relative, "/")) > 32 {
			return fmt.Errorf("invalid worktree path")
		}
		paths = append(paths, relative)
		if len(paths) > 4096 {
			return fmt.Errorf("worktree file count exceeds transfer bound")
		}
		return nil
	})
	if err != nil {
		return empty, err
	}
	sort.Strings(paths)
	hash := sha256.New()
	_, _ = hash.Write([]byte("itbem-sandbox-source-v1\n"))
	var total int64
	for _, relative := range paths {
		path := filepath.Join(root, filepath.FromSlash(relative))
		before, err := os.Lstat(path)
		if err != nil || !before.Mode().IsRegular() || before.Size() > 8*1024*1024 {
			return empty, fmt.Errorf("invalid or oversized worktree file")
		}
		total += before.Size()
		if total > 64*1024*1024 {
			return empty, fmt.Errorf("worktree bytes exceed transfer bound")
		}
		file, err := openSandboxSourceFile(confined, relative)
		if err != nil {
			return empty, err
		}
		opened, statErr := file.Stat()
		if statErr != nil || !os.SameFile(before, opened) {
			file.Close()
			return empty, fmt.Errorf("worktree file changed before hashing")
		}
		content := sha256.New()
		count, readErr := io.Copy(content, io.LimitReader(file, 8*1024*1024+1))
		after, statErr := file.Stat()
		closeErr := file.Close()
		current, currentErr := os.Lstat(path)
		if readErr != nil || closeErr != nil || statErr != nil || currentErr != nil || count != before.Size() || !os.SameFile(before, current) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
			return empty, fmt.Errorf("worktree changed while hashing")
		}
		_ = binary.Write(hash, binary.BigEndian, uint64(len([]byte(relative))))
		_, _ = hash.Write([]byte(relative))
		var executable byte
		if before.Mode().Perm()&0111 != 0 {
			executable = 1
		}
		_, _ = hash.Write([]byte{executable})
		_ = binary.Write(hash, binary.BigEndian, uint64(count))
		_, _ = hash.Write(content.Sum(nil))
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func sandboxWorktreeExcluded(name string) bool {
	switch name {
	case ".git", "node_modules", ".next", ".venv", "venv":
		return true
	}
	return false
}

func sandboxWorktreeCredential(name string) bool {
	name = strings.ToLower(name)
	if name == ".env.example" || name == ".env.sample" || name == ".env.template" {
		return false
	}
	return name == ".env" || strings.HasPrefix(name, ".env.") || name == ".aws" || name == ".ssh" || name == ".local" || name == ".codex" || name == ".config" || name == "credentials" || name == "id_rsa" || name == "id_ed25519" || strings.HasSuffix(name, ".key")
}
