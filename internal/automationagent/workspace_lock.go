package automationagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Only base-checkout mutations need serialization. Task worktrees retain their
// own branches and can execute concurrently after the short preparation phase.
// The kernel releases this lock if a worker crashes; no stale lease is deleted.
func lockManagedWorkspace(ctx context.Context, root string) (func(), error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(parent, "."+filepath.Base(root)+".itbem-managed.lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("managed workspace lock must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		locked, err := tryManagedWorkspaceLock(file)
		if err != nil {
			file.Close()
			return nil, err
		}
		if locked {
			return func() { releaseManagedWorkspaceLock(file); file.Close() }, nil
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
