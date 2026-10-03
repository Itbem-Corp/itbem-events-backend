//go:build integration

package delivery

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/deliverypolicy"
	"events-stocks/internal/releasegate"
	"events-stocks/models"
	"events-stocks/services/deliveryplansteps"
	"events-stocks/services/deliveryworkflow"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDelegatedQACoordinatorPostgresTransactions(t *testing.T) {
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

	seed := func(t *testing.T, fail bool, mutate func(*coordinatorFixture)) (coordinatorFixture, models.DeliveryPlan) {
		t.Helper()
		f := newCoordinatorFixture(t)
		head := fmt.Sprintf("%x", sha256.Sum256([]byte(f.task.ID.String())))[:40]
		f.candidate.Revisions[0].SHA = head
		matrix, err := releasegate.RevisionMatrixDigest(f.candidate.Revisions)
		require.NoError(t, err)
		f.task.EvidenceSubjectDigest, f.observation.MatrixDigest = matrix, matrix
		f.receipts[0].MatrixDigest, f.receipts[0].CommitSHA = matrix, head
		f.changes[0].CommitSHA, f.review.HeadSHA = head, head
		f.observation.Repositories[0].Commands[0].Passed = !fail
		// Provider identifiers and sealed subjects must be unique across fixtures.
		f.review.ReviewID = time.Now().UnixNano()
		checkID := f.review.ReviewID
		f.review.CheckRunID = &checkID
		f.review.SubjectSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(f.task.ID.String())))
		if mutate != nil {
			mutate(&f)
		}
		require.NoError(t, db.Create(&f.item).Error)
		require.NoError(t, db.Create(&f.intent).Error)
		require.NoError(t, db.Create(&f.task).Error)
		_, _, err = deliveryledger.RecordAutonomySnapshot(db, f.item.ID, f.authority, f.now.Add(-time.Hour))
		require.NoError(t, err)
		_, _, err = deliveryledger.RecordQAObservation(db, f.item.ID, f.observation, *f.task.CompletedAt)
		require.NoError(t, err)
		require.NoError(t, db.Create(&f.changes).Error)
		require.NoError(t, db.Create(&f.receipts).Error)
		require.NoError(t, db.Create(&f.review).Error)
		approval := models.DeliveryGate{ID: uuid.Must(uuid.NewV4()), WorkItemID: f.item.ID, Kind: deliveryworkflow.GatePlan, Decision: deliveryworkflow.DecisionApproved, Authority: "human", DecidedBy: "synthetic-owner", DecidedAt: f.now.Add(-time.Hour)}
		require.NoError(t, db.Create(&approval).Error)
		plan := models.DeliveryPlan{WorkItemID: f.item.ID, Version: 1, Status: "approved", Summary: "Approved bounded correction", StructuredJSON: f.item.PlanJSON, ApprovedGateID: &approval.ID, ContextDigest: "synthetic-context", ProposedBy: "synthetic-owner", CreatedAt: f.now.Add(-time.Hour)}
		require.NoError(t, db.Create(&plan).Error)
		_, _, err = ensureDeliveryPlanStepsTx(db, plan, "synthetic-owner")
		require.NoError(t, err)
		require.NoError(t, db.Model(&models.DeliveryPlanStep{}).Where("plan_id = ?", plan.ID).Update("status", models.DeliveryPlanStepCompleted).Error)
		return f, plan
	}
	assertUnchanged := func(t *testing.T, f coordinatorFixture) {
		t.Helper()
		var item models.DeliveryWorkItem
		require.NoError(t, db.First(&item, f.item.ID).Error)
		require.Equal(t, deliveryworkflow.StateQARunning, item.State)
		require.Equal(t, f.item.AutomationEpoch, item.AutomationEpoch)
		var count int64
		require.NoError(t, db.Model(&models.DeliveryGate{}).Where("work_item_id = ? AND kind = ?", f.item.ID, deliveryworkflow.GateQAReview).Count(&count).Error)
		require.Zero(t, count)
		require.NoError(t, db.Model(&models.DeliveryContinuation{}).Where("work_item_id = ? AND status = ?", f.item.ID, "pending").Count(&count).Error)
		require.Zero(t, count)
		var intent models.DeliveryContinuation
		require.NoError(t, db.First(&intent, f.intent.ID).Error)
		require.Equal(t, "dispatched", intent.Status)
		require.NoError(t, db.Model(&models.DeliveryEvidence{}).Where("work_item_id = ?", f.item.ID).Count(&count).Error)
		require.Zero(t, count)
	}

	t.Run("concurrent success advances exactly once without model authority", func(t *testing.T) {
		f, _ := seed(t, false, nil)
		var wg sync.WaitGroup
		errors := make(chan error, 2)
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errors <- completeContinuation(db, f.intent, f.task, map[string]any{"verdict": "failed"})
			}()
		}
		wg.Wait()
		close(errors)
		for err := range errors {
			require.NoError(t, err)
		}
		var item models.DeliveryWorkItem
		require.NoError(t, db.First(&item, f.item.ID).Error)
		require.Equal(t, deliveryworkflow.StateReleaseReview, item.State)
		require.Equal(t, f.item.AutomationEpoch+1, item.AutomationEpoch)
		require.Equal(t, "queued", item.AgentProgress)
		var gates []models.DeliveryGate
		require.NoError(t, db.Where("work_item_id = ? AND kind = ?", item.ID, deliveryworkflow.GateQAReview).Find(&gates).Error)
		require.Len(t, gates, 1)
		require.Equal(t, "delegated", gates[0].Authority)
		require.Equal(t, deliveryworkflow.DecisionApproved, gates[0].Decision)
		require.Contains(t, gates[0].EvidenceChecklist, "qa_event:")
		var next []models.DeliveryContinuation
		require.NoError(t, db.Where("work_item_id = ? AND status = ?", item.ID, "pending").Find(&next).Error)
		require.Len(t, next, 1)
		require.Equal(t, "summary", next[0].Phase)
		require.Equal(t, item.AutomationEpoch, next[0].Epoch)
		var reports []models.DeliveryEvidence
		require.NoError(t, db.Where("work_item_id = ?", item.ID).Find(&reports).Error)
		require.Len(t, reports, 1)
		require.Equal(t, f.task.OutputRef, reports[0].Reference)
		require.Contains(t, reports[0].MetadataJSON, f.task.ID.String())
	})
	t.Run("failed test allocates fresh approved steps and retains old results", func(t *testing.T) {
		f, original := seed(t, true, nil)
		require.NoError(t, completeContinuation(db, f.intent, f.task, nil))
		var item models.DeliveryWorkItem
		require.NoError(t, db.First(&item, f.item.ID).Error)
		require.Equal(t, deliveryworkflow.StateImplementation, item.State)
		var plans []models.DeliveryPlan
		require.NoError(t, db.Where("work_item_id = ?", item.ID).Order("version ASC").Find(&plans).Error)
		require.Len(t, plans, 2)
		require.Equal(t, plans[0].StructuredJSON, plans[1].StructuredJSON)
		require.Equal(t, original.ApprovedGateID, plans[1].ApprovedGateID)
		require.Equal(t, "approved", plans[1].Status)
		var old, fresh []models.DeliveryPlanStep
		require.NoError(t, db.Where("plan_id = ?", original.ID).Find(&old).Error)
		require.NoError(t, db.Where("plan_id = ?", plans[1].ID).Find(&fresh).Error)
		require.Len(t, old, 2)
		require.Len(t, fresh, 2)
		for _, step := range old {
			require.Equal(t, models.DeliveryPlanStepCompleted, step.Status)
		}
		for _, step := range fresh {
			require.Equal(t, models.DeliveryPlanStepPlanned, step.Status)
			require.Nil(t, step.AutomationTaskID)
			require.Empty(t, step.RunID)
			for _, prior := range old {
				require.NotEqual(t, prior.ID, step.ID)
			}
		}
		var next models.DeliveryContinuation
		require.NoError(t, db.Where("work_item_id = ? AND status = ?", item.ID, "pending").First(&next).Error)
		require.Equal(t, "implementation", next.Phase)
		var gate models.DeliveryGate
		require.NoError(t, db.Where("work_item_id = ? AND kind = ?", item.ID, deliveryworkflow.GateQAReview).First(&gate).Error)
		require.Equal(t, "delegated", gate.Authority)
		require.Equal(t, deliveryworkflow.DecisionChangesRequested, gate.Decision)
		// The new version must be admissible and have a dispatchable root, rather
		// than silently inheriting completed steps from the previous attempt.
		parent := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), Operation: "delivery.implementation", Status: "pending", DeliveryWorkItemID: &item.ID}
		require.NoError(t, db.Create(&parent).Error)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			execution, created, err := deliveryplansteps.CreateExecutionInTransaction(tx, parent.ID, plans[1].ID, 1, "synthetic-correction", f.now)
			if err != nil {
				return err
			}
			require.True(t, created)
			_, ready, err := deliveryplansteps.ReadyPlanStepsInTransaction(tx, execution.ID, f.now)
			if err != nil {
				return err
			}
			require.Len(t, ready, 1)
			require.Equal(t, "correct", ready[0].StepKey)
			return nil
		}))
	})
	t.Run("human policy retains manual QA decision", func(t *testing.T) {
		f, _ := seed(t, false, func(f *coordinatorFixture) {
			f.authority.Repositories[0].Policy.GateApprovalMode = deliverypolicy.GateApprovalHuman
		})
		require.NoError(t, completeContinuation(db, f.intent, f.task, map[string]any{"structured_result": map[string]any{"verdict": "passed"}}))
		var item models.DeliveryWorkItem
		require.NoError(t, db.First(&item, f.item.ID).Error)
		require.Equal(t, deliveryworkflow.StateQAReview, item.State)
		require.Equal(t, "waiting_for_user", item.AgentProgress)
		var count int64
		require.NoError(t, db.Model(&models.DeliveryGate{}).Where("work_item_id = ? AND kind = ?", item.ID, deliveryworkflow.GateQAReview).Count(&count).Error)
		require.Zero(t, count)
	})
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("human mandate remains manual explicit=%t", explicit), func(t *testing.T) {
			f, _ := seed(t, false, func(f *coordinatorFixture) {
				f.item.MandateJSON = ""
				if explicit {
					mandate := defaultDeliveryMandate(f.item, []string{"workspace://api"})
					var err error
					f.item.MandateJSON, err = marshalDeliveryMandate(mandate)
					require.NoError(t, err)
				}
			})
			require.NoError(t, completeContinuation(db, f.intent, f.task, map[string]any{"structured_result": map[string]any{"verdict": "passed"}}))
			var item models.DeliveryWorkItem
			require.NoError(t, db.First(&item, f.item.ID).Error)
			require.Equal(t, deliveryworkflow.StateQAReview, item.State)
			require.Equal(t, "waiting_for_user", item.AgentProgress)
			var count int64
			require.NoError(t, db.Model(&models.DeliveryGate{}).Where("work_item_id = ? AND kind = ?", item.ID, deliveryworkflow.GateQAReview).Count(&count).Error)
			require.Zero(t, count)
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(*coordinatorFixture)
	}{
		{"wrong source commit", func(f *coordinatorFixture) { f.receipts[0].CommitSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }},
		{"review by author", func(f *coordinatorFixture) { f.review.ReviewerActor = f.review.AuthorActor }},
		{"wrong independent PR", func(f *coordinatorFixture) { f.review.PullRequest++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, _ := seed(t, false, test.mutate)
			require.Error(t, completeContinuation(db, f.intent, f.task, nil))
			assertUnchanged(t, f)
		})
	}
	t.Run("tampered JSONB observation rolls back", func(t *testing.T) {
		f, _ := seed(t, false, nil)
		// Raw SQL models a storage corruption attempt; normal ledger writes are append-only.
		require.NoError(t, db.Exec("UPDATE delivery_events SET subject_digest = ? WHERE work_item_id = ? AND event_type = ?", "tampered", f.item.ID, deliveryledger.EventTypeQAObserved).Error)
		require.Error(t, completeContinuation(db, f.intent, f.task, nil))
		assertUnchanged(t, f)
	})
	for _, phase := range []string{"summary", "implementation"} {
		t.Run("rollback scheduling "+phase, func(t *testing.T) {
			f, _ := seed(t, phase == "implementation", nil)
			require.NoError(t, db.Exec(fmt.Sprintf("ALTER TABLE delivery_continuations ADD CONSTRAINT synthetic_no_next CHECK (work_item_id <> '%s' OR phase <> '%s')", f.item.ID, phase)).Error)
			t.Cleanup(func() {
				require.NoError(t, db.Exec("ALTER TABLE delivery_continuations DROP CONSTRAINT synthetic_no_next").Error)
			})
			require.Error(t, completeContinuation(db, f.intent, f.task, nil))
			assertUnchanged(t, f)
			var count int64
			require.NoError(t, db.Model(&models.DeliveryPlan{}).Where("work_item_id = ?", f.item.ID).Count(&count).Error)
			require.EqualValues(t, 1, count)
		})
	}
	t.Run("later negative review with equal timestamp rejects old approval", func(t *testing.T) {
		f, _ := seed(t, false, nil)
		negative := f.review
		negative.ID, negative.AutomationTaskID = uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
		negative.SubjectSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(negative.ID.String())))
		negative.ReviewID++
		negative.CheckRunID = nil
		negative.ReviewGatePassed = false
		require.NoError(t, db.Create(&negative).Error)
		require.ErrorContains(t, completeContinuation(db, f.intent, f.task, nil), "independent exact-SHA review")
		assertUnchanged(t, f)
	})
	t.Run("active plan execution prevents overlapping correction", func(t *testing.T) {
		f, plan := seed(t, true, nil)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			_, _, err := deliveryplansteps.CreateExecutionInTransaction(tx, f.task.ID, plan.ID, 1, "synthetic-active", f.now)
			return err
		}))
		require.ErrorContains(t, completeContinuation(db, f.intent, f.task, nil), "active approved-plan execution")
		assertUnchanged(t, f)
	})
	t.Run("exhausted correction budget preserves original plan", func(t *testing.T) {
		f, _ := seed(t, true, nil)
		for range maximumDelegatedCorrections {
			gate := models.DeliveryGate{WorkItemID: f.item.ID, Kind: deliveryworkflow.GateCodeReview, Decision: deliveryworkflow.DecisionChangesRequested, Authority: "delegated", DecidedBy: delegatedCoordinatorActor, DecidedAt: f.now.Add(-time.Minute)}
			require.NoError(t, db.Create(&gate).Error)
		}
		require.ErrorContains(t, completeContinuation(db, f.intent, f.task, nil), "budget exhausted")
		assertUnchanged(t, f)
		var count int64
		require.NoError(t, db.Model(&models.DeliveryPlan{}).Where("work_item_id = ?", f.item.ID).Count(&count).Error)
		require.EqualValues(t, 1, count)
	})
}
