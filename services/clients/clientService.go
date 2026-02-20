package clients

import (
	"context"
	"encoding/json"
	"events-stocks/configuration/constants"
	"events-stocks/models"
	"events-stocks/services/ports"
	ResourceService "events-stocks/services/resources"
	"fmt"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"mime/multipart"
	"strings"
)

// _clientSvc is the package-level singleton set by server.go.
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
	requestedType, err := s.clientTypeRepo.GetByID(clientTypeID)
	if err != nil {
		return nil, fmt.Errorf("invalid client type id: %w", err)
	}
	if parentID == nil {
		if requestedType.Code != "PLATFORM" {
			return nil, fmt.Errorf("root clients must be of type PLATFORM")
		}
	} else {
		allowed, role := s.clientRepo.CheckAccessRecursive(ownerUserID, *parentID)
		roleUpper := strings.ToUpper(role)
		if !allowed || (roleUpper != "OWNER" && roleUpper != "INHERITED_OWNER") {
			return nil, fmt.Errorf("permission denied: owner access required on parent organization")
		}
		parent, err := s.clientRepo.GetClientByID(*parentID)
		if err != nil {
			return nil, fmt.Errorf("parent client not found")
		}
		if parent.ClientType.Code == "AGENCY" && requestedType.Code != "CUSTOMER" {
			return nil, fmt.Errorf("agencies can only create customers")
		}
		if parent.ClientType.Code == "CUSTOMER" {
			return nil, fmt.Errorf("customers cannot have sub-clients")
		}
	}
	ownerRole, err := s.clientRoleRepo.GetByCode("Owner")
	if err != nil {
		return nil, fmt.Errorf("system configuration error: 'Owner' role not found in database")
	}
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = strings.ReplaceAll(slug, " ", "-")
	client := &models.Client{
		Name:         name,
		Code:         slug,
		ClientTypeID: clientTypeID,
		ParentID:     parentID,
	}
	member := &models.ClientMember{
		ClientID:     client.ID,
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

func (s *ClientService) UpdateClientDetails(
	clientID, requesterID uuid.UUID,
	name, logoName string,
	shouldDeleteOld bool,
) (*models.Client, error) {
	allowed, role := s.clientRepo.CheckAccessRecursive(requesterID, clientID)
	if !allowed || (strings.ToUpper(role) != "OWNER" && strings.ToUpper(role) != "INHERITED_OWNER") {
		return nil, fmt.Errorf("permission denied")
	}
	client, err := s.clientRepo.GetClientByID(clientID)
	if err != nil {
		return nil, fmt.Errorf("client not found")
	}
	if shouldDeleteOld && client.Logo != "" {
		fullPath := fmt.Sprintf("clients/%s/logo/%s", client.ID, client.Logo)
		_ = s.rs.DeleteObjectByPath(fullPath)
	}
	client.Name = name
	client.Logo = logoName
	slug := strings.ToLower(strings.TrimSpace(name))
	client.Code = strings.ReplaceAll(slug, " ", "-")
	if err := s.clientRepo.UpdateClient(client); err != nil {
		return nil, err
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
	allowed, _ := s.clientRepo.CheckAccessRecursive(userID, parentID)
	if !allowed {
		return nil, fmt.Errorf("access denied")
	}
	return s.clientRepo.GetChildrenClients(parentID)
}

func (s *ClientService) DeleteClient(clientID, requesterID uuid.UUID) error {
	allowed, role := s.clientRepo.CheckAccessRecursive(requesterID, clientID)
	if !allowed {
		return fmt.Errorf("access denied")
	}
	roleUpper := strings.ToUpper(role)
	if roleUpper != "OWNER" && roleUpper != "INHERITED_OWNER" {
		return fmt.Errorf("permission denied: only the owner can delete the organization")
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

func (s *ClientService) RemoveClientMember(clientID, requesterID, targetUserID uuid.UUID) error {
	if requesterID == targetUserID {
		return fmt.Errorf("cannot remove yourself using this endpoint")
	}
	myRole, err := s.clientRepo.GetMemberRole(clientID, requesterID)
	if err != nil {
		return fmt.Errorf("requester is not a member")
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

func (s *ClientService) UpdateClientMemberRole(clientID, requesterID, targetUserID, newRoleID uuid.UUID) error {
	myRole, err := s.clientRepo.GetMemberRole(clientID, requesterID)
	if err != nil {
		return fmt.Errorf("requester is not a member")
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
	return s.clientRepo.UpdateMemberRole(clientID, targetUserID, newRoleID)
}

func (s *ClientService) ListClientMembers(targetClientID, requesterID uuid.UUID) ([]models.ClientMember, error) {
	allowed, _ := s.clientRepo.CheckAccessRecursive(requesterID, targetClientID)
	if !allowed {
		return nil, fmt.Errorf("access denied: user is not a member of this client hierarchy")
	}
	return s.clientRepo.GetMembers(targetClientID)
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
		path, presignedURL, err := s.rs.UploadClientLogo(logoFile, logoHeader, client.ID)
		if err != nil {
			return client, fmt.Errorf("client created but logo failed: %w", err)
		}
		client.Logo = path
		if err := s.clientRepo.UpdateClient(client); err != nil {
			return client, fmt.Errorf("failed to save logo path: %w", err)
		}
		client.Logo = presignedURL
	}
	return client, nil
}
