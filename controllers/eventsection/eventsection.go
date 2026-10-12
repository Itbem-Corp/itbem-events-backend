package eventsection

import (
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"strings"
	"time"
)

var eventSectionSvc *eventsService.EventSectionService

func InitEventSectionController(svc *eventsService.EventSectionService) {
	eventSectionSvc = svc
}

// respondInvalidSectionConfig answers 400 for a rejected section payload. A
// link with a disallowed scheme (javascript:, data:...) gets the Spanish
// message and the stable code invalid_section_url (STU-N4).
func respondInvalidSectionConfig(c echo.Context, err error) error {
	var urlErr dtos.InvalidSectionURLError
	if errors.As(err, &urlErr) {
		return utils.Error(c, http.StatusBadRequest, urlErr.Error(), dtos.InvalidSectionURLCode)
	}
	return utils.Error(c, http.StatusBadRequest, "Invalid section config", err.Error())
}

// SectionConflictCode is the code of a PUT /sections/:id rejected because the
// section changed after the client loaded it (STU-N3).
const SectionConflictCode = "section_conflict"

// respondSectionConflict answers 409 with the current section in data so the
// editor can reload it. The code is sent both as "error" (the envelope's
// usual code field) and "code".
func respondSectionConflict(c echo.Context, current *models.EventSection) error {
	return c.JSON(http.StatusConflict, map[string]interface{}{
		"status":  http.StatusConflict,
		"message": "Esta sección cambió en otra pestaña o por otra persona.",
		"error":   SectionConflictCode,
		"code":    SectionConflictCode,
		"data":    dtos.NewEventSectionResponse(*current),
	})
}

// GET /events/:id/sections
func ListSectionsByEvent(c echo.Context) error {
	idParam := c.Param("id")
	eventID, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event UUID", err.Error())
	}
	if _, _, authErr := authz.RequireEventAccess(c, eventID); authErr != nil {
		return authz.Respond(c, authErr)
	}

	sections, err := eventSectionSvc.ListByEventID(eventID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading sections", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Sections loaded", dtos.NewEventSectionResponses(sections))
}

// POST /events/:id/sections
func CreateSection(c echo.Context) error {
	idParam := c.Param("id")
	eventID, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event UUID", err.Error())
	}
	user, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityEventManage)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}

	var payload dtos.EventSectionPayload
	if err := c.Bind(&payload); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	var section models.EventSection
	section.EventID = eventID
	if err := dtos.ApplyEventSectionPayload(&section, payload, true); err != nil {
		return respondInvalidSectionConfig(c, err)
	}
	if err := authz.RequireEventSectionForCreate(user, section.EventID); err != nil {
		return authz.Respond(c, err)
	}
	if err := eventSectionSvc.CreateEventSection(&section); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating section", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Section created", dtos.NewEventSectionResponse(section))
}

// PUT /sections/:id
func UpdateSection(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	_, section, authErr := authz.RequireEventSectionCapability(c, id, authz.CapabilityEventManage)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}
	originalEventID := section.EventID
	var payload dtos.EventSectionPayload
	if err := c.Bind(&payload); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	var expectedUpdatedAt *time.Time
	if raw := strings.TrimSpace(payload.ExpectedUpdatedAt); raw != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, raw)
		if parseErr != nil {
			return utils.Error(c, http.StatusBadRequest, "expected_updated_at debe ser una fecha RFC 3339 válida", "invalid_expected_updated_at")
		}
		expectedUpdatedAt = &parsed
		if !eventsService.SectionUpdatedAtMatches(section.UpdatedAt, parsed) {
			return respondSectionConflict(c, section)
		}
	}

	section.ID = id
	section.EventID = originalEventID
	if err := dtos.ApplyEventSectionPayload(section, payload, false); err != nil {
		return respondInvalidSectionConfig(c, err)
	}
	if expectedUpdatedAt != nil {
		if err := eventSectionSvc.UpdateEventSectionIfUnmodified(section, *expectedUpdatedAt); err != nil {
			if errors.Is(err, ports.ErrEventSectionConflict) {
				current, loadErr := eventSectionSvc.GetEventSectionByID(id)
				if loadErr != nil {
					return utils.Error(c, http.StatusInternalServerError, "Error loading section", loadErr.Error())
				}
				return respondSectionConflict(c, current)
			}
			return utils.WriteFailed(c, "Error updating section", err)
		}
	} else if err := eventSectionSvc.UpdateEventSection(section); err != nil {
		return utils.WriteFailed(c, "Error updating section", err)
	}

	return utils.Success(c, http.StatusOK, "Section updated", dtos.NewEventSectionResponse(*section))
}

// PATCH /events/:id/sections/reorder
func ReorderSections(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event UUID", err.Error())
	}

	user, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityEventManage)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}

	var body struct {
		Sections []sectionOrderRequest `json:"sections"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if len(body.Sections) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No sections provided", "")
	}
	if len(body.Sections) > 200 {
		return utils.Error(c, http.StatusBadRequest, "Too many sections", "max 200")
	}

	updates := make(map[uuid.UUID]int, len(body.Sections))
	for _, item := range body.Sections {
		order, hasOrder := item.order()
		if !hasOrder {
			return utils.Error(c, http.StatusBadRequest, "Missing section order", "order is required")
		}
		if order < 0 {
			return utils.Error(c, http.StatusBadRequest, "Invalid section order", "order must be zero or greater")
		}
		sectionID, err := uuid.FromString(item.ID)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid section UUID: "+item.ID, "")
		}
		if _, exists := updates[sectionID]; exists {
			return utils.Error(c, http.StatusBadRequest, "Duplicate section ID: "+item.ID, "")
		}

		section, authErr := authz.EnsureEventSectionAccess(user, sectionID)
		if authErr != nil {
			return authz.Respond(c, authErr)
		}
		if section.EventID != eventID {
			return utils.Error(c, http.StatusBadRequest, "Section does not belong to event", sectionID.String())
		}
		updates[sectionID] = order
	}

	if err := eventSectionSvc.BulkUpdateSectionOrder(eventID, updates); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error reordering sections", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Sections reordered", nil)
}

// DELETE /sections/:id
func DeleteSection(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if _, _, authErr := authz.RequireEventSectionCapability(c, id, authz.CapabilityEventManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	if err := eventSectionSvc.DeleteEventSection(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting section", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Section deleted", nil)
}

type sectionOrderRequest struct {
	ID           string `json:"id"`
	Order        *int   `json:"order"`
	SortOrder    *int   `json:"sort_order"`
	SortOrderAlt *int   `json:"sortOrder"`
}

func (r sectionOrderRequest) order() (int, bool) {
	if r.Order != nil {
		return *r.Order, true
	}
	if r.SortOrder != nil {
		return *r.SortOrder, true
	}
	if r.SortOrderAlt != nil {
		return *r.SortOrderAlt, true
	}
	return 0, false
}
