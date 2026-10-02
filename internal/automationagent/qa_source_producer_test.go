package automationagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestQASourceProducerPreservesOriginalCommitAcrossIndependentCheckouts(t *testing.T) {
	commit, fixture := qaSourcePackFixture(t)
	branch := "itbem-agent/11111111-1111-4111-8111-111111111111"
	server := Workspace{ID: "server", Root: t.TempDir(), Config: WorkspaceConfig{RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main"}}
	root, err := materializePublishedQASourcePack(context.Background(), server, branch, commit, fmt.Sprintf("%x", sha256.Sum256(fixture)), fixture)
	if err != nil {
		t.Fatal(err)
	}
	pack, digest, err := buildQASourcePack(context.Background(), root, commit)
	if err != nil {
		t.Fatal(err)
	}
	qa := server
	qa.ID, qa.Root = "independent-qa", t.TempDir()
	target, err := materializePublishedQASourcePack(context.Background(), qa, branch, commit, digest, pack)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"source.go", "unchanged.txt"} {
		want, e := os.ReadFile(filepath.Join(root, name))
		if e != nil {
			t.Fatal(e)
		}
		got, e := os.ReadFile(filepath.Join(target, name))
		if e != nil || !bytes.Equal(got, want) {
			t.Fatalf("source transfer changed %s: %v", name, e)
		}
	}
	if err := verifyQATargetRevision(context.Background(), qaTarget{root: target, branch: branch, reviewCommitSHA: commit}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildQASourcePack(context.Background(), root, "invalid"); err == nil {
		t.Fatal("invalid source subject accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := buildQASourcePack(ctx, root, commit); err == nil {
		t.Fatal("cancelled source export succeeded")
	}
}

func TestQASourcePackOutputBoundAppliesToReaderFrom(t *testing.T) {
	var output qaSourcePackBuffer
	output.Grow(maxQASourcePackBytes)
	output.Buffer.Write(make([]byte, maxQASourcePackBytes-1))
	if _, err := output.ReadFrom(bytes.NewReader([]byte("overflow"))); err == nil || output.Len() > maxQASourcePackBytes {
		t.Fatal("ReaderFrom bypassed pack bound")
	}
}
