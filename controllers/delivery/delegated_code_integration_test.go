//go:build integration

package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/deliveryledger"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDelegatedCodePublicationPostgresContinuation(t *testing.T) {
	container, err := postgrescontainer.Run(context.Background(), "postgres:16-alpine", postgrescontainer.WithDatabase("testdb"), postgrescontainer.WithUsername("test"), postgrescontainer.WithPassword("test"), postgrescontainer.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(context.Background(), "sslmode=disable")
	require.NoError(t, err)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableForeignKeyConstraintWhenMigrating: true})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error)
	require.NoError(t, configuration.MigrateModelsForTest(db))
	seed := func(t *testing.T, human bool) coordinatorFixture {
		t.Helper()
		f := newCoordinatorFixture(t)
		f.item.State, f.intent.Phase, f.task.Operation = deliveryworkflow.StateImplementation, "implementation", "delivery.implementation"
		f.task.QASourceReceiptRequired = false
		mandate := defaultDeliveryMandate(f.item, []string{"workspace://api"})
		if !human {
			mandate.HumanActions = []string{"approve_plan", "approve_release", "resolve_blocker"}
		}
		f.item.MandateJSON, err = marshalDeliveryMandate(mandate)
		require.NoError(t, err)
		f.changes = f.changes[1:]
		var metadata map[string]any
		require.NoError(t, json.Unmarshal([]byte(f.changes[0].MetadataJSON), &metadata))
		metadata["base_sha"], metadata["github_repository"] = f.authority.Repositories[0].SourceRevision, "example/service"
		encoded, err := json.Marshal(metadata)
		require.NoError(t, err)
		f.changes[0].MetadataJSON = string(encoded)
		require.NoError(t, db.Create(&f.item).Error)
		require.NoError(t, db.Create(&f.intent).Error)
		require.NoError(t, db.Create(&f.task).Error)
		require.NoError(t, db.Create(&f.changes).Error)
		_, _, err = deliveryledger.RecordAutonomySnapshot(db, f.item.ID, f.authority, f.now.Add(-time.Hour))
		require.NoError(t, err)
		approval := models.DeliveryGate{WorkItemID: f.item.ID, Kind: deliveryworkflow.GatePlan, Decision: deliveryworkflow.DecisionApproved, Authority: "human", DecidedBy: "synthetic-owner", DecidedAt: f.now.Add(-time.Hour)}
		require.NoError(t, db.Create(&approval).Error)
		plan := models.DeliveryPlan{WorkItemID: f.item.ID, Version: 1, Status: "approved", StructuredJSON: f.item.PlanJSON, ApprovedGateID: &approval.ID, ContextDigest: "synthetic-context", ProposedBy: "synthetic-owner", CreatedAt: f.now.Add(-time.Hour)}
		require.NoError(t, db.Create(&plan).Error)
		_, _, err = ensureDeliveryPlanStepsTx(db, plan, "synthetic-owner")
		require.NoError(t, err)
		require.NoError(t, db.Model(&models.DeliveryPlanStep{}).Where("plan_id = ?", plan.ID).Update("status", models.DeliveryPlanStepCompleted).Error)
		return f
	}
	count := func(t *testing.T, model any, item uuid.UUID) int64 {
		t.Helper()
		var n int64
		require.NoError(t, db.Model(model).Where("work_item_id = ?", item).Count(&n).Error)
		return n
	}

	t.Run("human mandate preserves publication decision", func(t *testing.T) {
		f := seed(t, true)
		require.NoError(t, completeContinuation(db, f.intent, f.task, nil))
		require.Zero(t, count(t, &models.DeliveryPublicationGrant{}, f.item.ID))
		var item models.DeliveryWorkItem
		require.NoError(t, db.First(&item, f.item.ID).Error)
		require.Equal(t, "waiting_for_user", item.AgentProgress)
	})
	t.Run("scheduling failure rolls back grant and submitted state", func(t *testing.T) {
		f := seed(t, false)
		require.NoError(t, db.Exec(fmt.Sprintf("ALTER TABLE delivery_continuations ADD CONSTRAINT synthetic_no_publication CHECK (work_item_id <> '%s' OR phase <> 'publish')", f.item.ID)).Error)
		t.Cleanup(func() {
			require.NoError(t, db.Exec("ALTER TABLE delivery_continuations DROP CONSTRAINT synthetic_no_publication").Error)
		})
		require.Error(t, completeContinuation(db, f.intent, f.task, nil))
		require.Zero(t, count(t, &models.DeliveryPublicationGrant{}, f.item.ID))
		var item models.DeliveryWorkItem
		require.NoError(t, db.First(&item, f.item.ID).Error)
		require.Equal(t, deliveryworkflow.StateImplementation, item.State)
		require.Equal(t, f.item.AutomationEpoch, item.AutomationEpoch)
	})
	for _, limitation := range []string{"tools", "repository"} {
		t.Run("mandate limits publication "+limitation, func(t *testing.T) {
			f := seed(t, false)
			mandate, err := resolveDeliveryMandate(f.item, nil)
			require.NoError(t, err)
			if limitation == "tools" {
				mandate.AllowedTools = []string{"context.read", "evidence.read"}
			} else {
				mandate.RepositoryRefs = []string{"workspace://other"}
			}
			raw, err := marshalDeliveryMandate(mandate)
			require.NoError(t, err)
			require.NoError(t, db.Model(&f.item).Update("mandate_json", raw).Error)
			require.Error(t, completeContinuation(db, f.intent, f.task, nil))
			require.Zero(t, count(t, &models.DeliveryPublicationGrant{}, f.item.ID))
		})
	}
	for _, scenario := range []string{"approved", "correction", "tampered", "missing findings", "security", "correction cap"} {
		t.Run("durable publication and independent review "+scenario, func(t *testing.T) {
			negative := scenario != "approved"
			f := seed(t, false)
			require.NoError(t, completeContinuation(db, f.intent, f.task, nil))
			require.NoError(t, completeContinuation(db, f.intent, f.task, nil))
			require.EqualValues(t, 1, count(t, &models.DeliveryPublicationGrant{}, f.item.ID))
			var grant models.DeliveryPublicationGrant
			require.NoError(t, db.Where("work_item_id = ?", f.item.ID).First(&grant).Error)
			require.Equal(t, delegatedCoordinatorActor, grant.GrantedBy)
			require.Equal(t, f.authority.Repositories[0].SourceRevision, grant.BaseSHA)
			require.Equal(t, "example/service", grant.GitHubRepository)
			var reviewIntent, publicationIntent models.DeliveryContinuation
			require.NoError(t, db.Where("work_item_id = ? AND phase = ?", f.item.ID, "code_review").First(&reviewIntent).Error)
			require.NoError(t, db.Where("work_item_id = ? AND phase = ?", f.item.ID, "publish").First(&publicationIntent).Error)
			// Reconciliation can run on a new API replica without a model call.
			require.NoError(t, advanceDelegatedCodeReview(db, reviewIntent))
			var before models.DeliveryWorkItem
			require.NoError(t, db.First(&before, f.item.ID).Error)
			require.Equal(t, deliveryworkflow.StateCodeReview, before.State)
			publishedAt := time.Now().UTC().Truncate(time.Microsecond)
			instance := uuid.Must(uuid.NewV4())
			publicationTask := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), Operation: "delivery.publish", Status: "completed", DeliveryWorkItemID: &f.item.ID, ContinuationID: &publicationIntent.ID, AgentInstanceID: &instance, EvidenceSubjectDigest: grant.ReviewDiffSHA256, CreatedAt: grant.GrantedAt, CompletedAt: &publishedAt, OutputRef: "s3://synthetic-private/publication.json"}
			require.NoError(t, db.Create(&publicationTask).Error)
			metadata, _ := json.Marshal(map[string]any{"branch_published": true, "verification_source": "itbem-github-app", "remote_repository": grant.GitHubRepository, "target_branch": "main", "base_sha": grant.BaseSHA, "review_diff_sha256": grant.ReviewDiffSHA256, "publication_grant_id": grant.ID.String(), "automation_task_id": publicationTask.ID.String(), "security_checks": []string{"security:secrets", "security:high-critical"}})
			head := fmt.Sprintf("%x", sha256.Sum256([]byte(publicationTask.ID.String())))[:40]
			publication := models.DeliveryChangeSet{WorkItemID: f.item.ID, RepositoryRef: grant.RepositoryRef, Branch: grant.Branch, CommitSHA: head, ReviewType: "pull_request", PullRequestURL: "https://github.com/example/service/pull/42", CIStatus: "passed", PreviewURL: "https://preview.example.test", MetadataJSON: string(metadata), CreatedBy: "itbem-github-app", CreatedAt: publishedAt}
			require.NoError(t, db.Create(&publication).Error)
			require.NoError(t, db.Model(&grant).Updates(map[string]any{"revoked_at": publishedAt, "revoked_by": "itbem-github-app", "revocation_reason": "Consumed after publication"}).Error)
			require.NoError(t, completeContinuation(db, publicationIntent, publicationTask, nil))
			require.NoError(t, advanceDelegatedCodeReview(db, reviewIntent))
			review := f.review
			review.HeadSHA, review.PublishedAt = head, publishedAt.Add(time.Millisecond)
			review.ReviewID = time.Now().UnixNano()
			checkID := review.ReviewID
			review.CheckRunID = &checkID
			review.SubjectSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(review.ID.String())))
			if negative {
				review.ReviewGatePassed, review.Event, review.Verdict = false, "REQUEST_CHANGES", "request_changes"
				result, err := automationagent.ParseCodeReview(`{"summary":"The approved handler has a null guard regression.","verdict":"request_changes","review_scope":["approved handler"],"findings":[{"id":"null-guard","severity":"medium","category":"correctness","title":"Restore null guard","file":"handler.go","line_start":7,"line_end":7,"evidence":"The approved handler dereferences the result.","evidence_quote":"result.Value","recommendation":"Guard a missing result before accessing its value.","confidence":0.95}],"test_plan":["Verify a missing result is handled."],"coverage_gaps":[]}`)
				require.NoError(t, err)
				canonical, err := json.Marshal(result)
				require.NoError(t, err)
				review.ReviewResultJSON, review.ReviewResultSHA256 = string(canonical), fmt.Sprintf("%x", sha256.Sum256(canonical))
			}
			switch scenario {
			case "tampered":
				review.ReviewResultSHA256 = strings.Repeat("0", 64)
			case "missing findings":
				review.ReviewResultJSON, review.ReviewResultSHA256 = "{}", ""
			case "security":
				review.ReviewResultJSON = strings.ReplaceAll(strings.ReplaceAll(review.ReviewResultJSON, `"medium"`, `"high"`), `"correctness"`, `"security"`)
				review.ReviewResultSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(review.ReviewResultJSON)))
			case "correction cap":
				for range maximumDelegatedCorrections {
					previous := models.DeliveryGate{WorkItemID: f.item.ID, Kind: deliveryworkflow.GateCodeReview, Decision: deliveryworkflow.DecisionChangesRequested, Authority: "delegated", DecidedBy: delegatedCoordinatorActor, DecidedAt: f.now}
					require.NoError(t, db.Create(&previous).Error)
				}
			}
			require.NoError(t, db.Create(&review).Error)
			if scenario != "approved" && scenario != "correction" {
				err := advanceDelegatedCodeReview(db, reviewIntent)
				if scenario == "tampered" {
					require.ErrorContains(t, err, "seal")
				} else {
					require.NoError(t, err)
				}
				var item models.DeliveryWorkItem
				require.NoError(t, db.First(&item, f.item.ID).Error)
				require.Equal(t, deliveryworkflow.StateCodeReview, item.State)
				require.EqualValues(t, 1, count(t, &models.DeliveryPlan{}, item.ID))
				if scenario != "tampered" {
					require.Equal(t, "blocked", item.AgentProgress)
				}
				return
			}
			var wg sync.WaitGroup
			errors := make(chan error, 2)
			for range 2 {
				wg.Add(1)
				go func() { defer wg.Done(); errors <- advanceDelegatedCodeReview(db, reviewIntent) }()
			}
			wg.Wait()
			close(errors)
			for err := range errors {
				require.NoError(t, err)
			}
			require.NoError(t, advanceDelegatedCodeReview(db, reviewIntent))
			var item models.DeliveryWorkItem
			require.NoError(t, db.First(&item, f.item.ID).Error)
			var gates []models.DeliveryGate
			require.NoError(t, db.Where("work_item_id = ? AND kind = ?", item.ID, deliveryworkflow.GateCodeReview).Find(&gates).Error)
			require.Len(t, gates, 1)
			require.Equal(t, "delegated", gates[0].Authority)
			require.Contains(t, gates[0].EvidenceChecklist, review.ID.String())
			if negative {
				require.Equal(t, deliveryworkflow.StateImplementation, item.State)
				require.EqualValues(t, 2, count(t, &models.DeliveryPlan{}, item.ID))
			} else {
				require.Equal(t, deliveryworkflow.StatePreviewPending, item.State)
				var previewIntent models.DeliveryContinuation
				require.NoError(t, db.Where("work_item_id = ? AND phase = ?", item.ID, "preview").First(&previewIntent).Error)
				require.NoError(t, advanceAvailablePreview(db, previewIntent))
				require.NoError(t, db.First(&item, f.item.ID).Error)
				require.Equal(t, deliveryworkflow.StateQARunning, item.State)
				var qa []models.DeliveryContinuation
				require.NoError(t, db.Where("work_item_id = ? AND phase = ?", item.ID, "qa").Find(&qa).Error)
				require.Len(t, qa, 1)
			}
		})
	}
}
