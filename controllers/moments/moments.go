package moments

import (
	"crypto/subtle"
	"errors"
	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	momentsService "events-stocks/services/moments"
	resourcesService "events-stocks/services/resources"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	momentSvc   *momentsService.MomentService
	adminResSvc *resourcesService.ResourceService
)

const maxSummaryEventIDs = 100
const momentViewURLTTLMinutes = 720

func InitMomentsController(svc *momentsService.MomentService, resSvc ...*resourcesService.ResourceService) {
	momentSvc = svc
	adminResSvc = nil
	if len(resSvc) > 0 {
		adminResSvc = resSvc[0]
	}
}

func newAdminMomentResponses(items []models.Moment) []dtos.MomentResponse {
	responses := make([]dtos.MomentResponse, 0, len(items))
	for i := range items {
		responses = append(responses, newAdminMomentResponse(&items[i]))
	}
	return responses
}

func recordMomentCreatedAnalytics(eventID uuid.UUID, description string) {
	if eventID == uuid.Nil {
		return
	}
	eventsService.IncrementAnalytics(eventID, "moment_uploads")
	if hasMomentComment(description) {
		eventsService.IncrementAnalytics(eventID, "moment_comments")
	}
}

func adjustMomentCommentAnalytics(eventID uuid.UUID, before string, after string) {
	if eventID == uuid.Nil {
		return
	}
	beforeHasComment := hasMomentComment(before)
	afterHasComment := hasMomentComment(after)
	switch {
	case beforeHasComment == afterHasComment:
		return
	case afterHasComment:
		eventsService.AdjustAnalytics(eventID, "moment_comments", 1)
	default:
		eventsService.AdjustAnalytics(eventID, "moment_comments", -1)
	}
}

func hasMomentComment(description string) bool {
	return strings.TrimSpace(description) != ""
}

func newAdminMomentResponse(moment *models.Moment) dtos.MomentResponse {
	response := dtos.NewMomentResponse(moment)
	if moment == nil {
		return response
	}
	svc := adminResSvc.WithBucket(moment.MediaBucket)
	response.ContentURL = canonicalMomentStoragePath(svc, moment.ContentURL)
	response.ThumbnailURL = canonicalMomentStoragePath(svc, moment.ThumbnailURL)
	if contentViewURL, expiresAt := presignMomentURLWithExpiry(svc, moment.ContentURL); strings.TrimSpace(contentViewURL) != "" {
		response.ContentViewURL = contentViewURL
		response.ContentViewURLExpiresAt = expiresAt
	}
	if thumbnailViewURL, expiresAt := presignMomentURLWithExpiry(svc, moment.ThumbnailURL); strings.TrimSpace(thumbnailViewURL) != "" {
		response.ThumbnailViewURL = thumbnailViewURL
		response.ThumbnailViewURLExpiresAt = expiresAt
	}
	return response
}

func canonicalMomentStoragePath(resSvc *resourcesService.ResourceService, path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || resSvc == nil {
		return path
	}
	// Scheme-relative URLs are external locations, not bucket keys. They do not
	// contain "://", so preserve them before invoking the legacy-key parser.
	if strings.HasPrefix(trimmed, "//") {
		return path
	}
	normalized := resSvc.CanonicalObjectKey(trimmed)
	if normalized == "" {
		return path
	}
	return normalized
}

func presignMomentURLWithExpiry(resSvc *resourcesService.ResourceService, path string) (string, *time.Time) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || resSvc == nil {
		return path, nil
	}
	trimmed = canonicalMomentStoragePath(resSvc, trimmed)
	if utils.IsAbsoluteURLLike(trimmed) {
		return path, nil
	}
	url, err := resSvc.GetPresignedURLWithTTL(trimmed, momentViewURLTTLMinutes)
	if err != nil || url == "" {
		return trimmed, nil
	}
	expiresAt := time.Now().UTC().Add(time.Duration(momentViewURLTTLMinutes) * time.Minute)
	return url, &expiresAt
}

func parseEventIDsParam(raw string) ([]uuid.UUID, error) {
	parts := strings.Split(raw, ",")
	ids := make([]uuid.UUID, 0, len(parts))
	seen := make(map[uuid.UUID]struct{}, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		id, err := uuid.FromString(trimmed)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

// GET /moments/summary?event_ids=<uuid>,<uuid>
func ListMomentSummaries(c echo.Context) error {
	rawEventIDs := c.QueryParam("event_ids")
	if strings.TrimSpace(rawEventIDs) == "" {
		return utils.Error(c, http.StatusBadRequest, "event_ids required", "Provide a comma-separated list of event IDs")
	}

	eventIDs, err := parseEventIDsParam(rawEventIDs)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event_ids", err.Error())
	}
	if len(eventIDs) == 0 {
		return utils.Error(c, http.StatusBadRequest, "event_ids required", "Provide at least one valid event ID")
	}
	if len(eventIDs) > maxSummaryEventIDs {
		return utils.Error(c, http.StatusBadRequest, "Too many event_ids", "max 100")
	}

	for _, eventID := range eventIDs {
		if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityView); authErr != nil {
			return authz.Respond(c, authErr)
		}
	}

	summaries, err := momentSvc.ListPendingSummaryByEventIDs(eventIDs)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading moment summaries", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Moment summaries loaded", summaries)
}

// GET /moments?event_id=<uuid>  (filters by event when param provided)
// Returns only moments ready for review (processing_status NOT IN ('pending','processing')).
// This ensures admins only see moments that have been fully optimized by Lambda.
func ListMoments(c echo.Context) error {
	if eventIDStr := c.QueryParam("event_id"); eventIDStr != "" {
		eventID, err := uuid.FromString(eventIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid event_id", err.Error())
		}
		if _, _, authErr := authz.RequireEventAccess(c, eventID); authErr != nil {
			return authz.Respond(c, authErr)
		}
		if c.QueryParam("page") != "" || c.QueryParam("page_size") != "" {
			page, err := strconv.Atoi(c.QueryParam("page"))
			if err != nil || page < 1 {
				return utils.Error(c, http.StatusBadRequest, "Invalid page", "page must be positive")
			}
			pageSize, err := strconv.Atoi(c.QueryParam("page_size"))
			if err != nil || pageSize < 1 || pageSize > 100 {
				return utils.Error(c, http.StatusBadRequest, "Invalid page_size", "page_size must be between 1 and 100")
			}
			var list, inFlight, reoptimizing []models.Moment
			var counts dtos.MomentDashboardCounts
			var listErr, inFlightErr, reoptimizingErr error
			var wait sync.WaitGroup
			wait.Add(3)
			go func() {
				defer wait.Done()
				list, counts, listErr = momentSvc.ListForDashboardPage(eventID, page, pageSize)
			}()
			go func() {
				defer wait.Done()
				inFlight, inFlightErr = momentSvc.ListInFlight(eventID)
			}()
			go func() {
				defer wait.Done()
				reoptimizing, reoptimizingErr = momentSvc.ListReoptimizing(eventID)
			}()
			wait.Wait()
			if listErr != nil {
				return utils.Error(c, http.StatusInternalServerError, "Error loading moments", listErr.Error())
			}
			if inFlightErr != nil {
				return utils.Error(c, http.StatusInternalServerError, "Error loading in-flight moments", inFlightErr.Error())
			}
			if reoptimizingErr != nil {
				return utils.Error(c, http.StatusInternalServerError, "Error loading reoptimizing moments", reoptimizingErr.Error())
			}
			totalPages := 0
			if counts.Total > 0 {
				totalPages = int((counts.Total + int64(pageSize) - 1) / int64(pageSize))
			}
			return utils.Success(c, http.StatusOK, "Moments page loaded", dtos.MomentDashboardPage{
				Data: newAdminMomentResponses(list), InFlight: newAdminMomentResponses(inFlight), Reoptimizing: newAdminMomentResponses(reoptimizing),
				Total: counts.Total, Page: page, PageSize: pageSize, TotalPages: totalPages, Counts: counts,
			})
		}
		// Only return moments that are ready for review (optimized or legacy).
		// Excludes moments still queued/processing by Lambda.
		list, err := momentSvc.ListForDashboard(eventID)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error loading moments", err.Error())
		}
		return utils.Success(c, http.StatusOK, "Moments loaded", newAdminMomentResponses(list))
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	if !user.IsPlatformAdmin() {
		return utils.Error(c, http.StatusBadRequest, "event_id required", "Non-root users must scope moments by event_id")
	}
	list, err := momentSvc.ListMoments()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading moments", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Moments loaded", newAdminMomentResponses(list))
}

// GET /moments/:id
func GetMoment(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	_, moment, authErr := authz.RequireMomentAccess(c, id)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}

	return utils.Success(c, http.StatusOK, "Moment loaded", newAdminMomentResponse(moment))
}

func ListInFlightMoments(c echo.Context) error {
	eventID, err := uuid.FromString(c.QueryParam("event_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event_id", err.Error())
	}
	if _, _, authErr := authz.RequireEventAccess(c, eventID); authErr != nil {
		return authz.Respond(c, authErr)
	}
	list, err := momentSvc.ListInFlight(eventID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading moments", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Moments loaded", newAdminMomentResponses(list))
}

func ListReoptimizingMoments(c echo.Context) error {
	eventID, err := uuid.FromString(c.QueryParam("event_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event_id", err.Error())
	}
	if _, _, authErr := authz.RequireEventAccess(c, eventID); authErr != nil {
		return authz.Respond(c, authErr)
	}
	list, err := momentSvc.ListReoptimizing(eventID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading moments", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Moments loaded", newAdminMomentResponses(list))
}

func ListMomentActivity(c echo.Context) error {
	eventID, err := uuid.FromString(c.QueryParam("event_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event_id", err.Error())
	}
	if _, _, authErr := authz.RequireEventAccess(c, eventID); authErr != nil {
		return authz.Respond(c, authErr)
	}

	var inFlight, reoptimizing []models.Moment
	var inFlightErr, reoptimizingErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		inFlight, inFlightErr = momentSvc.ListInFlight(eventID)
	}()
	go func() {
		defer wait.Done()
		reoptimizing, reoptimizingErr = momentSvc.ListReoptimizing(eventID)
	}()
	wait.Wait()
	if inFlightErr != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading in-flight moments", inFlightErr.Error())
	}
	if reoptimizingErr != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading reoptimizing moments", reoptimizingErr.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment activity loaded", map[string]any{
		"in_flight":    newAdminMomentResponses(inFlight),
		"reoptimizing": newAdminMomentResponses(reoptimizing),
	})
}

func ReorderMoments(c echo.Context) error {
	var body []struct {
		ID    string `json:"id"`
		Order int    `json:"order"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if len(body) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No moments provided", "")
	}

	updates := make(map[uuid.UUID]int, len(body))
	for _, item := range body {
		id, err := uuid.FromString(item.ID)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid UUID: "+item.ID, "")
		}
		if _, _, authErr := authz.RequireMomentCapability(c, id, authz.CapabilityEventManage); authErr != nil {
			return authz.Respond(c, authErr)
		}
		updates[id] = item.Order
	}

	if err := momentSvc.BulkUpdateOrder(updates); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error reordering moments", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Moments reordered", nil)
}

func BatchReoptimizeMoments(c echo.Context) error {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if len(body.IDs) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No IDs provided", "")
	}
	if len(body.IDs) > 200 {
		return utils.Error(c, http.StatusBadRequest, "Too many IDs", "max 200")
	}

	ids := make([]uuid.UUID, 0, len(body.IDs))
	for _, idStr := range body.IDs {
		id, err := uuid.FromString(idStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid UUID: "+idStr, "")
		}
		if _, _, authErr := authz.RequireMomentCapability(c, id, authz.CapabilityEventManage); authErr != nil {
			return authz.Respond(c, authErr)
		}
		ids = append(ids, id)
	}

	succeeded, skipped, failed, err := momentSvc.BatchReoptimize(ids)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error reoptimizing moments", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Moments queued for reoptimization", dtos.MomentBatchResultResponse{
		Succeeded: succeeded,
		Skipped:   skipped,
		Failed:    failed,
	})
}

// BackfillMomentVariants discovers a bounded batch of legacy image moments and
// reuses the monotonic reoptimization flow. Repeated calls skip queued/done
// variant rows, so operators can drain history without a one-shot migration.
func BackfillMomentVariants(c echo.Context) error {
	if _, err := authz.RequireRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	var body struct {
		Limit int `json:"limit"`
	}
	_ = c.Bind(&body)
	if body.Limit <= 0 {
		body.Limit = 100
	}
	if body.Limit > 200 {
		body.Limit = 200
	}
	var ids []uuid.UUID
	err := configuration.DB.Model(&models.Moment{}).
		Where("content_type LIKE 'image/%' AND content_url <> '' AND processing_status IN ('', 'done') AND jsonb_array_length(COALESCE(media_variants, '[]'::jsonb)) = 0").
		Order("updated_at ASC").Limit(body.Limit).Pluck("id", &ids).Error
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to load moment backfill candidates", err.Error())
	}
	succeeded, skipped, failed, err := momentSvc.BatchReoptimize(ids)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error backfilling moment variants", err.Error())
	}
	return utils.Success(c, http.StatusAccepted, "Moment variant backfill batch evaluated", dtos.MomentBatchResultResponse{Succeeded: succeeded, Skipped: skipped, Failed: failed})
}

// POST /moments
func CreateMoment(c echo.Context) error {
	var moment models.Moment
	if err := c.Bind(&moment); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if err := c.Validate(&moment); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}
	if moment.EventID == nil {
		return utils.Error(c, http.StatusBadRequest, "event_id required", "Moment must belong to an event")
	}
	if _, _, authErr := authz.RequireEventCapability(c, *moment.EventID, authz.CapabilityEventManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	if err := momentSvc.CreateMoment(&moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating moment", err.Error())
	}

	if moment.EventID != nil {
		go recordMomentCreatedAnalytics(*moment.EventID, moment.Description)
	}

	return utils.Success(c, http.StatusCreated, "Moment created", newAdminMomentResponse(&moment))
}

// PUT /moments/:id
func UpdateMoment(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	_, existing, authErr := authz.RequireMomentCapability(c, id, authz.CapabilityEventManage)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}

	var req struct {
		Title            *string `json:"title"`
		Description      *string `json:"description"`
		IsApproved       *bool   `json:"is_approved"`
		IsApprovedCamel  *bool   `json:"isApproved"`
		IsApprovedPascal *bool   `json:"IsApproved"`
		Order            *int    `json:"order"`
	}
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	previousDescription := existing.Description
	if req.Title != nil {
		existing.Title = *req.Title
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if isApproved := firstBoolPtr(req.IsApproved, req.IsApprovedCamel, req.IsApprovedPascal); isApproved != nil {
		existing.IsApproved = *isApproved
	}
	if req.Order != nil {
		existing.Order = *req.Order
	}

	if err := momentSvc.UpdateMoment(existing); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating moment", err.Error())
	}

	if req.Description != nil && existing.EventID != nil {
		go adjustMomentCommentAnalytics(*existing.EventID, previousDescription, existing.Description)
	}

	return utils.Success(c, http.StatusOK, "Moment updated", newAdminMomentResponse(existing))
}

// PUT /moments/:id/content  — internal callback for Lambda after video transcoding
// Requires X-Internal-Secret matching the active or temporary rotation secret.
func validInternalAPISecret(provided string) bool {
	if provided == "" {
		return false
	}

	valid := 0
	for _, environmentName := range []string{"INTERNAL_API_SECRET", "INTERNAL_API_SECRET_PREVIOUS"} {
		expected := os.Getenv(environmentName)
		if expected == "" {
			continue
		}
		valid |= subtle.ConstantTimeCompare([]byte(provided), []byte(expected))
	}
	return valid == 1
}

func UpdateMomentContent(c echo.Context) error {
	if !validInternalAPISecret(c.Request().Header.Get("X-Internal-Secret")) {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	var body struct {
		EventID                    string              `json:"event_id"`
		EventIDCamel               string              `json:"eventId"`
		EventIDPascal              string              `json:"EventID"`
		JobID                      string              `json:"job_id"`
		JobIDCamel                 string              `json:"jobId"`
		JobIDPascal                string              `json:"JobID"`
		Generation                 int64               `json:"generation"`
		GenerationPascal           int64               `json:"Generation"`
		ObjectKey                  string              `json:"object_key"`
		ObjectKeyCamel             string              `json:"objectKey"`
		ObjectKeyPascal            string              `json:"ObjectKey"`
		ContentURL                 string              `json:"content_url"`
		ContentURLCamel            string              `json:"contentUrl"`
		ContentURLUpperCamel       string              `json:"contentURL"`
		ContentURLPascal           string              `json:"ContentURL"`
		ProcessingStatus           string              `json:"processing_status"`
		ProcessingStatusCamel      string              `json:"processingStatus"`
		ProcessingStatusPascal     string              `json:"ProcessingStatus"`
		ThumbnailObjectKey         string              `json:"thumbnail_object_key"`
		ThumbnailObjectKeyCamel    string              `json:"thumbnailObjectKey"`
		ThumbnailObjectKeyPascal   string              `json:"ThumbnailObjectKey"`
		ThumbnailURL               string              `json:"thumbnail_url"`
		ThumbnailURLCamel          string              `json:"thumbnailUrl"`
		ThumbnailURLUpperCamel     string              `json:"thumbnailURL"`
		ThumbnailURLPascal         string              `json:"ThumbnailURL"`
		ProcessingDurationMs       int64               `json:"processing_duration_ms"`
		ProcessingDurationMsCamel  int64               `json:"processingDurationMs"`
		ProcessingDurationMsMS     int64               `json:"processingDurationMS"`
		ProcessingDurationMsPascal int64               `json:"ProcessingDurationMs"`
		OriginalSizeBytes          int64               `json:"original_size_bytes"`
		OriginalSizeBytesCamel     int64               `json:"originalSizeBytes"`
		OriginalSizeBytesPascal    int64               `json:"OriginalSizeBytes"`
		OptimizedSizeBytes         int64               `json:"optimized_size_bytes"`
		OptimizedSizeBytesCamel    int64               `json:"optimizedSizeBytes"`
		OptimizedSizeBytesPascal   int64               `json:"OptimizedSizeBytes"`
		ErrorMessage               string              `json:"error_message"`
		ErrorMessageCamel          string              `json:"errorMessage"`
		ErrorMessagePascal         string              `json:"ErrorMessage"`
		MediaVariants              []dtos.MediaVariant `json:"media_variants"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	contentURL := firstNonEmpty(body.ObjectKey, body.ObjectKeyCamel, body.ObjectKeyPascal, body.ContentURL, body.ContentURLCamel, body.ContentURLUpperCamel, body.ContentURLPascal)
	thumbnailURL := firstNonEmpty(body.ThumbnailObjectKey, body.ThumbnailObjectKeyCamel, body.ThumbnailObjectKeyPascal, body.ThumbnailURL, body.ThumbnailURLCamel, body.ThumbnailURLUpperCamel, body.ThumbnailURLPascal)
	processingStatus := firstNonEmpty(body.ProcessingStatus, body.ProcessingStatusCamel, body.ProcessingStatusPascal)
	errorMessage := firstNonEmpty(body.ErrorMessage, body.ErrorMessageCamel, body.ErrorMessagePascal)
	durationMs := firstNonZeroInt64(body.ProcessingDurationMs, body.ProcessingDurationMsCamel, body.ProcessingDurationMsMS, body.ProcessingDurationMsPascal)
	originalBytes := firstNonZeroInt64(body.OriginalSizeBytes, body.OriginalSizeBytesCamel, body.OriginalSizeBytesPascal)
	optimizedBytes := firstNonZeroInt64(body.OptimizedSizeBytes, body.OptimizedSizeBytesCamel, body.OptimizedSizeBytesPascal)
	eventID := firstNonEmpty(body.EventID, body.EventIDCamel, body.EventIDPascal)
	jobID := firstNonEmpty(body.JobID, body.JobIDCamel, body.JobIDPascal)
	generation := firstNonZeroInt64(body.Generation, body.GenerationPascal)

	if err := momentSvc.ApplyMediaProcessingCallback(dtos.MediaProcessingCallback{
		MomentID:             id.String(),
		EventID:              eventID,
		JobID:                jobID,
		Generation:           generation,
		ObjectKey:            contentURL,
		ThumbnailObjectKey:   thumbnailURL,
		ProcessingStatus:     processingStatus,
		ErrorMessage:         errorMessage,
		ProcessingDurationMs: durationMs,
		OriginalSizeBytes:    originalBytes,
		OptimizedSizeBytes:   optimizedBytes,
		MediaVariants:        body.MediaVariants,
	}); err != nil {
		if errors.Is(err, momentsService.ErrInvalidMomentProcessingStatus) {
			return utils.Error(c, http.StatusBadRequest, "Invalid processing status", err.Error())
		}
		if errors.Is(err, momentsService.ErrInvalidMomentProcessingCallback) {
			return utils.Error(c, http.StatusBadRequest, "Invalid processing callback", err.Error())
		}
		if errors.Is(err, momentsService.ErrStaleMomentProcessingCallback) {
			return utils.Error(c, http.StatusConflict, "Stale processing callback", err.Error())
		}
		return utils.Error(c, http.StatusInternalServerError, "Error updating moment", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment content updated", nil)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstBoolPtr(values ...*bool) *bool {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// DELETE /moments/:id
func DeleteMoment(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	if _, _, authErr := authz.RequireMomentCapability(c, id, authz.CapabilityEventManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	if err := momentSvc.DeleteMoment(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting moment", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment deleted", nil)
}

// DELETE /moments/bulk — body: {"ids": ["uuid1", "uuid2", ...]}
func BulkDeleteMoments(c echo.Context) error {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if len(body.IDs) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No IDs provided", "")
	}

	ids := make([]uuid.UUID, 0, len(body.IDs))
	for _, idStr := range body.IDs {
		id, err := uuid.FromString(idStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid UUID: "+idStr, "")
		}
		if _, _, authErr := authz.RequireMomentCapability(c, id, authz.CapabilityEventManage); authErr != nil {
			return authz.Respond(c, authErr)
		}
		ids = append(ids, id)
	}

	if err := momentSvc.BulkDeleteMoments(ids); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting moments", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Moments deleted", nil)
}

// PUT /moments/:id/requeue — admin action to retry failed/stuck Lambda processing.
// Resets processing_status to "pending" and re-publishes the SQS job.
func RequeueMoment(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	_, moment, authErr := authz.RequireMomentCapability(c, id, authz.CapabilityEventManage)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}

	if err := momentSvc.RequeueMoment(moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error requeueing moment", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment requeued", newAdminMomentResponse(moment))
}

// POST /moments/bulk-approve — bulk approve or reject multiple moments.
func BulkApproveRejectMoments(c echo.Context) error {
	var body struct {
		IDs              []string `json:"ids"`
		IsApproved       *bool    `json:"is_approved"`
		IsApprovedCamel  *bool    `json:"isApproved"`
		IsApprovedPascal *bool    `json:"IsApproved"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if len(body.IDs) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No IDs provided", "")
	}

	uuids := make([]uuid.UUID, 0, len(body.IDs))
	for _, idStr := range body.IDs {
		id, err := uuid.FromString(idStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid UUID: "+idStr, "")
		}
		uuids = append(uuids, id)
	}
	for _, id := range uuids {
		if _, _, authErr := authz.RequireMomentCapability(c, id, authz.CapabilityEventManage); authErr != nil {
			return authz.Respond(c, authErr)
		}
	}

	isApproved := false
	if value := firstBoolPtr(body.IsApproved, body.IsApprovedCamel, body.IsApprovedPascal); value != nil {
		isApproved = *value
	}
	if err := momentSvc.BulkUpdateApproval(uuids, isApproved); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating moments", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Moments updated", nil)
}
