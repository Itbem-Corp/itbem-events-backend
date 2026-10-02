package automationagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestQASourceResourceBoundaryRejectsUnboundedScratchAndAppliesRealLimits(t *testing.T) {
	var hostFS unix.Statfs_t
	if err := unix.Statfs("/dev/shm", &hostFS); err != nil {
		t.Fatal(err)
	}
	if hostFS.Bsize <= 0 || hostFS.Blocks > uint64((256<<20)/hostFS.Bsize) {
		unbounded, err := os.MkdirTemp("/dev/shm", "itbem-qa-unbounded-test-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(unbounded)
		t.Setenv("ITBEM_QA_SOURCE_SCRATCH_ROOT", unbounded)
		if _, err := qaSourceScratchRoot(); err == nil {
			t.Fatal("oversized real tmpfs was accepted")
		}
		if os.Getenv("ITBEM_QA_RESOURCE_TEST_CONTAINER") == "1" {
			t.Fatal("container fixture does not have bounded tmpfs")
		}
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--network=none", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m", "--tmpfs", "/dev/shm:rw,noexec,nosuid,nodev,size=128m,mode=1777", "--mount", "type=bind,src="+binary+",dst=/qa-resource-test,readonly", "-e", "ITBEM_QA_RESOURCE_TEST_CONTAINER=1", "golang:1.25-bookworm", "/qa-resource-test", "-test.run=^TestQASourceResourceBoundaryRejectsUnboundedScratchAndAppliesRealLimits$", "-test.v")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bounded real filesystem fixture failed: %v %s", err, output)
		}
		return
	}
	t.Setenv("ITBEM_QA_SOURCE_SCRATCH_ROOT", t.TempDir())
	if _, err := qaSourceScratchRoot(); err == nil {
		t.Fatal("unqualified temporary directory accepted")
	}
	root, err := os.MkdirTemp("/dev/shm", "itbem-qa-source-test-")
	if err != nil {
		t.Fatal("required bounded tmpfs fixture unavailable:", err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ITBEM_QA_SOURCE_SCRATCH_ROOT", root)
	if _, err := qaSourceScratchRoot(); err != nil {
		t.Fatal(err)
	}
	ctx := withQASourceResourceBoundary(context.Background())
	if err := qaSourceGit(ctx, root, nil, "init", "--template=", "--initial-branch=source"); err != nil {
		t.Fatal(err)
	}
	program, args := qaSourceCommandArguments(ctx, nil)
	// Inspect the limits from inside a real descendant of the same wrapper.
	args[len(args)-1] = "cat"
	args = append(args, "/proc/self/limits")
	output, err := exec.Command(program, args...).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []string{"536870912", "67108864", "30"} {
		if !strings.Contains(string(output), limit) {
			t.Fatalf("missing resource bound %s", limit)
		}
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := qaSourceScratchRoot(); err == nil {
		t.Fatal("nonprivate scratch accepted")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "scratch")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ITBEM_QA_SOURCE_SCRATCH_ROOT", link)
	if _, err := qaSourceScratchRoot(); err == nil {
		t.Fatal("scratch symlink accepted")
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(root, &fs); err != nil {
		t.Fatal(err)
	}
}
