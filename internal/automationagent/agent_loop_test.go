package automationagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"events-stocks/models"
)

type checkpointStore struct {
	objects           map[string][]byte
	patchObjects      map[string][]byte
	patchContentTypes map[string]string
	patchPutErr       error
	patchEventOrder   []string
	readErr           error
}

func (s *checkpointStore) Get(_ context.Context, bucket, key string) ([]byte, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	raw, ok := s.objects[bucket+"/"+key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return raw, nil
}
func (s *checkpointStore) PutEncryptedJSON(_ context.Context, bucket, key string, body []byte) error {
	if s.objects == nil {
		s.objects = map[string][]byte{}
	}
	s.objects[bucket+"/"+key] = append([]byte(nil), body...)
	return nil
}
func (s *checkpointStore) PutEncryptedObject(_ context.Context, bucket, key string, body []byte, contentType string) error {
	if s.patchPutErr != nil {
		return s.patchPutErr
	}
	if s.patchObjects == nil {
		s.patchObjects = map[string][]byte{}
		s.patchContentTypes = map[string]string{}
	}
	s.patchObjects[bucket+"/"+key] = append([]byte(nil), body...)
	s.patchContentTypes[bucket+"/"+key] = contentType
	s.patchEventOrder = append(s.patchEventOrder, "upload")
	return nil
}

type sequenceProvider struct {
	responses             []string
	calls                 int
	leases                []InferenceLease
	inferenceCapabilities int
}

func (p *sequenceProvider) Complete(ctx context.Context, _ []Message, _ int) (Completion, error) {
	p.calls++
	lease, ok := InferenceLeaseFromContext(ctx)
	if ok {
		p.leases = append(p.leases, lease)
	}
	if _, ok := InferenceCapabilityFromContext(ctx); ok {
		p.inferenceCapabilities++
	}
	if p.calls > len(p.responses) {
		return Completion{}, errors.New("unexpected provider call")
	}
	return Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: p.responses[p.calls-1], Usage: map[string]any{"prompt_tokens": 10, "completion_tokens": 20}, ResponseID: fmt.Sprint(p.calls), CallID: stepCallbackUUID(), ReceiptID: stepCallbackUUID()}, nil
}

func agentFixture(t *testing.T) (*Worker, *checkpointStore, *sequenceProvider, *fakeCallback, TaskMessage, TaskInput, agentCheckpoint) {
	t.Helper()
	store := &checkpointStore{objects: map[string][]byte{}}
	provider := &sequenceProvider{}
	callback := &fakeCallback{operation: "delivery.implementation"}
	worker, err := NewWorker(WorkerConfig{
		InputBucket: "itbem-ai-inputs-test", OutputBucket: "itbem-ai-outputs-test",
		WorkerID: "a69b7f51-58b9-4f0e-aef3-1fbc23f79826", AgentKey: "generalist", MachineID: "b69b7f51-58b9-4f0e-aef3-1fbc23f79827",
	}, store, callback, provider)
	if err != nil {
		t.Fatal(err)
	}
	message := TaskMessage{}
	message.Payload.TaskID = "d4a4b837-2e18-43af-9f58-6d59629db2bb"
	message.Payload.Operation = "delivery.implementation"
	input := TaskInput{AgentExecution: true, Prompt: "implement", Delivery: json.RawMessage(`{"approved_plan":{"files_impacted":["note.go"]},"work_item":{"acceptance_criteria":["Value is two"]},"context_sources":[{"kind":"repository","reference":"workspace://repo"}]}`)}
	raw, _ := json.Marshal(input)
	cp := agentCheckpoint{TaskID: message.Payload.TaskID, InputDigest: fmt.Sprintf("%x", sha256.Sum256(raw)), RunID: "9f5d9d66-e58d-4357-8869-83f09bca8222", Started: time.Now().UTC(), Messages: []Message{{Role: "user", Content: "implement"}}}
	return worker, store, provider, callback, message, input, cp
}
func saveTestCheckpoint(t *testing.T, w *Worker, s *checkpointStore, cp agentCheckpoint) {
	t.Helper()
	raw, _ := json.Marshal(cp)
	if err := s.PutEncryptedJSON(context.Background(), w.config.OutputBucket, "automation/"+cp.TaskID+"/agent-checkpoint.json", raw); err != nil {
		t.Fatal(err)
	}
}
func testGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	result, err := runLocal(context.Background(), root, time.Minute, "", "git", args...)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("git %v: %#v %v", args, result, err)
	}
	return strings.TrimSpace(result.Output)
}
func testRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	testGit(t, root, "init")
	testGit(t, root, "config", "user.email", "agent@example.invalid")
	testGit(t, root, "config", "user.name", "Agent tests")
	return root
}

func TestAgentRepairsFailedValidationAndAccountsEveryCall(t *testing.T) {
	w, s, p, callback, message, input, cp := agentFixture(t)
	root := testRepository(t)
	files := map[string]string{"go.mod": "module agentfixture\n\ngo 1.25.0\n", "note.go": "package fixture\nvar Value = 0\n", "note_test.go": "package fixture\nimport \"testing\"\nfunc TestValue(t *testing.T){if Value!=2{t.Fatal(\"Value must be two\")}}\n"}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "initial")
	config, _ := json.Marshal(map[string]WorkspaceConfig{"repo": {Path: root, ValidationCommands: [][]string{{"go", "test", "./..."}}, AcceptanceChecks: []AcceptanceCheck{{Criterion: "Value is two", Command: []string{"go", "test", "./..."}}}}})
	t.Setenv("ITBEM_AI_WORKSPACES_JSON", string(config))
	for i := 0; i < 2; i++ {
		raw, _ := json.Marshal(map[string]string{"action": "patch", "summary": "change value", "patch": fmt.Sprintf("diff --git a/note.go b/note.go\n--- a/note.go\n+++ b/note.go\n@@ -1,2 +1,2 @@\n package fixture\n-var Value = %d\n+var Value = %d\n", i, i+1)})
		p.responses = append(p.responses, string(raw))
	}
	saveTestCheckpoint(t, w, s, cp)
	if err := w.processImplementationAgent(context.Background(), message, input, cp.RunID); err != nil {
		t.Fatal(err)
	}
	last := callback.updates[len(callback.updates)-1]
	if p.calls != 2 || len(p.leases) != 2 || p.leases[0].TaskID != message.Payload.TaskID || p.leases[0].RunID != cp.RunID || p.leases[0].Operation != message.Payload.Operation || p.leases[0].StepID != "" || last.Status != "completed" || len(last.ToolExecutions) != 1 || last.Execution == nil {
		t.Fatalf("calls=%d update=%+v", p.calls, last)
	}
	thinkingCalls := []int{}
	for _, update := range callback.updates {
		if update.ProgressStep == "thinking" {
			thinkingCalls = append(thinkingCalls, update.ProgressCall)
			if update.Status != "running" || update.RunID != cp.RunID {
				t.Fatalf("inference progress escaped its active run: %#v", update)
			}
		}
	}
	if !reflect.DeepEqual(thinkingCalls, []int{0, 1, 1, 2}) {
		t.Fatalf("expected ordinary loop progress plus a server capability renewal immediately before each provider call; progress calls=%v", thinkingCalls)
	}
	body, _ := os.ReadFile(filepath.Join(root, "note.go"))
	if !strings.Contains(string(body), "Value = 0") {
		t.Fatal("base checkout changed")
	}
	if err := w.processImplementationAgent(context.Background(), message, input, "d436bcfd-b22d-4090-a462-b4ed841e521a"); err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 {
		t.Fatal("recovery repeated inference")
	}
	last = callback.updates[len(callback.updates)-1]
	if last.RecoveryRunID != cp.RunID || last.Status != "completed" {
		t.Fatalf("recovered update %+v", last)
	}
}

func TestImplementationAgentRunsTargetedPlanStepAndRecordsOnlyVerifiedCompletion(t *testing.T) {
	_, store, provider, _, message, input, _ := agentFixture(t)
	steps, _ := planStepTestFixture()
	steps[0].AcceptanceCriteria = []string{"First artifact exists"}
	steps[1].AcceptanceCriteria = []string{"Second artifact exists"}
	delivery, err := json.Marshal(map[string]any{
		"approved_plan": map[string]any{
			"files_impacted":    []string{"step1.txt", "step2.txt"},
			"repository_impact": []any{map[string]any{"reference": "workspace://repo", "impact": "changes"}},
		},
		"work_item": map[string]any{
			"acceptance_criteria": []string{"First artifact exists", "Second artifact exists"},
		},
		"context_sources": []any{map[string]any{"kind": "repository", "reference": "workspace://repo"}},
		"plan_steps":      steps,
		"plan_execution":  planStepExecutionBindingForTest(steps),
	})
	if err != nil {
		t.Fatal(err)
	}
	input.Delivery = delivery
	message.Payload.PlanStepID = steps[0].ID

	root := testRepository(t)
	files := map[string]string{
		"go.mod":         "module agentfixture\n\ngo 1.25.0\n",
		"checks_test.go": "package fixture\nimport (\"os\"; \"testing\")\nfunc TestStepOne(t *testing.T){if _,err:=os.Stat(\"step1.txt\");err!=nil{t.Fatal(\"first artifact missing\")}}\nfunc TestStepTwo(t *testing.T){if _,err:=os.Stat(\"step2.txt\");err!=nil{t.Fatal(\"second artifact missing\")}}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "initial")
	config, _ := json.Marshal(map[string]WorkspaceConfig{"repo": {
		Path:               root,
		ValidationCommands: [][]string{{"go", "test", "-run", "^TestStepOne$", "./..."}},
		AcceptanceChecks: []AcceptanceCheck{
			{Criterion: "First artifact exists", Command: []string{"go", "test", "-run", "^TestStepOne$", "./..."}},
			{Criterion: "Second artifact exists", Command: []string{"go", "test", "-run", "^TestStepTwo$", "./..."}},
		},
	}})
	t.Setenv("ITBEM_AI_WORKSPACES_JSON", string(config))
	provider.responses = []string{
		`{"action":"patch","summary":"Create first artifact","patch":"diff --git a/step1.txt b/step1.txt\nnew file mode 100644\n--- /dev/null\n+++ b/step1.txt\n@@ -0,0 +1 @@\n+ready\n"}`,
		`{"action":"patch","summary":"Create second artifact","patch":"diff --git a/step2.txt b/step2.txt\nnew file mode 100644\n--- /dev/null\n+++ b/step2.txt\n@@ -0,0 +1 @@\n+verified\n"}`,
	}
	baseStepCallback := newPlanStepTestCallback(steps)
	stepCallback := &orderedPlanStepActivityCallback{planStepTestCallback: baseStepCallback, store: store}
	worker, err := NewWorker(WorkerConfig{
		InputBucket: "itbem-ai-inputs-test", OutputBucket: "itbem-ai-outputs-test",
		WorkerID: stepCallbackUUID(), AgentKey: "generalist",
	}, store, stepCallback, provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.processImplementationAgent(context.Background(), message, input, stepCallbackUUID()); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("targeted plan step should use one provider call; got %d calls", provider.calls)
	}
	if len(provider.leases) != 1 || provider.leases[0].StepID != steps[0].ID {
		t.Fatalf("provider inference was not bound to the claimed active plan step: %#v", provider.leases)
	}
	baseStepCallback.mu.Lock()
	statuses := append([]PlanStepStatusRequest(nil), baseStepCallback.statusRequests...)
	claimCount := len(baseStepCallback.claimRequests)
	activityRequests := append([]PlanStepActivityRequest(nil), baseStepCallback.activityRequests...)
	activityStepIDs := append([]string(nil), baseStepCallback.activityStepIDs...)
	baseStepCallback.mu.Unlock()
	if claimCount != 1 || len(statuses) != 1 || statuses[0].Status != "completed" || statuses[0].StepID != steps[0].ID {
		t.Fatalf("targeted step needs one verified completion event: claims=%d statuses=%#v", claimCount, statuses)
	}
	stepCallback.orderMu.Lock()
	order := append([]string(nil), stepCallback.order...)
	stepCallback.orderMu.Unlock()
	if !reflect.DeepEqual(order, []string{"acceptance_evidence", "step_completed"}) {
		t.Fatalf("completed status must follow a successfully recorded acceptance event: %v", order)
	}
	completedEvidence := 0
	emptyDependencyManifestSHA256, _ := models.DeliveryPlanStepDependencyPatchManifestSHA256(nil)
	for requestIndex, request := range activityRequests {
		if request.Action != "evidence" || request.Phase != "completed" {
			continue
		}
		completedEvidence++
		if request.Details == nil || request.Details.ReviewDiffSHA256 == "" || len(request.Details.AcceptanceChecks) != 1 || !request.Details.AcceptanceChecks[0].Passed {
			t.Fatalf("acceptance activity must carry only a passing criterion digest and reviewed diff digest: %#v", request)
		}
		if request.Details.AppliedDependencyManifestSHA256 != emptyDependencyManifestSHA256 || request.Details.AppliedDependencyPatchCount == nil || *request.Details.AppliedDependencyPatchCount != 0 {
			t.Fatalf("acceptance evidence omitted the manifest actually applied, including the empty-set receipt: %#v", request.Details)
		}
		var step *PlanStepDTO
		for index := range steps {
			if steps[index].ID == activityStepIDs[requestIndex] {
				step = &steps[index]
			}
		}
		if step == nil || request.Details.AcceptanceChecks[0].CriterionSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(step.AcceptanceCriteria[0]))) {
			t.Fatalf("worker criterion hash must cover the exact frozen UTF-8 criterion: %#v", request.Details)
		}
		encoded, _ := json.Marshal(request.Details)
		if len(encoded) > 4096 || strings.Contains(string(encoded), step.AcceptanceCriteria[0]) || strings.Contains(string(encoded), "output") || strings.Contains(string(encoded), "criterion\"") {
			t.Fatalf("acceptance activity included unredacted criterion text or exceeded its bound: %s", encoded)
		}
	}
	if completedEvidence != 1 {
		t.Fatalf("expected one completed acceptance event for the targeted step, got %d", completedEvidence)
	}
	if len(store.patchObjects) != 1 {
		t.Fatalf("expected one encrypted patch artifact for the targeted step, got %d", len(store.patchObjects))
	}
	for requestIndex, request := range activityRequests {
		if request.Action != "evidence" || request.Phase != "completed" {
			continue
		}
		if request.Details == nil || len(request.Details.PatchArtifacts) != 1 {
			t.Fatalf("acceptance event omitted the uploaded patch reference: %#v", request.Details)
		}
		manifestDigest, digestErr := models.DeliveryPlanStepPatchArtifactManifestSHA256(request.Details.PatchArtifacts)
		if digestErr != nil || request.Details.ReviewDiffSHA256 != manifestDigest {
			t.Fatalf("acceptance review digest is not the canonical patch manifest: got=%s want=%s err=%v", request.Details.ReviewDiffSHA256, manifestDigest, digestErr)
		}
		artifact := request.Details.PatchArtifacts[0]
		objectKey := "itbem-ai-outputs-test/automation/" + message.Payload.TaskID + "/runs/" + request.RunID + "/steps/" + activityStepIDs[requestIndex] + "/patches/" + artifact.SHA256 + ".patch"
		objectBody, exists := store.patchObjects[objectKey]
		if !exists || artifact.SizeBytes != int64(len(objectBody)) || artifact.BaseSHA == "" || artifact.RepositoryRef != "workspace://repo" || store.patchContentTypes[objectKey] != stepPatchArtifactMediaType {
			t.Fatalf("acceptance reference does not match the encrypted patch object: ref=%#v key=%s exists=%v", artifact, objectKey, exists)
		}
		encodedRequest, _ := json.Marshal(request)
		if bytes.Contains(encodedRequest, objectBody) || strings.Contains(string(encodedRequest), privateStepPatchArtifactKey) {
			t.Fatal("callback activity contains patch bytes or an internal payload marker")
		}
		for key, stored := range store.objects {
			if bytes.Contains(stored, objectBody) {
				t.Fatalf("patch bytes were copied into checkpoint/result object %q", key)
			}
		}
	}
	if !reflect.DeepEqual(store.patchEventOrder, []string{"upload", "acceptance", "complete"}) {
		t.Fatalf("patch upload, acceptance event, and Complete were not ordered safely: %v", store.patchEventOrder)
	}
	for _, update := range stepCallback.updates {
		encodedUpdate, _ := json.Marshal(update)
		if strings.Contains(string(encodedUpdate), root) || strings.Contains(string(encodedUpdate), privateStepPatchArtifactKey) {
			t.Fatalf("callback update exposed a local path or internal patch payload: %s", encodedUpdate)
		}
		for _, patchBody := range store.patchObjects {
			if bytes.Contains(encodedUpdate, patchBody) {
				t.Fatal("callback update exposed patch bytes")
			}
		}
	}
	var checkpoint agentCheckpoint
	checkpointRaw := store.objects[worker.config.OutputBucket+"/automation/"+message.Payload.TaskID+"/agent-checkpoint.json"]
	if err := json.Unmarshal(checkpointRaw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.CompletedPlanSteps) != 1 || checkpoint.CompletedPlanSteps[0] != steps[0].StepKey || checkpoint.ActivePlanStepKey != "" || checkpoint.Result == nil {
		t.Fatalf("checkpoint did not preserve plan-step progress: %#v", checkpoint)
	}
	last := stepCallback.updates[len(stepCallback.updates)-1]
	if last.Status != "completed" {
		t.Fatalf("task was not completed after all step checks passed: %#v", last)
	}
}

type orderedPlanStepActivityCallback struct {
	*planStepTestCallback
	orderMu sync.Mutex
	order   []string
	store   *checkpointStore
}

func (callback *orderedPlanStepActivityCallback) RecordPlanStepActivity(ctx context.Context, stepID string, request PlanStepActivityRequest) error {
	if request.Action == "evidence" && request.Phase == "completed" {
		if callback.store != nil {
			callback.store.patchEventOrder = append(callback.store.patchEventOrder, "acceptance")
		}
		callback.orderMu.Lock()
		callback.order = append(callback.order, "acceptance_evidence")
		callback.orderMu.Unlock()
	}
	return callback.planStepTestCallback.RecordPlanStepActivity(ctx, stepID, request)
}

func (callback *orderedPlanStepActivityCallback) UpdatePlanStepStatus(ctx context.Context, request PlanStepStatusRequest) (PlanStepDTO, error) {
	if request.Status == "completed" {
		if callback.store != nil {
			callback.store.patchEventOrder = append(callback.store.patchEventOrder, "complete")
		}
		callback.orderMu.Lock()
		callback.order = append(callback.order, "step_completed")
		callback.orderMu.Unlock()
	}
	return callback.planStepTestCallback.UpdatePlanStepStatus(ctx, request)
}

func TestImplementationAgentTargetedChildCompletesOnlyAssignedPlanStep(t *testing.T) {
	_, store, provider, _, message, input, _ := agentFixture(t)
	steps, _ := planStepTestFixture()
	steps[0].AcceptanceCriteria = []string{"First artifact exists"}
	steps[1].AcceptanceCriteria = []string{"Second artifact exists"}
	delivery, err := json.Marshal(map[string]any{
		"approved_plan": map[string]any{
			"files_impacted":    []string{"step1.txt", "step2.txt"},
			"repository_impact": []any{map[string]any{"reference": "workspace://repo", "impact": "changes"}},
		},
		"work_item": map[string]any{
			"acceptance_criteria": []string{"First artifact exists", "Second artifact exists"},
		},
		"context_sources": []any{map[string]any{"kind": "repository", "reference": "workspace://repo"}},
		"plan_steps":      steps,
		"plan_execution":  planStepExecutionBindingForTest(steps),
	})
	if err != nil {
		t.Fatal(err)
	}
	input.Delivery = delivery
	message.Payload.PlanStepID = steps[0].ID

	root := testRepository(t)
	files := map[string]string{
		"go.mod":         "module agentfixture\n\ngo 1.25.0\n",
		"checks_test.go": "package fixture\nimport (\"os\"; \"testing\")\nfunc TestStepOne(t *testing.T){if _,err:=os.Stat(\"step1.txt\");err!=nil{t.Fatal(\"first artifact missing\")}}\nfunc TestStepTwo(t *testing.T){if _,err:=os.Stat(\"step2.txt\");err!=nil{t.Fatal(\"second artifact missing\")}}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "initial")
	config, _ := json.Marshal(map[string]WorkspaceConfig{"repo": {
		Path:               root,
		ValidationCommands: [][]string{{"go", "test", "-run", "^TestStepOne$", "./..."}},
		AcceptanceChecks: []AcceptanceCheck{
			{Criterion: "First artifact exists", Command: []string{"go", "test", "-run", "^TestStepOne$", "./..."}},
			{Criterion: "Second artifact exists", Command: []string{"go", "test", "-run", "^TestStepTwo$", "./..."}},
		},
	}})
	t.Setenv("ITBEM_AI_WORKSPACES_JSON", string(config))
	provider.responses = []string{
		`{"action":"patch","summary":"Create first artifact","patch":"diff --git a/step1.txt b/step1.txt\nnew file mode 100644\n--- /dev/null\n+++ b/step1.txt\n@@ -0,0 +1 @@\n+ready\n"}`,
	}
	stepCallback := newPlanStepTestCallback(steps)
	worker, err := NewWorker(WorkerConfig{
		InputBucket: "itbem-ai-inputs-test", OutputBucket: "itbem-ai-outputs-test",
		WorkerID: stepCallbackUUID(), AgentKey: "generalist",
	}, store, stepCallback, provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.processImplementationAgent(context.Background(), message, input, stepCallbackUUID()); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("targeted child should make progress only for its assigned step, got %d calls", provider.calls)
	}
	stepCallback.mu.Lock()
	claims := append([]PlanStepClaimRequest(nil), stepCallback.claimRequests...)
	statuses := append([]PlanStepStatusRequest(nil), stepCallback.statusRequests...)
	stepCallback.mu.Unlock()
	if len(claims) != 1 || claims[0].StepID != steps[0].ID || len(statuses) != 1 || statuses[0].StepID != steps[0].ID || statuses[0].Status != "completed" {
		t.Fatalf("targeted task claimed or completed an unexpected step: claims=%#v statuses=%#v", claims, statuses)
	}
	var checkpoint agentCheckpoint
	checkpointRaw := store.objects[worker.config.OutputBucket+"/automation/"+message.Payload.TaskID+"/agent-checkpoint.json"]
	if err := json.Unmarshal(checkpointRaw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.TargetPlanStepID != steps[0].ID || len(checkpoint.CompletedPlanSteps) != 1 || checkpoint.CompletedPlanSteps[0] != steps[0].StepKey || checkpoint.Result == nil {
		t.Fatalf("targeted checkpoint includes unexpected step progress: %#v", checkpoint)
	}
	if stepCallback.updates[len(stepCallback.updates)-1].Status != "completed" {
		t.Fatal("targeted child task did not complete after its step evidence passed")
	}
}

func TestImplementationAgentNeverCompletesWhenPatchArtifactUploadFails(t *testing.T) {
	worker, store, provider, _, message, input, checkpoint := agentFixture(t)
	steps, _ := planStepTestFixture()
	steps[0].AcceptanceCriteria = []string{"First artifact exists"}
	steps[1].AcceptanceCriteria = []string{"Second artifact exists"}
	delivery, err := json.Marshal(map[string]any{
		"approved_plan": map[string]any{
			"files_impacted":    []string{"step1.txt", "step2.txt"},
			"repository_impact": []any{map[string]any{"reference": "workspace://repo", "impact": "changes"}},
		},
		"work_item":       map[string]any{"acceptance_criteria": []string{"First artifact exists", "Second artifact exists"}},
		"context_sources": []any{map[string]any{"kind": "repository", "reference": "workspace://repo"}},
		"plan_steps":      steps,
		"plan_execution":  planStepExecutionBindingForTest(steps),
	})
	if err != nil {
		t.Fatal(err)
	}
	input.Delivery = delivery
	message.Payload.PlanStepID = steps[0].ID
	provider.responses = []string{`{"action":"patch","summary":"Create first artifact","patch":"diff --git a/step1.txt b/step1.txt\nnew file mode 100644\n--- /dev/null\n+++ b/step1.txt\n@@ -0,0 +1 @@\n+ready\n"}`}
	root := testRepository(t)
	files := map[string]string{
		"go.mod":         "module agentfixture\n\ngo 1.25.0\n",
		"checks_test.go": "package fixture\nimport (\"os\"; \"testing\")\nfunc TestStepOne(t *testing.T){if _,err:=os.Stat(\"step1.txt\");err!=nil{t.Fatal(\"first artifact missing\")}}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "initial")
	config, _ := json.Marshal(map[string]WorkspaceConfig{"repo": {
		Path:               root,
		ValidationCommands: [][]string{{"go", "test", "-run", "^TestStepOne$", "./..."}},
		AcceptanceChecks: []AcceptanceCheck{
			{Criterion: "First artifact exists", Command: []string{"go", "test", "-run", "^TestStepOne$", "./..."}},
			{Criterion: "Second artifact exists", Command: []string{"go", "test", "-run", "^TestStepOne$", "./..."}},
		},
	}})
	t.Setenv("ITBEM_AI_WORKSPACES_JSON", string(config))
	baseCallback := newPlanStepTestCallback(steps)
	worker.callback = identityTaskCallback{inner: baseCallback, identity: worker.identity()}
	store.patchPutErr = errors.New("object storage unavailable")
	checkpoint.PlanID = steps[0].PlanID
	checkpoint.TargetPlanStepID = steps[0].ID
	encodedInput, _ := json.Marshal(input)
	digestInput := append(append([]byte(nil), encodedInput...), []byte("\x00plan_step_id="+steps[0].ID)...)
	checkpoint.InputDigest = fmt.Sprintf("%x", sha256.Sum256(digestInput))
	saveTestCheckpoint(t, worker, store, checkpoint)

	if err := worker.processImplementationAgent(context.Background(), message, input, checkpoint.RunID); err != nil {
		t.Fatal(err)
	}
	baseCallback.mu.Lock()
	statuses := append([]PlanStepStatusRequest(nil), baseCallback.statusRequests...)
	baseCallback.mu.Unlock()
	for _, status := range statuses {
		if status.Status == "completed" {
			t.Fatal("step was completed after its required patch artifact upload failed")
		}
	}
	if len(store.patchObjects) != 0 {
		t.Fatalf("failed patch artifact upload unexpectedly persisted an object: %#v", store.patchObjects)
	}
	if len(statuses) == 0 || statuses[len(statuses)-1].Status != "failed" {
		t.Fatalf("step was not left terminally failed after upload failure: statuses=%#v updates=%#v provider_calls=%d checkpoint=%s", statuses, baseCallback.updates, provider.calls, store.objects[worker.config.OutputBucket+"/automation/"+message.Payload.TaskID+"/agent-checkpoint.json"])
	}
}

func TestImplementationAgentRejectsUnregisteredProviderBeforeInference(t *testing.T) {
	w, store, provider, callback, message, _, checkpoint := agentFixture(t)
	w.config.RequireProviderCapabilities = true
	saveTestCheckpoint(t, w, store, checkpoint)

	if err := w.processImplementationAgent(context.Background(), message, TaskInput{
		AgentExecution: true,
		Prompt:         "implement",
		Delivery:       json.RawMessage(`{"approved_plan":{"files_impacted":["note.go"]},"work_item":{"acceptance_criteria":["Value is two"]},"context_sources":[{"kind":"repository","reference":"workspace://repo"}]}`),
	}, checkpoint.RunID); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 0 {
		t.Fatalf("runtime capability admission must stop implementation before inference, got %d calls", provider.calls)
	}
	last := callback.updates[len(callback.updates)-1]
	if last.Status != "failed" || !strings.Contains(last.ErrorMessage, "capability contract") {
		t.Fatalf("unregistered implementation provider must fail clearly: %#v", last)
	}
	if _, ok := store.objects[w.config.OutputBucket+"/automation/"+message.Payload.TaskID+"/runs/"+checkpoint.RunID+"/request.json"]; ok {
		t.Fatal("unregistered implementation provider must not persist a billable request")
	}
}

func TestFinishImplementationAgentKeepsPartialEvidenceFailed(t *testing.T) {
	w, store, _, callback, message, _, cp := agentFixture(t)
	cp.Calls = []agentCall{{Completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: "{}", Usage: map[string]any{"total_tokens": 1}, ResponseID: "partial"}, RequestRef: "request", ResponseRef: "response"}}
	cp.PartialResult = map[string]any{
		"partial":                true,
		"completed_repositories": []string{"workspace://api"},
		"failed_repository":      "workspace://web",
	}
	cp.Failure = "workspace://web could not be reconciled"
	if err := w.finishImplementationAgent(context.Background(), message, cp.RunID, cp); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) == 0 || callback.updates[len(callback.updates)-1].Status != "failed" {
		t.Fatalf("partial implementation must remain failed: %#v", callback.updates)
	}
	if callback.updates[len(callback.updates)-1].Execution != nil {
		t.Fatal("partial implementation must not expose a successful execution handoff")
	}
	raw := store.objects[w.config.OutputBucket+"/automation/"+message.Payload.TaskID+"/runs/"+cp.RunID+"/result.json"]
	var output map[string]any
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	artifacts, _ := output["artifacts"].(map[string]any)
	implementation, _ := artifacts["partial_implementation"].(map[string]any)
	if implementation["partial"] != true {
		t.Fatalf("private partial evidence missing: %#v", output)
	}
}

func TestAgentAmbiguousCallDoesNotRepeatInference(t *testing.T) {
	w, s, p, callback, message, input, cp := agentFixture(t)
	cp.Pending = true
	saveTestCheckpoint(t, w, s, cp)
	if err := w.processImplementationAgent(context.Background(), message, input, cp.RunID); err != nil {
		t.Fatal(err)
	}
	last := callback.updates[len(callback.updates)-1]
	if p.calls != 0 || last.Status != "failed" || !strings.Contains(last.ErrorMessage, "uncertain") {
		t.Fatalf("unexpected %+v; calls %d", last, p.calls)
	}
}

func TestAgentRecoversDurableResponseBeforeExecutingItsAction(t *testing.T) {
	w, s, p, callback, message, input, cp := agentFixture(t)
	cp.Pending = true
	saveTestCheckpoint(t, w, s, cp)
	completion := Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: `{"action":"blocked","reason":"needs authorization"}`, Usage: map[string]any{"input_tokens": 10, "output_tokens": 20}}
	raw, _ := json.Marshal(map[string]any{"completion": completion, "rejected": false})
	s.objects[w.config.OutputBucket+"/automation/"+cp.TaskID+"/runs/"+cp.RunID+"/response.json"] = raw
	if err := w.processImplementationAgent(context.Background(), message, input, cp.RunID); err != nil {
		t.Fatal(err)
	}
	last := callback.updates[len(callback.updates)-1]
	if p.calls != 0 || last.Status != "failed" || last.Usage == nil || !strings.Contains(last.ErrorMessage, "assistance") {
		t.Fatalf("unexpected response recovery: %+v calls %d", last, p.calls)
	}
}

func TestImplementationAgentRedactsProviderOutputBeforePersistingOrReturning(t *testing.T) {
	w, store, provider, callback, message, input, checkpoint := agentFixture(t)
	secret := "ghp_" + strings.Repeat("A", 36)
	providerOutput, err := json.Marshal(map[string]any{
		"action":            "blocked",
		"reason":            "Human review requested; token=" + secret,
		"summary":           "Human review is required",
		"analysis":          "PRIVATE_ANALYSIS_CANARY",
		"reasoning_content": "PRIVATE_REASONING_CANARY",
		"api_key":           secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.responses = []string{string(providerOutput)}
	saveTestCheckpoint(t, w, store, checkpoint)

	if err := w.processImplementationAgent(context.Background(), message, input, checkpoint.RunID); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || len(callback.updates) == 0 {
		t.Fatalf("expected one inference and a final callback, got calls=%d updates=%d", provider.calls, len(callback.updates))
	}
	for key, body := range store.objects {
		if strings.Contains(string(body), secret) || strings.Contains(string(body), "PRIVATE_ANALYSIS_CANARY") || strings.Contains(string(body), "PRIVATE_REASONING_CANARY") {
			t.Fatalf("private provider canary was persisted in %s: %s", key, body)
		}
	}
	responseBody := store.objects[w.config.OutputBucket+"/automation/"+message.Payload.TaskID+"/runs/"+checkpoint.RunID+"/response.json"]
	var saved map[string]any
	if err := json.Unmarshal(responseBody, &saved); err != nil {
		t.Fatal(err)
	}
	completion, _ := saved["completion"].(map[string]any)
	content, _ := completion["content"].(string)
	var safeAction map[string]any
	if err := json.Unmarshal([]byte(content), &safeAction); err != nil {
		t.Fatal(err)
	}
	reason, _ := safeAction["reason"].(string)
	if safeAction["summary"] != "Human review is required" || !strings.Contains(reason, "<redacted>") || safeAction["analysis"] != nil || safeAction["reasoning_content"] != nil || safeAction["api_key"] != nil {
		t.Fatalf("sanitized response did not preserve the public summary while redacting private fields: %s", responseBody)
	}
	update := callback.updates[len(callback.updates)-1]
	if update.Status != "failed" || !strings.Contains(update.ErrorMessage, "Human review requested") || strings.Contains(update.ErrorMessage, secret) || strings.Contains(fmt.Sprintf("%+v", update), "PRIVATE_") {
		t.Fatalf("callback did not preserve the safe reason or leaked a private canary: %+v", update)
	}
}

func TestSanitizeAgentProviderContentOmitsUnstructuredPrivateText(t *testing.T) {
	got, err := sanitizeAgentProviderContent("PRIVATE_CHAIN_OF_THOUGHT_CANARY; answer: {not valid JSON}")
	if err != nil {
		t.Fatal(err)
	}
	if got != omittedProviderResponse || strings.Contains(got, "PRIVATE_CHAIN_OF_THOUGHT_CANARY") {
		t.Fatalf("unstructured provider text crossed the persistence boundary: %q", got)
	}
}

func TestAgentMissingAcceptanceConfigurationFailsBeforeInference(t *testing.T) {
	w, _, p, callback, message, input, cp := agentFixture(t)
	if err := w.processImplementationAgent(context.Background(), message, input, cp.RunID); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 || callback.updates[len(callback.updates)-1].Status != "failed" {
		t.Fatal("unconfigured task must not spend")
	}
}

func TestCommandBufferBoundsMemoryWithoutShortWrites(t *testing.T) {
	var buffer boundedCommandBuffer
	input := []byte(strings.Repeat("x", maxCommandOutput*3))
	n, err := buffer.Write(input)
	if err != nil || n != len(input) || buffer.Len() != maxCommandOutput {
		t.Fatalf("buffer n=%d size=%d err=%v", n, buffer.Len(), err)
	}
}
func TestAgentStorageOutageDoesNotBecomeFreshRun(t *testing.T) {
	w, s, p, _, message, input, cp := agentFixture(t)
	s.readErr = errors.New("storage down")
	if err := w.processImplementationAgent(context.Background(), message, input, cp.RunID); err == nil || p.calls != 0 {
		t.Fatal("storage failure must stop inference")
	}
}
func TestAgentCallBudgetAndFailureLedger(t *testing.T) {
	w, s, p, callback, message, input, cp := agentFixture(t)
	for i := 0; i < AgentMaxCalls; i++ {
		p.responses = append(p.responses, `{"action":"unknown"}`)
	}
	saveTestCheckpoint(t, w, s, cp)
	if err := w.processImplementationAgent(context.Background(), message, input, cp.RunID); err != nil {
		t.Fatal(err)
	}
	last := callback.updates[len(callback.updates)-1]
	if p.calls != AgentMaxCalls || last.Status != "failed" || len(last.ToolExecutions) != AgentMaxCalls-1 {
		t.Fatalf("unexpected calls %d, update %+v", p.calls, last)
	}
}
func TestAgentPatchCannotExpandApprovedFiles(t *testing.T) {
	delivery := json.RawMessage(`{"approved_plan":{"files_impacted":["note.go"]}}`)
	for _, path := range []string{"other.go", "../note.go", ".env", ".git/config"} {
		raw, _ := json.Marshal(ChangeProposal{Summary: "change", Patch: "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n@@ -1 +1 @@\n-a\n+b\n"})
		if validateAgentPatchScope(delivery, string(raw)) == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}
func TestReviewedDiffDigestIncludesTailBeyondLogLimit(t *testing.T) {
	root := testRepository(t)
	path := filepath.Join(root, "large.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "initial")
	base := testGit(t, root, "rev-parse", "HEAD")
	prefix := strings.Repeat("long changed line to exceed truncated command output\n", 400)
	if err := os.WriteFile(path, []byte(prefix+"first tail\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := worktreeDiffSHA256(context.Background(), root, base, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(prefix+"other tail\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := worktreeDiffSHA256(context.Background(), root, base, false)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("tail change invisible to fingerprint")
	}
	testGit(t, root, "add", ".")
	staged, err := worktreeDiffSHA256(context.Background(), root, base, true)
	if err != nil || staged != second {
		t.Fatalf("staged digest differs: %s %s %v", staged, second, err)
	}
}
