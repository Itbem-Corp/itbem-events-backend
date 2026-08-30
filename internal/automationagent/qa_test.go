package automationagent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type artifactFakeStore struct {
	fakeStore
	objects map[string][]byte
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
	result, artifacts, err := RunQA(context.Background(), "task", delivery, lookup)
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
	uploaded, references, err := worker.uploadArtifacts(context.Background(), "task", result, artifacts)
	if err != nil || len(store.objects) != 2 || len(uploaded["artifacts"].([]map[string]any)) != 2 || len(references) != 2 {
		t.Fatalf("unexpected uploaded artifacts: %#v / %#v / %#v / %v", uploaded, references, store.objects, err)
	}
	for _, reference := range references {
		if len(reference.SHA256) != 64 {
			t.Fatalf("uploaded QA evidence must carry a SHA-256 digest: %#v", reference)
		}
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
		context.Background(), "task", "http://127.0.0.1:3000/login", delivery, t.TempDir(),
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
	captureProgram := `package main
import (
  "encoding/base64"
  "os"
)
func main() {
  if len(os.Args) != 3 { os.Exit(2) }
  body, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScL92gAAAABJRU5ErkJggg==")
  if err := os.WriteFile(os.Args[2], body, 0600); err != nil { panic(err) }
}`
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
	if err := os.WriteFile(filepath.Join(root, "capture.go"), []byte(captureProgram), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "semantic.go"), []byte(semanticProgram), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusOK) }))
	defer server.Close()
	registry := `{"repo":{"path":"` + filepath.ToSlash(root) + `","qa_screenshot_command":["go","run","capture.go","{preview_url}","{artifact_path}"],"qa_semantic_command":["go","run","semantic.go","{preview_url}","{artifact_path}"]}}`
	lookup := func(name string) string {
		if name == "ITBEM_AI_WORKSPACES_JSON" {
			return registry
		}
		return ""
	}
	delivery := []byte(`{"work_item":{"preview_url":"` + server.URL + `"},"context_sources":[{"kind":"repository","reference":"workspace://repo"}]}`)
	result, artifacts, err := RunQA(context.Background(), "task", delivery, lookup)
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
		"tool": "stagehand", "provider": "minimax", "model": "MiniMax-M3",
		"usage": map[string]any{"input_tokens": float64(120), "output_tokens": float64(40), "total_tokens": float64(160)},
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

func TestSemanticQAEnvironmentOnlyExposesProviderCredentialToPinnedStagehand(t *testing.T) {
	lookup := func(name string) string {
		if name == "MINIMAX_API_KEY" {
			return "test-minimax-key"
		}
		return ""
	}
	ordinary, err := semanticQAEnvironment([]string{"go", "run", "semantic.go", "{preview_url}", "{artifact_path}"}, lookup)
	if err != nil || len(ordinary) != 0 {
		t.Fatalf("ordinary repository QA must not receive provider credentials: %#v / %v", ordinary, err)
	}
	pinned := []string{"node", "C:/agent/itbem-events-backend/tools/stagehand-qa/run.mjs", "--url", "{preview_url}", "--output", "{artifact_path}"}
	environment, err := semanticQAEnvironment(pinned, lookup)
	if err != nil || environment["MINIMAX_API_KEY"] != "test-minimax-key" || len(environment) != 1 {
		t.Fatalf("pinned Stagehand runner must receive only its provider credential: %#v / %v", environment, err)
	}
	if _, err := semanticQAEnvironment(pinned, func(string) string { return "" }); err == nil {
		t.Fatal("pinned Stagehand runner must fail closed without its configured provider credential")
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
			map[string]any{"call_key": "semantic-assessment", "provider": "minimax", "model": "MiniMax-M3", "usage": map[string]any{"input_tokens": float64(120), "output_tokens": float64(40), "total_tokens": float64(160)}},
			map[string]any{"call_key": "semantic-retry", "call_status": "failed", "provider": "minimax", "model": "MiniMax-M3", "usage": map[string]any{"input_tokens": float64(60), "output_tokens": float64(20), "total_tokens": float64(80)}},
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
	delivery := []byte(`{"context_sources":[{"kind":"repository","reference":"workspace://repo"}],"change_sets":[{"repository_ref":"workspace://repo","branch":"` + branch + `","review_type":"local_worktree","ci_status":"passed"}]}`)
	targets, err := deliveryQATargets(delivery, lookup)
	if err != nil || len(targets) != 1 || targets[0].root != worktree || targets[0].testedDirectory != "reviewed isolated worktree" {
		t.Fatalf("QA must bind to the reviewed isolated worktree: %#v / %v", targets, err)
	}
	contractDelivery := []byte(`{"approved_plan":{"qa_execution_matrix":[{"repository_ref":"workspace://repo","run_validation":true,"run_qa":false,"run_stagehand":false,"collect_evidence":false}]},"context_sources":[{"kind":"repository","reference":"workspace://repo"}],"change_sets":[{"repository_ref":"workspace://repo","branch":"` + branch + `","review_type":"local_worktree","ci_status":"passed"}]}`)
	contractTargets, err := deliveryQATargets(contractDelivery, lookup)
	if err != nil || len(contractTargets) != 1 || !contractTargets[0].execution.RunValidation || contractTargets[0].execution.RunQA || contractTargets[0].execution.RunStagehand || contractTargets[0].execution.CollectEvidence {
		t.Fatalf("reviewed worktree must receive its exact approved QA execution contract: %#v / %v", contractTargets, err)
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
