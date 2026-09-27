package automation

import (
	"events-stocks/configuration"
	"events-stocks/models"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestClaimAutomationTaskDistinguishesBusyFromTerminal(t *testing.T) {
	t.Setenv(attemptPolicySigningKeyEnv, strings.Repeat("b", 48))
	t.Setenv(attemptPolicyPreviousSigningKeyEnv, "")
	for _, scenario := range []struct {
		name, status, owner string
		lease               *time.Time
		busy, renew         bool
	}{
		{name: "active foreign owner", status: "running", owner: "owner", lease: futureLease(), busy: true},
		{name: "renew same owner", status: "running", owner: "new-run", lease: futureLease(), renew: true},
		{name: "expired owner", status: "running", owner: "owner", lease: pastLease(), renew: true},
		{name: "missing lease", status: "running", owner: "owner", renew: true},
		{name: "completed", status: "completed", owner: "owner", lease: futureLease()},
		{name: "failed", status: "failed", owner: "owner", lease: futureLease()},
		{name: "cancelled", status: "cancelled", owner: "owner", lease: futureLease()},
		{name: "cancellation requested", status: "cancel_requested", owner: "owner", lease: futureLease()},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			connection, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: connection}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			original := configuration.DB
			configuration.DB = db
			t.Cleanup(func() { configuration.DB = original })
			taskID := uuid.Must(uuid.NewV4())
			identity := testClaimIdentity()
			expectClaimWorkerIdentity(mock, taskID, "delivery.plan")
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_assignments" WHERE child_automation_task_id = \$1 LIMIT \$2`).
				WithArgs(taskID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "execution_id", "delivery_plan_step_id", "child_automation_task_id", "target_machine_id", "target_agent_key", "status"}))
			mock.ExpectExec(`UPDATE "automation_tasks"`).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(`SELECT .* FROM "automation_tasks"`).WillReturnRows(sqlmock.NewRows([]string{"id", "operation", "status", "run_id", "lease_expires_at", "max_completion_tokens", "delivery_work_item_id", "worker_id", "agent_key", "machine_id"}).AddRow(taskID.String(), "delivery.plan", scenario.status, scenario.owner, scenario.lease, 0, nil, identity.WorkerID, identity.AgentKey, identity.MachineID))
			if scenario.renew {
				if scenario.owner == "new-run" {
					snapshot := models.AutomationInferenceAttemptPolicy{
						ID: uuid.Must(uuid.NewV4()), AutomationTaskID: taskID, RunID: "new-run", Operation: "delivery.plan",
						RoutesJSON: "[]", RoutesHash: sha256Hex([]byte("[]")), MaxCompletionTokens: 0, MaxInferenceCalls: inferenceAttemptCallQuota("delivery.plan"), CreatedAt: time.Now().UTC(),
					}
					snapshot.SnapshotHash = inferenceAttemptSnapshotHash(snapshot)
					if err := signAutomationInferenceAttemptPolicy(&snapshot); err != nil {
						t.Fatal(err)
					}
					expectAttemptPolicyRow(mock, snapshot)
					mock.ExpectExec(`UPDATE "automation_tasks"`).WillReturnResult(sqlmock.NewResult(0, 1))
				} else {
					mock.ExpectExec(`UPDATE "automation_tasks"`).WillReturnResult(sqlmock.NewResult(0, 1))
					expectClaimedTask(mock, taskID, "delivery.plan", "new-run", 0, nil)
					expectUnconfiguredAttemptPolicyCreation(mock, taskID, "new-run", "delivery.plan", 0)
				}
				mock.ExpectQuery(`SELECT .*parent_task_id.*delivery_plan_step_assignments.*`).
					WillReturnRows(sqlmock.NewRows([]string{"parent_task_id"}))
			}
			mock.ExpectCommit()
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/claim", nil), recorder)
			ctx.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{InstanceID: uuid.Must(uuid.NewV4()), AgentKey: identity.AgentKey, MachineID: identity.MachineID})
			if err := claimAutomationTaskRun(ctx, taskID, "new-run", identity); err != nil {
				t.Fatal(err)
			}
			if got := recorder.Header().Get("X-ITBEM-Automation-Run-Busy"); (got == "1") != scenario.busy {
				t.Fatalf("busy header=%q; expected busy=%v", got, scenario.busy)
			}
			expected := http.StatusConflict
			if scenario.renew {
				expected = http.StatusNoContent
			}
			if recorder.Code != expected {
				t.Fatalf("status=%d, want %d: %s; unmet SQL expectations: %v", recorder.Code, expected, recorder.Body.String(), mock.ExpectationsWereMet())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func futureLease() *time.Time { value := time.Now().UTC().Add(time.Hour); return &value }
func pastLease() *time.Time   { value := time.Now().UTC().Add(-time.Hour); return &value }
