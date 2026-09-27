//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"events-stocks/configuration"
	automation "events-stocks/controllers/automation"
	delivery "events-stocks/controllers/delivery"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/services/deliveryplansteps"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestDeliveryPlanStepEvidenceSignedUploadRoundTrip exercises the active Go
// callback, assignment/lease checks, PostgreSQL append-only evidence ledger,
// SSE-S3 object write, project-authorized metadata API, and authorized proxy
// download against disposable PostgreSQL and an in-memory S3-compatible server.
func TestDeliveryPlanStepEvidenceSignedUploadRoundTrip(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")

	// Keep every relational fixture and append-only artifact row inside one
	// outer transaction. Handler transactions use PostgreSQL savepoints on this
	// connection, so rollback leaves no synthetic rows in the shared test DB.
	fixtureTx := db.Begin()
	require.NoError(t, fixtureTx.Error)
	configuration.DB = fixtureTx
	t.Cleanup(func() {
		configuration.DB = db
		_ = fixtureTx.Rollback().Error
	})

	const bucket = "itbem-step-evidence-integration"
	type storedObject struct {
		body        []byte
		contentType string
		encryption  string
	}
	var objectMu sync.Mutex
	objects := map[string]storedObject{}
	var putCount int
	s3Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/" + bucket + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, prefix)
		objectMu.Lock()
		defer objectMu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			putCount++
			objects[key] = storedObject{body: body, contentType: r.Header.Get(echo.HeaderContentType), encryption: r.Header.Get("X-Amz-Server-Side-Encryption")}
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			object, ok := objects[key]
			if !ok {
				http.Error(w, "missing object", http.StatusNotFound)
				return
			}
			w.Header().Set(echo.HeaderContentType, object.contentType)
			w.Header().Set(echo.HeaderContentLength, strconv.Itoa(len(object.body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(object.body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
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
	config := &models.Config{
		AwsRegion: "us-east-1", S3Region: "us-east-1", S3ClientId: "integration-access-key",
		S3ClientSecret: "integration-synthetic-secret", S3Endpoint: s3Server.URL, S3UsePathStyle: "true",
		AutomationOutputBucket: bucket,
	}
	s3Context, cancelS3 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelS3()
	s3Client, region, err := configuration.BuildS3Client(s3Context, config)
	require.NoError(t, err)
	configuration.SetS3Client(s3Client)
	configuration.SetS3Region(region)
	configuration.SetS3Endpoint(s3Server.URL, true)

	now := time.Now().UTC()
	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	subject := "integration-evidence-viewer-" + suffix
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Evidence customer " + suffix, Code: "EV_" + suffix, Level: 10, IsActive: true}
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Evidence customer " + suffix, Code: "evidence-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	foreignClientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Foreign evidence customer " + suffix, Code: "FEV_" + suffix, Level: 10, IsActive: true}
	foreignClient := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Foreign evidence customer " + suffix, Code: "foreign-evidence-" + suffix, ClientTypeID: foreignClientType.ID, IsActive: true}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Evidence project " + suffix, Slug: "evidence-" + suffix, Status: "active", CreatedBy: subject}
	foreignProject := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: foreignClient.ID, Name: "Foreign project " + suffix, Slug: "foreign-evidence-" + suffix, Status: "active", CreatedBy: "integration"}
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject, Title: "Capture step evidence", ExpectedOutcome: "Store and authorize a report", State: "implementation", AcceptanceJSON: `["The artifact is stored privately"]`}
	foreignItem := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: foreignProject.ID, RequestedBy: "integration", Title: "Foreign evidence task", State: "planning"}
	gate := models.DeliveryGate{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Kind: "plan_approval", Decision: "approved", DecidedBy: "synthetic-reviewer", EvidenceChecklist: `[]`, DecidedAt: now, CreatedAt: now}
	plan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Version: 1, Status: "approved", Summary: "Evidence roundtrip", StructuredJSON: `{}`, ContextDigest: "integration-evidence-v1", ProposedBy: "integration", ApprovedGateID: &gate.ID, CreatedAt: now}
	requirementsJSON, err := json.Marshal([]deliveryplansteps.EvidenceRequirement{{
		Key: "qa-report", Title: "QA report", Description: "Synthetic plain-text report", Required: true,
		ContentTypes: []string{"text/plain"}, MaxBytes: 2048,
	}})
	require.NoError(t, err)
	step := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "capture-report", IdempotencyKey: "capture-report-" + suffix,
		Role: models.DeliveryPlanStepRoleImplementation, DisplayOrder: 1, Title: "Capture QA report", Objective: "Upload one small plain-text artifact",
		AcceptanceCriteriaJSON: `["The artifact is stored privately"]`, EvidenceRequirementsJSON: string(requirementsJSON),
		Status: models.DeliveryPlanStepRunning, CreatedBy: subject, CreatedAt: now, UpdatedAt: now,
	}
	integrationStep := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "final-integration", IdempotencyKey: "final-integration-" + suffix,
		Role: models.DeliveryPlanStepRoleIntegration, DisplayOrder: 2, Title: "Verify final report handoff",
		Objective: "Verify the approved implementation acceptance criterion", AcceptanceCriteriaJSON: `["The artifact is stored privately"]`,
		EvidenceRequirementsJSON: `[]`, Status: models.DeliveryPlanStepPlanned, CreatedBy: subject, CreatedAt: now, UpdatedAt: now,
	}
	finalDependency := models.DeliveryPlanStepDependency{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: integrationStep.ID, DependsOnStepID: step.ID, CreatedAt: now,
	}
	parentTask := models.AutomationTask{
		ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), RequestedBy: subject,
		DeliveryWorkItemID: &item.ID, CorrelationID: item.ID.String(), Operation: "delivery.implementation",
		InputRef: "private://integration/evidence-parent", Status: "queued", CreatedAt: now, UpdatedAt: now,
	}
	worker := makePlanFanoutWorker(t, "evidenceworker-"+suffix[:4], "step-evidence", item.ID, subject, now)
	worker.task.Status = "running"
	worker.task.AttemptCount = 1
	worker.task.LeaseExpiresAt = evidenceTimePtr(now.Add(20 * time.Minute))
	worker.task.AgentInstanceID = &worker.instance.ID
	worker.heartbeat.AgentInstanceID = &worker.instance.ID
	worker.profile.CreatedAt, worker.profile.UpdatedAt = now, now
	worker.heartbeat.CreatedAt, worker.heartbeat.UpdatedAt = now, now
	worker.instance.CreatedAt, worker.instance.UpdatedAt = now, now
	worker.task.CreatedAt, worker.task.UpdatedAt = now, now
	stepTaskID := worker.task.ID
	stepLeaseExpiresAt := now.Add(15 * time.Minute)
	step.AutomationTaskID, step.RunID = &stepTaskID, worker.runID.String()
	step.WorkerID, step.AgentKey, step.MachineID = worker.heartbeat.WorkerID, worker.profile.AgentKey, worker.heartbeat.MachineID
	step.LeaseFence, step.LeaseExpiresAt = 17, &stepLeaseExpiresAt
	step.StartedAt = evidenceTimePtr(now)
	planHash, err := deliveryplansteps.ApprovedPlanContentHash(plan, []models.DeliveryPlanStep{step, integrationStep}, []models.DeliveryPlanStepDependency{finalDependency})
	require.NoError(t, err)
	execution := models.DeliveryPlanExecution{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: parentTask.ID, IdempotencyKey: "step-evidence-" + suffix,
		PlanID: plan.ID, PlanVersion: plan.Version, ApprovedGateID: gate.ID, PlanHash: planHash,
		MaxConcurrency: 1, Status: models.DeliveryPlanExecutionRunning, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	assignment := models.DeliveryPlanStepAssignment{
		ID: uuid.Must(uuid.NewV4()), ExecutionID: execution.ID, DeliveryPlanStepID: step.ID,
		ChildAutomationTaskID: worker.task.ID, TargetMachineID: worker.heartbeat.MachineID, TargetAgentKey: worker.profile.AgentKey,
		Status: models.DeliveryPlanStepAssignmentRunning, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	foreignPlan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: foreignItem.ID, Version: 1, Status: "proposed", Summary: "Foreign plan", StructuredJSON: `{}`, CreatedAt: now}
	foreignStep := models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4()), PlanID: foreignPlan.ID, StepKey: "foreign", IdempotencyKey: "foreign-" + suffix, Role: models.DeliveryPlanStepRoleImplementation, DisplayOrder: 1, Title: "Foreign step", AcceptanceCriteriaJSON: `[]`, EvidenceRequirementsJSON: `[]`, Status: models.DeliveryPlanStepPlanned, CreatedAt: now, UpdatedAt: now}
	member := models.DeliveryProjectMember{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, CognitoSub: subject, Role: "viewer", Permissions: `[]`, CreatedBy: "integration", CreatedAt: now, UpdatedAt: now}

	for _, row := range []any{
		&clientType, &client, &foreignClientType, &foreignClient, &project, &foreignProject, &item, &foreignItem,
		&gate, &plan, &step, &integrationStep, &finalDependency, &foreignPlan, &foreignStep, &member,
		&worker.profile, &worker.heartbeat, &worker.instance, &parentTask, &worker.task, &execution, &assignment,
	} {
		require.NoError(t, configuration.DB.Create(row).Error)
	}

	restoreAuth := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		if cognitoSub != subject {
			return nil, requireAuthUserError{}
		}
		return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: subject, IsActive: true, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restoreAuth)

	e := echo.New()
	callbacks := e.Group("/api/internal/automation")
	callbacks.Use(automation.AgentCallbackAuthentication)
	callbacks.POST("/steps/:id/evidence", automation.UploadDeliveryPlanStepEvidence, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("config", config)
			c.SetRequest(c.Request().WithContext(configuration.WithConfig(c.Request().Context(), config)))
			return next(c)
		}
	})
	actorContext := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("cognito_sub", subject)
			c.Set("tenant_code", "itbem")
			c.Set("workspace_mode", "organization")
			c.Set("organization_id", client.ID)
			return next(c)
		}
	}
	e.GET("/api/automation/plans/:id/steps/:stepId/evidence", delivery.ListPlanStepEvidence, actorContext)
	e.GET("/api/automation/plans/:id/steps/:stepId/evidence/:evidenceId/content", delivery.DownloadPlanStepEvidence, actorContext)
	server := httptest.NewServer(e)
	t.Cleanup(server.Close)
	clientHTTP := server.Client()

	contents := []byte("Synthetic QA report: integration path stored privately.\n")
	digest := sha256.Sum256(contents)
	sha256Hex := hex.EncodeToString(digest[:])
	eventID := uuid.Must(uuid.NewV4())
	query := url.Values{
		"task_id":         {worker.task.ID.String()},
		"run_id":          {worker.runID.String()},
		"worker_id":       {worker.heartbeat.WorkerID},
		"agent_key":       {worker.profile.AgentKey},
		"machine_id":      {worker.heartbeat.MachineID},
		"fencing_token":   {strconv.FormatInt(step.LeaseFence, 10)},
		"event_id":        {eventID.String()},
		"requirement_key": {"qa-report"},
		"file_name":       {"qa-report.txt"},
		"content_type":    {"text/plain"},
	}
	uploadPath := "/api/internal/automation/steps/" + step.ID.String() + "/evidence?" + query.Encode()
	postSigned := func() (int, http.Header, []byte, error) {
		req, requestErr := http.NewRequest(http.MethodPost, server.URL+uploadPath, bytes.NewReader(contents))
		if requestErr != nil {
			return 0, nil, nil, requestErr
		}
		req.ContentLength = int64(len(contents))
		req.Header.Set(echo.HeaderContentType, "text/plain")
		timestamp := time.Now().Unix()
		nonce := uuid.Must(uuid.NewV4()).String()
		signature, signErr := agentcallbackauth.SignRequest(worker.privateKey, worker.instance.ID.String(), req.Method, req.URL.RequestURI(), timestamp, nonce, contents)
		if signErr != nil {
			return 0, nil, nil, signErr
		}
		encodedSignature, signErr := agentcallbackauth.EncodeSignature(signature)
		if signErr != nil {
			return 0, nil, nil, signErr
		}
		req.Header.Set(agentcallbackauth.InstanceIDHeader, worker.instance.ID.String())
		req.Header.Set(agentcallbackauth.TimestampHeader, strconv.FormatInt(timestamp, 10))
		req.Header.Set(agentcallbackauth.NonceHeader, nonce)
		req.Header.Set(agentcallbackauth.SignatureHeader, encodedSignature)
		req.Header.Set("X-Content-SHA256", sha256Hex)
		resp, doErr := clientHTTP.Do(req)
		if doErr != nil {
			return 0, nil, nil, doErr
		}
		defer resp.Body.Close()
		body, readErr := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Clone(), body, readErr
	}

	status, _, uploadBody, err := postSigned()
	require.NoError(t, err)
	require.Equalf(t, http.StatusCreated, status, string(uploadBody))
	var uploadEnvelope struct {
		Data struct {
			ID         uuid.UUID `json:"id"`
			StepID     uuid.UUID `json:"step_id"`
			SHA256     string    `json:"sha256"`
			SizeBytes  int64     `json:"size_bytes"`
			Idempotent bool      `json:"idempotent"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(uploadBody, &uploadEnvelope))
	require.NotEqual(t, uuid.Nil, uploadEnvelope.Data.ID)
	require.Equal(t, step.ID, uploadEnvelope.Data.StepID)
	require.Equal(t, sha256Hex, uploadEnvelope.Data.SHA256)
	require.EqualValues(t, len(contents), uploadEnvelope.Data.SizeBytes)
	require.False(t, uploadEnvelope.Data.Idempotent)
	require.NotContains(t, string(uploadBody), bucket)

	var record models.DeliveryPlanStepEvidence
	require.NoError(t, configuration.DB.Where("event_id = ?", eventID).First(&record).Error)
	require.Equal(t, plan.ID, record.PlanID)
	require.Equal(t, plan.Version, record.PlanVersion)
	require.Equal(t, step.ID, record.StepID)
	require.Equal(t, worker.task.ID, record.AutomationTaskID)
	require.Equal(t, worker.runID.String(), record.RunID)
	require.Equal(t, worker.instance.ID, record.AgentInstanceID)
	require.EqualValues(t, step.LeaseFence, record.FencingToken)
	require.Equal(t, bucket, record.Bucket)
	require.Equal(t, sha256Hex, record.SHA256)
	objectMu.Lock()
	stored, found := objects[record.ObjectKey]
	require.True(t, found, "S3 object should exist at the server-derived private key")
	require.Equal(t, contents, stored.body)
	require.Equal(t, "text/plain", stored.contentType)
	require.Equal(t, "AES256", stored.encryption, "writes must explicitly request SSE-S3 encryption")
	require.Equal(t, 1, putCount)
	objectMu.Unlock()

	status, _, retryBody, err := postSigned()
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, status, string(retryBody))
	var retryEnvelope struct {
		Data struct {
			ID         uuid.UUID `json:"id"`
			Idempotent bool      `json:"idempotent"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(retryBody, &retryEnvelope))
	require.Equal(t, uploadEnvelope.Data.ID, retryEnvelope.Data.ID)
	require.True(t, retryEnvelope.Data.Idempotent)
	var evidenceCount int64
	require.NoError(t, configuration.DB.Model(&models.DeliveryPlanStepEvidence{}).Where("event_id = ?", eventID).Count(&evidenceCount).Error)
	require.EqualValues(t, 1, evidenceCount, "same event identity must create one append-only metadata row")
	objectMu.Lock()
	require.Equal(t, 1, putCount, "idempotent retry should not write another S3 object")
	objectMu.Unlock()

	listPath := "/api/automation/plans/" + plan.ID.String() + "/steps/" + step.ID.String() + "/evidence?limit=10"
	listResponse, err := clientHTTP.Get(server.URL + listPath)
	require.NoError(t, err)
	listBody, err := io.ReadAll(listResponse.Body)
	require.NoError(t, err)
	require.NoError(t, listResponse.Body.Close())
	require.Equalf(t, http.StatusOK, listResponse.StatusCode, string(listBody))
	var listEnvelope struct {
		Data struct {
			PlanID      uuid.UUID `json:"plan_id"`
			PlanVersion int       `json:"plan_version"`
			StepID      uuid.UUID `json:"step_id"`
			Items       []struct {
				ID             uuid.UUID `json:"id"`
				RequirementKey string    `json:"requirement_key"`
				FileName       string    `json:"file_name"`
				ContentType    string    `json:"content_type"`
				SizeBytes      int64     `json:"size_bytes"`
				SHA256         string    `json:"sha256"`
				TaskID         uuid.UUID `json:"automation_task_id"`
				RunID          string    `json:"run_id"`
				AgentKey       string    `json:"agent_key"`
				InstanceID     uuid.UUID `json:"agent_instance_id"`
				Fence          int64     `json:"fencing_token"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(listBody, &listEnvelope))
	require.Equal(t, plan.ID, listEnvelope.Data.PlanID)
	require.Equal(t, plan.Version, listEnvelope.Data.PlanVersion)
	require.Equal(t, step.ID, listEnvelope.Data.StepID)
	require.Len(t, listEnvelope.Data.Items, 1)
	listed := listEnvelope.Data.Items[0]
	require.Equal(t, uploadEnvelope.Data.ID, listed.ID)
	require.Equal(t, "qa-report", listed.RequirementKey)
	require.Equal(t, "qa-report.txt", listed.FileName)
	require.Equal(t, "text/plain", listed.ContentType)
	require.EqualValues(t, len(contents), listed.SizeBytes)
	require.Equal(t, sha256Hex, listed.SHA256)
	require.Equal(t, worker.task.ID, listed.TaskID)
	require.Equal(t, worker.runID.String(), listed.RunID)
	require.Equal(t, worker.profile.AgentKey, listed.AgentKey)
	require.Equal(t, worker.instance.ID, listed.InstanceID)
	require.Equal(t, step.LeaseFence, listed.Fence)
	for _, privateValue := range []string{bucket, record.ObjectKey, worker.heartbeat.MachineID, worker.heartbeat.WorkerID, "object_key", "machine_id", "worker_id"} {
		require.NotContains(t, string(listBody), privateValue, "metadata endpoint must not expose private storage or process identity")
	}

	foreignPath := "/api/automation/plans/" + foreignPlan.ID.String() + "/steps/" + foreignStep.ID.String() + "/evidence"
	foreignResponse, err := clientHTTP.Get(server.URL + foreignPath)
	require.NoError(t, err)
	foreignBody, err := io.ReadAll(foreignResponse.Body)
	require.NoError(t, err)
	require.NoError(t, foreignResponse.Body.Close())
	require.Equalf(t, http.StatusNotFound, foreignResponse.StatusCode, string(foreignBody), "a member of one client must not read another client's plan evidence")
	require.NotContains(t, string(foreignBody), foreignPlan.ID.String())
	foreignEvidenceID := uuid.Must(uuid.NewV4())
	foreignDownloadPath := "/api/automation/plans/" + foreignPlan.ID.String() + "/steps/" + foreignStep.ID.String() + "/evidence/" + foreignEvidenceID.String() + "/content"
	foreignDownloadResponse, err := clientHTTP.Get(server.URL + foreignDownloadPath)
	require.NoError(t, err)
	foreignDownloadBody, err := io.ReadAll(foreignDownloadResponse.Body)
	require.NoError(t, err)
	require.NoError(t, foreignDownloadResponse.Body.Close())
	require.Equalf(t, http.StatusNotFound, foreignDownloadResponse.StatusCode, string(foreignDownloadBody), "foreign-project authorization must reject before evidence content is returned")
	require.NotContains(t, string(foreignDownloadBody), foreignEvidenceID.String())

	downloadPath := listPath[:strings.Index(listPath, "?")] + "/" + uploadEnvelope.Data.ID.String() + "/content"
	downloadResponse, err := clientHTTP.Get(server.URL + downloadPath)
	require.NoError(t, err)
	downloaded, err := io.ReadAll(downloadResponse.Body)
	require.NoError(t, err)
	require.NoError(t, downloadResponse.Body.Close())
	require.Equalf(t, http.StatusOK, downloadResponse.StatusCode, string(downloaded))
	require.Equal(t, contents, downloaded)
	require.Equal(t, "text/plain", downloadResponse.Header.Get(echo.HeaderContentType))
	require.Equal(t, "nosniff", downloadResponse.Header.Get("X-Content-Type-Options"))
	require.Contains(t, strings.ToLower(downloadResponse.Header.Get(echo.HeaderContentDisposition)), "attachment")
	_, dispositionParams, err := mime.ParseMediaType(downloadResponse.Header.Get(echo.HeaderContentDisposition))
	require.NoError(t, err)
	require.Equal(t, "qa-report.txt", dispositionParams["filename"])
	require.Contains(t, downloadResponse.Header.Get(echo.HeaderCacheControl), "no-store")
	require.Empty(t, downloadResponse.Header.Get("Location"), "download is proxied, not redirected to object storage")

	// Change one private S3 byte without changing the stored object's length.
	// The proxy must detect the mismatch against the persisted SHA-256 and fail
	// closed instead of serving the tampered artifact.
	objectMu.Lock()
	originalObject := objects[record.ObjectKey]
	objectMu.Unlock()
	require.NotEmpty(t, originalObject.body)
	tamperedObject := storedObject{
		body: append([]byte(nil), originalObject.body...), contentType: originalObject.contentType,
		encryption: originalObject.encryption,
	}
	tamperedObject.body[0] ^= 1
	objectMu.Lock()
	objects[record.ObjectKey] = tamperedObject
	objectMu.Unlock()
	t.Cleanup(func() {
		objectMu.Lock()
		objects[record.ObjectKey] = originalObject
		objectMu.Unlock()
	})
	tamperedResponse, err := clientHTTP.Get(server.URL + downloadPath)
	require.NoError(t, err)
	tamperedBytes, err := io.ReadAll(tamperedResponse.Body)
	require.NoError(t, err)
	require.NoError(t, tamperedResponse.Body.Close())
	require.Equalf(t, http.StatusServiceUnavailable, tamperedResponse.StatusCode, string(tamperedBytes), "digest mismatch must not disclose tampered bytes")
	require.NotEqual(t, tamperedObject.body, tamperedBytes)

	// The evidence ledger is append-only even for direct application writes.
	mutationErr := configuration.DB.Transaction(func(tx *gorm.DB) error {
		return tx.Model(&models.DeliveryPlanStepEvidence{}).Where("id = ?", record.ID).Update("file_name", "rewritten.txt").Error
	})
	require.Error(t, mutationErr)
	require.Contains(t, strings.ToLower(mutationErr.Error()), "append-only")
	var unchanged models.DeliveryPlanStepEvidence
	require.NoError(t, configuration.DB.Where("id = ?", record.ID).First(&unchanged).Error)
	require.Equal(t, "qa-report.txt", unchanged.FileName)

}

func evidenceTimePtr(value time.Time) *time.Time { return &value }
