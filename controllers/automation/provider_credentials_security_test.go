package automation

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

type providerCredentialResolverSpy struct {
	provider string
	apiKey   string
	err      error
}

func (*providerCredentialResolverSpy) APIKey(context.Context, string) (string, error) {
	return "stored-key-must-not-be-returned", nil
}

func (r *providerCredentialResolverSpy) ReplaceAPIKey(_ context.Context, provider, apiKey string) error {
	r.provider, r.apiKey = provider, apiKey
	return r.err
}

func TestBoundedProviderCredentialDecoderRejectsEmptyAndOversizedKeys(t *testing.T) {
	for _, body := range []string{
		`{"api_key":"   "}`,
		`{"api_key":"` + strings.Repeat("x", maxProviderCredentialBytes+1) + `"}`,
		`{"api_key":"valid","extra":"unknown"}`,
		`{"API_KEY":"noncanonical-field"}`,
		`{"api_key":"first-canary","api_key":"second-canary"}`,
		`{"api_key":"first-canary","API_KEY":"second-canary"}`,
		`{"api_key":"first-canary","api\u005fkey":"second-canary"}`,
		`{"api_key":"valid"} {}`,
	} {
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body)), httptest.NewRecorder())
		if _, err := decodeBoundedProviderCredentialRequest(ctx); err == nil {
			t.Fatalf("invalid provider credential body was accepted: %s", body[:min(len(body), 60)])
		}
	}

	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"api_key":"  canary-key  "}`)), httptest.NewRecorder())
	request, err := decodeBoundedProviderCredentialRequest(ctx)
	if err != nil || request.APIKey != "canary-key" {
		t.Fatalf("valid provider credential did not normalize safely: key=%q err=%v", request.APIKey, err)
	}
}

func TestUpsertProviderCredentialAllowsOnlyKnownProvidersAndNeverEchoesSecrets(t *testing.T) {
	t.Run("unknown provider is rejected before storage", func(t *testing.T) {
		resolver := &providerCredentialResolverSpy{}
		ctx, recorder, restore := providerCredentialMutationContext(t, "not-a-provider", `{"api_key":"unknown-provider-canary"}`, resolver)
		defer restore()
		if err := UpsertProviderCredential(ctx); err != nil {
			t.Fatalf("handler returned unexpected error: %v", err)
		}
		if recorder.Code != http.StatusBadRequest || resolver.provider != "" || resolver.apiKey != "" {
			t.Fatalf("unknown provider reached storage: status=%d resolver=%#v body=%s", recorder.Code, resolver, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "unknown-provider-canary") {
			t.Fatal("invalid provider response echoed the credential")
		}
	})

	t.Run("duplicate credential fields are rejected before storage", func(t *testing.T) {
		resolver := &providerCredentialResolverSpy{}
		body := `{"api_key":"first-canary","API_KEY":"duplicate-field-canary"}`
		ctx, recorder, restore := providerCredentialMutationContext(t, "openrouter", body, resolver)
		defer restore()
		if err := UpsertProviderCredential(ctx); err != nil {
			t.Fatalf("handler returned unexpected error: %v", err)
		}
		if recorder.Code != http.StatusBadRequest || resolver.provider != "" || resolver.apiKey != "" {
			t.Fatalf("ambiguous credential request reached storage: status=%d resolver=%#v body=%s", recorder.Code, resolver, recorder.Body.String())
		}
		for _, secret := range []string{"first-canary", "duplicate-field-canary"} {
			if strings.Contains(recorder.Body.String(), secret) {
				t.Fatalf("invalid credential response echoed %q: %s", secret, recorder.Body.String())
			}
		}
	})

	t.Run("successful storage returns status only", func(t *testing.T) {
		const canary = "provider-secret-canary-91"
		resolver := &providerCredentialResolverSpy{}
		ctx, recorder, restore := providerCredentialMutationContext(t, " OpenRouter ", `{"api_key":"`+canary+`"}`, resolver)
		defer restore()
		db, mock := automationCostLedgerTestDB(t)
		previousDB := configuration.DB
		configuration.DB = db
		t.Cleanup(func() { configuration.DB = previousDB })
		expectProviderCredentialBundleLock(mock)

		if err := UpsertProviderCredential(ctx); err != nil {
			t.Fatalf("handler returned unexpected error: %v", err)
		}
		if recorder.Code != http.StatusOK || resolver.provider != string(automationagent.ProviderOpenRouter) || resolver.apiKey != canary || recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("credential storage result: status=%d resolver=%#v body=%s", recorder.Code, resolver, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), canary) || strings.Contains(recorder.Body.String(), "stored-key-must-not-be-returned") {
			t.Fatalf("credential API returned a secret: %s", recorder.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("resolver failure is redacted", func(t *testing.T) {
		const canary = "provider-secret-canary-92"
		resolver := &providerCredentialResolverSpy{err: errors.New("secret-store rejected " + canary)}
		ctx, recorder, restore := providerCredentialMutationContext(t, "openrouter", `{"api_key":"`+canary+`"}`, resolver)
		defer restore()
		db, mock := automationCostLedgerTestDB(t)
		previousDB := configuration.DB
		configuration.DB = db
		t.Cleanup(func() { configuration.DB = previousDB })
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\(\$1\)\)`).
			WithArgs(aiCredentialBundleAdvisoryLockKey).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectRollback()

		if err := UpsertProviderCredential(ctx); err != nil {
			t.Fatalf("handler returned unexpected error: %v", err)
		}
		if recorder.Code != http.StatusBadRequest || strings.Contains(recorder.Body.String(), canary) || strings.Contains(recorder.Body.String(), "secret-store rejected") {
			t.Fatalf("resolver details leaked: status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func providerCredentialMutationContext(t *testing.T, provider, body string, resolver *providerCredentialResolverSpy) (echo.Context, *httptest.ResponseRecorder, func()) {
	t.Helper()
	previousResolver := inferenceCredentials
	inferenceCredentials = resolver
	restoreAuth := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	e := echo.New()
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(httptest.NewRequest(http.MethodPut, "/api/automation/ai/providers/credential", strings.NewReader(body)), recorder)
	ctx.SetParamNames("provider")
	ctx.SetParamValues(provider)
	ctx.Set("cognito_sub", "provider-credential-security-test")
	return ctx, recorder, func() {
		inferenceCredentials = previousResolver
		restoreAuth()
	}
}

func expectProviderCredentialBundleLock(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\(\$1\)\)`).
		WithArgs(aiCredentialBundleAdvisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}
