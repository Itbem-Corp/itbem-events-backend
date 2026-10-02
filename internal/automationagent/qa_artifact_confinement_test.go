package automationagent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQAArtifactReadsConfineAncestorLinksAndAuthorityPaths(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.txt"), []byte("synthetic evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	body, err := readSafeWorkspaceArtifact(root, filepath.Join(root, "report.txt"))
	if err != nil || string(body) != "synthetic evidence" {
		t.Fatalf("valid report rejected: %q / %v", body, err)
	}
	for _, name := range []string{".git", ".aws", ".ssh", ".local", ".config"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, name, "report.txt")
		if err := os.WriteFile(path, []byte("synthetic restricted"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSafeWorkspaceArtifact(root, path); err == nil {
			t.Fatalf("authority path %s accepted", name)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "report.txt"), []byte("synthetic outside canary"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "reports")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlink privilege unavailable")
	}
	path := filepath.Join(link, "report.txt")
	if _, err := readSafeWorkspaceArtifact(link, path); err == nil {
		t.Fatal("a symlink evidence root was accepted")
	}
	// Demonstrate why a lexical containment check plus leaf Lstat is insufficient.
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("fixture leaf must appear regular: %v", err)
	}
	if _, err := readSafeWorkspaceArtifact(root, path); err == nil {
		t.Fatal("ancestor symlink escaped workspace")
	}
	artifacts, err := collectQAArtifacts(root, []string{"reports/*.txt"})
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("collector exposed outside canary: %#v / %v", artifacts, err)
	}
}
