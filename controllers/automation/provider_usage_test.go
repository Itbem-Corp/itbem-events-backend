package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

type providerUsageRoundTripper func(*http.Request) (*http.Response, error)

func (f providerUsageRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type providerUsageResolverSpy struct {
	globalReads  int
	hasReads     []string
	projectReads []string
	keys         map[string]string
	configured   map[string]bool
	secretError  error
}

func (r *providerUsageResolverSpy) APIKey(context.Context, string) (string, error) {
	r.globalReads++
	return "global-inference-key-must-not-be-used", nil
}

func (*providerUsageResolverSpy) ReplaceAPIKey(context.Context, string, string) error { return nil }

func (r *providerUsageResolverSpy) APIKeyForProject(_ context.Context, projectID, provider string) (string, error) {
	r.projectReads = append(r.projectReads, projectID+":"+provider)
	if r.secretError != nil {
		return "", r.secretError
	}
	return r.keys[projectID+":"+provider], nil
}

func (r *providerUsageResolverSpy) HasAPIKeyForProject(_ context.Context, projectID, provider string) (bool, error) {
	r.hasReads = append(r.hasReads, projectID+":"+provider)
	if r.secretError != nil {
		return false, r.secretError
	}
	return r.configured[projectID+":"+provider], nil
}

func (*providerUsageResolverSpy) ReplaceProjectAPIKey(context.Context, string, string, string) error {
	return nil
}

func (*providerUsageResolverSpy) DeleteProjectAPIKey(context.Context, string, string) error {
	return nil
}

var _ projectCredentialResolver = (*providerUsageResolverSpy)(nil)

func TestFetchProjectProviderUsageUsesExplicitProjectCredentialsAndSafeProviderProjection(t *testing.T) {
	projectID := "a1b2c3d4-e5f6-4789-8123-456789abcdef"
	otherProjectID := "b1b2c3d4-e5f6-4789-8123-456789abcdef"
	deepSeekKey, miniMaxKey := "deepseek-project-key-secret", "minimax-project-key-secret"
	resolver := &providerUsageResolverSpy{
		keys:       map[string]string{projectID + ":deepseek": deepSeekKey, projectID + ":minimax": miniMaxKey},
		configured: map[string]bool{projectID + ":deepseek": true, projectID + ":minimax": true, projectID + ":openrouter": true},
	}
	resetAt := time.Date(2026, 9, 24, 13, 0, 0, 0, time.UTC)
	var seen []string
	client := &http.Client{Transport: providerUsageRoundTripper(func(request *http.Request) (*http.Response, error) {
		seen = append(seen, request.URL.Host+request.URL.Path)
		switch {
		case request.URL.Path == "/user/balance":
			if request.Header.Get("Authorization") != "Bearer "+deepSeekKey {
				t.Errorf("DeepSeek request did not use the selected project's key")
			}
			return providerUsageHTTPResponse(http.StatusOK, `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00","topped_up_balance":"100.00"},{"currency":"USD","total_balance":"12.30","granted_balance":"2.30","topped_up_balance":"10.00"}]}`), nil
		case request.URL.Path == "/v1/token_plan/remains":
			if request.Header.Get("Authorization") != "Bearer "+miniMaxKey {
				t.Errorf("MiniMax request did not use the selected project's key")
			}
			body := fmt.Sprintf(`{"base_resp":{"status_code":0,"status_msg":"private-provider-text-must-not-escape"},"model_remains":[{"model_name":"general","current_interval_total_count":100,"current_interval_usage_count":61,"current_interval_remaining_percent":67,"end_time":%d,"remains_time":1,"current_weekly_total_count":500,"current_weekly_usage_count":321,"current_weekly_remaining_percent":88,"weekly_end_time":%d,"weekly_remains_time":1}]}`, resetAt.UnixMilli(), resetAt.Add(24*time.Hour).UnixMilli())
			return providerUsageHTTPResponse(http.StatusOK, body), nil
		default:
			t.Errorf("unexpected provider endpoint %s", request.URL.String())
			return providerUsageHTTPResponse(http.StatusNotFound, `{}`), nil
		}
	})}

	observedAt := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	accounts := fetchProjectProviderUsage(context.Background(), resolver, client, projectID, observedAt)
	if len(accounts) != 4 { // DeepSeek returns separate CNY/USD balances; MiniMax and OpenRouter each return one account.
		t.Fatalf("provider account count=%d, want 4: %#v", len(accounts), accounts)
	}
	if accounts[0].Currency != "CNY" || accounts[0].Balance == nil || accounts[0].Balance.Total != "110.00" || accounts[1].Currency != "USD" || accounts[1].Balance.Total != "12.30" {
		t.Fatalf("DeepSeek native currency balances were not preserved: %#v", accounts[:2])
	}
	miniMax := accounts[2]
	if miniMax.Provider != "minimax" || miniMax.Status != providerUsageStatusAvailable || miniMax.BillingModel != "subscription_quota" || len(miniMax.Windows) != 2 {
		t.Fatalf("MiniMax Token Plan observation is incomplete: %#v", miniMax)
	}
	window := miniMax.Windows[0]
	if window.Unit != "percent" || window.Remaining == nil || window.Remaining.String() != "67" || window.Used != nil || window.Limit != nil || window.ResetAt == nil || !window.ResetAt.Equal(resetAt) {
		t.Fatalf("MiniMax window must preserve provider-reported values without deriving quantities/reset: %#v", window)
	}
	if miniMax.Windows[1].ResetAt == nil || !miniMax.Windows[1].ResetAt.Equal(resetAt.Add(24*time.Hour)) {
		t.Fatalf("weekly MiniMax absolute reset timestamp was not preserved: %#v", miniMax.Windows[1])
	}
	openRouter := accounts[3]
	if openRouter.Status != providerUsageStatusNotSupported || openRouter.CredentialScope != "separate_management_credential_required" || openRouter.ErrorCode != providerUsageErrorManagementKey {
		t.Fatalf("OpenRouter must be explicitly unsupported without separate management auth: %#v", openRouter)
	}
	if len(seen) != 2 || resolver.globalReads != 0 || len(resolver.hasReads) != 2 || len(resolver.projectReads) != 2 {
		t.Fatalf("provider usage attempted an unsupported/global credential path: requests=%v resolver=%#v", seen, resolver)
	}
	if strings.Contains(strings.Join(seen, " "), otherProjectID) {
		t.Fatal("provider API request escaped the explicit project scope")
	}
	encoded, err := json.Marshal(accounts)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{deepSeekKey, miniMaxKey, "private-provider-text-must-not-escape", "remains_time", "current_interval_usage_count"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("safe DTO leaked a provider key/raw field %q: %s", private, encoded)
		}
	}
	for _, call := range resolver.projectReads {
		if !strings.HasPrefix(call, projectID+":") || strings.HasPrefix(call, otherProjectID+":") {
			t.Fatalf("credential lookup escaped explicit project scope: %v", resolver.projectReads)
		}
	}
}

func TestProviderUsageDoesNotInferMiniMaxAmountsOrCountdownReset(t *testing.T) {
	window, include, ok := miniMaxQuotaWindow("general_interval", json.Number("1500"), "", "")
	if !ok || !include || window.Limit == nil || window.Limit.String() != "1500" || window.Used != nil || window.Remaining != nil || window.ResetAt != nil {
		t.Fatalf("raw provider limit was not retained without inference: %#v", window)
	}
	window, include, ok = miniMaxQuotaWindow("general_interval", "", "", json.Number("123456789"))
	if !ok || include || window.ResetAt != nil {
		t.Fatalf("a countdown without an absolute provider timestamp was inferred: %#v / %v / %v", window, include, ok)
	}
}

func TestProviderUsageErrorsAreGenericAndNeverPersistResponseText(t *testing.T) {
	const secret = "provider-key-sensitive-error"
	resolver := &providerUsageResolverSpy{
		keys:       map[string]string{"project-1:deepseek": secret},
		configured: map[string]bool{"project-1:deepseek": true},
	}
	client := &http.Client{Transport: providerUsageRoundTripper(func(*http.Request) (*http.Response, error) {
		return providerUsageHTTPResponse(http.StatusUnauthorized, `{"error":"`+secret+` upstream raw message"}`), nil
	})}
	accounts := fetchOneProjectProviderUsage(context.Background(), resolver, client, "project-1", "deepseek", time.Now().UTC())
	if len(accounts) != 1 || accounts[0].Status != providerUsageStatusError || accounts[0].ErrorCode != providerUsageErrorAuth {
		t.Fatalf("provider auth failure was not mapped to a generic status: %#v", accounts)
	}
	encoded, err := json.Marshal(accounts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "upstream raw message") {
		t.Fatalf("provider usage error leaked key or response body: %s", encoded)
	}
	resolver.secretError = errors.New(secret + " secret-store detail")
	accounts = fetchOneProjectProviderUsage(context.Background(), resolver, client, "project-1", "deepseek", time.Now().UTC())
	encoded, _ = json.Marshal(accounts)
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "secret-store detail") || accounts[0].ErrorCode != providerUsageErrorCredentialStore {
		t.Fatalf("credential resolver detail leaked through provider usage status: %s / %#v", encoded, accounts)
	}
}

func TestProviderUsageMissingProjectKeyNeverFallsBackToGlobalCredential(t *testing.T) {
	const projectID = "a1b2c3d4-e5f6-4789-8123-456789abcdef"
	resolver := &providerUsageResolverSpy{configured: map[string]bool{}, keys: map[string]string{}}
	requests := 0
	client := &http.Client{Transport: providerUsageRoundTripper(func(*http.Request) (*http.Response, error) {
		requests++
		return providerUsageHTTPResponse(http.StatusOK, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"1.00","granted_balance":"0.00","topped_up_balance":"1.00"}]}`), nil
	})}
	account := fetchOneProjectProviderUsage(context.Background(), resolver, client, projectID, "deepseek", time.Now().UTC())[0]
	if account.Status != providerUsageStatusNotConfigured || account.ErrorCode != providerUsageErrorNotConfigured || requests != 0 || resolver.globalReads != 0 || len(resolver.projectReads) != 0 {
		t.Fatalf("absent project key silently fell back to a global key: account=%#v requests=%d resolver=%#v", account, requests, resolver)
	}
}

func TestProviderUsageRoutesAndMigrationRegistration(t *testing.T) {
	if endpoint, ok := providerUsageEndpoint("deepseek"); !ok || endpoint != "https://api.deepseek.com/user/balance" {
		t.Fatalf("unexpected DeepSeek balance endpoint %q / %v", endpoint, ok)
	}
	if endpoint, ok := providerUsageEndpoint("minimax"); !ok || endpoint != "https://www.minimax.io/v1/token_plan/remains" {
		t.Fatalf("unexpected MiniMax Token Plan endpoint %q / %v", endpoint, ok)
	}
	if _, ok := providerUsageEndpoint("openrouter"); ok {
		t.Fatal("OpenRouter must not use its inference credential for management/quota calls")
	}
	found := false
	for _, model := range configuration.GetAllModels() {
		if _, ok := model.(*models.AutomationProviderUsageSnapshot); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("provider usage snapshot model is not included in GORM migration registration")
	}
}

func TestProviderUsageSnapshotModelIsAppendOnly(t *testing.T) {
	snapshot := &models.AutomationProviderUsageSnapshot{}
	if err := snapshot.BeforeUpdate(nil); !errors.Is(err, models.ErrProviderUsageSnapshotAppendOnly) {
		t.Fatalf("snapshot update hook=%v, want append-only rejection", err)
	}
	if err := snapshot.BeforeDelete(nil); !errors.Is(err, models.ErrProviderUsageSnapshotAppendOnly) {
		t.Fatalf("snapshot delete hook=%v, want append-only rejection", err)
	}
}

func TestAppendProviderUsageSnapshotsCreatesNewRowsForEveryCapture(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	projectID := uuid.Must(uuid.NewV4())
	account := providerUsageAccount{Provider: "openrouter", Status: providerUsageStatusNotSupported, BillingModel: "management_api_not_supported", CredentialScope: "separate_management_credential_required", ObservedAt: time.Now().UTC(), ErrorCode: providerUsageErrorManagementKey, Windows: []providerUsageWindow{}}
	for range 2 {
		mock.ExpectBegin()
		mock.ExpectQuery(`INSERT INTO "automation_provider_usage_snapshots"`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.Must(uuid.NewV4())))
		mock.ExpectCommit()
		if err := appendProviderUsageSnapshots(context.Background(), db, uuid.Must(uuid.NewV4()), projectID, account.ObservedAt, []providerUsageAccount{account}); err != nil {
			t.Fatalf("append-only snapshot insert failed: %v", err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetProjectProviderUsageReturnsSuccessfulEmptyEnvelopeAndChecksProjectMembership(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	projectID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "usage-empty-project-member"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject}, nil
	}})
	t.Cleanup(restore)
	expectProjectCredentialProject(mock, projectID, organizationID)
	expectOrganizationCredentialClientIDs(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "role","permissions" FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectID, subject, 1).
		WillReturnRows(sqlmock.NewRows([]string{"role", "permissions"}).AddRow("viewer", `[]`))
	mock.ExpectQuery(`SELECT \* FROM "automation_provider_usage_snapshots" WHERE project_id = \$1 ORDER BY observed_at DESC,created_at DESC,id DESC,"automation_provider_usage_snapshots"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "capture_id", "project_id", "provider", "status", "billing_model", "credential_scope", "observed_at", "currency", "balance_total", "balance_granted", "balance_topped_up", "balance_is_available", "windows_json", "error_code", "created_at"}))
	ctx, recorder := projectCredentialTestContext(http.MethodGet, projectID, organizationID, "organization", subject, "")
	if err := GetProjectProviderUsage(ctx); err != nil {
		t.Fatalf("empty project usage read returned an error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("empty project usage status=%d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data providerUsageResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope.Data.ProjectID != projectID.String() || envelope.Data.ObservedAt != nil || envelope.Data.Accounts == nil || len(envelope.Data.Accounts) != 0 {
		t.Fatalf("empty GET envelope is not UI-compatible: %#v / %v / %s", envelope.Data, err, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetProjectProviderUsageReturnsOnlyLatestAppendOnlyCapture(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	projectID, organizationID, captureID, rowID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "usage-latest-project-member"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject}, nil
	}})
	t.Cleanup(restore)
	expectProjectCredentialProject(mock, projectID, organizationID)
	expectOrganizationCredentialClientIDs(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "role","permissions" FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectID, subject, 1).
		WillReturnRows(sqlmock.NewRows([]string{"role", "permissions"}).AddRow("viewer", `[]`))
	observedAt := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	columns := []string{"id", "capture_id", "project_id", "provider", "status", "billing_model", "credential_scope", "observed_at", "currency", "balance_total", "balance_granted", "balance_topped_up", "balance_is_available", "windows_json", "error_code", "created_at"}
	mock.ExpectQuery(`SELECT \* FROM "automation_provider_usage_snapshots" WHERE project_id = \$1 ORDER BY observed_at DESC,created_at DESC,id DESC,"automation_provider_usage_snapshots"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(rowID, captureID, projectID, "deepseek", "available", "token", "project", observedAt, "USD", "12.30", "2.30", "10.00", true, "[]", "", observedAt))
	mock.ExpectQuery(`SELECT \* FROM "automation_provider_usage_snapshots" WHERE project_id = \$1 AND capture_id = \$2 ORDER BY provider ASC, currency ASC, id ASC`).
		WithArgs(projectID, captureID).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(rowID, captureID, projectID, "deepseek", "available", "token", "project", observedAt, "USD", "12.30", "2.30", "10.00", true, "[]", "", observedAt))
	ctx, recorder := projectCredentialTestContext(http.MethodGet, projectID, organizationID, "organization", subject, "")
	if err := GetProjectProviderUsage(ctx); err != nil {
		t.Fatalf("latest project usage read returned an error: %v", err)
	}
	var envelope struct {
		Data providerUsageResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || recorder.Code != http.StatusOK || envelope.Data.ObservedAt == nil || !envelope.Data.ObservedAt.Equal(observedAt) || len(envelope.Data.Accounts) != 1 {
		t.Fatalf("latest snapshot response=%s err=%v", recorder.Body.String(), err)
	}
	if account := envelope.Data.Accounts[0]; account.Provider != "deepseek" || account.Currency != "USD" || account.Balance == nil || account.Balance.Total != "12.30" {
		t.Fatalf("latest provider snapshot was not projected safely: %#v", account)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProviderUsageRefreshRequiresProjectManagerBeforeCredentialReads(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB, previousResolver := configuration.DB, inferenceCredentials
	configuration.DB = db
	resolver := &providerUsageResolverSpy{configured: map[string]bool{}, keys: map[string]string{}}
	inferenceCredentials = resolver
	t.Cleanup(func() { configuration.DB, inferenceCredentials = previousDB, previousResolver })
	projectID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "usage-viewer-cannot-refresh"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) { return &models.User{CognitoSub: subject}, nil }})
	t.Cleanup(restore)
	expectProjectCredentialProject(mock, projectID, organizationID)
	expectOrganizationCredentialClientIDs(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "role","permissions" FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectID, subject, 1).
		WillReturnRows(sqlmock.NewRows([]string{"role", "permissions"}).AddRow("viewer", `[]`))
	ctx, recorder := projectCredentialTestContext(http.MethodPost, projectID, organizationID, "organization", subject, "")
	if err := RefreshProjectProviderUsage(ctx); err != nil {
		t.Fatalf("refresh denial returned unexpected handler error: %v", err)
	}
	if recorder.Code != http.StatusForbidden || len(resolver.hasReads) != 0 || resolver.globalReads != 0 {
		t.Fatalf("viewer refresh status=%d resolver_reads=%#v global=%d, want 403/no credential access", recorder.Code, resolver.hasReads, resolver.globalReads)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshProjectProviderUsageReturnsAndAppendsProjectScopedCapture(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB, previousResolver, previousHTTPClient := configuration.DB, inferenceCredentials, providerUsageHTTPClient
	configuration.DB = db
	projectID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	deepSeekKey, miniMaxKey := "scoped-deepseek-refresh-key", "scoped-minimax-refresh-key"
	resolver := &providerUsageResolverSpy{
		keys:       map[string]string{projectID.String() + ":deepseek": deepSeekKey, projectID.String() + ":minimax": miniMaxKey},
		configured: map[string]bool{projectID.String() + ":deepseek": true, projectID.String() + ":minimax": true},
	}
	inferenceCredentials = resolver
	providerUsageHTTPClient = &http.Client{Transport: providerUsageRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/user/balance" && request.Header.Get("Authorization") == "Bearer "+deepSeekKey {
			return providerUsageHTTPResponse(http.StatusOK, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"9.50","granted_balance":"1.00","topped_up_balance":"8.50"}]}`), nil
		}
		if request.URL.Path == "/v1/token_plan/remains" && request.Header.Get("Authorization") == "Bearer "+miniMaxKey {
			return providerUsageHTTPResponse(http.StatusOK, `{"base_resp":{"status_code":0,"status_msg":"not-for-client"},"model_remains":[{"model_name":"general","current_interval_remaining_percent":74,"weekly_end_time":0}]}`), nil
		}
		return providerUsageHTTPResponse(http.StatusUnauthorized, `{"error":"raw-provider-error"}`), nil
	})}
	t.Cleanup(func() {
		configuration.DB, inferenceCredentials, providerUsageHTTPClient = previousDB, previousResolver, previousHTTPClient
	})
	subject := "usage-refresh-manager"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject}, nil
	}})
	t.Cleanup(restore)
	expectProjectCredentialProject(mock, projectID, organizationID)
	expectOrganizationCredentialClientIDs(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "role","permissions" FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectID, subject, 1).
		WillReturnRows(sqlmock.NewRows([]string{"role", "permissions"}).AddRow("delivery_manager", `[]`))
	mock.ExpectBegin()
	ids := sqlmock.NewRows([]string{"id"})
	for range 3 { // one USD DeepSeek row, one MiniMax row, one unsupported OpenRouter row
		ids.AddRow(uuid.Must(uuid.NewV4()))
	}
	mock.ExpectQuery(`INSERT INTO "automation_provider_usage_snapshots"`).WillReturnRows(ids)
	mock.ExpectCommit()
	ctx, recorder := projectCredentialTestContext(http.MethodPost, projectID, organizationID, "organization", subject, "")
	if err := RefreshProjectProviderUsage(ctx); err != nil {
		t.Fatalf("authorized usage refresh returned an error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("authorized usage refresh status=%d: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data providerUsageResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope.Data.ProjectID != projectID.String() || envelope.Data.ObservedAt == nil || len(envelope.Data.Accounts) != 3 {
		t.Fatalf("refresh did not return a full capture DTO: %s / %v", recorder.Body.String(), err)
	}
	if resolver.globalReads != 0 || len(resolver.projectReads) != 2 {
		t.Fatalf("refresh escaped project credential namespace: %#v", resolver)
	}
	for _, private := range []string{deepSeekKey, miniMaxKey, "not-for-client", "raw-provider-error"} {
		if strings.Contains(recorder.Body.String(), private) {
			t.Fatalf("refresh response leaked provider secret/text %q: %s", private, recorder.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetProjectProviderUsageRejectsCrossOrganizationBeforeReadingHistory(t *testing.T) {
	for _, test := range []struct {
		name    string
		method  string
		handler echo.HandlerFunc
	}{
		{name: "read", method: http.MethodGet, handler: GetProjectProviderUsage},
		{name: "refresh", method: http.MethodPost, handler: RefreshProjectProviderUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock := automationCostLedgerTestDB(t)
			previousDB, previousResolver := configuration.DB, inferenceCredentials
			configuration.DB = db
			resolver := &providerUsageResolverSpy{configured: map[string]bool{}, keys: map[string]string{}}
			inferenceCredentials = resolver
			t.Cleanup(func() { configuration.DB, inferenceCredentials = previousDB, previousResolver })
			projectID := uuid.Must(uuid.NewV4())
			projectOrganizationID, selectedOrganizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			subject := "usage-cross-organization"
			restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
				return &models.User{CognitoSub: subject, IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
			}})
			t.Cleanup(restore)
			expectProjectCredentialProject(mock, projectID, projectOrganizationID)
			expectOrganizationCredentialClientIDs(mock, selectedOrganizationID, selectedOrganizationID)
			ctx, recorder := projectCredentialTestContext(test.method, projectID, selectedOrganizationID, "organization", subject, "")
			if err := test.handler(ctx); err != nil {
				t.Fatalf("cross-organization %s returned unexpected handler error: %v", test.name, err)
			}
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("cross-organization %s status=%d, want generic 404: %s", test.name, recorder.Code, recorder.Body.String())
			}
			for _, private := range []string{projectID.String(), projectOrganizationID.String(), selectedOrganizationID.String()} {
				if strings.Contains(recorder.Body.String(), private) {
					t.Fatalf("cross-organization response disclosed %q: %s", private, recorder.Body.String())
				}
			}
			if resolver.globalReads != 0 || len(resolver.hasReads) != 0 || len(resolver.projectReads) != 0 {
				t.Fatalf("cross-organization %s reached credentials: %#v", test.name, resolver)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func providerUsageHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}
