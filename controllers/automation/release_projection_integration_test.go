//go:build integration

package automation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/deliverypolicy"
	"events-stocks/internal/environmentevidence"
	"events-stocks/internal/qaevidence"
	"events-stocks/internal/releasegate"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Exercises the real callback projection and append-only ledger, without
// provider calls or production credentials. HTTP authentication is separate.
func TestReleaseProjectionRollsBackEvidenceWhenCurrentPolicyIsMissing(t *testing.T) {
	ctx := context.Background()
	container, err := postgrescontainer.Run(ctx, "postgres:16-alpine", postgrescontainer.WithDatabase("testdb"), postgrescontainer.WithUsername("test"), postgrescontainer.WithPassword("test"), postgrescontainer.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableForeignKeyConstraintWhenMigrating: true})
	require.NoError(t, err)
	rawDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rawDB.Close()) })
	require.NoError(t, db.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error)
	require.NoError(t, configuration.MigrateModelsForTest(db))

	itemID, taskID, projectID, grantID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	now := time.Now().UTC().Truncate(time.Microsecond)
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	branch := "itbem-agent/11111111-1111-4111-8111-111111111111"
	require.NoError(t, db.Create(&models.DeliveryProject{ID: projectID, ClientID: uuid.Must(uuid.NewV4()), Name: "synthetic", Slug: projectID.String(), CreatedBy: "synthetic-human"}).Error)
	require.NoError(t, db.Create(&models.DeliveryWorkItem{ID: itemID, ProjectID: projectID, RequestedBy: "synthetic-human", Title: "synthetic release", PlanJSON: `{"repository_impact":[{"reference":"workspace://repo","impact":"changes"}]}`}).Error)
	require.NoError(t, db.Create(&models.DeliveryPublicationGrant{ID: grantID, WorkItemID: itemID, RepositoryRef: "workspace://repo", BaseSHA: base, GitHubRepository: "example/service", ReviewDiffSHA256: strings.Repeat("c", 64), Branch: branch, GrantedBy: "synthetic-human", GrantedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), RevokedBy: "itbem-github-app", RevokedAt: &now}).Error)
	metadata, err := json.Marshal(map[string]any{"publication_grant_id": grantID.String(), "base_sha": base, "remote_repository": "example/service", "target_branch": "main", "branch_published": true, "verification_source": "itbem-github-app"})
	require.NoError(t, err)
	require.NoError(t, db.Create(&models.DeliveryChangeSet{ID: uuid.Must(uuid.NewV4()), WorkItemID: itemID, RepositoryRef: "workspace://repo", Branch: branch, CommitSHA: head, ReviewType: "pull_request", PullRequestURL: "https://github.com/example/service/pull/42", CreatedBy: "itbem-github-app", MetadataJSON: string(metadata), CreatedAt: now}).Error)
	candidate := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: itemID.String(), Revisions: []releasegate.Revision{{Repository: "example/service", Branch: "main", SHA: head}}, Policy: releasegate.Policy{Resolved: false}}
	digest, err := releasegate.RevisionMatrixDigest(candidate.Revisions)
	require.NoError(t, err)
	task := models.AutomationTask{ID: taskID, JobID: uuid.Must(uuid.NewV4()), Operation: "delivery.release_gate", DeliveryWorkItemID: &itemID, EvidenceSubjectDigest: digest, RequestedBy: "synthetic-human", Status: "completed", CompletedAt: &now}
	require.NoError(t, db.Create(&task).Error)
	environment := environmentevidence.Observation{SchemaVersion: 1, TaskID: taskID.String(), MatrixDigest: digest, Repositories: []environmentevidence.Repository{{Repository: "example/service", HeadSHA: head, Workflow: ".github/workflows/deploy.yml", Environment: "production", WorkflowExists: true, EnvironmentExists: true, RequiredSecretReferences: []string{}, RequiredVariableReferences: []string{}, MissingSecretReferences: []string{}, MissingVariableReferences: []string{}}}}
	handoff, err := json.Marshal(map[string]any{"schema_version": 2, "gatekeeper_input": candidate, "environment_observation": environment})
	require.NoError(t, err)
	err = db.Transaction(func(tx *gorm.DB) error { return persistReleaseGateEvaluation(tx, &task, handoff, now) })
	require.ErrorContains(t, err, "current environment policy")
	var count int64
	require.NoError(t, db.Model(&models.DeliveryEvent{}).Where("work_item_id = ?", itemID).Count(&count).Error)
	require.Zero(t, count, "rejected policy must roll back the earlier environment observation")
	var project models.DeliveryProject
	require.NoError(t, db.First(&project, projectID).Error)
	mode, method := deliverypolicy.ModeRelease, "squash"
	tests, branches, health, empty := []string{"unit", "contract"}, []string{"main"}, []string{"health"}, []string{}
	workflow, environmentName, recovery := ".github/workflows/deploy.yml", "production", string(releasegate.RecoveryRollback)
	patch := deliverypolicy.Patch{Mode: &mode, MergeMethod: &method, RequiredTestKinds: &tests, AllowedTargetBranches: &branches, DeploymentWorkflow: &workflow, DeploymentEnvironment: &environmentName, RequiredSecretReferences: &empty, RequiredVariableReferences: &empty, RequiredHealthChecks: &health, RecoveryDefault: &recovery}
	policyID := uuid.Must(uuid.NewV4())
	layer := deliverypolicy.Layer{SchemaVersion: deliverypolicy.SchemaVersion, RevisionID: policyID.String(), Level: deliverypolicy.LevelProject, OrganizationID: project.ClientID.String(), ProjectID: project.ID.String(), Patch: patch}
	policyDigest, err := deliverypolicy.LayerDigest(layer)
	require.NoError(t, err)
	patchRaw, err := json.Marshal(patch)
	require.NoError(t, err)
	require.NoError(t, db.Create(&models.DeliveryPolicyRevision{ID: policyID, SchemaVersion: deliverypolicy.SchemaVersion, Level: string(deliverypolicy.LevelProject), OrganizationID: project.ClientID.String(), ProjectID: &project.ID, PatchJSON: string(patchRaw), ContentSHA256: policyDigest, ProposedBy: "synthetic-policy-author", CreatedAt: now.Add(-2 * time.Hour)}).Error)
	require.NoError(t, db.Create(&models.DeliveryPolicyDecision{ID: uuid.Must(uuid.NewV4()), PolicyRevisionID: policyID, PolicyDigest: policyDigest, Action: "approved", ActorCognitoSub: "synthetic-independent-reviewer", OccurredAt: now.Add(-time.Hour)}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return persistReleaseGateEvaluation(tx, &task, handoff, now) }))
	var events []models.DeliveryEvent
	require.NoError(t, db.Where("work_item_id = ?", itemID).Order("sequence").Find(&events).Error)
	require.Len(t, events, 2)
	envProjection, err := deliveryledger.ProjectEnvironmentObservation(events[0])
	require.NoError(t, err)
	require.Equal(t, digest, envProjection.Observation.MatrixDigest)
	gateProjection, err := deliveryledger.ProjectGateEvaluation(events[1])
	require.NoError(t, err)
	require.Equal(t, "blocked", gateProjection.State, "missing QA and Vault must remain blocking even after policy approval")
	require.NotEmpty(t, gateProjection.Reasons)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return persistReleaseGateEvaluation(tx, &task, handoff, now) }))
	require.NoError(t, db.Model(&models.DeliveryEvent{}).Where("work_item_id = ?", itemID).Count(&count).Error)
	require.Equal(t, int64(2), count, "projection replay must not duplicate events")

	qaID := uuid.Must(uuid.NewV4())
	require.NoError(t, db.Create(&models.DeliveryContextSnapshot{ID: uuid.Must(uuid.NewV4()), WorkItemID: itemID, SourceID: uuid.Must(uuid.NewV4()), Kind: "repository", Name: "synthetic", Reference: "workspace://repo", Revision: head, MetadataJSON: `{"depends_on_repositories":[]}`, CapturedAt: now}).Error)
	qaTime := now.Add(time.Second)
	qaTask := models.AutomationTask{ID: qaID, JobID: uuid.Must(uuid.NewV4()), Operation: "delivery.qa", DeliveryWorkItemID: &itemID, EvidenceSubjectDigest: digest, Status: "completed", CompletedAt: &qaTime}
	require.NoError(t, db.Create(&qaTask).Error)
	qaObservation := qaevidence.Observation{SchemaVersion: 2, TaskID: qaID.String(), MatrixDigest: digest, PreviewPassed: true, RepositoryExecutionOrder: []string{"workspace://repo"}, Repositories: []qaevidence.Repository{{Reference: "workspace://repo", Branch: branch, Commands: []qaevidence.Command{{Index: 0, Phase: "validation", Kind: "unit", Passed: true}, {Index: 1, Phase: "qa", Kind: "contract", Passed: true}, {Index: 2, Phase: "qa", Kind: "security:secrets", Passed: true}, {Index: 3, Phase: "qa", Kind: "security:high-critical", Passed: true}}}}}
	qaRaw, err := json.Marshal(qaObservation)
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return persistQAObservation(tx, &qaTask, qaRaw, qaTime) }))
	nextTime := now.Add(2 * time.Second)
	nextTask := task
	nextTask.ID = uuid.Must(uuid.NewV4())
	nextTask.JobID = uuid.Must(uuid.NewV4())
	nextTask.CompletedAt = &nextTime
	require.NoError(t, db.Create(&nextTask).Error)
	environment.TaskID = nextTask.ID.String()
	nextHandoff, err := json.Marshal(map[string]any{"schema_version": 2, "gatekeeper_input": candidate, "environment_observation": environment})
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return persistReleaseGateEvaluation(tx, &nextTask, nextHandoff, nextTime) }))
	var latest models.DeliveryEvent
	require.NoError(t, db.Where("work_item_id = ? AND event_type = ?", itemID, deliveryledger.EventTypeReleaseGateEvaluated).Order("sequence DESC").First(&latest).Error)
	var gatePayload struct {
		Input releasegate.Input `json:"input"`
	}
	require.NoError(t, json.Unmarshal([]byte(latest.PayloadJSON), &gatePayload))
	require.Len(t, gatePayload.Input.Tests, 2)
	for _, test := range gatePayload.Input.Tests {
		require.Equal(t, releasegate.StatusPassed, test.Status)
		require.Equal(t, digest, test.MatrixDigest)
	}
	require.Len(t, gatePayload.Input.Security, 1)
	require.True(t, gatePayload.Input.Security[0].SecretScanPassed)
	projected, err := deliveryledger.ProjectGateEvaluation(latest)
	require.NoError(t, err)
	require.Equal(t, "blocked", projected.State, "QA success must not replace absent Vault authority")

	// Exercise the real signed terminal callback with an original immutable
	// request/result pair. A recovered deterministic run still needs both refs.
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	encodedPublic, err := agentcallbackauth.EncodePublicKey(public)
	require.NoError(t, err)
	instance := uuid.Must(uuid.NewV4())
	require.NoError(t, db.Create(&models.AutomationAgentInstance{ID: instance, AgentKey: "release", MachineID: "synthetic", PublicKey: encodedPublic, Status: "active"}).Error)
	recoveredTask := nextTask
	recoveredTask.ID = uuid.Must(uuid.NewV4())
	recoveredTask.JobID = uuid.Must(uuid.NewV4())
	recoveredTask.Status, recoveredTask.RunID = "running", uuid.Must(uuid.NewV4()).String()
	recoveredTask.CompletedAt = nil
	recoveredTask.AgentInstanceID, recoveredTask.AgentKey, recoveredTask.MachineID = &instance, "release", "synthetic"
	require.NoError(t, db.Create(&recoveredTask).Error)
	environment.TaskID = recoveredTask.ID.String()
	recoveredHandoff, err := json.Marshal(map[string]any{"schema_version": 2, "gatekeeper_input": candidate, "environment_observation": environment})
	require.NoError(t, err)
	originalRun := uuid.Must(uuid.NewV4()).String()
	prefix := "s3://synthetic-outputs/automation/" + recoveredTask.ID.String() + "/runs/" + originalRun
	callback := callbackRequest{Status: "completed", RunID: recoveredTask.RunID, RecoveryRunID: originalRun, OutputRef: prefix + "/result.json", Execution: recoveredHandoff, Deterministic: true}
	app := echo.New()
	app.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("config", &models.Config{AutomationOutputBucket: "synthetic-outputs"})
			return next(c)
		}
	})
	app.PUT("/api/internal/automation/tasks/:id", Complete, AgentCallbackAuthentication)
	call := func() *httptest.ResponseRecorder {
		body, marshalErr := json.Marshal(map[string]any{"status": callback.Status, "run_id": callback.RunID, "recovery_run_id": callback.RecoveryRunID, "request_ref": callback.RequestRef, "output_ref": callback.OutputRef, "execution": callback.Execution, "deterministic": callback.Deterministic})
		require.NoError(t, marshalErr)
		path := "/api/internal/automation/tasks/" + recoveredTask.ID.String()
		timestamp, nonce := time.Now().UTC().Unix(), uuid.Must(uuid.NewV4()).String()
		signature, signErr := agentcallbackauth.SignRequest(private, instance.String(), http.MethodPut, path, timestamp, nonce, body)
		require.NoError(t, signErr)
		encoded, encodeErr := agentcallbackauth.EncodeSignature(signature)
		require.NoError(t, encodeErr)
		request := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(agentcallbackauth.InstanceIDHeader, instance.String())
		request.Header.Set(agentcallbackauth.TimestampHeader, strconv.FormatInt(timestamp, 10))
		request.Header.Set(agentcallbackauth.NonceHeader, nonce)
		request.Header.Set(agentcallbackauth.SignatureHeader, encoded)
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		return response
	}
	rejected := call()
	require.Equal(t, http.StatusBadRequest, rejected.Code, rejected.Body.String())
	callback.RequestRef = prefix + "/request.json"
	accepted := call()
	require.Equal(t, http.StatusNoContent, accepted.Code, accepted.Body.String())
	var completed models.AutomationTask
	require.NoError(t, db.First(&completed, recoveredTask.ID).Error)
	require.Equal(t, "completed", completed.Status)
	latest = models.DeliveryEvent{}
	require.NoError(t, db.Where("work_item_id = ? AND event_type = ?", itemID, deliveryledger.EventTypeReleaseGateEvaluated).Order("sequence DESC").First(&latest).Error)
	projected, err = deliveryledger.ProjectGateEvaluation(latest)
	require.NoError(t, err)
	require.Equal(t, "blocked", projected.State)

}
