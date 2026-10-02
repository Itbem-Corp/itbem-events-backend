package automationagent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReviewedDiffDoesNotExecuteConfiguredTextConversion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a Linux synthetic shell fixture")
	}
	root := setupImplementationRepository(t)
	base, err := runLocal(context.Background(), root, time.Minute, "", "git", "rev-parse", "HEAD")
	if err != nil || base.ExitCode != 0 {
		t.Fatal("fixture base missing")
	}
	baseSHA := strings.TrimSpace(base.Output)
	marker := filepath.Join(t.TempDir(), "converter-ran")
	script := filepath.Join(t.TempDir(), "synthetic-converter")
	if err := os.WriteFile(script, []byte(fmt.Sprintf("#!/bin/sh\nprintf synthetic > %q\nprintf lossy\n", marker)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("README.md diff=synthetic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("raw reviewed content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configured, err := runLocal(context.Background(), root, time.Minute, "", "git", "config", "diff.synthetic.textconv", script)
	if err != nil || configured.ExitCode != 0 {
		t.Fatal("fixture converter configuration failed")
	}
	probe, err := runLocal(context.Background(), root, time.Minute, "", "git", "diff", "--textconv", baseSHA)
	if err != nil || probe.ExitCode != 0 {
		t.Fatal("synthetic converter positive control failed")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("synthetic conversion did not execute in positive control")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	digest, err := worktreeDiffSHA256(context.Background(), root, baseSHA, false)
	if err != nil {
		t.Fatal(err)
	}
	artifactDigest, patch, err := captureWorktreePatch(context.Background(), root, baseSHA, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if artifactDigest != digest || fmt.Sprintf("%x", sha256.Sum256(patch)) != digest || !strings.Contains(string(patch), "+raw reviewed content") {
		t.Fatal("reviewed diff did not preserve exact raw bytes")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("host converter executed while binding reviewed source")
	}
}
