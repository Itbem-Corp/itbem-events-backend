package moments

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"events-stocks/controllers/publicaccess"
	"events-stocks/dtos"
	"events-stocks/internal/accesstoken"
	"events-stocks/internal/previewtoken"
	"events-stocks/internal/publicaccessproof"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	momentsService "events-stocks/services/moments"
	"events-stocks/services/ports"
	resourcesService "events-stocks/services/resources"
	validations "events-stocks/services/validations"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

type uploadCounterRepository interface {
	GetKey(ctx context.Context, key string) (string, error)
	Increment(ctx context.Context, key string) (int64, error)
	Decrement(ctx context.Context, key string) (int64, error)
	Expire(ctx context.Context, key string, ttl time.Duration) error
}

func respondMomentUploadError(c echo.Context, message string, err error) error {
	status, detail := resourcesService.UploadErrorResponse(err)
	return utils.Error(c, status, message, detail)
}

var (
	publicTokenRepo       ports.AccessTokenRepository
	publicEventRepo       ports.EventsRepository
	publicEventConfigRepo ports.EventConfigRepository
	publicInvitationRepo  ports.InvitationRepository
	publicUploadCounter   uploadCounterRepository
	publicResSvc          *resourcesService.ResourceService
	publicMomentNow       = time.Now
)

type momentUploadTokenRequest struct {
	PrettyToken           string `json:"pretty_token"`
	PrettyTokenCamel      string `json:"prettyToken"`
	PrettyTokenPascal     string `json:"PrettyToken"`
	InvitationToken       string `json:"invitation_token"`
	InvitationTokenCamel  string `json:"invitationToken"`
	InvitationTokenPascal string `json:"InvitationToken"`
	Token                 string `json:"token"`
	TokenPascal           string `json:"Token"`
}

func (r momentUploadTokenRequest) invitationToken() string {
	return publicMomentInvitationToken(
		r.PrettyToken,
		r.PrettyTokenCamel,
		r.PrettyTokenPascal,
		r.InvitationToken,
		r.InvitationTokenCamel,
		r.InvitationTokenPascal,
		r.Token,
		r.TokenPascal,
	)
}

type momentUploadFileRequest struct {
	ContentType       string `json:"content_type"`
	ContentTypeCamel  string `json:"contentType"`
	ContentTypePascal string `json:"ContentType"`
	Filename          string `json:"filename"`
	FilenameCamel     string `json:"fileName"`
	FilenamePascal    string `json:"FileName"`
	FileSize          int64  `json:"file_size"`
	FileSizeCamel     int64  `json:"fileSize"`
	FileSizePascal    int64  `json:"FileSize"`
}

func (r momentUploadFileRequest) filename() string {
	return firstNonEmpty(r.Filename, r.FilenameCamel, r.FilenamePascal)
}

func (r momentUploadFileRequest) contentType() string {
	return firstNonEmpty(r.ContentType, r.ContentTypeCamel, r.ContentTypePascal)
}

func (r momentUploadFileRequest) fileSize() int64 {
	return firstNonZeroInt64(r.FileSize, r.FileSizeCamel, r.FileSizePascal)
}

type momentUploadObjectRequest struct {
	ObjectKey         string `json:"object_key"`
	ObjectKeyCamel    string `json:"objectKey"`
	ObjectKeyPascal   string `json:"ObjectKey"`
	S3Key             string `json:"s3_key"`
	S3KeyCamel        string `json:"s3Key"`
	S3KeyPascal       string `json:"S3Key"`
	ContentType       string `json:"content_type"`
	ContentTypeCamel  string `json:"contentType"`
	ContentTypePascal string `json:"ContentType"`
	Description       string `json:"description"`
	FileSize          int64  `json:"file_size"`
	FileSizeCamel     int64  `json:"fileSize"`
	FileSizePascal    int64  `json:"FileSize"`
}

func (r momentUploadObjectRequest) objectKey() string {
	return uploadObjectKey(r.ObjectKey, r.ObjectKeyCamel, r.ObjectKeyPascal, r.S3Key, r.S3KeyCamel, r.S3KeyPascal)
}

func (r momentUploadObjectRequest) contentType() string {
	return firstNonEmpty(r.ContentType, r.ContentTypeCamel, r.ContentTypePascal)
}

func (r momentUploadObjectRequest) fileSize() int64 {
	return firstNonZeroInt64(r.FileSize, r.FileSizeCamel, r.FileSizePascal)
}

type multipartUploadReferenceRequest struct {
	UploadID       string `json:"upload_id"`
	UploadIDCamel  string `json:"uploadId"`
	UploadIDPascal string `json:"UploadID"`
}

func (r multipartUploadReferenceRequest) uploadID() string {
	return firstNonEmpty(r.UploadID, r.UploadIDCamel, r.UploadIDPascal)
}

type publicMomentUploadURLRequest struct {
	momentUploadTokenRequest
	momentUploadFileRequest
}

type publicMomentConfirmRequest struct {
	momentUploadTokenRequest
	momentUploadObjectRequest
}

type sharedUploadURLRequest = momentUploadFileRequest

type sharedBatchUploadURLRequest struct {
	Files []momentUploadFileRequest `json:"files"`
}

type sharedMomentConfirmRequest = momentUploadObjectRequest

type sharedMultipartStartRequest struct {
	momentUploadFileRequest
}

type sharedMultipartAbortRequest struct {
	multipartUploadReferenceRequest
	momentUploadObjectRequest
}

type sharedMultipartCompleteRequest struct {
	multipartUploadReferenceRequest
	momentUploadObjectRequest
	Parts              []dtos.CompletedUploadPart `json:"parts"`
	PartsPascal        []dtos.CompletedUploadPart `json:"Parts"`
	CompletedParts     []dtos.CompletedUploadPart `json:"completed_parts"`
	CompletedPartsAlt  []dtos.CompletedUploadPart `json:"completedParts"`
	CompletedPartsCaps []dtos.CompletedUploadPart `json:"CompletedParts"`
}

func (r sharedMultipartCompleteRequest) completedParts() []dtos.CompletedUploadPart {
	switch {
	case len(r.Parts) > 0:
		return r.Parts
	case len(r.PartsPascal) > 0:
		return r.PartsPascal
	case len(r.CompletedParts) > 0:
		return r.CompletedParts
	case len(r.CompletedPartsAlt) > 0:
		return r.CompletedPartsAlt
	default:
		return r.CompletedPartsCaps
	}
}

func InitPublicMomentsController(
	tokenRepo ports.AccessTokenRepository,
	eventRepo ports.EventsRepository,
	eventConfigRepo ports.EventConfigRepository,
	invitationRepo ports.InvitationRepository,
	uploadCounter uploadCounterRepository,
	resSvc *resourcesService.ResourceService,
) {
	publicTokenRepo = tokenRepo
	publicEventRepo = eventRepo
	publicEventConfigRepo = eventConfigRepo
	publicInvitationRepo = invitationRepo
	publicUploadCounter = uploadCounter
	publicResSvc = resSvc
}

func getEventByIdentifier(identifier string) (*models.Event, error) {
	if publicEventRepo == nil {
		return nil, errors.New("event repository not initialized")
	}
	return publicEventRepo.GetEventByIdentifier(identifier)
}

// uploadLimitKey returns the Redis key for tracking per-IP uploads for an event.
func uploadLimitKey(eventID, ip string) string {
	return fmt.Sprintf("moments:upload_count:%s:%s", eventID, ip)
}

const uploadWindowDays = 30

// defaultMaxUploadsPerIP is the global fallback when EventConfig.MaxUploadsPerGuest is 0.
// 30 = allows up to 3 batches of 10 files from the shared upload page.
const defaultMaxUploadsPerIP = 30

var publicMomentVideoExtensions = map[string]bool{
	".3gp":  true,
	".avi":  true,
	".m4v":  true,
	".mkv":  true,
	".mov":  true,
	".mp4":  true,
	".webm": true,
}

func publicMomentUploadSizeLimit(filename, contentType string) (int64, int) {
	normalizedContentType := strings.TrimSpace(strings.ToLower(contentType))
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	if strings.HasPrefix(normalizedContentType, "video/") || publicMomentVideoExtensions[extension] {
		return resourcesService.MaxVideoFileSizeBytes, resourcesService.MaxVideoFileSizeMB
	}
	return resourcesService.MaxMomentImageFileSizeBytes, resourcesService.MaxMomentImageFileSizeMB
}

func publicMomentContentTypeFromObjectKey(objectKey string) string {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(objectKey)))
	switch extension {
	case ".avi":
		return "video/x-msvideo"
	case ".mkv":
		return "video/x-matroska"
	case ".heic":
		return "image/heic"
	case ".heif":
		return "image/heif"
	case ".avif":
		return "image/avif"
	}
	return mime.TypeByExtension(extension)
}

func effectiveMaxUploads(maxUploads int) int {
	if maxUploads <= 0 {
		return defaultMaxUploadsPerIP
	}
	return maxUploads
}

func maxUploadsForConfig(cfg *models.EventConfig) int {
	if cfg == nil {
		return defaultMaxUploadsPerIP
	}
	return effectiveMaxUploads(cfg.MaxUploadsPerGuest)
}

// checkAndIncrementUploadLimit checks the per-IP upload counter.
// maxUploads is the per-event configured limit; pass 0 to use the global default.
// Returns (limitReached, currentCount, quotaReserved, error).
func checkAndIncrementUploadLimit(ctx context.Context, eventID, ip string, maxUploads int) (bool, int64, bool, error) {
	maxUploads = effectiveMaxUploads(maxUploads)
	key := uploadLimitKey(eventID, ip)
	if publicUploadCounter == nil {
		return false, 0, false, nil
	}
	count, err := publicUploadCounter.Increment(ctx, key)
	if err != nil {
		// Redis unavailable — allow the upload rather than blocking
		return false, 0, false, nil
	}
	// Set TTL on first increment
	if count == 1 {
		_ = publicUploadCounter.Expire(ctx, key, uploadWindowDays*24*time.Hour)
	}
	return count > int64(maxUploads), count, true, nil
}

func rollbackUploadLimit(eventID, ip string) {
	if publicUploadCounter == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := publicUploadCounter.Decrement(ctx, uploadLimitKey(eventID, ip)); err != nil {
		slog.Error("moment upload quota rollback failed", "event_id", eventID, "error", err)
	}
}

func publicResourceServiceForEvent(event *models.Event) *resourcesService.ResourceService {
	if publicResSvc == nil || event == nil {
		return publicResSvc
	}
	return publicResSvc.WithBucket(event.MediaBucket).WithOrganization(event.ClientID)
}

func cleanupMomentUpload(eventID, ip, objectKey string, releaseQuota bool, scoped ...*resourcesService.ResourceService) {
	svc := publicResSvc
	if len(scoped) > 0 && scoped[0] != nil {
		svc = scoped[0]
	}
	if svc != nil && strings.TrimSpace(objectKey) != "" {
		if err := svc.DeleteMomentUpload(objectKey); err != nil {
			slog.Error("moment upload object rollback failed", "event_id", eventID, "object_key", objectKey, "error", err)
		}
	}
	if releaseQuota {
		rollbackUploadLimit(eventID, ip)
	}
}

func cleanupMultipartUpload(eventID, objectKey, uploadID string, scoped ...*resourcesService.ResourceService) {
	svc := publicResSvc
	if len(scoped) > 0 && scoped[0] != nil {
		svc = scoped[0]
	}
	if svc == nil {
		return
	}
	if err := svc.AbortMomentMultipartUpload(objectKey, uploadID); err != nil {
		slog.Warn("multipart upload abort rollback failed", "event_id", eventID, "object_key", objectKey, "error", err)
	}
	cleanupMomentUpload(eventID, "", objectKey, false, svc)
}

const defaultPageLimit = 20
const maxPageLimit = 500
const multipartPartSize = 8 * 1024 * 1024

type uploadQuota struct {
	Limit     int64
	Used      int64
	Remaining int64
}

func getUploadQuota(c echo.Context, eventID uuid.UUID, cfg *models.EventConfig) uploadQuota {
	limit := int64(maxUploadsForConfig(cfg))

	var used int64
	if publicUploadCounter != nil {
		countStr, _ := publicUploadCounter.GetKey(c.Request().Context(), uploadLimitKey(eventID.String(), c.RealIP()))
		fmt.Sscanf(countStr, "%d", &used)
		if used < 0 {
			used = 0
		}
	}

	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	return uploadQuota{Limit: limit, Used: used, Remaining: remaining}
}

func uploadsOpenForMoments(cfg *models.EventConfig) bool {
	return cfg != nil && cfg.AllowUploads && !cfg.ShowMomentWall
}

func sharedUploadsOpen(cfg *models.EventConfig) bool {
	return uploadsOpenForMoments(cfg) && cfg.ShareUploadsEnabled
}

func requirePublicEventActive(c echo.Context, event *models.Event, previewAllowed bool) (bool, error) {
	if event != nil && !event.IsActive && !previewAllowed {
		return false, utils.Error(c, http.StatusForbidden, "Event is not public", "")
	}
	return true, nil
}

func requirePublicEventAccessWindow(c echo.Context, cfg *models.EventConfig, previewAllowed bool) (bool, error) {
	if !previewAllowed && !publicaccess.EventAccessWindowOpen(cfg, publicMomentNow()) {
		return false, utils.Error(c, http.StatusForbidden, "Event is not public", "")
	}
	return true, nil
}

func requireUploadsOpen(c echo.Context, cfg *models.EventConfig) (bool, error) {
	if cfg == nil {
		return false, utils.Error(c, http.StatusNotFound, "Event config not found", "")
	}
	if cfg.ShowMomentWall {
		return false, utils.Error(c, http.StatusForbidden, "Uploads are closed because the moments wall is already published", "")
	}
	if !cfg.AllowUploads {
		return false, utils.Error(c, http.StatusForbidden, "Uploads are disabled for this event", "")
	}
	return true, nil
}

func requireSharedUploadsOpen(c echo.Context, cfg *models.EventConfig) (bool, error) {
	if ok, err := requireUploadsOpen(c, cfg); !ok {
		return false, err
	}
	if !cfg.ShareUploadsEnabled {
		return false, utils.Error(c, http.StatusForbidden, "Shared uploads are not enabled for this event", "")
	}
	return true, nil
}

func publicMomentInvitationToken(values ...string) string {
	for _, value := range values {
		if token := strings.TrimSpace(value); token != "" {
			return token
		}
	}
	return ""
}

func withMomentUploadQuota(moment *models.Moment, quota uploadQuota) interface{} {
	if moment == nil {
		return dtos.PublicUploadQuotaResponse{
			UploadsLimit:     quota.Limit,
			UploadsUsed:      quota.Used,
			UploadsRemaining: quota.Remaining,
		}
	}
	publicMoment := dtos.NewPublicMoment(*moment)
	if publicResSvc != nil {
		svc := publicResSvc.WithBucket(moment.MediaBucket)
		publicMoment.ContentURL = canonicalMomentStoragePath(svc, publicMoment.ContentURL)
		publicMoment.ThumbnailURL = canonicalMomentStoragePath(svc, publicMoment.ThumbnailURL)
		if contentViewURL, expiresAt := presignMomentURLWithExpiry(svc, publicMoment.ContentURL); strings.TrimSpace(contentViewURL) != "" {
			publicMoment.ContentViewURL = contentViewURL
			publicMoment.ContentViewURLExpiresAt = expiresAt
		}
		if thumbnailViewURL, expiresAt := presignMomentURLWithExpiry(svc, publicMoment.ThumbnailURL); strings.TrimSpace(thumbnailViewURL) != "" {
			publicMoment.ThumbnailViewURL = thumbnailViewURL
			publicMoment.ThumbnailViewURLExpiresAt = expiresAt
		}
	}
	return dtos.PublicMomentUploadResponse{
		PublicMoment:     publicMoment,
		UploadsLimit:     quota.Limit,
		UploadsUsed:      quota.Used,
		UploadsRemaining: quota.Remaining,
	}
}

type publicMomentsPageOptions struct {
	Total                int64
	Page                 int
	Limit                int
	HasMore              bool
	NextCursor           string
	Published            bool
	MomentsWallPublished bool
	AllowUploads         bool
	AllowMessages        bool
	ShareUploadsEnabled  bool
}

func publicMomentsPage(event *models.Event, moments []models.Moment, quota uploadQuota, opts publicMomentsPageOptions) dtos.PublicMomentsPage {
	items := newPublicMomentResponses(moments)
	if items == nil {
		items = []dtos.PublicMoment{}
	}

	var eventName string
	var eventType string
	var eventDate *time.Time
	var timezone string
	if event != nil {
		eventName = event.Name
		eventType = event.EventType.Name
		timezone = event.Timezone
		if !event.EventDateTime.IsZero() {
			copyDate := event.EventDateTime
			eventDate = &copyDate
		}
	}

	return dtos.PublicMomentsPage{
		Items:                items,
		Total:                opts.Total,
		Page:                 opts.Page,
		Limit:                opts.Limit,
		HasMore:              opts.HasMore,
		NextCursor:           opts.NextCursor,
		Published:            opts.Published,
		MomentsWallPublished: opts.MomentsWallPublished,
		ShowMomentWall:       opts.MomentsWallPublished,
		AllowUploads:         opts.AllowUploads,
		AllowMessages:        opts.AllowMessages,
		ShareUploadsEnabled:  opts.ShareUploadsEnabled,
		UploadsLimit:         quota.Limit,
		UploadsUsed:          quota.Used,
		UploadsRemaining:     quota.Remaining,
		EventName:            eventName,
		EventType:            eventType,
		EventDate:            eventDate,
		EventDateTime:        eventDate,
		Timezone:             timezone,
	}
}

func uploadLimitReachedPayload(eventName string, maxUploads int, uploadedCount int64) dtos.PublicUploadLimitReachedResponse {
	maxUploads = effectiveMaxUploads(maxUploads)
	remaining := int64(maxUploads) - uploadedCount
	if remaining < 0 {
		remaining = 0
	}
	return dtos.PublicUploadLimitReachedResponse{
		Message:          fmt.Sprintf("Gracias por compartir tus momentos en %s. Ya registramos tus contribuciones permitidas.", eventName),
		AlreadyUploaded:  true,
		EventName:        eventName,
		UploadsLimit:     int64(maxUploads),
		UploadsUsed:      uploadedCount,
		UploadsRemaining: remaining,
	}
}

func writeUploadLimitReached(c echo.Context, eventName string, maxUploads int, uploadedCount int64) error {
	payload := uploadLimitReachedPayload(eventName, maxUploads, uploadedCount)
	message := payload.Message
	if message == "" {
		message = "Upload limit reached"
	}
	return utils.ErrorWithData(c, http.StatusTooManyRequests, message, "", payload)
}

type publicMomentCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
	Order     *int      `json:"order,omitempty"`
}

func encodeCursor(moment models.Moment) string {
	order := moment.Order
	payload, _ := json.Marshal(publicMomentCursor{
		CreatedAt: moment.CreatedAt,
		ID:        moment.ID.String(),
		Order:     &order,
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeCursor(raw string) (*publicMomentCursor, error) {
	if raw == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var cursor publicMomentCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, err
	}
	if cursor.ID == "" || cursor.CreatedAt.IsZero() {
		return nil, fmt.Errorf("cursor is missing required fields")
	}
	if cursor.Order == nil {
		return nil, fmt.Errorf("cursor is missing order; restart pagination")
	}
	return &cursor, nil
}

func newPublicMomentResponses(moments []models.Moment) []dtos.PublicMoment {
	items := dtos.NewPublicMoments(moments)
	if publicResSvc == nil {
		return items
	}

	for i := range items {
		svc := publicResSvc.WithBucket(moments[i].MediaBucket)
		items[i].ContentURL = canonicalMomentStoragePath(svc, items[i].ContentURL)
		items[i].ThumbnailURL = canonicalMomentStoragePath(svc, items[i].ThumbnailURL)
		if contentViewURL, expiresAt := presignMomentURLWithExpiry(svc, items[i].ContentURL); strings.TrimSpace(contentViewURL) != "" {
			items[i].ContentViewURL = contentViewURL
			items[i].ContentViewURLExpiresAt = expiresAt
		}
		if thumbnailViewURL, expiresAt := presignMomentURLWithExpiry(svc, items[i].ThumbnailURL); strings.TrimSpace(thumbnailViewURL) != "" {
			items[i].ThumbnailViewURL = thumbnailViewURL
			items[i].ThumbnailViewURLExpiresAt = expiresAt
		}
		for variantIndex := range items[i].MediaVariants {
			variant := &items[i].MediaVariants[variantIndex]
			variant.URL = canonicalMomentStoragePath(svc, variant.URL)
			if viewURL, expiresAt := presignMomentURLWithExpiry(svc, variant.URL); strings.TrimSpace(viewURL) != "" {
				variant.ViewURL = viewURL
				variant.ExpiresAt = expiresAt
			}
		}
	}
	return items
}

func parsePublicLimit(c echo.Context, fallback int) int {
	limit := fallback
	if l, err := strconv.Atoi(c.QueryParam("limit")); err == nil && l > 0 {
		if l > maxPageLimit {
			return maxPageLimit
		}
		return l
	}
	return limit
}

func loadPublicEventConfig(eventID uuid.UUID) *models.EventConfig {
	if publicEventConfigRepo == nil {
		return eventsService.NewDefaultEventConfig(eventID)
	}
	cfg, err := publicEventConfigRepo.GetEventConfigByID(eventID)
	if err != nil || cfg == nil {
		return eventsService.NewDefaultEventConfig(eventID)
	}
	return cfg.WithVisibilityDefaults()
}

func allowMomentWallPreview(c echo.Context, eventID uuid.UUID) (bool, error) {
	token := utils.PublicPreviewToken(c)
	if token == "" {
		return false, nil
	}
	if !previewtoken.Validate(token, eventID) {
		return false, fmt.Errorf("invalid preview token")
	}
	return true, nil
}

func allowMomentWallInvitation(c echo.Context, eventID uuid.UUID) (bool, error) {
	invitationToken := utils.PublicInvitationQueryToken(c)
	if invitationToken == "" {
		return false, nil
	}
	if publicTokenRepo == nil || publicInvitationRepo == nil {
		return false, fmt.Errorf("invitation repositories not initialized")
	}
	token, err := accesstoken.Lookup(publicTokenRepo, invitationToken)
	if err != nil || token == nil || isExpiredPublicInvitationToken(token) {
		return false, fmt.Errorf("invalid invitation token")
	}
	inv, err := publicInvitationRepo.GetInvitationByIDLite(token.InvitationID)
	if err != nil || inv == nil || inv.EventID != eventID {
		return false, fmt.Errorf("token does not belong to this event")
	}
	return true, nil
}

func requirePublicEventPasswordAccess(c echo.Context, eventID uuid.UUID, cfg *models.EventConfig, previewAllowed bool) (bool, error) {
	if cfg == nil || !cfg.HasAuthPasswordPreview() || previewAllowed {
		return true, nil
	}
	if publicaccessproof.Validate(utils.PublicEventAccessToken(c), eventID, eventsService.EventConfigAccessVersion(cfg)) {
		return true, nil
	}
	return false, utils.Error(c, http.StatusUnauthorized, "Event password required", "")
}

// GET /api/events/:identifier/moments?page=1&limit=20
// Returns only approved + fully optimized moments (processing_status empty or "done").
// Results are cached in Redis/Valkey; cache is busted on approval changes or Lambda completion.
func ListPublicMoments(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
	}

	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}

	// Respect ShowMomentWall flag
	cfg := loadPublicEventConfig(event.ID)
	previewAllowed, previewErr := allowMomentWallPreview(c, event.ID)
	uploadStatusProbe := c.QueryParam("purpose") == "upload"
	if ok, err := requirePublicEventActive(c, event, previewAllowed); !ok {
		return err
	}
	if ok, err := requirePublicEventAccessWindow(c, cfg, previewAllowed); !ok {
		return err
	}
	invitationAllowed := false
	if !cfg.IsPublic && !previewAllowed && !uploadStatusProbe {
		var invitationErr error
		invitationAllowed, invitationErr = allowMomentWallInvitation(c, event.ID)
		if invitationErr != nil {
			return utils.Error(c, http.StatusUnauthorized, "Invalid invitation token", invitationErr.Error())
		}
		if !invitationAllowed {
			if previewErr != nil {
				return utils.Error(c, http.StatusForbidden, "Invalid preview token", previewErr.Error())
			}
			return utils.Error(c, http.StatusForbidden, "Event is not public", "")
		}
	}
	if ok, err := requirePublicEventPasswordAccess(c, event.ID, cfg, previewAllowed); !ok {
		return err
	}
	wallPublished := cfg.ShowMomentWall
	allowUploads := uploadsOpenForMoments(cfg)
	allowMessages := cfg.AllowMessages
	shareUploadsEnabled := sharedUploadsOpen(cfg)
	quota := getUploadQuota(c, event.ID, cfg)
	if uploadStatusProbe && !cfg.IsPublic && !previewAllowed {
		return utils.Success(c, http.StatusOK, "Upload status loaded", publicMomentsPage(event, nil, quota, publicMomentsPageOptions{
			Total:                0,
			HasMore:              false,
			Published:            false,
			MomentsWallPublished: wallPublished,
			AllowUploads:         allowUploads,
			AllowMessages:        allowMessages,
			ShareUploadsEnabled:  shareUploadsEnabled,
		}))
	}
	if !cfg.ShowMomentWall && !previewAllowed {
		return utils.Success(c, http.StatusOK, "Moments not yet available", publicMomentsPage(event, nil, quota, publicMomentsPageOptions{
			Total:                0,
			HasMore:              false,
			Published:            false,
			MomentsWallPublished: wallPublished,
			AllowUploads:         allowUploads,
			AllowMessages:        allowMessages,
			ShareUploadsEnabled:  shareUploadsEnabled,
		}))
	}

	// Cursor mode is used by the modern infinite gallery.
	if _, hasCursor := c.QueryParams()["cursor"]; hasCursor {
		cursor, err := decodeCursor(c.QueryParam("cursor"))
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid cursor", err.Error())
		}
		limit := parsePublicLimit(c, 100)
		var afterCreatedAt *time.Time
		afterID := ""
		if cursor != nil {
			afterCreatedAt = &cursor.CreatedAt
			afterID = cursor.ID
		}

		var afterOrder *int
		if cursor != nil {
			afterOrder = cursor.Order
		}
		items, total, err := momentsService.ListApprovedForWallCursor(event.ID, afterCreatedAt, afterID, afterOrder, limit+1)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error loading moments", err.Error())
		}
		hasMore := len(items) > limit
		if hasMore {
			items = items[:limit]
		}
		nextCursor := ""
		if hasMore {
			nextCursor = encodeCursor(items[len(items)-1])
		}
		return utils.Success(c, http.StatusOK, "Moments loaded", publicMomentsPage(event, items, quota, publicMomentsPageOptions{
			Total:                total,
			Limit:                limit,
			HasMore:              hasMore,
			NextCursor:           nextCursor,
			Published:            true,
			MomentsWallPublished: wallPublished,
			AllowUploads:         allowUploads,
			AllowMessages:        allowMessages,
			ShareUploadsEnabled:  shareUploadsEnabled,
		}))
	}

	// Parse pagination params
	page := 1
	limit := parsePublicLimit(c, defaultPageLimit)
	if p, err := strconv.Atoi(c.QueryParam("page")); err == nil && p > 0 {
		page = p
	}

	// Cached + paginated approved moments
	items, total, err := momentsService.ListApprovedForWall(event.ID, page, limit)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading moments", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Moments loaded", publicMomentsPage(event, items, quota, publicMomentsPageOptions{
		Total:                total,
		Page:                 page,
		Limit:                limit,
		HasMore:              int64(page*limit) < total,
		Published:            true,
		MomentsWallPublished: wallPublished,
		AllowUploads:         allowUploads,
		AllowMessages:        allowMessages,
		ShareUploadsEnabled:  shareUploadsEnabled,
	}))
}

// POST /api/events/:identifier/moments  — requires a personal invitation token.
func CreatePublicMoment(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
	}

	prettyToken := publicMomentInvitationToken(
		c.FormValue("pretty_token"),
		c.FormValue("prettyToken"),
		c.FormValue("PrettyToken"),
		c.FormValue("invitation_token"),
		c.FormValue("invitationToken"),
		c.FormValue("InvitationToken"),
		c.FormValue("Token"),
		c.FormValue("token"),
	)
	if prettyToken == "" {
		return utils.Error(c, http.StatusUnauthorized, "Missing invitation token", "")
	}

	token, err := accesstoken.Lookup(publicTokenRepo, prettyToken)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusUnauthorized, "Invalid invitation token", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error validating token", err.Error())
	}
	if token == nil || isExpiredPublicInvitationToken(token) {
		return utils.Error(c, http.StatusUnauthorized, "Invalid invitation token", "")
	}

	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}
	if ok, err := requirePublicEventActive(c, event, false); !ok {
		return err
	}

	cfg := loadPublicEventConfig(event.ID)
	if ok, err := requirePublicEventAccessWindow(c, cfg, false); !ok {
		return err
	}
	if ok, err := requirePublicEventPasswordAccess(c, event.ID, cfg, false); !ok {
		return err
	}
	if ok, err := requireUploadsOpen(c, cfg); !ok {
		return err
	}

	// Verify token belongs to this event
	inv, err := publicInvitationRepo.GetInvitationByIDLite(token.InvitationID)
	if err != nil || inv == nil || inv.EventID != event.ID {
		return utils.Error(c, http.StatusUnauthorized, "Token does not belong to this event", "")
	}

	// Per-IP upload limit — respect per-event config, fall back to global default.
	// Check before reading the file, but only increment after a valid upload.
	maxUploads := maxUploadsForConfig(cfg)
	if ok, err := ensureUploadSlots(c, event, cfg, 1); !ok {
		return err
	}

	file, header, err := c.Request().FormFile("file")
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Missing file", err.Error())
	}
	defer file.Close()
	if publicResSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Resource service unavailable", "")
	}

	eventResSvc := publicResourceServiceForEvent(event)
	rawKey, contentType, err := eventResSvc.UploadRawToMomentsFolderContext(c.Request().Context(), file, header, event.ID.String())
	if err != nil {
		return respondMomentUploadError(c, "Error uploading file", err)
	}
	limitReached, uploadedCount, quotaReserved, _ := checkAndIncrementUploadLimit(c.Request().Context(), event.ID.String(), c.RealIP(), maxUploads)
	if limitReached {
		cleanupMomentUpload(event.ID.String(), c.RealIP(), rawKey, quotaReserved, eventResSvc)
		return writeUploadLimitReached(c, event.Name, maxUploads, uploadedCount)
	}

	description := publicMomentDescription(cfg, c.FormValue("description"))
	eventID := event.ID
	invID := token.InvitationID
	isApproved := cfg != nil && cfg.AutoApproveUploads
	moment := models.Moment{
		EventID:          &eventID,
		InvitationID:     &invID,
		ContentURL:       rawKey,
		MediaBucket:      eventResSvc.Bucket,
		ContentType:      contentType,
		Description:      description,
		IsApproved:       isApproved,
		ProcessingStatus: "pending",
	}

	if err := momentsService.CreateMoment(&moment); err != nil {
		cleanupMomentUpload(event.ID.String(), c.RealIP(), rawKey, quotaReserved, eventResSvc)
		return utils.Error(c, http.StatusInternalServerError, "Error saving moment", err.Error())
	}

	// A queue failure is persisted as terminal "failed" by the service. The raw
	// object remains private and can be retried by an admin; never publish it as
	// a legacy-ready moment.
	momentsService.EnqueueMediaProcessing(&moment, rawKey, eventResSvc.Bucket, contentType)

	go recordMomentCreatedAnalytics(eventID, description)

	return utils.Success(c, http.StatusCreated, "Moment submitted for review", withMomentUploadQuota(&moment, getUploadQuota(c, event.ID, cfg)))
}

// POST /api/events/:identifier/moments/shared — shared QR upload (no personal token)
// Requires EventConfig.ShareUploadsEnabled = true.
func CreateSharedMoment(c echo.Context) error {
	event, cfg, ok := loadSharedUploadContext(c)
	if !ok {
		return nil
	}

	// Per-IP upload limit — respect per-event config, fall back to global default.
	// Check before reading the file, but only increment after a valid upload.
	maxUploads := maxUploadsForConfig(cfg)
	if ok, err := ensureUploadSlots(c, event, cfg, 1); !ok {
		return err
	}

	file, header, err := c.Request().FormFile("file")
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Missing file", err.Error())
	}
	defer file.Close()
	if publicResSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Resource service unavailable", "")
	}

	eventResSvc := publicResourceServiceForEvent(event)
	rawKey, contentType, err := eventResSvc.UploadRawToMomentsFolderContext(c.Request().Context(), file, header, event.ID.String())
	if err != nil {
		return respondMomentUploadError(c, "Error uploading file", err)
	}
	limitReached, uploadedCount, quotaReserved, _ := checkAndIncrementUploadLimit(c.Request().Context(), event.ID.String(), c.RealIP(), maxUploads)
	if limitReached {
		cleanupMomentUpload(event.ID.String(), c.RealIP(), rawKey, quotaReserved, eventResSvc)
		return writeUploadLimitReached(c, event.Name, maxUploads, uploadedCount)
	}

	description := publicMomentDescription(cfg, c.FormValue("description"))
	eventID := event.ID
	isApproved := cfg.AutoApproveUploads
	moment := models.Moment{
		EventID:          &eventID,
		ContentURL:       rawKey,
		MediaBucket:      eventResSvc.Bucket,
		ContentType:      contentType,
		Description:      description,
		IsApproved:       isApproved,
		ProcessingStatus: "pending",
	}

	if err := momentsService.CreateMoment(&moment); err != nil {
		cleanupMomentUpload(event.ID.String(), c.RealIP(), rawKey, quotaReserved, eventResSvc)
		return utils.Error(c, http.StatusInternalServerError, "Error saving moment", err.Error())
	}

	momentsService.EnqueueMediaProcessing(&moment, rawKey, eventResSvc.Bucket, contentType)

	go recordMomentCreatedAnalytics(eventID, description)

	return utils.Success(c, http.StatusCreated, "Moment submitted for review", withMomentUploadQuota(&moment, getUploadQuota(c, event.ID, cfg)))
}

func RequestPublicMomentUploadURL(c echo.Context) error {
	identifier := c.Param("identifier")
	var body publicMomentUploadURLRequest
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	event, cfg, _, err := validatePersonalUploadRequest(
		c,
		identifier,
		body.invitationToken(),
	)
	if err != nil {
		return writePublicUploadRequestError(c, err)
	}
	if ok, err := requireUploadsOpen(c, cfg); !ok {
		return err
	}
	if ok, err := ensureUploadSlots(c, event, cfg, 1); !ok {
		return err
	}
	return issueMomentUploadURL(c, event, cfg, body.filename(), body.contentType(), body.fileSize())
}

func ConfirmPublicMoment(c echo.Context) error {
	identifier := c.Param("identifier")
	var body publicMomentConfirmRequest
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	event, cfg, invID, err := validatePersonalUploadRequest(
		c,
		identifier,
		body.invitationToken(),
	)
	if err != nil {
		return writePublicUploadRequestError(c, err)
	}
	if ok, err := requireUploadsOpen(c, cfg); !ok {
		return err
	}
	moment, err := confirmPresignedMoment(c, event, cfg, invID, body.objectKey(), body.contentType(), body.fileSize(), publicMomentDescription(cfg, body.Description))
	if c.Response().Committed {
		return nil
	}
	if err != nil {
		return err
	}
	return utils.Success(c, http.StatusCreated, "Moment submitted for review", withMomentUploadQuota(moment, getUploadQuota(c, event.ID, cfg)))
}

func RequestSharedUploadURL(c echo.Context) error {
	event, cfg, ok := loadSharedUploadContext(c)
	if !ok {
		return nil
	}
	var body sharedUploadURLRequest
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if cfg == nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", "")
	}
	if ok, err := ensureUploadSlots(c, event, cfg, 1); !ok {
		return err
	}
	return issueMomentUploadURL(c, event, cfg, body.filename(), body.contentType(), body.fileSize())
}

func RequestBatchSharedUploadURLs(c echo.Context) error {
	event, cfg, ok := loadSharedUploadContext(c)
	if !ok {
		return nil
	}
	if publicResSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Resource service unavailable", "")
	}
	if cfg == nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", "")
	}
	var body sharedBatchUploadURLRequest
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if len(body.Files) == 0 {
		return utils.Error(c, http.StatusBadRequest, "files are required", "")
	}
	if len(body.Files) > 10 {
		return utils.Error(c, http.StatusBadRequest, "too many files", "max 10")
	}
	if ok, err := ensureUploadSlots(c, event, cfg, len(body.Files)); !ok {
		return err
	}
	urls := make([]dtos.MomentUploadURLResponse, 0, len(body.Files))
	eventResSvc := publicResourceServiceForEvent(event)
	for _, file := range body.Files {
		if err := validateRequestedMomentUploadSize(file.filename(), file.contentType(), file.fileSize()); err != nil {
			return respondMomentUploadError(c, "Invalid file", err)
		}
		s3Key, uploadURL, contentType, err := eventResSvc.PrepareMomentUploadURL(event.ID.String(), file.filename(), file.contentType())
		if err != nil {
			return respondMomentUploadError(c, "Could not prepare file upload", err)
		}
		urls = append(urls, dtos.NewMomentUploadURLResponse(uploadURL, s3Key, contentType))
	}
	return utils.Success(c, http.StatusOK, "Upload URLs generated", dtos.NewMomentUploadURLBatchResponse(urls, uploadQuotaMetadata(getUploadQuota(c, event.ID, cfg))))
}

func ConfirmSharedMoment(c echo.Context) error {
	event, cfg, ok := loadSharedUploadContext(c)
	if !ok {
		return nil
	}
	var body sharedMomentConfirmRequest
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	moment, err := confirmPresignedMoment(c, event, cfg, nil, body.objectKey(), body.contentType(), body.fileSize(), publicMomentDescription(cfg, body.Description))
	if c.Response().Committed {
		return nil
	}
	if err != nil {
		return err
	}
	return utils.Success(c, http.StatusCreated, "Moment submitted for review", withMomentUploadQuota(moment, getUploadQuota(c, event.ID, cfg)))
}

func StartSharedMultipartUpload(c echo.Context) error {
	event, cfg, ok := loadSharedUploadContext(c)
	if !ok {
		return nil
	}
	if publicResSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Resource service unavailable", "")
	}
	if cfg == nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", "")
	}
	var body sharedMultipartStartRequest
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	fileSize := body.fileSize()
	if fileSize <= 0 {
		return utils.Error(c, http.StatusBadRequest, "file_size is required", "")
	}
	filename := body.filename()
	contentType := body.contentType()
	maxBytes, maxMB := publicMomentUploadSizeLimit(filename, contentType)
	if fileSize > maxBytes {
		return utils.Error(c, http.StatusBadRequest, "file too large", fmt.Sprintf("max %d MB", maxMB))
	}
	if ok, err := ensureUploadSlots(c, event, cfg, 1); !ok {
		return err
	}
	partCount := int(math.Ceil(float64(fileSize) / float64(multipartPartSize)))
	eventResSvc := publicResourceServiceForEvent(event)
	s3Key, uploadID, partURLs, contentType, err := eventResSvc.PrepareMomentMultipartUpload(event.ID.String(), filename, contentType, partCount)
	if err != nil {
		return respondMomentUploadError(c, "Error preparing multipart upload", err)
	}
	return utils.Success(c, http.StatusOK, "Multipart upload started", dtos.NewSharedMultipartUploadStartResponseWithQuota(uploadID, s3Key, partURLs, contentType, uploadQuotaMetadata(getUploadQuota(c, event.ID, cfg))))
}

func AbortSharedMultipartUpload(c echo.Context) error {
	event, ok := loadPublicEventContext(c)
	if !ok {
		return nil
	}
	cfg := loadPublicEventConfig(event.ID)
	previewAllowed, previewErr := allowMomentWallPreview(c, event.ID)
	if previewErr != nil {
		return utils.Error(c, http.StatusForbidden, "Invalid preview token", previewErr.Error())
	}
	if ok, err := requirePublicEventPasswordAccess(c, event.ID, cfg, previewAllowed); !ok {
		return err
	}
	if publicResSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Resource service unavailable", "")
	}
	var body sharedMultipartAbortRequest
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	objectKey := body.objectKey()
	uploadID := body.uploadID()
	if uploadID == "" || objectKey == "" {
		return utils.Error(c, http.StatusBadRequest, "upload_id and object_key are required", "")
	}
	eventResSvc := publicResourceServiceForEvent(event)
	if err := eventResSvc.ValidateMomentRawKey(event.ID.String(), objectKey); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid upload key", err.Error())
	}
	if err := eventResSvc.AbortMomentMultipartUpload(objectKey, uploadID); err != nil {
		return respondMomentUploadError(c, "Error aborting multipart upload", err)
	}
	return utils.Success(c, http.StatusOK, "Multipart upload aborted", nil)
}

func CompleteSharedMultipartUpload(c echo.Context) error {
	event, cfg, ok := loadSharedUploadContext(c)
	if !ok {
		return nil
	}
	if publicResSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Resource service unavailable", "")
	}
	var body sharedMultipartCompleteRequest
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	objectKey := body.objectKey()
	uploadID := body.uploadID()
	if uploadID == "" || objectKey == "" {
		return utils.Error(c, http.StatusBadRequest, "upload_id and object_key are required", "")
	}
	eventResSvc := publicResourceServiceForEvent(event)
	if err := eventResSvc.ValidateMomentRawKey(event.ID.String(), objectKey); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid upload key", err.Error())
	}
	if existing, lookupErr := findConfirmedMoment(event.ID, objectKey); lookupErr != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error checking upload confirmation", lookupErr.Error())
	} else if existing != nil {
		return utils.Success(c, http.StatusCreated, "Moment submitted for review", withMomentUploadQuota(existing, getUploadQuota(c, event.ID, cfg)))
	}
	if ok, err := ensureUploadSlots(c, event, cfg, 1); !ok {
		cleanupMultipartUpload(event.ID.String(), objectKey, uploadID, eventResSvc)
		return err
	}
	if err := eventResSvc.CompleteMomentMultipartUpload(objectKey, uploadID, body.completedParts()); err != nil {
		message := "Error completing multipart upload"
		if validations.IsValidationError(err) {
			message = "Invalid multipart upload"
		}
		return respondMomentUploadError(c, message, err)
	}
	contentType := body.contentType()
	if contentType == "" {
		contentType = publicMomentContentTypeFromObjectKey(objectKey)
	}
	moment, confirmErr := confirmPresignedMoment(c, event, cfg, nil, objectKey, contentType, body.fileSize(), publicMomentDescription(cfg, body.Description))
	if c.Response().Committed {
		return nil
	}
	if confirmErr != nil {
		return confirmErr
	}
	return utils.Success(c, http.StatusCreated, "Moment submitted for review", withMomentUploadQuota(moment, getUploadQuota(c, event.ID, cfg)))
}

type publicUploadRequestError struct {
	status  int
	message string
	detail  string
}

func (e *publicUploadRequestError) Error() string {
	return e.message
}

func newPublicUploadRequestError(status int, message, detail string) error {
	return &publicUploadRequestError{status: status, message: message, detail: detail}
}

func writePublicUploadRequestError(c echo.Context, err error) error {
	var reqErr *publicUploadRequestError
	if errors.As(err, &reqErr) {
		return utils.Error(c, reqErr.status, reqErr.message, reqErr.detail)
	}
	return err
}

func validatePersonalUploadRequest(c echo.Context, identifier, prettyToken string) (*models.Event, *models.EventConfig, *uuid.UUID, error) {
	if identifier == "" {
		return nil, nil, nil, newPublicUploadRequestError(http.StatusBadRequest, "Missing event identifier", "")
	}
	if prettyToken == "" {
		return nil, nil, nil, newPublicUploadRequestError(http.StatusUnauthorized, "Missing invitation token", "")
	}
	token, err := accesstoken.Lookup(publicTokenRepo, prettyToken)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, newPublicUploadRequestError(http.StatusUnauthorized, "Invalid invitation token", "")
		}
		return nil, nil, nil, newPublicUploadRequestError(http.StatusInternalServerError, "Error validating token", err.Error())
	}
	if token == nil || isExpiredPublicInvitationToken(token) {
		return nil, nil, nil, newPublicUploadRequestError(http.StatusUnauthorized, "Invalid invitation token", "")
	}
	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, newPublicUploadRequestError(http.StatusNotFound, "Event not found", "")
		}
		return nil, nil, nil, newPublicUploadRequestError(http.StatusInternalServerError, "Error loading event", err.Error())
	}
	if !event.IsActive {
		return nil, nil, nil, newPublicUploadRequestError(http.StatusForbidden, "Event is not public", "")
	}
	inv, err := publicInvitationRepo.GetInvitationByIDLite(token.InvitationID)
	if err != nil || inv == nil || inv.EventID != event.ID {
		return nil, nil, nil, newPublicUploadRequestError(http.StatusUnauthorized, "Token does not belong to this event", "")
	}
	cfg := loadPublicEventConfig(event.ID)
	if !publicaccess.EventAccessWindowOpen(cfg, publicMomentNow()) {
		return nil, nil, nil, newPublicUploadRequestError(http.StatusForbidden, "Event is not public", "")
	}
	if cfg != nil && cfg.HasAuthPasswordPreview() && !publicaccessproof.Validate(utils.PublicEventAccessToken(c), event.ID, eventsService.EventConfigAccessVersion(cfg)) {
		return nil, nil, nil, newPublicUploadRequestError(http.StatusUnauthorized, "Event password required", "")
	}
	return event, cfg, &token.InvitationID, nil
}

func isExpiredPublicInvitationToken(token *models.InvitationAccessToken) bool {
	return token != nil && token.ExpiresAt != nil && publicMomentNow().After(*token.ExpiresAt)
}

func issueMomentUploadURL(c echo.Context, event *models.Event, cfg *models.EventConfig, filename, contentType string, fileSize int64) error {
	if publicResSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Resource service unavailable", "")
	}
	if err := validateRequestedMomentUploadSize(filename, contentType, fileSize); err != nil {
		return respondMomentUploadError(c, "Invalid file", err)
	}
	eventResSvc := publicResourceServiceForEvent(event)
	s3Key, uploadURL, normalizedContentType, err := eventResSvc.PrepareMomentUploadURL(event.ID.String(), filename, contentType)
	if err != nil {
		return respondMomentUploadError(c, "Could not prepare file upload", err)
	}
	return utils.Success(c, http.StatusOK, "Upload URL generated", dtos.NewMomentUploadURLResponseWithQuota(uploadURL, s3Key, normalizedContentType, uploadQuotaMetadata(getUploadQuota(c, event.ID, cfg))))
}

func validateRequestedMomentUploadSize(filename, contentType string, fileSize int64) error {
	if fileSize <= 0 {
		return nil
	}
	maxBytes, maxMB := publicMomentUploadSizeLimit(filename, contentType)
	if fileSize > maxBytes {
		return validations.ValidationError{Msg: fmt.Sprintf("file size exceeds %d MB", maxMB)}
	}
	return nil
}

func uploadQuotaMetadata(quota uploadQuota) dtos.UploadQuotaMetadata {
	return dtos.NewUploadQuotaMetadata(quota.Limit, quota.Used, quota.Remaining)
}

func uploadObjectKey(values ...string) string {
	return firstNonEmpty(values...)
}

func publicMomentDescription(cfg *models.EventConfig, description string) string {
	if cfg != nil && !cfg.AllowMessages {
		return ""
	}
	return description
}

func ensureUploadSlots(c echo.Context, event *models.Event, cfg *models.EventConfig, requested int) (bool, error) {
	if requested <= 0 || publicUploadCounter == nil {
		return true, nil
	}
	maxUploads := maxUploadsForConfig(cfg)
	countStr, _ := publicUploadCounter.GetKey(c.Request().Context(), uploadLimitKey(event.ID.String(), c.RealIP()))
	var uploadedCount int64
	fmt.Sscanf(countStr, "%d", &uploadedCount)
	if uploadedCount+int64(requested) <= int64(maxUploads) {
		return true, nil
	}
	return false, writeUploadLimitReached(c, event.Name, maxUploads, uploadedCount)
}

func loadPublicEventContext(c echo.Context) (*models.Event, bool) {
	identifier := c.Param("identifier")
	if identifier == "" {
		_ = utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
		return nil, false
	}
	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			_ = utils.Error(c, http.StatusNotFound, "Event not found", "")
			return nil, false
		}
		_ = utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
		return nil, false
	}
	return event, true
}

func loadSharedUploadContext(c echo.Context) (*models.Event, *models.EventConfig, bool) {
	event, ok := loadPublicEventContext(c)
	if !ok {
		return nil, nil, false
	}
	previewAllowed, previewErr := allowMomentWallPreview(c, event.ID)
	if previewErr != nil {
		_ = utils.Error(c, http.StatusForbidden, "Invalid preview token", previewErr.Error())
		return nil, nil, false
	}
	if ok, _ := requirePublicEventActive(c, event, previewAllowed); !ok {
		return nil, nil, false
	}
	cfg := loadPublicEventConfig(event.ID)
	if ok, _ := requirePublicEventAccessWindow(c, cfg, previewAllowed); !ok {
		return nil, nil, false
	}
	if ok, _ := requirePublicEventPasswordAccess(c, event.ID, cfg, previewAllowed); !ok {
		return nil, nil, false
	}
	if ok, _ := requireSharedUploadsOpen(c, cfg); !ok {
		return nil, nil, false
	}
	return event, cfg, true
}

func confirmPresignedMoment(c echo.Context, event *models.Event, cfg *models.EventConfig, invitationID *uuid.UUID, objectKey, contentType string, expectedSize int64, description string) (*models.Moment, error) {
	if publicResSvc == nil {
		return nil, utils.Error(c, http.StatusInternalServerError, "Resource service unavailable", "")
	}
	if objectKey == "" {
		return nil, utils.Error(c, http.StatusBadRequest, "object_key is required", "")
	}
	eventResSvc := publicResourceServiceForEvent(event)
	if err := eventResSvc.ValidateMomentRawKey(event.ID.String(), objectKey); err != nil {
		return nil, utils.Error(c, http.StatusBadRequest, "Invalid upload key", err.Error())
	}
	existing, lookupErr := findConfirmedMoment(event.ID, objectKey)
	if lookupErr != nil {
		return nil, utils.Error(c, http.StatusInternalServerError, "Error checking upload confirmation", lookupErr.Error())
	}
	if existing != nil {
		if tagErr := eventResSvc.MarkMomentUploadConfirmed(c.Request().Context(), objectKey); tagErr != nil {
			slog.Warn("could not mark existing moment upload as confirmed", "event_id", event.ID, "object_key", objectKey, "error", tagErr)
		}
		return existing, nil
	}
	verifiedContentType, verifyErr := eventResSvc.VerifyMomentUploadSize(objectKey, contentType, expectedSize)
	if verifyErr != nil {
		cleanupMomentUpload(event.ID.String(), c.RealIP(), objectKey, false, eventResSvc)
		return nil, respondMomentUploadError(c, "Upload verification failed", verifyErr)
	}
	contentType = verifiedContentType

	maxUploads := maxUploadsForConfig(cfg)
	ip := c.RealIP()
	ctx := c.Request().Context()
	limitReached, uploadedCount, quotaReserved, _ := checkAndIncrementUploadLimit(ctx, event.ID.String(), ip, maxUploads)
	if limitReached {
		cleanupMomentUpload(event.ID.String(), ip, objectKey, quotaReserved, eventResSvc)
		return nil, writeUploadLimitReached(c, event.Name, maxUploads, uploadedCount)
	}

	eventID := event.ID
	isApproved := cfg != nil && cfg.AutoApproveUploads
	moment := models.Moment{
		EventID:          &eventID,
		InvitationID:     invitationID,
		ContentURL:       objectKey,
		MediaBucket:      eventResSvc.Bucket,
		ContentType:      contentType,
		Description:      description,
		IsApproved:       isApproved,
		ProcessingStatus: "pending",
	}
	if err := momentsService.CreateMoment(&moment); err != nil {
		cleanupMomentUpload(event.ID.String(), ip, objectKey, quotaReserved, eventResSvc)
		return nil, utils.Error(c, http.StatusInternalServerError, "Error saving moment", err.Error())
	}
	if tagErr := eventResSvc.MarkMomentUploadConfirmed(ctx, objectKey); tagErr != nil {
		slog.Warn("could not mark moment upload as confirmed", "event_id", event.ID, "moment_id", moment.ID, "object_key", objectKey, "error", tagErr)
	}
	momentsService.EnqueueMediaProcessing(&moment, objectKey, eventResSvc.Bucket, contentType)
	go recordMomentCreatedAnalytics(eventID, description)
	return &moment, nil
}

func findConfirmedMoment(eventID uuid.UUID, objectKey string) (*models.Moment, error) {
	if !momentsService.IsInitialized() {
		return nil, nil
	}
	moment, err := momentsService.GetMomentByEventIDAndContentURL(eventID, strings.TrimSpace(objectKey))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return moment, err
}
