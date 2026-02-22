package moments

import (
	"events-stocks/models"
	momentsService "events-stocks/services/moments"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"os"
)

var momentSvc *momentsService.MomentService

func InitMomentsController(svc *momentsService.MomentService) {
	momentSvc = svc
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
		// Only return moments that are ready for review (optimized or legacy).
		// Excludes moments still queued/processing by Lambda.
		list, err := momentSvc.ListForDashboard(eventID)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error loading moments", err.Error())
		}
		return utils.Success(c, http.StatusOK, "Moments loaded", list)
	}
	list, err := momentSvc.ListMoments()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading moments", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Moments loaded", list)
}

// GET /moments/:id
func GetMoment(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	moment, err := momentSvc.GetMomentByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Moment not found", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment loaded", moment)
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

	if err := momentSvc.CreateMoment(&moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating moment", err.Error())
	}

	// TODO: track moment_uploads in EventAnalytics once models.Moment has an EventID field.
	// Currently Moment only has InvitationID; resolve via Invitation -> EventID to enable:
	// go func() { eventService.IncrementAnalytics(eventID, "moment_uploads") }()

	return utils.Success(c, http.StatusCreated, "Moment created", moment)
}

// PUT /moments/:id
func UpdateMoment(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	var moment models.Moment
	if err := c.Bind(&moment); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if err := c.Validate(&moment); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}

	moment.ID = id
	if err := momentSvc.UpdateMoment(&moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating moment", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment updated", moment)
}

// PUT /moments/:id/content  — internal callback for Lambda after video transcoding
// Requires header: X-Internal-Secret matching INTERNAL_API_SECRET env var.
func UpdateMomentContent(c echo.Context) error {
	secret := os.Getenv("INTERNAL_API_SECRET")
	if secret == "" || c.Request().Header.Get("X-Internal-Secret") != secret {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	var body struct {
		ContentURL           string `json:"content_url"`
		ProcessingStatus     string `json:"processing_status"`
		ProcessingDurationMs int64  `json:"processing_duration_ms"`
		OriginalSizeBytes    int64  `json:"original_size_bytes"`
		OptimizedSizeBytes   int64  `json:"optimized_size_bytes"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	if err := momentSvc.UpdateMomentContent(id, body.ContentURL, body.ProcessingStatus, body.ProcessingDurationMs, body.OriginalSizeBytes, body.OptimizedSizeBytes); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating moment", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment content updated", nil)
}

// DELETE /moments/:id
func DeleteMoment(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	if err := momentSvc.DeleteMoment(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting moment", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment deleted", nil)
}

// PUT /moments/:id/requeue — admin action to retry failed/stuck Lambda processing.
// Resets processing_status to "pending" and re-publishes the SQS job.
func RequeueMoment(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	moment, err := momentSvc.GetMomentByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Moment not found", err.Error())
	}

	if err := momentSvc.RequeueMoment(moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error requeueing moment", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment requeued", moment)
}

// POST /moments/bulk-approve — bulk approve or reject multiple moments.
func BulkApproveRejectMoments(c echo.Context) error {
	var body struct {
		IDs        []string `json:"ids"`
		IsApproved bool     `json:"is_approved"`
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

	if err := momentSvc.BulkUpdateApproval(uuids, body.IsApproved); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating moments", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Moments updated", nil)
}
