package automationagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/uuid"
)

func TestImplementationCandidateResponseRejectsAmbiguousOrForeignFiles(t *testing.T) {
	for _, response := range [][]byte{
		[]byte(`{"changes":[],"changes":[]}`), []byte(`{"Changes":[]}`),
		[]byte(`{"changes":[],"execute":true}`), []byte(`{"changes":null}`),
		[]byte(`{"changes":[{"path":"page_test.go","content":"x"},{"path":"store.go","content":"x"}]}`),
		[]byte(`{"changes":[{"path":"page.go","content":"x"},{"path":"page.go","content":"x"}]}`),
		[]byte(`{"changes":[{"path":"../page.go","content":"x"},{"path":"store.go","content":"x"}]}`),
		[]byte(`{"changes":[{"path":"page.go","content":"\u0000"},{"path":"store.go","content":"x"}]}`),
		[]byte(`{"changes":[{"path":"page.go","content":null},{"path":"store.go","content":"x"}]}`),
		[]byte(`{"changes":[{"path":"page.go","content":"\ud800"},{"path":"store.go","content":"x"}]}`),
		[]byte(`{"changes":[{"path":"page.go","content":"\udc00"},{"path":"store.go","content":"x"}]}`),
		bytes.Repeat([]byte("x"), 65537),
	} {
		if _, err := implementationCandidateFiles(response); err == nil {
			t.Fatalf("invalid response accepted: %.100s", response)
		}
	}
}

func TestImplementationCandidateResponseDockerRoundTrip(t *testing.T) {
	if os.Getenv("ITBEM_DOCKER_SANDBOX_E2E") != "1" {
		t.Skip("set ITBEM_DOCKER_SANDBOX_E2E=1 for candidate execution")
	}
	for _, control := range []string{"fixture", "reference"} {
		workspace, digest := implementationOracleTestWorkspace(t, control)
		changes := []map[string]string{}
		for _, name := range []string{"page.go", "store.go"} {
			content, err := os.ReadFile(filepath.Join(workspace.Root, name))
			if err != nil {
				t.Fatal(err)
			}
			changes = append(changes, map[string]string{"path": name, "content": string(content)})
		}
		response, err := json.Marshal(map[string]any{"changes": changes})
		if err != nil {
			t.Fatal(err)
		}
		task := uuid.Must(uuid.NewV4()).String()
		if _, err := ExecuteImplementationPilotResponse(context.Background(), workspace, task, "sha256:wrong", response); err == nil {
			t.Fatal("rebound package accepted")
		}
		result, err := ExecuteImplementationPilotResponse(context.Background(), workspace, task, digest, response)
		if err != nil {
			t.Fatal(err)
		}
		expected := 0
		if control == "fixture" {
			expected = 1
		}
		responseDigest := sha256.Sum256(response)
		if result["exit_code"] != expected || result["response_sha256"] != hex.EncodeToString(responseDigest[:]) {
			t.Fatalf("unexpected candidate result: %#v", result)
		}
		result["control"] = control
		evidence, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("implementation candidate execution evidence: %s", evidence)
		// The supplied workspace is untouched; execution used its own prepared tree.
		if _, err := os.Stat(filepath.Join(workspace.Root, "page_test.go")); err != nil {
			t.Fatal(err)
		}
	}
}
