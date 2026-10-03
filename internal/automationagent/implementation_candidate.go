package automationagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"events-stocks/internal/evidencejson"
)

func implementationCandidateFiles(response []byte) (map[string][]byte, error) {
	if len(response) > 65536 {
		return nil, fmt.Errorf("implementation response exceeds 64 KiB")
	}
	if err := evidencejson.Validate(response); err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(response, &envelope); err != nil || len(envelope) != 1 || envelope["changes"] == nil {
		return nil, fmt.Errorf("implementation response requires only changes")
	}
	var changes []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["changes"], &changes); err != nil || len(changes) != 2 {
		return nil, fmt.Errorf("implementation response requires exactly two files")
	}
	files := map[string][]byte{}
	for _, change := range changes {
		var path, content string
		if len(change) != 2 || change["path"] == nil || change["content"] == nil || json.Unmarshal(change["path"], &path) != nil || json.Unmarshal(change["content"], &content) != nil {
			return nil, fmt.Errorf("implementation changes require path and content strings")
		}
		if (path != "page.go" && path != "store.go") || files[path] != nil || strings.TrimSpace(content) == "" || strings.ContainsRune(content, 0) {
			return nil, fmt.Errorf("invalid implementation replacement file")
		}
		files[path] = []byte(content)
	}
	files["go.mod"] = bytes.ReplaceAll(implementationPilotModule, []byte("\r\n"), []byte("\n"))
	files["page_test.go"] = bytes.ReplaceAll(implementationPilotOracle, []byte("\r\n"), []byte("\n"))
	return files, nil
}

// ExecuteImplementationPilotResponse materializes validated replacements in an
// owned temporary directory and invokes the fixed isolated oracle. The caller
// must authorize the task and authenticate response provenance separately.
func ExecuteImplementationPilotResponse(ctx context.Context, workspace Workspace, taskID, preparedDigest string, response []byte) (map[string]any, error) {
	files, err := implementationCandidateFiles(response)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "itbem-implementation-candidate-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), content, 0644); err != nil {
			return nil, err
		}
	}
	workspace.Root = root
	result, err := ExecuteImplementationPilotOracle(ctx, workspace, taskID, preparedDigest)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(response)
	result["response_sha256"] = hex.EncodeToString(digest[:])
	return result, nil
}
