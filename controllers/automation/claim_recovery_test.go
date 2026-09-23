package automation

import (
	"events-stocks/configuration"
	"net/http"
	"net/http/httptest"
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
			mock.ExpectBegin()
			mock.ExpectExec(`UPDATE "automation_tasks"`).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(`SELECT .* FROM "automation_tasks"`).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "run_id", "lease_expires_at"}).AddRow(taskID, scenario.status, scenario.owner, scenario.lease))
			if scenario.renew {
				mock.ExpectExec(`UPDATE "automation_tasks"`).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectCommit()
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/claim", nil), recorder)
			if err := claimAutomationTaskRun(ctx, taskID, "new-run"); err != nil {
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
				t.Fatalf("status=%d, want %d: %s", recorder.Code, expected, recorder.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func futureLease() *time.Time { value := time.Now().UTC().Add(time.Hour); return &value }
func pastLease() *time.Time   { value := time.Now().UTC().Add(-time.Hour); return &value }
