package clients

import (
	"bytes"
	"context"
	"errors"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"
	resourcesService "events-stocks/services/resources"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/textproto"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type clientTestMultipartFile struct{ *bytes.Reader }

func (f *clientTestMultipartFile) Close() error { return nil }

type clientTestLogoStorage struct {
	uploadErr error
	deleted   []string
}

func (s *clientTestLogoStorage) FileExists(string, string, string, string) (bool, string, error) {
	return true, "", nil
}
func (s *clientTestLogoStorage) GetPresignedFileURL(filename, folder, _, _ string, _ int) (string, error) {
	return "https://signed.example/" + folder + "/" + filename, nil
}
func (s *clientTestLogoStorage) GetPresignedPutURL(string, string, string, string, int) (string, error) {
	return "", nil
}
func (s *clientTestLogoStorage) CreateMultipartUpload(string, string, string, string) (string, error) {
	return "", nil
}
func (s *clientTestLogoStorage) GetPresignedUploadPartURL(string, string, string, string, int, int) (string, error) {
	return "", nil
}
func (s *clientTestLogoStorage) CompleteMultipartUpload(string, string, string, string, []dtos.CompletedUploadPart) error {
	return nil
}
func (s *clientTestLogoStorage) AbortMultipartUpload(string, string, string, string) error {
	return nil
}
func (s *clientTestLogoStorage) UpdateFile([]byte, string, string, string, string, string) (string, error) {
	return "", nil
}
func (s *clientTestLogoStorage) UploadRawBytesSimple([]byte, string, string, string, string, string) error {
	return s.uploadErr
}
func (s *clientTestLogoStorage) DeleteFile(filename, folder, _, _ string) error {
	s.deleted = append(s.deleted, folder+"/"+filename)
	return nil
}
func (s *clientTestLogoStorage) GetFileStream(string, string, string, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

var _ ports.ObjectStorageRepository = (*clientTestLogoStorage)(nil)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

type mockClientRepo struct {
	CreateClientFunc         func(client *models.Client) error
	GetClientByIDFunc        func(id uuid.UUID) (*models.Client, error)
	UpdateClientFunc         func(client *models.Client) error
	DeleteClientFunc         func(id uuid.UUID) error
	GetAllClientsFunc        func() ([]models.Client, error)
	ListClientsPaginatedFunc func(userID *uuid.UUID, query dtos.ClientsListQuery) ([]models.Client, int64, error)
	GetClientsByUserFunc     func(userID uuid.UUID) ([]models.Client, error)
	GetChildrenClientsFunc   func(parentID uuid.UUID) ([]models.Client, error)
	CheckAccessRecursiveFunc func(userID, targetClientID uuid.UUID) (bool, string)
	IsMemberFunc             func(userID, clientID uuid.UUID) (bool, string)
	AddMemberFunc            func(member *models.ClientMember) error
	RemoveMemberFunc         func(clientID, userID uuid.UUID) error
	UpdateMemberRoleFunc     func(clientID, userID, newRoleID uuid.UUID) error
	GetMemberRoleFunc        func(clientID, userID uuid.UUID) (*models.ClientRole, error)
	GetMembersFunc           func(clientID uuid.UUID) ([]models.ClientMember, error)
	DeleteAllMembersFunc     func(clientID uuid.UUID) error
	ListClientsByUserFunc    func(userID uuid.UUID) ([]models.Client, error)
	CountClientsByUsersFunc  func(userIDs []uuid.UUID) (map[uuid.UUID]int64, error)
}

func (m *mockClientRepo) CreateClient(client *models.Client) error {
	if m.CreateClientFunc != nil {
		return m.CreateClientFunc(client)
	}
	return nil
}
func (m *mockClientRepo) GetClientByID(id uuid.UUID) (*models.Client, error) {
	if m.GetClientByIDFunc != nil {
		return m.GetClientByIDFunc(id)
	}
	return &models.Client{}, nil
}
func (m *mockClientRepo) UpdateClient(client *models.Client) error {
	if m.UpdateClientFunc != nil {
		return m.UpdateClientFunc(client)
	}
	return nil
}
func (m *mockClientRepo) DeleteClient(id uuid.UUID) error {
	if m.DeleteClientFunc != nil {
		return m.DeleteClientFunc(id)
	}
	return nil
}
func (m *mockClientRepo) GetAllClients() ([]models.Client, error) {
	if m.GetAllClientsFunc != nil {
		return m.GetAllClientsFunc()
	}
	return nil, nil
}
func (m *mockClientRepo) ListClientsPaginated(userID *uuid.UUID, query dtos.ClientsListQuery) ([]models.Client, int64, error) {
	if m.ListClientsPaginatedFunc != nil {
		return m.ListClientsPaginatedFunc(userID, query)
	}
	return nil, 0, nil
}
func (m *mockClientRepo) GetClientsByUser(userID uuid.UUID) ([]models.Client, error) {
	if m.GetClientsByUserFunc != nil {
		return m.GetClientsByUserFunc(userID)
	}
	return nil, nil
}
func (m *mockClientRepo) GetChildrenClients(parentID uuid.UUID) ([]models.Client, error) {
	if m.GetChildrenClientsFunc != nil {
		return m.GetChildrenClientsFunc(parentID)
	}
	return nil, nil
}
func (m *mockClientRepo) CheckAccessRecursive(userID, targetClientID uuid.UUID) (bool, string) {
	if m.CheckAccessRecursiveFunc != nil {
		return m.CheckAccessRecursiveFunc(userID, targetClientID)
	}
	return false, ""
}
func (m *mockClientRepo) IsMember(userID, clientID uuid.UUID) (bool, string) {
	if m.IsMemberFunc != nil {
		return m.IsMemberFunc(userID, clientID)
	}
	return false, ""
}
func (m *mockClientRepo) AddMember(member *models.ClientMember) error {
	if m.AddMemberFunc != nil {
		return m.AddMemberFunc(member)
	}
	return nil
}
func (m *mockClientRepo) RemoveMember(clientID, userID uuid.UUID) error {
	if m.RemoveMemberFunc != nil {
		return m.RemoveMemberFunc(clientID, userID)
	}
	return nil
}
func (m *mockClientRepo) UpdateMemberRole(clientID, userID, newRoleID uuid.UUID) error {
	if m.UpdateMemberRoleFunc != nil {
		return m.UpdateMemberRoleFunc(clientID, userID, newRoleID)
	}
	return nil
}
func (m *mockClientRepo) GetMemberRole(clientID, userID uuid.UUID) (*models.ClientRole, error) {
	if m.GetMemberRoleFunc != nil {
		return m.GetMemberRoleFunc(clientID, userID)
	}
	return nil, errors.New("not found")
}
func (m *mockClientRepo) GetMembers(clientID uuid.UUID) ([]models.ClientMember, error) {
	if m.GetMembersFunc != nil {
		return m.GetMembersFunc(clientID)
	}
	return nil, nil
}
func (m *mockClientRepo) DeleteAllMembers(clientID uuid.UUID) error {
	if m.DeleteAllMembersFunc != nil {
		return m.DeleteAllMembersFunc(clientID)
	}
	return nil
}
func (m *mockClientRepo) ListClientsByUser(userID uuid.UUID) ([]models.Client, error) {
	if m.ListClientsByUserFunc != nil {
		return m.ListClientsByUserFunc(userID)
	}
	return nil, nil
}
func (m *mockClientRepo) CountClientsByUsers(userIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	if m.CountClientsByUsersFunc != nil {
		return m.CountClientsByUsersFunc(userIDs)
	}
	return map[uuid.UUID]int64{}, nil
}

var _ ports.ClientRepository = (*mockClientRepo)(nil)

// ---------------------------------------------------------------------------

type mockClientRoleRepo struct {
	GetByCodeFunc          func(code string) (*models.ClientRole, error)
	GetByIDFunc            func(id uuid.UUID) (*models.ClientRole, error)
	GetAssignableRolesFunc func(myHierarchyLevel int) ([]models.ClientRole, error)
}

func (m *mockClientRoleRepo) GetByCode(code string) (*models.ClientRole, error) {
	if m.GetByCodeFunc != nil {
		return m.GetByCodeFunc(code)
	}
	return nil, errors.New("not found")
}
func (m *mockClientRoleRepo) GetByID(id uuid.UUID) (*models.ClientRole, error) {
	if m.GetByIDFunc != nil {
		return m.GetByIDFunc(id)
	}
	return nil, errors.New("not found")
}
func (m *mockClientRoleRepo) GetAssignableRoles(myHierarchyLevel int) ([]models.ClientRole, error) {
	if m.GetAssignableRolesFunc != nil {
		return m.GetAssignableRolesFunc(myHierarchyLevel)
	}
	return nil, nil
}

var _ ports.ClientRoleRepository = (*mockClientRoleRepo)(nil)

// ---------------------------------------------------------------------------

type mockClientTypeRepo struct {
	GetByIDFunc       func(id uuid.UUID) (*models.ClientType, error)
	GetByCodeFunc     func(code string) (*models.ClientType, error)
	GetChildTypesFunc func(parentLevel int) ([]models.ClientType, error)
	GetRootTypeFunc   func() ([]models.ClientType, error)
}

func (m *mockClientTypeRepo) GetByID(id uuid.UUID) (*models.ClientType, error) {
	if m.GetByIDFunc != nil {
		return m.GetByIDFunc(id)
	}
	return nil, errors.New("not found")
}
func (m *mockClientTypeRepo) GetByCode(code string) (*models.ClientType, error) {
	if m.GetByCodeFunc != nil {
		return m.GetByCodeFunc(code)
	}
	return nil, errors.New("not found")
}
func (m *mockClientTypeRepo) GetChildTypes(parentLevel int) ([]models.ClientType, error) {
	if m.GetChildTypesFunc != nil {
		return m.GetChildTypesFunc(parentLevel)
	}
	return nil, nil
}
func (m *mockClientTypeRepo) GetRootType() ([]models.ClientType, error) {
	if m.GetRootTypeFunc != nil {
		return m.GetRootTypeFunc()
	}
	return nil, nil
}

var _ ports.ClientTypeRepository = (*mockClientTypeRepo)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newClientService(cr *mockClientRepo, crr *mockClientRoleRepo, ctr *mockClientTypeRepo) *ClientService {
	if cr == nil {
		cr = &mockClientRepo{}
	}
	if crr == nil {
		crr = &mockClientRoleRepo{}
	}
	if ctr == nil {
		ctr = &mockClientTypeRepo{}
	}
	// rs, cache, and tx are nil — safe for unit tests (no S3, Redis, or transaction calls)
	return NewClientService(cr, crr, ctr, nil, nil, nil)
}

// ownerRole returns a ClientRole with Hierarchy = 1 (most powerful).
func ownerRole() *models.ClientRole {
	return &models.ClientRole{ID: uuid.Must(uuid.NewV4()), Code: "Owner", Hierarchy: 1}
}

// adminRole returns a ClientRole with Hierarchy = 2.
func adminRole() *models.ClientRole {
	return &models.ClientRole{ID: uuid.Must(uuid.NewV4()), Code: "Admin", Hierarchy: 2}
}

// memberRole returns a ClientRole with Hierarchy = 3 (least powerful).
func memberRole() *models.ClientRole {
	return &models.ClientRole{ID: uuid.Must(uuid.NewV4()), Code: "Member", Hierarchy: 3}
}

// ---------------------------------------------------------------------------
// CreateClient tests
// ---------------------------------------------------------------------------

func TestCreateClient_RootMustBePlatform(t *testing.T) {
	typeID := uuid.Must(uuid.NewV4())
	ctr := &mockClientTypeRepo{
		GetByIDFunc: func(id uuid.UUID) (*models.ClientType, error) {
			return &models.ClientType{Code: "AGENCY"}, nil // not PLATFORM
		},
	}

	svc := newClientService(nil, nil, ctr)
	client, err := svc.CreateClient("My Org", typeID, uuid.Must(uuid.NewV4()), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "root clients must be of type PLATFORM")
	assert.Nil(t, client)
}

func TestCreateClient_Root_InvalidType(t *testing.T) {
	typeID := uuid.Must(uuid.NewV4())
	ctr := &mockClientTypeRepo{
		GetByIDFunc: func(id uuid.UUID) (*models.ClientType, error) {
			return nil, errors.New("type not found")
		},
	}

	svc := newClientService(nil, nil, ctr)
	client, err := svc.CreateClient("Bad Org", typeID, uuid.Must(uuid.NewV4()), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid client type id")
	assert.Nil(t, client)
}

func TestCreateClient_Root_Platform_Success(t *testing.T) {
	typeID := uuid.Must(uuid.NewV4())
	ownerUserID := uuid.Must(uuid.NewV4())
	ownerRoleID := uuid.Must(uuid.NewV4())

	ctr := &mockClientTypeRepo{
		GetByIDFunc: func(id uuid.UUID) (*models.ClientType, error) {
			return &models.ClientType{Code: "PLATFORM"}, nil
		},
	}
	var capturedMember *models.ClientMember
	cr := &mockClientRepo{
		CreateClientFunc: func(client *models.Client) error { return nil },
		AddMemberFunc: func(member *models.ClientMember) error {
			capturedMember = member
			return nil
		},
	}
	crr := &mockClientRoleRepo{
		GetByCodeFunc: func(code string) (*models.ClientRole, error) {
			return &models.ClientRole{ID: ownerRoleID, Code: "Owner"}, nil
		},
	}

	svc := newClientService(cr, crr, ctr)
	client, err := svc.CreateClient("My Platform", typeID, ownerUserID, nil)

	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, "My Platform", client.Name)
	assert.Equal(t, "my-platform", client.Code)
	assert.NotEqual(t, uuid.Nil, client.ID)
	require.NotNil(t, capturedMember)
	assert.Equal(t, client.ID, capturedMember.ClientID)
	assert.Equal(t, ownerUserID, capturedMember.UserID)
	assert.Equal(t, ownerRoleID, capturedMember.ClientRoleID)
}

func TestCreateClient_WithParent_RequiresOwnerAccess(t *testing.T) {
	typeID := uuid.Must(uuid.NewV4())
	parentID := uuid.Must(uuid.NewV4())
	ownerUserID := uuid.Must(uuid.NewV4())

	ctr := &mockClientTypeRepo{
		GetByIDFunc: func(id uuid.UUID) (*models.ClientType, error) {
			return &models.ClientType{Code: "AGENCY"}, nil
		},
	}
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(userID, targetClientID uuid.UUID) (bool, string) {
			return true, "ADMIN" // ADMIN, not OWNER
		},
	}

	svc := newClientService(cr, nil, ctr)
	client, err := svc.CreateClient("Sub Org", typeID, ownerUserID, &parentID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner access required")
	assert.Nil(t, client)
}

func TestCreateClient_WithParent_AccessDenied(t *testing.T) {
	typeID := uuid.Must(uuid.NewV4())
	parentID := uuid.Must(uuid.NewV4())

	ctr := &mockClientTypeRepo{
		GetByIDFunc: func(id uuid.UUID) (*models.ClientType, error) {
			return &models.ClientType{Code: "CUSTOMER"}, nil
		},
	}
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(userID, targetClientID uuid.UUID) (bool, string) {
			return false, "" // no access
		},
	}

	svc := newClientService(cr, nil, ctr)
	client, err := svc.CreateClient("Sub Org", typeID, uuid.Must(uuid.NewV4()), &parentID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner access required")
	assert.Nil(t, client)
}

func TestCreateClient_AgencyParent_CanOnlyCreateCustomer(t *testing.T) {
	typeID := uuid.Must(uuid.NewV4())
	parentID := uuid.Must(uuid.NewV4())

	ctr := &mockClientTypeRepo{
		GetByIDFunc: func(id uuid.UUID) (*models.ClientType, error) {
			// Requested type is not CUSTOMER
			return &models.ClientType{Code: "AGENCY"}, nil
		},
	}
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(userID, targetClientID uuid.UUID) (bool, string) {
			return true, "OWNER"
		},
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{
				ClientType: models.ClientType{Code: "AGENCY"},
			}, nil
		},
	}

	svc := newClientService(cr, nil, ctr)
	client, err := svc.CreateClient("New Org", typeID, uuid.Must(uuid.NewV4()), &parentID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "agencies can only create customers")
	assert.Nil(t, client)
}

func TestCreateClient_PlatformParent_CanOnlyCreateAgency(t *testing.T) {
	typeID := uuid.Must(uuid.NewV4())
	parentID := uuid.Must(uuid.NewV4())

	ctr := &mockClientTypeRepo{
		GetByIDFunc: func(id uuid.UUID) (*models.ClientType, error) {
			return &models.ClientType{Code: "CUSTOMER"}, nil
		},
	}
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(userID, targetClientID uuid.UUID) (bool, string) {
			return true, "OWNER"
		},
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{ClientType: models.ClientType{Code: "PLATFORM"}}, nil
		},
	}

	svc := newClientService(cr, nil, ctr)
	client, err := svc.CreateClient("Invalid child", typeID, uuid.Must(uuid.NewV4()), &parentID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "platforms can only create agencies")
	assert.Nil(t, client)
}

func TestCreateClient_PrimaryRootCanProvisionAgencyWithoutParentMembership(t *testing.T) {
	typeID := uuid.Must(uuid.NewV4())
	parentID := uuid.Must(uuid.NewV4())
	rootUserID := uuid.Must(uuid.NewV4())
	ownerRoleID := uuid.Must(uuid.NewV4())

	ctr := &mockClientTypeRepo{
		GetByIDFunc: func(id uuid.UUID) (*models.ClientType, error) {
			return &models.ClientType{Code: "AGENCY"}, nil
		},
	}
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(userID, targetClientID uuid.UUID) (bool, string) {
			return false, ""
		},
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{ClientType: models.ClientType{Code: "PLATFORM"}}, nil
		},
		CreateClientFunc: func(client *models.Client) error { return nil },
		AddMemberFunc:    func(member *models.ClientMember) error { return nil },
	}
	crr := &mockClientRoleRepo{
		GetByCodeFunc: func(code string) (*models.ClientRole, error) {
			return &models.ClientRole{ID: ownerRoleID, Code: "Owner"}, nil
		},
	}

	svc := newClientService(cr, crr, ctr)
	client, err := svc.CreateClientAsPrimaryRoot("Nueva agencia", typeID, rootUserID, &parentID)

	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, parentID, *client.ParentID)
	assert.Equal(t, typeID, client.ClientTypeID)
}

func TestCreateClient_CustomerParent_CannotHaveSubClients(t *testing.T) {
	typeID := uuid.Must(uuid.NewV4())
	parentID := uuid.Must(uuid.NewV4())

	ctr := &mockClientTypeRepo{
		GetByIDFunc: func(id uuid.UUID) (*models.ClientType, error) {
			return &models.ClientType{Code: "CUSTOMER"}, nil
		},
	}
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(userID, targetClientID uuid.UUID) (bool, string) {
			return true, "OWNER"
		},
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{
				ClientType: models.ClientType{Code: "CUSTOMER"},
			}, nil
		},
	}

	svc := newClientService(cr, nil, ctr)
	client, err := svc.CreateClient("Sub Org", typeID, uuid.Must(uuid.NewV4()), &parentID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "customers cannot have sub-clients")
	assert.Nil(t, client)
}

// ---------------------------------------------------------------------------
// AddUserToClient tests
// ---------------------------------------------------------------------------

func TestAddUserToClient_AlreadyMember(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	roleID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		IsMemberFunc: func(uID, cID uuid.UUID) (bool, string) {
			return true, "ADMIN"
		},
	}

	svc := newClientService(cr, nil, nil)
	err := svc.AddUserToClient(clientID, userID, roleID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already a member")
}

func TestAddUserToClient_Success(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	roleID := uuid.Must(uuid.NewV4())

	var capturedMember *models.ClientMember
	cr := &mockClientRepo{
		IsMemberFunc: func(uID, cID uuid.UUID) (bool, string) {
			return false, ""
		},
		AddMemberFunc: func(member *models.ClientMember) error {
			capturedMember = member
			return nil
		},
	}

	svc := newClientService(cr, nil, nil)
	err := svc.AddUserToClient(clientID, userID, roleID)

	require.NoError(t, err)
	require.NotNil(t, capturedMember)
	assert.Equal(t, clientID, capturedMember.ClientID)
	assert.Equal(t, userID, capturedMember.UserID)
	assert.Equal(t, roleID, capturedMember.ClientRoleID)
	assert.True(t, capturedMember.IsActive)
}

func TestCanAssignClientRole_OwnerCanAssignAdmin(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	requesterID := uuid.Must(uuid.NewV4())
	adminRoleID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(userID, targetClientID uuid.UUID) (bool, string) {
			return true, "Owner"
		},
	}
	crr := &mockClientRoleRepo{
		GetByCodeFunc: func(code string) (*models.ClientRole, error) {
			assert.Equal(t, "OWNER", code)
			return ownerRole(), nil
		},
		GetByIDFunc: func(id uuid.UUID) (*models.ClientRole, error) {
			assert.Equal(t, adminRoleID, id)
			return adminRole(), nil
		},
	}

	svc := newClientService(cr, crr, nil)
	err := svc.CanAssignClientRole(clientID, requesterID, adminRoleID)

	require.NoError(t, err)
}

func TestCanAssignClientRole_InheritedOwnerCanAssignAdmin(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	requesterID := uuid.Must(uuid.NewV4())
	adminRoleID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(userID, targetClientID uuid.UUID) (bool, string) {
			return true, "INHERITED_OWNER"
		},
	}
	crr := &mockClientRoleRepo{
		GetByCodeFunc: func(code string) (*models.ClientRole, error) {
			assert.Equal(t, "OWNER", code)
			return ownerRole(), nil
		},
		GetByIDFunc: func(id uuid.UUID) (*models.ClientRole, error) {
			assert.Equal(t, adminRoleID, id)
			return adminRole(), nil
		},
	}

	svc := newClientService(cr, crr, nil)
	err := svc.CanAssignClientRole(clientID, requesterID, adminRoleID)

	require.NoError(t, err)
}

func TestCanAssignClientRole_AdminCannotAssignAdmin(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	requesterID := uuid.Must(uuid.NewV4())
	adminRoleID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(userID, targetClientID uuid.UUID) (bool, string) {
			return true, "Admin"
		},
	}
	crr := &mockClientRoleRepo{
		GetByCodeFunc: func(code string) (*models.ClientRole, error) {
			assert.Equal(t, "ADMIN", code)
			return adminRole(), nil
		},
		GetByIDFunc: func(id uuid.UUID) (*models.ClientRole, error) {
			assert.Equal(t, adminRoleID, id)
			return adminRole(), nil
		},
	}

	svc := newClientService(cr, crr, nil)
	err := svc.CanAssignClientRole(clientID, requesterID, adminRoleID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "equal or higher")
}

func TestCanAssignClientRoleAsRoot_RejectsOwnerAndAllowsLowerRoles(t *testing.T) {
	ownerRoleID := uuid.Must(uuid.NewV4())
	memberRoleID := uuid.Must(uuid.NewV4())
	crr := &mockClientRoleRepo{
		GetByIDFunc: func(id uuid.UUID) (*models.ClientRole, error) {
			if id == ownerRoleID {
				return ownerRole(), nil
			}
			return memberRole(), nil
		},
	}

	svc := newClientService(nil, crr, nil)
	require.Error(t, svc.CanAssignClientRoleAsRoot(ownerRoleID))
	require.NoError(t, svc.CanAssignClientRoleAsRoot(memberRoleID))
}

// ---------------------------------------------------------------------------
// RemoveClientMember tests
// ---------------------------------------------------------------------------

func TestRemoveClientMember_CannotRemoveSelf(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())

	svc := newClientService(nil, nil, nil)
	err := svc.RemoveClientMember(clientID, userID, userID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot remove yourself")
}

func TestRemoveClientMember_RequesterNotMember(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	requesterID := uuid.Must(uuid.NewV4())
	targetID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		GetMemberRoleFunc: func(cID, uID uuid.UUID) (*models.ClientRole, error) {
			if uID == requesterID {
				return nil, errors.New("not found") // requester is not a member
			}
			return adminRole(), nil
		},
	}

	svc := newClientService(cr, nil, nil)
	err := svc.RemoveClientMember(clientID, requesterID, targetID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requester is not a member")
}

func TestRemoveClientMember_TargetNotMember(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	requesterID := uuid.Must(uuid.NewV4())
	targetID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		GetMemberRoleFunc: func(cID, uID uuid.UUID) (*models.ClientRole, error) {
			if uID == requesterID {
				return ownerRole(), nil
			}
			return nil, errors.New("not found") // target is not a member
		},
	}

	svc := newClientService(cr, nil, nil)
	err := svc.RemoveClientMember(clientID, requesterID, targetID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "target user is not a member")
}

func TestRemoveClientMember_CannotRemoveHigherHierarchy(t *testing.T) {
	// Admin (hierarchy=2) tries to remove Owner (hierarchy=1)
	// myRole.Hierarchy(2) >= targetRole.Hierarchy(1) → denied
	clientID := uuid.Must(uuid.NewV4())
	requesterID := uuid.Must(uuid.NewV4())
	targetID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		GetMemberRoleFunc: func(cID, uID uuid.UUID) (*models.ClientRole, error) {
			if uID == requesterID {
				return adminRole(), nil // hierarchy 2
			}
			return ownerRole(), nil // hierarchy 1 — more powerful
		},
	}

	svc := newClientService(cr, nil, nil)
	err := svc.RemoveClientMember(clientID, requesterID, targetID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestRemoveClientMember_CannotRemoveEqualHierarchy(t *testing.T) {
	// Admin (hierarchy=2) tries to remove another Admin (hierarchy=2)
	clientID := uuid.Must(uuid.NewV4())
	requesterID := uuid.Must(uuid.NewV4())
	targetID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		GetMemberRoleFunc: func(cID, uID uuid.UUID) (*models.ClientRole, error) {
			return adminRole(), nil // both are Admin (hierarchy 2)
		},
	}

	svc := newClientService(cr, nil, nil)
	err := svc.RemoveClientMember(clientID, requesterID, targetID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestRemoveClientMember_Success(t *testing.T) {
	// Owner (hierarchy=1) removes Admin (hierarchy=2)
	clientID := uuid.Must(uuid.NewV4())
	requesterID := uuid.Must(uuid.NewV4())
	targetID := uuid.Must(uuid.NewV4())

	var removedUserID uuid.UUID
	cr := &mockClientRepo{
		GetMemberRoleFunc: func(cID, uID uuid.UUID) (*models.ClientRole, error) {
			if uID == requesterID {
				return ownerRole(), nil // hierarchy 1
			}
			return adminRole(), nil // hierarchy 2
		},
		RemoveMemberFunc: func(cID, uID uuid.UUID) error {
			removedUserID = uID
			return nil
		},
	}

	svc := newClientService(cr, nil, nil)
	err := svc.RemoveClientMember(clientID, requesterID, targetID)

	require.NoError(t, err)
	assert.Equal(t, targetID, removedUserID)
}

func TestRemoveClientMember_AllowsInheritedOwnerForChildOrganization(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	requesterID := uuid.Must(uuid.NewV4())
	targetID := uuid.Must(uuid.NewV4())
	removed := uuid.Nil
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(userID, targetClientID uuid.UUID) (bool, string) {
			assert.Equal(t, requesterID, userID)
			assert.Equal(t, clientID, targetClientID)
			return true, "INHERITED_OWNER"
		},
		GetMemberRoleFunc: func(_ uuid.UUID, userID uuid.UUID) (*models.ClientRole, error) {
			assert.Equal(t, targetID, userID)
			return adminRole(), nil
		},
		RemoveMemberFunc: func(_ uuid.UUID, userID uuid.UUID) error {
			removed = userID
			return nil
		},
	}

	err := newClientService(cr, nil, nil).RemoveClientMember(clientID, requesterID, targetID)
	require.NoError(t, err)
	assert.Equal(t, targetID, removed)
}

func TestRemoveClientMemberAsRoot_ProtectsOwnerAndRemovesMember(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	ownerID := uuid.Must(uuid.NewV4())
	memberID := uuid.Must(uuid.NewV4())
	removed := uuid.Nil
	cr := &mockClientRepo{
		GetMemberRoleFunc: func(_ uuid.UUID, userID uuid.UUID) (*models.ClientRole, error) {
			if userID == ownerID {
				return ownerRole(), nil
			}
			return memberRole(), nil
		},
		RemoveMemberFunc: func(_ uuid.UUID, userID uuid.UUID) error {
			removed = userID
			return nil
		},
	}

	svc := newClientService(cr, nil, nil)
	require.Error(t, svc.RemoveClientMemberAsRoot(clientID, ownerID))
	require.NoError(t, svc.RemoveClientMemberAsRoot(clientID, memberID))
	assert.Equal(t, memberID, removed)
}

// ---------------------------------------------------------------------------
// UpdateClientMemberRole tests
// ---------------------------------------------------------------------------

func TestUpdateClientMemberRole_RequesterNotMember(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	requesterID := uuid.Must(uuid.NewV4())
	targetID := uuid.Must(uuid.NewV4())
	newRoleID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		GetMemberRoleFunc: func(cID, uID uuid.UUID) (*models.ClientRole, error) {
			if uID == requesterID {
				return nil, errors.New("not found")
			}
			return memberRole(), nil
		},
	}

	svc := newClientService(cr, nil, nil)
	err := svc.UpdateClientMemberRole(clientID, requesterID, targetID, newRoleID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requester is not a member")
}

func TestUpdateClientMemberRole_CannotModifyHigherHierarchy(t *testing.T) {
	// Admin (hierarchy=2) tries to change role of Owner (hierarchy=1)
	clientID := uuid.Must(uuid.NewV4())
	requesterID := uuid.Must(uuid.NewV4())
	targetID := uuid.Must(uuid.NewV4())
	newRoleID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		GetMemberRoleFunc: func(cID, uID uuid.UUID) (*models.ClientRole, error) {
			if uID == requesterID {
				return adminRole(), nil // hierarchy 2
			}
			return ownerRole(), nil // hierarchy 1 — cannot be modified
		},
	}

	svc := newClientService(cr, nil, nil)
	err := svc.UpdateClientMemberRole(clientID, requesterID, targetID, newRoleID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestUpdateClientMemberRole_CannotGrantRoleAboveOwn(t *testing.T) {
	// Admin (hierarchy=2) wants to promote Member to Owner (hierarchy=1)
	// myRole.Hierarchy(2) >= newRole.Hierarchy(1) → denied
	clientID := uuid.Must(uuid.NewV4())
	requesterID := uuid.Must(uuid.NewV4())
	targetID := uuid.Must(uuid.NewV4())
	ownerRoleID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		GetMemberRoleFunc: func(cID, uID uuid.UUID) (*models.ClientRole, error) {
			if uID == requesterID {
				return adminRole(), nil // hierarchy 2
			}
			return memberRole(), nil // hierarchy 3 — can be modified by Admin
		},
	}
	crr := &mockClientRoleRepo{
		GetByIDFunc: func(id uuid.UUID) (*models.ClientRole, error) {
			return &models.ClientRole{ID: id, Code: "Owner", Hierarchy: 1}, nil // trying to grant Owner
		},
	}

	svc := newClientService(cr, crr, nil)
	err := svc.UpdateClientMemberRole(clientID, requesterID, targetID, ownerRoleID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot grant a role equal or higher than your own")
}

func TestUpdateClientMemberRole_Success(t *testing.T) {
	// Owner (hierarchy=1) promotes Member (hierarchy=3) to Admin (hierarchy=2)
	clientID := uuid.Must(uuid.NewV4())
	requesterID := uuid.Must(uuid.NewV4())
	targetID := uuid.Must(uuid.NewV4())
	adminRoleID := uuid.Must(uuid.NewV4())

	var updatedRoleID uuid.UUID
	cr := &mockClientRepo{
		GetMemberRoleFunc: func(cID, uID uuid.UUID) (*models.ClientRole, error) {
			if uID == requesterID {
				return ownerRole(), nil // hierarchy 1
			}
			return memberRole(), nil // hierarchy 3
		},
		UpdateMemberRoleFunc: func(cID, uID, rID uuid.UUID) error {
			updatedRoleID = rID
			return nil
		},
	}
	crr := &mockClientRoleRepo{
		GetByIDFunc: func(id uuid.UUID) (*models.ClientRole, error) {
			return &models.ClientRole{ID: id, Code: "Admin", Hierarchy: 2}, nil
		},
	}

	svc := newClientService(cr, crr, nil)
	err := svc.UpdateClientMemberRole(clientID, requesterID, targetID, adminRoleID)

	require.NoError(t, err)
	assert.Equal(t, adminRoleID, updatedRoleID)
}

func TestUpdateClientMemberRoleAsRoot_ProtectsOwnerAndUpdatesMember(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	ownerID := uuid.Must(uuid.NewV4())
	memberID := uuid.Must(uuid.NewV4())
	adminRoleID := uuid.Must(uuid.NewV4())
	updated := uuid.Nil
	cr := &mockClientRepo{
		GetMemberRoleFunc: func(_ uuid.UUID, userID uuid.UUID) (*models.ClientRole, error) {
			if userID == ownerID {
				return ownerRole(), nil
			}
			return memberRole(), nil
		},
		UpdateMemberRoleFunc: func(_ uuid.UUID, _ uuid.UUID, roleID uuid.UUID) error {
			updated = roleID
			return nil
		},
	}
	crr := &mockClientRoleRepo{GetByIDFunc: func(uuid.UUID) (*models.ClientRole, error) { return adminRole(), nil }}

	svc := newClientService(cr, crr, nil)
	require.Error(t, svc.UpdateClientMemberRoleAsRoot(clientID, ownerID, adminRoleID))
	require.NoError(t, svc.UpdateClientMemberRoleAsRoot(clientID, memberID, adminRoleID))
	assert.Equal(t, adminRoleID, updated)
}

// ---------------------------------------------------------------------------
// GetClientDetails tests
// ---------------------------------------------------------------------------

func TestGetClientDetails_AccessDenied(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return false, ""
		},
	}

	svc := newClientService(cr, nil, nil)
	client, err := svc.GetClientDetails(clientID, userID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "access denied")
	assert.Nil(t, client)
}

func TestGetClientDetails_Success(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return true, "OWNER"
		},
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{ID: clientID, Name: "Test Org"}, nil
		},
	}

	svc := newClientService(cr, nil, nil)
	client, err := svc.GetClientDetails(clientID, userID)

	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, "Test Org", client.Name)
}

// ---------------------------------------------------------------------------
// GetClientChildren tests
// ---------------------------------------------------------------------------

func TestGetClientChildren_AccessDenied(t *testing.T) {
	parentID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return false, ""
		},
	}

	svc := newClientService(cr, nil, nil)
	children, err := svc.GetClientChildren(parentID, userID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "access denied")
	assert.Nil(t, children)
}

func TestGetClientChildren_ViewerDoesNotSeeDescendants(t *testing.T) {
	parentID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return true, "VIEWER"
		},
	}

	svc := newClientService(cr, nil, nil)
	children, err := svc.GetClientChildren(parentID, userID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "access denied")
	assert.Nil(t, children)
}

func TestGetClientChildren_Success(t *testing.T) {
	parentID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return true, "INHERITED_OWNER"
		},
		GetChildrenClientsFunc: func(pID uuid.UUID) ([]models.Client, error) {
			return []models.Client{
				{ID: uuid.Must(uuid.NewV4()), Name: "Child A"},
				{ID: uuid.Must(uuid.NewV4()), Name: "Child B"},
			}, nil
		},
	}

	svc := newClientService(cr, nil, nil)
	children, err := svc.GetClientChildren(parentID, userID)

	require.NoError(t, err)
	assert.Len(t, children, 2)
}

// ---------------------------------------------------------------------------
// ListClientMembers tests
// ---------------------------------------------------------------------------

func TestListClientMembers_AccessDenied(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return false, ""
		},
	}

	svc := newClientService(cr, nil, nil)
	members, err := svc.ListClientMembers(clientID, userID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
	assert.Nil(t, members)
}

func TestListClientMembers_Success(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())

	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return true, "ADMIN"
		},
		GetMembersFunc: func(cID uuid.UUID) ([]models.ClientMember, error) {
			return []models.ClientMember{
				{UserID: uuid.Must(uuid.NewV4())},
				{UserID: uuid.Must(uuid.NewV4())},
			}, nil
		},
	}

	svc := newClientService(cr, nil, nil)
	members, err := svc.ListClientMembers(clientID, userID)

	require.NoError(t, err)
	assert.Len(t, members, 2)
}

func TestListClientMembers_InheritedAdminCanManageDescendantTeam(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return true, "INHERITED_ADMIN"
		},
		GetMembersFunc: func(cID uuid.UUID) ([]models.ClientMember, error) {
			return []models.ClientMember{}, nil
		},
	}

	svc := newClientService(cr, nil, nil)
	members, err := svc.ListClientMembers(clientID, userID)

	require.NoError(t, err)
	assert.Empty(t, members)
}

// ---------------------------------------------------------------------------
// DeleteClient tests
// ---------------------------------------------------------------------------

func TestDeleteClient_AccessDenied(t *testing.T) {
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return false, ""
		},
	}

	svc := newClientService(cr, nil, nil)
	err := svc.DeleteClient(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "access denied")
}

func TestDeleteClient_NotOwner(t *testing.T) {
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return true, "ADMIN" // has access but not owner
		},
	}

	svc := newClientService(cr, nil, nil)
	err := svc.DeleteClient(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "only the owner can delete")
}

func TestDeleteClient_RejectsOrganizationWithChildren(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	deleteCalled := false
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return true, "OWNER"
		},
		GetChildrenClientsFunc: func(parentID uuid.UUID) ([]models.Client, error) {
			return []models.Client{{ID: uuid.Must(uuid.NewV4()), Name: "Child"}}, nil
		},
		DeleteClientFunc: func(id uuid.UUID) error {
			deleteCalled = true
			return nil
		},
	}

	svc := newClientService(cr, nil, nil)
	err := svc.DeleteClient(clientID, uuid.Must(uuid.NewV4()))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "sub-organizations")
	assert.False(t, deleteCalled)
}

func TestDeleteClient_Success(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	membersDeleted := false
	clientDeleted := false

	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return true, "OWNER"
		},
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{ID: clientID, Logo: ""}, nil // no logo — avoids s.rs call
		},
		DeleteAllMembersFunc: func(cID uuid.UUID) error {
			membersDeleted = true
			return nil
		},
		DeleteClientFunc: func(id uuid.UUID) error {
			clientDeleted = true
			return nil
		},
	}

	svc := newClientService(cr, nil, nil)
	err := svc.DeleteClient(clientID, uuid.Must(uuid.NewV4()))

	require.NoError(t, err)
	assert.True(t, membersDeleted)
	assert.True(t, clientDeleted)
}

// ---------------------------------------------------------------------------
// mockCacheRepo for cache-related tests
// ---------------------------------------------------------------------------

type mockCacheRepo struct {
	GetKeyFunc              func(ctx context.Context, key string) (string, error)
	SaveKeyFunc             func(ctx context.Context, key, value string, ttl time.Duration) error
	InvalidateFunc          func(resource, key string) error
	DeleteKeysByPatternFunc func(ctx context.Context, pattern string) error
}

func (m *mockCacheRepo) GetKey(ctx context.Context, key string) (string, error) {
	if m.GetKeyFunc != nil {
		return m.GetKeyFunc(ctx, key)
	}
	return "", nil
}
func (m *mockCacheRepo) SaveKey(ctx context.Context, key, value string, ttl time.Duration) error {
	if m.SaveKeyFunc != nil {
		return m.SaveKeyFunc(ctx, key, value, ttl)
	}
	return nil
}
func (m *mockCacheRepo) Invalidate(resource, key string) error {
	if m.InvalidateFunc != nil {
		return m.InvalidateFunc(resource, key)
	}
	return nil
}
func (m *mockCacheRepo) DeleteKeysByPattern(ctx context.Context, pattern string) error {
	if m.DeleteKeysByPatternFunc != nil {
		return m.DeleteKeysByPatternFunc(ctx, pattern)
	}
	return nil
}

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

func newClientServiceWithCache(cr *mockClientRepo, cache *mockCacheRepo) *ClientService {
	if cr == nil {
		cr = &mockClientRepo{}
	}
	crr := &mockClientRoleRepo{
		GetByCodeFunc: func(code string) (*models.ClientRole, error) {
			return &models.ClientRole{ID: uuid.Must(uuid.NewV4()), Code: "Owner", Hierarchy: 1}, nil
		},
	}
	ctr := &mockClientTypeRepo{}
	return NewClientService(cr, crr, ctr, nil, cache, nil)
}

// ---------------------------------------------------------------------------
// GetMyClients cache tests
// ---------------------------------------------------------------------------

func TestGetMyClients_CacheHit_ReturnsCached(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	cachedJSON := `[{"id":"` + userID.String() + `","name":"Cached Corp"}]`

	cache := &mockCacheRepo{
		GetKeyFunc: func(ctx context.Context, key string) (string, error) {
			return cachedJSON, nil
		},
	}
	repoCalled := false
	cr := &mockClientRepo{
		GetClientsByUserFunc: func(uid uuid.UUID) ([]models.Client, error) {
			repoCalled = true
			return nil, nil
		},
	}

	svc := newClientServiceWithCache(cr, cache)
	clients, err := svc.GetMyClients(userID)

	require.NoError(t, err)
	assert.Len(t, clients, 1)
	assert.False(t, repoCalled, "repo should not be called on cache hit")
}

func TestGetMyClients_CacheMiss_StoresInCache(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	savedKey := ""

	cache := &mockCacheRepo{
		GetKeyFunc: func(ctx context.Context, key string) (string, error) {
			return "", errors.New("miss")
		},
		SaveKeyFunc: func(ctx context.Context, key, value string, ttl time.Duration) error {
			savedKey = key
			return nil
		},
	}
	cr := &mockClientRepo{
		GetClientsByUserFunc: func(uid uuid.UUID) ([]models.Client, error) {
			return []models.Client{{Name: "Acme"}}, nil
		},
	}

	svc := newClientServiceWithCache(cr, cache)
	clients, err := svc.GetMyClients(userID)

	require.NoError(t, err)
	assert.Len(t, clients, 1)
	assert.Contains(t, savedKey, "myclients")
}

func TestGetMyClients_NilCache_CallsRepo(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	repoCalled := false

	cr := &mockClientRepo{
		GetClientsByUserFunc: func(uid uuid.UUID) ([]models.Client, error) {
			repoCalled = true
			return []models.Client{{Name: "DirectCorp"}}, nil
		},
	}

	svc := newClientService(cr, nil, nil) // nil cache
	clients, err := svc.GetMyClients(userID)

	require.NoError(t, err)
	assert.Len(t, clients, 1)
	assert.True(t, repoCalled)
}

func TestUpdateClientDetails_InvalidatesAllMyClients(t *testing.T) {
	patternDeleted := false
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			if pattern == "v1:tenant:*:user:*:myclients" {
				patternDeleted = true
			}
			return nil
		},
	}
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return true, "OWNER"
		},
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{ID: id, Name: "Old Name"}, nil
		},
		UpdateClientFunc: func(client *models.Client) error { return nil },
	}

	svc := newClientServiceWithCache(cr, cache)
	_, err := svc.UpdateClientDetails(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), "New Name", "", false)

	require.NoError(t, err)
	assert.True(t, patternDeleted, "UpdateClientDetails should invalidate versioned tenant client caches")
}

func TestAddUserToClient_InvalidatesCache(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	invalidated := false

	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			if resource == "myclients" && key == "v1:tenant:eventiapp:user:"+userID.String() {
				invalidated = true
			}
			return nil
		},
	}
	cr := &mockClientRepo{
		IsMemberFunc:  func(uid, cid uuid.UUID) (bool, string) { return false, "" },
		AddMemberFunc: func(m *models.ClientMember) error { return nil },
	}

	svc := newClientServiceWithCache(cr, cache)
	err := svc.AddUserToClient(uuid.Must(uuid.NewV4()), userID, uuid.Must(uuid.NewV4()))

	require.NoError(t, err)
	assert.True(t, invalidated, "AddUserToClient should invalidate the new member's cache")
}

func TestRemoveClientMember_InvalidatesTargetUserCache(t *testing.T) {
	requesterID := uuid.Must(uuid.NewV4())
	targetID := uuid.Must(uuid.NewV4())
	invalidated := false

	cache := &mockCacheRepo{
		InvalidateFunc: func(resource, key string) error {
			if resource == "myclients" && key == "v1:tenant:eventiapp:user:"+targetID.String() {
				invalidated = true
			}
			return nil
		},
	}
	cr := &mockClientRepo{
		GetMemberRoleFunc: func(clientID, userID uuid.UUID) (*models.ClientRole, error) {
			if userID == requesterID {
				return &models.ClientRole{Hierarchy: 1}, nil // requester = owner
			}
			return &models.ClientRole{Hierarchy: 2}, nil // target = admin
		},
		RemoveMemberFunc: func(cID, uID uuid.UUID) error { return nil },
	}

	svc := newClientServiceWithCache(cr, cache)
	err := svc.RemoveClientMember(uuid.Must(uuid.NewV4()), requesterID, targetID)

	require.NoError(t, err)
	assert.True(t, invalidated, "RemoveClientMember should invalidate removed user's cache")
}

func TestDeleteClient_InvalidatesAllMyClients(t *testing.T) {
	patternDeleted := false
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			if pattern == "v1:tenant:*:user:*:myclients" {
				patternDeleted = true
			}
			return nil
		},
	}
	cr := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uID, cID uuid.UUID) (bool, string) {
			return true, "OWNER"
		},
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{ID: id, Logo: ""}, nil
		},
		DeleteAllMembersFunc: func(cID uuid.UUID) error { return nil },
		DeleteClientFunc:     func(id uuid.UUID) error { return nil },
	}

	svc := newClientServiceWithCache(cr, cache)
	err := svc.DeleteClient(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()))

	require.NoError(t, err)
	assert.True(t, patternDeleted, "DeleteClient should invalidate all user caches")
}

func clientLogoFileHeader(filename, contentType string, size int64) *multipart.FileHeader {
	return &multipart.FileHeader{
		Filename: filename,
		Header: textproto.MIMEHeader{
			"Content-Type": []string{contentType},
		},
		Size: size,
	}
}

func validClientLogoPNG(t *testing.T) []byte {
	t.Helper()
	var payload bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	require.NoError(t, png.Encode(&payload, img))
	return payload.Bytes()
}

func newClientCreateWithLogoService(repo *mockClientRepo, storage ports.ObjectStorageRepository) *ClientService {
	roleRepo := &mockClientRoleRepo{GetByCodeFunc: func(string) (*models.ClientRole, error) {
		return ownerRole(), nil
	}}
	typeRepo := &mockClientTypeRepo{GetByIDFunc: func(id uuid.UUID) (*models.ClientType, error) {
		return &models.ClientType{ID: id, Code: "PLATFORM"}, nil
	}}
	resourceSvc := resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: storage},
	)
	return NewClientService(repo, roleRepo, typeRepo, resourceSvc, nil, nil)
}

func TestCreateClientWithLogoRollsBackClientWhenLogoUploadFails(t *testing.T) {
	var deletedClientID uuid.UUID
	repo := &mockClientRepo{DeleteClientFunc: func(id uuid.UUID) error {
		deletedClientID = id
		return nil
	}}
	storage := &clientTestLogoStorage{uploadErr: errors.New("storage unavailable")}
	svc := newClientCreateWithLogoService(repo, storage)
	payload := validClientLogoPNG(t)

	client, err := svc.CreateClientWithLogo(
		"Platform",
		uuid.Must(uuid.NewV4()),
		uuid.Must(uuid.NewV4()),
		nil,
		&clientTestMultipartFile{Reader: bytes.NewReader(payload)},
		clientLogoFileHeader("logo.png", "image/png", int64(len(payload))),
	)

	require.Error(t, err)
	assert.Nil(t, client)
	assert.NotEqual(t, uuid.Nil, deletedClientID)
}

func TestCreateClientWithLogoRollsBackObjectAndClientWhenLogoPathSaveFails(t *testing.T) {
	var deletedClientID uuid.UUID
	repo := &mockClientRepo{
		UpdateClientFunc: func(*models.Client) error { return errors.New("database unavailable") },
		DeleteClientFunc: func(id uuid.UUID) error {
			deletedClientID = id
			return nil
		},
	}
	storage := &clientTestLogoStorage{}
	svc := newClientCreateWithLogoService(repo, storage)
	payload := validClientLogoPNG(t)

	client, err := svc.CreateClientWithLogo(
		"Platform",
		uuid.Must(uuid.NewV4()),
		uuid.Must(uuid.NewV4()),
		nil,
		&clientTestMultipartFile{Reader: bytes.NewReader(payload)},
		clientLogoFileHeader("logo.png", "image/png", int64(len(payload))),
	)

	require.Error(t, err)
	assert.Nil(t, client)
	assert.NotEqual(t, uuid.Nil, deletedClientID)
	require.Len(t, storage.deleted, 1)
	assert.Contains(t, storage.deleted[0], "organizations/"+deletedClientID.String()+"/branding/logo/")
}

func TestMyClientsCacheKeyIsIsolatedByTenant(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	svc := newClientServiceWithCache(&mockClientRepo{}, &mockCacheRepo{})

	itbemKey := svc.WithTenantScope("itbem", "").myClientsKey(userID)
	cafettonKey := svc.WithTenantScope("cafettonhouse", "").myClientsKey(userID)

	assert.Equal(t, "v1:tenant:itbem:user:"+userID.String()+":myclients", itbemKey)
	assert.Equal(t, "v1:tenant:cafettonhouse:user:"+userID.String()+":myclients", cafettonKey)
	assert.NotEqual(t, itbemKey, cafettonKey)
}
