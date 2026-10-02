package automationagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/inferencecapability"
	"github.com/gofrs/uuid"
)

type artifactFakeStore struct {
	fakeStore
	objects map[string][]byte
}

func reviewedQAMetadata(t *testing.T, repository, worktree, revision string) map[string]string {
	t.Helper()
	base, err := runLocal(context.Background(), repository, time.Minute, "", "git", "rev-parse", revision)
	if err != nil || base.ExitCode != 0 {
		t.Fatalf("fixture base unavailable: %#v / %v", base, err)
	}
	sha := strings.TrimSpace(base.Output)
	digest, err := worktreeDiffSHA256(context.Background(), worktree, sha, false)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"base_sha": sha, "review_diff_sha256": digest}
}

func TestRunQARejectsSourceChangesAndPreservesPartialEvidence(t *testing.T) {
	root := setupImplementationRepository(t)
	worktree, branch, err := isolatedWorktree(context.Background(), Workspace{Root: root}, "51c0b65f-025f-49d9-963b-06165f280e46")
	if err != nil {
		t.Fatal(err)
	}
	program := "package main\nimport \"os\"\nfunc main() { if err := os.WriteFile(\"README.md\", []byte(\"modified during QA\\n\"), 0600); err != nil { panic(err) } }\n"
	if err := os.WriteFile(filepath.Join(worktree, "mutate.go"), []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	added, err := runLocal(context.Background(), worktree, time.Minute, "", "git", "add", "--intent-to-add", "mutate.go")
	if err != nil || added.ExitCode != 0 {
		t.Fatal("synthetic source staging failed")
	}
	metadata := reviewedQAMetadata(t, root, worktree, "HEAD")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	registry, err := json.Marshal(map[string]any{"repo": map[string]any{"path": root, "validation_commands": [][]string{{"go", "run", "mutate.go"}, {"go", "version"}}}})
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := json.Marshal(map[string]any{"work_item": map[string]string{"preview_url": server.URL}, "change_sets": []any{map[string]any{"repository_ref": "workspace://repo", "branch": branch, "review_type": "local_worktree", "ci_status": "passed", "metadata": metadata}}, "approved_plan": map[string]any{"qa_execution_matrix": []any{map[string]any{"repository_ref": "workspace://repo", "run_validation": true, "run_qa": false, "run_stagehand": false, "collect_evidence": false}}}})
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := RunQA(context.Background(), "synthetic-qa-task", "synthetic-qa-run", delivery, func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return string(registry)
		}
		return ""
	})
	var failure *QAExecutionError
	if !errors.As(err, &failure) {
		t.Fatalf("source change did not fail QA with retained evidence: %#v / %v", result, err)
	}
	runs := result["repository_runs"].([]any)
	if len(runs) != 1 || len(runs[0].(map[string]any)["commands"].([]any)) != 1 || runs[0].(map[string]any)["review_binding"].(map[string]string)["review_diff_sha256"] != metadata["review_diff_sha256"] {
		t.Fatalf("QA executed another command or lost reviewed binding: %#v", result)
	}
}

func (s *artifactFakeStore) PutEncryptedObject(_ context.Context, bucket, key string, body []byte, _ string) error {
	if s.objects == nil {
		s.objects = map[string][]byte{}
	}
	s.objects[bucket+"/"+key] = append([]byte(nil), body...)
	return nil
}

func TestRunQACapturesBoundedRegisteredWorkspaceEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	// The QA orchestrator must be deterministic in unit tests. Real Chromium
	// invocation is exercised by the separately validated default command, but
	// can be unavailable on a Windows CI host because of OS-level GPU policy.
	// This remains an approved `go` screenshot command and produces a genuine
	// minimal PNG through the same bounded artifact path used in production.
	captureProgram := `package main
import (
  "encoding/base64"
  "os"
)
func main() {
  body, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScL92gAAAABJRU5ErkJggg==")
  if len(os.Args) != 3 { os.Exit(2) }
  if err := os.WriteFile(os.Args[2], body, 0600); err != nil { panic(err) }
}`
	if err := os.WriteFile(filepath.Join(root, "capture.go"), []byte(captureProgram), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusOK) }))
	defer server.Close()
	registry := `{"repo":{"path":"` + filepath.ToSlash(root) + `","qa_artifact_patterns":["result.txt"],"qa_screenshot_command":["go","run","capture.go","{preview_url}","{artifact_path}"]}}`
	lookup := func(name string) string {
		if name == "ITBEM_AI_WORKSPACES_JSON" {
			return registry
		}
		return ""
	}
	delivery := []byte(`{"work_item":{"preview_url":"` + server.URL + `"},"context_sources":[{"kind":"repository","reference":"workspace://repo"}]}`)
	result, artifacts, err := RunQA(context.Background(), "task", "run", delivery, lookup)
	if err != nil || result["preview"].(map[string]any)["passed"] != true || len(artifacts) != 2 {
		t.Fatalf("unexpected QA result: %#v / %#v / %v", result, artifacts, err)
	}
	if artifacts[1].Name != "repo-preview-desktop.png" {
		t.Fatalf("screenshot evidence must retain a distinguishable gallery name: %#v", artifacts)
	}
	store := &artifactFakeStore{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, &fakeCallback{}, fakeProvider{})
	if err != nil {
		t.Fatal(err)
	}
	uploaded, references, err := worker.uploadArtifacts(context.Background(), "task", "d4a4b837-2e18-43af-9f58-6d59629db2bb", result, artifacts)
	if err != nil || len(store.objects) != 2 || len(uploaded["artifacts"].([]map[string]any)) != 2 || len(references) != 2 {
		t.Fatalf("unexpected uploaded artifacts: %#v / %#v / %#v / %v", uploaded, references, store.objects, err)
	}
	for _, reference := range references {
		if len(reference.SHA256) != 64 {
			t.Fatalf("uploaded QA evidence must carry a SHA-256 digest: %#v", reference)
		}
	}
}

func TestRunQAMultiRepositoryTraversalUsesDependencyOrderAndPrivateEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusOK) }))
	defer server.Close()

	// Keep this topology test hermetic. Browser availability is covered by the
	// dedicated default-command checks; this test is about dependency order and
	// private evidence separation and therefore uses the same approved fixture
	// screenshot contract as the bounded workspace test above.
	captureProgram := `package main
import (
  "encoding/base64"
  "os"
)
func main() {
  body, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScL92gAAAABJRU5ErkJggg==")
  if len(os.Args) != 3 { os.Exit(2) }
  if err := os.WriteFile(os.Args[2], body, 0600); err != nil { panic(err) }
}`
	workspaceIDs := []string{"api", "dashboard"}
	roots := make(map[string]string, len(workspaceIDs))
	branches := make(map[string]string, len(workspaceIDs))
	for _, id := range workspaceIDs {
		root := t.TempDir()
		roots[id] = root
		for _, command := range [][]string{{"git", "init"}, {"git", "config", "user.email", "qa@example.invalid"}, {"git", "config", "user.name", "ITBEM QA"}} {
			result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("git setup for %s failed: %#v / %v", id, result, err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte(id+" evidence\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "capture.go"), []byte(captureProgram), 0600); err != nil {
			t.Fatal(err)
		}
		for _, command := range [][]string{{"git", "add", "result.txt"}, {"git", "commit", "-m", "initial"}} {
			result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("git commit setup for %s failed: %#v / %v", id, result, err)
			}
		}
		worktree, branch, err := isolatedWorktree(context.Background(), Workspace{Root: root}, "b5b5b837-2e18-43af-9f58-6d59629db2bb")
		if id == "dashboard" {
			// A repository-specific task identity is required so the two worktrees
			// remain isolated even when they share one QA traversal.
			worktree, branch, err = isolatedWorktree(context.Background(), Workspace{Root: root}, "c6c6c837-2e18-43af-9f58-6d59629db2cc")
		}
		if err != nil || worktree == "" || branch == "" {
			t.Fatalf("reviewed worktree for %s was not created: %s / %v", id, worktree, err)
		}
		branches[id] = branch
		if err := os.WriteFile(filepath.Join(worktree, "result.txt"), []byte(id+" reviewed evidence\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	registryEntries := map[string]any{}
	for _, id := range workspaceIDs {
		registryEntries[id] = map[string]any{
			"path":                  roots[id],
			"validation_commands":   [][]string{{"go", "version"}},
			"qa_commands":           [][]string{{"go", "env", "GOMOD"}},
			"qa_artifact_patterns":  []string{"result.txt"},
			"qa_screenshot_command": []string{"go", "run", "capture.go", "{preview_url}", "{artifact_path}"},
		}
	}
	registryBytes, err := json.Marshal(registryEntries)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(name string) string {
		if name == "ITBEM_AI_WORKSPACES_JSON" {
			return string(registryBytes)
		}
		return ""
	}
	delivery := map[string]any{
		"work_item": map[string]any{"preview_url": server.URL},
		"context_sources": []any{
			map[string]any{"kind": "repository", "reference": "workspace://api"},
			map[string]any{"kind": "repository", "reference": "workspace://dashboard"},
		},
		"repository_topology": []any{
			map[string]any{"reference": "workspace://api", "depends_on": []string{}},
			map[string]any{"reference": "workspace://dashboard", "depends_on": []string{"workspace://api"}},
		},
		"change_sets": []any{
			map[string]any{"repository_ref": "workspace://dashboard", "branch": branches["dashboard"], "review_type": "local_worktree", "ci_status": "passed", "metadata": reviewedQAMetadata(t, roots["dashboard"], filepath.Join(roots["dashboard"], ".itbem-agent-worktrees", strings.TrimPrefix(branches["dashboard"], "itbem-agent/")), "HEAD")},
			map[string]any{"repository_ref": "workspace://api", "branch": branches["api"], "review_type": "local_worktree", "ci_status": "passed", "metadata": reviewedQAMetadata(t, roots["api"], filepath.Join(roots["api"], ".itbem-agent-worktrees", strings.TrimPrefix(branches["api"], "itbem-agent/")), "HEAD")},
		},
		"approved_plan": map[string]any{"qa_execution_matrix": []any{
			map[string]any{"repository_ref": "workspace://api", "run_validation": true, "run_qa": true, "run_stagehand": false, "collect_evidence": true},
			map[string]any{"repository_ref": "workspace://dashboard", "run_validation": true, "run_qa": true, "run_stagehand": false, "collect_evidence": true},
		}},
	}
	deliveryBytes, err := json.Marshal(delivery)
	if err != nil {
		t.Fatal(err)
	}
	result, artifacts, err := RunQA(context.Background(), "d7d7d837-2e18-43af-9f58-6d59629db2dd", "run", deliveryBytes, lookup)
	if err != nil {
		t.Fatal(err)
	}
	order, ok := result["repository_execution_order"].([]string)
	if !ok || len(order) != 2 || order[0] != "workspace://api" || order[1] != "workspace://dashboard" {
		t.Fatalf("QA must execute repositories in frozen dependency order: %#v", result["repository_execution_order"])
	}
	runs, ok := result["repository_runs"].([]any)
	if !ok || len(runs) != 2 {
		t.Fatalf("QA must retain one run record per repository: %#v", result["repository_runs"])
	}
	if len(artifacts) != 2 || artifacts[0].Name != "api-result.txt" || artifacts[1].Name != "dashboard-result.txt" {
		t.Fatalf("multi-repository evidence must remain private and distinguishable: %#v", artifacts)
	}
}

func TestQAScreenshotArtifactSlotsReserveVisualEvidence(t *testing.T) {
	if got := qaScreenshotArtifactSlots(nil); got != 2 {
		t.Fatalf("default screenshot harness must reserve desktop and mobile slots, got %d", got)
	}
	if got := qaScreenshotArtifactSlots([]string{"go", "run", "capture.go", "{preview_url}", "{artifact_path}"}); got != 1 {
		t.Fatalf("pinned workspace harness must reserve its evidence slot, got %d", got)
	}
	if got := qaSemanticArtifactSlots([]string{"node", "stagehand.mjs", "--url", "{preview_url}", "--output", "{artifact_path}"}); got != 9 {
		t.Fatalf("semantic QA must reserve report and bounded browser evidence slots, got %d", got)
	}
}

func TestCheckPreviewNavigatesWithSignedURLButReturnsOnlySanitizedURL(t *testing.T) {
	const queryCanary = "qa-preview-query-canary-92b"
	const fragmentCanary = "qa-preview-fragment-canary-71c"
	var observedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		observedQuery = request.URL.RawQuery
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	previewURL := server.URL + "/preview?token=" + queryCanary + "#" + fragmentCanary

	result := checkPreview(context.Background(), previewURL)
	if observedQuery != "token="+queryCanary {
		t.Fatalf("preview probe must navigate with the complete query credential, got %q", observedQuery)
	}
	if got := result["url"]; got != server.URL+"/preview" {
		t.Fatalf("preview result must contain only the safe URL, got %#v", got)
	}
	if strings.Contains(fmt.Sprint(result), queryCanary) || strings.Contains(fmt.Sprint(result), fragmentCanary) {
		t.Fatalf("preview result leaked a signed URL component: %#v", result)
	}
}

func TestSanitizePreviewReportRedactsCredentialsAndDropsPrivateReasoning(t *testing.T) {
	const (
		urlQueryCanary      = "REPORT_URL_QUERY_CANARY"
		apiKeyCanary        = "REPORT_API_KEY_CANARY"
		cookieCanary        = "REPORT_COOKIE_CANARY"
		refreshTokenCanary  = "REPORT_REFRESH_TOKEN_CANARY"
		authorizationCanary = "REPORT_AUTHORIZATION_CANARY"
		providerTokenCanary = "sk-or-v1-REPORT_PROVIDER_TOKEN_CANARY_1234567890"
		jwtCanary           = "eyJREPORTHEADER123456.eyJREPORTPAYLOAD123456.REPORTSIGNATURE123456" // gitleaks:allow synthetic test-only security canary; never used for provider access
		analysisCanary      = "PRIVATE_ANALYSIS_CANARY"
		reasoningCanary     = "PRIVATE_REASONING_CANARY"
		chainCanary         = "PRIVATE_CHAIN_OF_THOUGHT_CANARY"
	)
	body := []byte(`{"tool":"probe","summary":"SAFE_SUMMARY_CANARY","status":"passed","preview_url":"https://preview.example.test/login?token=` + urlQueryCanary + `","credentials":{"provider_api_key":"` + apiKeyCanary + `","cookie":"` + cookieCanary + `","refresh_token":"` + refreshTokenCanary + `"},"nested":{"Authorization":"Bearer ` + authorizationCanary + `","request":"Authorization: Bearer TEXT_BEARER_CANARY ` + providerTokenCanary + ` ` + jwtCanary + `","analysis":"` + analysisCanary + `","reasoning_content":"` + reasoningCanary + `","chain-of-thought":"` + chainCanary + `","reasoning_summary":"SAFE_REASONING_SUMMARY_CANARY","evidence":{"status":"captured","summary":"SAFE_EVIDENCE_SUMMARY_CANARY"}}}`)
	sanitized, report := sanitizePreviewReport(body, "https://preview.example.test/login")
	if report == nil {
		t.Fatal("expected a structured sanitized report")
	}
	for _, forbidden := range []string{urlQueryCanary, apiKeyCanary, cookieCanary, refreshTokenCanary, authorizationCanary, "TEXT_BEARER_CANARY", providerTokenCanary, jwtCanary, analysisCanary, reasoningCanary, chainCanary} {
		if strings.Contains(string(sanitized), forbidden) || strings.Contains(fmt.Sprint(report), forbidden) {
			t.Fatalf("sanitized QA report leaked canary %q: bytes=%s report=%#v", forbidden, sanitized, report)
		}
	}
	if !strings.Contains(string(sanitized), "SAFE_SUMMARY_CANARY") || !strings.Contains(string(sanitized), "SAFE_REASONING_SUMMARY_CANARY") || !strings.Contains(string(sanitized), "SAFE_EVIDENCE_SUMMARY_CANARY") || !strings.Contains(string(sanitized), `"status":"passed"`) || !strings.Contains(string(sanitized), `"status":"captured"`) {
		t.Fatalf("sanitization must preserve summaries, statuses and evidence: %s", sanitized)
	}
	var decoded map[string]any
	if err := json.Unmarshal(sanitized, &decoded); err != nil {
		t.Fatal(err)
	}
	nested := decoded["nested"].(map[string]any)
	for _, privateField := range []string{"analysis", "reasoning_content", "chain-of-thought"} {
		if _, exists := nested[privateField]; exists {
			t.Fatalf("private reasoning field %q must be removed, got %#v", privateField, nested)
		}
	}
	if nested["reasoning_summary"] != "SAFE_REASONING_SUMMARY_CANARY" {
		t.Fatalf("a safe reasoning summary must remain available: %#v", nested)
	}
	command := sanitizePreviewCommand([]string{"qa", "--api-key", "CLI_API_KEY_CANARY", "--password=CLI_INLINE_PASSWORD_CANARY", "sk-proj-CLI_PROVIDER_TOKEN_CANARY_1234567890"}, "")
	if strings.Contains(fmt.Sprint(command), "CLI_API_KEY_CANARY") || strings.Contains(fmt.Sprint(command), "CLI_INLINE_PASSWORD_CANARY") || strings.Contains(fmt.Sprint(command), "CLI_PROVIDER_TOKEN_CANARY") || command[2] != "[REDACTED]" {
		t.Fatalf("command sanitizer leaked a flag value or credential-shaped token: %#v", command)
	}
	freeText := sanitizePreviewText("reasoning: PRIVATE_TEXT_REASONING_CANARY\nsummary: SAFE_TEXT_SUMMARY_CANARY", "")
	if strings.Contains(freeText, "PRIVATE_TEXT_REASONING_CANARY") || !strings.Contains(freeText, "SAFE_TEXT_SUMMARY_CANARY") {
		t.Fatalf("free-form output must remove private reasoning and keep safe summaries: %q", freeText)
	}
}

func TestQASanitizerFixtureProcess(t *testing.T) {
	for index, argument := range os.Args {
		if argument != "--qa-report-path" {
			continue
		}
		if index+1 >= len(os.Args) {
			t.Fatal("missing fixture report path")
		}
		fixture := `{"tool":"probe","summary":"CAPTURE_SAFE_SUMMARY_CANARY","status":"passed","credentials":{"api_key":"CAPTURE_REPORT_API_KEY_CANARY","session":"CAPTURE_SESSION_CANARY"},"details":{"chain_of_thought":"CAPTURE_PRIVATE_COT_CANARY","thinking":"CAPTURE_PRIVATE_THINKING_CANARY","reasoning_summary":"CAPTURE_SAFE_REASONING_SUMMARY_CANARY","evidence":{"status":"captured","note":"Authorization: Bearer CAPTURE_TEXT_BEARER_CANARY sk-or-v1-CAPTURE_PROVIDER_TOKEN_CANARY_1234567890 eyJCAPTUREHEADER123456.eyJCAPTUREPAYLOAD123456.CAPTURESIGNATURE123456"}},"preview_url":"https://preview.example.test/home?token=CAPTURE_URL_QUERY_CANARY"}` // gitleaks:allow synthetic test-only security canary; never used for provider access
		if err := os.WriteFile(os.Args[index+1], []byte(fixture), 0600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "access_token=CAPTURE_OUTPUT_TOKEN_CANARY Authorization: Bearer CAPTURE_OUTPUT_BEARER_CANARY sk-proj-CAPTURE_OUTPUT_PROVIDER_TOKEN_CANARY_1234567890 reasoning: CAPTURE_OUTPUT_PRIVATE_REASONING_CANARY")
		return
	}
}

func TestCaptureSemanticQAStoresOnlySanitizedReportAndSummary(t *testing.T) {
	root := t.TempDir()
	workspace := Workspace{ID: "repo", Root: root, Config: WorkspaceConfig{SandboxRuntime: WorkspaceSandboxProcess}}
	command := []string{
		os.Args[0], "-test.v", "-test.run=^TestQASanitizerFixtureProcess$", "--",
		"--api-key", "CAPTURE_COMMAND_FLAG_CANARY", "--password=CAPTURE_COMMAND_INLINE_CANARY",
		"--qa-report-path", "{artifact_path}", "--preview-url", "{preview_url}",
	}
	result, artifacts, err := captureSemanticQA(context.Background(), "sanitize-task", "sanitize-run", "https://preview.example.test/home", json.RawMessage(`{"approved_plan":{}}`), workspace, root, command, func(string) string { return "" })
	if err != nil {
		t.Fatalf("synthetic semantic QA fixture failed: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].ContentType != "application/json" {
		t.Fatalf("expected one sanitized JSON report artifact, got %#v", artifacts)
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(root, ".itbem-agent-evidence", "sanitize-task", "semantic-qa.json")
	persistedBytes, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{string(artifacts[0].Body), string(persistedBytes), string(resultBytes)} {
		for _, forbidden := range []string{
			"CAPTURE_REPORT_API_KEY_CANARY", "CAPTURE_SESSION_CANARY", "CAPTURE_PRIVATE_COT_CANARY", "CAPTURE_PRIVATE_THINKING_CANARY",
			"CAPTURE_TEXT_BEARER_CANARY", "CAPTURE_PROVIDER_TOKEN_CANARY", "CAPTUREHEADER123456", "CAPTUREPAYLOAD123456", "CAPTURESIGNATURE123456",
			"CAPTURE_URL_QUERY_CANARY", "CAPTURE_OUTPUT_TOKEN_CANARY", "CAPTURE_OUTPUT_BEARER_CANARY", "CAPTURE_OUTPUT_PROVIDER_TOKEN_CANARY",
			"CAPTURE_OUTPUT_PRIVATE_REASONING_CANARY", "CAPTURE_COMMAND_FLAG_CANARY", "CAPTURE_COMMAND_INLINE_CANARY",
		} {
			if strings.Contains(payload, forbidden) {
				t.Fatalf("semantic QA persisted or returned forbidden canary %q: %s", forbidden, payload)
			}
		}
	}
	if string(artifacts[0].Body) != string(persistedBytes) {
		t.Fatalf("on-disk synthetic report must match the sanitized artifact bytes: disk=%s artifact=%s", persistedBytes, artifacts[0].Body)
	}
	if !strings.Contains(string(persistedBytes), "CAPTURE_SAFE_SUMMARY_CANARY") || !strings.Contains(string(persistedBytes), "CAPTURE_SAFE_REASONING_SUMMARY_CANARY") || !strings.Contains(string(persistedBytes), `"status":"passed"`) || !strings.Contains(string(persistedBytes), `"status":"captured"`) {
		t.Fatalf("sanitizer must preserve permitted summary/status/evidence: %s", persistedBytes)
	}
	if !strings.Contains(string(resultBytes), "CAPTURE_SAFE_SUMMARY_CANARY") || !strings.Contains(string(resultBytes), "CAPTURE_SAFE_REASONING_SUMMARY_CANARY") {
		t.Fatalf("returned result must retain safe summaries: %s", resultBytes)
	}
	if commandResult, ok := result["command"].([]string); !ok || commandResult[5] != "[REDACTED]" || strings.Contains(strings.Join(commandResult, " "), "CAPTURE_COMMAND_INLINE_CANARY") {
		t.Fatalf("returned command must redact separated and inline credentials: %#v", result["command"])
	}
}

func TestStagehandSignedPreviewUsesPrivateURLFileAndSanitizesReturnedEvidence(t *testing.T) {
	installGatewayTestCapability(t, "task", "run", inferencecapability.OperationDeliveryQA)
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is unavailable")
	}
	node := "node"
	const queryCanary = "qa-preview-query-canary-31d"
	const fragmentCanary = "qa-preview-fragment-canary-84f"
	previewURL := "https://preview.example.test/review?token=" + queryCanary + "#" + fragmentCanary
	root := t.TempDir()
	runner := filepath.Join(root, "itbem-events-backend", "tools", "stagehand-qa", "run.mjs")
	if err := os.MkdirAll(filepath.Dir(runner), 0700); err != nil {
		t.Fatal(err)
	}
	program := `import fs from "node:fs";
import path from "node:path";
const args = process.argv.slice(2);
const urlFile = args[args.indexOf("--url-file") + 1];
const output = args[args.indexOf("--output") + 1];
if (!urlFile || !output) process.exit(2);
const navigatedURL = fs.readFileSync(urlFile, "utf8");
fs.writeFileSync(path.join(process.cwd(), "observed-navigation-url.txt"), navigatedURL);
fs.writeFileSync(path.join(process.cwd(), "observed-argv.json"), JSON.stringify(args));
fs.writeFileSync(output, JSON.stringify({ tool: "probe", preview_url: navigatedURL, request: { url: navigatedURL }, argv: args }));
process.stdout.write(navigatedURL);
`
	if err := os.WriteFile(runner, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	delivery := json.RawMessage(`{"approved_plan":{}}`)
	lookup := func(name string) string {
		switch name {
		case "ITBEM_AI_GATEWAY_URL":
			return "https://gateway.example.test/api/internal/automation/inference"
		case "AUTOMATION_CALLBACK_SECRET":
			return "test-callback-secret"
		default:
			return ""
		}
	}
	workspace := Workspace{ID: "repo", Root: root, Config: WorkspaceConfig{SandboxRuntime: WorkspaceSandboxProcess}}
	result, artifacts, err := captureSemanticQA(context.Background(), "task", "run", previewURL, delivery, workspace, root,
		[]string{node, runner, "--url", "{preview_url}", "--output", "{artifact_path}"}, lookup)
	if err != nil {
		t.Fatalf("pinned Stagehand should execute with a private URL file: %v", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(root, "observed-navigation-url.txt")); readErr != nil || string(got) != previewURL {
		t.Fatalf("Stagehand navigation must receive the complete URL, got %q / %v", got, readErr)
	}
	argv, err := os.ReadFile(filepath.Join(root, "observed-argv.json"))
	if err != nil {
		t.Fatal(err)
	}
	semanticJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{queryCanary, fragmentCanary} {
		if strings.Contains(string(argv), sensitive) || strings.Contains(string(semanticJSON), sensitive) {
			t.Fatalf("signed URL canary leaked into Stagehand argv/result: argv=%s result=%s", argv, semanticJSON)
		}
	}
	command, ok := result["command"].([]string)
	if !ok || len(command) == 0 || !containsString(command, "--url-file") {
		t.Fatalf("reported Stagehand command must use --url-file: %#v", result["command"])
	}
	for _, artifact := range artifacts {
		if strings.Contains(string(artifact.Body), queryCanary) || strings.Contains(string(artifact.Body), fragmentCanary) {
			t.Fatalf("signed URL canary leaked into a persisted QA artifact %s: %s", artifact.Name, artifact.Body)
		}
	}
	if len(artifacts) != 1 || artifacts[0].ContentType != "application/json" {
		t.Fatalf("test runner should produce one JSON artifact: %#v", artifacts)
	}
	var persisted map[string]any
	if err := json.Unmarshal(artifacts[0].Body, &persisted); err != nil {
		t.Fatalf("sanitized report is not valid JSON: %v", err)
	}
	if persisted["preview_url"] != "https://preview.example.test/review" {
		t.Fatalf("persisted report must retain only the safe navigation URL: %#v", persisted)
	}
	urlFileIndex := -1
	for index, argument := range command {
		if argument == "--url-file" {
			urlFileIndex = index
			break
		}
	}
	if urlFileIndex < 0 || urlFileIndex+1 >= len(command) {
		t.Fatalf("Stagehand command is missing the private URL-file argument: %#v", command)
	}
	fileArgument := command[urlFileIndex+1]
	if _, err := os.Stat(fileArgument); !os.IsNotExist(err) {
		t.Fatalf("private URL file must be removed after Stagehand exits; stat err=%v", err)
	}
}

func TestGenericSemanticAndScreenshotRunnersFailClosedForSignedURL(t *testing.T) {
	const queryCanary = "qa-preview-query-canary-55a"
	const fragmentCanary = "qa-preview-fragment-canary-16e"
	previewURL := "https://preview.example.test/review?token=" + queryCanary + "#" + fragmentCanary
	root := t.TempDir()
	marker := filepath.Join(root, "runner-executed")
	program := `package main
import "os"
func main() { _ = os.WriteFile("runner-executed", []byte("yes"), 0600) }
`
	if err := os.WriteFile(filepath.Join(root, "runner.go"), []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := Workspace{ID: "repo", Root: root, Config: WorkspaceConfig{SandboxRuntime: WorkspaceSandboxProcess}}
	semantic, _, semanticErr := captureSemanticQA(context.Background(), "task", "run", previewURL, json.RawMessage(`{"approved_plan":{}}`), workspace, root,
		[]string{"go", "run", "runner.go", "{preview_url}"}, func(string) string { return "" })
	if semanticErr == nil || strings.Contains(fmt.Sprint(semantic), queryCanary) || strings.Contains(semanticErr.Error(), queryCanary) || strings.Contains(semanticErr.Error(), fragmentCanary) {
		t.Fatalf("generic semantic QA must fail closed without exposing the signed URL: %#v / %v", semantic, semanticErr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("generic semantic runner executed before rejecting a signed URL, stat err=%v", err)
	}
	screenshot, _, screenshotErr := captureScreenshotAt(context.Background(), "task", previewURL, workspace, root,
		[]string{"go", "run", "runner.go", "{preview_url}"}, qaScreenshotViewports[0])
	if screenshotErr == nil || strings.Contains(fmt.Sprint(screenshot), queryCanary) || strings.Contains(screenshotErr.Error(), queryCanary) || strings.Contains(screenshotErr.Error(), fragmentCanary) {
		t.Fatalf("custom screenshot runner must fail closed without exposing the signed URL: %#v / %v", screenshot, screenshotErr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("screenshot runner executed before rejecting a signed URL, stat err=%v", err)
	}
	if _, err := defaultScreenshotCommandAt(previewURL, filepath.Join(root, "screenshot.png"), qaScreenshotViewports[0]); err == nil || strings.Contains(err.Error(), queryCanary) || strings.Contains(err.Error(), fragmentCanary) {
		t.Fatalf("default screenshot command must reject signed URLs with a safe error, got %v", err)
	}
}

func TestBrowserQAPlanUsesOnlyApprovedPlanCases(t *testing.T) {
	delivery := []byte(`{"approved_plan":{"browser_qa_mode":"approved_navigation","browser_qa_cases":[{"id":"login-route","title":"Reach login","steps":[{"kind":"navigate","path":"/login"},{"kind":"assert_visible","selector":"form"},{"kind":"click","selector":"a[data-qa=forgot-password]","expected_path":"/forgot-password"}]}]}}`)
	plan, err := browserQAPlan(delivery)
	if err != nil {
		t.Fatal(err)
	}
	if string(plan) == "" || !strings.Contains(string(plan), `"approved_navigation"`) || !strings.Contains(string(plan), `"login-route"`) {
		t.Fatalf("approved browser cases were not compiled: %s", plan)
	}
	if _, err := browserQAPlan([]byte(`{"approved_plan":{"browser_qa_mode":"anything_else"}}`)); err == nil {
		t.Fatal("unsupported browser QA modes must be rejected before a runner starts")
	}
	tooManyCases := map[string]any{"browser_qa_mode": "read_only", "browser_qa_cases": []any{}}
	for index := 0; index < 4; index++ {
		tooManyCases["browser_qa_cases"] = append(tooManyCases["browser_qa_cases"].([]any), map[string]any{
			"id": fmt.Sprintf("case-%d", index+1), "title": "Bounded evidence", "steps": []any{map[string]any{"kind": "navigate", "path": "/login"}},
		})
	}
	if err := ValidateApprovedBrowserQAPlan(tooManyCases); err == nil {
		t.Fatal("more than three browser QA cases would exceed the reserved visual evidence capacity")
	}
}

func TestApprovedTestFlowUsesOnlyNamedTestValuesAndObservedAssertions(t *testing.T) {
	valid := map[string]any{
		"browser_qa_mode": "approved_test_flow",
		"browser_qa_cases": []any{map[string]any{
			"id": "operator-login", "title": "Test operator reaches the workspace",
			"steps": []any{
				map[string]any{"kind": "navigate", "path": "/login"},
				map[string]any{"kind": "fill", "selector": "input[type=email]", "value_env": "ITBEM_QA_LOGIN_EMAIL"},
				map[string]any{"kind": "fill", "selector": "input[type=password]", "value_env": "ITBEM_QA_LOGIN_PASSWORD"},
				map[string]any{"kind": "click", "selector": "button[type=submit]"},
				map[string]any{"kind": "assert_path", "path": "/"},
			},
		}},
	}
	if err := ValidateApprovedBrowserQAPlan(valid); err != nil {
		t.Fatalf("expected reviewed test flow to be accepted: %v", err)
	}
	steps := valid["browser_qa_cases"].([]any)[0].(map[string]any)["steps"].([]any)
	steps[1].(map[string]any)["value_env"] = "literal-password"
	if err := ValidateApprovedBrowserQAPlan(valid); err == nil {
		t.Fatal("literal browser test values must be rejected")
	}
	steps[1].(map[string]any)["value_env"] = "ITBEM_QA_LOGIN_EMAIL"
	steps = steps[:4]
	valid["browser_qa_cases"].([]any)[0].(map[string]any)["steps"] = steps
	if err := ValidateApprovedBrowserQAPlan(valid); err == nil {
		t.Fatal("a test-flow click without a following assertion must be rejected")
	}
}

func TestBrowserQATestEnvironmentExposesOnlyApprovedReferences(t *testing.T) {
	delivery := []byte(`{"approved_plan":{"browser_qa_mode":"approved_test_flow","browser_qa_cases":[{"id":"login","title":"Login","steps":[{"kind":"fill","selector":"input[type=email]","value_env":"ITBEM_QA_LOGIN_EMAIL"},{"kind":"fill","selector":"input[type=password]","value_env":"ITBEM_QA_LOGIN_PASSWORD"}]}]}}`)
	environment, err := browserQATestEnvironment(delivery, func(name string) string {
		switch name {
		case "ITBEM_QA_LOGIN_EMAIL":
			return "qa@example.test"
		case "ITBEM_QA_LOGIN_PASSWORD":
			return "test-value"
		default:
			return ""
		}
	})
	if err != nil || len(environment) != 2 || environment["ITBEM_QA_LOGIN_EMAIL"] != "qa@example.test" || environment["ITBEM_QA_LOGIN_PASSWORD"] != "test-value" {
		t.Fatalf("approved Stagehand test flow must receive only its named values: %#v / %v", environment, err)
	}
	if _, err := browserQATestEnvironment(delivery, func(string) string { return "" }); err == nil {
		t.Fatal("a configured test flow must fail closed when an approved value is missing")
	}
}

func TestApprovedTestFlowCannotPassValuesToAnUnpinnedQACommand(t *testing.T) {
	delivery := []byte(`{"approved_plan":{"browser_qa_mode":"approved_test_flow","browser_qa_cases":[{"id":"login","title":"Login","steps":[{"kind":"fill","selector":"input[type=email]","value_env":"ITBEM_QA_LOGIN_EMAIL"}]}]}}`)
	_, _, err := captureSemanticQA(
		context.Background(), "task", "run", "http://127.0.0.1:3000/login", delivery, Workspace{ID: "test", Root: t.TempDir(), Config: WorkspaceConfig{SandboxRuntime: WorkspaceSandboxProcess}}, t.TempDir(),
		[]string{"go", "run", "not-stagehand.go", "{preview_url}", "{artifact_path}"},
		func(name string) string {
			if name == "ITBEM_QA_LOGIN_EMAIL" {
				return "qa@example.test"
			}
			return ""
		},
	)
	if err == nil || !strings.Contains(err.Error(), "pinned Stagehand") {
		t.Fatalf("a generic repository command must never receive browser test values: %v", err)
	}
}

func TestApprovedQAExecutionPoliciesRequireCompleteEvidenceContract(t *testing.T) {
	valid := []byte(`{"approved_plan":{"qa_execution_matrix":[{"repository_ref":"workspace://backend","run_validation":true,"run_qa":true,"run_stagehand":false,"collect_evidence":true},{"repository_ref":"workspace://dashboard","run_validation":false,"run_qa":true,"run_stagehand":true,"collect_evidence":true}]}}`)
	policies, configured, err := approvedQAExecutionPolicies(valid)
	if err != nil || !configured || !policies["workspace://backend"].RunValidation || !policies["workspace://dashboard"].RunStagehand {
		t.Fatalf("expected reviewable QA execution policies: %#v / %v / %v", policies, configured, err)
	}
	invalid := []byte(`{"approved_plan":{"qa_execution_matrix":[{"repository_ref":"workspace://dashboard","run_validation":false,"run_qa":true,"run_stagehand":true,"collect_evidence":false}]}}`)
	if _, _, err := approvedQAExecutionPolicies(invalid); err == nil {
		t.Fatal("Stagehand without retained evidence must be rejected before QA starts")
	}
}

func TestValidateApprovedBrowserQAPlanRejectsUnsafeOrUnapprovedSteps(t *testing.T) {
	valid := map[string]any{
		"browser_qa_mode": "approved_navigation",
		"browser_qa_cases": []any{map[string]any{
			"id": "login-route", "title": "Reach login",
			"steps": []any{
				map[string]any{"kind": "navigate", "path": "/login"},
				map[string]any{"kind": "assert_visible", "selector": "form"},
				map[string]any{"kind": "click", "selector": "a[data-qa=forgot-password]", "expected_path": "/forgot-password"},
			},
		}},
	}
	if err := ValidateApprovedBrowserQAPlan(valid); err != nil {
		t.Fatalf("valid browser QA plan rejected: %v", err)
	}
	valid["browser_qa_mode"] = "read_only"
	if err := ValidateApprovedBrowserQAPlan(valid); err == nil {
		t.Fatal("a click must require approved navigation mode")
	}
	valid["browser_qa_mode"] = "approved_navigation"
	valid["browser_qa_cases"].([]any)[0].(map[string]any)["steps"].([]any)[0].(map[string]any)["path"] = "https://outside.example"
	if err := ValidateApprovedBrowserQAPlan(valid); err == nil {
		t.Fatal("cross-origin browser navigation must be rejected")
	}
}

func TestRunQAAttachesConfiguredSemanticReportAndScreenshot(t *testing.T) {
	root := t.TempDir()
	semanticProgram := `package main
import (
  "encoding/base64"
  "os"
  "path/filepath"
)
func main() {
  if len(os.Args) != 3 { os.Exit(2) }
  if err := os.WriteFile(os.Args[2], []byte("{\"verdict\":\"passed\",\"summary\":\"semantic smoke completed\"}"), 0600); err != nil { panic(err) }
  body, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScL92gAAAABJRU5ErkJggg==")
  if err := os.WriteFile(filepath.Join(filepath.Dir(os.Args[2]), "semantic-qa.png"), body, 0600); err != nil { panic(err) }
}`
	if err := os.WriteFile(filepath.Join(root, "semantic.go"), []byte(semanticProgram), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusOK) }))
	defer server.Close()
	registry := `{"repo":{"path":"` + filepath.ToSlash(root) + `","qa_semantic_command":["go","run","semantic.go","{preview_url}","{artifact_path}"]}}`
	lookup := func(name string) string {
		if name == "ITBEM_AI_WORKSPACES_JSON" {
			return registry
		}
		return ""
	}
	delivery := []byte(`{"work_item":{"preview_url":"` + server.URL + `"},"context_sources":[{"kind":"repository","reference":"workspace://repo"}]}`)
	result, artifacts, err := RunQA(context.Background(), "task", "run", delivery, lookup)
	if err != nil {
		t.Fatal(err)
	}
	semantic, ok := result["semantic"].(map[string]any)
	if !ok || semantic["passed"] != true || len(artifacts) < 2 {
		t.Fatalf("semantic result was not retained: %#v / %#v", result, artifacts)
	}
	if artifacts[0].Name != "repo-semantic-qa.json" || artifacts[1].Name != "repo-semantic-qa.png" {
		t.Fatalf("semantic evidence must be a report followed by its screenshot: %#v", artifacts)
	}
}

func TestRunQARefreshesCapabilityAfterRepositoryCommandsBeforeStagehand(t *testing.T) {
	fixture := newStagehandCapabilityRefreshFixture(t)
	initialCapability := installGatewayTestCapability(t, fixture.taskID, fixture.runID, inferencecapability.OperationDeliveryQA)
	var refreshedCapability string
	_, _, err := runQAWithCapabilityRefresh(context.Background(), fixture.taskID, fixture.runID, fixture.delivery, fixture.lookup, func(context.Context) (bool, error) {
		refreshedCapability = installGatewayTestCapability(t, fixture.taskID, fixture.runID, inferencecapability.OperationDeliveryQA)
		if refreshedCapability == initialCapability {
			t.Fatal("refresh must issue a new run capability")
		}
		return true, appendRefreshOrder(fixture.orderPath)
	})
	if err != nil {
		t.Fatalf("QA with a refreshed Stagehand capability failed: %v", err)
	}
	order, err := os.ReadFile(fixture.orderPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(order), "refresh\nqa-command\nrefresh\nstagehand\n"; got != want {
		t.Fatalf("capability must refresh after repository QA and immediately before Stagehand; got %q, want %q", got, want)
	}
	observedCapability, err := os.ReadFile(fixture.capabilityPath)
	if err != nil || string(observedCapability) != refreshedCapability {
		t.Fatalf("Stagehand did not receive the freshly issued capability: got %q, want %q, err=%v", observedCapability, refreshedCapability, err)
	}
}

func TestRunQADoesNotStartStagehandWhenCapabilityRefreshFailsOrIsRejected(t *testing.T) {
	tests := []struct {
		name     string
		accepted bool
		cause    error
	}{
		{name: "claim rejected"},
		{name: "refresh request failed", accepted: true, cause: errors.New("callback unavailable")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStagehandCapabilityRefreshFixture(t)
			installGatewayTestCapability(t, fixture.taskID, fixture.runID, inferencecapability.OperationDeliveryQA)
			refreshCalls := 0
			_, _, err := runQAWithCapabilityRefresh(context.Background(), fixture.taskID, fixture.runID, fixture.delivery, fixture.lookup, func(context.Context) (bool, error) {
				refreshCalls++
				if err := appendRefreshOrder(fixture.orderPath); err != nil {
					return false, err
				}
				if refreshCalls == 1 {
					return true, nil
				}
				return test.accepted, test.cause
			})
			var refreshErr *qaCapabilityRefreshError
			if !errors.As(err, &refreshErr) {
				t.Fatalf("refresh failure must be distinguished from a QA failure, got %v", err)
			}
			if test.cause == nil {
				if !errors.Is(err, errQACapabilityNotAccepted) {
					t.Fatalf("a rejected claim must fail closed, got %v", err)
				}
			} else if !errors.Is(err, test.cause) {
				t.Fatalf("refresh error must be preserved for retry, got %v", err)
			}
			order, readErr := os.ReadFile(fixture.orderPath)
			if readErr != nil || string(order) != "refresh\nqa-command\nrefresh\n" {
				t.Fatalf("Stagehand must not start after a failed/rejected refresh: order=%q err=%v", order, readErr)
			}
			if _, statErr := os.Stat(fixture.capabilityPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("Stagehand must not write output after refresh failure: stat err=%v", statErr)
			}
		})
	}
}

type stagehandCapabilityRefreshFixture struct {
	taskID         string
	runID          string
	delivery       json.RawMessage
	lookup         func(string) string
	orderPath      string
	capabilityPath string
}

func TestQAStopsBetweenCommandsWhenTaskAuthorityIsRevoked(t *testing.T) {
	fixture := newStagehandCapabilityRefreshFixture(t)
	var registry map[string]WorkspaceConfig
	if err := json.Unmarshal([]byte(fixture.lookup("ITBEM_AI_WORKSPACES_JSON")), &registry); err != nil {
		t.Fatal(err)
	}
	for id, config := range registry {
		if len(config.QACommands) != 1 {
			t.Fatal("fixture must contain exactly one QA command")
		}
		config.QACommands = append(config.QACommands, config.QACommands[0])
		registry[id] = config
	}
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return string(raw)
		}
		return fixture.lookup(key)
	}
	calls := 0
	result, _, err := runQAWithCapabilityRefresh(context.Background(), fixture.taskID, fixture.runID, fixture.delivery, lookup, func(context.Context) (bool, error) {
		calls++
		if err := appendRefreshOrder(fixture.orderPath); err != nil {
			return false, err
		}
		return calls == 1, nil
	})
	if !errors.Is(err, errQACapabilityNotAccepted) {
		t.Fatalf("revocation was not propagated: %v", err)
	}
	order, readErr := os.ReadFile(fixture.orderPath)
	if readErr != nil || string(order) != "refresh\nqa-command\nrefresh\n" || calls != 2 {
		t.Fatalf("another command ran after revocation: %q / %v", order, readErr)
	}
	runs := result["repository_runs"].([]any)
	if len(runs) != 1 || len(runs[0].(map[string]any)["commands"].([]any)) != 1 {
		t.Fatalf("partial execution evidence lost: %#v", result)
	}
}

func newStagehandCapabilityRefreshFixture(t *testing.T) stagehandCapabilityRefreshFixture {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is unavailable")
	}
	root := t.TempDir()
	runner := filepath.Join(root, "itbem-events-backend", "tools", "stagehand-qa", "run.mjs")
	if err := os.MkdirAll(filepath.Dir(runner), 0700); err != nil {
		t.Fatal(err)
	}
	const stagehandProgram = `import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../../");
const args = process.argv.slice(2);
const output = args[args.indexOf("--output") + 1];
const capability = process.env.STAGEHAND_QA_INFERENCE_CAPABILITY || "";
if (!output || !capability) process.exit(2);
fs.appendFileSync(path.join(root, "order.txt"), "stagehand\n");
fs.writeFileSync(path.join(root, "capability.txt"), capability);
fs.writeFileSync(output, JSON.stringify({ tool: "refresh-test", verdict: "passed" }));
fs.writeFileSync(output.replace(/\.json$/, ".png"), Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScL92gAAAABJRU5ErkJggg==", "base64"));
`
	if err := os.WriteFile(runner, []byte(stagehandProgram), 0600); err != nil {
		t.Fatal(err)
	}
	qaCommand := filepath.Join(root, "qa-before.go")
	const qaProgram = `package main
import (
  "os"
)
func main() {
  file, err := os.OpenFile("order.txt", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
  if err != nil { panic(err) }
  if _, err := file.WriteString("qa-command\n"); err != nil { panic(err) }
  if err := file.Close(); err != nil { panic(err) }
}`
	if err := os.WriteFile(qaCommand, []byte(qaProgram), 0600); err != nil {
		t.Fatal(err)
	}
	registryBytes, err := json.Marshal(map[string]any{
		"repo": map[string]any{
			"path":                root,
			"qa_commands":         [][]string{{"go", "run", qaCommand}},
			"qa_semantic_command": []string{node, runner, "--url", "{preview_url}", "--output", "{artifact_path}"},
			"sandbox_runtime":     WorkspaceSandboxProcess,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusOK) }))
	t.Cleanup(server.Close)
	lookup := func(name string) string {
		switch name {
		case "ITBEM_AI_WORKSPACES_JSON":
			return string(registryBytes)
		case "ITBEM_AI_GATEWAY_URL":
			return "https://gateway.example.test/api/internal/automation/inference"
		default:
			return ""
		}
	}
	taskID, runID := "qa-capability-refresh-task", "qa-capability-refresh-run"
	delivery := json.RawMessage(`{"work_item":{"preview_url":"` + server.URL + `"},"context_sources":[{"kind":"repository","reference":"workspace://repo"}]}`)
	return stagehandCapabilityRefreshFixture{
		taskID: taskID, runID: runID, delivery: delivery, lookup: lookup,
		orderPath: filepath.Join(root, "order.txt"), capabilityPath: filepath.Join(root, "capability.txt"),
	}
}

func appendRefreshOrder(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString("refresh\n"); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func TestStagehandEvidenceManifestBindsEveryUploadedPNG(t *testing.T) {
	landing := []byte("stagehand-landing-png")
	mobile := []byte("stagehand-mobile-png")
	manifest := func(name string, body []byte) map[string]any {
		digest := sha256.Sum256(body)
		return map[string]any{
			"name": name, "content_type": "image/png", "bytes": float64(len(body)), "sha256": fmt.Sprintf("%x", digest),
		}
	}
	report := map[string]any{"tool": "stagehand", "evidence": map[string]any{"artifacts": []any{
		manifest("semantic-qa.png", landing), manifest("semantic-qa-mobile.png", mobile),
	}}}
	artifacts := []LocalArtifact{
		{Name: "semantic-qa.json", ContentType: "application/json", Body: []byte("{}")},
		{Name: "semantic-qa.png", ContentType: "image/png", Body: landing},
		{Name: "semantic-qa-mobile.png", ContentType: "image/png", Body: mobile},
	}
	if err := verifyStagehandEvidenceManifest(report, artifacts); err != nil {
		t.Fatalf("valid Stagehand evidence rejected: %v", err)
	}

	artifacts[2].Body = []byte("swapped")
	if err := verifyStagehandEvidenceManifest(report, artifacts); err == nil {
		t.Fatal("swapped screenshot must not reach a human QA gate")
	}
}

func TestStagehandEvidenceManifestRejectsMissingOrUnsafeArtifacts(t *testing.T) {
	body := []byte("stagehand-png")
	digest := sha256.Sum256(body)
	report := map[string]any{"tool": "stagehand", "evidence": map[string]any{"artifacts": []any{
		map[string]any{"name": "../outside.png", "content_type": "image/png", "bytes": float64(len(body)), "sha256": fmt.Sprintf("%x", digest)},
		map[string]any{"name": "semantic-qa-mobile.png", "content_type": "image/png", "bytes": float64(len(body)), "sha256": fmt.Sprintf("%x", digest)},
	}}}
	artifacts := []LocalArtifact{
		{Name: "semantic-qa.png", ContentType: "image/png", Body: body},
		{Name: "semantic-qa-mobile.png", ContentType: "image/png", Body: body},
	}
	if err := verifyStagehandEvidenceManifest(report, artifacts); err == nil {
		t.Fatal("unsafe or missing Stagehand evidence must be rejected")
	}
}

func TestStagehandToolExecutionUsesOnlyUploadedSemanticReport(t *testing.T) {
	result := map[string]any{"semantic": map[string]any{"report": map[string]any{
		"tool": "stagehand", "calls": []any{map[string]any{
			"call_key": "semantic-assessment", "call_id": uuid.Must(uuid.NewV4()).String(), "receipt_id": uuid.Must(uuid.NewV4()).String(),
			"provider": "minimax", "model": "MiniMax-M3", "call_status": "completed",
			"usage": map[string]any{"input_tokens": float64(120), "output_tokens": float64(40), "total_tokens": float64(160)},
		}},
	}}}
	references := []ArtifactReference{{Name: "dashboard-semantic-qa.json", Reference: "s3://private/automation/task/artifacts/01-dashboard-semantic-qa.json", ContentType: "application/json"}}
	executions := stagehandToolExecutions(result, references)
	if len(executions) != 1 || executions[0].Tool != "stagehand" || executions[0].CallKey != "semantic-assessment" || executions[0].RequestRef != references[0].Reference || executions[0].ResponseRef != references[0].Reference {
		t.Fatalf("semantic report must have one artifact-bound ledger handoff: %#v", executions)
	}
	if got := stagehandToolExecutions(result, nil); len(got) != 0 {
		t.Fatalf("a report without uploaded evidence must not create a ledger handoff: %#v", got)
	}
}

func TestSemanticQAEnvironmentOnlyExposesScopedCapabilityToPinnedStagehand(t *testing.T) {
	capability := installGatewayTestCapability(t, "task-1", "run-1", inferencecapability.OperationDeliveryQA)
	lookup := func(name string) string {
		switch name {
		case "ITBEM_AI_GATEWAY_URL":
			return "https://gateway.example.test/api/internal/automation/inference"
		case "AUTOMATION_CALLBACK_SECRET":
			return "test-callback-secret"
		case "MINIMAX_API_KEY":
			return "unused-provider-key"
		}
		return ""
	}
	ordinary, err := semanticQAEnvironment([]string{"go", "run", "semantic.go", "{preview_url}", "{artifact_path}"}, "task", "run", lookup)
	if err != nil || len(ordinary) != 0 {
		t.Fatalf("ordinary repository QA must not receive provider credentials: %#v / %v", ordinary, err)
	}
	pinned := []string{"node", "C:/agent/itbem-events-backend/tools/stagehand-qa/run.mjs", "--url", "{preview_url}", "--output", "{artifact_path}"}
	environment, err := semanticQAEnvironment(pinned, "task-1", "run-1", lookup)
	if err != nil || environment["STAGEHAND_QA_INFERENCE_URL"] == "" || environment["STAGEHAND_QA_INFERENCE_CAPABILITY"] == "" || strings.Contains(environment["STAGEHAND_QA_INFERENCE_CAPABILITY"], "test-callback-secret") || environment["STAGEHAND_QA_TASK_ID"] != "task-1" || environment["STAGEHAND_QA_RUN_ID"] != "run-1" || environment["STAGEHAND_QA_OPERATION"] != "delivery.qa" || environment["AUTOMATION_CALLBACK_SECRET"] != "" || environment["MINIMAX_API_KEY"] != "" || len(environment) != 5 {
		t.Fatalf("pinned Stagehand runner must receive only its scoped inference capability: %#v / %v", environment, err)
	}
	if environment["STAGEHAND_QA_INFERENCE_CAPABILITY"] != capability {
		t.Fatal("Stagehand must receive the capability issued to this exact worker run")
	}
	for _, runtime := range []string{"/usr/bin/node", "C:/managed/node.exe", `C:\managed\node.exe`} {
		command := []string{runtime, "C:/agent/itbem-events-backend/tools/stagehand-qa/run.mjs"}
		got, err := semanticQAEnvironment(command, "task-1", "run-1", lookup)
		if err != nil || got["STAGEHAND_QA_INFERENCE_CAPABILITY"] != capability {
			t.Errorf("absolute managed Node runtime %q must receive its bound Stagehand capability: %#v / %v", runtime, got, err)
		}
	}
	for _, runtime := range []string{"/usr/bin/pwsh", "./node", "node.exe"} {
		command := []string{runtime, "tools/stagehand-qa/run.mjs"}
		got, err := semanticQAEnvironment(command, "task-1", "run-1", lookup)
		if err != nil || len(got) != 0 {
			t.Errorf("unapproved runtime %q must not receive Stagehand capability: %#v / %v", runtime, got, err)
		}
	}
	verified, err := inferencecapability.Verify(testGatewayCapabilitySigningKey, environment["STAGEHAND_QA_INFERENCE_CAPABILITY"], "task-1", "run-1", "delivery.qa", time.Now().UTC())
	if err != nil || verified.TaskID != "task-1" || verified.RunID != "run-1" || verified.Operation != inferencecapability.OperationDeliveryQA || verified.WorkerID == "" || verified.MachineID == "" {
		t.Fatalf("Stagehand capability was not signed for its exact run: %v", err)
	}
	// Check the effective subprocess environment after the same transform
	// used by runLocalWithEnv, not only the explicit Stagehand overrides.
	effective := repositoryCommandEnvironment([]string{
		"AUTOMATION_CALLBACK_SECRET=test-callback-secret",
		"ITBEM_AGENT_PRIVATE_KEY=private-key-must-not-reach-stagehand",
		"MINIMAX_API_KEY=provider-key-must-not-reach-stagehand",
	}, environment)
	for _, entry := range effective {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "AUTOMATION_CALLBACK_SECRET") || strings.EqualFold(name, "MINIMAX_API_KEY") || strings.EqualFold(name, "ITBEM_AGENT_PRIVATE_KEY") {
			t.Fatalf("Stagehand subprocess inherited forbidden credential environment variable %s", name)
		}
	}
	if _, err := semanticQAEnvironment(pinned, "task", "run", func(string) string { return "" }); err == nil {
		t.Fatal("pinned Stagehand runner must fail closed without its bound gateway identity")
	}
}

func TestResolveSemanticQACommandUsesOnlyConfiguredManagedNodeRuntime(t *testing.T) {
	command := []string{"node", "C:/agent/itbem-events-backend/tools/stagehand-qa/run.mjs", "--url", "{preview_url}", "--output", "{artifact_path}"}
	resolved, err := resolveSemanticQACommand(command, func(name string) string {
		if name == "ITBEM_STAGEHAND_NODE_EXECUTABLE" {
			return "C:/managed/node.exe"
		}
		return ""
	})
	if err != nil || resolved[0] != "C:/managed/node.exe" || command[0] != "node" {
		t.Fatalf("Stagehand Node runtime was not resolved safely: %#v / %v", resolved, err)
	}
	if _, err := resolveSemanticQACommand(command, func(string) string { return "C:/managed/not-node.exe" }); err == nil {
		t.Fatal("an unapproved runtime path must be rejected")
	}
}

func TestStagehandToolExecutionKeepsEachReportedCallSeparate(t *testing.T) {
	result := map[string]any{"semantic": map[string]any{"report": map[string]any{
		"tool": "stagehand",
		"calls": []any{
			map[string]any{"call_key": "semantic-assessment", "call_id": uuid.Must(uuid.NewV4()).String(), "receipt_id": uuid.Must(uuid.NewV4()).String(), "provider": "minimax", "model": "MiniMax-M3", "usage": map[string]any{"input_tokens": float64(120), "output_tokens": float64(40), "total_tokens": float64(160)}},
			map[string]any{"call_key": "semantic-retry", "call_id": uuid.Must(uuid.NewV4()).String(), "receipt_id": uuid.Must(uuid.NewV4()).String(), "call_status": "failed", "provider": "minimax", "model": "MiniMax-M3", "usage": map[string]any{"input_tokens": float64(60), "output_tokens": float64(20), "total_tokens": float64(80)}},
		},
	}}}
	references := []ArtifactReference{{Name: "dashboard-semantic-qa.json", Reference: "s3://private/automation/task/artifacts/01-dashboard-semantic-qa.json", ContentType: "application/json"}}
	executions := stagehandToolExecutions(result, references)
	if len(executions) != 2 || executions[0].CallKey != "semantic-assessment" || executions[1].CallKey != "semantic-retry" || executions[1].CallStatus != "failed" {
		t.Fatalf("every reported Stagehand inference must become its own ledger handoff: %#v", executions)
	}
}

func TestDeliveryQATargetsUsesTheReviewedWorktreeInsteadOfBaseRepository(t *testing.T) {
	root := t.TempDir()
	for _, command := range [][]string{{"git", "init"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "README.md"}, {"git", "commit", "-m", "initial"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git commit setup failed: %#v / %v", result, err)
		}
	}
	implementationTaskID := "a4a4b837-2e18-43af-9f58-6d59629db2bb"
	worktree, branch, err := isolatedWorktree(context.Background(), Workspace{Root: root}, implementationTaskID)
	if err != nil {
		t.Fatal(err)
	}
	registry := `{"repo":{"path":"` + filepath.ToSlash(root) + `"}}`
	lookup := func(name string) string {
		if name == "ITBEM_AI_WORKSPACES_JSON" {
			return registry
		}
		return ""
	}
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("reviewed change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(reviewedQAMetadata(t, root, worktree, "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	delivery := []byte(`{"context_sources":[{"kind":"repository","reference":"workspace://repo"}],"change_sets":[{"repository_ref":"workspace://repo","branch":"` + branch + `","review_type":"local_worktree","ci_status":"passed","metadata":` + string(metadata) + `}]}`)
	targets, err := deliveryQATargets(delivery, lookup)
	if err != nil || len(targets) != 1 || targets[0].root != worktree || targets[0].testedDirectory != "reviewed isolated worktree" {
		t.Fatalf("QA must bind to the reviewed isolated worktree: %#v / %v", targets, err)
	}
	contractDelivery := []byte(`{"approved_plan":{"qa_execution_matrix":[{"repository_ref":"workspace://repo","run_validation":true,"run_qa":false,"run_stagehand":false,"collect_evidence":false}]},"context_sources":[{"kind":"repository","reference":"workspace://repo"}],"change_sets":[{"repository_ref":"workspace://repo","branch":"` + branch + `","review_type":"local_worktree","ci_status":"passed","metadata":` + string(metadata) + `}]}`)
	contractTargets, err := deliveryQATargets(contractDelivery, lookup)
	if err != nil || len(contractTargets) != 1 || !contractTargets[0].execution.RunValidation || contractTargets[0].execution.RunQA || contractTargets[0].execution.RunStagehand || contractTargets[0].execution.CollectEvidence {
		t.Fatalf("reviewed worktree must receive its exact approved QA execution contract: %#v / %v", contractTargets, err)
	}
	var unbound map[string]any
	if err := json.Unmarshal(delivery, &unbound); err != nil {
		t.Fatal(err)
	}
	delete(unbound["change_sets"].([]any)[0].(map[string]any), "metadata")
	unboundJSON, err := json.Marshal(unbound)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deliveryQATargets(unboundJSON, lookup); err == nil {
		t.Fatal("missing reviewed revision metadata was accepted")
	}
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("changed after review\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := deliveryQATargets(delivery, lookup); err == nil {
		t.Fatal("changed reviewed diff was accepted")
	}
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("reviewed change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changedBranch, err := runLocal(context.Background(), worktree, time.Minute, "", "git", "checkout", "-b", "synthetic-wrong-branch")
	if err != nil || changedBranch.ExitCode != 0 {
		t.Fatalf("fixture branch change failed: %#v / %v", changedBranch, err)
	}
	if _, err := deliveryQATargets(delivery, lookup); err == nil {
		t.Fatal("same path with a different branch was accepted")
	}
	restored, err := runLocal(context.Background(), worktree, time.Minute, "", "git", "checkout", branch)
	if err != nil || restored.ExitCode != 0 {
		t.Fatalf("fixture branch restore failed: %#v / %v", restored, err)
	}
	stagehandWithoutRunner := []byte(`{"approved_plan":{"qa_execution_matrix":[{"repository_ref":"workspace://repo","run_validation":false,"run_qa":true,"run_stagehand":true,"collect_evidence":true}]},"context_sources":[{"kind":"repository","reference":"workspace://repo"}],"change_sets":[{"repository_ref":"workspace://repo","branch":"` + branch + `","review_type":"local_worktree","ci_status":"passed"}]}`)
	if _, err := deliveryQATargets(stagehandWithoutRunner, lookup); err == nil {
		t.Fatal("a requested Stagehand run must fail closed when the workspace has no configured runner")
	}
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	if _, err := deliveryQATargets(delivery, lookup); err == nil {
		t.Fatal("QA must fail closed when the reviewed worktree is unavailable")
	}
}
