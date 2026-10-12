package clients

import (
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/internal/products"
	"events-stocks/internal/tenantresources"
	"events-stocks/middleware/applicationaccess"
	"events-stocks/models"
	"events-stocks/services/applications"
	"events-stocks/services/clients"
	resourcesService "events-stocks/services/resources"
	"events-stocks/services/users"
	validations "events-stocks/services/validations"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

var clientSvc *clients.ClientService

// Stable error codes returned in the `error` field so the dashboard can map
// them without parsing English or Go error text.
const (
	ErrCodeClientHasChildren      = "CLIENT_HAS_CHILDREN"
	ErrCodeClientPermissionDenied = "CLIENT_PERMISSION_DENIED"
	ErrCodeClientNotFound         = "CLIENT_NOT_FOUND"
	ErrCodeClientTypeInvalid      = "CLIENT_TYPE_INVALID"
	ErrCodeClientTypeNotAllowed   = "CLIENT_TYPE_NOT_ALLOWED"
	ErrCodeClientHierarchyLimit   = "CLIENT_HIERARCHY_LIMIT"
	ErrCodeClientParentNotFound   = "CLIENT_PARENT_NOT_FOUND"
	ErrCodeClientCodeConflict     = "CLIENT_CODE_CONFLICT"
	ErrCodeClientNameRequired     = "CLIENT_NAME_REQUIRED"
)

func InitClientsController(svc *clients.ClientService) {
	clientSvc = svc
}

func clientServiceForContext(c echo.Context) *clients.ClientService {
	bucket, err := tenantresources.BucketFromContext(c)
	if err != nil {
		return clientSvc
	}
	tenantCode, _ := c.Get("tenant_code").(string)
	return clientSvc.WithTenantScope(tenantCode, bucket)
}

// clientAuthzService is the client service for an organization check by id
// on behalf of user: members stay inside their session application (no other
// product's portal tree, ROLES-N2), platform admins keep their behaviour.
func clientAuthzService(c echo.Context, user *models.User) *clients.ClientService {
	return withApplicationScope(c, user, clientSvc)
}

func withApplicationScope(c echo.Context, user *models.User, svc *clients.ClientService) *clients.ClientService {
	if svc == nil || user == nil || user.IsPlatformAdmin() {
		return svc
	}
	tenantCode, _ := c.Get("tenant_code").(string)
	return svc.WithApplicationScope(sessionApplicationCode(c, strings.ToLower(strings.TrimSpace(tenantCode))))
}

func operationalRootMayNotManageOrganization(c echo.Context, user *models.User) bool {
	if user.EffectiveRootLevel() != models.RootLevelOperational {
		return false
	}
	_ = utils.Error(c, http.StatusForbidden, "Permission Denied", "Operational roots cannot change organization structure")
	return true
}

// platformTeamManager decides whether a member write takes the platform path:
// any organization, no membership required, and the *AsRoot service rules
// that never assign, change or remove an Owner and grant only roles within
// the requester's own capabilities (so Root 2 never grants Admin, Event
// manager or Editor). Both root levels manage teams (members:manage); a
// Root 2 is additionally kept away from platform-root accounts by
// operationalRootMayNotManageRootTarget.
func platformTeamManager(user *models.User) bool {
	return user.IsPlatformAdmin()
}

func operationalRootMayNotManageRootTarget(c echo.Context, requester *models.User, targetUserID uuid.UUID) bool {
	if requester.EffectiveRootLevel() != models.RootLevelOperational {
		return false
	}
	target, err := users.GetUserByID(targetUserID)
	if err != nil {
		_ = utils.Error(c, http.StatusNotFound, "User not found", err.Error())
		return true
	}
	if target.IsPlatformAdmin() {
		_ = utils.Error(c, http.StatusForbidden, "Permission Denied", "Operational roots cannot modify platform-root accounts")
		return true
	}
	return false
}

func CreateNewClient(c echo.Context) error {
	_, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "Invalid token")
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}
	if operationalRootMayNotManageOrganization(c, user) {
		return nil
	}

	name := strings.TrimSpace(c.FormValue("name"))
	clientTypeIDStr := c.FormValue("client_type_id")
	parentIDStr := c.FormValue("parent_id")

	if name == "" {
		return utils.Error(c, http.StatusBadRequest, "El nombre de la organización es obligatorio", ErrCodeClientNameRequired)
	}

	clientTypeID, err := uuid.FromString(clientTypeIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", "client_type_id must be a valid UUID")
	}

	var parentID *uuid.UUID
	if parentIDStr != "" && parentIDStr != "null" {
		u, err := uuid.FromString(parentIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid parent_id", "parent_id must be a valid UUID")
		}
		parentID = &u
	}
	if parentID == nil && !user.IsPrimaryRoot() {
		return utils.Error(c, http.StatusForbidden, "Permission Denied", "Only the primary platform administrator can create a platform organization")
	}

	file, header, err := c.Request().FormFile("logo")
	if err == nil {
		defer file.Close()
	}

	var newClient *models.Client
	scopedClientSvc := withApplicationScope(c, user, clientServiceForContext(c))
	if user.IsPrimaryRoot() {
		newClient, err = scopedClientSvc.CreateClientWithLogoAsPrimaryRoot(name, clientTypeID, user.ID, parentID, file, header)
	} else {
		newClient, err = scopedClientSvc.CreateClientWithLogo(name, clientTypeID, user.ID, parentID, file, header)
	}
	if err != nil {
		return respondCreateClientError(c, err)
	}

	return utils.Success(c, http.StatusCreated, "Client created", dtos.NewClientResponse(newClient))
}

// respondCreateClientError maps organization-creation failures to the right
// status. Only logo failures use the upload messages; rule violations are 4xx
// with a Spanish message and a stable code, and anything else is a plain 500
// that does not pretend to be an upload problem.
func respondCreateClientError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, clients.ErrClientPermissionDenied):
		return utils.Error(c, http.StatusForbidden, "No tienes permiso para crear organizaciones dentro de la organización seleccionada", ErrCodeClientPermissionDenied)
	case errors.Is(err, clients.ErrClientParentNotFound):
		return utils.Error(c, http.StatusNotFound, "La organización superior no existe", ErrCodeClientParentNotFound)
	case errors.Is(err, clients.ErrClientHierarchyLimit):
		return utils.Error(c, http.StatusBadRequest, "Las organizaciones de tipo Cliente no pueden tener sub-organizaciones. La jerarquía permitida es Plataforma › Agencia › Cliente.", ErrCodeClientHierarchyLimit)
	case errors.Is(err, clients.ErrClientChildTypeNotAllowed):
		return utils.Error(c, http.StatusBadRequest, "Ese tipo de organización no se puede crear dentro de la organización seleccionada", ErrCodeClientTypeNotAllowed)
	case errors.Is(err, clients.ErrClientRootTypeRequired):
		return utils.Error(c, http.StatusBadRequest, "Una organización sin organización superior debe ser de tipo Plataforma", ErrCodeClientTypeNotAllowed)
	case errors.Is(err, clients.ErrClientTypeInvalid):
		return utils.Error(c, http.StatusBadRequest, "El tipo de organización no es válido", ErrCodeClientTypeInvalid)
	case errors.Is(err, clients.ErrClientCodeConflict):
		return utils.Error(c, http.StatusConflict, "Ya existe una organización con un identificador igual; inténtalo de nuevo con otro nombre", ErrCodeClientCodeConflict)
	case resourcesService.IsInvalidMediaFile(err):
		return utils.Error(c, http.StatusBadRequest, resourcesService.InvalidMediaFileMessage, resourcesService.InvalidMediaFileCode)
	case validations.IsValidationError(err):
		status, detail := resourcesService.UploadErrorResponse(err)
		return utils.Error(c, status, "Invalid logo", detail)
	case errors.Is(err, clients.ErrClientLogoUpload):
		status, detail := resourcesService.UploadErrorResponse(err)
		return utils.Error(c, status, "Failed to create client", detail)
	default:
		slog.Error("organization creation failed", "error", err)
		return utils.Error(c, http.StatusInternalServerError, "No se pudo crear la organización", "CLIENT_CREATE_FAILED")
	}
}

func GetClient(c echo.Context) error {
	_, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	user, err := authz.CurrentUser(c)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	clientIDStr := c.Param("id")
	clientID, err := uuid.FromString(clientIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid Client ID", err.Error())
	}

	var client *models.Client
	if user.IsPrimaryRoot() {
		client, err = clientSvc.GetClientDetailsAsPrimaryRoot(clientID)
	} else {
		client, err = clientAuthzService(c, user).GetClientDetails(clientID, user.ID)
	}
	if err != nil {
		if errors.Is(err, clients.ErrClientAccessDenied) {
			return utils.Error(c, http.StatusForbidden, "Access Denied", "You do not have permission to view this organization")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error fetching client", err.Error())
	}
	if client.Logo != "" {
		client.Logo = clientSvc.GetClientLogoURL(client.ID, client.Logo)
	}

	return utils.Success(c, http.StatusOK, "Client details", dtos.NewClientResponse(client))
}

func ListMyClients(c echo.Context) error {
	_, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	user, err := authz.CurrentUser(c)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}
	tenantCode, _ := c.Get("tenant_code").(string)
	tenantCode = strings.ToLower(strings.TrimSpace(tenantCode))
	// A member (not a platform admin) only lists the organizations their
	// application session can enter: Owner/Admin inheritance stops at another
	// product's portal organization (ROLES-N2). Platform admins keep the
	// platform-wide list.
	applicationCode := ""
	if !user.IsPlatformAdmin() {
		applicationCode = sessionApplicationCode(c, tenantCode)
	}
	if products.AllowsPlatformAuthority(tenantCode) {
		tenantCode = ""
	}
	if c.QueryParam("page_size") != "" {
		page, err := strconv.Atoi(c.QueryParam("page"))
		if err != nil || page < 1 {
			page = 1
		}
		pageSize, err := strconv.Atoi(c.QueryParam("page_size"))
		if err != nil || pageSize < 1 || pageSize > 100 {
			return utils.Error(c, http.StatusBadRequest, "Invalid page_size", "page_size must be between 1 and 100")
		}
		var userID *uuid.UUID
		if !user.IsPlatformAdmin() {
			userID = &user.ID
		}
		query := dtos.ClientsListQuery{
			Page: page, PageSize: pageSize,
			Search:          strings.TrimSpace(c.QueryParam("search")),
			TenantCode:      tenantCode,
			ApplicationCode: applicationCode,
		}
		myClients, total, err := clientSvc.ListClientsPaginated(userID, query)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error fetching clients", err.Error())
		}
		for i := range myClients {
			if myClients[i].Logo != "" {
				myClients[i].Logo = clientSvc.GetClientLogoURL(myClients[i].ID, myClients[i].Logo)
			}
		}
		totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
		return utils.Success(c, http.StatusOK, "User clients", dtos.ClientsPageResponse{
			Data: dtos.NewClientResponses(myClients), Total: total, Page: page, PageSize: pageSize, TotalPages: totalPages,
		})
	}

	if tenantCode != "" || applicationCode != "" {
		myClients, _, err := clientSvc.ListClientsPaginated(
			&user.ID,
			dtos.ClientsListQuery{Page: 1, PageSize: 1000, TenantCode: tenantCode, ApplicationCode: applicationCode},
		)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error fetching clients", err.Error())
		}
		for i := range myClients {
			if myClients[i].Logo != "" {
				myClients[i].Logo = clientSvc.GetClientLogoURL(myClients[i].ID, myClients[i].Logo)
			}
		}
		return utils.Success(c, http.StatusOK, "User clients", dtos.NewClientResponses(myClients))
	}

	var myClients []models.Client
	if user.IsPlatformAdmin() {
		myClients, err = clients.GetAllClients()
	} else {
		myClients, err = clientServiceForContext(c).GetMyClients(user.ID)
	}
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching clients", err.Error())
	}

	for i := range myClients {
		if myClients[i].Logo != "" {
			myClients[i].Logo = clientSvc.GetClientLogoURL(myClients[i].ID, myClients[i].Logo)
		}
	}

	return utils.Success(c, http.StatusOK, "User clients", dtos.NewClientResponses(myClients))
}

// sessionApplicationCode is the product of the authenticated application
// session (set by applicationaccess.Require), falling back to the resolved
// tenant. Empty when neither names a known product.
func sessionApplicationCode(c echo.Context, tenantCode string) string {
	if session, ok := c.Get(applicationaccess.ContextKey).(*applications.Session); ok && session != nil {
		if code := strings.ToLower(strings.TrimSpace(session.Application.Code)); code != "" {
			return code
		}
	}
	if definition, ok := products.Resolve(tenantCode); ok {
		return definition.Code.String()
	}
	return ""
}

func GetMySubClients(c echo.Context) error {
	_, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	user, err := authz.CurrentUser(c)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	parentIDStr := c.QueryParam("parent_id")
	if parentIDStr == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing parent_id", "You must provide parent_id query parameter")
	}

	parentID, err := uuid.FromString(parentIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid Parent ID", err.Error())
	}

	var children []models.Client
	switch {
	case user.IsPrimaryRoot():
		children, err = clientSvc.GetClientChildrenAsPrimaryRoot(parentID)
	case user.IsPlatformAdmin():
		children, err = clientSvc.GetClientChildren(parentID, user.ID)
	default:
		// Members stay inside their application session: no other product's
		// portal organizations (ROLES-N2 follow-up).
		tenantCode, _ := c.Get("tenant_code").(string)
		children, err = clientSvc.GetClientChildrenInApplication(parentID, user.ID, sessionApplicationCode(c, strings.ToLower(strings.TrimSpace(tenantCode))))
	}
	if err != nil {
		return utils.Error(c, http.StatusForbidden, "Access Denied", "You do not have permission to view children of this organization")
	}

	for i := range children {
		if children[i].Logo != "" {
			children[i].Logo = clientSvc.GetClientLogoURL(children[i].ID, children[i].Logo)
		}
	}

	return utils.Success(c, http.StatusOK, "Sub-clients list", dtos.NewClientResponses(children))
}

func InviteUser(c echo.Context) error {
	_, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "Invalid token")
	}
	requester, err := authz.CurrentUser(c)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}
	var req struct {
		ClientID uuid.UUID `json:"client_id"`
		UserID   uuid.UUID `json:"user_id"`
		Email    string    `json:"email"`
		RoleID   uuid.UUID `json:"role_id"`
	}
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", err.Error())
	}
	if req.ClientID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", "client_id is required")
	}
	if req.RoleID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", "role_id is required")
	}

	if !platformTeamManager(requester) {
		if err := clientAuthzService(c, requester).CanManageClientMembers(req.ClientID, requester.ID); err != nil {
			return utils.Error(c, http.StatusForbidden, "Permission Denied", "You cannot add members to this client")
		}
	}
	var rolePermissionErr error
	if platformTeamManager(requester) {
		rolePermissionErr = clientSvc.CanAssignClientRoleAsRoot(requester, req.RoleID)
	} else {
		rolePermissionErr = clientAuthzService(c, requester).CanAssignClientRole(req.ClientID, requester.ID, req.RoleID)
	}
	if rolePermissionErr != nil {
		return utils.Error(c, http.StatusForbidden, "Permission Denied", rolePermissionErr.Error())
	}

	targetUserID := req.UserID
	if targetUserID == uuid.Nil {
		email := strings.TrimSpace(req.Email)
		if email == "" {
			return utils.Error(c, http.StatusBadRequest, "Invalid data", "user_id or email is required")
		}
		targetUser, err := users.GetUserByEmail(email)
		if err != nil {
			return utils.Error(c, http.StatusNotFound, "User not found", "No user exists with that email")
		}
		targetUserID = targetUser.ID
	}
	if operationalRootMayNotManageRootTarget(c, requester, targetUserID) {
		return nil
	}
	// A direct membership takes precedence over access inherited from a parent
	// organization, so adding an existing account can demote an inherited
	// Owner or Admin here and in every organization below. Apply the same
	// target ranking as every other member write.
	if err := clientSvc.AuthorizeMemberTarget(requester, req.ClientID, targetUserID, users.GetUserByID); err != nil {
		if errors.Is(err, clients.ErrMemberTargetDenied) {
			return utils.Error(c, http.StatusForbidden, "Permission Denied", err.Error())
		}
		return utils.Error(c, http.StatusInternalServerError, "Authorization error", err.Error())
	}

	if err := clientSvc.AddUserToClient(req.ClientID, targetUserID, req.RoleID); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to add user", err.Error())
	}

	return utils.Success(c, http.StatusOK, "User added to client", dtos.ClientMemberLinkResponse{
		UserID:   targetUserID,
		ClientID: req.ClientID,
		RoleID:   req.RoleID,
	})
}

func DeleteClient(c echo.Context) error {
	_, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	user, err := authz.CurrentUser(c)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}
	if operationalRootMayNotManageOrganization(c, user) {
		return nil
	}

	clientIDStr := c.Param("id")
	clientID, err := uuid.FromString(clientIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid Client ID", err.Error())
	}

	if user.IsPrimaryRoot() {
		err = clientSvc.DeleteClientAsPrimaryRoot(clientID)
	} else {
		err = clientAuthzService(c, user).DeleteClient(clientID, user.ID)
	}
	if err != nil {
		switch {
		case errors.Is(err, clients.ErrClientHasChildren):
			return utils.Error(c, http.StatusConflict, "No se puede eliminar una organización que tiene sub-organizaciones. Elimina o mueve primero sus sub-organizaciones.", ErrCodeClientHasChildren)
		case errors.Is(err, clients.ErrClientPermissionDenied), errors.Is(err, clients.ErrClientAccessDenied):
			return utils.Error(c, http.StatusForbidden, "Solo el propietario puede eliminar la organización", ErrCodeClientPermissionDenied)
		case errors.Is(err, clients.ErrClientNotFound):
			return utils.Error(c, http.StatusNotFound, "La organización no existe", ErrCodeClientNotFound)
		}
		return utils.Error(c, http.StatusInternalServerError, "Error deleting client", "No se pudo eliminar la organización")
	}

	return utils.Success(c, http.StatusOK, "Client deleted successfully", nil)
}

// CreateClientMember registers a new user and links them to a client.
// Cross-domain: uses users package-level functions for user creation.
func CreateClientMember(c echo.Context) error {
	_, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "Invalid token")
	}

	requester, err := authz.CurrentUser(c)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}
	var req struct {
		Email     string    `json:"email"`
		FirstName string    `json:"first_name"`
		LastName  string    `json:"last_name"`
		ClientID  uuid.UUID `json:"client_id"`
		RoleID    uuid.UUID `json:"role_id"`
	}

	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", err.Error())
	}

	if !platformTeamManager(requester) {
		if err := clientAuthzService(c, requester).CanManageClientMembers(req.ClientID, requester.ID); err != nil {
			return utils.Error(c, http.StatusForbidden, "Permission Denied", "You cannot add members to this client")
		}
	}
	var rolePermissionErr error
	if platformTeamManager(requester) {
		rolePermissionErr = clientSvc.CanAssignClientRoleAsRoot(requester, req.RoleID)
	} else {
		rolePermissionErr = clientAuthzService(c, requester).CanAssignClientRole(req.ClientID, requester.ID, req.RoleID)
	}
	if rolePermissionErr != nil {
		return utils.Error(c, http.StatusForbidden, "Permission Denied", rolePermissionErr.Error())
	}

	tenantCode, _ := c.Get("tenant_code").(string)
	newUser, err := users.InviteUserForTenant(req.Email, req.FirstName, req.LastName, tenantCode)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Registration failed", err.Error())
	}

	err = clientSvc.AddUserToClient(req.ClientID, newUser.ID, req.RoleID)
	if err != nil {
		_ = users.DeleteFullAccount(newUser.CognitoSub)
		return utils.Error(c, http.StatusInternalServerError, "Failed to link user to client", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Member created and linked successfully", dtos.ClientMemberLinkResponse{
		UserID:   newUser.ID,
		ClientID: req.ClientID,
		Email:    newUser.Email,
		RoleID:   req.RoleID,
	})
}

func ListClientMembers(c echo.Context) error {
	_, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	user, err := authz.CurrentUser(c)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	targetClientIDStr := c.QueryParam("client_id")
	if targetClientIDStr == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing client_id", "You must specify which client to list")
	}

	targetClientID, err := uuid.FromString(targetClientIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if rawPageSize := c.QueryParam("page_size"); rawPageSize != "" {
		pageSize, sizeErr := strconv.Atoi(rawPageSize)
		if sizeErr != nil || pageSize < 1 {
			return utils.Error(c, http.StatusBadRequest, "Invalid page_size", "page_size must be positive")
		}
		if pageSize > 100 {
			pageSize = 100
		}
		page, pageErr := strconv.Atoi(c.QueryParam("page"))
		if pageErr != nil || page < 1 {
			page = 1
		}
		var members []models.ClientMember
		var total int64
		var listErr error
		if user.IsPlatformAdmin() {
			members, total, listErr = clientSvc.ListClientMembersPageAsRoot(targetClientID, page, pageSize, c.QueryParam("search"))
		} else {
			members, total, listErr = clientAuthzService(c, user).ListClientMembersPage(targetClientID, user.ID, page, pageSize, c.QueryParam("search"))
		}
		if listErr != nil {
			return utils.Error(c, http.StatusForbidden, "Access Denied", listErr.Error())
		}
		totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
		return utils.Success(c, http.StatusOK, "Client members page", dtos.ClientMembersPage{Data: dtos.NewClientMemberResponses(members), Total: total, Page: page, PageSize: pageSize, TotalPages: totalPages})
	}

	var members []models.ClientMember
	if user.IsPlatformAdmin() {
		members, err = clientSvc.ListClientMembersAsRoot(targetClientID)
	} else {
		members, err = clientAuthzService(c, user).ListClientMembers(targetClientID, user.ID)
	}
	if err != nil {
		return utils.Error(c, http.StatusForbidden, "Access Denied", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Client members list", dtos.NewClientMemberResponses(members))
}

func RemoveMember(c echo.Context) error {
	_, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	requester, err := authz.CurrentUser(c)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}
	targetUserID, err := uuid.FromString(c.Param("user_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid user ID", err.Error())
	}

	clientID, err := uuid.FromString(c.QueryParam("client_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid client ID", err.Error())
	}
	if operationalRootMayNotManageRootTarget(c, requester, targetUserID) {
		return nil
	}

	var removeErr error
	if platformTeamManager(requester) {
		removeErr = clientSvc.RemoveClientMemberAsRoot(clientID, targetUserID)
	} else {
		removeErr = clientAuthzService(c, requester).RemoveClientMember(clientID, requester.ID, targetUserID)
	}
	if removeErr != nil {
		return utils.Error(c, http.StatusForbidden, "Operation failed", removeErr.Error())
	}

	return utils.Success(c, http.StatusOK, "Member removed successfully", nil)
}

func UpdateMemberRole(c echo.Context) error {
	_, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	requester, err := authz.CurrentUser(c)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}
	targetUserID, err := uuid.FromString(c.Param("user_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid user ID", err.Error())
	}

	clientID, err := uuid.FromString(c.QueryParam("client_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid client ID", err.Error())
	}
	if operationalRootMayNotManageRootTarget(c, requester, targetUserID) {
		return nil
	}

	var req struct {
		NewRoleID uuid.UUID `json:"new_role_id"`
		RoleID    uuid.UUID `json:"role_id"`
	}
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", err.Error())
	}
	if req.NewRoleID == uuid.Nil {
		req.NewRoleID = req.RoleID
	}
	if req.NewRoleID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", "new_role_id is required")
	}

	var updateErr error
	if platformTeamManager(requester) {
		updateErr = clientSvc.UpdateClientMemberRoleAsRoot(requester, clientID, targetUserID, req.NewRoleID)
	} else {
		updateErr = clientAuthzService(c, requester).UpdateClientMemberRole(clientID, requester.ID, targetUserID, req.NewRoleID)
	}
	if updateErr != nil {
		return utils.Error(c, http.StatusForbidden, "Operation failed", updateErr.Error())
	}

	return utils.Success(c, http.StatusOK, "Member role updated", nil)
}

func UpdateClient(c echo.Context) error {
	_, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "Session expired")
	}

	clientID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid client ID", err.Error())
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}
	if operationalRootMayNotManageOrganization(c, user) {
		return nil
	}

	name := c.FormValue("name")
	file, header, errFile := c.Request().FormFile("logo")
	if errFile == nil {
		defer file.Close()
	} else {
		file, header = nil, nil
	}
	removeLogo := c.FormValue("remove_logo") == "true"

	// Authorization happens inside UpdateClientWithLogo BEFORE the logo is
	// uploaded (ADM-8): a caller without OWNER access never writes to storage.
	res, err := withApplicationScope(c, user, clientServiceForContext(c)).UpdateClientWithLogo(clientID, user.ID, user.IsPrimaryRoot(), name, file, header, removeLogo)
	if err != nil {
		switch {
		case errors.Is(err, clients.ErrClientPermissionDenied):
			return utils.Error(c, http.StatusForbidden, "No tienes permiso para editar esta organización", ErrCodeClientPermissionDenied)
		case errors.Is(err, clients.ErrClientNotFound):
			return utils.Error(c, http.StatusNotFound, "La organización no existe", ErrCodeClientNotFound)
		case resourcesService.IsInvalidMediaFile(err):
			return utils.Error(c, http.StatusBadRequest, resourcesService.InvalidMediaFileMessage, resourcesService.InvalidMediaFileCode)
		case validations.IsValidationError(err):
			status, detail := resourcesService.UploadErrorResponse(err)
			return utils.Error(c, status, "Invalid logo", detail)
		default:
			status, detail := resourcesService.UploadErrorResponse(err)
			if status == http.StatusInternalServerError {
				return utils.Error(c, http.StatusInternalServerError, "Update error", "No se pudo actualizar la organización")
			}
			return utils.Error(c, status, "Error uploading logo", detail)
		}
	}

	if res.Logo != "" {
		res.Logo = clientSvc.GetClientLogoURL(res.ID, res.Logo)
	}

	return utils.Success(c, http.StatusOK, "Organization updated successfully", dtos.NewClientResponse(res))
}
