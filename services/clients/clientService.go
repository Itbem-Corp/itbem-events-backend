package clients

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/configuration/constants"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/internal/products"
	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/services/ports"
	ResourceService "events-stocks/services/resources"
	"events-stocks/utils"
	"fmt"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"log/slog"
	"mime/multipart"
	"strings"
)

// Sentinel errors let controllers map organization failures to stable HTTP
// statuses and error codes instead of string-matching or returning 500.
// The texts are kept identical to the historical messages.
var (
	ErrClientAccessDenied     = errors.New("access denied")
	ErrClientPermissionDenied = errors.New("permission denied")
	ErrClientNotFound         = errors.New("client not found")
	ErrClientHasChildren      = errors.New("cannot delete organization while it has sub-organizations")

	// Creation rule errors. Each one wraps the historical English detail so
	// existing logs and tests keep matching, while the controller maps the
	// sentinel to a 4xx with a Spanish message instead of a generic 500.
	ErrClientTypeInvalid         = errors.New("invalid client type")
	ErrClientRootTypeRequired    = errors.New("root organization type required")
	ErrClientParentNotFound      = errors.New("parent organization not found")
	ErrClientChildTypeNotAllowed = errors.New("organization type not allowed under parent")
	ErrClientHierarchyLimit      = errors.New("organization hierarchy limit reached")
	ErrClientCodeConflict        = errors.New("organization code already exists")
	ErrClientLogoUpload          = errors.New("organization logo upload failed")
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

// AuthorizeMemberTarget is the entry point for controllers outside the
// clients domain (member application toggles, event assignments).
func AuthorizeMemberTarget(requester *models.User, clientID, targetUserID uuid.UUID, lookupUser UserLookup) error {
	if _clientSvc == nil {
		return fmt.Errorf("client service is not configured")
	}
	return _clientSvc.AuthorizeMemberTarget(requester, clientID, targetUserID, lookupUser)
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
	tenantCode     string
	// applicationCode is the session product of a request-scoped service
	// (WithApplicationScope). When set, every organization check by id also
	// requires the application-scoped reach (portal stop, ROLES-N2).
	applicationCode string
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

func (s *ClientService) WithResourceBucket(bucket string) *ClientService {
	if s == nil || s.rs == nil {
		return s
	}
	clone := *s
	clone.rs = s.rs.WithBucket(bucket)
	return &clone
}

// WithTenantScope creates an immutable request-scoped service. Tenant identity
// is included in user-dependent cache keys so a Cognito identity shared across
// applications cannot receive cached data from another entrypoint.
func (s *ClientService) WithTenantScope(tenantCode, bucket string) *ClientService {
	if s == nil {
		return nil
	}
	clone := *s
	clone.tenantCode = normalizeTenantCode(tenantCode)
	if clone.rs != nil {
		clone.rs = clone.rs.WithBucket(bucket)
	}
	return &clone
}

// WithApplicationScope returns a request-scoped copy whose organization
// checks by id stay inside applicationCode: Owner/Admin inheritance does not
// reach another application's portal tree, the same boundary as the
// organization lists and the session. An empty code keeps the unscoped
// behaviour (internal callers that check a third user's membership).
func (s *ClientService) WithApplicationScope(applicationCode string) *ClientService {
	if s == nil {
		return nil
	}
	clone := *s
	clone.applicationCode = strings.ToLower(strings.TrimSpace(applicationCode))
	return &clone
}

// checkAccess is CheckAccessRecursive plus, for an application-scoped
// service, the application reach. It fails closed when the repository cannot
// answer the reach question.
func (s *ClientService) checkAccess(userID, clientID uuid.UUID) (bool, string) {
	allowed, role := s.clientRepo.CheckAccessRecursive(userID, clientID)
	if !allowed || s.applicationCode == "" {
		return allowed, role
	}
	reach, ok := s.clientRepo.(ports.ApplicationClientReach)
	if !ok {
		return false, ""
	}
	reachable, err := reach.ReachableClientIDsInApplication(userID, s.applicationCode, []uuid.UUID{clientID})
	if err != nil || !reachable[clientID] {
		return false, ""
	}
	return allowed, role
}

func normalizeTenantCode(value string) string {
	return products.NormalizeOrDefault(value).String()
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
		return nil, fmt.Errorf("%w: invalid client type id: %v", ErrClientTypeInvalid, err)
	}
	if parentID == nil {
		if requestedType.Code != "PLATFORM" {
			return nil, fmt.Errorf("%w: root clients must be of type PLATFORM", ErrClientRootTypeRequired)
		}
	} else {
		if !primaryRoot {
			allowed, role := s.checkAccess(ownerUserID, *parentID)
			roleUpper := strings.ToUpper(role)
			if !allowed || (roleUpper != "OWNER" && roleUpper != "INHERITED_OWNER") {
				return nil, fmt.Errorf("%w: owner access required on parent organization", ErrClientPermissionDenied)
			}
		}
		parent, err := s.clientRepo.GetClientByID(*parentID)
		if err != nil {
			return nil, fmt.Errorf("%w: parent client not found", ErrClientParentNotFound)
		}
		allowedChildren := models.AllowedChildClientTypeCodes(parent.ClientType.Code)
		if len(allowedChildren) == 0 {
			return nil, fmt.Errorf("%w: customers cannot have sub-clients", ErrClientHierarchyLimit)
		}
		if !containsFold(allowedChildren, requestedType.Code) {
			if strings.EqualFold(parent.ClientType.Code, models.ClientTypeCodePlatform) {
				return nil, fmt.Errorf("%w: platforms can only create agencies", ErrClientChildTypeNotAllowed)
			}
			return nil, fmt.Errorf("%w: agencies can only create customers", ErrClientChildTypeNotAllowed)
		}
	}
	ownerRole, err := s.clientRoleRepo.GetByCode("Owner")
	if err != nil {
		return nil, fmt.Errorf("system configuration error: 'Owner' role not found in database")
	}
	clientID, err := uuid.NewV4()
	if err != nil {
		return nil, fmt.Errorf("failed to generate client id: %w", err)
	}
	slug, err := s.uniqueClientCode(name)
	if err != nil {
		return nil, err
	}
	mediaBucket := ""
	if s.rs != nil {
		mediaBucket = s.rs.Bucket
	}
	client := &models.Client{
		ID:           clientID,
		Name:         name,
		Code:         slug,
		ClientTypeID: clientTypeID,
		ParentID:     parentID,
		MediaBucket:  mediaBucket,
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
			if isUniqueViolation(err) {
				return nil, fmt.Errorf("%w: %v", ErrClientCodeConflict, err)
			}
			return nil, fmt.Errorf("failed to create client and assign owner: %w", err)
		}
	} else {
		if err := s.clientRepo.CreateClient(client); err != nil {
			if isUniqueViolation(err) {
				return nil, fmt.Errorf("%w: %v", ErrClientCodeConflict, err)
			}
			return nil, err
		}
		if err := s.clientRepo.AddMember(member); err != nil {
			return nil, fmt.Errorf("failed to assign owner: %w", err)
		}
	}
	s.invalidateMyClients(ownerUserID)
	return client, nil
}

// CreatableClientTypeCodes returns the organization type codes the requester
// may create. With a parent it is exactly the types allowed under that parent
// (after the same Owner check as creation). Without a parent, a primary root
// gets every type, and anybody else gets the union of the types they
// may create under every organization they own (directly or inherited), so an
// Agency owner is offered "Customer", never "Platform".
func (s *ClientService) CreatableClientTypeCodes(requesterID uuid.UUID, parentID *uuid.UUID, primaryRoot bool) ([]string, error) {
	if parentID != nil {
		if !primaryRoot {
			allowed, role := s.checkAccess(requesterID, *parentID)
			roleUpper := strings.ToUpper(role)
			if !allowed || (roleUpper != "OWNER" && roleUpper != "INHERITED_OWNER") {
				return nil, fmt.Errorf("%w: owner access required on parent organization", ErrClientPermissionDenied)
			}
		}
		parent, err := s.clientRepo.GetClientByID(*parentID)
		if err != nil {
			return nil, fmt.Errorf("%w: parent client not found", ErrClientParentNotFound)
		}
		return models.AllowedChildClientTypeCodes(parent.ClientType.Code), nil
	}
	if primaryRoot {
		// Root 1 provisions every level; the parent is validated on create.
		return []string{models.ClientTypeCodePlatform, models.ClientTypeCodeAgency, models.ClientTypeCodeCustomer}, nil
	}
	owned, _, err := s.clientRepo.ListClientsPaginated(&requesterID, dtos.ClientsListQuery{Page: 1, PageSize: 1000})
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	codes := make([]string, 0, 2)
	for _, client := range owned {
		role := strings.ToUpper(client.AccessRole)
		if role != "OWNER" && role != "INHERITED_OWNER" {
			continue
		}
		for _, code := range models.AllowedChildClientTypeCodes(client.ClientType.Code) {
			if _, ok := seen[code]; !ok {
				seen[code] = struct{}{}
				codes = append(codes, code)
			}
		}
	}
	return codes, nil
}

// maxClientCodeAttempts bounds the numbered-suffix search for a free code.
const maxClientCodeAttempts = 50

// uniqueClientCode derives the organization's stable code from its name and,
// when the repository can check it, appends "-2", "-3"... until the code is
// free (also avoiding application codes, which carry product meaning). The
// unique index stays the final guard; a lost race is ErrClientCodeConflict.
func (s *ClientService) uniqueClientCode(name string) (string, error) {
	base := utils.Slugify(name)
	if base == "" {
		base = "organizacion"
	}
	checker, ok := s.clientRepo.(ports.ClientCodeRepository)
	if !ok {
		return base, nil
	}
	candidate := base
	for attempt := 2; attempt <= maxClientCodeAttempts+1; attempt++ {
		taken, err := checker.ClientCodeTaken(candidate)
		if err != nil {
			return "", fmt.Errorf("check organization code: %w", err)
		}
		if !taken {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, attempt)
	}
	return "", fmt.Errorf("%w: no free code for %q", ErrClientCodeConflict, base)
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "duplicate key value") || strings.Contains(message, "SQLSTATE 23505")
}

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
}

func (s *ClientService) GetClientDetails(clientID, userID uuid.UUID) (*models.Client, error) {
	allowed, _ := s.checkAccess(userID, clientID)
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
	if err := s.authorizeClientUpdate(clientID, requesterID, primaryRoot); err != nil {
		return nil, err
	}
	client, err := s.clientRepo.GetClientByID(clientID)
	if err != nil {
		return nil, ErrClientNotFound
	}
	oldLogo := client.Logo
	oldMediaBucket := client.MediaBucket
	// The code is a stable identifier: it is matched against application
	// codes (membership application resolution) and used by integrations, so
	// renaming an organization changes only its display name.
	if trimmedName := strings.TrimSpace(name); trimmedName != "" {
		client.Name = trimmedName
	}
	if shouldDeleteOld || logoName != "" {
		client.Logo = logoName
		if logoName != "" && s.rs != nil {
			client.MediaBucket = s.rs.Bucket
		}
	}
	if err := s.clientRepo.UpdateClient(client); err != nil {
		if logoName != "" && logoName != oldLogo && s.rs != nil {
			s.cleanupClientLogo(client.ID, logoName)
		}
		return nil, err
	}
	if shouldDeleteOld && oldLogo != "" && oldLogo != logoName && s.rs != nil {
		if err := s.rs.WithBucket(oldMediaBucket).DeleteObjectByPath(clientLogoObjectPath(client.ID, oldLogo)); err != nil {
			slog.Warn("failed to delete replaced client logo", "client_id", client.ID, "logo", oldLogo, "error", err)
		}
	}
	s.invalidateAllMyClients()
	return client, nil
}

// authorizeClientUpdate is the single OWNER check for organization edits.
// It must run before any side effect (logo upload) happens.
func (s *ClientService) authorizeClientUpdate(clientID, requesterID uuid.UUID, primaryRoot bool) error {
	if primaryRoot {
		return nil
	}
	allowed, role := s.checkAccess(requesterID, clientID)
	roleUpper := strings.ToUpper(role)
	if !allowed || (roleUpper != "OWNER" && roleUpper != "INHERITED_OWNER") {
		return ErrClientPermissionDenied
	}
	return nil
}

// UpdateClientWithLogo authorizes the requester and confirms the organization
// exists BEFORE uploading a new logo, so an unauthorized caller can never
// write objects under the organization's branding prefix. Any failure after
// the upload removes the freshly uploaded object. primaryRoot must only be
// true for a Root 1 caller (the controller decides that).
func (s *ClientService) UpdateClientWithLogo(
	clientID, requesterID uuid.UUID,
	primaryRoot bool,
	name string,
	logoFile multipart.File,
	logoHeader *multipart.FileHeader,
	removeLogo bool,
) (*models.Client, error) {
	if err := s.authorizeClientUpdate(clientID, requesterID, primaryRoot); err != nil {
		return nil, err
	}
	if _, err := s.clientRepo.GetClientByID(clientID); err != nil {
		return nil, ErrClientNotFound
	}
	logoName := ""
	shouldDeleteOld := false
	if logoFile != nil {
		filename, err := s.UploadClientLogo(logoFile, logoHeader, clientID)
		if err != nil {
			return nil, err
		}
		logoName = filename
		shouldDeleteOld = true
	} else if removeLogo {
		shouldDeleteOld = true
	}
	client, err := s.updateClientDetails(clientID, requesterID, name, logoName, shouldDeleteOld, primaryRoot)
	if err != nil {
		// updateClientDetails already removes the new object when the row
		// update fails; cover the re-check failures (access revoked or the
		// organization deleted between the check and the update) here.
		if logoName != "" && (errors.Is(err, ErrClientPermissionDenied) || errors.Is(err, ErrClientNotFound)) {
			s.cleanupClientLogo(clientID, logoName)
		}
		return nil, err
	}
	return client, nil
}

func (s *ClientService) myClientsKey(userID uuid.UUID) string {
	return cacheutil.TenantKey(s.tenantCode, "user:"+userID.String(), "myclients")
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
	client, err := s.clientRepo.GetClientByID(clientID)
	if err != nil {
		return ""
	}
	return s.rs.WithBucket(client.MediaBucket).GetClientLogoURL(clientID, logoName)
}

func (s *ClientService) cleanupClientLogo(clientID uuid.UUID, logoName string) {
	if s == nil || s.rs == nil || strings.TrimSpace(logoName) == "" {
		return
	}
	if err := s.rs.DeleteObjectByPath(clientLogoObjectPath(clientID, logoName)); err != nil {
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
		_ = s.cache.Invalidate("myclients", cacheutil.TenantNamespace(s.tenantCode, "user:"+userID.String()))
	}
}

func (s *ClientService) invalidateAllMyClients() {
	if s.cache != nil {
		_ = s.cache.DeleteKeysByPattern(context.Background(), "v1:tenant:*:user:*:myclients")
	}
}

func (s *ClientService) GetClientChildren(parentID, userID uuid.UUID) ([]models.Client, error) {
	allowed, role := s.checkAccess(userID, parentID)
	role = strings.TrimPrefix(strings.ToUpper(role), "INHERITED_")
	if !allowed || (role != "OWNER" && role != "ADMIN") {
		return nil, fmt.Errorf("access denied")
	}
	return s.clientRepo.GetChildrenClients(parentID)
}

// GetClientChildrenInApplication is GetClientChildren for a member of one
// application session: the parent must be inside the application's reach
// and children that are another application's portal (with their trees) are
// left out, the same boundary as GET /api/clients (ROLES-N2).
func (s *ClientService) GetClientChildrenInApplication(parentID, userID uuid.UUID, applicationCode string) ([]models.Client, error) {
	children, err := s.GetClientChildren(parentID, userID)
	if err != nil {
		return nil, err
	}
	reach, ok := s.clientRepo.(ports.ApplicationClientReach)
	if !ok || strings.TrimSpace(applicationCode) == "" {
		return children, nil
	}
	ids := make([]uuid.UUID, 0, len(children)+1)
	ids = append(ids, parentID)
	for _, child := range children {
		ids = append(ids, child.ID)
	}
	reachable, err := reach.ReachableClientIDsInApplication(userID, applicationCode, ids)
	if err != nil {
		return nil, err
	}
	if !reachable[parentID] {
		return nil, fmt.Errorf("access denied")
	}
	scoped := make([]models.Client, 0, len(children))
	for _, child := range children {
		if reachable[child.ID] {
			scoped = append(scoped, child)
		}
	}
	return scoped, nil
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
		allowed, role := s.checkAccess(requesterID, clientID)
		if !allowed {
			return ErrClientAccessDenied
		}
		roleUpper := strings.ToUpper(role)
		if roleUpper != "OWNER" && roleUpper != "INHERITED_OWNER" {
			return fmt.Errorf("%w: only the owner can delete the organization", ErrClientPermissionDenied)
		}
	}
	children, err := s.clientRepo.GetChildrenClients(clientID)
	if err != nil {
		return fmt.Errorf("failed to verify organization hierarchy: %w", err)
	}
	if len(children) > 0 {
		return ErrClientHasChildren
	}
	client, err := s.clientRepo.GetClientByID(clientID)
	if err != nil {
		return ErrClientNotFound
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
	// The logo is removed only after the rows are gone: a failed or rolled
	// back delete must never leave a live organization without its logo.
	if client.Logo != "" && s.rs != nil {
		if err := s.rs.WithBucket(client.MediaBucket).DeleteObjectByPath(clientLogoObjectPath(client.ID, client.Logo)); err != nil {
			slog.Warn("failed to delete logo of deleted client", "client_id", client.ID, "error", err)
		}
	}
	s.invalidateAllMyClients()
	return nil
}

func clientLogoObjectPath(clientID uuid.UUID, logo string) string {
	logo = strings.TrimSpace(logo)
	if strings.Contains(logo, "/") {
		return strings.TrimLeft(logo, "/")
	}
	return fmt.Sprintf("clients/%s/logo/%s", clientID, logo)
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
	allowed, role := s.checkAccess(requesterID, clientID)
	roleUpper := strings.ToUpper(role)
	if !allowed || (roleUpper != "OWNER" && roleUpper != "ADMIN" && roleUpper != "INHERITED_OWNER" && roleUpper != "INHERITED_ADMIN") {
		return fmt.Errorf("permission denied: cannot manage members for this client")
	}
	return nil
}

func (s *ClientService) CanAssignClientRole(clientID, requesterID, roleID uuid.UUID) error {
	allowed, roleCode := s.checkAccess(requesterID, clientID)
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

// CanAssignClientRoleAsRoot is the platform-root counterpart of
// CanAssignClientRole. No root assigns Owner from member management, and a
// root assigns only roles whose capabilities its own level holds
// (authz.PlatformRootMayGrantRole): a Root 2 can never hand an account it
// controls the event or organization powers it lacks itself (Admin, Event
// manager, Editor). A requester that is not a platform root is denied.
func (s *ClientService) CanAssignClientRoleAsRoot(requester *models.User, roleID uuid.UUID) error {
	targetRole, err := s.clientRoleRepo.GetByID(roleID)
	if err != nil {
		return fmt.Errorf("invalid role")
	}
	if targetRole.Hierarchy <= 1 {
		return fmt.Errorf("permission denied: the owner role cannot be assigned from member management")
	}
	if !authz.PlatformRootMayGrantRole(requester, targetRole.Code) {
		return fmt.Errorf("permission denied: your platform administrator level cannot assign a role with more capabilities than its own")
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
	allowed, roleCode := s.checkAccess(userID, clientID)
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
	allowed, roleCode := s.checkAccess(requesterID, clientID)
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

// ErrMemberTargetDenied is returned by AuthorizeMemberTarget when the
// requester may not change the target member's access.
var ErrMemberTargetDenied = errors.New("permission denied")

// UserLookup loads a user account by ID (users.GetUserByID in production).
type UserLookup func(userID uuid.UUID) (*models.User, error)

// AuthorizeMemberTarget applies the delegation invariant to actions that
// change another member's access inside clientID without going through the
// direct-membership rank checks (application entitlements, event assignments,
// and adding an existing account with InviteUser, whose new direct
// membership would take precedence over an inherited Owner/Admin role). The
// caller must already have checked members:manage on clientID.
//
//   - A primary root may act on any member.
//   - Nobody else may act on a platform-root account.
//   - An operational root may act on any member except an Owner.
//   - An organization Owner/Admin may act only on members whose effective
//     role in clientID ranks strictly below its own (the CanAssignClientRole
//     rule), which also excludes itself.
//
// A target without a user row or without access to clientID has no rank to
// protect; the caller's own validation decides what happens to it. Lookup
// failures are returned unwrapped so callers can tell them from a denial.
func (s *ClientService) AuthorizeMemberTarget(requester *models.User, clientID, targetUserID uuid.UUID, lookupUser UserLookup) error {
	if requester == nil {
		return fmt.Errorf("%w: unknown requester", ErrMemberTargetDenied)
	}
	if requester.IsPrimaryRoot() {
		return nil
	}
	if lookupUser == nil {
		return fmt.Errorf("member target lookup is not configured")
	}
	target, err := lookupUser(targetUserID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("load target user: %w", err)
	}
	if err == nil && target != nil && target.IsPlatformAdmin() {
		return fmt.Errorf("%w: platform-root accounts are managed only by a primary root", ErrMemberTargetDenied)
	}
	targetRole, err := s.memberTargetRole(clientID, targetUserID)
	if err != nil {
		return err
	}
	if requester.IsPlatformAdmin() {
		if targetRole != nil && targetRole.Hierarchy <= 1 {
			return fmt.Errorf("%w: the organization owner's access cannot be changed", ErrMemberTargetDenied)
		}
		return nil
	}
	myRole, err := s.getEffectiveManagerRole(clientID, requester.ID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMemberTargetDenied, err)
	}
	if targetUserID == requester.ID || (targetRole != nil && myRole.Hierarchy >= targetRole.Hierarchy) {
		return fmt.Errorf("%w: you cannot change the access of a member with equal or higher hierarchy", ErrMemberTargetDenied)
	}
	return nil
}

// memberTargetRole resolves the target's effective (direct or inherited)
// role in clientID, or nil when it has no access there.
func (s *ClientService) memberTargetRole(clientID, targetUserID uuid.UUID) (*models.ClientRole, error) {
	allowed, roleCode := s.clientRepo.CheckAccessRecursive(targetUserID, clientID)
	if !allowed || roleCode == "" {
		return nil, nil
	}
	code := strings.TrimPrefix(strings.ToUpper(roleCode), "INHERITED_")
	role, err := s.clientRoleRepo.GetByCode(code)
	if err != nil || role == nil {
		return nil, fmt.Errorf("resolve target member role %q: %v", code, err)
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

// UpdateClientMemberRoleAsRoot re-roles a member on the platform path: never
// an Owner, and only to a role CanAssignClientRoleAsRoot lets requester grant.
func (s *ClientService) UpdateClientMemberRoleAsRoot(requester *models.User, clientID, targetUserID, newRoleID uuid.UUID) error {
	targetRole, err := s.clientRepo.GetMemberRole(clientID, targetUserID)
	if err != nil {
		return fmt.Errorf("target user is not a member")
	}
	if targetRole.Hierarchy <= 1 {
		return fmt.Errorf("permission denied: the organization owner cannot be modified")
	}
	if err := s.CanAssignClientRoleAsRoot(requester, newRoleID); err != nil {
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
			return nil, fmt.Errorf("%w: failed to create client logo: %w", ErrClientLogoUpload, err)
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
		return nil, fmt.Errorf("%w: failed to create client logo: %w", ErrClientLogoUpload, err)
	}
	client.Logo = path
	if err := s.clientRepo.UpdateClient(client); err != nil {
		s.rollbackCreatedClient(client, ownerUserID, path)
		return nil, fmt.Errorf("failed to save logo path: %w", err)
	}
	client.Logo = presignedURL
	return client, nil
}

// CreatableClientTypeCodes is the package-level wrapper over the DI instance.
func CreatableClientTypeCodes(requesterID uuid.UUID, parentID *uuid.UUID, primaryRoot bool) ([]string, error) {
	if _clientSvc == nil {
		return nil, fmt.Errorf("client service not initialized")
	}
	return _clientSvc.CreatableClientTypeCodes(requesterID, parentID, primaryRoot)
}
