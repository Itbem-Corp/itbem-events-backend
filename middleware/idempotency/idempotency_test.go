package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCriticalMutationsBypassesUnprotectedRoutes(t *testing.T) {
	e := echo.New()
	request := httptest.NewRequest(http.MethodPost, "/api/resources", strings.NewReader(`{"name":"x"}`))
	recorder := httptest.NewRecorder()
	context := e.NewContext(request, recorder)
	context.SetPath("/api/resources")

	called := false
	handler := CriticalMutations(func(c echo.Context) error {
		called = true
		return c.NoContent(http.StatusAccepted)
	})

	require.NoError(t, handler(context))
	assert.True(t, called)
	assert.Equal(t, http.StatusAccepted, recorder.Code)
}

func TestCriticalMutationsAllowsLegacyRequestWithoutKey(t *testing.T) {
	e := echo.New()
	request := httptest.NewRequest(http.MethodPost, "/api/events", strings.NewReader(`{"name":"x"}`))
	recorder := httptest.NewRecorder()
	context := e.NewContext(request, recorder)
	context.SetPath("/api/events")

	called := false
	handler := CriticalMutations(func(c echo.Context) error {
		called = true
		return c.NoContent(http.StatusCreated)
	})

	require.NoError(t, handler(context))
	assert.True(t, called)
	assert.Equal(t, "bypassed", recorder.Header().Get("Idempotency-Status"))
}

func TestCriticalMutationsRejectsOversizedKeyBeforeExecutingHandler(t *testing.T) {
	e := echo.New()
	request := httptest.NewRequest(http.MethodPost, "/api/users/invite", strings.NewReader(`{"email":"person@example.com"}`))
	request.Header.Set(headerKey, strings.Repeat("a", maxKeyLength+1))
	recorder := httptest.NewRecorder()
	context := e.NewContext(request, recorder)
	context.SetPath("/api/users/invite")

	called := false
	handler := CriticalMutations(func(c echo.Context) error {
		called = true
		return c.NoContent(http.StatusCreated)
	})

	require.NoError(t, handler(context))
	assert.False(t, called)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestCriticalMutationsReplaysCompletedResponseWithoutExecutingAgain(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
	})
	require.NoError(t, err)
	originalDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = originalDB })

	body := `{"email":"person@example.com"}`
	hash := sha256.Sum256([]byte("\n" + body))
	requestHash := hex.EncodeToString(hash[:])
	recordID := uuid.Must(uuid.NewV4())
	now := time.Now().UTC()

	mock.ExpectExec(`DELETE FROM "idempotency_records"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`INSERT INTO "idempotency_records"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(recordID))
	mock.ExpectExec(`UPDATE "idempotency_records" SET`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	executions := 0
	handler := CriticalMutations(func(c echo.Context) error {
		executions++
		return c.JSON(http.StatusCreated, map[string]string{"id": "user-1"})
	})
	first := executeProtectedRequest(t, handler, body, "invite-user-1")
	assert.Equal(t, http.StatusCreated, first.Code)
	assert.Equal(t, 1, executions)

	mock.ExpectExec(`DELETE FROM "idempotency_records"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`INSERT INTO "idempotency_records"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT \* FROM "idempotency_records"`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_code", "actor_sub", "method", "route", "key",
			"request_hash", "state", "status_code", "content_type", "response_body",
			"created_at", "updated_at", "expires_at",
		}).AddRow(
			recordID, "itbem", "actor-1", http.MethodPost, "/api/users/invite", "invite-user-1",
			requestHash, "completed", http.StatusCreated, "application/json; charset=UTF-8",
			[]byte("{\"id\":\"user-1\"}\n"), now, now, now.Add(recordTTL),
		))

	second := executeProtectedRequest(t, handler, body, "invite-user-1")
	assert.Equal(t, http.StatusCreated, second.Code)
	assert.Equal(t, "true", second.Header().Get(headerReplayed))
	assert.JSONEq(t, `{"id":"user-1"}`, second.Body.String())
	assert.Equal(t, 1, executions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func executeProtectedRequest(t *testing.T, handler echo.HandlerFunc, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	request := httptest.NewRequest(http.MethodPost, "/api/users/invite", strings.NewReader(body))
	request.Header.Set(headerKey, key)
	recorder := httptest.NewRecorder()
	context := e.NewContext(request, recorder)
	context.SetPath("/api/users/invite")
	context.Set("tenant_code", "itbem")
	context.Set("cognito_sub", "actor-1")
	require.NoError(t, handler(context))
	return recorder
}
