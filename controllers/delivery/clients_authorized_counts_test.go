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
	"gorm.io/gorm"
)

func TestListClientsAggregatesOnlyProjectsVisibleInSelectedOrganization(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })

	organizationID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	visibleProjectID := uuid.Must(uuid.NewV4())
	inaccessibleProjectID := uuid.Must(uuid.NewV4())
	subject := "client-count-project-member"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)

	// Both projects belong to this client and selected organization, but this
	// actor is a member only of visibleProjectID. The aggregate subqueries must
	// use the same membership scope as the client directory.
	if visibleProjectID == inaccessibleProjectID {
		t.Fatal("fixture projects must be distinct")
	}
	expectDeliveryOrganizationClientIDs(mock, organizationID, organizationID, clientID)
	mock.ExpectQuery(`(?s)SELECT DISTINCT clients\.\* FROM "clients" JOIN delivery_projects ON .*JOIN delivery_project_members ON .*delivery_projects\.client_id IN \(\$2,\$3\).*ORDER BY clients\.name ASC`).
		WithArgs(subject, organizationID, clientID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "code", "client_type_id", "logo", "media_bucket", "is_active", "parent_id", "created_at", "updated_at", "deleted_at"}).
			AddRow(clientID, "Visible client", "visible-client", uuid.Must(uuid.NewV4()), "", "", true, organizationID, time.Now().UTC(), time.Now().UTC(), nil))
	mock.ExpectQuery(`SELECT \* FROM "delivery_client_profiles" WHERE "delivery_client_profiles"\."client_id" = \$1`).
		WithArgs(clientID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	projectCountQuery := `(?s)SELECT count\(\*\) FROM "delivery_projects" WHERE .*delivery_projects\.client_id = \$\d+.*delivery_projects\.id IN \(SELECT delivery_projects\.id FROM "delivery_projects" JOIN delivery_project_members ON .*delivery_project_members\.cognito_sub = \$\d+.*delivery_projects\.client_id IN \(\$\d+,\$\d+\).*\)`
	mock.ExpectQuery(projectCountQuery).
		WithArgs(clientID, subject, clientID, organizationID, clientID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	conversationCountQuery := `(?s)SELECT count\(\*\) FROM "delivery_context_sources" WHERE .*delivery_context_sources\.project_id IN \(SELECT delivery_projects\.id FROM "delivery_projects" JOIN delivery_project_members ON .*delivery_project_members\.cognito_sub = \$\d+.*delivery_projects\.client_id IN \(\$\d+,\$\d+\).*\)`
	mock.ExpectQuery(conversationCountQuery).
		WithArgs("client_conversation", subject, clientID, organizationID, clientID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/clients", nil, nil, "")
	setDeliveryOrganizationWorkspace(ctx, organizationID)
	if err := ListClients(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("client list status = %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, expected := range []string{`"project_count":1`, `"conversation_count":1`} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("client list did not return the authorized aggregate %s: %s", expected, recorder.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryClientVisibleProjectsPreservesPlatformAndOrganizationScopes(t *testing.T) {
	db, _ := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	organizationID, clientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	organizationClientIDs := []uuid.UUID{organizationID, clientID}

	for _, test := range []struct {
		name             string
		user             *models.User
		workspaceMode    string
		wantMembership   bool
		wantOrganization bool
	}{
		{
			name:          "ordinary organization member",
			user:          &models.User{CognitoSub: "viewer", IsRoot: false, RootLevel: models.RootLevelNone},
			workspaceMode: "organization", wantMembership: true, wantOrganization: true,
		},
		{
			name:          "platform administrator organization view",
			user:          &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary},
			workspaceMode: "organization", wantOrganization: true,
		},
		{
			name:          "platform administrator platform view",
			user:          &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary},
			workspaceMode: "platform",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := deliveryClientVisibleProjectsQuery(test.user, test.workspaceMode, organizationClientIDs, clientID).
				Session(&gorm.Session{DryRun: true}).Find(&[]models.DeliveryProject{}).
				Statement.SQL.String()
			if got := strings.Contains(query, "JOIN delivery_project_members"); got != test.wantMembership {
				t.Fatalf("membership filter present = %v, want %v; query: %s", got, test.wantMembership, query)
			}
			if got := strings.Contains(query, "delivery_projects.client_id IN"); got != test.wantOrganization {
				t.Fatalf("organization filter present = %v, want %v; query: %s", got, test.wantOrganization, query)
			}
		})
	}
}
