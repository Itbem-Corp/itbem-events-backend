package automationagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func qaSourceScratchRoot() (string, error) {
	root := os.Getenv("ITBEM_QA_SOURCE_SCRATCH_ROOT")
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("QA source requires a configured bounded scratch filesystem")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(root) {
		return "", fmt.Errorf("QA source scratch must be a real directory")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", fmt.Errorf("QA source scratch must be private")
	}
	var stat unix.Stat_t
	if unix.Stat(root, &stat) != nil || stat.Uid != uint32(os.Geteuid()) {
		return "", fmt.Errorf("QA source scratch owner is invalid")
	}
	var fs unix.Statfs_t
	if unix.Statfs(root, &fs) != nil || fs.Type != unix.TMPFS_MAGIC || fs.Bsize <= 0 || fs.Blocks > uint64((256<<20)/fs.Bsize) || fs.Flags&(unix.ST_NOEXEC|unix.ST_NOSUID|unix.ST_NODEV) != (unix.ST_NOEXEC|unix.ST_NOSUID|unix.ST_NODEV) {
		return "", fmt.Errorf("QA source scratch requires bounded noexec nosuid nodev tmpfs")
	}
	if _, err := exec.LookPath("prlimit"); err != nil {
		return "", fmt.Errorf("QA source process resource limiter is unavailable")
	}
	return root, nil
}

func qaSourceCommandArguments(ctx context.Context, args []string) (string, []string) {
	if bounded, _ := ctx.Value(qaSourceResourceBoundaryKey{}).(bool); !bounded {
		return "git", args
	}
	return "prlimit", append([]string{"--as=536870912:536870912", "--cpu=30:30", "--fsize=67108864:67108864", "--nofile=128:128", "--core=0:0", "--", "git"}, args...)
}
