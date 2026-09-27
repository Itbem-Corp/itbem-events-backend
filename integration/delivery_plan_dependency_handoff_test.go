//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"events-stocks/configuration"
	automation "events-stocks/controllers/automation"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// TestDeliveryPlanDependencyPatchHandoff exercises a real cross-instance,
// cross-machine handoff: one signed worker completes a dependency and registers
// its verified patch; a different signed worker claims the dependent step,
// obtains a metadata-only manifest, downloads the patch through the authorized
// stream endpoint, persists receipt-bound application evidence and completes.
// PostgreSQL is the disposable integration database and the AWS SDK uses a
// private in-memory S3-compatible endpoint.
func TestDeliveryPlanDependencyPatchHandoff(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "")

	const bucket = "itbem-dependency-patches-integration"
	var objectMu sync.Mutex
	objects := map[string][]byte{}
	var requestedKeys []string
	s3Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/" + bucket + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, prefix)
		objectMu.Lock()
		requestedKeys = append(requestedKeys, key)
		body, found := objects[key]
		body = append([]byte(nil), body...)
		objectMu.Unlock()
		if !found {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(s3Server.Close)

	previousS3Client := configuration.GetS3Client(nil)
	previousS3Region := configuration.GetS3Region()
	previousS3Endpoint, previousPathStyle := configuration.GetS3Endpoint()
	t.Cleanup(func() {
		configuration.SetS3Client(previousS3Client)
		configuration.SetS3Region(previousS3Region)
		configuration.SetS3Endpoint(previousS3Endpoint, previousPathStyle)
	})
	s3Context, cancelS3 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelS3()
	appConfig := &models.Config{
		AwsRegion: "us-east-1", S3Region: "us-east-1", S3ClientId: "integration",
		S3ClientSecret: "integration", S3Endpoint: s3Server.URL, S3UsePathStyle: "true",
		AutomationOutputBucket: bucket,
	}
	s3Client, region, err := configuration.BuildS3Client(s3Context, appConfig)
	require.NoError(t, err)
	configuration.SetS3Client(s3Client)
	configuration.SetS3Region(region)
	configuration.SetS3Endpoint(s3Server.URL, true)
	putObject := func(key string, body []byte) {
		objectMu.Lock()
		defer objectMu.Unlock()
		objects[key] = append([]byte(nil), body...)
	}

	// Reuse the established PostgreSQL plan fixture for the parent node, then
	// add one explicit dependent node and edge in the same approved plan.
	parentEventFixture := createStepEventFixture(t, db)
	var plan models.DeliveryPlan
	var parentStep models.DeliveryPlanStep
	require.NoError(t, db.First(&plan, "id = ?", parentEventFixture.PlanID).Error)
	require.NoError(t, db.First(&parentStep, "id = ?", parentEventFixture.StepID).Error)
	now := time.Now().UTC()
	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	const repositoryRef = "workspace://handofffixture"
	parentStep.AcceptanceCriteriaJSON = `["parent patch is ready"]`
	parentStep.UpdatedAt = now
	childStep := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "consume-parent-patch",
		IdempotencyKey: "consume-parent-patch-" + suffix, DisplayOrder: parentStep.DisplayOrder + 1,
		Title: "Apply dependency patch", Objective: "Use the verified parent patch on another machine",
		AcceptanceCriteriaJSON: `["dependency patch was applied"]`, Status: models.DeliveryPlanStepPlanned,
		CreatedBy: "integration", CreatedAt: now, UpdatedAt: now,
	}
	dependency := models.DeliveryPlanStepDependency{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: childStep.ID,
		DependsOnStepID: parentStep.ID, CreatedAt: now,
	}
	snapshot := models.DeliveryContextSnapshot{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: plan.WorkItemID, SourceID: uuid.Must(uuid.NewV4()),
		Kind: "repository", Name: "Dependency patch handoff fixture", Reference: repositoryRef,
		Revision: strings.Repeat("a", 40), CapturedAt: now, CreatedAt: now,
	}
	gate := models.DeliveryGate{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: plan.WorkItemID, Kind: "plan_approval", Decision: "approved",
		DecidedBy: "integration-reviewer", DecidedAt: now, CreatedAt: now,
	}
	workerA := makePlanFanoutWorker(t, "handoffalpha-"+suffix[:5], "handoff-source", plan.WorkItemID, "dependency-handoff-integration", now)
	workerB := makePlanFanoutWorker(t, "handoffbeta-"+suffix[:5], "handoff-consumer", plan.WorkItemID, "dependency-handoff-integration", now)
	workers := []*planFanoutWorker{&workerA, &workerB}
	leaseExpiry := now.Add(10 * time.Minute)
	for _, worker := range workers {
		worker.task.Status = "running"
		worker.task.LeaseExpiresAt = &leaseExpiry
		worker.task.AttemptCount = 1
		worker.task.RunID = worker.runID.String()
		worker.task.WorkerID = worker.heartbeat.WorkerID
		worker.task.AgentKey = worker.profile.AgentKey
		worker.task.MachineID = worker.heartbeat.MachineID
		worker.instance.Status = "active"
		worker.instance.MachineID = worker.heartbeat.MachineID
		worker.profile.CreatedAt, worker.profile.UpdatedAt = now, now
		worker.heartbeat.CreatedAt, worker.heartbeat.UpdatedAt = now, now
		worker.instance.CreatedAt, worker.instance.UpdatedAt = now, now
		worker.task.CreatedAt, worker.task.UpdatedAt = now, now
	}
	require.NotEqual(t, workerA.heartbeat.MachineID, workerB.heartbeat.MachineID, "parent and consumer must run on different local machines")
	parentTaskID := workerA.task.ID
	parentStep.Status = models.DeliveryPlanStepRunning
	parentStep.AutomationTaskID = &parentTaskID
	parentStep.RunID, parentStep.WorkerID = workerA.runID.String(), workerA.heartbeat.WorkerID
	parentStep.AgentKey, parentStep.MachineID = workerA.profile.AgentKey, workerA.heartbeat.MachineID
	parentStep.LeaseFence, parentStep.LeaseExpiresAt = 9, &leaseExpiry
	plan.Status, plan.ApprovedGateID = "approved", &gate.ID
	for _, value := range []any{&snapshot, &gate, &childStep, &dependency, &workerA.profile, &workerA.heartbeat, &workerA.instance, &workerA.task, &workerB.profile, &workerB.heartbeat, &workerB.instance, &workerB.task} {
		require.NoError(t, db.Create(value).Error)
	}
	require.NoError(t, db.Model(&models.DeliveryPlan{}).Where("id = ?", plan.ID).Updates(map[string]any{
		"status": plan.Status, "approved_gate_id": gate.ID,
	}).Error)
	require.NoError(t, db.Model(&models.DeliveryPlanStep{}).Where("id = ?", parentStep.ID).Updates(map[string]any{
		"acceptance_criteria_json": parentStep.AcceptanceCriteriaJSON,
		"status":                   parentStep.Status, "automation_task_id": parentTaskID, "run_id": parentStep.RunID,
		"worker_id": parentStep.WorkerID, "agent_key": parentStep.AgentKey, "machine_id": parentStep.MachineID,
		"lease_fence": parentStep.LeaseFence, "lease_expires_at": leaseExpiry,
	}).Error)
	t.Cleanup(func() {
		instanceIDs := []uuid.UUID{workerA.instance.ID, workerB.instance.ID}
		workerIDs := []string{workerA.heartbeat.WorkerID, workerB.heartbeat.WorkerID}
		agentKeys := []string{workerA.profile.AgentKey, workerB.profile.AgentKey}
		taskIDs := []uuid.UUID{workerA.task.ID, workerB.task.ID}
		_ = db.Unscoped().Where("instance_id IN ?", instanceIDs).Delete(&models.AutomationAgentCallbackNonce{}).Error
		_ = db.Unscoped().Where("id IN ?", instanceIDs).Delete(&models.AutomationAgentInstance{}).Error
		_ = db.Unscoped().Where("worker_id IN ?", workerIDs).Delete(&models.AutomationAgentHeartbeat{}).Error
		_ = db.Unscoped().Where("id IN ?", taskIDs).Delete(&models.AutomationTask{}).Error
		_ = db.Unscoped().Where("agent_key IN ?", agentKeys).Delete(&models.AutomationAgentProfile{}).Error
	})

	router := echo.New()
	withConfig := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("config", appConfig)
			c.SetRequest(c.Request().WithContext(configuration.WithConfig(c.Request().Context(), appConfig)))
			return next(c)
		}
	}
	callbacks := router.Group("/api/internal/automation")
	callbacks.Use(automation.AgentCallbackAuthentication)
	callbacks.POST("/steps/claim", automation.ClaimDeliveryPlanStep, withConfig)
	callbacks.PUT("/steps/:id", automation.TransitionDeliveryPlanStep, withConfig)
	callbacks.POST("/steps/:id/activity", automation.RecordDeliveryPlanStepActivity, withConfig)
	callbacks.POST("/steps/:id/dependency-patches/manifest", automation.GetDeliveryPlanStepDependencyPatchManifest, withConfig)
	callbacks.POST("/steps/:id/dependency-patches/:sha256", automation.GetDeliveryPlanStepDependencyPatch, withConfig)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client := server.Client()

	const cleanPatch = "diff --git a/src/parent.go b/src/parent.go\nindex 1111111..2222222 100644\n--- a/src/parent.go\n+++ b/src/parent.go\n@@ -1 +1 @@\n-package before\n+package after\n"
	patchDigest := patchArtifactSHA256(cleanPatch)
	patchKey := patchArtifactObjectKey(workerA.task.ID, workerA.runID, parentStep.ID, patchDigest)
	patchReference := models.DeliveryPlanStepPatchArtifactReference{
		RepositoryRef: repositoryRef, BaseSHA: strings.Repeat("a", 40), SHA256: patchDigest, SizeBytes: int64(len(cleanPatch)),
	}
	artifactManifest, err := models.DeliveryPlanStepPatchArtifactManifestSHA256([]models.DeliveryPlanStepPatchArtifactReference{patchReference})
	require.NoError(t, err)
	patchRequest := func(stepID uuid.UUID, worker *planFanoutWorker, fence int64, sequence int64, ref models.DeliveryPlanStepPatchArtifactReference, manifestSHA, digest string, criterion string) planFanoutCallbackResult {
		checks := []map[string]any{{"criterion_sha256": planFanoutCriterionSHA256(criterion), "passed": true}}
		body, marshalErr := json.Marshal(map[string]any{
			"event_id": uuid.Must(uuid.NewV4()).String(), "task_id": worker.task.ID.String(), "run_id": worker.runID.String(),
			"worker_id": worker.heartbeat.WorkerID, "agent_key": worker.profile.AgentKey, "machine_id": worker.heartbeat.MachineID,
			"fencing_token": strconv.FormatInt(fence, 10), "sequence": sequence,
			"action": models.DeliveryPlanStepActivityEvidence, "phase": models.DeliveryPlanStepActivityCompleted,
			"details": map[string]any{
				"acceptance_checks": checks, "review_diff_sha256": manifestSHA,
				"patch_artifacts": []models.DeliveryPlanStepPatchArtifactReference{ref},
			},
		})
		if marshalErr != nil {
			return planFanoutCallbackResult{Status: http.StatusInternalServerError, Err: marshalErr}
		}
		return callPlanFanoutEndpoint(client, server.URL, http.MethodPost,
			"/api/internal/automation/steps/"+stepID.String()+"/activity", body, worker)
	}

	// A plausible secret-bearing unified diff is rejected and leaves no source
	// artifact row. The valid patch is then registered under the same source
	// worker lease and exercise sequence, proving fail-closed scanning.
	secretPatch := "diff --git a/src/config.go b/src/config.go\nindex 1111111..2222222 100644\n--- a/src/config.go\n+++ b/src/config.go\n@@ -1 +1 @@\n+API_KEY=integration-fake-value-123456\n"
	secretDigest := patchArtifactSHA256(secretPatch)
	secretReference := patchReference
	secretReference.SHA256, secretReference.SizeBytes = secretDigest, int64(len(secretPatch))
	secretKey := patchArtifactObjectKey(workerA.task.ID, workerA.runID, parentStep.ID, secretDigest)
	putObject(secretKey, []byte(secretPatch))
	secretManifest, manifestErr := models.DeliveryPlanStepPatchArtifactManifestSHA256([]models.DeliveryPlanStepPatchArtifactReference{secretReference})
	require.NoError(t, manifestErr)
	secretResult := patchRequest(parentStep.ID, &workerA, 9, 1, secretReference, secretManifest, secretDigest, "parent patch is ready")
	require.NoError(t, secretResult.Err)
	require.NotEqual(t, http.StatusOK, secretResult.Status, secretResult.Body)
	var sourceArtifactCount int64
	require.NoError(t, db.Model(&models.DeliveryPlanStepPatchArtifact{}).Where("step_id = ?", parentStep.ID).Count(&sourceArtifactCount).Error)
	require.Zero(t, sourceArtifactCount, "rejected secret content must not create artifact metadata")

	putObject(patchKey, []byte(cleanPatch))
	parentEvidence := patchRequest(parentStep.ID, &workerA, 9, 1, patchReference, artifactManifest, patchDigest, "parent patch is ready")
	require.NoError(t, parentEvidence.Err)
	require.Equalf(t, http.StatusOK, parentEvidence.Status, parentEvidence.Body)
	parentTransition := callPlanFanoutTransition(client, server.URL, parentStep.ID, &workerA, 9, models.DeliveryPlanStepCompleted)
	require.NoError(t, parentTransition.Err)
	require.Equalf(t, http.StatusOK, parentTransition.Status, parentTransition.Body)
	var parentArtifact models.DeliveryPlanStepPatchArtifact
	require.NoError(t, db.Where("step_id = ? AND sha256 = ?", parentStep.ID, patchDigest).First(&parentArtifact).Error)
	require.Equal(t, patchKey, parentArtifact.ObjectKey)

	claim, err := callPlanFanoutClaim(client, server.URL, plan, uuid.Nil, &workerB)
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, claim.Status, claim.Body)
	require.True(t, claim.Claim.Data.Available, "the completed dependency should release its directly dependent step")
	require.NotNil(t, claim.Claim.Data.Step)
	require.Equal(t, childStep.ID.String(), claim.Claim.Data.Step.ID)
	childFence, err := strconv.ParseInt(claim.Claim.Data.FencingToken, 10, 64)
	require.NoError(t, err)
	require.Positive(t, childFence)

	callbackBody := func(worker *planFanoutWorker, taskID, runID uuid.UUID, fence int64) []byte {
		body, marshalErr := json.Marshal(map[string]any{
			"task_id": taskID.String(), "run_id": runID.String(), "worker_id": worker.heartbeat.WorkerID,
			"agent_key": worker.profile.AgentKey, "machine_id": worker.heartbeat.MachineID,
			"fencing_token": strconv.FormatInt(fence, 10),
		})
		require.NoError(t, marshalErr)
		return body
	}
	manifestPath := "/api/internal/automation/steps/" + childStep.ID.String() + "/dependency-patches/manifest"
	manifestResponse := callPlanFanoutEndpoint(client, server.URL, http.MethodPost, manifestPath,
		callbackBody(&workerB, workerB.task.ID, workerB.runID, childFence), &workerB)
	require.NoError(t, manifestResponse.Err)
	require.Equalf(t, http.StatusOK, manifestResponse.Status, manifestResponse.Body)
	var envelope struct {
		Data struct {
			ManifestSHA256 string                                            `json:"manifest_sha256"`
			PatchCount     int                                               `json:"patch_count"`
			TotalSizeBytes int64                                             `json:"total_size_bytes"`
			Patches        []models.DeliveryPlanStepDependencyPatchReference `json:"patches"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(manifestResponse.Body), &envelope))
	require.Len(t, envelope.Data.Patches, 1)
	require.Equal(t, 1, envelope.Data.PatchCount)
	require.EqualValues(t, len(cleanPatch), envelope.Data.TotalSizeBytes)
	require.Equal(t, parentStep.ID.String(), envelope.Data.Patches[0].DependencyStepID)
	require.Equal(t, repositoryRef, envelope.Data.Patches[0].RepositoryRef)
	require.Equal(t, patchDigest, envelope.Data.Patches[0].SHA256)
	require.EqualValues(t, len(cleanPatch), envelope.Data.Patches[0].SizeBytes)
	expectedManifest, err := models.DeliveryPlanStepDependencyPatchManifestSHA256(envelope.Data.Patches)
	require.NoError(t, err)
	require.Equal(t, expectedManifest, envelope.Data.ManifestSHA256)
	require.NotContains(t, manifestResponse.Body, bucket, "safe manifest must not disclose the storage bucket")
	require.NotContains(t, manifestResponse.Body, patchKey, "safe manifest must not disclose the storage key")
	require.NotContains(t, manifestResponse.Body, cleanPatch, "manifest is metadata-only; bytes are available only from the stream endpoint")

	manifestReceiptCount := func() int64 {
		var count int64
		require.NoError(t, db.Model(&models.DeliveryPlanStepDependencyPatch{}).
			Where("step_id = ? AND automation_task_id = ? AND run_id = ? AND fencing_token = ?", childStep.ID, workerB.task.ID, workerB.runID.String(), childFence).
			Count(&count).Error)
		return count
	}
	requestedObjectCount := func() int {
		objectMu.Lock()
		defer objectMu.Unlock()
		return len(requestedKeys)
	}
	t.Run("rejects dependency step from another plan", func(t *testing.T) {
		foreignPlan := models.DeliveryPlan{
			ID: uuid.Must(uuid.NewV4()), WorkItemID: plan.WorkItemID, Version: plan.Version + 1,
			Status: "draft", Summary: "cross-plan dependency test", StructuredJSON: `{}`,
			ContextDigest: "cross-plan-" + suffix, ProposedBy: "integration", CreatedAt: now,
		}
		foreignStep := models.DeliveryPlanStep{
			ID: uuid.Must(uuid.NewV4()), PlanID: foreignPlan.ID, StepKey: "foreign-plan-step",
			IdempotencyKey: "foreign-plan-step-" + suffix, DisplayOrder: 1,
			Title: "Foreign plan dependency", Objective: "Must not be consumable across plans",
			AcceptanceCriteriaJSON: `[]`, Status: models.DeliveryPlanStepCompleted,
			CreatedBy: "integration", CreatedAt: now, UpdatedAt: now,
		}
		crossPlanEdge := models.DeliveryPlanStepDependency{
			ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: childStep.ID,
			DependsOnStepID: foreignStep.ID, CreatedAt: now,
		}
		require.NoError(t, db.Create(&foreignPlan).Error)
		require.NoError(t, db.Create(&foreignStep).Error)
		require.NoError(t, db.Create(&crossPlanEdge).Error)
		t.Cleanup(func() {
			db.Delete(&crossPlanEdge)
			db.Delete(&foreignStep)
			db.Delete(&foreignPlan)
		})

		readsBefore := requestedObjectCount()
		response := callPlanFanoutEndpoint(client, server.URL, http.MethodPost, manifestPath,
			callbackBody(&workerB, workerB.task.ID, workerB.runID, childFence), &workerB)
		require.NoError(t, response.Err)
		require.Equalf(t, http.StatusConflict, response.Status, response.Body)
		require.Zero(t, manifestReceiptCount(), "a cross-plan dependency must not create a handoff receipt")
		require.Equal(t, readsBefore, requestedObjectCount(), "the rejected manifest must not read patch bytes from object storage")
	})
	streamPath := func(digest string) string {
		return "/api/internal/automation/steps/" + childStep.ID.String() + "/dependency-patches/" + digest
	}
	stream := func(path string, worker *planFanoutWorker, taskID, runID uuid.UUID, fence int64) (int, http.Header, []byte, error) {
		body := callbackBody(worker, taskID, runID, fence)
		request, requestErr := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(string(body)))
		if requestErr != nil {
			return 0, nil, nil, requestErr
		}
		timestamp := time.Now().Unix()
		nonce := uuid.Must(uuid.NewV4()).String()
		signature, signErr := agentcallbackauth.SignRequest(worker.privateKey, worker.instance.ID.String(), http.MethodPost, request.URL.RequestURI(), timestamp, nonce, body)
		if signErr != nil {
			return 0, nil, nil, signErr
		}
		encoded, encodeErr := agentcallbackauth.EncodeSignature(signature)
		if encodeErr != nil {
			return 0, nil, nil, encodeErr
		}
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		request.Header.Set(agentcallbackauth.InstanceIDHeader, worker.instance.ID.String())
		request.Header.Set(agentcallbackauth.TimestampHeader, strconv.FormatInt(timestamp, 10))
		request.Header.Set(agentcallbackauth.NonceHeader, nonce)
		request.Header.Set(agentcallbackauth.SignatureHeader, encoded)
		response, requestErr := client.Do(request)
		if requestErr != nil {
			return 0, nil, nil, requestErr
		}
		defer response.Body.Close()
		responseBody, readErr := io.ReadAll(response.Body)
		return response.StatusCode, response.Header.Clone(), responseBody, readErr
	}

	// Object streaming is bounded even if the stored object no longer matches
	// the registered metadata size; the reader must stop at the server limit.
	oversizedPatch := make([]byte, models.MaxDeliveryPlanStepPatchArtifactBytes+1)
	putObject(patchKey, oversizedPatch)
	status, _, oversizedBody, streamErr := stream(streamPath(patchDigest), &workerB, workerB.task.ID, workerB.runID, childFence)
	require.NoError(t, streamErr)
	require.NotEqual(t, http.StatusOK, status, string(oversizedBody))
	require.Zero(t, manifestReceiptCount(), "an oversized source object must not create a handoff receipt")
	putObject(patchKey, []byte(cleanPatch))

	// A signed but tampered private object retains the registered key and length,
	// yet must never be streamed or acknowledged as handed to the consumer.
	tamperedPatch := strings.Replace(cleanPatch, "package after", "package alter", 1)
	require.Equal(t, len(cleanPatch), len(tamperedPatch), "the test changes bytes without changing the declared size")
	putObject(patchKey, []byte(tamperedPatch))
	status, _, tamperedBody, streamErr := stream(streamPath(patchDigest), &workerB, workerB.task.ID, workerB.runID, childFence)
	require.NoError(t, streamErr)
	require.NotEqual(t, http.StatusOK, status, string(tamperedBody))
	require.Zero(t, manifestReceiptCount(), "tampered source bytes must not create a handoff receipt")
	wrongSizePatch := cleanPatch + "\n"
	putObject(patchKey, []byte(wrongSizePatch))
	status, _, wrongSizeBody, streamErr := stream(streamPath(patchDigest), &workerB, workerB.task.ID, workerB.runID, childFence)
	require.NoError(t, streamErr)
	require.NotEqual(t, http.StatusOK, status, string(wrongSizeBody))
	require.Zero(t, manifestReceiptCount(), "source object size must match the server-registered metadata")
	putObject(patchKey, []byte(cleanPatch))

	wrongDigest := patchArtifactSHA256("not a patch from the completed parent")
	status, _, missingBody, streamErr := stream(streamPath(wrongDigest), &workerB, workerB.task.ID, workerB.runID, childFence)
	require.NoError(t, streamErr)
	require.NotEqual(t, http.StatusOK, status, string(missingBody))
	require.Zero(t, manifestReceiptCount(), "a digest not belonging to a direct dependency must not create a receipt")
	for name, requestTuple := range map[string]struct {
		taskID uuid.UUID
		runID  uuid.UUID
		fence  int64
	}{
		"cross task":  {taskID: uuid.Must(uuid.NewV4()), runID: workerB.runID, fence: childFence},
		"cross run":   {taskID: workerB.task.ID, runID: uuid.Must(uuid.NewV4()), fence: childFence},
		"stale fence": {taskID: workerB.task.ID, runID: workerB.runID, fence: childFence - 1},
	} {
		t.Run(name, func(t *testing.T) {
			response := callPlanFanoutEndpoint(client, server.URL, http.MethodPost, manifestPath,
				callbackBody(&workerB, requestTuple.taskID, requestTuple.runID, requestTuple.fence), &workerB)
			require.NoError(t, response.Err)
			require.NotEqual(t, http.StatusOK, response.Status, response.Body)
			require.Zero(t, manifestReceiptCount())
		})
	}

	status, headers, streamedPatch, streamErr := stream(streamPath(patchDigest), &workerB, workerB.task.ID, workerB.runID, childFence)
	require.NoError(t, streamErr)
	require.Equal(t, http.StatusOK, status, string(streamedPatch))
	require.Equal(t, cleanPatch, string(streamedPatch), "patch bytes must be returned only from the authenticated stream route")
	require.Equal(t, "application/vnd.git-patch", headers.Get(echo.HeaderContentType))
	require.Contains(t, strings.ToLower(headers.Get(echo.HeaderCacheControl)), "no-store")
	require.Equal(t, "nosniff", headers.Get(echo.HeaderXContentTypeOptions))
	require.NotContains(t, headers.Get("Location"), bucket)
	require.NotContains(t, headers.Get("Location"), patchKey)
	require.EqualValues(t, 1, manifestReceiptCount(), "a successful authenticated stream must persist a receipt for this consumer lease")

	postAppliedEvidence := func(eventID uuid.UUID, sequence int64, appliedManifest string) planFanoutCallbackResult {
		reviewDigest := sha256.Sum256([]byte("consumer review evidence"))
		details := map[string]any{
			"acceptance_checks":                  []map[string]any{{"criterion_sha256": planFanoutCriterionSHA256("dependency patch was applied"), "passed": true}},
			"review_diff_sha256":                 hex.EncodeToString(reviewDigest[:]),
			"applied_dependency_manifest_sha256": appliedManifest,
			"applied_dependency_patch_count":     1,
		}
		body, marshalErr := json.Marshal(map[string]any{
			"event_id": eventID.String(), "task_id": workerB.task.ID.String(), "run_id": workerB.runID.String(),
			"worker_id": workerB.heartbeat.WorkerID, "agent_key": workerB.profile.AgentKey, "machine_id": workerB.heartbeat.MachineID,
			"fencing_token": strconv.FormatInt(childFence, 10), "sequence": sequence,
			"action": models.DeliveryPlanStepActivityEvidence, "phase": models.DeliveryPlanStepActivityCompleted, "details": details,
		})
		if marshalErr != nil {
			return planFanoutCallbackResult{Status: http.StatusInternalServerError, Err: marshalErr}
		}
		return callPlanFanoutEndpoint(client, server.URL, http.MethodPost,
			"/api/internal/automation/steps/"+childStep.ID.String()+"/activity", body, &workerB)
	}
	wrongEvidence := postAppliedEvidence(uuid.Must(uuid.NewV4()), 1, strings.Repeat("0", 64))
	require.NoError(t, wrongEvidence.Err)
	require.NotEqual(t, http.StatusOK, wrongEvidence.Status, wrongEvidence.Body)
	var childActivityCount int64
	require.NoError(t, db.Model(&models.DeliveryPlanStepActivityEvent{}).Where("step_id = ? AND run_id = ?", childStep.ID, workerB.runID.String()).Count(&childActivityCount).Error)
	require.Zero(t, childActivityCount, "incorrect applied-manifest evidence must not be persisted")

	correctEvidence := postAppliedEvidence(uuid.Must(uuid.NewV4()), 2, envelope.Data.ManifestSHA256)
	require.NoError(t, correctEvidence.Err)
	require.Equalf(t, http.StatusOK, correctEvidence.Status, correctEvidence.Body)
	childTransition := callPlanFanoutTransition(client, server.URL, childStep.ID, &workerB, childFence, models.DeliveryPlanStepCompleted)
	require.NoError(t, childTransition.Err)
	require.Equalf(t, http.StatusOK, childTransition.Status, childTransition.Body)
	var completedChild models.DeliveryPlanStep
	require.NoError(t, db.First(&completedChild, "id = ?", childStep.ID).Error)
	require.Equal(t, models.DeliveryPlanStepCompleted, completedChild.Status)
	require.EqualValues(t, 1, manifestReceiptCount())

	objectMu.Lock()
	defer objectMu.Unlock()
	require.Contains(t, requestedKeys, patchKey, "private source keys must be derived and read only server-side")
	require.NotContains(t, manifestResponse.Body, strings.TrimPrefix(patchKey, "automation/"))
}
