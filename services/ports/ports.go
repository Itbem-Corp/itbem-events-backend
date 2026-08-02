package ports

import (
	"context"
	"errors"
	"events-stocks/dtos"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"io"
	"time"
)

var (
	ErrStaleCoverProcessing             = errors.New("stale event cover processing callback")
	ErrInvalidCoverProcessingTransition = errors.New("invalid event cover processing transition")
)

var ErrMultipartUploadNotFound = errors.New("multipart upload not found")

// CacheRepository is the minimal Redis interface used by all services.
type CacheRepository interface {
	Invalidate(resource string, key string) error
	DeleteKeysByPattern(ctx context.Context, pattern string) error
	GetKey(ctx context.Context, key string) (string, error)
	SaveKey(ctx context.Context, key string, value string, ttl time.Duration) error
}

type MediaJobPublisher interface {
	PublishMediaJob(msg dtos.MediaProcessMessage) (bool, error)
}

type ObjectStorageRepository interface {
	FileExists(filename, folder, bucket, provider string) (bool, string, error)
	GetPresignedFileURL(filename, folder, bucket, provider string, minutes int) (string, error)
	GetPresignedPutURL(objectKey, bucket, provider, contentType string, minutes int) (string, error)
	CreateMultipartUpload(objectKey, bucket, provider, contentType string) (string, error)
	GetPresignedUploadPartURL(objectKey, bucket, provider, uploadID string, partNumber, minutes int) (string, error)
	CompleteMultipartUpload(objectKey, bucket, provider, uploadID string, parts []dtos.CompletedUploadPart) error
	AbortMultipartUpload(objectKey, bucket, provider, uploadID string) error
	UpdateFile(content []byte, filename, contentType, folder, bucket, provider string) (string, error)
	UploadRawBytesSimple(content []byte, filename, contentType, folder, bucket, provider string) error
	DeleteFile(filename, folder, bucket, provider string) error
	GetFileStream(filename, folder, bucket, provider string) (io.ReadCloser, error)
}

type ObjectStorageMetadata struct {
	Size        int64
	ContentType string
}

// ObjectStorageMetadataReader is optional so adapters can adopt stronger
// presigned-upload verification without breaking minimal test doubles.
type ObjectStorageMetadataReader interface {
	GetObjectMetadata(filename, folder, bucket, provider string) (ObjectStorageMetadata, error)
}

// ObjectStorageStreamUploader is optional so adapters can add bounded-memory
// uploads without expanding the core storage contract or its test doubles.
type ObjectStorageStreamUploader interface {
	UploadStream(ctx context.Context, body io.Reader, contentLength int64, filename, contentType, folder, bucket, provider string) error
}

type ObjectStorageUploadConfirmer interface {
	MarkUploadConfirmed(ctx context.Context, filename, folder, bucket, provider string) error
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
	GetEventByIDRaw(id uuid.UUID) (*models.Event, error)
	GetEventByIDForSpec(id uuid.UUID) (*models.Event, error)
	GetEventByIdentifier(identifier string) (*models.Event, error)
	GetEventsByClientID(clientID uuid.UUID) ([]models.Event, error)
	GetAllEventsForDashboard() ([]models.Event, error)
	GetEventsForUser(userID uuid.UUID) ([]models.Event, error)
	UpdateEventCover(id uuid.UUID, coverImageURL string) error
	IdentifierExists(identifier string) bool
}

// EventsDashboardRepository is an optional optimized projection implemented
// by production repositories. EventService retains an in-memory fallback for
// adapters that only implement EventsRepository.
type EventsDashboardRepository interface {
	GetEventDashboardOverview(clientID, userID *uuid.UUID, now time.Time) (dtos.EventDashboardOverview, error)
}

// EventCoverVariantsRepository is optional so lightweight adapters remain
// compatible while production persists the responsive cover set atomically.
type EventCoverVariantsRepository interface {
	UpdateEventCoverWithVariants(id uuid.UUID, coverImageURL string, variants models.MediaVariants) error
}

type EventCoverProcessingRepository interface {
	BeginEventCoverProcessing(id uuid.UUID, pendingURL, jobID string) (*models.Event, string, error)
	ApplyEventCoverProcessing(id uuid.UUID, callback dtos.MediaProcessingCallback) (*models.Event, string, models.MediaVariants, error)
}

type EventsPageRepository interface {
	ListEventPage(clientID, userID *uuid.UUID, query dtos.EventListQuery) ([]models.Event, int, dtos.EventListCounts, error)
}

type EventsNotificationRepository interface {
	ListEventNotifications(clientID, userID *uuid.UUID, now time.Time) ([]dtos.EventNotification, error)
}

// EventConfigRepository is the data access contract for EventConfig records.
type EventConfigRepository interface {
	CreateEventConfig(m *models.EventConfig) error
	UpdateEventConfig(m *models.EventConfig) error
	DeleteEventConfig(id uuid.UUID) error
	GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error)
}

type EventAnalyticsRepository interface {
	CreateEventAnalytics(m *models.EventAnalytics) error
	UpdateEventAnalytics(m *models.EventAnalytics) error
	DeleteEventAnalytics(id uuid.UUID) error
	GetEventAnalyticsByID(id uuid.UUID) (*models.EventAnalytics, error)
	GetEventAnalyticsByEventID(eventID uuid.UUID) (*models.EventAnalytics, error)
	ListEventAnalyticss() ([]models.EventAnalytics, error)
}

// EventMemberRepository isolates event-member persistence from application use cases.
type EventMemberRepository interface {
	List(eventID uuid.UUID) ([]models.EventMember, error)
	Upsert(eventID, userID uuid.UUID, role string) (*models.EventMember, error)
	Remove(eventID, userID uuid.UUID) error
}

// EventSectionRepository is the data access contract for EventSection records.
type EventSectionRepository interface {
	CreateEventSection(m *models.EventSection) error
	UpdateEventSection(m *models.EventSection) error
	DeleteEventSection(id uuid.UUID) error
	BulkUpdateSectionOrder(eventID uuid.UUID, updates map[uuid.UUID]int) error
	GetEventSectionByID(id uuid.UUID) (*models.EventSection, error)
	ListEventSections() ([]models.EventSection, error)
	ListByEventID(eventID uuid.UUID) ([]models.EventSection, error)
	ListByEventIDForSpec(eventID uuid.UUID) ([]models.EventSection, error)
}

// GuestRepository is the data access contract for Guest records.
type GuestRepository interface {
	CreateGuest(m *models.Guest) error
	UpdateGuest(m *models.Guest) error
	DeleteGuest(id uuid.UUID) error
	GetGuestByID(id uuid.UUID) (*models.Guest, error)
	GetGuestByInvitationID(invitationID uuid.UUID) (*models.Guest, error)
	CreateGuests(guests []models.Guest) error
	BulkDeleteGuests(ids []uuid.UUID) error
	ListGuestsByEventID(eventID uuid.UUID) ([]models.Guest, error)
	GetGuestSummaryByEventID(eventID uuid.UUID) (dtos.GuestSummary, error)
	ListAttendeesByEventID(eventID uuid.UUID) ([]models.Guest, error)
	GetPendingStatusID() uuid.UUID
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
	GetMomentByEventIDAndContentURL(eventID uuid.UUID, contentURL string) (*models.Moment, error)
	ListMoments() ([]models.Moment, error)
	ListByEventID(eventID uuid.UUID, approvedOnly bool) ([]models.Moment, error)
	UpdateMomentContent(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error
	BulkDeleteMoments(ids []uuid.UUID) error
	// ListForDashboard returns moments ready for admin review (excludes pending/processing).
	ListForDashboard(eventID uuid.UUID) ([]models.Moment, error)
	ListForDashboardPage(eventID uuid.UUID, page, pageSize int) ([]models.Moment, dtos.MomentDashboardCounts, error)
	ListPendingSummaryByEventIDs(eventIDs []uuid.UUID) ([]dtos.MomentSummary, error)
	// ListApprovedForWall returns approved+optimized moments paginated for the public wall.
	ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error)
	// BulkUpdateApproval updates is_approved for multiple moments.
	BulkUpdateApproval(ids []uuid.UUID, isApproved bool) error
	// GetDistinctEventIDsByMomentIDs returns unique event_id values for the given moment IDs.
	GetDistinctEventIDsByMomentIDs(ids []uuid.UUID) ([]uuid.UUID, error)
	GetMomentsByIDs(ids []uuid.UUID) ([]models.Moment, error)
	BulkUpdateOrder(updates map[uuid.UUID]int) error
	ListProcessingByEventID(eventID uuid.UUID, rawOnly bool) ([]models.Moment, error)
	ListApprovedForWallCursor(eventID uuid.UUID, afterCreatedAt *time.Time, afterID string, afterOrder *int, limit int) ([]models.Moment, int64, error)
}

// MomentProcessingRepository adds the CAS operations required by the async
// media pipeline without widening the legacy MomentRepository contract used by
// existing tests and adapters.
type MomentProcessingRepository interface {
	BeginMediaProcessingJob(id, eventID uuid.UUID, inputKey, jobID string) (int64, error)
	ApplyMediaProcessingUpdate(id, eventID uuid.UUID, jobID string, generation int64, allowedCurrentStatuses []string, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64, mediaVariants models.MediaVariants) (bool, error)
}

type EventTableRepository interface {
	CreateEventTable(table *models.EventTable) error
	UpdateEventTable(table *models.EventTable) error
	DeleteEventTable(id uuid.UUID) error
	GetEventTableByID(id uuid.UUID) (*models.EventTable, error)
	ListEventTablesByEventID(eventID uuid.UUID) ([]models.EventTable, error)
	AssignGuestsToTables(eventID uuid.UUID, assignments map[uuid.UUID]*uuid.UUID) error
	SaveSeatingPlan(eventID uuid.UUID, plan dtos.SeatingPlanSaveRequest) ([]models.EventTable, error)
}

type ResourceRepository interface {
	CreateResource(resource *models.Resource) error
	UpdateResource(resource *models.Resource) error
	TouchResourceUpdatedAt(id uuid.UUID, updatedAt time.Time) error
	DeleteResource(id uuid.UUID) error
	GetResourceByID(id uuid.UUID) (*models.Resource, error)
	ListResourcesBySection(sectionID *uuid.UUID) ([]models.Resource, error)
	ListResourceTypesRaw() ([]models.ResourceType, error)
}

type ColorRepository interface {
	CreateColor(color *models.Color) error
	UpdateColor(color *models.Color) error
	DeleteColor(id uuid.UUID) error
	GetColorByID(id uuid.UUID) (*models.Color, error)
	ListColors() ([]models.Color, error)
	CreateMultipleColors(colors []models.Color) error
	CreatePalette(palette *models.ColorPalette) error
	UpdatePalette(palette *models.ColorPalette) error
	DeletePalette(id uuid.UUID) error
	GetColorPaletteByID(id uuid.UUID) (*models.ColorPalette, error)
	ListColorPalettes() ([]models.ColorPalette, error)
	CreatePattern(pattern *models.ColorPalettePattern) error
	UpdatePattern(pattern *models.ColorPalettePattern) error
	DeletePattern(id uuid.UUID) error
	GetColorPatternByID(id uuid.UUID) (*models.ColorPalettePattern, error)
	ListAllPatterns() ([]models.ColorPalettePattern, error)
}

type FontRepository interface {
	CreateFont(font *models.Font) error
	UpdateFont(font *models.Font) error
	DeleteFont(id uuid.UUID) error
	GetFontByID(id uuid.UUID) (*models.Font, error)
	ListFonts(page int, pageSize int, name string) ([]models.Font, error)
	CreateMultipleFonts(fonts []models.Font) error
	CreateFontSet(fontSet *models.FontSet) error
	UpdateFontSet(fontSet *models.FontSet) error
	DeleteFontSet(id uuid.UUID) error
	GetFontSetByID(id uuid.UUID) (*models.FontSet, error)
	ListFontSets(page int, pageSize int, name string) ([]models.FontSet, error)
	CreateFontPattern(pattern *models.FontSetPattern) error
	UpdateFontPattern(pattern *models.FontSetPattern) error
	DeleteFontPattern(id uuid.UUID) error
	GetFontPatternByID(id uuid.UUID) (*models.FontSetPattern, error)
	ListFontPatterns(fontSetID *uuid.UUID) ([]models.FontSetPattern, error)
}

type DesignTemplateRepository interface {
	CreateDesignTemplate(m *models.DesignTemplate) error
	UpdateDesignTemplate(m *models.DesignTemplate) error
	DeleteDesignTemplate(id uuid.UUID) error
	GetDesignTemplateByID(id uuid.UUID) (*models.DesignTemplate, error)
	ListDesignTemplates() ([]models.DesignTemplate, error)
}

type EventTypeRepository interface {
	CreateEventType(m *models.EventType) error
	UpdateEventType(m *models.EventType) error
	DeleteEventType(id uuid.UUID) error
	GetEventTypeByID(id uuid.UUID) (*models.EventType, error)
	ListEventTypes() ([]models.EventType, error)
}

type GuestStatusRepository interface {
	CreateGuestStatus(m *models.GuestStatus) error
	UpdateGuestStatus(m *models.GuestStatus) error
	DeleteGuestStatus(id uuid.UUID) error
	GetGuestStatusByID(id uuid.UUID) (*models.GuestStatus, error)
	ListGuestStatuss() ([]models.GuestStatus, error)
}

type MomentTypeRepository interface {
	CreateMomentType(m *models.MomentType) error
	UpdateMomentType(m *models.MomentType) error
	DeleteMomentType(id uuid.UUID) error
	GetMomentTypeByID(id uuid.UUID) (*models.MomentType, error)
	ListMomentTypes() ([]models.MomentType, error)
}

// UserRepository is the data access contract for User records.
type UserRepository interface {
	CreateUser(user *models.User) error
	UpdateUser(user *models.User) error
	DeleteUser(id uuid.UUID) error
	GetUserByID(id uuid.UUID) (*models.User, error)
	GetUserByCognitoSub(sub string) (*models.User, error)
	GetUserByEmail(email string) (*models.User, error)
	UpdateUserFields(userID uuid.UUID, fields map[string]interface{}) error
	ClearProfileImage(userID uuid.UUID) error
	SetUserRoot(userID uuid.UUID, isRoot bool) error
	ListAllUsers() ([]models.User, error)
	ListAllUsersPaginated(query dtos.AdminUsersListQuery) ([]models.User, int64, error)
	SetUserActive(userID uuid.UUID, active bool) error
}

// ClientRepository is the data access contract for Client records.
type ClientRepository interface {
	CreateClient(client *models.Client) error
	GetClientByID(id uuid.UUID) (*models.Client, error)
	UpdateClient(client *models.Client) error
	DeleteClient(id uuid.UUID) error
	GetAllClients() ([]models.Client, error)
	ListClientsPaginated(userID *uuid.UUID, query dtos.ClientsListQuery) ([]models.Client, int64, error)
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

type ClientMembersPageRepository interface {
	ListMembersPage(clientID uuid.UUID, page, pageSize int, search string) ([]models.ClientMember, int64, error)
}

type UserClientStatusRepository interface {
	CountUserClientStatuses(userID uuid.UUID) (active, inactive int64, err error)
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
