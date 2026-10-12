package clients

import (
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reachClientRepo adds the application reach (ports.ApplicationClientReach).
type reachClientRepo struct {
	*mockClientRepo
	reach map[uuid.UUID]bool
	apps  []string
}

func (r *reachClientRepo) ReachableClientIDsInApplication(_ uuid.UUID, app string, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	r.apps = append(r.apps, app)
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		if r.reach[id] {
			out[id] = true
		}
	}
	return out, nil
}

func TestApplicationScopedServiceStopsAtOtherProductPortals(t *testing.T) {
	ownerID := uuid.Must(uuid.NewV4())
	eventiChild := uuid.Must(uuid.NewV4())
	itbemPortal := uuid.Must(uuid.NewV4())
	repo := &reachClientRepo{
		mockClientRepo: &mockClientRepo{
			CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "INHERITED_OWNER" },
			GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
				return &models.Client{ID: id, Name: "Org"}, nil
			},
			GetMembersFunc: func(uuid.UUID) ([]models.ClientMember, error) { return []models.ClientMember{{}}, nil },
			GetMemberRoleFunc: func(uuid.UUID, uuid.UUID) (*models.ClientRole, error) {
				return nil, assert.AnError
			},
		},
		reach: map[uuid.UUID]bool{eventiChild: true},
	}
	base := NewClientService(repo, &mockClientRoleRepo{}, &mockClientTypeRepo{}, nil, nil, nil)
	scoped := base.WithApplicationScope(" EventiApp ")

	// In-product inheritance still works.
	_, err := scoped.GetClientDetails(eventiChild, ownerID)
	require.NoError(t, err)
	require.NoError(t, scoped.CanManageClientMembers(eventiChild, ownerID))
	assert.Equal(t, "eventiapp", repo.apps[0])

	// Another product's portal org is out of reach: no details, no members.
	_, err = scoped.GetClientDetails(itbemPortal, ownerID)
	assert.Error(t, err)
	assert.Error(t, scoped.CanManageClientMembers(itbemPortal, ownerID))
	_, err = scoped.ListClientMembers(itbemPortal, ownerID)
	assert.Error(t, err)
	_, err = scoped.GetEffectiveMemberRole(ownerID, itbemPortal)
	assert.Error(t, err)
	assert.Error(t, scoped.RemoveClientMember(itbemPortal, ownerID, uuid.Must(uuid.NewV4())))

	// The unscoped service (internal callers) keeps the old behaviour.
	_, err = base.GetClientDetails(itbemPortal, ownerID)
	assert.NoError(t, err)
}

func TestApplicationScopedServiceFailsClosedWithoutReach(t *testing.T) {
	repo := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "OWNER" },
		GetClientByIDFunc:        func(id uuid.UUID) (*models.Client, error) { return &models.Client{ID: id}, nil },
	}
	scoped := NewClientService(repo, &mockClientRoleRepo{}, &mockClientTypeRepo{}, nil, nil, nil).WithApplicationScope("eventiapp")
	_, err := scoped.GetClientDetails(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()))
	assert.Error(t, err)
}
