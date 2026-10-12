package clientrepository

import (
	"testing"

	"events-stocks/configuration"
	"events-stocks/dtos"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func clientRepoTestDB(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	previous := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previous })
	mock.MatchExpectationsInOrder(false)
	return mock
}

// ROLES-97 / ROLES-N4: the scoped list must expose the access role of every
// row, so the memberships API can mark inherited organizations and the
// creatable-types catalog can see which organizations the caller owns.
func TestListClientsPaginatedAttachesAccessRoles(t *testing.T) {
	mock := clientRepoTestDB(t)
	userID := uuid.Must(uuid.NewV4())
	direct := uuid.Must(uuid.NewV4())
	inherited := uuid.Must(uuid.NewV4())

	mock.ExpectQuery(`(?s)SELECT COUNT\(DISTINCT\("clients"\."id"\)\).*reachable_clients`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`(?s)SELECT DISTINCT clients\.\*, scoped_access\.access_role AS access_role.*reachable_clients`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "access_role"}).
			AddRow(direct, "Agencia", "OWNER").
			AddRow(inherited, "Cliente", "INHERITED_OWNER"))
	mock.ExpectQuery(`(?s)SELECT scoped_access\.client_id AS client_id, scoped_access\.access_role AS access_role FROM "clients".*WHERE clients\.id IN`).
		WillReturnRows(sqlmock.NewRows([]string{"client_id", "access_role"}).
			AddRow(direct, "OWNER").
			AddRow(inherited, "INHERITED_OWNER"))

	clients, total, err := ListClientsPaginated(&userID, dtos.ClientsListQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListClientsPaginated: %v", err)
	}
	if total != 2 || len(clients) != 2 {
		t.Fatalf("got total=%d len=%d, want 2/2", total, len(clients))
	}
	roles := map[uuid.UUID]string{}
	for _, client := range clients {
		roles[client.ID] = client.AccessRole
	}
	if roles[direct] != "OWNER" || roles[inherited] != "INHERITED_OWNER" {
		t.Fatalf("access roles not attached: %#v", roles)
	}
	responses := dtos.NewClientResponses(clients)
	for _, response := range responses {
		if want := response.ID == inherited; response.Inherited != want {
			t.Fatalf("client %s inherited=%v, want %v", response.ID, response.Inherited, want)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ROLES-N2: with an application code the reach stops at another
// application's portal organization, in the list, the count and the
// access-role lookup alike.
func TestListClientsPaginatedApplicationScopeStopsAtForeignPortals(t *testing.T) {
	mock := clientRepoTestDB(t)
	userID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	boundary := `.*reachable_clients.*FROM applications other\s+WHERE LOWER\(other\.code\) = LOWER\(child\.code\) AND LOWER\(other\.code\) <> \$2`

	mock.ExpectQuery(`(?s)SELECT COUNT` + boundary).
		WithArgs(userID, "eventiapp").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT DISTINCT clients` + boundary).
		WithArgs(userID, "eventiapp", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(clientID, "EventiApp"))
	mock.ExpectQuery(`SELECT scoped_access\.client_id` + boundary).
		WithArgs(userID, "eventiapp", clientID).
		WillReturnRows(sqlmock.NewRows([]string{"client_id", "access_role"}).AddRow(clientID, "OWNER"))

	clients, _, err := ListClientsPaginated(&userID, dtos.ClientsListQuery{Page: 1, PageSize: 20, ApplicationCode: " EventiApp "})
	if err != nil {
		t.Fatalf("ListClientsPaginated: %v", err)
	}
	if len(clients) != 1 || clients[0].AccessRole != "OWNER" {
		t.Fatalf("unexpected clients: %#v", clients)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
