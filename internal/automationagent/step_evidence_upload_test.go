package automationagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/services/deliveryplansteps"
)

func TestUploadPlanStepEvidenceSignsURIQueryMIMEAndRawBytes(t *testing.T) {
	identity, instanceID := newTestMachineIdentity(t)
	lease := PlanStepLeaseRequest{
		StepID: stepCallbackUUID(), TaskID: stepCallbackUUID(), RunID: stepCallbackUUID(), WorkerID: stepCallbackUUID(),
		AgentKey: "generalist", MachineID: identity.machineID, FencingToken: "11",
	}
	body := []byte(`{"safe":true}`)
	contentType := "application/json"
	eventID := stepCallbackUUID()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("raw request body could not be read: %v", err)
		}
		if !bytes.Equal(gotBody, body) {
			t.Errorf("upload body changed: got %q", gotBody)
		}
		assertSignedCallbackRequest(t, r, body, identity, instanceID)
		query := r.URL.Query()
		for key, want := range map[string]string{
			"task_id": lease.TaskID, "run_id": lease.RunID, "worker_id": lease.WorkerID,
			"agent_key": lease.AgentKey, "machine_id": lease.MachineID, "agent_instance_id": instanceID,
			"fencing_token": lease.FencingToken, "event_id": eventID, "requirement_key": "acceptance-report",
			"file_name": "acceptance-report.json", "content_type": contentType,
		} {
			if query.Get(key) != want {
				t.Errorf("signed evidence query %s = %q, want %q", key, query.Get(key), want)
			}
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/internal/automation/steps/"+lease.StepID+"/evidence" || r.Header.Get("Content-Type") != query.Get("content_type") || r.Header.Get("X-Content-SHA256") != fmt.Sprintf("%x", sha256.Sum256(body)) {
			t.Errorf("request route or MIME/digest binding is incorrect: method=%s path=%s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": PlanStepEvidenceUploadReceipt{
			ID: stepCallbackUUID(), StepID: lease.StepID, RequirementKey: "acceptance-report", FileName: "acceptance-report.json",
			ContentType: contentType, SizeBytes: int64(len(body)), SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), CreatedAt: time.Now().UTC(), Idempotent: true,
		}})
	}))
	defer server.Close()
	callback := newTestCallbackWithIdentity(t, server, identity, instanceID)
	receipt, err := callback.UploadPlanStepEvidence(context.Background(), lease, "acceptance-report", "acceptance-report.json", contentType, eventID, body)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.StepID != lease.StepID || receipt.RequirementKey != "acceptance-report" || receipt.SizeBytes != int64(len(body)) || !receipt.Idempotent {
		t.Fatalf("unexpected safe upload receipt: %#v", receipt)
	}
}

func TestUploadPlanStepEvidenceRejectsOversizeAndUnapprovedMIMEBeforeNetwork(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	identity, instanceID := newTestMachineIdentity(t)
	callback := newTestCallbackWithIdentity(t, server, identity, instanceID)
	lease := PlanStepLeaseRequest{
		StepID: stepCallbackUUID(), TaskID: stepCallbackUUID(), RunID: stepCallbackUUID(), WorkerID: stepCallbackUUID(),
		AgentKey: "generalist", MachineID: stepCallbackUUID(), FencingToken: "1",
	}
	validID := stepCallbackUUID()
	if _, err := callback.UploadPlanStepEvidence(context.Background(), lease, "report", "report.json", "application/json", validID, make([]byte, maxPlanStepEvidenceUploadBytes+1)); err == nil {
		t.Fatal("oversized evidence was accepted")
	}
	if _, err := callback.UploadPlanStepEvidence(context.Background(), lease, "report", "report.json", "text/html", validID, []byte("<script>")); err == nil {
		t.Fatal("unapproved MIME type was accepted")
	}
	if _, err := callback.UploadPlanStepEvidence(context.Background(), lease, "report", "report.json", "application/json", validID, []byte(`{"api_key":"sk-proj-abcdefghijklmnopqrstuvwxyz123456"}`)); err == nil {
		t.Fatal("credential-bearing evidence was accepted")
	}
	if requests != 0 {
		t.Fatalf("invalid uploads reached the network: %d", requests)
	}
}

func TestGeneratedEvidenceReportIsPrivateMinimalAndIdempotent(t *testing.T) {
	step := &PlanStepDTO{ID: stepCallbackUUID(), PlanID: stepCallbackUUID(), PlanVersion: 2, StepKey: "build", AcceptanceCriteria: []string{"must not be copied into an artifact"}}
	evidence := planStepEvidence{
		PlanID: step.PlanID, PlanVersion: step.PlanVersion, StepID: step.ID, StepKey: step.StepKey,
		AcceptanceCriteria: []string{"must not be copied into an artifact"}, ReviewDiffSHA256: strings.Repeat("a", 64),
		Checks: []map[string]any{{"criterion": "sensitive user text must not be copied", "passed": true, "output": "sensitive command output"}},
	}
	first, err := encodePlanStepEvidenceReport(step, evidence)
	if err != nil {
		t.Fatal(err)
	}
	second, err := encodePlanStepEvidenceReport(step, evidence)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("same verified artifact did not serialize deterministically: %v", err)
	}
	for _, forbidden := range []string{"must not be copied", "sensitive user text", "sensitive command output"} {
		if strings.Contains(string(first), forbidden) {
			t.Fatalf("generated evidence copied free-form text: %s", first)
		}
	}
	digest := sha256.Sum256(first)
	sha := hex.EncodeToString(digest[:])
	lease := PlanStepLeaseRequest{
		TaskID: stepCallbackUUID(), RunID: stepCallbackUUID(), StepID: step.ID,
		WorkerID: stepCallbackUUID(), AgentKey: "generalist", MachineID: stepCallbackUUID(), FencingToken: "41",
	}
	firstID := planStepEvidenceEventID(lease, "acceptance-report", sha)
	retryID := planStepEvidenceEventID(lease, "acceptance-report", sha)
	if firstID != retryID || !validStepCallbackUUID(firstID) {
		t.Fatalf("same-fence evidence retries must reuse a valid deterministic event id: %q / %q", firstID, retryID)
	}

	takeoverLease := lease
	takeoverLease.FencingToken = "42"
	takeoverID := planStepEvidenceEventID(takeoverLease, "acceptance-report", sha)
	if firstID == takeoverID || !validStepCallbackUUID(takeoverID) {
		t.Fatalf("a new fence must receive a distinct valid event id for the same artifact: %q / %q", firstID, takeoverID)
	}

	otherTaskLease := lease
	otherTaskLease.TaskID = stepCallbackUUID()
	if firstID == planStepEvidenceEventID(otherTaskLease, "acceptance-report", sha) {
		t.Fatal("evidence event id must be scoped to the task identity")
	}
}

type evidenceUploadTaskFake struct {
	TaskCallback
	err   error
	calls int
}

func (fake *evidenceUploadTaskFake) UploadPlanStepEvidence(_ context.Context, _ PlanStepLeaseRequest, _ string, _ string, _ string, _ string, body []byte) (PlanStepEvidenceUploadReceipt, error) {
	fake.calls++
	if len(body) == 0 || scanPlanStepEvidenceBytes(body) != nil {
		return PlanStepEvidenceUploadReceipt{}, errors.New("unsafe evidence")
	}
	if fake.err != nil {
		return PlanStepEvidenceUploadReceipt{}, fake.err
	}
	return PlanStepEvidenceUploadReceipt{ID: stepCallbackUUID()}, nil
}

func TestRequiredEvidenceUploadFailsClosedWhileOptionalUploadDoesNot(t *testing.T) {
	stepID, planID := stepCallbackUUID(), stepCallbackUUID()
	base := PlanStepDTO{ID: stepID, PlanID: planID, PlanVersion: 1, StepKey: "work"}
	evidence := planStepEvidence{PlanID: planID, PlanVersion: 1, StepID: stepID, StepKey: "work", AcceptanceCriteria: []string{"one"}, ReviewDiffSHA256: strings.Repeat("b", 64), Checks: []map[string]any{{"passed": true}}}
	newExecution := func(required bool, callbackErr error) (*planStepExecution, *evidenceUploadTaskFake) {
		claimed := base
		claimed.EvidenceRequirements = []deliveryplansteps.EvidenceRequirement{{Key: "acceptance-report", Title: "Acceptance report", Required: required, ContentTypes: []string{"application/json"}, MaxBytes: 1024}}
		fake := &evidenceUploadTaskFake{err: callbackErr}
		return &planStepExecution{
			claim: PlanStepClaim{Step: &claimed}, task: fake,
			lease:          PlanStepLeaseRequest{StepID: stepID, RunID: stepCallbackUUID()},
			leaseExpiresAt: time.Now().Add(time.Minute), ctx: context.Background(),
		}, fake
	}
	failedRequired, requiredCallback := newExecution(true, errors.New("transport error"))
	if err := uploadRequiredPlanStepEvidence(failedRequired, evidence); err == nil || requiredCallback.calls != 1 {
		t.Fatalf("required evidence failure did not block completion: err=%v calls=%d", err, requiredCallback.calls)
	}
	failedOptional, optionalCallback := newExecution(false, errors.New("transport error"))
	if err := uploadRequiredPlanStepEvidence(failedOptional, evidence); err != nil || optionalCallback.calls != 1 {
		t.Fatalf("optional evidence upload failure should not block completion: err=%v calls=%d", err, optionalCallback.calls)
	}
	unknownRequired, unknownCallback := newExecution(true, nil)
	unknownRequired.claim.Step.EvidenceRequirements[0].Key = "test-report"
	if err := uploadRequiredPlanStepEvidence(unknownRequired, evidence); err == nil || unknownCallback.calls != 0 {
		t.Fatalf("test-result evidence was fabricated from acceptance checks: err=%v calls=%d", err, unknownCallback.calls)
	}
	ambiguousRequired, ambiguousCallback := newExecution(true, nil)
	ambiguousRequired.claim.Step.EvidenceRequirements[0].Key = "report"
	if err := uploadRequiredPlanStepEvidence(ambiguousRequired, evidence); err == nil || ambiguousCallback.calls != 0 {
		t.Fatalf("ambiguous required evidence was guessed or uploaded: err=%v calls=%d", err, ambiguousCallback.calls)
	}
	acceptanceRequired, acceptanceCallback := newExecution(true, nil)
	if err := uploadRequiredPlanStepEvidence(acceptanceRequired, evidence); err != nil || acceptanceCallback.calls != 1 {
		t.Fatalf("verified acceptance report was not uploaded: err=%v calls=%d", err, acceptanceCallback.calls)
	}
}

func TestPlanStepDTOValidatesAndBindsEvidenceRequirements(t *testing.T) {
	step := stepCallbackDTO(stepCallbackUUID(), stepCallbackUUID(), "running")
	step.EvidenceRequirements = []deliveryplansteps.EvidenceRequirement{{Key: "test-report", Title: "Test report", Required: true, ContentTypes: []string{"application/json"}, MaxBytes: 4096}}
	if err := validatePlanStepDTO(step); err != nil {
		t.Fatalf("valid requirement rejected: %v", err)
	}
	changed := step
	changed.EvidenceRequirements = []deliveryplansteps.EvidenceRequirement{{Key: "test-report", Title: "Changed", Required: true, ContentTypes: []string{"application/json"}, MaxBytes: 4096}}
	if samePlanStepEvidenceRequirements(step.EvidenceRequirements, changed.EvidenceRequirements) {
		t.Fatal("claim comparison ignored a changed immutable evidence requirement")
	}
	step.EvidenceRequirements[0].ContentTypes = []string{"text/html"}
	if err := validatePlanStepDTO(step); err == nil {
		t.Fatal("unsupported evidence MIME type passed DTO validation")
	}
}
