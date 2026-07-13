package clients

import (
	"context"
	"encoding/json"
	"events-stocks/configuration/constants"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"
	ResourceService "events-stocks/services/resources"
	"fmt"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"log/slog"
	"mime/multipart"
	"strings"
)

// _clientSvc is the package-level singleton set by internal/app.
var _clientSvc *ClientService

// SetDefaultClientService wires the package-level functions to the DI instance.
func SetDefaultClientService(svc *ClientService) { _clientSvc = svc }

func CreateClient(name string, clientTypeID uuid.UUID, ownerUserID uuid.UUID, parentID *uuid.UUID) (*models.Client, error) {
	return _clientSvc.CreateClient(name, clientTypeID, ownerUserID, parentID)
}

func GetClientDetails(clientID, userID uuid.UUID) (*models.Client, error) {
	return _clientSvc.GetClientDetails(clientID, userID)
}

func UpdateClientDetails(
	clientID, requesterID uuid.UUID,
	name, logoName string,
	shouldDeleteOld bool,
) (*models.Client, error) {
	return _clientSvc.UpdateClientDetails(clientID, requesterID, name, logoName, shouldDeleteOld)
}

func GetMyClients(userID uuid.UUID) ([]models.Client, error) {
	return _clientSvc.GetMyClients(userID)
}

func GetAllClients() ([]models.Client, error) {
	return _clientSvc.GetAllClients()
}

func GetClientChildren(parentID, userID uuid.UUID) ([]models.Client, error) {
	return _clientSvc.GetClientChildren(parentID, userID)
}

func DeleteClient(clientID, requesterID uuid.UUID) error {
	return _clientSvc.DeleteClient(clientID, requesterID)
}

func AddUserToClient(clientID, userID, roleID uuid.UUID) error {
	return _clientSvc.AddUserToClient(clientID, userID, roleID)
}

func RemoveClientMember(clientID, requesterID, targetUserID uuid.UUID) error {
	return _clientSvc.RemoveClientMember(clientID, requesterID, targetUserID)
}

func UpdateClientMemberRole(clientID, requesterID, targetUserID, newRoleID uuid.UUID) error {
	return _clientSvc.UpdateClientMemberRole(clientID, requesterID, targetUserID, newRoleID)
}

func ListClientMembers(targetClientID, requesterID uuid.UUID) ([]models.ClientMember, error) {
	return _clientSvc.ListClientMembers(targetClientID, requesterID)
}

func CreateClientWithLogo(
	name string,
	clientTypeID uuid.UUID,
	ownerUserID uuid.UUID,
	parentID *uuid.UUID,
	logoFile multipart.File,
	logoHeader *multipart.FileHeader,
) (*models.Client, error) {
	return _clientSvc.CreateClientWithLogo(name, clientTypeID, ownerUserID, parentID, logoFile, logoHeader)
}

// ClientService is the injectable, struct-based client service.
type ClientService struct {
	clientRepo     ports.ClientRepository
	clientRoleRepo ports.ClientRoleRepository
	clientTypeRepo ports.ClientTypeRepository
	rs             *ResourceService.ResourceService
	cache          ports.CacheRepository
	tx             ports.Transactor
}

func NewClientService(
	clientRepo ports.ClientRepository,
	clientRoleRepo ports.ClientRoleRepository,
	clientTypeRepo ports.ClientTypeRepository,
	rs *ResourceService.ResourceService,
	cache ports.CacheRepository,
	tx ports.Transactor,
) *ClientService {
	return &ClientService{
		clientRepo:     clientRepo,
		clientRoleRepo: clientRoleRepo,
		clientTypeRepo: clientTypeRepo,
		rs:             rs,
		cache:          cache,
		tx:             tx,
	}
}

func (s *ClientService) CreateClient(name string, clientTypeID uuid.UUID, ownerUserID uuid.UUID, parentID *uuid.UUID) (*models.Client, error) {
	return s.createClient(name, clientTypeID, ownerUserID, parentID, false)
}

// CreateClientAsPrimaryRoot is deliberately separate from normal organization
// creation: Root 1 may provision the hierarchy without requiring a direct
// Owner membership on the parent. Root 2 never receives this bypass.
func (s *ClientService) CreateClientAsPrimaryRoot(name string, clientTypeID uuid.UUID, ownerUserID uuid.UUID, parentID *uuid.UUID) (*models.Client, error) {
	return s.createClient(name, clientTypeID, ownerUserID, parentID, true)
}

func (s *ClientService) createClient(name string, clientTypeID uuid.UUID, ownerUserID uuid.UUID, parentID *uuid.UUID, primaryRoot bool) (*models.Client, error) {
	requestedType, err := s.clientTypeRepo.GetByID(clientTypeID)
	if err != nil {
		return nil, fmt.Errorf("invalid client type id: %w", err)
	}
	if parentID == nil {
		if requestedType.Code != "PLATFORM" {
			return nil, fmt.Errorf("root clients must be of type PLATFORM")
		}
	} else {
		if !primaryRoot {
			allowed, role := s.clientRepo.CheckAccessRecursive(ownerUserID, *parentID)
			roleUpper := strings.ToUpper(role)
			if !allowed || (roleUpper != "OWNER" && roleUpper != "INHERITED_OWNER") {
				return nil, fmt.Errorf("permission denied: owner access required on parent organization")
			}
		}
		parent, err := s.clientRepo.GetClientByID(*parentID)
		if err != nil {
			return nil, fmt.Errorf("parent client not found")
		}
		switch strings.ToUpper(parent.ClientType.Code) {
		case "PLATFORM":
			if strings.ToUpper(requestedType.Code) != "AGENCY" {
				return nil, fmt.Errorf("platforms can only create agencies")
			}
		case "AGENCY":
			if strings.ToUpper(requestedType.Code) != "CUSTOMER" {
				return nil, fmt.Errorf("agencies can only create customers")
			}
		default:
			return nil, fmt.Errorf("customers cannot have sub-clients")
		}
	}
	ownerRole, err := s.clientRoleRepo.GetByCode("Owner")
	if err != nil {
		return nil, fmt.Errorf("system configuration error: 'Owner' role not found in database")
	}
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = strings.ReplaceAll(slug, " ", "-")
	clientID, err := uuid.NewV4()
	if err != nil {
		return nil, fmt.Errorf("failed to generate client id: %w", err)
	}
	client := &models.Client{
		ID:           clientID,
		Name:         name,
		Code:         slug,
		ClientTypeID: clientTypeID,
		ParentID:     parentID,
	}
	member := &models.ClientMember{
		ClientID:     clientID,
		UserID:       ownerUserID,
		ClientRoleID: ownerRole.ID,
		IsActive:     true,
	}
	if s.tx != nil {
		if err := s.tx.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(client).Error; err != nil {
				return err
			}
			return tx.Create(member).Error
		}); err != nil {
			return nil, fmt.Errorf("failed to create client and assign owner: %w", err)
		}
	} else {
		if err := s.clientRepo.CreateClient(client); err != nil {
			return nil, err
		}
		if err := s.clientRepo.AddMember(member); err != nil {
			return nil, fmt.Errorf("failed to assign owner: %w", err)
		}
	}
	s.invalidateMyClients(ownerUserID)
	return client, nil
}

func (s *ClientService) GetClientDetails(clientID, userID uuid.UUID) (*models.Client, error) {
	allowed, _ := s.clientRepo.CheckAccessRecursive(userID, clientID)
	if !allowed {
		return nil, fmt.Errorf("access denied: user is not a member of this client hierarchy")
	}
	return s.clientRepo.GetClientByID(clientID)
}

func (s *ClientService) GetClientDetailsAsPrimaryRoot(clientID uuid.UUID) (*models.Client, error) {
	return s.clientRepo.GetClientByID(clientID)
}

func (s *ClientService) GetAllClients() ([]models.Client, error) {
	return s.clientRepo.GetAllClients()
}

func (s *ClientService) ListClientsPaginated(userID *uuid.UUID, query dtos.ClientsListQuery) ([]models.Client, int64, error) {
	return s.clientRepo.ListClientsPaginated(userID, query)
}

func (s *ClientService) UpdateClientDetails(
	clientID, requesterID uuid.UUID,
	name, logoName string,
	shouldDeleteOld bool,
) (*models.Client, error) {
	return s.updateClientDetails(clientID, requesterID, name, logoName, shouldDeleteOld, false)
}

func (s *ClientService) UpdateClientDetailsAsPrimaryRoot(
	clientID uuid.UUID,
	name, logoName string,
	shouldDeleteOld bool,
) (*models.Client, error) {
	return s.updateClientDetails(clientID, uuid.Nil, name, logoName, shouldDeleteOld, true)
}

func (s *ClientService) updateClientDetails(
	clientID, requesterID uuid.UUID,
	name, logoName string,
	shouldDeleteOld bool,
	primaryRoot bool,
) (*models.Client, error) {
	if !primaryRoot {
		allowed, role := s.clientRepo.CheckAccessRecursive(requesterID, clientID)
		if !allowed || (strings.ToUpper(role) != "OWNER" && strings.ToUpper(role) != "INHERITED_OWNER") {
			return nil, fmt.Errorf("permission denied")
		}
	}
	client, err := s.clientRepo.GetClientByID(clientID)
	if err != nil {
		return nil, fmt.Errorf("client not found")
	}
	oldLogo := client.Logo
	if trimmedName := strings.TrimSpace(name); trimmedName != "" {
		client.Name = trimmedName
		slug := strings.ToLower(trimmedName)
		client.Code = strings.ReplaceAll(slug, " ", "-")
	}
	if shouldDeleteOld || logoName != "" {
		client.Logo = logoName
	}
	if err := s.clientRepo.UpdateClient(client); err != nil {
		if logoName != "" && logoName != oldLogo && s.rs != nil {
			s.cleanupClientLogo(client.ID, logoName)
		}
		return nil, err
	}
	if shouldDeleteOld && oldLogo != "" && oldLogo != logoName && s.rs != nil {
		if err := s.rs.DeleteObjectByPath(fmt.Sprintf("clients/%s/logo/%s", client.ID, oldLogo)); err != nil {
			slog.Warn("failed to delete replaced client logo", "client_id", client.ID, "logo", oldLogo, "error", err)
		}
	}
	s.invalidateAllMyClients()
	return client, nil
}

func (s *ClientService) myClientsKey(userID uuid.UUID) string {
	return userID.String() + ":myclients"
}

func (s *ClientService) GetMyClients(userID uuid.UUID) ([]models.Client, error) {
	if s.cache != nil {
		ctx := context.Background()
		if cached, err := s.cache.GetKey(ctx, s.myClientsKey(userID)); err == nil && cached != "" {
			var result []models.Client
			if err := json.Unmarshal([]byte(cached), &result); err == nil {
				return result, nil
			}
		}
	}
	data, err := s.clientRepo.GetClientsByUser(userID)
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		if jsonBytes, err := json.Marshal(data); err == nil {
			ctx := context.Background()
			_ = s.cache.SaveKey(ctx, s.myClientsKey(userID), string(jsonBytes), constants.ShortTimeTTL)
		}
	}
	return data, nil
}

func (s *ClientService) UploadClientLogo(file multipart.File, header *multipart.FileHeader, clientID uuid.UUID) (string, error) {
	if s.rs == nil {
		return "", fmt.Errorf("resource service unavailable")
	}
	filename, _, err := s.rs.UploadClientLogo(file, header, clientID)
	return filename, err
}

func (s *ClientService) GetClientLogoURL(clientID uuid.UUID, logoName string) string {
	if s.rs == nil || logoName == "" {
		return ""
	}
	return s.rs.GetClientLogoURL(clientID, logoName)
}

func (s *ClientService) cleanupClientLogo(clientID uuid.UUID, logoName string) {
	if s == nil || s.rs == nil || strings.TrimSpace(logoName) == "" {
		return
	}
	if err := s.rs.DeleteObjectByPath(fmt.Sprintf("clients/%s/logo/%s", clientID, logoName)); err != nil {
		slog.Error("client logo rollback failed", "client_id", clientID, "logo", logoName, "error", err)
	}
}

func (s *ClientService) rollbackCreatedClient(client *models.Client, ownerUserID uuid.UUID, logoName string) {
	if client == nil {
		return
	}
	if strings.TrimSpace(logoName) != "" {
		s.cleanupClientLogo(client.ID, logoName)
	}
	if err := s.clientRepo.DeleteClient(client.ID); err != nil {
		slog.Error("client creation rollback failed", "client_id", client.ID, "error", err)
	}
	s.invalidateMyClients(ownerUserID)
}

func (s *ClientService) invalidateMyClients(userID uuid.UUID) {
	if s.cache != nil {
		_ = s.cache.Invalidate("myclients", userID.String())
	}
}

func (s *ClientService) invalidateAllMyClients() {
	if s.cache != nil {
		_ = s.cache.DeleteKeysByPattern(context.Background(), "*:myclients")
	}
}

func (s *ClientService) GetClientChildren(parentID, userID uuid.UUID) ([]models.Client, error) {
	allowed, role := s.clientRepo.CheckAccessRecursive(userID, parentID)
	role = strings.TrimPrefix(strings.ToUpper(role), "INHERITED_")
	if !allowed || (role != "OWNER" && role != "ADMIN") {
		return nil, fmt.Errorf("access denied")
	}
	return s.clientRepo.GetChildrenClients(parentID)
}

func (s *ClientService) GetClientChildrenAsPrimaryRoot(parentID uuid.UUID) ([]models.Client, error) {
	return s.clientRepo.GetChildrenClients(parentID)
}

func (s *ClientService) DeleteClient(clientID, requesterID uuid.UUID) error {
	return s.deleteClient(clientID, requesterID, false)
}

func (s *ClientService) DeleteClientAsPrimaryRoot(clientID uuid.UUID) error {
	return s.deleteClient(clientID, uuid.Nil, true)
}

func (s *ClientService) deleteClient(clientID, requesterID uuid.UUID, primaryRoot bool) error {
	if !primaryRoot {
		allowed, role := s.clientRepo.CheckAccessRecursive(requesterID, clientID)
		if !allowed {
			return fmt.Errorf("access denied")
		}
		roleUpper := strings.ToUpper(role)
		if roleUpper != "OWNER" && roleUpper != "INHERITED_OWNER" {
			return fmt.Errorf("permission denied: only the owner can delete the organization")
		}
	}
	children, err := s.clientRepo.GetChildrenClients(clientID)
	if err != nil {
		return fmt.Errorf("failed to verify organization hierarchy: %w", err)
	}
	if len(children) > 0 {
		return fmt.Errorf("cannot delete organization while it has sub-organizations")
	}
	client, err := s.clientRepo.GetClientByID(clientID)
	if err == nil && client.Logo != "" {
		fullPath := fmt.Sprintf("clients/%s/logo/%s", client.ID, client.Logo)
		_ = s.rs.DeleteObjectByPath(fullPath)
	}
	if s.tx != nil {
		if err := s.tx.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("client_id = ?", clientID).Delete(&models.ClientMember{}).Error; err != nil {
				return fmt.Errorf("failed to detach members: %w", err)
			}
			if err := tx.Where("id = ?", clientID).Delete(&models.Client{}).Error; err != nil {
				return fmt.Errorf("failed to delete client: %w", err)
			}
			return nil
		}); err != nil {
			return err
		}
	} else {
		if err := s.clientRepo.DeleteAllMembers(clientID); err != nil {
			return fmt.Errorf("failed to detach members: %w", err)
		}
		if err := s.clientRepo.DeleteClient(clientID); err != nil {
			return fmt.Errorf("failed to delete client: %w", err)
		}
	}
	s.invalidateAllMyClients()
	return nil
}

func (s *ClientService) AddUserToClient(clientID, userID, roleID uuid.UUID) error {
	isMember, _ := s.clientRepo.IsMember(userID, clientID)
	if isMember {
		return fmt.Errorf("user is already a member")
	}
	member := &models.ClientMember{
		ClientID:     clientID,
		UserID:       userID,
		ClientRoleID: roleID,
		IsActive:     true,
	}
	if err := s.clientRepo.AddMember(member); err != nil {
		return err
	}
	s.invalidateMyClients(userID)
	return nil
}

func (s *ClientService) CanManageClientMembers(clientID, requesterID uuid.UUID) error {
	allowed, role := s.clientRepo.CheckAccessRecursive(requesterID, clientID)
	roleUpper := strings.ToUpper(role)
	if !allowed || (roleUpper != "OWNER" && roleUpper != "ADMIN" && roleUpper != "INHERITED_OWNER" && roleUpper != "INHERITED_ADMIN") {
		return fmt.Errorf("permission denied: cannot manage members for this client")
	}
	return nil
}

func (s *ClientService) CanAssignClientRole(clientID, requesterID, roleID uuid.UUID) error {
	allowed, roleCode := s.clientRepo.CheckAccessRecursive(requesterID, clientID)
	if !allowed {
		return fmt.Errorf("permission denied: cannot manage members for this client")
	}

	normalizedRoleCode := strings.TrimPrefix(strings.ToUpper(roleCode), "INHERITED_")
	if normalizedRoleCode != "OWNER" && normalizedRoleCode != "ADMIN" {
		return fmt.Errorf("permission denied: cannot manage members for this client")
	}

	requesterRole, err := s.clientRoleRepo.GetByCode(normalizedRoleCode)
	if err != nil {
		return fmt.Errorf("requester role not found")
	}
	targetRole, err := s.clientRoleRepo.GetByID(roleID)
	if err != nil {
		return fmt.Errorf("invalid role")
	}
	if requesterRole.Hierarchy >= targetRole.Hierarchy {
		return fmt.Errorf("permission denied: you cannot assign a role equal or higher than your own")
	}
	return nil
}

func (s *ClientService) CanAssignClientRoleAsRoot(roleID uuid.UUID) error {
	targetRole, err := s.clientRoleRepo.GetByID(roleID)
	if err != nil {
		return fmt.Errorf("invalid role")
	}
	if targetRole.Hierarchy <= 1 {
		return fmt.Errorf("permission denied: the owner role cannot be assigned from member management")
	}
	return nil
}

func (s *ClientService) GetDirectMemberRole(userID, clientID uuid.UUID) (string, error) {
	isMember, roleCode := s.clientRepo.IsMember(userID, clientID)
	if !isMember {
		return "", fmt.Errorf("user is not a member of this organization")
	}
	return roleCode, nil
}

// GetEffectiveMemberRole includes eligible inherited access. It is intended
// for presentation of assignable roles; mutation paths still validate the
// hierarchy again server-side.
func (s *ClientService) GetEffectiveMemberRole(userID, clientID uuid.UUID) (string, error) {
	allowed, roleCode := s.clientRepo.CheckAccessRecursive(userID, clientID)
	if !allowed || roleCode == "" {
		return "", fmt.Errorf("user is not a member of this organization")
	}
	return roleCode, nil
}

// getEffectiveManagerRole resolves direct and inherited Owner/Admin access
// into the canonical role object used by every member-mutation hierarchy
// check. This keeps propagation consistent: an inherited administrator can
// manage a child organization, but never a peer or superior role.
func (s *ClientService) getEffectiveManagerRole(clientID, requesterID uuid.UUID) (*models.ClientRole, error) {
	allowed, roleCode := s.clientRepo.CheckAccessRecursive(requesterID, clientID)
	if allowed && roleCode != "" {
		switch strings.TrimPrefix(strings.ToUpper(roleCode), "INHERITED_") {
		case "OWNER":
			return &models.ClientRole{Code: "Owner", Hierarchy: 1}, nil
		case "ADMIN":
			return &models.ClientRole{Code: "Admin", Hierarchy: 2}, nil
		default:
			return nil, fmt.Errorf("permission denied: cannot manage members for this client")
		}
	}

	// Keep the direct lookup as a compatibility fallback for deployments where
	// the recursive membership projection has not yet been backfilled.
	role, err := s.clientRepo.GetMemberRole(clientID, requesterID)
	if err != nil {
		return nil, fmt.Errorf("requester is not a member")
	}
	if role.Hierarchy > 2 {
		return nil, fmt.Errorf("permission denied: cannot manage members for this client")
	}
	return role, nil
}

func (s *ClientService) RemoveClientMember(clientID, requesterID, targetUserID uuid.UUID) error {
	if requesterID == targetUserID {
		return fmt.Errorf("cannot remove yourself using this endpoint")
	}
	myRole, err := s.getEffectiveManagerRole(clientID, requesterID)
	if err != nil {
		return err
	}
	targetRole, err := s.clientRepo.GetMemberRole(clientID, targetUserID)
	if err != nil {
		return fmt.Errorf("target user is not a member")
	}
	if myRole.Hierarchy >= targetRole.Hierarchy {
		return fmt.Errorf("permission denied: you cannot remove a member with equal or higher hierarchy")
	}
	if err := s.clientRepo.RemoveMember(clientID, targetUserID); err != nil {
		return err
	}
	s.invalidateMyClients(targetUserID)
	return nil
}

func (s *ClientService) RemoveClientMemberAsRoot(clientID, targetUserID uuid.UUID) error {
	targetRole, err := s.clientRepo.GetMemberRole(clientID, targetUserID)
	if err != nil {
		return fmt.Errorf("target user is not a member")
	}
	if targetRole.Hierarchy <= 1 {
		return fmt.Errorf("permission denied: the organization owner cannot be removed")
	}
	if err := s.clientRepo.RemoveMember(clientID, targetUserID); err != nil {
		return err
	}
	s.invalidateMyClients(targetUserID)
	return nil
}

func (s *ClientService) UpdateClientMemberRole(clientID, requesterID, targetUserID, newRoleID uuid.UUID) error {
	myRole, err := s.getEffectiveManagerRole(clientID, requesterID)
	if err != nil {
		return err
	}
	targetRole, err := s.clientRepo.GetMemberRole(clientID, targetUserID)
	if err != nil {
		return fmt.Errorf("target user is not a member")
	}
	if myRole.Hierarchy >= targetRole.Hierarchy {
		return fmt.Errorf("permission denied: you cannot modify a member with equal or higher hierarchy")
	}
	newRoleObj, err := s.clientRoleRepo.GetByID(newRoleID)
	if err != nil {
		return fmt.Errorf("invalid new role")
	}
	if myRole.Hierarchy >= newRoleObj.Hierarchy {
		return fmt.Errorf("permission denied: you cannot grant a role equal or higher than your own")
	}
	if err := s.clientRepo.UpdateMemberRole(clientID, targetUserID, newRoleID); err != nil {
		return err
	}
	s.invalidateMyClients(targetUserID)
	return nil
}

func (s *ClientService) UpdateClientMemberRoleAsRoot(clientID, targetUserID, newRoleID uuid.UUID) error {
	targetRole, err := s.clientRepo.GetMemberRole(clientID, targetUserID)
	if err != nil {
		return fmt.Errorf("target user is not a member")
	}
	if targetRole.Hierarchy <= 1 {
		return fmt.Errorf("permission denied: the organization owner cannot be modified")
	}
	if err := s.CanAssignClientRoleAsRoot(newRoleID); err != nil {
		return err
	}
	if err := s.clientRepo.UpdateMemberRole(clientID, targetUserID, newRoleID); err != nil {
		return err
	}
	s.invalidateMyClients(targetUserID)
	return nil
}

func (s *ClientService) ListClientMembers(targetClientID, requesterID uuid.UUID) ([]models.ClientMember, error) {
	if err := s.CanManageClientMembers(targetClientID, requesterID); err != nil {
		return nil, err
	}
	return s.clientRepo.GetMembers(targetClientID)
}

func (s *ClientService) ListClientMembersAsRoot(targetClientID uuid.UUID) ([]models.ClientMember, error) {
	return s.clientRepo.GetMembers(targetClientID)
}

func (s *ClientService) ListClientMembersPage(targetClientID, requesterID uuid.UUID, page, pageSize int, search string) ([]models.ClientMember, int64, error) {
	if err := s.CanManageClientMembers(targetClientID, requesterID); err != nil {
		return nil, 0, err
	}
	if repo, ok := s.clientRepo.(ports.ClientMembersPageRepository); ok {
		return repo.ListMembersPage(targetClientID, page, pageSize, search)
	}
	members, err := s.clientRepo.GetMembers(targetClientID)
	if err != nil {
		return nil, 0, err
	}
	return members, int64(len(members)), nil
}

func (s *ClientService) ListClientMembersPageAsRoot(targetClientID uuid.UUID, page, pageSize int, search string) ([]models.ClientMember, int64, error) {
	if repo, ok := s.clientRepo.(ports.ClientMembersPageRepository); ok {
		return repo.ListMembersPage(targetClientID, page, pageSize, search)
	}
	members, err := s.clientRepo.GetMembers(targetClientID)
	if err != nil {
		return nil, 0, err
	}
	return members, int64(len(members)), nil
}

func (s *ClientService) CreateClientWithLogo(
	name string,
	clientTypeID uuid.UUID,
	ownerUserID uuid.UUID,
	parentID *uuid.UUID,
	logoFile multipart.File,
	logoHeader *multipart.FileHeader,
) (*models.Client, error) {
	client, err := s.CreateClient(name, clientTypeID, ownerUserID, parentID)
	if err != nil {
		return nil, err
	}
	if logoFile != nil {
		if s.rs == nil {
			s.rollbackCreatedClient(client, ownerUserID, "")
			return nil, fmt.Errorf("resource service unavailable")
		}
		path, presignedURL, err := s.rs.UploadClientLogo(logoFile, logoHeader, client.ID)
		if err != nil {
			s.rollbackCreatedClient(client, ownerUserID, "")
			return nil, fmt.Errorf("failed to create client logo: %w", err)
		}
		client.Logo = path
		if err := s.clientRepo.UpdateClient(client); err != nil {
			s.rollbackCreatedClient(client, ownerUserID, path)
			return nil, fmt.Errorf("failed to save logo path: %w", err)
		}
		client.Logo = presignedURL
	}
	return client, nil
}

func (s *ClientService) CreateClientWithLogoAsPrimaryRoot(
	name string,
	clientTypeID uuid.UUID,
	ownerUserID uuid.UUID,
	parentID *uuid.UUID,
	logoFile multipart.File,
	logoHeader *multipart.FileHeader,
) (*models.Client, error) {
	client, err := s.CreateClientAsPrimaryRoot(name, clientTypeID, ownerUserID, parentID)
	if err != nil {
		return nil, err
	}
	if logoFile == nil {
		return client, nil
	}
	if s.rs == nil {
		s.rollbackCreatedClient(client, ownerUserID, "")
		return nil, fmt.Errorf("resource service unavailable")
	}
	path, presignedURL, err := s.rs.UploadClientLogo(logoFile, logoHeader, client.ID)
	if err != nil {
		s.rollbackCreatedClient(client, ownerUserID, "")
		return nil, fmt.Errorf("failed to create client logo: %w", err)
	}
	client.Logo = path
	if err := s.clientRepo.UpdateClient(client); err != nil {
		s.rollbackCreatedClient(client, ownerUserID, path)
		return nil, fmt.Errorf("failed to save logo path: %w", err)
	}
	client.Logo = presignedURL
	return client, nil
}
