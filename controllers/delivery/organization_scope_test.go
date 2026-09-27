package delivery

import (
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

func TestProjectAuthorizationRejectsMemberOutsideSelectedOrganization(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	projectID, owningOrganizationID, selectedOrganizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	childID, grandchildID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: "org-scope-member", IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	expectDeliveryOrganizationClientIDs(mock, selectedOrganizationID, selectedOrganizationID, childID, grandchildID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, owningOrganizationID))

	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/projects/"+projectID.String(), []string{"id"}, []string{projectID.String()}, "")
	setDeliveryOrganizationWorkspace(ctx, selectedOrganizationID)
	allowed, err := AuthorizeProjectView(ctx, projectID)
	if allowed || err == nil || recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-organization member access = %v, %v, status %d; want denied generic 404", allowed, err, recorder.Code)
	}
	for _, forbidden := range []string{projectID.String(), owningOrganizationID.String(), selectedOrganizationID.String()} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("cross-organization response disclosed %q: %s", forbidden, recorder.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAuthorizationAllowsChildAndGrandchildOnlyWithProjectMembership(t *testing.T) {
	for _, generation := range []string{"child", "grandchild"} {
		t.Run(generation, func(t *testing.T) {
			db, mock := newEpicTestDB(t)
			previousDB := configuration.DB
			configuration.DB = db
			t.Cleanup(func() { configuration.DB = previousDB })
			organizationID, childID, grandchildID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			projectID := uuid.Must(uuid.NewV4())
			clientID := childID
			if generation == "grandchild" {
				clientID = grandchildID
			}
			subject := "descendant-project-member"
			restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
				return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
			}})
			t.Cleanup(restore)
			expectDeliveryOrganizationClientIDs(mock, organizationID, organizationID, childID, grandchildID)
			mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
				WithArgs(projectID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, clientID))
			mock.ExpectQuery(`SELECT \* FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
				WithArgs(projectID, subject, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "cognito_sub", "role", "permissions", "created_by", "created_at", "updated_at"}).
					AddRow(uuid.Must(uuid.NewV4()), projectID, subject, "viewer", `[]`, "project-admin", time.Now().UTC(), time.Now().UTC()))
			ctx, _ := epicTestContext(http.MethodGet, "/api/automation/projects/"+projectID.String(), []string{"id"}, []string{projectID.String()}, "")
			setDeliveryOrganizationWorkspace(ctx, organizationID)
			allowed, err := AuthorizeProjectView(ctx, projectID)
			if err != nil || !allowed {
				t.Fatalf("%s project with viewer membership = %v, %v; want allowed", generation, allowed, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProjectAuthorizationRejectsSiblingClientEvenWhenUserCouldBeProjectMember(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID, childID, grandchildID, siblingID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	projectID := uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: "sibling-project-member", IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	expectDeliveryOrganizationClientIDs(mock, organizationID, organizationID, childID, grandchildID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, siblingID))
	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/projects/"+projectID.String(), []string{"id"}, []string{projectID.String()}, "")
	setDeliveryOrganizationWorkspace(ctx, organizationID)
	allowed, err := AuthorizeProjectView(ctx, projectID)
	if allowed || err == nil || recorder.Code != http.StatusNotFound {
		t.Fatalf("sibling project access = %v, %v, status %d; want denied generic 404", allowed, err, recorder.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAuthorizationAlsoScopesPlatformAdminsInOrganizationWorkspace(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	projectID, owningOrganizationID, selectedOrganizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	expectDeliveryOrganizationClientIDs(mock, selectedOrganizationID, selectedOrganizationID)
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, owningOrganizationID))
	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/projects/"+projectID.String(), nil, nil, "")
	setDeliveryOrganizationWorkspace(ctx, selectedOrganizationID)
	allowed, err := AuthorizeProjectView(ctx, projectID)
	if allowed || err == nil || recorder.Code != http.StatusNotFound {
		t.Fatalf("platform admin organization workspace access = %v, %v, status %d; want denied generic 404", allowed, err, recorder.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAuthorizationFailsClosedWithoutOrganizationContext(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: "org-scope-member", IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/projects/"+uuid.Must(uuid.NewV4()).String(), nil, nil, "")
	ctx.Set("workspace_mode", "organization")
	allowed, err := AuthorizeProjectView(ctx, uuid.Must(uuid.NewV4()))
	if allowed || err == nil || recorder.Code != http.StatusNotFound {
		t.Fatalf("missing organization context access = %v, %v, status %d; want denied generic 404", allowed, err, recorder.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAuthorizationKeepsExplicitPlatformWorkspaceForAdmins(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
	projectID := uuid.Must(uuid.NewV4())
	ctx, _ := epicTestContext(http.MethodGet, "/api/automation/projects/"+projectID.String(), nil, nil, "")
	ctx.Set("workspace_mode", "platform")
	allowed, err := AuthorizeProjectView(ctx, projectID)
	if err != nil || !allowed {
		t.Fatalf("platform admin project access = %v, %v; want allowed", allowed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListProjectsFiltersBySelectedOrganization(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID := uuid.Must(uuid.NewV4())
	subject := "org-scoped-project-list"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	childID, grandchildID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expectDeliveryOrganizationClientIDs(mock, organizationID, organizationID, childID, grandchildID)
	mock.ExpectQuery(`(?s)SELECT .* FROM "delivery_projects" JOIN delivery_project_members ON .*delivery_projects\.client_id IN \(\$2,\$3,\$4\).*"delivery_projects"\."deleted_at" IS NULL ORDER BY updated_at DESC`).
		WithArgs(subject, organizationID, childID, grandchildID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/projects", nil, nil, "")
	setDeliveryOrganizationWorkspace(ctx, organizationID)
	if err := ListProjects(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("project list status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListClientsFiltersBySelectedOrganization(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID := uuid.Must(uuid.NewV4())
	subject := "org-scoped-client-list"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	childID, grandchildID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expectDeliveryOrganizationClientIDs(mock, organizationID, organizationID, childID, grandchildID)
	mock.ExpectQuery(`(?s)SELECT DISTINCT clients\.\* FROM "clients" JOIN delivery_projects ON .*JOIN delivery_project_members ON .*delivery_projects\.client_id IN \(\$2,\$3,\$4\).*ORDER BY clients\.name ASC`).
		WithArgs(subject, organizationID, childID, grandchildID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/clients", nil, nil, "")
	setDeliveryOrganizationWorkspace(ctx, organizationID)
	if err := ListClients(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("client list status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAndClientListsFailClosedWithoutSelectedOrganization(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	subject := "missing-org-list-actor"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)
	for _, test := range []struct {
		name string
		path string
		call func(echo.Context) error
	}{
		{name: "projects", path: "/api/automation/projects", call: ListProjects},
		{name: "clients", path: "/api/automation/clients", call: ListClients},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, recorder := epicTestContext(http.MethodGet, test.path, nil, nil, "")
			ctx.Set("workspace_mode", "organization")
			if err := test.call(ctx); err == nil || recorder.Code != http.StatusNotFound {
				t.Fatalf("%s list without selected organization returned err=%v status=%d body=%s", test.name, err, recorder.Code, recorder.Body.String())
			}
		})
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAutomationPortfolioConjoinsOrganizationAndProjectMembership(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "org-scoped-portfolio"
	childID, grandchildID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expectDeliveryOrganizationClientIDs(mock, organizationID, organizationID, childID, grandchildID)
	mock.ExpectQuery(`SELECT "project_id","role","permissions" FROM "delivery_project_members" WHERE cognito_sub = \$1`).
		WithArgs(subject).
		WillReturnRows(sqlmock.NewRows([]string{"project_id", "role", "permissions"}).AddRow(projectID, "viewer", `[]`))
	mock.ExpectQuery(`(?s)SELECT delivery_projects\.id,delivery_projects\.client_id,delivery_projects\.name,delivery_projects\.status,delivery_projects\.updated_at FROM "delivery_projects" WHERE delivery_projects\.client_id IN \(\$1,\$2,\$3\) AND delivery_projects\.id IN \(\$4\) AND "delivery_projects"\."deleted_at" IS NULL ORDER BY delivery_projects\.updated_at DESC, delivery_projects\.id DESC`).
		WithArgs(organizationID, childID, grandchildID, projectID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	input, err := loadAutomationPortfolio(&models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, "organization", organizationID)
	if err != nil {
		t.Fatalf("load scoped portfolio: %v", err)
	}
	if input.Projects == nil || len(input.Projects) != 0 {
		t.Fatalf("portfolio projects were not empty after scoped query: %#v", input.Projects)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func setDeliveryOrganizationWorkspace(ctx interface{ Set(string, interface{}) }, organizationID uuid.UUID) {
	ctx.Set("workspace_mode", "organization")
	ctx.Set("organization_id", organizationID)
}

func expectDeliveryOrganizationClientIDs(mock sqlmock.Sqlmock, organizationID uuid.UUID, clientIDs ...uuid.UUID) {
	mock.ExpectQuery(`(?s)WITH RECURSIVE organization_clients\(id\) AS \(.*WHERE clients\.id = \$1 AND clients\.deleted_at IS NULL.*UNION\s+SELECT child\.id.*child\.parent_id = parent\.id.*child\.deleted_at IS NULL.*\)\s*SELECT id FROM organization_clients`).
		WithArgs(organizationID).
		WillReturnRows(func() *sqlmock.Rows {
			rows := sqlmock.NewRows([]string{"id"})
			for _, clientID := range clientIDs {
				rows.AddRow(clientID)
			}
			return rows
		}())
}
