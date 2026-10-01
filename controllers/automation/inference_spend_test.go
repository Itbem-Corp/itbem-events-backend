package automation

import (
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaskSpendRequiresRoot(t *testing.T) {
	configureAIActionPolicyTestRoot(t, 2)
	r := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/spend", nil), r)
	c.Set("cognito_sub", "synthetic-operator")
	if err := GetTaskInferenceSpend(c); err != nil {
		t.Fatal(err)
	}
	if r.Code != 403 {
		t.Fatal("non-root accounting access")
	}
}

func TestTaskSpendKeepsUnknownTotalsAndAllCalls(t *testing.T) {
	configureAIActionPolicyTestRoot(t, 1)
	_, mock, cleanup := attemptPolicyClaimDB(t)
	defer cleanup()
	id := uuid.Must(uuid.NewV4())
	mock.ExpectQuery(`SELECT provider, model, currency, pricing_basis,`).WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"provider", "model", "currency", "pricing_basis", "calls", "observed_calls", "unknown_cost_calls", "verified_input_tokens", "verified_output_tokens", "verified_cost_micros", "complete_cost_micros"}).
			AddRow("synthetic", "fixture", "USD", "api_equivalent", 6, 4, 2, 400, 160, 400, nil))
	r := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/spend", nil), r)
	c.Set("cognito_sub", "synthetic-operator")
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	if err := GetTaskInferenceSpend(c); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"calls":6`, `"complete_cost_microusd":null`, `"verified_cost_microusd":400`, `"scope":"all_task_runs"`} {
		if !strings.Contains(r.Body.String(), want) {
			t.Fatalf("missing %s: %s", want, r.Body.String())
		}
	}
	if r.Code != 200 || r.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("unsafe accounting response")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
