package automationagent

import (
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

	"events-stocks/models"
	"github.com/gofrs/uuid"
)

func stepCallbackUUID() string { return uuid.Must(uuid.NewV4()).String() }

func stepCallbackClaimRequest() PlanStepClaimRequest {
	return PlanStepClaimRequest{
		TaskID: stepCallbackUUID(), PlanID: stepCallbackUUID(),
		RunID: stepCallbackUUID(), WorkerID: stepCallbackUUID(), AgentKey: "generalist", MachineID: stepCallbackUUID(), LeaseSeconds: 60,
	}
}

func stepCallbackDTO(stepID, planID, status string) PlanStepDTO {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	return PlanStepDTO{
		ID: stepID, PlanID: planID, PlanVersion: 3, StepKey: "prepare", Order: 0, Title: "Prepare",
		Role:      models.DeliveryPlanStepRoleImplementation,
		Objective: "Prepare the isolated worktree", AcceptanceCriteria: []string{"Worktree is ready"}, DependsOn: []string{},
		Status: status, AgentKey: "generalist", CreatedAt: now, UpdatedAt: now,
	}
}

func newStepCallbackForTest(t *testing.T, server *httptest.Server) *HTTPCallback {
	t.Helper()
	return newTestHTTPCallback(t, server)
}

func TestPlanStepCallbackUsesAuthenticatedRoutesAndExpectedEnvelopes(t *testing.T) {
	claimRequest := stepCallbackClaimRequest()
	stepID := stepCallbackUUID()
	claimRequest.StepID = stepID
	identity, instanceID := newTestMachineIdentity(t)
	leaseExpiry := time.Date(2026, 9, 23, 12, 1, 0, 0, time.UTC)
	claimStep := stepCallbackDTO(stepID, claimRequest.PlanID, "running")
	claimStep.AutomationTaskID = claimRequest.TaskID
	var got []struct {
		Method       string
		Path         string
		LegacySecret string
		Body         map[string]any
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 70<<10))
		if err != nil {
			t.Errorf("could not read test request")
			return
		}
		assertSignedCallbackRequest(t, r, body, identity, instanceID)
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("invalid JSON request: %s", err)
			return
		}
		got = append(got, struct {
			Method       string
			Path         string
			LegacySecret string
			Body         map[string]any
		}{r.Method, r.URL.Path, r.Header.Get("X-Automation-Secret"), decoded})
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/internal/automation/steps/claim":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"available": true, "step": claimStep,
				"fencing_token": "7", "lease_expires_at": leaseExpiry,
			}})
		case "/api/internal/automation/steps/" + stepID + "/lease":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"lease_expires_at": leaseExpiry}})
		case "/api/internal/automation/steps/" + stepID:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": stepCallbackDTO(stepID, claimRequest.PlanID, "completed")})
		default:
			t.Errorf("unexpected step callback path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	callback := newTestCallbackWithIdentity(t, server, identity, instanceID)

	claim, err := callback.ClaimPlanStep(context.Background(), claimRequest)
	if err != nil {
		t.Fatalf("ClaimPlanStep returned an error: %v", err)
	}
	if !claim.Available || claim.Step == nil || claim.Step.ID != stepID || claim.Step.Status != "running" || claim.FencingToken != "7" || !claim.LeaseExpiresAt.Equal(leaseExpiry) {
		t.Fatalf("unexpected claim response: %#v", claim)
	}
	lease, err := callback.RenewPlanStepLease(context.Background(), PlanStepLeaseRequest{
		StepID: stepID, TaskID: claimRequest.TaskID, RunID: claimRequest.RunID, WorkerID: claimRequest.WorkerID,
		AgentKey: claimRequest.AgentKey, MachineID: claimRequest.MachineID, LeaseSeconds: claimRequest.LeaseSeconds, FencingToken: claim.FencingToken,
	})
	if err != nil || !lease.LeaseExpiresAt.Equal(leaseExpiry) {
		t.Fatalf("RenewPlanStepLease = %#v, %v", lease, err)
	}
	updated, err := callback.UpdatePlanStepStatus(context.Background(), PlanStepStatusRequest{
		StepID: stepID, TaskID: claimRequest.TaskID, RunID: claimRequest.RunID, WorkerID: claimRequest.WorkerID,
		AgentKey: claimRequest.AgentKey, MachineID: claimRequest.MachineID,
		FencingToken: claim.FencingToken, Status: "completed",
	})
	if err != nil || updated.ID != stepID || updated.Status != "completed" {
		t.Fatalf("UpdatePlanStepStatus = %#v, %v", updated, err)
	}
	if len(got) != 3 {
		t.Fatalf("received %d requests, want 3", len(got))
	}
	if got[0].Body["step_id"] != stepID {
		t.Fatalf("targeted claim omitted step_id: %#v", got[0].Body)
	}
	want := []struct{ method, path string }{
		{http.MethodPost, "/api/internal/automation/steps/claim"},
		{http.MethodPut, "/api/internal/automation/steps/" + stepID + "/lease"},
		{http.MethodPut, "/api/internal/automation/steps/" + stepID},
	}
	for index, request := range got {
		if request.Method != want[index].method || request.Path != want[index].path || request.LegacySecret != "" {
			t.Fatalf("request %d did not match its signed contract", index)
		}
		if _, leaked := request.Body["callback_secret"]; leaked || strings.Contains(fmt.Sprint(request.Body), "callback-master-test-value") {
			t.Fatalf("request %d included the callback secret in its body", index)
		}
	}
	if got[0].Body["task_id"] != claimRequest.TaskID || got[0].Body["lease_seconds"] != float64(60) {
		t.Fatalf("claim request fields were not serialized correctly: %#v", got[0].Body)
	}
	if got[1].Body["fencing_token"] != "7" || got[1].Body["lease_seconds"] != float64(claimRequest.LeaseSeconds) || got[2].Body["status"] != "completed" {
		t.Fatal("lease or status request omitted its fencing/status fields")
	}
	for _, index := range []int{1, 2} {
		if got[index].Body["agent_key"] != claimRequest.AgentKey || got[index].Body["machine_id"] != claimRequest.MachineID {
			t.Fatalf("request %d omitted the claiming worker's identity: %#v", index, got[index].Body)
		}
	}
}

func TestPlanStepDependencyPatchCallbacksUseSignedManifestAndRawPatchRoutes(t *testing.T) {
	lease := PlanStepLeaseRequest{
		StepID: stepCallbackUUID(), TaskID: stepCallbackUUID(), RunID: stepCallbackUUID(), WorkerID: stepCallbackUUID(),
		AgentKey: "generalist", MachineID: stepCallbackUUID(), FencingToken: "8",
	}
	patch := []byte("diff --git a/file.bin b/file.bin\nGIT binary patch\n")
	patchDigest := sha256.Sum256(patch)
	patchSHA := hex.EncodeToString(patchDigest[:])
	reference := models.DeliveryPlanStepDependencyPatchReference{
		DependencyStepID: stepCallbackUUID(), RepositoryRef: "workspace://backend",
		BaseSHA: strings.Repeat("a", 40), SHA256: patchSHA, SizeBytes: int64(len(patch)),
	}
	manifestSHA, err := models.DeliveryPlanStepDependencyPatchManifestSHA256([]models.DeliveryPlanStepDependencyPatchReference{reference})
	if err != nil {
		t.Fatal(err)
	}
	identity, instanceID := newTestMachineIdentity(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 70<<10))
		if readErr != nil {
			t.Errorf("could not read dependency patch request")
			return
		}
		assertSignedCallbackRequest(t, r, body, identity, instanceID)
		var gotLease PlanStepLeaseRequest
		if json.Unmarshal(body, &gotLease) != nil || gotLease.TaskID != lease.TaskID || gotLease.RunID != lease.RunID || gotLease.WorkerID != lease.WorkerID || gotLease.AgentKey != lease.AgentKey || gotLease.MachineID != lease.MachineID || gotLease.FencingToken != lease.FencingToken {
			t.Errorf("signed callback did not bind the exact active lease")
		}
		calls++
		switch r.URL.Path {
		case "/api/internal/automation/steps/" + lease.StepID + "/dependency-patches/manifest":
			if r.Method != http.MethodPost {
				t.Errorf("manifest callback method = %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": PlanStepDependencyPatchManifest{
				ManifestSHA256: manifestSHA, PatchCount: 1, TotalSizeBytes: int64(len(patch)), Patches: []models.DeliveryPlanStepDependencyPatchReference{reference},
			}})
		case "/api/internal/automation/steps/" + lease.StepID + "/dependency-patches/" + patchSHA:
			if r.Method != http.MethodPost {
				t.Errorf("patch callback method = %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/vnd.git-patch")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(patch)
		default:
			t.Errorf("unexpected dependency patch path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	callback := newTestCallbackWithIdentity(t, server, identity, instanceID)
	manifest, err := callback.GetPlanStepDependencyPatchManifest(context.Background(), lease)
	if err != nil || manifest.ManifestSHA256 != manifestSHA || manifest.PatchCount != 1 || len(manifest.Patches) != 1 {
		t.Fatalf("manifest callback = %#v, %v", manifest, err)
	}
	gotPatch, err := callback.GetPlanStepDependencyPatch(context.Background(), lease, patchSHA)
	if err != nil || string(gotPatch) != string(patch) {
		t.Fatalf("raw dependency patch callback = %q, %v", gotPatch, err)
	}
	wipeBytes(gotPatch)
	if calls != 2 {
		t.Fatalf("got %d signed dependency patch requests, want 2", calls)
	}
}

func TestPlanStepDependencyPatchManifestRejectsTamperedMetadata(t *testing.T) {
	emptyDigest, err := models.DeliveryPlanStepDependencyPatchManifestSHA256(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePlanStepDependencyPatchManifest(PlanStepDependencyPatchManifest{ManifestSHA256: emptyDigest, PatchCount: 0, TotalSizeBytes: 0}); err != nil {
		t.Fatalf("valid empty manifest rejected: %v", err)
	}
	if err := validatePlanStepDependencyPatchManifest(PlanStepDependencyPatchManifest{ManifestSHA256: strings.Repeat("0", 64), PatchCount: 0}); err == nil {
		t.Fatal("tampered empty manifest digest was accepted")
	}
	patch := []byte("patch")
	digest := sha256.Sum256(patch)
	reference := models.DeliveryPlanStepDependencyPatchReference{
		DependencyStepID: stepCallbackUUID(), RepositoryRef: "workspace://backend", BaseSHA: strings.Repeat("b", 40),
		SHA256: hex.EncodeToString(digest[:]), SizeBytes: int64(len(patch)),
	}
	manifestSHA, err := models.DeliveryPlanStepDependencyPatchManifestSHA256([]models.DeliveryPlanStepDependencyPatchReference{reference})
	if err != nil {
		t.Fatal(err)
	}
	manifest := PlanStepDependencyPatchManifest{ManifestSHA256: manifestSHA, PatchCount: 1, TotalSizeBytes: int64(len(patch)), Patches: []models.DeliveryPlanStepDependencyPatchReference{reference}}
	if err := validatePlanStepDependencyPatchManifest(manifest); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	manifest.TotalSizeBytes++
	if err := validatePlanStepDependencyPatchManifest(manifest); err == nil {
		t.Fatal("tampered total byte count was accepted")
	}
	manifest.TotalSizeBytes--
	manifest.Patches[0].RepositoryRef = "workspace://other"
	if err := validatePlanStepDependencyPatchManifest(manifest); err == nil {
		t.Fatal("tampered manifest metadata was accepted")
	}
}

func TestPlanStepDependencyPatchDownloadRejectsTamperedBytesAndUnsafeHeaders(t *testing.T) {
	lease := PlanStepLeaseRequest{StepID: stepCallbackUUID(), TaskID: stepCallbackUUID(), RunID: stepCallbackUUID(), WorkerID: stepCallbackUUID(), AgentKey: "generalist", MachineID: stepCallbackUUID(), FencingToken: "10"}
	patch := []byte("original patch")
	digest := sha256.Sum256(patch)
	sha := hex.EncodeToString(digest[:])
	for _, test := range []struct {
		name        string
		body        []byte
		cacheHeader string
	}{
		{name: "digest tamper", body: []byte("changed bytes"), cacheHeader: "no-store"},
		{name: "cacheable", body: patch, cacheHeader: "public, max-age=60"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.git-patch")
				w.Header().Set("Cache-Control", test.cacheHeader)
				w.Header().Set("X-Content-Type-Options", "nosniff")
				_, _ = w.Write(test.body)
			}))
			defer server.Close()
			callback := newStepCallbackForTest(t, server)
			got, err := callback.GetPlanStepDependencyPatch(context.Background(), lease, sha)
			if err == nil {
				wipeBytes(got)
				t.Fatal("tampered or cacheable patch response was accepted")
			}
		})
	}
}

func TestPlanStepClaimRetriesUseStableRunIdentityAndReturnSameClaim(t *testing.T) {
	claimRequest := stepCallbackClaimRequest()
	stepID := stepCallbackUUID()
	leaseExpiry := time.Date(2026, 9, 23, 12, 1, 0, 0, time.UTC)
	claimStep := stepCallbackDTO(stepID, claimRequest.PlanID, "running")
	claimStep.AutomationTaskID = claimRequest.TaskID
	var firstBody string
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls++
		if calls == 1 {
			firstBody = string(body)
		} else if string(body) != firstBody {
			t.Error("retry changed the task/run claim identity")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"available": true, "step": claimStep, "fencing_token": "9", "lease_expires_at": leaseExpiry,
		}})
	}))
	defer server.Close()
	callback := newStepCallbackForTest(t, server)

	first, err := callback.ClaimPlanStep(context.Background(), claimRequest)
	if err != nil {
		t.Fatal("first claim retry test call failed")
	}
	second, err := callback.ClaimPlanStep(context.Background(), claimRequest)
	if err != nil {
		t.Fatal("second claim retry test call failed")
	}
	if calls != 2 || first.Step == nil || second.Step == nil || first.Step.ID != second.Step.ID || first.FencingToken != second.FencingToken || first.FencingToken != "9" {
		t.Fatal("same run identity did not preserve the idempotent claim")
	}
}

func TestPlanStepClaimRejectsResponseAttributedToDifferentPlanTaskOrAgent(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*PlanStepDTO)
	}{
		{
			name: "different plan",
			mutate: func(step *PlanStepDTO) {
				step.PlanID = stepCallbackUUID()
			},
		},
		{
			name: "different task",
			mutate: func(step *PlanStepDTO) {
				step.AutomationTaskID = stepCallbackUUID()
			},
		},
		{
			name: "different agent profile",
			mutate: func(step *PlanStepDTO) {
				step.AgentKey = "specialist"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			claimRequest := stepCallbackClaimRequest()
			stepID := stepCallbackUUID()
			step := stepCallbackDTO(stepID, claimRequest.PlanID, "running")
			step.AutomationTaskID = claimRequest.TaskID
			step.AgentKey = claimRequest.AgentKey
			test.mutate(&step)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
					"available": true, "step": step, "fencing_token": "9",
					"lease_expires_at": time.Now().UTC().Add(time.Minute),
				}})
			}))
			defer server.Close()
			callback := newStepCallbackForTest(t, server)
			if claim, err := callback.ClaimPlanStep(context.Background(), claimRequest); err == nil || claim.Available {
				t.Fatalf("mismatched claim attribution was accepted: %#v", claim)
			}
		})
	}
}

func TestPlanStepClaimRejectsNonCanonicalFencingTokens(t *testing.T) {
	for _, token := range []string{"fence-7", "0", "-1", "+1", "01", "9223372036854775808", " 1"} {
		t.Run(token, func(t *testing.T) {
			claimRequest := stepCallbackClaimRequest()
			stepID := stepCallbackUUID()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
					"available": true, "step": stepCallbackDTO(stepID, claimRequest.PlanID, "running"),
					"fencing_token": token, "lease_expires_at": time.Now().UTC().Add(time.Minute),
				}})
			}))
			defer server.Close()
			callback := newStepCallbackForTest(t, server)
			if claim, err := callback.ClaimPlanStep(context.Background(), claimRequest); err == nil || claim.Available {
				t.Fatalf("malformed fencing token %q was accepted: %#v", token, claim)
			}
		})
	}
}

func TestPlanStepClaimReportsNoReadyStepWithoutAClaimToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"available": false}})
	}))
	defer server.Close()
	callback := newStepCallbackForTest(t, server)
	claim, err := callback.ClaimPlanStep(context.Background(), stepCallbackClaimRequest())
	if err != nil || claim.Available || claim.Step != nil || claim.FencingToken != "" || !claim.LeaseExpiresAt.IsZero() {
		t.Fatalf("empty claim response was not handled: %#v, %v", claim, err)
	}
}

func TestPlanStepCallbackValidatesLimitsAndStatusesBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	callback := newStepCallbackForTest(t, server)

	claim := stepCallbackClaimRequest()
	claim.LeaseSeconds = maxPlanStepLeaseSeconds + 1
	if _, err := callback.ClaimPlanStep(context.Background(), claim); err == nil {
		t.Fatal("oversized lease was accepted")
	}
	claim = stepCallbackClaimRequest()
	claim.LeaseSeconds = 14
	if err := validatePlanStepClaimRequest(claim); err == nil {
		t.Fatal("14-second lease was accepted")
	}
	claim.LeaseSeconds = 15
	if err := validatePlanStepClaimRequest(claim); err != nil {
		t.Fatalf("15-second lease was rejected: %v", err)
	}
	claim = stepCallbackClaimRequest()
	claim.AgentKey = "a"
	if err := validatePlanStepClaimRequest(claim); err == nil {
		t.Fatal("one-character agent key was accepted")
	}
	claim.AgentKey = "ab"
	if err := validatePlanStepClaimRequest(claim); err != nil {
		t.Fatalf("two-character agent key was rejected: %v", err)
	}
	claim = stepCallbackClaimRequest()
	claim.MachineID = "machine-local-01"
	if err := validatePlanStepClaimRequest(claim); err == nil {
		t.Fatal("non-UUID machine id was accepted")
	}
	claim.MachineID = stepCallbackUUID()
	if err := validatePlanStepClaimRequest(claim); err != nil {
		t.Fatalf("valid UUID machine id was rejected: %v", err)
	}
	claim.StepID = "not-a-uuid"
	if err := validatePlanStepClaimRequest(claim); err == nil {
		t.Fatal("invalid targeted step id was accepted")
	}
	base := PlanStepLeaseRequest{
		StepID: stepCallbackUUID(), TaskID: stepCallbackUUID(), RunID: stepCallbackUUID(), WorkerID: stepCallbackUUID(),
		AgentKey: "generalist", MachineID: stepCallbackUUID(), FencingToken: "7",
	}
	for _, token := range []string{"fence", "0", "-1", "+7", "07", "9223372036854775808", " 7"} {
		invalidLease := base
		invalidLease.FencingToken = token
		if err := validatePlanStepLeaseRequest(invalidLease); err == nil {
			t.Errorf("invalid fencing token %q was accepted for a callback", token)
		}
	}
	if _, err := callback.RenewPlanStepLease(context.Background(), PlanStepLeaseRequest{
		StepID: base.StepID, TaskID: base.TaskID, RunID: base.RunID, WorkerID: base.WorkerID,
		AgentKey: base.AgentKey, MachineID: base.MachineID,
	}); err == nil {
		t.Fatal("missing fencing token was accepted")
	}
	invalidLease := base
	invalidLease.AgentKey = "a"
	if err := validatePlanStepLeaseRequest(invalidLease); err == nil {
		t.Fatal("invalid agent key was accepted for lease renewal")
	}
	invalidLease = base
	invalidLease.MachineID = "machine-local-01"
	if err := validatePlanStepLeaseRequest(invalidLease); err == nil {
		t.Fatal("non-UUID machine identity was accepted for lease renewal")
	}
	optionalMachine := base
	optionalMachine.MachineID = ""
	if err := validatePlanStepLeaseRequest(optionalMachine); err != nil {
		t.Fatalf("omitted machine identity must remain optional: %v", err)
	}
	update := PlanStepStatusRequest{
		StepID: base.StepID, TaskID: base.TaskID, RunID: base.RunID, WorkerID: base.WorkerID,
		AgentKey: base.AgentKey, MachineID: base.MachineID, FencingToken: base.FencingToken,
	}
	for _, status := range []string{"planned", "ready", "skipped"} {
		update.Status = status
		if _, err := callback.UpdatePlanStepStatus(context.Background(), update); err == nil {
			t.Fatalf("non-worker status %q was accepted", status)
		}
	}
	update.Status = "completed"
	update.AgentKey = "a"
	if _, err := callback.UpdatePlanStepStatus(context.Background(), update); err == nil {
		t.Fatal("invalid agent key was accepted for a status update")
	}
	update.AgentKey = base.AgentKey
	update.MachineID = "machine-local-01"
	if _, err := callback.UpdatePlanStepStatus(context.Background(), update); err == nil {
		t.Fatal("non-UUID machine identity was accepted for a status update")
	}
	if calls != 0 {
		t.Fatalf("invalid requests reached the server %d times", calls)
	}
}

func TestPlanStepDTOAcceptsSingleCharacterStepKey(t *testing.T) {
	step := stepCallbackDTO(stepCallbackUUID(), stepCallbackUUID(), "planned")
	step.StepKey = "a"
	if err := validatePlanStepDTO(step); err != nil {
		t.Fatalf("single-character step key accepted by the control plane was rejected: %v", err)
	}
	step.StepKey = "A"
	if err := validatePlanStepDTO(step); err == nil {
		t.Fatal("uppercase step key must remain invalid")
	}
}

func TestPlanStepCallbackMapsFencedConflictWithoutExposingResponseBody(t *testing.T) {
	privateMarker := "PRIVATE-PLAN-ERROR-MUST-NOT-LEAK"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, privateMarker, http.StatusConflict)
	}))
	defer server.Close()
	callback := newStepCallbackForTest(t, server)
	request := PlanStepLeaseRequest{StepID: stepCallbackUUID(), TaskID: stepCallbackUUID(), RunID: stepCallbackUUID(), WorkerID: stepCallbackUUID(), AgentKey: "generalist", FencingToken: "11"}
	_, err := callback.RenewPlanStepLease(context.Background(), request)
	if !errors.Is(err, ErrPlanStepLeaseLost) || strings.Contains(err.Error(), privateMarker) || strings.Contains(err.Error(), "callback-master-test-value") {
		t.Fatalf("fenced conflict response was not safely mapped: %v", err)
	}
}

func TestPlanStepCallbackRejectsCrossHostRedirectWithoutForwardingSecret(t *testing.T) {
	destinationCalls := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls++
		if r.Header.Get("X-Automation-Secret") != "" {
			t.Error("legacy callback shared secret followed a redirect")
		}
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/redirected", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	callback := newStepCallbackForTest(t, origin)

	_, err := callback.ClaimPlanStep(context.Background(), stepCallbackClaimRequest())
	if err == nil || !strings.Contains(err.Error(), "(307)") || destinationCalls != 0 || strings.Contains(err.Error(), "callback-master-test-value") {
		t.Fatalf("redirect handling did not fail closed: err=%v destination calls=%d", err, destinationCalls)
	}
}

func TestPlanStepCallbackBoundsAndValidatesResponseEnvelope(t *testing.T) {
	for _, test := range []struct {
		name string
		body func() string
	}{
		{name: "missing data", body: func() string { return `{"message":"PRIVATE-ENVELOPE-MARKER"}` }},
		{name: "oversized", body: func() string {
			return `{"data":"PRIVATE-ENVELOPE-MARKER"}` + strings.Repeat(" ", maxPlanStepCallbackResponseBytes)
		}},
		{name: "invalid claim data", body: func() string { return `{"data":{"step":{},"fencing_token":"","lease_expires_at":""}}` }},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, test.body())
			}))
			defer server.Close()
			callback := newStepCallbackForTest(t, server)
			_, err := callback.ClaimPlanStep(context.Background(), stepCallbackClaimRequest())
			if err == nil || strings.Contains(err.Error(), "PRIVATE-ENVELOPE-MARKER") || strings.Contains(err.Error(), "callback-master-test-value") {
				t.Fatalf("invalid response was not safely rejected: %v", err)
			}
		})
	}
}
