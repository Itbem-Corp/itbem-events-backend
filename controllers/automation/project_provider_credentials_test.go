package automation

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/aicredentials"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestParseProjectCredentialRouteIsStrictlyScoped(t *testing.T) {
	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	context := e.NewContext(request, httptest.NewRecorder())
	context.SetParamNames("projectId", "provider")
	context.SetParamValues("18a34b7d-3b57-4d61-b525-1394355f8601", "OpenRouter")
	projectID, provider, ok := parseProjectCredentialRoute(context)
	if !ok || projectID.String() != "18a34b7d-3b57-4d61-b525-1394355f8601" || provider != "openrouter" {
		t.Fatalf("valid project credential route parsed as %s/%q/%v", projectID, provider, ok)
	}
	context.SetParamValues("18a34b7d-3b57-4d61-b525-1394355f8601", "unknown-provider")
	if _, _, ok := parseProjectCredentialRoute(context); ok {
		t.Fatal("unknown provider was accepted for project credentials")
	}
	context.SetParamValues("not-a-project", "minimax")
	if _, _, ok := parseProjectCredentialRoute(context); ok {
		t.Fatal("invalid project id was accepted")
	}
}

func TestProjectCredentialManagementRequiresExplicitPrivilege(t *testing.T) {
	for _, permission := range []string{`["ai:credentials:manage"]`, `["automation:credentials:manage"]`} {
		if !projectCredentialPermission(permission) {
			t.Fatalf("explicit project credential management permission was rejected: %s", permission)
		}
	}
	for _, permission := range []string{`[]`, `["delivery:view"]`, `not-json`} {
		if projectCredentialPermission(permission) {
			t.Fatalf("unrelated/malformed permission granted project credential access: %s", permission)
		}
	}
}

func TestProjectProviderCredentialBodyIsStrictAndNeverEchoesInputInError(t *testing.T) {
	e := echo.New()
	for _, body := range []string{
		`{"api_key":"project-canary","extra":"private"}`,
		`{"api_key":"first-canary","api_key":"second-canary"}`,
		`{"api_key":"first-canary","API_KEY":"second-canary"}`,
		`{"api_key":"first-canary","api\u005fkey":"second-canary"}`,
		`{"API_KEY":"noncanonical-canary"}`,
	} {
		request := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
		context := e.NewContext(request, httptest.NewRecorder())
		if _, err := decodeProviderCredentialRequest(context); err == nil {
			t.Fatalf("invalid credential body was accepted: %s", body)
		} else if strings.Contains(err.Error(), "canary") {
			t.Fatalf("invalid credential body error echoed a submitted value: %v", err)
		}
	}
}

func TestProjectProviderCredentialDuplicateFieldsNeverReachResolver(t *testing.T) {
	for _, body := range []string{
		`{"api_key":"first-canary","api_key":"duplicate-canary"}`,
		`{"api_key":"first-canary","API_KEY":"duplicate-canary"}`,
		`{"api_key":"first-canary","api\u005fkey":"duplicate-canary"}`,
	} {
		t.Run(body, func(t *testing.T) {
			db, mock := automationCostLedgerTestDB(t)
			previousDB, previousResolver := configuration.DB, inferenceCredentials
			configuration.DB = db
			resolver := &projectCredentialResolverSpy{}
			inferenceCredentials = resolver
			t.Cleanup(func() { configuration.DB, inferenceCredentials = previousDB, previousResolver })

			projectID, clientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			subject := "credential-duplicate-field-test"
			restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
				return &models.User{CognitoSub: subject, IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
			}})
			t.Cleanup(restore)
			expectProjectCredentialProject(mock, projectID, clientID)

			ctx, recorder := projectCredentialTestContext(http.MethodPut, projectID, uuid.Nil, "platform", subject, body)
			if err := UpsertProjectProviderCredential(ctx); err != nil {
				t.Fatalf("handler returned unexpected error: %v", err)
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("ambiguous project credential status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			if resolver.calls() != 0 {
				t.Fatalf("ambiguous request reached resolver %d times", resolver.calls())
			}
			for _, secret := range []string{"first-canary", "duplicate-canary"} {
				if strings.Contains(recorder.Body.String(), secret) {
					t.Fatalf("project credential error echoed %q: %s", secret, recorder.Body.String())
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type projectCredentialResolverSpy struct {
	statusReads int
	upserts     int
	deletes     int
}

func (*projectCredentialResolverSpy) APIKey(context.Context, string) (string, error) {
	return "", nil
}

func (*projectCredentialResolverSpy) ReplaceAPIKey(context.Context, string, string) error { return nil }

func (r *projectCredentialResolverSpy) APIKeyForProject(context.Context, string, string) (string, error) {
	return "", nil
}

func (r *projectCredentialResolverSpy) HasAPIKeyForProject(context.Context, string, string) (bool, error) {
	r.statusReads++
	return true, nil
}

func (r *projectCredentialResolverSpy) ReplaceProjectAPIKey(context.Context, string, string, string) error {
	r.upserts++
	return nil
}

func (r *projectCredentialResolverSpy) DeleteProjectAPIKey(context.Context, string, string) error {
	r.deletes++
	return nil
}

func (r *projectCredentialResolverSpy) calls() int { return r.statusReads + r.upserts + r.deletes }

func TestProjectProviderCredentialEndpointsRejectCrossOrganizationBeforeResolver(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		handler echo.HandlerFunc
		root    bool
	}{
		{name: "status member", method: http.MethodGet, handler: GetProjectProviderCredentialStatus},
		{name: "upsert manager", method: http.MethodPut, handler: UpsertProjectProviderCredential},
		{name: "delete manager", method: http.MethodDelete, handler: DeleteProjectProviderCredential},
		{name: "status platform admin", method: http.MethodGet, handler: GetProjectProviderCredentialStatus, root: true},
		{name: "upsert platform admin", method: http.MethodPut, handler: UpsertProjectProviderCredential, root: true},
		{name: "delete platform admin", method: http.MethodDelete, handler: DeleteProjectProviderCredential, root: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db, mock := automationCostLedgerTestDB(t)
			previousDB, previousResolver := configuration.DB, inferenceCredentials
			configuration.DB = db
			resolver := &projectCredentialResolverSpy{}
			inferenceCredentials = resolver
			t.Cleanup(func() { configuration.DB, inferenceCredentials = previousDB, previousResolver })

			projectID := uuid.Must(uuid.NewV4())
			projectOrganizationID, selectedOrganizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			subject := "credential-cross-organization"
			user := &models.User{CognitoSub: subject, IsRoot: test.root, RootLevel: models.RootLevelNone}
			if test.root {
				user.RootLevel = models.RootLevelPrimary
			}
			restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) { return user, nil }})
			t.Cleanup(restore)
			expectProjectCredentialProject(mock, projectID, projectOrganizationID)
			expectOrganizationCredentialClientIDs(mock, selectedOrganizationID, selectedOrganizationID)

			body := ""
			if test.method == http.MethodPut {
				body = `{"api_key":"credential-must-not-be-read"}`
			}
			ctx, recorder := projectCredentialTestContext(test.method, projectID, selectedOrganizationID, "organization", subject, body)
			err := test.handler(ctx)
			if err != nil {
				t.Fatalf("handler returned unexpected error: %v", err)
			}
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("cross-organization %s status = %d, want generic 404: %s", test.name, recorder.Code, recorder.Body.String())
			}
			for _, forbidden := range []string{projectID.String(), projectOrganizationID.String(), selectedOrganizationID.String(), "credential-must-not-be-read"} {
				if strings.Contains(recorder.Body.String(), forbidden) {
					t.Fatalf("cross-organization response disclosed %q: %s", forbidden, recorder.Body.String())
				}
			}
			if resolver.calls() != 0 {
				t.Fatalf("cross-organization request reached credential resolver %d times", resolver.calls())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProjectProviderCredentialEndpointsAllowAuthorizedProjectMemberInsideWorkspace(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		handler echo.HandlerFunc
		body    string
	}{
		{name: "status", method: http.MethodGet, handler: GetProjectProviderCredentialStatus},
		{name: "upsert", method: http.MethodPut, handler: UpsertProjectProviderCredential, body: `{"api_key":"inside-workspace-key"}`},
		{name: "delete", method: http.MethodDelete, handler: DeleteProjectProviderCredential},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db, mock := automationCostLedgerTestDB(t)
			previousDB, previousResolver := configuration.DB, inferenceCredentials
			configuration.DB = db
			resolver := &projectCredentialResolverSpy{}
			inferenceCredentials = resolver
			t.Cleanup(func() { configuration.DB, inferenceCredentials = previousDB, previousResolver })

			projectID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			subject := "credential-workspace-member"
			restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
				return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
			}})
			t.Cleanup(restore)
			expectProjectCredentialProject(mock, projectID, organizationID)
			expectOrganizationCredentialClientIDs(mock, organizationID, organizationID)
			mock.ExpectQuery(`SELECT "role","permissions" FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
				WithArgs(projectID, subject, 1).
				WillReturnRows(sqlmock.NewRows([]string{"role", "permissions"}).AddRow("delivery_manager", `[]`))
			if test.method != http.MethodGet {
				mock.ExpectBegin()
				mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(hashtext($1))")).
					WithArgs(aiCredentialBundleAdvisoryLockKey).
					WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}

			ctx, recorder := projectCredentialTestContext(test.method, projectID, organizationID, "organization", subject, test.body)
			if err := test.handler(ctx); err != nil {
				t.Fatalf("handler returned unexpected error: %v", err)
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("same-organization %s status = %d, want 200: %s", test.name, recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), `"status":"stored"`) && test.method != http.MethodDelete {
				t.Fatalf("same-organization %s response did not return the safe credential status: %s", test.name, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "inside-workspace-key") {
				t.Fatal("credential mutation response echoed the API key")
			}
			wantCalls := 1
			if resolver.calls() != wantCalls {
				t.Fatalf("same-organization %s resolver calls = %d, want %d", test.name, resolver.calls(), wantCalls)
			}
			if test.method == http.MethodGet && resolver.statusReads != 1 || test.method == http.MethodPut && resolver.upserts != 1 || test.method == http.MethodDelete && resolver.deletes != 1 {
				t.Fatalf("same-organization %s called the wrong resolver operation: %#v", test.name, resolver)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProjectProviderCredentialEndpointsRespectRecursiveOrganizationScope(t *testing.T) {
	endpoints := []struct {
		name    string
		method  string
		handler echo.HandlerFunc
		body    string
	}{
		{name: "status", method: http.MethodGet, handler: GetProjectProviderCredentialStatus},
		{name: "upsert", method: http.MethodPut, handler: UpsertProjectProviderCredential, body: `{"api_key":"descendant-project-key"}`},
		{name: "delete", method: http.MethodDelete, handler: DeleteProjectProviderCredential},
	}
	for _, projectDepth := range []struct {
		name  string
		index int
	}{
		{name: "child", index: 1},
		{name: "grandchild", index: 2},
		{name: "unrelated sibling", index: 3},
	} {
		for _, endpoint := range endpoints {
			t.Run(projectDepth.name+"/"+endpoint.name, func(t *testing.T) {
				db, mock := automationCostLedgerTestDB(t)
				previousDB, previousResolver := configuration.DB, inferenceCredentials
				configuration.DB = db
				resolver := &projectCredentialResolverSpy{}
				inferenceCredentials = resolver
				t.Cleanup(func() { configuration.DB, inferenceCredentials = previousDB, previousResolver })

				projectID := uuid.Must(uuid.NewV4())
				organizationID, childID, grandchildID, siblingID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
				clientIDs := []uuid.UUID{organizationID, childID, grandchildID}
				projectClientID := []uuid.UUID{organizationID, childID, grandchildID, siblingID}[projectDepth.index]
				subject := "credential-recursive-scope-member"
				restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
					return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
				}})
				t.Cleanup(restore)
				expectProjectCredentialProject(mock, projectID, projectClientID)
				expectOrganizationCredentialClientIDs(mock, organizationID, clientIDs...)

				insideScope := projectDepth.index != 3
				if insideScope {
					mock.ExpectQuery(`SELECT "role","permissions" FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
						WithArgs(projectID, subject, 1).
						WillReturnRows(sqlmock.NewRows([]string{"role", "permissions"}).AddRow("delivery_manager", `[]`))
					if endpoint.method != http.MethodGet {
						mock.ExpectBegin()
						mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(hashtext($1))")).
							WithArgs(aiCredentialBundleAdvisoryLockKey).
							WillReturnResult(sqlmock.NewResult(0, 1))
						mock.ExpectCommit()
					}
				}

				ctx, recorder := projectCredentialTestContext(endpoint.method, projectID, organizationID, "organization", subject, endpoint.body)
				if err := endpoint.handler(ctx); err != nil {
					t.Fatalf("handler returned unexpected error: %v", err)
				}
				wantStatus := http.StatusOK
				wantCalls := 1
				if !insideScope {
					wantStatus, wantCalls = http.StatusNotFound, 0
				}
				if recorder.Code != wantStatus {
					t.Fatalf("%s %s status = %d, want %d: %s", projectDepth.name, endpoint.name, recorder.Code, wantStatus, recorder.Body.String())
				}
				if resolver.calls() != wantCalls {
					t.Fatalf("%s %s credential resolver calls = %d, want %d", projectDepth.name, endpoint.name, resolver.calls(), wantCalls)
				}
				if !insideScope {
					for _, forbidden := range []string{projectID.String(), projectClientID.String(), organizationID.String(), "descendant-project-key"} {
						if strings.Contains(recorder.Body.String(), forbidden) {
							t.Fatalf("out-of-scope response disclosed %q: %s", forbidden, recorder.Body.String())
						}
					}
				}
				if strings.Contains(recorder.Body.String(), "descendant-project-key") {
					t.Fatal("credential endpoint echoed the submitted key")
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestSameOrganizationNonmemberCannotReadOrMutateProjectCredential(t *testing.T) {
	for _, endpoint := range []struct {
		name    string
		method  string
		handler echo.HandlerFunc
		body    string
	}{
		{name: "status", method: http.MethodGet, handler: GetProjectProviderCredentialStatus},
		{name: "upsert", method: http.MethodPut, handler: UpsertProjectProviderCredential, body: `{"api_key":"cross-project-submission-canary"}`},
		{name: "delete", method: http.MethodDelete, handler: DeleteProjectProviderCredential},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			db, mock := automationCostLedgerTestDB(t)
			previousDB, previousResolver := configuration.DB, inferenceCredentials
			configuration.DB = db
			resolver := &projectCredentialResolverSpy{}
			inferenceCredentials = resolver
			t.Cleanup(func() { configuration.DB, inferenceCredentials = previousDB, previousResolver })

			requestedProjectID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			subject := "credential-member-of-different-project"
			restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
				return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
			}})
			t.Cleanup(restore)
			expectProjectCredentialProject(mock, requestedProjectID, organizationID)
			expectOrganizationCredentialClientIDs(mock, organizationID, organizationID)
			// The organization contains the requested project, but this subject has
			// no project membership there; organization membership alone is not enough.
			mock.ExpectQuery(`SELECT "role","permissions" FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
				WithArgs(requestedProjectID, subject, 1).
				WillReturnRows(sqlmock.NewRows([]string{"role", "permissions"}))

			ctx, recorder := projectCredentialTestContext(endpoint.method, requestedProjectID, organizationID, "organization", subject, endpoint.body)
			if err := endpoint.handler(ctx); err != nil {
				t.Fatalf("handler returned unexpected error: %v", err)
			}
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("same-organization nonmember status = %d, want 403: %s", recorder.Code, recorder.Body.String())
			}
			if resolver.calls() != 0 {
				t.Fatalf("cross-project request reached credential resolver %d times", resolver.calls())
			}
			if strings.Contains(recorder.Body.String(), "cross-project-submission-canary") || strings.Contains(recorder.Body.String(), requestedProjectID.String()) {
				t.Fatalf("nonmember response exposed a credential or project identifier: %s", recorder.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCredentialStatusNeverSerializesCentralBundleOrOtherProjects(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB, previousResolver := configuration.DB, inferenceCredentials
	configuration.DB = db
	t.Cleanup(func() { configuration.DB, inferenceCredentials = previousDB, previousResolver })

	projectA, projectB, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	store := &isolatedCredentialTestStore{value: []byte(`{"schema_version":2,"credentials":{"openrouter":{"api_key":"global-status-canary"}},"projects":{"` + projectA.String() + `":{"credentials":{"openrouter":{"api_key":"project-a-status-canary"}}},"` + projectB.String() + `":{"credentials":{"openrouter":{"api_key":"project-b-status-canary"}}}}}`)}
	resolver, err := aicredentials.NewResolver(store, "synthetic-test-bundle", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	inferenceCredentials = resolver

	subject := "credential-status-member"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	expectProjectCredentialProject(mock, projectA, organizationID)
	expectOrganizationCredentialClientIDs(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "role","permissions" FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectA, subject, 1).
		WillReturnRows(sqlmock.NewRows([]string{"role", "permissions"}).AddRow("contributor", `[]`))

	ctx, recorder := projectCredentialTestContext(http.MethodGet, projectA, organizationID, "organization", subject, "")
	if err := GetProjectProviderCredentialStatus(ctx); err != nil {
		t.Fatalf("authorized status returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"stored"`) {
		t.Fatalf("authorized project status = %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{
		"global-status-canary", "project-a-status-canary", "project-b-status-canary",
		projectB.String(), `"credentials"`, `"api_key"`, `"projects"`,
	} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("status response exposed bundle material %q: %s", forbidden, recorder.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectCredentialStoreErrorsDoNotEchoSubmittedKey(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB, previousResolver := configuration.DB, inferenceCredentials
	configuration.DB = db
	t.Cleanup(func() { configuration.DB, inferenceCredentials = previousDB, previousResolver })

	projectID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	const apiKey = "store-error-api-key-canary"
	store := &isolatedCredentialTestStore{
		value:  []byte(`{"schema_version":2,"projects":{}}`),
		putErr: errors.New("synthetic store failure included " + apiKey),
	}
	resolver, err := aicredentials.NewResolver(store, "synthetic-test-bundle", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	inferenceCredentials = resolver
	subject := "credential-write-manager"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	expectProjectCredentialProject(mock, projectID, organizationID)
	expectOrganizationCredentialClientIDs(mock, organizationID, organizationID)
	mock.ExpectQuery(`SELECT "role","permissions" FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectID, subject, 1).
		WillReturnRows(sqlmock.NewRows([]string{"role", "permissions"}).AddRow("delivery_manager", `[]`))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(hashtext($1))")).
		WithArgs(aiCredentialBundleAdvisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	ctx, recorder := projectCredentialTestContext(http.MethodPut, projectID, organizationID, "organization", subject, `{"api_key":"`+apiKey+`"}`)
	if err := UpsertProjectProviderCredential(ctx); err != nil {
		t.Fatalf("handler returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("store failure status = %d, want generic failure: %s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{apiKey, "synthetic store failure", "synthetic-test-bundle"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("store error response disclosed %q: %s", forbidden, recorder.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type isolatedCredentialTestStore struct {
	value  []byte
	putErr error
}

func (s *isolatedCredentialTestStore) Get(context.Context, string) ([]byte, error) {
	return append([]byte(nil), s.value...), nil
}

func (s *isolatedCredentialTestStore) Put(_ context.Context, _ string, value []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	s.value = append([]byte(nil), value...)
	return nil
}

func TestProjectProviderCredentialMutationsStillRequireManagePermission(t *testing.T) {
	endpoints := []struct {
		name    string
		method  string
		handler echo.HandlerFunc
		body    string
		status  int
	}{
		{name: "status", method: http.MethodGet, handler: GetProjectProviderCredentialStatus, status: http.StatusOK},
		{name: "upsert", method: http.MethodPut, handler: UpsertProjectProviderCredential, body: `{"api_key":"member-key"}`, status: http.StatusForbidden},
		{name: "delete", method: http.MethodDelete, handler: DeleteProjectProviderCredential, status: http.StatusForbidden},
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			db, mock := automationCostLedgerTestDB(t)
			previousDB, previousResolver := configuration.DB, inferenceCredentials
			configuration.DB = db
			resolver := &projectCredentialResolverSpy{}
			inferenceCredentials = resolver
			t.Cleanup(func() { configuration.DB, inferenceCredentials = previousDB, previousResolver })

			projectID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			subject := "credential-view-only-member"
			restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
				return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
			}})
			t.Cleanup(restore)
			expectProjectCredentialProject(mock, projectID, organizationID)
			expectOrganizationCredentialClientIDs(mock, organizationID, organizationID)
			mock.ExpectQuery(`SELECT "role","permissions" FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
				WithArgs(projectID, subject, 1).
				WillReturnRows(sqlmock.NewRows([]string{"role", "permissions"}).AddRow("contributor", `[]`))

			ctx, recorder := projectCredentialTestContext(endpoint.method, projectID, organizationID, "organization", subject, endpoint.body)
			if err := endpoint.handler(ctx); err != nil {
				t.Fatalf("handler returned unexpected error: %v", err)
			}
			if recorder.Code != endpoint.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, endpoint.status, recorder.Body.String())
			}
			wantCalls := 0
			if endpoint.method == http.MethodGet {
				wantCalls = 1
			}
			if resolver.calls() != wantCalls {
				t.Fatalf("credential resolver calls = %d, want %d", resolver.calls(), wantCalls)
			}
			if strings.Contains(recorder.Body.String(), "member-key") {
				t.Fatal("credential response echoed submitted key")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProjectProviderCredentialStatusAllowsPlatformAdminInPlatformWorkspace(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB, previousResolver := configuration.DB, inferenceCredentials
	configuration.DB = db
	resolver := &projectCredentialResolverSpy{}
	inferenceCredentials = resolver
	t.Cleanup(func() { configuration.DB, inferenceCredentials = previousDB, previousResolver })

	projectID, projectOrganizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: "credential-platform-admin", IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	expectProjectCredentialProject(mock, projectID, projectOrganizationID)
	ctx, recorder := projectCredentialTestContext(http.MethodGet, projectID, uuid.Nil, "platform", "credential-platform-admin", "")
	if err := GetProjectProviderCredentialStatus(ctx); err != nil {
		t.Fatalf("platform workspace status returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusOK || resolver.statusReads != 1 {
		t.Fatalf("platform workspace status = %d with %d resolver reads, want 200/1: %s", recorder.Code, resolver.statusReads, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func projectCredentialTestContext(method string, projectID, organizationID uuid.UUID, workspaceMode, subject, body string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	request := httptest.NewRequest(method, "/api/automation/ai/projects/"+projectID.String()+"/providers/openrouter/credential", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(request, recorder)
	ctx.SetParamNames("projectId", "provider")
	ctx.SetParamValues(projectID.String(), "openrouter")
	ctx.Set("cognito_sub", subject)
	ctx.Set("tenant_code", "itbem")
	ctx.Set("workspace_mode", workspaceMode)
	if organizationID != uuid.Nil {
		ctx.Set("organization_id", organizationID)
	}
	return ctx, recorder
}

func expectProjectCredentialProject(mock sqlmock.Sqlmock, projectID, organizationID uuid.UUID) {
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, organizationID))
}

func expectOrganizationCredentialClientIDs(mock sqlmock.Sqlmock, organizationID uuid.UUID, clientIDs ...uuid.UUID) {
	rows := sqlmock.NewRows([]string{"id"})
	for _, clientID := range clientIDs {
		rows.AddRow(clientID)
	}
	mock.ExpectQuery(`WITH RECURSIVE organization_clients`).
		WithArgs(organizationID).
		WillReturnRows(rows)
}
