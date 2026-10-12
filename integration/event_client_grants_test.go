//go:build integration

package integration_test

import (
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/models"
	clientrepository "events-stocks/repositories/clientrepository"
	eventclientrepository "events-stocks/repositories/eventclientrepository"
	eventsrepository "events-stocks/repositories/eventsrepository"
)

type eventClientWorld struct {
	db         *gorm.DB
	repo       *eventclientrepository.Repository
	suffix     string
	clientType models.ClientType
	roles      map[string]models.ClientRole
	root       models.Client
	cafetton   models.Client
	eventType  models.EventType
	eventiApp  models.Application
}

func newEventClientWorld(t *testing.T) *eventClientWorld {
	t.Helper()
	db := configuration.DB
	w := &eventClientWorld{db: db, repo: eventclientrepository.New(db), suffix: uuid.Must(uuid.NewV4()).String()[:8], roles: map[string]models.ClientRole{}}
	w.clientType = models.ClientType{Name: "type-" + w.suffix, Code: "TYPE_" + w.suffix}
	require.NoError(t, db.Create(&w.clientType).Error)
	for _, code := range []string{"OWNER", "EDITOR"} {
		role := models.ClientRole{Name: code, Code: code}
		require.NoError(t, db.Where(models.ClientRole{Code: code}).FirstOrCreate(&role).Error)
		w.roles[code] = role
	}
	// Applications are global; create them once per database.
	for _, code := range []string{"eventiapp", "cafettonhouse"} {
		app := models.Application{Code: code, Name: code, IsActive: true}
		require.NoError(t, db.Where(models.Application{Code: code}).FirstOrCreate(&app).Error)
		if code == "eventiapp" {
			w.eventiApp = app
		}
	}
	w.root = w.client(t, "eventiapp", nil)
	w.cafetton = w.client(t, "cafettonhouse", nil)
	w.eventType = models.EventType{Name: "boda-" + w.suffix}
	require.NoError(t, db.Create(&w.eventType).Error)
	return w
}

// client creates an organization; a fixed code (eventiapp, cafettonhouse) is
// reused across tests because client codes are unique.
func (w *eventClientWorld) client(t *testing.T, code string, parent *models.Client) models.Client {
	t.Helper()
	client := models.Client{Name: code, Code: code, ClientTypeID: w.clientType.ID, IsActive: true}
	if code == "eventiapp" || code == "cafettonhouse" {
		require.NoError(t, w.db.Where(models.Client{Code: code}).Attrs(client).FirstOrCreate(&client).Error)
		return client
	}
	client.Code = code + "-" + w.suffix + "-" + uuid.Must(uuid.NewV4()).String()[:4]
	if parent != nil {
		client.ParentID = &parent.ID
	}
	require.NoError(t, w.db.Create(&client).Error)
	return client
}

func (w *eventClientWorld) user(t *testing.T, email string) models.User {
	t.Helper()
	user := models.User{CognitoSub: "sub-" + uuid.Must(uuid.NewV4()).String(), Email: email, FirstName: "Ana", LastName: "López", IsActive: true}
	require.NoError(t, w.db.Create(&user).Error)
	return user
}

func (w *eventClientWorld) event(t *testing.T, org models.Client, name string) models.Event {
	t.Helper()
	event := models.Event{
		ID: uuid.Must(uuid.NewV4()), Name: name + " " + uuid.Must(uuid.NewV4()).String()[:8], Identifier: name + "-" + uuid.Must(uuid.NewV4()).String()[:8],
		EventTypeID: w.eventType.ID, ClientID: &org.ID, IsActive: true, EventDateTime: time.Now().Add(48 * time.Hour),
	}
	require.NoError(t, w.db.Create(&event).Error)
	return event
}

func (w *eventClientWorld) member(t *testing.T, org models.Client, user models.User, role string, active bool) models.ClientMember {
	t.Helper()
	member := models.ClientMember{ClientID: org.ID, UserID: user.ID, ClientRoleID: w.roles[role].ID, IsActive: true}
	require.NoError(t, w.db.Create(&member).Error)
	if !active {
		require.NoError(t, w.db.Model(&member).Update("is_active", false).Error)
	}
	return member
}

// memberApplication gives a membership an active access row for the EventiApp
// application, which the caller-access event lists require.
func (w *eventClientWorld) memberApplication(t *testing.T, member models.ClientMember) {
	t.Helper()
	access := models.ClientMemberApplication{ClientMemberID: member.ID, ApplicationID: w.eventiApp.ID, IsActive: true}
	require.NoError(t, w.db.Create(&access).Error)
}

func (w *eventClientWorld) grant(t *testing.T, event models.Event, user, actor models.User) {
	t.Helper()
	_, _, err := w.repo.Grant(nil, event.ID, user.ID, *event.ClientID, actor.ID)
	require.NoError(t, err)
}

func TestEventClientGrantActivePredicates(t *testing.T) {
	w := newEventClientWorld(t)
	org := w.client(t, "org", &w.root)
	manager := w.user(t, "manager-"+w.suffix+"@ejemplo.mx")
	client := w.user(t, "client-"+w.suffix+"@ejemplo.mx")
	event := w.event(t, org, "boda")
	w.grant(t, event, client, manager)

	active, err := w.repo.HasActiveGrant(event.ID, client.ID)
	require.NoError(t, err)
	assert.True(t, active)
	count, err := w.repo.CountActiveForUser(client.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Revoked.
	revoked, err := w.repo.Revoke(nil, event.ID, client.ID, manager.ID)
	require.NoError(t, err)
	assert.True(t, revoked)
	active, _ = w.repo.HasActiveGrant(event.ID, client.ID)
	assert.False(t, active)
	again, err := w.repo.Revoke(nil, event.ID, client.ID, manager.ID)
	require.NoError(t, err)
	assert.False(t, again)

	// Event soft-deleted.
	deleted := w.event(t, org, "deleted")
	w.grant(t, deleted, client, manager)
	require.NoError(t, w.db.Delete(&deleted).Error)
	active, _ = w.repo.HasActiveGrant(deleted.ID, client.ID)
	assert.False(t, active)

	// Organization inactive, then deleted.
	inactiveOrg := w.client(t, "inactive", &w.root)
	inactiveEvent := w.event(t, inactiveOrg, "inactive")
	w.grant(t, inactiveEvent, client, manager)
	require.NoError(t, w.db.Model(&inactiveOrg).Update("is_active", false).Error)
	active, _ = w.repo.HasActiveGrant(inactiveEvent.ID, client.ID)
	assert.False(t, active)
	deletedOrg := w.client(t, "gone", &w.root)
	goneEvent := w.event(t, deletedOrg, "gone")
	w.grant(t, goneEvent, client, manager)
	require.NoError(t, w.db.Delete(&deletedOrg).Error)
	active, _ = w.repo.HasActiveGrant(goneEvent.ID, client.ID)
	assert.False(t, active)

	// Event moved to another organization.
	moved := w.event(t, org, "moved")
	w.grant(t, moved, client, manager)
	other := w.client(t, "other", &w.root)
	require.NoError(t, w.db.Model(&moved).Update("client_id", other.ID).Error)
	active, _ = w.repo.HasActiveGrant(moved.ID, client.ID)
	assert.False(t, active)

	// Former (inactive) member of the organization.
	formerOrg := w.client(t, "former", &w.root)
	formerEvent := w.event(t, formerOrg, "former")
	w.grant(t, formerEvent, client, manager)
	w.member(t, formerOrg, client, "EDITOR", false)
	active, _ = w.repo.HasActiveGrant(formerEvent.ID, client.ID)
	assert.False(t, active)
}

func TestEventClientGrantConcurrentInsertKeepsOneActiveRow(t *testing.T) {
	w := newEventClientWorld(t)
	org := w.client(t, "org", &w.root)
	manager := w.user(t, "m-"+w.suffix+"@ejemplo.mx")
	client := w.user(t, "c-"+w.suffix+"@ejemplo.mx")
	event := w.event(t, org, "boda")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := w.repo.Grant(nil, event.ID, client.ID, org.ID, manager.ID)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var active int64
	require.NoError(t, w.db.Model(&models.EventClientGrant{}).Where("event_id = ? AND user_id = ? AND revoked_at IS NULL", event.ID, client.ID).Count(&active).Error)
	assert.Equal(t, int64(1), active)
}

func TestEventClientGrantRevokesStaleRowFromPreviousOrganization(t *testing.T) {
	w := newEventClientWorld(t)
	orgP := w.client(t, "p", &w.root)
	orgQ := w.client(t, "q", &w.root)
	manager := w.user(t, "m-"+w.suffix+"@ejemplo.mx")
	client := w.user(t, "c-"+w.suffix+"@ejemplo.mx")
	event := w.event(t, orgP, "boda")
	w.grant(t, event, client, manager)
	require.NoError(t, w.db.Model(&event).Update("client_id", orgQ.ID).Error)
	created, grant, err := w.repo.Grant(nil, event.ID, client.ID, orgQ.ID, manager.ID)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, orgQ.ID, grant.OrganizationID)
	active, _ := w.repo.HasActiveGrant(event.ID, client.ID)
	assert.True(t, active)

	revokedAll, err := w.repo.RevokeAllForEvent(nil, event.ID, manager.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), revokedAll)
}

func TestResolveClientProductUsesNearestBrandedAncestor(t *testing.T) {
	w := newEventClientWorld(t)
	eventiOrg := w.client(t, "org", &w.root)
	child := w.client(t, "child", &eventiOrg)
	cafettonOrg := w.client(t, "cafe", &w.cafetton)
	orphan := w.client(t, "orphan", nil)
	for org, want := range map[uuid.UUID]string{eventiOrg.ID: "eventiapp", child.ID: "eventiapp", cafettonOrg.ID: "cafettonhouse", orphan.ID: ""} {
		got, err := clientrepository.ResolveClientProduct(org)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestCheckAccessRecursiveEDirectAndInherited(t *testing.T) {
	w := newEventClientWorld(t)
	parent := w.client(t, "parent", &w.root)
	child := w.client(t, "child", &parent)
	owner := w.user(t, "o-"+w.suffix+"@ejemplo.mx")
	editor := w.user(t, "e-"+w.suffix+"@ejemplo.mx")
	w.member(t, parent, owner, "OWNER", true)
	w.member(t, parent, editor, "EDITOR", true)

	allowed, role, err := clientrepository.CheckAccessRecursiveE(owner.ID, child.ID)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.Contains(t, role, "INHERITED_OWNER")
	allowed, _, err = clientrepository.CheckAccessRecursiveE(editor.ID, child.ID)
	require.NoError(t, err)
	assert.False(t, allowed, "only Owner/Admin propagate")
	allowed, _, err = clientrepository.CheckAccessRecursiveE(editor.ID, parent.ID)
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestEventListScopeIncludesGrantsOnlyWhenEnabled(t *testing.T) {
	w := newEventClientWorld(t)
	org := w.client(t, "org", &w.root)
	ownOrg := w.client(t, "own", &w.root)
	manager := w.user(t, "m-"+w.suffix+"@ejemplo.mx")
	client := w.user(t, "c-"+w.suffix+"@ejemplo.mx")
	w.memberApplication(t, w.member(t, ownOrg, client, "EDITOR", true))
	caller := dtos.EventCallerAccess{UserID: client.ID, ApplicationID: w.eventiApp.ID}
	granted := w.event(t, org, "granted")
	notGranted := w.event(t, org, "not-granted")
	own := w.event(t, ownOrg, "own")
	w.grant(t, granted, client, manager)

	query := dtos.EventListQuery{Page: 1, PageSize: 50, Filter: "all", Now: time.Now()}
	eventsrepository.ConfigureEventClientScope(false)
	t.Cleanup(func() { eventsrepository.ConfigureEventClientScope(false) })
	events, total, counts, err := eventsrepository.ListEventPage(nil, &caller, query)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{own.ID}, eventIDs(events))
	assert.Equal(t, 1, total)
	assert.Equal(t, 1, counts.All)

	eventsrepository.ConfigureEventClientScope(true)
	events, total, counts, err = eventsrepository.ListEventPage(nil, &caller, query)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{own.ID, granted.ID}, eventIDs(events))
	assert.NotContains(t, eventIDs(events), notGranted.ID)
	assert.Equal(t, 2, total)
	assert.Equal(t, 2, counts.All)

	all, err := eventsrepository.GetEventsForUser(caller)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{own.ID, granted.ID}, eventIDs(all), "each event once")

	marked, err := w.repo.ActiveGrantedEventIDs(client.ID, []uuid.UUID{own.ID, granted.ID})
	require.NoError(t, err)
	assert.Equal(t, map[uuid.UUID]struct{}{granted.ID: {}}, marked)

	// Revocation removes the row from the list.
	_, err = w.repo.Revoke(nil, granted.ID, client.ID, manager.ID)
	require.NoError(t, err)
	events, _, _, err = eventsrepository.ListEventPage(nil, &caller, query)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{own.ID}, eventIDs(events))
}

func TestEventClientDirectoryQueries(t *testing.T) {
	w := newEventClientWorld(t)
	org := w.client(t, "org", &w.root)
	otherOrg := w.client(t, "other", &w.root)
	manager := w.user(t, "m-"+w.suffix+"@ejemplo.mx")
	client := w.user(t, "Cliente-"+w.suffix+"@Ejemplo.mx")
	outsider := w.user(t, "x-"+w.suffix+"@ejemplo.mx")
	first := w.event(t, org, "primera")
	second := w.event(t, org, "segunda")
	foreign := w.event(t, otherOrg, "ajena")
	w.grant(t, first, client, manager)
	w.grant(t, second, client, manager)
	w.grant(t, foreign, outsider, manager)

	candidates, err := w.repo.ListOrganizationClients(org.ID, first.ID, "", 50)
	require.NoError(t, err)
	require.Len(t, candidates, 1, "only people with grant history in this organization")
	assert.Equal(t, client.ID, candidates[0].UserID)
	assert.Equal(t, 2, candidates[0].AssignedEvents)
	assert.True(t, candidates[0].AssignedToEvent)

	known, err := w.repo.IsOrganizationClient(org.ID, outsider.ID)
	require.NoError(t, err)
	assert.False(t, known)

	rows, total, err := w.repo.ListActiveForOrganization(org.ID, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	require.Len(t, rows, 2)
	assert.Equal(t, client.ID, rows[0].Client.UserID)
	assert.Equal(t, manager.ID, rows[0].GrantedBy.UserID)
	assert.Equal(t, manager.Email, rows[0].GrantedBy.Email)

	assignments, total, err := w.repo.ListOrganizationEventsForClient(org.ID, client.ID, "", 1, 20)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	for _, row := range assignments {
		assert.True(t, row.Assigned)
	}

	owners, err := w.repo.EventOrganizations([]uuid.UUID{first.ID, foreign.ID})
	require.NoError(t, err)
	assert.Equal(t, org.ID, *owners[first.ID])
	assert.Equal(t, otherOrg.ID, *owners[foreign.ID])

	live, err := w.repo.FindLiveUsersByEmail("cliente-"+w.suffix+"@ejemplo.mx", 2)
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, client.ID, live[0].ID)

	child := w.client(t, "child", &org)
	w.member(t, org, outsider, "EDITOR", false)
	member, err := w.repo.HasMembershipInOrganizationTree(child.ID, outsider.ID)
	require.NoError(t, err)
	assert.True(t, member, "an inactive membership in an ancestor counts")

	revokedAll, err := w.repo.RevokeAllForOrganizationUser(nil, org.ID, client.ID, manager.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), revokedAll)
	count, err := w.repo.CountActiveForUser(client.ID)
	require.NoError(t, err)
	assert.Zero(t, count)
}

func eventIDs(events []models.Event) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	return ids
}
