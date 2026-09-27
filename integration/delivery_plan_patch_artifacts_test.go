//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"events-stocks/configuration"
	automation "events-stocks/controllers/automation"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// TestDeliveryPlanStepPatchArtifacts verifies the signed callback boundary,
// private object lookup and append-only artifact metadata against the real
// disposable PostgreSQL database used by the integration package. S3 itself
// is represented by a path-style HTTP endpoint that serves an in-memory object
// map, so the production AWS SDK request/key derivation is exercised without
// requiring a separately managed LocalStack service.
func TestDeliveryPlanStepPatchArtifacts(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "")

	bucket := "itbem-patch-artifacts-integration"
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

	previousClient := configuration.GetS3Client(nil)
	previousRegion := configuration.GetS3Region()
	previousEndpoint, previousPathStyle := configuration.GetS3Endpoint()
	t.Cleanup(func() {
		configuration.SetS3Client(previousClient)
		configuration.SetS3Region(previousRegion)
		configuration.SetS3Endpoint(previousEndpoint, previousPathStyle)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s3Config := &models.Config{
		AwsRegion: "us-east-1", S3Region: "us-east-1", S3ClientId: "integration",
		S3ClientSecret: "integration", S3Endpoint: s3Server.URL, S3UsePathStyle: "true",
		AutomationOutputBucket: bucket,
	}
	s3Client, region, err := configuration.BuildS3Client(ctx, s3Config)
	require.NoError(t, err)
	configuration.SetS3Client(s3Client)
	configuration.SetS3Region(region)
	configuration.SetS3Endpoint(s3Server.URL, true)

	fixture := createStepEventFixture(t, db)
	var plan models.DeliveryPlan
	var step models.DeliveryPlanStep
	require.NoError(t, db.First(&plan, "id = ?", fixture.PlanID).Error)
	require.NoError(t, db.First(&step, "id = ?", fixture.StepID).Error)
	now := time.Now().UTC()
	workspaceRef := "workspace://patchfixture"
	snapshot := models.DeliveryContextSnapshot{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: plan.WorkItemID, SourceID: uuid.Must(uuid.NewV4()),
		Kind: "repository", Name: "Patch artifact integration repository", Reference: workspaceRef,
		Revision: strings.Repeat("a", 40), CapturedAt: now, CreatedAt: now,
	}
	gate := models.DeliveryGate{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: plan.WorkItemID, Kind: "plan_approval", Decision: "approved",
		DecidedBy: "integration-reviewer", DecidedAt: now, CreatedAt: now,
	}
	worker := makePlanFanoutWorker(t, "patchqa-"+uuid.Must(uuid.NewV4()).String()[:8], "patch-artifact", plan.WorkItemID, "patch-artifact-integration", now)
	worker.task.Status = "running"
	leaseExpiry := now.Add(10 * time.Minute)
	worker.task.LeaseExpiresAt = &leaseExpiry
	worker.task.AttemptCount = 1
	worker.profile.OperationsJSON = `["delivery.implementation"]`
	worker.instance.Status = "active"
	worker.instance.CreatedAt, worker.instance.UpdatedAt = now, now
	worker.task.CreatedAt, worker.task.UpdatedAt = now, now
	worker.profile.CreatedAt, worker.profile.UpdatedAt = now, now
	worker.heartbeat.CreatedAt, worker.heartbeat.UpdatedAt = now, now
	worker.instance.MachineID = worker.heartbeat.MachineID
	worker.task.WorkerID, worker.task.AgentKey, worker.task.MachineID = worker.heartbeat.WorkerID, worker.profile.AgentKey, worker.heartbeat.MachineID
	worker.task.RunID = worker.runID.String()
	stepTaskID := worker.task.ID
	step.Status = models.DeliveryPlanStepRunning
	step.AutomationTaskID = &stepTaskID
	step.RunID, step.WorkerID = worker.runID.String(), worker.heartbeat.WorkerID
	step.AgentKey, step.MachineID = worker.profile.AgentKey, worker.heartbeat.MachineID
	step.LeaseFence, step.LeaseExpiresAt = 9, &leaseExpiry
	plan.Status = "approved"
	plan.ApprovedGateID = &gate.ID
	for _, value := range []any{&snapshot, &gate, &worker.profile, &worker.heartbeat, &worker.instance, &worker.task} {
		require.NoError(t, db.Create(value).Error)
	}
	require.NoError(t, db.Model(&models.DeliveryPlan{}).Where("id = ?", plan.ID).Updates(map[string]any{
		"status": plan.Status, "approved_gate_id": gate.ID,
	}).Error)
	require.NoError(t, db.Model(&models.DeliveryPlanStep{}).Where("id = ?", step.ID).Updates(map[string]any{
		"status": step.Status, "automation_task_id": stepTaskID, "run_id": step.RunID,
		"worker_id": step.WorkerID, "agent_key": step.AgentKey, "machine_id": step.MachineID,
		"lease_fence": step.LeaseFence, "lease_expires_at": leaseExpiry,
	}).Error)
	t.Cleanup(func() {
		_ = db.Unscoped().Where("instance_id = ?", worker.instance.ID).Delete(&models.AutomationAgentCallbackNonce{}).Error
		_ = db.Unscoped().Where("id = ?", worker.instance.ID).Delete(&models.AutomationAgentInstance{}).Error
		_ = db.Unscoped().Where("worker_id = ?", worker.heartbeat.WorkerID).Delete(&models.AutomationAgentHeartbeat{}).Error
		_ = db.Unscoped().Where("id = ?", worker.task.ID).Delete(&models.AutomationTask{}).Error
		_ = db.Unscoped().Where("agent_key = ?", worker.profile.AgentKey).Delete(&models.AutomationAgentProfile{}).Error
	})

	router := echo.New()
	router.POST("/api/internal/automation/steps/:id/activity", func(c echo.Context) error {
		c.Set("config", s3Config)
		return automation.RecordDeliveryPlanStepActivity(c)
	}, automation.AgentCallbackAuthentication)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client := server.Client()

	const validPatch = "diff --git a/src/fixture.go b/src/fixture.go\nindex 1111111..2222222 100644\n--- a/src/fixture.go\n+++ b/src/fixture.go\n@@ -1 +1 @@\n-package old\n+package new\n"
	validDigest := patchArtifactSHA256(validPatch)
	validKey := patchArtifactObjectKey(worker.task.ID, worker.runID, step.ID, validDigest)
	putObject := func(key string, value []byte) {
		objectMu.Lock()
		defer objectMu.Unlock()
		objects[key] = append([]byte(nil), value...)
	}
	putObject(validKey, []byte(validPatch))
	postWithManifest := func(eventID uuid.UUID, taskID, runID uuid.UUID, fence int64, sequence int64, ref models.DeliveryPlanStepPatchArtifactReference, manifest string, extra map[string]any) planFanoutCallbackResult {
		payload := map[string]any{
			"event_id": eventID.String(), "task_id": taskID.String(), "run_id": runID.String(),
			"worker_id": worker.heartbeat.WorkerID, "agent_key": worker.profile.AgentKey,
			"machine_id": worker.heartbeat.MachineID, "fencing_token": fmt.Sprintf("%d", fence),
			"sequence": sequence, "action": models.DeliveryPlanStepActivityEvidence,
			"phase": models.DeliveryPlanStepActivityCompleted,
			"details": map[string]any{
				"patch_artifacts":    []models.DeliveryPlanStepPatchArtifactReference{ref},
				"review_diff_sha256": manifest,
			},
		}
		if extra != nil {
			for key, value := range extra {
				payload[key] = value
			}
		}
		body, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return planFanoutCallbackResult{Status: http.StatusInternalServerError, Err: marshalErr}
		}
		return callPlanFanoutEndpoint(client, server.URL, http.MethodPost,
			"/api/internal/automation/steps/"+step.ID.String()+"/activity", body, &worker)
	}
	post := func(eventID uuid.UUID, taskID, runID uuid.UUID, fence int64, sequence int64, ref models.DeliveryPlanStepPatchArtifactReference, extra map[string]any) planFanoutCallbackResult {
		manifest, manifestErr := models.DeliveryPlanStepPatchArtifactManifestSHA256([]models.DeliveryPlanStepPatchArtifactReference{ref})
		if manifestErr != nil {
			return planFanoutCallbackResult{Status: http.StatusInternalServerError, Err: manifestErr}
		}
		return postWithManifest(eventID, taskID, runID, fence, sequence, ref, manifest, extra)
	}
	validRef := models.DeliveryPlanStepPatchArtifactReference{
		RepositoryRef: workspaceRef, BaseSHA: strings.Repeat("a", 40), SHA256: validDigest, SizeBytes: int64(len(validPatch)),
	}

	validEventID := uuid.Must(uuid.NewV4())
	first := post(validEventID, worker.task.ID, worker.runID, 9, 1, validRef, nil)
	require.NoError(t, first.Err)
	require.Equalf(t, http.StatusOK, first.Status, first.Body)
	require.Contains(t, first.Body, `"idempotent":false`)
	var artifacts []models.DeliveryPlanStepPatchArtifact
	require.NoError(t, db.Where("step_id = ? AND run_id = ?", step.ID, worker.runID.String()).Find(&artifacts).Error)
	require.Len(t, artifacts, 1)
	stored := artifacts[0]
	require.Equal(t, plan.ID, stored.PlanID)
	require.Equal(t, worker.task.ID, stored.AutomationTaskID)
	require.Equal(t, worker.runID.String(), stored.RunID)
	require.Equal(t, step.ID, stored.StepID)
	require.Equal(t, worker.heartbeat.WorkerID, stored.WorkerID)
	require.Equal(t, worker.profile.AgentKey, stored.AgentKey)
	require.Equal(t, worker.heartbeat.MachineID, stored.MachineID)
	require.NotNil(t, stored.AgentInstanceID)
	require.Equal(t, worker.instance.ID, *stored.AgentInstanceID)
	require.EqualValues(t, 9, stored.FencingToken)
	require.Equal(t, workspaceRef, stored.RepositoryRef)
	require.Equal(t, strings.Repeat("a", 40), stored.BaseSHA)
	require.Equal(t, bucket, stored.Bucket)
	require.Equal(t, validKey, stored.ObjectKey)
	require.Equal(t, validDigest, stored.SHA256)
	require.EqualValues(t, len(validPatch), stored.SizeBytes)
	require.NotContains(t, first.Body, bucket, "callback response must not reveal private storage metadata")
	require.NotContains(t, first.Body, validKey, "callback response must not reveal a private object key")
	var storedActivity models.DeliveryPlanStepActivityEvent
	require.NoError(t, db.Where("step_id = ? AND run_id = ? AND sequence = ?", step.ID, worker.runID.String(), 1).First(&storedActivity).Error)
	require.Contains(t, storedActivity.DetailsJSON, validDigest)
	require.NotContains(t, storedActivity.DetailsJSON, bucket)
	require.NotContains(t, storedActivity.DetailsJSON, validKey)
	require.NotContains(t, storedActivity.DetailsJSON, "package new", "patch bytes must remain private in object storage")

	mutationErr := db.Model(&models.DeliveryPlanStepPatchArtifact{}).
		Where("id = ?", stored.ID).Update("size_bytes", stored.SizeBytes+1).Error
	require.Error(t, mutationErr, "verified patch metadata must be append-only")
	require.Contains(t, strings.ToLower(mutationErr.Error()), "append-only")
	deleteErr := db.Unscoped().Delete(&stored).Error
	require.Error(t, deleteErr, "verified patch metadata must not be deletable")
	require.Contains(t, strings.ToLower(deleteErr.Error()), "append-only")

	// A fresh HTTP signature/nonce around the identical event is an idempotent
	// retry. It must not duplicate the append-only artifact metadata.
	replay := post(validEventID, worker.task.ID, worker.runID, 9, 1, validRef, nil)
	require.NoError(t, replay.Err)
	require.Equalf(t, http.StatusOK, replay.Status, replay.Body)
	require.Contains(t, replay.Body, `"idempotent":true`)
	require.NoError(t, db.Where("step_id = ? AND run_id = ?", step.ID, worker.runID.String()).Find(&artifacts).Error)
	require.Len(t, artifacts, 1)

	assertRejectedWithoutRows := func(name string, response planFanoutCallbackResult, expectedStatus int) {
		t.Helper()
		require.NoError(t, response.Err, name)
		require.Equalf(t, expectedStatus, response.Status, "%s: %s", name, response.Body)
		var count int64
		require.NoError(t, db.Model(&models.DeliveryPlanStepPatchArtifact{}).Where("step_id = ? AND run_id = ?", step.ID, worker.runID.String()).Count(&count).Error)
		require.EqualValues(t, 1, count, "%s must not persist a patch artifact", name)
	}

	t.Run("manifest digest mismatch", func(t *testing.T) {
		response := postWithManifest(uuid.Must(uuid.NewV4()), worker.task.ID, worker.runID, 9, 2, validRef, strings.Repeat("0", 64), nil)
		assertRejectedWithoutRows("manifest digest mismatch", response, http.StatusBadRequest)
	})

	t.Run("object bytes do not match declared digest", func(t *testing.T) {
		badBody := []byte(strings.Replace(validPatch, "package new", "package tampered", 1))
		badDigest := patchArtifactSHA256(validPatch)
		badKey := patchArtifactObjectKey(worker.task.ID, worker.runID, step.ID, badDigest)
		putObject(badKey, badBody)
		ref := models.DeliveryPlanStepPatchArtifactReference{RepositoryRef: workspaceRef, BaseSHA: strings.Repeat("a", 40), SHA256: badDigest, SizeBytes: int64(len(badBody))}
		response := post(uuid.Must(uuid.NewV4()), worker.task.ID, worker.runID, 9, 3, ref, nil)
		assertRejectedWithoutRows("tampered object digest", response, http.StatusBadRequest)
	})
	t.Run("cross repository reference", func(t *testing.T) {
		ref := validRef
		ref.RepositoryRef = "workspace://otherrepo"
		response := post(uuid.Must(uuid.NewV4()), worker.task.ID, worker.runID, 9, 4, ref, nil)
		assertRejectedWithoutRows("cross repository", response, http.StatusBadRequest)
	})
	t.Run("cross task", func(t *testing.T) {
		response := post(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), worker.runID, 9, 5, validRef, nil)
		require.NoError(t, response.Err)
		require.NotEqual(t, http.StatusOK, response.Status, response.Body)
		var count int64
		require.NoError(t, db.Model(&models.DeliveryPlanStepPatchArtifact{}).Where("step_id = ? AND run_id = ?", step.ID, worker.runID.String()).Count(&count).Error)
		require.EqualValues(t, 1, count)
	})
	t.Run("cross run", func(t *testing.T) {
		response := post(uuid.Must(uuid.NewV4()), worker.task.ID, uuid.Must(uuid.NewV4()), 9, 6, validRef, nil)
		assertRejectedWithoutRows("cross run", response, http.StatusConflict)
	})
	t.Run("stale fence", func(t *testing.T) {
		response := post(uuid.Must(uuid.NewV4()), worker.task.ID, worker.runID, 8, 7, validRef, nil)
		assertRejectedWithoutRows("stale fence", response, http.StatusConflict)
	})
	t.Run("caller cannot supply object key", func(t *testing.T) {
		response := post(uuid.Must(uuid.NewV4()), worker.task.ID, worker.runID, 9, 8, validRef, map[string]any{"object_key": "other-task/private.patch"})
		assertRejectedWithoutRows("caller-supplied storage key", response, http.StatusBadRequest)
	})
	t.Run("sensitive patch path", func(t *testing.T) {
		unsafePatch := "diff --git a/.env.local b/.env.local\nindex 1111111..2222222 100644\n--- a/.env.local\n+++ b/.env.local\n@@ -1 +1 @@\n+LOCAL_TEST_VALUE=not-a-real-secret\n"
		digest := patchArtifactSHA256(unsafePatch)
		key := patchArtifactObjectKey(worker.task.ID, worker.runID, step.ID, digest)
		putObject(key, []byte(unsafePatch))
		ref := models.DeliveryPlanStepPatchArtifactReference{RepositoryRef: workspaceRef, BaseSHA: strings.Repeat("a", 40), SHA256: digest, SizeBytes: int64(len(unsafePatch))}
		response := post(uuid.Must(uuid.NewV4()), worker.task.ID, worker.runID, 9, 9, ref, nil)
		assertRejectedWithoutRows("sensitive patch path", response, http.StatusBadRequest)
	})
	t.Run("patch exceeds maximum size", func(t *testing.T) {
		ref := validRef
		ref.SizeBytes = models.MaxDeliveryPlanStepPatchArtifactBytes + 1
		// The manifest helper intentionally refuses an already-invalid reference.
		// Sign the immediately-valid boundary manifest but submit the oversized
		// descriptor, so the request fails the size validation before manifest
		// comparison rather than merely failing helper-side construction.
		boundaryRef := ref
		boundaryRef.SizeBytes = models.MaxDeliveryPlanStepPatchArtifactBytes
		manifest, manifestErr := models.DeliveryPlanStepPatchArtifactManifestSHA256([]models.DeliveryPlanStepPatchArtifactReference{boundaryRef})
		require.NoError(t, manifestErr)
		response := postWithManifest(uuid.Must(uuid.NewV4()), worker.task.ID, worker.runID, 9, 10, ref, manifest, nil)
		assertRejectedWithoutRows("oversized patch descriptor", response, http.StatusBadRequest)
	})

	objectMu.Lock()
	defer objectMu.Unlock()
	require.Contains(t, requestedKeys, validKey, "the API must derive the private key from task/run/step/digest")
	require.NotContains(t, requestedKeys, "other-task/private.patch", "the API must never read a caller-supplied key")
}

func patchArtifactSHA256(patch string) string {
	digest := sha256.Sum256([]byte(patch))
	return hex.EncodeToString(digest[:])
}

func patchArtifactObjectKey(taskID, runID, stepID uuid.UUID, digest string) string {
	return fmt.Sprintf("automation/%s/runs/%s/steps/%s/patches/%s.patch", taskID, runID, stepID, digest)
}
