package applications

import (
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/products"
	"events-stocks/models"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type UserSyncFunc func(cognitoSub string) (*models.User, error)
type ProfileImageURLFunc func(user *models.User) string

type OrganizationAccess struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Code         string    `json:"code"`
	Logo         string    `json:"logo,omitempty"`
	AccessRole   string    `json:"access_role"`
	Capabilities []string  `gorm:"-" json:"capabilities"`
}

type MemberApplicationAccess struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	IsActive  bool   `json:"is_active"`
	IsEnabled bool   `json:"is_enabled"`
}

type Session struct {
	Application   models.Application       `json:"application"`
	User          dtos.UserProfileResponse `json:"user"`
	Organizations []OrganizationAccess     `json:"organizations"`
	Capabilities  []string                 `json:"capabilities"`
}

type SessionService struct {
	db              *gorm.DB
	syncUser        UserSyncFunc
	profileImageURL ProfileImageURLFunc
	cacheMu         sync.RWMutex
	cache           map[string]cachedSession
}

type cachedSession struct {
	session   *Session
	expiresAt time.Time
}

const sessionCacheTTL = 15 * time.Second

func NewSessionService(db *gorm.DB, syncUser UserSyncFunc, profileImageURL ...ProfileImageURLFunc) *SessionService {
	service := &SessionService{db: db, syncUser: syncUser, cache: make(map[string]cachedSession)}
	if len(profileImageURL) > 0 {
		service.profileImageURL = profileImageURL[0]
	}
	return service
}

func (service *SessionService) Resolve(cognitoSub, tenantCode string) (*Session, error) {
	if service == nil || service.db == nil || service.syncUser == nil {
		return nil, fmt.Errorf("application session service is not configured")
	}
	definition, known := products.Resolve(tenantCode)
	if !known {
		return nil, ErrApplicationAccessDenied
	}
	code := definition.Code.String()
	cacheKey := applicationSessionCacheKey(code, cognitoSub)
	if session := service.cached(cacheKey); session != nil {
		return session, nil
	}

	var application models.Application
	if err := service.db.Where("LOWER(code) = ? AND is_active = true", code).First(&application).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrApplicationAccessDenied
		}
		return nil, fmt.Errorf("load application: %w", err)
	}
	user, err := service.syncUser(cognitoSub)
	if err != nil || user == nil || !user.IsActive {
		return nil, ErrApplicationAccessDenied
	}

	organizations, err := service.organizationAccess(user.ID, application.ID)
	if err != nil {
		return nil, err
	}
	if (!application.AllowsPlatformAdmin || !user.IsPlatformAdmin()) && len(organizations) == 0 {
		return nil, ErrApplicationAccessDenied
	}
	for index := range organizations {
		organizations[index].Capabilities = applicationOrganizationCapabilities(
			application,
			organizations[index].AccessRole,
		)
	}

	profileImage := user.ProfileImage
	if service.profileImageURL != nil {
		profileImage = service.profileImageURL(user)
	}
	session := &Session{
		Application:   application,
		User:          dtos.NewUserProfileResponse(user, profileImage),
		Organizations: organizations,
		Capabilities:  effectiveApplicationCapabilities(application, user, organizations),
	}
	service.cacheMu.Lock()
	service.cache[cacheKey] = cachedSession{session: session, expiresAt: time.Now().Add(sessionCacheTTL)}
	service.cacheMu.Unlock()
	return session, nil
}

// applicationSessionCacheKey is process-local, but remains versioned and
// product-scoped so an identity that belongs to multiple products can never
// receive a session calculated for another application.
func applicationSessionCacheKey(productCode, cognitoSub string) string {
	return "v1:application-session:" + productCode + ":subject:" + strings.TrimSpace(cognitoSub)
}

func (service *SessionService) cached(key string) *Session {
	service.cacheMu.RLock()
	entry, ok := service.cache[key]
	service.cacheMu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		if ok {
			service.cacheMu.Lock()
			delete(service.cache, key)
			service.cacheMu.Unlock()
		}
		return nil
	}
	return entry.session
}

func (service *SessionService) ListMemberApplications(clientID, userID uuid.UUID) ([]MemberApplicationAccess, error) {
	var member models.ClientMember
	if err := service.db.Where("client_id = ? AND user_id = ? AND is_active = true", clientID, userID).First(&member).Error; err != nil {
		return nil, fmt.Errorf("load organization membership: %w", err)
	}
	result := make([]MemberApplicationAccess, 0)
	err := service.db.Raw(`
		WITH RECURSIVE ancestry AS (
			SELECT id, parent_id, 0 AS depth
			FROM clients
			WHERE id = ? AND deleted_at IS NULL
			UNION ALL
			SELECT parent.id, parent.parent_id, ancestry.depth + 1
			FROM ancestry
			JOIN clients parent ON parent.id = ancestry.parent_id
			WHERE parent.deleted_at IS NULL AND ancestry.depth < 31
		)
		SELECT DISTINCT ON (applications.id)
			applications.code,
			applications.name,
			COALESCE(member_access.is_active, false) AS is_active,
			client_applications.is_active AS is_enabled
		FROM ancestry
		JOIN client_applications
			ON client_applications.client_id = ancestry.id
			AND client_applications.is_active = true
		JOIN applications
			ON applications.id = client_applications.application_id
			AND applications.is_active = true
		LEFT JOIN client_member_applications member_access
			ON member_access.application_id = applications.id
			AND member_access.client_member_id = ?
		ORDER BY applications.id, ancestry.depth ASC
	`, clientID, member.ID).Scan(&result).Error
	if err != nil {
		return nil, fmt.Errorf("list member applications: %w", err)
	}
	return result, nil
}

func (service *SessionService) SetMemberApplicationAccess(
	clientID, userID uuid.UUID,
	applicationCode string,
	active bool,
) error {
	var cognitoSub string
	err := service.db.Transaction(func(tx *gorm.DB) error {
		var member models.ClientMember
		if err := tx.Where("client_id = ? AND user_id = ? AND is_active = true", clientID, userID).First(&member).Error; err != nil {
			return fmt.Errorf("load organization membership: %w", err)
		}
		var application models.Application
		if err := tx.Raw(`
			WITH RECURSIVE ancestry AS (
				SELECT id, parent_id, 0 AS depth
				FROM clients
				WHERE id = ? AND deleted_at IS NULL
				UNION ALL
				SELECT parent.id, parent.parent_id, ancestry.depth + 1
				FROM ancestry
				JOIN clients parent ON parent.id = ancestry.parent_id
				WHERE parent.deleted_at IS NULL AND ancestry.depth < 31
			)
			SELECT applications.*
			FROM ancestry
			JOIN client_applications
				ON client_applications.client_id = ancestry.id
				AND client_applications.is_active = true
			JOIN applications
				ON applications.id = client_applications.application_id
				AND applications.is_active = true
			WHERE LOWER(applications.code) = ?
			LIMIT 1
		`, clientID, strings.ToLower(strings.TrimSpace(applicationCode))).Scan(&application).Error; err != nil {
			return fmt.Errorf("load enabled application: %w", err)
		}
		if application.ID == uuid.Nil {
			return ErrApplicationNotEnabled
		}
		access := models.ClientMemberApplication{
			ClientMemberID: member.ID,
			ApplicationID:  application.ID,
			IsActive:       active,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "client_member_id"}, {Name: "application_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"is_active", "updated_at"}),
		}).Create(&access).Error; err != nil {
			return fmt.Errorf("save member application access: %w", err)
		}
		if err := tx.Model(&models.User{}).Select("cognito_sub").Where("id = ?", userID).Scan(&cognitoSub).Error; err != nil {
			return fmt.Errorf("load member identity: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	service.invalidateCognitoSub(cognitoSub)
	return nil
}

func (service *SessionService) invalidateCognitoSub(cognitoSub string) {
	if strings.TrimSpace(cognitoSub) == "" {
		return
	}
	service.cacheMu.Lock()
	defer service.cacheMu.Unlock()
	for key := range service.cache {
		if strings.HasSuffix(key, ":"+cognitoSub) {
			delete(service.cache, key)
		}
	}
}

func (service *SessionService) organizationAccess(userID, applicationID uuid.UUID) ([]OrganizationAccess, error) {
	organizations := make([]OrganizationAccess, 0)
	err := service.db.Raw(`
		SELECT DISTINCT
			clients.id,
			clients.name,
			clients.code,
			clients.logo,
			client_roles.code AS access_role
		FROM client_member_applications access
		JOIN client_members
			ON client_members.id = access.client_member_id
			AND client_members.is_active = true
		JOIN clients
			ON clients.id = client_members.client_id
			AND clients.deleted_at IS NULL
			AND clients.is_active = true
		JOIN client_roles ON client_roles.id = client_members.client_role_id
		WHERE access.application_id = ?
			AND access.is_active = true
			AND client_members.user_id = ?
		ORDER BY clients.name ASC
	`, applicationID, userID).Scan(&organizations).Error
	if err != nil {
		return nil, fmt.Errorf("load application organizations: %w", err)
	}
	return organizations, nil
}

func effectiveApplicationCapabilities(
	application models.Application,
	user *models.User,
	organizations []OrganizationAccess,
) []string {
	capabilities := newCapabilitySet("session:view")
	if moduleEnabled(application.Modules, "home") {
		capabilities.add("dashboard:view")
	}
	platformAuthority := application.AllowsPlatformAdmin && user != nil && user.IsPlatformAdmin()
	if platformAuthority {
		if user.IsPrimaryRoot() {
			capabilities.add("audit:view")
		}
		if moduleEnabled(application.Modules, "metrics") {
			capabilities.add("metrics:view")
		}
		if moduleEnabled(application.Modules, "events") {
			capabilities.add("events:view", "guests:manage", "checkin:run", "analytics:view", "members:manage")
			if user.IsPrimaryRoot() {
				capabilities.add("events:create", "events:manage", "events:delete")
			}
		}
		if moduleEnabled(application.Modules, "users") {
			capabilities.add("platform:users:view", "platform:users:support")
			if user.IsPrimaryRoot() {
				capabilities.add("platform:users:manage", "platform:users:root-manage")
			}
		}
		if moduleEnabled(application.Modules, "organizations") {
			capabilities.add("organizations:view")
			if user.IsPrimaryRoot() {
				capabilities.add("organizations:manage", "members:manage", "applications:manage")
			} else {
				capabilities.add("members:manage")
			}
		}
	}

	for _, organization := range organizations {
		for _, capability := range organization.Capabilities {
			capabilities.add(capability)
		}
	}
	if !moduleEnabled(application.Modules, "events") {
		capabilities.remove("events:view", "events:create", "events:manage", "events:delete", "guests:manage", "checkin:run", "analytics:view")
	}
	if !moduleEnabled(application.Modules, "organizations") {
		capabilities.remove("organizations:view", "organizations:manage")
	}
	if !moduleEnabled(application.Modules, "metrics") {
		capabilities.remove("metrics:view")
	}
	return capabilities.values()
}

func organizationCapabilities(role string) []string {
	code := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(role)), "INHERITED_")
	capabilities := newCapabilitySet("organizations:view")
	switch code {
	case "OWNER", "ADMIN":
		capabilities.add(
			"organizations:manage", "members:manage",
			"events:view", "events:create", "events:manage", "events:delete",
			"guests:manage", "checkin:run", "analytics:view", "metrics:view",
		)
	case "EVENT_MANAGER":
		capabilities.add("events:view", "events:create", "events:manage", "guests:manage", "checkin:run", "analytics:view", "metrics:view")
	case "EDITOR":
		capabilities.add("events:view", "events:manage", "guests:manage", "analytics:view", "metrics:view")
	case "MEMBER":
		capabilities.add("events:view", "guests:manage")
	case "CHECKIN":
		capabilities.add("events:view", "checkin:run")
	case "ANALYST":
		capabilities.add("events:view", "analytics:view", "metrics:view")
	case "GUEST", "VIEWER":
		capabilities.add("events:view")
	}
	return capabilities.values()
}

func applicationOrganizationCapabilities(application models.Application, role string) []string {
	capabilities := newCapabilitySet(organizationCapabilities(role)...)
	if !moduleEnabled(application.Modules, "events") {
		capabilities.remove("events:view", "events:create", "events:manage", "events:delete", "guests:manage", "checkin:run", "analytics:view")
	}
	if !moduleEnabled(application.Modules, "organizations") {
		capabilities.remove("organizations:view", "organizations:manage")
	}
	if !moduleEnabled(application.Modules, "metrics") {
		capabilities.remove("metrics:view")
	}
	return capabilities.values()
}

func moduleEnabled(modules models.StringList, expected string) bool {
	for _, module := range modules {
		if strings.EqualFold(strings.TrimSpace(module), expected) {
			return true
		}
	}
	return false
}

type capabilitySet map[string]struct{}

func newCapabilitySet(values ...string) capabilitySet {
	set := make(capabilitySet)
	set.add(values...)
	return set
}

func (set capabilitySet) add(values ...string) {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
}

func (set capabilitySet) remove(values ...string) {
	for _, value := range values {
		delete(set, value)
	}
}

func (set capabilitySet) values() []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

var ErrApplicationAccessDenied = errors.New("user does not have access to this application")
var ErrApplicationNotEnabled = errors.New("application is not enabled for this organization")
