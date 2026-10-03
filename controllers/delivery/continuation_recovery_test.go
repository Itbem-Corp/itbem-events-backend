package delivery

import (
	"net/http"
	"testing"
	"time"

	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestContinuationInfrastructureWaitDoesNotConsumeCorrectionAttempts(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		if !transientContinuationAdmission(status) {
			t.Errorf("temporary HTTP %d would exhaust the correction budget", status)
		}
	}
	for _, status := range []int{0, 200, 400, 401, 403, 404, 409, 422, 600} {
		if transientContinuationAdmission(status) {
			t.Errorf("HTTP %d must not receive infrastructure retries", status)
		}
	}
}

func TestCompletedQAContinuationReconcilesSignedCallbackSubmission(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	itemID, intentID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .*delivery_work_items.*FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"id", "automation_epoch", "state"}).AddRow(itemID, 3, deliveryworkflow.StateQAReview))
	mock.ExpectQuery(`SELECT .*delivery_continuations`).WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).AddRow(intentID, "claimed"))
	mock.ExpectQuery(`SELECT .*automation_tasks`).WillReturnRows(sqlmock.NewRows([]string{"id", "output_ref", "completed_at"}).AddRow(taskID, "s3://private/result.json", time.Now().UTC()))
	mock.ExpectQuery(`SELECT .*delivery_evidences`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.Must(uuid.NewV4())))
	mock.ExpectQuery(`INSERT INTO "delivery_messages"`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.Must(uuid.NewV4())))
	mock.ExpectExec(`UPDATE "delivery_work_items"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "delivery_continuations"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	intent := models.DeliveryContinuation{ID: intentID, WorkItemID: itemID, Epoch: 3, Phase: "qa"}
	if err := completeContinuation(db, intent, models.AutomationTask{ID: taskID}, map[string]any{"structured_result": map[string]any{"verdict": "passed"}}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestContinuationReconcilesOnlyCallbackSubmittedStates(t *testing.T) {
	states := []string{deliveryworkflow.StatePlanning, deliveryworkflow.StatePlanReview, deliveryworkflow.StateImplementation, deliveryworkflow.StateCodeReview, deliveryworkflow.StatePreviewPending, deliveryworkflow.StateQARunning, deliveryworkflow.StateQAReview, deliveryworkflow.StateReleaseReview, deliveryworkflow.StateReleased, deliveryworkflow.StateCancelled}
	for _, action := range []deliveryworkflow.Action{deliveryworkflow.ActionSubmitPlan, deliveryworkflow.ActionSubmitCodeReview, deliveryworkflow.ActionSubmitQA, deliveryworkflow.ActionApprovePlan, deliveryworkflow.ActionApproveRelease} {
		for _, state := range states {
			want := action == deliveryworkflow.ActionSubmitCodeReview && state == deliveryworkflow.StateCodeReview || action == deliveryworkflow.ActionSubmitQA && state == deliveryworkflow.StateQAReview
			if continuationSubmissionAlreadyApplied(state, action) != want {
				t.Errorf("state %s action %s unexpectedly bypassed a transition", state, action)
			}
		}
	}
}
