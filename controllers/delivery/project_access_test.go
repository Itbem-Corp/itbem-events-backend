package delivery

import (
	"net/http"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
)

func TestAuthorizeProjectViewDelegatesPlatformAndMembershipRules(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	projectID := uuid.Must(uuid.NewV4())

	t.Run("platform admin keeps cross-project access", func(t *testing.T) {
		restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
			return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
		}})
		defer restore()
		ctx, _ := epicTestContext(http.MethodGet, "/api/automation/agents/generalist/history?project_id="+projectID.String(), nil, nil, "")
		ctx.Set("workspace_mode", "platform")
		allowed, err := AuthorizeProjectView(ctx, projectID)
		if err != nil || !allowed {
			t.Fatalf("platform admin project view = %v, %v; want true", allowed, err)
		}
	})

	t.Run("viewer membership is evaluated by the canonical role matrix", func(t *testing.T) {
		subject := "shared-project-viewer"
		restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
			return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
		}})
		defer restore()
		organizationID := uuid.Must(uuid.NewV4())
		expectDeliveryOrganizationClientIDs(mock, organizationID, organizationID)
		mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
			WithArgs(projectID, 1).
			WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, organizationID))
		mock.ExpectQuery(`SELECT \* FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
			WithArgs(projectID, subject, 1).
			WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "cognito_sub", "role", "permissions", "created_by", "created_at", "updated_at"}).
				AddRow(uuid.Must(uuid.NewV4()), projectID, subject, "viewer", `[]`, "project-admin", time.Now().UTC(), time.Now().UTC()))
		ctx, _ := epicTestContext(http.MethodGet, "/api/automation/agents/generalist/history?project_id="+projectID.String(), nil, nil, "")
		setDeliveryOrganizationWorkspace(ctx, organizationID)
		allowed, err := AuthorizeProjectView(ctx, projectID)
		if err != nil || !allowed {
			t.Fatalf("viewer project membership = %v, %v; want true", allowed, err)
		}
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
