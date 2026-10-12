package clients

import (
	"errors"
	"testing"

	"events-stocks/dtos"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codeCheckingClientRepo adds the optional ClientCodeRepository behaviour.
type codeCheckingClientRepo struct {
	*mockClientRepo
	taken map[string]bool
}

func (r *codeCheckingClientRepo) ClientCodeTaken(code string) (bool, error) {
	return r.taken[code], nil
}

func ownerRoleRepo() *mockClientRoleRepo {
	return &mockClientRoleRepo{GetByCodeFunc: func(string) (*models.ClientRole, error) {
		return &models.ClientRole{ID: uuid.Must(uuid.NewV4()), Code: "Owner"}, nil
	}}
}

// ROLES-103: a sub-organization under a Customer is a hierarchy-limit error
// (400 in the controller), never an opaque failure.
func TestCreateClient_UnderCustomer_IsHierarchyLimit(t *testing.T) {
	parentID := uuid.Must(uuid.NewV4())
	ctr := &mockClientTypeRepo{GetByIDFunc: func(uuid.UUID) (*models.ClientType, error) {
		return &models.ClientType{Code: models.ClientTypeCodeCustomer}, nil
	}}
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "INHERITED_OWNER" },
		GetClientByIDFunc: func(uuid.UUID) (*models.Client, error) {
			return &models.Client{ClientType: models.ClientType{Code: models.ClientTypeCodeCustomer}}, nil
		},
	}
	_, err := newClientService(cr, ownerRoleRepo(), ctr).CreateClient("Nieta", uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), &parentID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrClientHierarchyLimit))
	assert.Contains(t, err.Error(), "customers cannot have sub-clients")
}

func TestCreateClient_RuleErrorsAreTyped(t *testing.T) {
	parentID := uuid.Must(uuid.NewV4())
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "OWNER" },
		GetClientByIDFunc: func(uuid.UUID) (*models.Client, error) {
			return &models.Client{ClientType: models.ClientType{Code: models.ClientTypeCodeAgency}}, nil
		},
	}
	agency := &mockClientTypeRepo{GetByIDFunc: func(uuid.UUID) (*models.ClientType, error) {
		return &models.ClientType{Code: models.ClientTypeCodeAgency}, nil
	}}
	_, err := newClientService(cr, ownerRoleRepo(), agency).CreateClient("X", uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), &parentID)
	assert.True(t, errors.Is(err, ErrClientChildTypeNotAllowed))

	_, err = newClientService(&mockClientRepo{}, ownerRoleRepo(), &mockClientTypeRepo{}).CreateClient("X", uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), nil)
	assert.True(t, errors.Is(err, ErrClientTypeInvalid))

	_, err = newClientService(&mockClientRepo{}, ownerRoleRepo(), agency).CreateClient("X", uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), nil)
	assert.True(t, errors.Is(err, ErrClientRootTypeRequired))

	denied := &mockClientRepo{CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "ADMIN" }}
	_, err = newClientService(denied, ownerRoleRepo(), agency).CreateClient("X", uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), &parentID)
	assert.True(t, errors.Is(err, ErrClientPermissionDenied))
}

// The code avoids existing organization/application codes with a numbered
// suffix instead of failing on the unique index.
func TestCreateClient_CodeGetsNumberedSuffixWhenTaken(t *testing.T) {
	platform := &mockClientTypeRepo{GetByIDFunc: func(uuid.UUID) (*models.ClientType, error) {
		return &models.ClientType{Code: models.ClientTypeCodePlatform}, nil
	}}
	repo := &codeCheckingClientRepo{mockClientRepo: &mockClientRepo{}, taken: map[string]bool{"itbem": true, "itbem-2": true}}
	svc := NewClientService(repo, ownerRoleRepo(), platform, nil, nil, nil)

	client, err := svc.CreateClient("  ITBEM ", uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), nil)
	require.NoError(t, err)
	assert.Equal(t, "itbem-3", client.Code)

	client, err = svc.CreateClient("Boda Andrés", uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), nil)
	require.NoError(t, err)
	assert.Equal(t, "boda-andres", client.Code)
}

func TestCreateClient_UniqueViolationIsCodeConflict(t *testing.T) {
	platform := &mockClientTypeRepo{GetByIDFunc: func(uuid.UUID) (*models.ClientType, error) {
		return &models.ClientType{Code: models.ClientTypeCodePlatform}, nil
	}}
	cr := &mockClientRepo{CreateClientFunc: func(*models.Client) error {
		return errors.New(`ERROR: duplicate key value violates unique constraint "idx_clients_code" (SQLSTATE 23505)`)
	}}
	_, err := newClientService(cr, ownerRoleRepo(), platform).CreateClient("Dup", uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), nil)
	assert.True(t, errors.Is(err, ErrClientCodeConflict))
}

// ROLES-101: renaming changes only the display name; the code is stable.
func TestUpdateClientDetails_RenameKeepsCode(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	var saved *models.Client
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "INHERITED_OWNER" },
		GetClientByIDFunc: func(uuid.UUID) (*models.Client, error) {
			return &models.Client{ID: clientID, Name: "QA roles 1", Code: "qa-roles-1"}, nil
		},
		UpdateClientFunc: func(client *models.Client) error { saved = client; return nil },
	}
	updated, err := newClientService(cr, nil, nil).UpdateClientDetails(clientID, uuid.Must(uuid.NewV4()), "QA roles 1 editada", "", false)
	require.NoError(t, err)
	require.NotNil(t, saved)
	assert.Equal(t, "QA roles 1 editada", updated.Name)
	assert.Equal(t, "qa-roles-1", updated.Code)
	assert.Equal(t, "qa-roles-1", saved.Code)
}

// ROLES-102: an Agency owner is offered "Customer", not "Platform".
func TestCreatableClientTypeCodes(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	agencyOwner := &mockClientRepo{ListClientsPaginatedFunc: func(*uuid.UUID, dtos.ClientsListQuery) ([]models.Client, int64, error) {
		return []models.Client{
			{AccessRole: "Owner", ClientType: models.ClientType{Code: models.ClientTypeCodeAgency}},
			{AccessRole: "INHERITED_OWNER", ClientType: models.ClientType{Code: models.ClientTypeCodeCustomer}},
			{AccessRole: "Admin", ClientType: models.ClientType{Code: models.ClientTypeCodePlatform}},
		}, 3, nil
	}}
	codes, err := newClientService(agencyOwner, nil, nil).CreatableClientTypeCodes(userID, nil, false)
	require.NoError(t, err)
	assert.Equal(t, []string{models.ClientTypeCodeCustomer}, codes)

	codes, err = newClientService(&mockClientRepo{}, nil, nil).CreatableClientTypeCodes(userID, nil, true)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"PLATFORM", "AGENCY", "CUSTOMER"}, codes)

	parentID := uuid.Must(uuid.NewV4())
	withParent := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "INHERITED_OWNER" },
		GetClientByIDFunc: func(uuid.UUID) (*models.Client, error) {
			return &models.Client{ClientType: models.ClientType{Code: models.ClientTypeCodeCustomer}}, nil
		},
	}
	codes, err = newClientService(withParent, nil, nil).CreatableClientTypeCodes(userID, &parentID, false)
	require.NoError(t, err)
	assert.Empty(t, codes)

	adminOnly := &mockClientRepo{CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "ADMIN" }}
	_, err = newClientService(adminOnly, nil, nil).CreatableClientTypeCodes(userID, &parentID, false)
	assert.True(t, errors.Is(err, ErrClientPermissionDenied))
}
