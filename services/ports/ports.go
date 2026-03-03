package ports

import (
	"context"
	"events-stocks/dtos"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

// CacheRepository is the minimal Redis interface used by all services.
type CacheRepository interface {
	Invalidate(resource string, key string) error
	DeleteKeysByPattern(ctx context.Context, pattern string) error
	GetKey(ctx context.Context, key string) (string, error)
	SaveKey(ctx context.Context, key string, value string, ttl time.Duration) error
}

// Transactor allows atomic multi-table writes without importing configuration.DB directly.
type Transactor interface {
	Transaction(fn func(tx *gorm.DB) error) error
}

// EventsRepository is the data access contract for Event records.
type EventsRepository interface {
	CreateEvent(event *models.Event) error
	UpdateEvent(event *models.Event) error
	DeleteEvent(id uuid.UUID) error
	ListEvents(page int, pageSize int, name string) ([]models.Event, error)
	GetEventByID(id uuid.UUID) (string, error)
	IdentifierExists(identifier string) bool
}

// EventConfigRepository is the data access contract for EventConfig records.
type EventConfigRepository interface {
	CreateEventConfig(m *models.EventConfig) error
	UpdateEventConfig(m *models.EventConfig) error
	DeleteEventConfig(id uuid.UUID) error
	GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error)
}

// EventSectionRepository is the data access contract for EventSection records.
type EventSectionRepository interface {
	CreateEventSection(m *models.EventSection) error
	UpdateEventSection(m *models.EventSection) error
	DeleteEventSection(id uuid.UUID) error
	GetEventSectionByID(id uuid.UUID) (*models.EventSection, error)
	ListEventSections() ([]models.EventSection, error)
	ListByEventID(eventID uuid.UUID) ([]models.EventSection, error)
}

// GuestRepository is the data access contract for Guest records.
type GuestRepository interface {
	CreateGuest(m *models.Guest) error
	UpdateGuest(m *models.Guest) error
	DeleteGuest(id uuid.UUID) error
	GetGuestByID(id uuid.UUID) (*models.Guest, error)
	GetGuestByInvitationID(invitationID uuid.UUID) (*models.Guest, error)
	CreateGuests(guests []models.Guest) error
	GetPendingStatusID() uuid.UUID
}
// TableRepository is the data access contract for Table records.
type TableRepository interface {
	ListByEventID(eventID uuid.UUID) ([]models.Table, error)
	Create(table *models.Table) error
	Update(table *models.Table) error
	Delete(id uuid.UUID) error
	GetByID(id uuid.UUID) (*models.Table, error)
	BatchAssign(assignments []dtos.SeatAssignment) error
}


// InvitationRepository is the data access contract for Invitation records.
type InvitationRepository interface {
	CreateInvitation(m *models.Invitation) error
	UpdateInvitation(m *models.Invitation) error
	DeleteInvitation(id uuid.UUID) error
	GetInvitationByID(id uuid.UUID) (*models.Invitation, error)
	GetInvitationByIDLite(id uuid.UUID) (*models.Invitation, error)
	ListInvitations() ([]models.Invitation, error)
	ListByEventID(eventID uuid.UUID) ([]models.Invitation, error)
}

// AccessTokenRepository is the data access contract for InvitationAccessToken records.
type AccessTokenRepository interface {
	GetByToken(token string) (*models.InvitationAccessToken, error)
	GetByPrettyToken(code string) (*models.InvitationAccessToken, error)
	GeneratePrettyToken(eventID uuid.UUID, length int) (string, error)
}

// InvitationLogRepository is the data access contract for InvitationLog records.
type InvitationLogRepository interface {
	CreateInvitationLog(m *models.InvitationLog) error
	CreateManyInvitationLogs(logs []models.InvitationLog) error
}

// MomentRepository is the data access contract for Moment records.
type MomentRepository interface {
	CreateMoment(m *models.Moment) error
	UpdateMoment(m *models.Moment) error
	DeleteMoment(id uuid.UUID) error
	GetMomentByID(id uuid.UUID) (*models.Moment, error)
	ListMoments() ([]models.Moment, error)
	ListByEventID(eventID uuid.UUID, approvedOnly bool) ([]models.Moment, error)
	UpdateMomentContent(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error
	// ListForDashboard returns moments ready for admin review (excludes pending/processing).
	ListForDashboard(eventID uuid.UUID) ([]models.Moment, error)
	// ListApprovedForWall returns approved+optimized moments paginated for the public wall.
	ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error)
	// BulkUpdateApproval updates is_approved for multiple moments.
	BulkUpdateApproval(ids []uuid.UUID, isApproved bool) error
	// GetDistinctEventIDsByMomentIDs returns unique event_id values for the given moment IDs.
	GetDistinctEventIDsByMomentIDs(ids []uuid.UUID) ([]uuid.UUID, error)
	// SummaryByEventIDs returns the pending moment count per event in one query.
	SummaryByEventIDs(eventIDs []uuid.UUID) ([]models.MomentSummary, error)
	// BulkReorder sets custom display order for multiple moments.
	BulkReorder(items []models.MomentOrderItem) error
}

// UserRepository is the data access contract for User records.
type UserRepository interface {
	CreateUser(user *models.User) error
	UpdateUser(user *models.User) error
	DeleteUser(id uuid.UUID) error
	GetUserByID(id uuid.UUID) (*models.User, error)
	GetUserByCognitoSub(sub string) (*models.User, error)
	UpdateUserFields(userID uuid.UUID, fields map[string]interface{}) error
	ClearProfileImage(userID uuid.UUID) error
	SetUserRoot(userID uuid.UUID, isRoot bool) error
	ListAllUsers() ([]models.User, error)
	ListAllUsersPaginated(page, pageSize int) ([]models.User, int64, error)
	SetUserActive(userID uuid.UUID, active bool) error
}

// ClientRepository is the data access contract for Client records.
type ClientRepository interface {
	CreateClient(client *models.Client) error
	GetClientByID(id uuid.UUID) (*models.Client, error)
	UpdateClient(client *models.Client) error
	DeleteClient(id uuid.UUID) error
	GetClientsByUser(userID uuid.UUID) ([]models.Client, error)
	GetChildrenClients(parentID uuid.UUID) ([]models.Client, error)
	CheckAccessRecursive(userID, targetClientID uuid.UUID) (bool, string)
	IsMember(userID, clientID uuid.UUID) (bool, string)
	AddMember(member *models.ClientMember) error
	RemoveMember(clientID, userID uuid.UUID) error
	UpdateMemberRole(clientID, userID, newRoleID uuid.UUID) error
	GetMemberRole(clientID, userID uuid.UUID) (*models.ClientRole, error)
	GetMembers(clientID uuid.UUID) ([]models.ClientMember, error)
	DeleteAllMembers(clientID uuid.UUID) error
	ListClientsByUser(userID uuid.UUID) ([]models.Client, error)
	CountClientsByUsers(userIDs []uuid.UUID) (map[uuid.UUID]int64, error)
}

// ClientRoleRepository is the data access contract for ClientRole records.
type ClientRoleRepository interface {
	GetByCode(code string) (*models.ClientRole, error)
	GetByID(id uuid.UUID) (*models.ClientRole, error)
	GetAssignableRoles(myHierarchyLevel int) ([]models.ClientRole, error)
}

// ClientTypeRepository is the data access contract for ClientType records.
type ClientTypeRepository interface {
	GetByID(id uuid.UUID) (*models.ClientType, error)
	GetByCode(code string) (*models.ClientType, error)
	GetChildTypes(parentLevel int) ([]models.ClientType, error)
	GetRootType() ([]models.ClientType, error)
}

// AuthProviderRepository is the data access contract for Cognito/auth operations.
type AuthProviderRepository interface {
	GetUser(sub string, provider string) (*dtos.AuthUser, error)
	UpdateUser(sub string, attrs map[string]string, provider string) error
	DeleteUser(sub string, provider string) error
	CreateUser(req dtos.CreateAuthUserRequest, provider string) (*dtos.AuthUser, error)
	SetUserEnabled(sub string, enabled bool, provider string) error
	InviteUser(email, firstName, lastName, provider string) (*dtos.AuthUser, error)
}
