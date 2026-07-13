package guests

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"events-stocks/controllers/publicaccess"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	guestService "events-stocks/services/guests"
	"events-stocks/services/ports"
	resourcesService "events-stocks/services/resources"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var errAttendeeServiceUnavailable = errors.New("attendee service unavailable")

const maxGuestCSVRows = 250000

func guestCSVStatus(guest models.Guest) string {
	status := strings.ToUpper(strings.TrimSpace(guest.RSVPStatus))
	if status == "" {
		status = strings.ToUpper(strings.TrimSpace(guest.GuestStatus.Code))
	}
	if status == "" {
		return "PENDING"
	}
	return status
}

func guestCSVTable(guest models.Guest) string {
	if guest.Table != nil && strings.TrimSpace(guest.Table.Name) != "" {
		return strings.TrimSpace(guest.Table.Name)
	}
	table := strings.TrimSpace(guest.TableNumber)
	if table == "" || strings.HasPrefix(strings.ToLower(table), "mesa") {
		return table
	}
	return "Mesa " + table
}

func ExportGuestsCSV(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event UUID", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityGuestManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	query := dtos.CheckinGuestsListQuery{
		Page: 1, PageSize: maxGuestCSVRows, Search: c.QueryParam("search"), Filter: c.QueryParam("filter"),
		Sort: c.QueryParam("sort"), Direction: c.QueryParam("direction"),
	}
	list, total, listErr := guestSvc.ListCheckinGuests(eventID, query)
	if listErr != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error exporting guests", listErr.Error())
	}
	if total > maxGuestCSVRows {
		return utils.Error(c, http.StatusRequestEntityTooLarge, "Guest export is too large", "Narrow the export with search or status filters")
	}

	var body bytes.Buffer
	body.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(&body)
	exportView := strings.ToLower(strings.TrimSpace(c.QueryParam("view")))
	invitationView := exportView == "invitations"
	rsvpView := exportView == "rsvp"
	if rsvpView {
		_ = writer.Write([]string{"Nombre", "Email", "Teléfono", "Estado", "Canal", "+1s", "Respondió", "Agregado"})
	} else if invitationView {
		_ = writer.Write([]string{"Nombre", "Email", "Teléfono", "Estado RSVP", "Fecha respuesta", "Método", "Acompañantes"})
	} else {
		_ = writer.Write([]string{"Nombre", "Apellido", "Email", "Teléfono", "Mesa", "Asistentes", "Acompañantes", "Estado", "Restricciones", "Notas RSVP", "Notas internas"})
	}
	for _, guest := range list {
		partySize := guest.GuestsCount
		if strings.EqualFold(guestCSVStatus(guest), "DECLINED") {
			partySize = 0
		} else if partySize < 1 {
			partySize = 1
		}
		if rsvpView {
			respondedAt := ""
			if guest.RSVPAt != nil {
				respondedAt = guest.RSVPAt.UTC().Format(time.RFC3339)
			} else if !strings.EqualFold(guestCSVStatus(guest), "PENDING") && !guest.UpdatedAt.IsZero() {
				respondedAt = guest.UpdatedAt.UTC().Format(time.RFC3339)
			}
			_ = writer.Write([]string{
				strings.TrimSpace(guest.FirstName + " " + guest.LastName), guest.Email, guest.Phone, guestCSVStatus(guest),
				guest.RSVPMethod, strconv.Itoa(max(partySize-1, 0)), respondedAt, guest.CreatedAt.UTC().Format(time.RFC3339),
			})
		} else if invitationView {
			respondedAt := ""
			if guest.RSVPAt != nil {
				respondedAt = guest.RSVPAt.UTC().Format(time.RFC3339)
			}
			_ = writer.Write([]string{
				strings.TrimSpace(guest.FirstName + " " + guest.LastName), guest.Email, guest.Phone, guestCSVStatus(guest),
				respondedAt, guest.RSVPMethod, strconv.Itoa(max(partySize-1, 0)),
			})
		} else {
			_ = writer.Write([]string{
				guest.FirstName, guest.LastName, guest.Email, guest.Phone, guestCSVTable(guest), strconv.Itoa(partySize),
				strconv.Itoa(max(partySize-1, 0)), guestCSVStatus(guest), guest.DietaryRestrictions, guest.RSVPNotes, guest.Notes,
			})
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error encoding guest export", err.Error())
	}

	c.Response().Header().Set(echo.HeaderContentType, "text/csv; charset=utf-8")
	filename := "invitados.csv"
	if rsvpView {
		filename = "rsvp.csv"
	} else if invitationView {
		filename = "invitaciones.csv"
	}
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="`+filename+`"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", body.Bytes())
}

func GetCheckinWorkspace(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event UUID", err.Error())
	}
	_, event, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityCheckin)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}

	query := dtos.CheckinGuestsListQuery{Page: 1, PageSize: 60, Filter: "ALL", SkipTotal: true}
	var pageGuests []models.Guest
	var total int64
	var summary dtos.GuestSummary
	var statuses []models.GuestStatus
	type result struct{ err error }
	results := make(chan result, 3)
	go func() {
		var loadErr error
		pageGuests, total, loadErr = guestSvc.ListCheckinGuests(eventID, query)
		results <- result{loadErr}
	}()
	go func() {
		var loadErr error
		summary, loadErr = guestSvc.GetGuestSummaryByEventID(eventID)
		results <- result{loadErr}
	}()
	go func() {
		var loadErr error
		statuses, loadErr = guestService.ListGuestStatuss()
		results <- result{loadErr}
	}()
	for range 3 {
		if loadErr := (<-results).err; loadErr != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error loading check-in workspace", loadErr.Error())
		}
	}
	total = summary.Total

	page := dtos.CheckinGuestsPageResponse{
		Data: dtos.NewGuestResponses(pageGuests), Total: total, Page: 1, PageSize: 60,
		TotalPages: int((total + 59) / 60), Summary: &summary,
	}
	return utils.Success(c, http.StatusOK, "Check-in workspace loaded", dtos.CheckinWorkspaceResponse{
		Event: dtos.NewEventResponse(event), Statuses: dtos.NewGuestStatusResponses(statuses), Guests: page,
	})
}

var (
	guestSvc             *guestService.GuestService
	eventSectionSvc      *eventsService.EventSectionService
	guestConfigRepo      ports.EventConfigRepository
	guestAccessTokenRepo ports.AccessTokenRepository
	guestInvitationRepo  ports.InvitationRepository
	guestEventRepo       ports.EventsRepository
	guestResourceSvc     *resourcesService.ResourceService
)

type PublicGuestAccessDeps struct {
	ConfigRepo     ports.EventConfigRepository
	TokenRepo      ports.AccessTokenRepository
	InvitationRepo ports.InvitationRepository
	EventRepo      ports.EventsRepository
	ResourceSvc    *resourcesService.ResourceService
}

func InitGuestsController(svc *guestService.GuestService, sectionSvc *eventsService.EventSectionService, deps ...PublicGuestAccessDeps) {
	guestSvc = svc
	eventSectionSvc = sectionSvc
	guestConfigRepo = nil
	guestAccessTokenRepo = nil
	guestInvitationRepo = nil
	guestEventRepo = nil
	guestResourceSvc = nil
	if len(deps) == 0 {
		return
	}
	guestConfigRepo = deps[0].ConfigRepo
	guestAccessTokenRepo = deps[0].TokenRepo
	guestInvitationRepo = deps[0].InvitationRepo
	guestEventRepo = deps[0].EventRepo
	guestResourceSvc = deps[0].ResourceSvc
}

// GET /guests/:key
func GetGuests(c echo.Context) error {
	keyParam := c.Param("key")
	if guestSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Guest service unavailable", "")
	}

	if strings.HasPrefix(keyParam, "summary:") {
		eventIDStr := strings.TrimPrefix(keyParam, "summary:")
		eventID, err := uuid.FromString(eventIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid event UUID", err.Error())
		}
		if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityAnalyticsView); authErr != nil {
			return authz.Respond(c, authErr)
		}
		summary, err := guestSvc.GetGuestSummaryByEventID(eventID)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error loading guest summary", err.Error())
		}
		return utils.Success(c, http.StatusOK, "Guest summary loaded", summary)
	}

	if strings.HasPrefix(keyParam, "all:") {
		eventIDStr := strings.TrimPrefix(keyParam, "all:")
		eventID, err := uuid.FromString(eventIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid event UUID", err.Error())
		}
		if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityGuestManage); authErr != nil {
			return authz.Respond(c, authErr)
		}
		guests, err := guestSvc.ListGuestsByEventID(eventID)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error loading guests", err.Error())
		}
		return utils.Success(c, http.StatusOK, "Guests loaded", dtos.NewGuestResponses(guests))
	}

	if strings.HasPrefix(keyParam, "checkin:") || strings.HasPrefix(keyParam, "page:") || strings.HasPrefix(keyParam, "invitations:") {
		includeSummary := strings.HasPrefix(keyParam, "checkin:")
		includeShareSummary := strings.HasPrefix(keyParam, "invitations:")
		eventIDStr := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(keyParam, "checkin:"), "page:"), "invitations:")
		eventID, err := uuid.FromString(eventIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid event UUID", err.Error())
		}
		capability := authz.CapabilityGuestManage
		if strings.HasPrefix(keyParam, "checkin:") {
			capability = authz.CapabilityCheckin
		}
		if _, _, authErr := authz.RequireEventCapability(c, eventID, capability); authErr != nil {
			return authz.Respond(c, authErr)
		}
		page, err := strconv.Atoi(c.QueryParam("page"))
		if err != nil || page < 1 {
			page = 1
		}
		pageSize, err := strconv.Atoi(c.QueryParam("page_size"))
		if err != nil || pageSize < 1 || pageSize > 100 {
			return utils.Error(c, http.StatusBadRequest, "Invalid page_size", "page_size must be between 1 and 100")
		}
		query := dtos.CheckinGuestsListQuery{
			Page: page, PageSize: pageSize, Search: c.QueryParam("search"), Filter: c.QueryParam("filter"), QR: c.QueryParam("qr"),
			Sort: c.QueryParam("sort"), Direction: c.QueryParam("direction"),
		}
		var guests []models.Guest
		var total int64
		var summary *dtos.GuestSummary
		var shareSummary *dtos.GuestShareSummary
		if includeSummary || includeShareSummary {
			type pageResult struct {
				guests []models.Guest
				total  int64
				err    error
			}
			type summaryResult struct {
				summary dtos.GuestSummary
				err     error
			}
			type shareSummaryResult struct {
				summary dtos.GuestShareSummary
				err     error
			}
			pageResults := make(chan pageResult, 1)
			summaryResults := make(chan summaryResult, 1)
			shareSummaryResults := make(chan shareSummaryResult, 1)
			go func() {
				list, count, listErr := guestSvc.ListCheckinGuests(eventID, query)
				pageResults <- pageResult{guests: list, total: count, err: listErr}
			}()
			if includeSummary {
				go func() {
					loadedSummary, summaryErr := guestSvc.GetGuestSummaryByEventID(eventID)
					summaryResults <- summaryResult{summary: loadedSummary, err: summaryErr}
				}()
			}
			if includeShareSummary {
				go func() {
					loadedSummary, summaryErr := guestSvc.GetGuestShareSummaryByEventID(eventID)
					shareSummaryResults <- shareSummaryResult{summary: loadedSummary, err: summaryErr}
				}()
			}

			page := <-pageResults
			if page.err != nil {
				return utils.Error(c, http.StatusInternalServerError, "Error loading check-in guests", page.err.Error())
			}
			if includeSummary {
				loadedSummary := <-summaryResults
				if loadedSummary.err != nil {
					return utils.Error(c, http.StatusInternalServerError, "Error loading check-in summary", loadedSummary.err.Error())
				}
				summary = &loadedSummary.summary
			}
			if includeShareSummary {
				loadedSummary := <-shareSummaryResults
				if loadedSummary.err != nil {
					return utils.Error(c, http.StatusInternalServerError, "Error loading guest share summary", loadedSummary.err.Error())
				}
				shareSummary = &loadedSummary.summary
			}
			guests, total = page.guests, page.total
		} else {
			var listErr error
			guests, total, listErr = guestSvc.ListCheckinGuests(eventID, query)
			if listErr != nil {
				return utils.Error(c, http.StatusInternalServerError, "Error loading guests", listErr.Error())
			}
		}
		totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
		return utils.Success(c, http.StatusOK, "Check-in guests loaded", dtos.CheckinGuestsPageResponse{
			Data: dtos.NewGuestResponses(guests), Total: total, Page: page, PageSize: pageSize, TotalPages: totalPages,
			Summary: summary, ShareSummary: shareSummary,
		})
	}

	if strings.HasPrefix(keyParam, "analytics:") {
		eventIDStr := strings.TrimPrefix(keyParam, "analytics:")
		eventID, err := uuid.FromString(eventIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid event UUID", err.Error())
		}
		if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityGuestManage); authErr != nil {
			return authz.Respond(c, authErr)
		}
		guests, err := guestSvc.ListAnalyticsGuestsByEventID(eventID)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error loading analytics guests", err.Error())
		}
		return utils.Success(c, http.StatusOK, "Analytics guests loaded", guests)
	}

	if strings.HasPrefix(keyParam, "seating:") {
		eventIDStr := strings.TrimPrefix(keyParam, "seating:")
		eventID, err := uuid.FromString(eventIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid event UUID", err.Error())
		}
		if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityGuestManage); authErr != nil {
			return authz.Respond(c, authErr)
		}
		guests, err := guestSvc.ListSeatingGuestsByEventID(eventID)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error loading seating guests", err.Error())
		}
		return utils.Success(c, http.StatusOK, "Seating guests loaded", guests)
	}

	if strings.HasPrefix(keyParam, "share:") {
		eventIDStr := strings.TrimPrefix(keyParam, "share:")
		eventID, err := uuid.FromString(eventIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid event UUID", err.Error())
		}
		if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityGuestManage); authErr != nil {
			return authz.Respond(c, authErr)
		}
		summary, err := guestSvc.GetGuestShareSummaryByEventID(eventID)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error loading guest share summary", err.Error())
		}
		return utils.Success(c, http.StatusOK, "Guest share summary loaded", summary)
	}

	guestID, err := uuid.FromString(keyParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid guest UUID", err.Error())
	}
	_, guest, authErr := authz.RequireGuestCapability(c, guestID, authz.CapabilityGuestManage)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}

	return utils.Success(c, http.StatusOK, "Guest loaded", dtos.NewGuestResponses([]models.Guest{*guest}))
}

// POST /guests/:id/rsvp-token
// Creates or repairs the personal RSVP token for a legacy guest.
func EnsureRSVPToken(c echo.Context) error {
	guestID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid guest UUID", err.Error())
	}
	if guestSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Guest service unavailable", "")
	}
	if _, _, authErr := authz.RequireGuestCapability(c, guestID, authz.CapabilityGuestManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	guest, err := guestSvc.EnsureRSVPToken(guestID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error generating RSVP token", err.Error())
	}

	return utils.Success(c, http.StatusOK, "RSVP token ready", dtos.NewGuestResponse(guest))
}

// POST /guests
func CreateGuest(c echo.Context) error {
	rawBody := readJSONRequestBody(c)
	requestFields := decodeJSONRequestFields(rawBody)

	var guest models.Guest
	if err := c.Bind(&guest); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	applyGuestJSONAliases(&guest, requestFields)
	if err := c.Validate(&guest); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}
	if guest.EventID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "event_id required", "Guest must belong to an event")
	}
	if guest.GuestsCount > 0 {
		guest.RSVPGuestCount = guest.GuestsCount
	}
	if _, _, authErr := authz.RequireEventCapability(c, guest.EventID, authz.CapabilityGuestManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	guests := []models.Guest{guest}
	if err := guestSvc.CreateGuestsWithInvitations(guests); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating guest", err.Error())
	}
	return utils.Success(c, http.StatusCreated, "Guest and invitation created", dtos.NewGuestResponse(&guests[0]))
}

func CreateGuests(c echo.Context) error {
	rawBody := readJSONRequestBody(c)
	requestItems := decodeJSONRequestFieldList(rawBody)

	var guests []models.Guest
	if err := c.Bind(&guests); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	for i := range guests {
		if i < len(requestItems) {
			applyGuestJSONAliases(&guests[i], requestItems[i])
		}
	}

	if len(guests) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No guests provided", "")
	}

	eventID := guests[0].EventID
	if eventID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "event_id required", "Guests must belong to an event")
	}
	for i := range guests {
		if guests[i].EventID == uuid.Nil {
			return utils.Error(c, http.StatusBadRequest, "event_id required", "Guests must belong to an event")
		}
		if guests[i].EventID != eventID {
			return utils.Error(c, http.StatusBadRequest, "Mixed events not supported", "Batch guests must belong to a single event")
		}
	}
	if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityGuestManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	if err := guestSvc.CreateGuestsWithInvitations(guests); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating guests with invitations", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Guests and invitations created", dtos.NewGuestResponses(guests))
}

// PUT /guests/:id
func UpdateGuest(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	_, guest, authErr := authz.RequireGuestCapability(c, id, authz.CapabilityGuestManage)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}
	rawBody := readJSONRequestBody(c)
	requestFields := decodeJSONRequestFields(rawBody)
	originalEventID := guest.EventID
	if err := c.Bind(guest); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	applyGuestJSONAliases(guest, requestFields)
	guest.ID = id
	guest.EventID = originalEventID
	if guestCountFieldPresent(requestFields) {
		if guest.GuestsCount > 0 {
			guest.RSVPGuestCount = guest.GuestsCount
		} else {
			guest.RSVPGuestCount = 0
		}
	}
	if err := c.Validate(guest); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}
	if err := guestSvc.UpdateGuest(guest); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating guest", err.Error())
	}

	updatedGuest := guest
	if reloadedGuest, err := guestSvc.GetGuestByID(id); err == nil && reloadedGuest != nil {
		updatedGuest = reloadedGuest
	}

	return utils.Success(c, http.StatusOK, "Guest updated", dtos.NewGuestResponse(updatedGuest))
}

type jsonRequestFields map[string]json.RawMessage

func readJSONRequestBody(c echo.Context) []byte {
	body := c.Request().Body
	if body == nil {
		return nil
	}

	raw, err := io.ReadAll(body)
	if err != nil {
		c.Request().Body = io.NopCloser(bytes.NewReader(nil))
		return nil
	}
	c.Request().Body = io.NopCloser(bytes.NewReader(raw))
	return raw
}

func decodeJSONRequestFields(raw []byte) jsonRequestFields {
	fields := make(jsonRequestFields)
	if len(raw) == 0 {
		return fields
	}

	var payload jsonRequestFields
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fields
	}
	return payload
}

func decodeJSONRequestFieldList(raw []byte) []jsonRequestFields {
	if len(raw) == 0 {
		return nil
	}

	var items []jsonRequestFields
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	return items
}

func guestCountFieldPresent(fields jsonRequestFields) bool {
	return rawJSONValue(fields, "guests_count", "guest_count", "guestCount", "GuestCount", "guestsCount", "GuestsCount") != nil
}

func applyGuestJSONAliases(guest *models.Guest, fields jsonRequestFields) {
	if guest == nil || len(fields) == 0 {
		return
	}

	setString(fields, []string{"firstName", "FirstName"}, func(value string) { guest.FirstName = value })
	setString(fields, []string{"lastName", "LastName"}, func(value string) { guest.LastName = value })
	setString(fields, []string{"nickname", "Nickname"}, func(value string) { guest.Nickname = value })
	setString(fields, []string{"role", "Role"}, func(value string) { guest.Role = value })
	setString(fields, []string{"headline", "Headline"}, func(value string) { guest.Headline = value })
	setString(fields, []string{"bio", "Bio"}, func(value string) { guest.Bio = value })
	setString(fields, []string{"signature", "Signature"}, func(value string) { guest.Signature = value })
	setString(fields, []string{"notes", "Notes"}, func(value string) { guest.Notes = value })
	setString(fields, []string{"rsvpNotes", "RSVPNotes", "RsvpNotes"}, func(value string) { guest.RSVPNotes = value })
	setString(fields, []string{"showContactInfo", "ShowContactInfo"}, func(value string) { guest.ShowContactInfo = parseBoolString(value) })
	setBool(fields, []string{"showContactInfo", "ShowContactInfo"}, func(value bool) { guest.ShowContactInfo = value })
	setString(fields, []string{"tableNumber", "TableNumber"}, func(value string) { guest.TableNumber = value })
	setString(fields, []string{"imageUrl", "imageURL", "ImageURL", "ImageUrl"}, func(value string) { guest.ImageURL = value })
	setString(fields, []string{"image1Url", "image1URL", "image_1_url", "Image1URL", "Image1Url"}, func(value string) { guest.Image1URL = value })
	setString(fields, []string{"image2Url", "image2URL", "image_2_url", "Image2URL", "Image2Url"}, func(value string) { guest.Image2URL = value })
	setString(fields, []string{"image3Url", "image3URL", "image_3_url", "Image3URL", "Image3Url"}, func(value string) { guest.Image3URL = value })
	setString(fields, []string{"dietaryRestrictions", "DietaryRestrictions"}, func(value string) { guest.DietaryRestrictions = value })
	setString(fields, []string{"rsvpStatus", "RSVPStatus", "RsvpStatus"}, func(value string) { guest.RSVPStatus = value })
	setString(fields, []string{"rsvpMethod", "RSVPMethod", "RsvpMethod"}, func(value string) { guest.RSVPMethod = value })
	setString(fields, []string{"isHost", "IsHost"}, func(value string) { guest.IsHost = parseBoolString(value) })
	setBool(fields, []string{"isHost", "IsHost"}, func(value bool) { guest.IsHost = value })

	setInt(fields, []string{"guest_count", "guestCount", "GuestCount", "guestsCount", "GuestsCount"}, func(value int) { guest.GuestsCount = value })
	setInt(fields, []string{"maxGuests", "MaxGuests"}, func(value int) { guest.MaxGuests = value })
	setInt(fields, []string{"rsvpGuestCount", "RSVPGuestCount", "RsvpGuestCount"}, func(value int) { guest.RSVPGuestCount = value })
	setInt(fields, []string{"public_order", "publicOrder", "PublicOrder", "display_order", "displayOrder", "DisplayOrder", "sort_order", "sortOrder", "SortOrder", "Order"}, func(value int) { guest.Order = value })

	setUUID(fields, []string{"eventId", "eventID", "EventID", "EventId"}, func(value uuid.UUID) { guest.EventID = value })
	setUUID(fields, []string{"statusId", "statusID", "StatusID", "StatusId"}, func(value uuid.UUID) {
		guest.StatusID = value
		guest.GuestStatusID = value
	})
	setUUID(fields, []string{"guestStatusId", "guestStatusID", "GuestStatusID", "GuestStatusId"}, func(value uuid.UUID) {
		guest.StatusID = value
		guest.GuestStatusID = value
	})
	setUUID(fields, []string{"rsvpTokenId", "rsvpTokenID", "RSVPTokenID", "RsvpTokenId"}, func(value uuid.UUID) { guest.RSVPTokenID = &value })
	setUUIDPtr(fields, []string{"tableId", "tableID", "TableID", "TableId"}, func(value *uuid.UUID) { guest.TableID = value })
	setUUIDPtr(fields, []string{"invitationId", "invitationID", "InvitationID", "InvitationId"}, func(value *uuid.UUID) { guest.InvitationID = value })
	setTimePtr(fields, []string{"rsvpAt", "RSVPAt", "RsvpAt"}, func(value *time.Time) { guest.RSVPAt = value })
}

func rawJSONValue(fields jsonRequestFields, keys ...string) json.RawMessage {
	for _, key := range keys {
		if raw, ok := fields[key]; ok {
			return raw
		}
	}
	return nil
}

func setString(fields jsonRequestFields, keys []string, assign func(string)) {
	raw := rawJSONValue(fields, keys...)
	if raw == nil {
		return
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		assign(value)
	}
}

func setBool(fields jsonRequestFields, keys []string, assign func(bool)) {
	raw := rawJSONValue(fields, keys...)
	if raw == nil {
		return
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err == nil {
		assign(value)
	}
}

func setInt(fields jsonRequestFields, keys []string, assign func(int)) {
	raw := rawJSONValue(fields, keys...)
	if raw == nil {
		return
	}
	var value int
	if err := json.Unmarshal(raw, &value); err == nil {
		assign(value)
		return
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if number, err := strconv.Atoi(text); err == nil {
		assign(number)
	}
}

func setUUID(fields jsonRequestFields, keys []string, assign func(uuid.UUID)) {
	raw := rawJSONValue(fields, keys...)
	if raw == nil {
		return
	}
	id, ok := parseJSONUUID(raw)
	if ok {
		assign(id)
	}
}

func setUUIDPtr(fields jsonRequestFields, keys []string, assign func(*uuid.UUID)) {
	raw := rawJSONValue(fields, keys...)
	if raw == nil {
		return
	}
	if string(raw) == "null" {
		assign(nil)
		return
	}
	id, ok := parseJSONUUID(raw)
	if !ok {
		return
	}
	if id == uuid.Nil {
		assign(nil)
		return
	}
	assign(&id)
}

func setTimePtr(fields jsonRequestFields, keys []string, assign func(*time.Time)) {
	raw := rawJSONValue(fields, keys...)
	if raw == nil {
		return
	}
	if string(raw) == "null" {
		assign(nil)
		return
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		assign(nil)
		return
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return
	}
	assign(&parsed)
}

func parseJSONUUID(raw json.RawMessage) (uuid.UUID, bool) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return uuid.Nil, false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return uuid.Nil, true
	}
	id, err := uuid.FromString(text)
	return id, err == nil
}

func parseBoolString(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on", "si":
		return true
	default:
		return false
	}
}

// DELETE /guests/bulk — body: {"ids": ["uuid1", "uuid2", ...]}
func BulkDeleteGuests(c echo.Context) error {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if len(body.IDs) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No IDs provided", "")
	}
	uuids := make([]uuid.UUID, 0, len(body.IDs))
	for _, id := range body.IDs {
		u, err := uuid.FromString(id)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid UUID: "+id, err.Error())
		}
		uuids = append(uuids, u)
	}
	for _, id := range uuids {
		if _, _, authErr := authz.RequireGuestCapability(c, id, authz.CapabilityGuestManage); authErr != nil {
			return authz.Respond(c, authErr)
		}
	}

	if err := guestSvc.BulkDeleteGuests(uuids); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting guests", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Guests deleted", nil)
}

func BulkUpdateGuestStatus(c echo.Context) error {
	var body struct {
		EventID       string   `json:"event_id"`
		IDs           []string `json:"ids"`
		StatusID      string   `json:"status_id"`
		GuestStatusID string   `json:"guest_status_id"`
		RSVPStatus    string   `json:"rsvp_status"`
		RSVPMethod    string   `json:"rsvp_method"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	eventID, err := uuid.FromString(strings.TrimSpace(body.EventID))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event_id", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityCheckin); authErr != nil {
		return authz.Respond(c, authErr)
	}
	if len(body.IDs) == 0 || len(body.IDs) > 100 {
		return utils.Error(c, http.StatusBadRequest, "Invalid IDs", "ids must contain between 1 and 100 guests")
	}
	statusIDRaw := strings.TrimSpace(body.StatusID)
	if statusIDRaw == "" {
		statusIDRaw = strings.TrimSpace(body.GuestStatusID)
	}
	statusID, err := uuid.FromString(statusIDRaw)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid status_id", err.Error())
	}
	rsvpStatus := strings.ToLower(strings.TrimSpace(body.RSVPStatus))
	if rsvpStatus != "pending" && rsvpStatus != "confirmed" && rsvpStatus != "declined" {
		return utils.Error(c, http.StatusBadRequest, "Invalid rsvp_status", "expected pending, confirmed, or declined")
	}
	rsvpMethod := strings.ToLower(strings.TrimSpace(body.RSVPMethod))
	if rsvpMethod == "" {
		rsvpMethod = "host"
	}
	if rsvpMethod != "host" {
		return utils.Error(c, http.StatusBadRequest, "Invalid rsvp_method", "bulk dashboard updates must use host")
	}
	ids := make([]uuid.UUID, 0, len(body.IDs))
	seen := make(map[uuid.UUID]struct{}, len(body.IDs))
	for _, rawID := range body.IDs {
		id, err := uuid.FromString(strings.TrimSpace(rawID))
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid guest id", err.Error())
		}
		if _, duplicate := seen[id]; duplicate {
			return utils.Error(c, http.StatusBadRequest, "Duplicate guest id", id.String())
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if err := guestSvc.BulkUpdateGuestStatus(eventID, ids, statusID, rsvpStatus, rsvpMethod); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Error updating guest statuses", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Guest statuses updated", nil)
}

// DELETE /guests/:id
func DeleteGuest(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if _, _, authErr := authz.RequireGuestCapability(c, id, authz.CapabilityGuestManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	if err := guestSvc.DeleteGuest(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting guest", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Guest deleted", nil)
}

// attendeeDeps allows injecting repository calls for testing.
type attendeeDeps struct {
	getSection     func(id uuid.UUID) (*models.EventSection, error)
	getAttendees   func(eventID uuid.UUID) ([]models.Guest, error)
	allowAccess    func(c echo.Context, eventID uuid.UUID) (bool, error)
	sectionVisible func(section *models.EventSection) (bool, error)
	imageViewURL   func(path string) (string, *time.Time)
}

func handleGetAttendees(deps attendeeDeps, c echo.Context) error {
	sectionID, err := uuid.FromString(c.Param("sectionId"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid section UUID", err.Error())
	}
	section, err := deps.getSection(sectionID)
	if err != nil {
		if errors.Is(err, errAttendeeServiceUnavailable) {
			return utils.Error(c, http.StatusInternalServerError, "Service unavailable", "")
		}
		return utils.Error(c, http.StatusNotFound, "Section not found", err.Error())
	}
	if deps.allowAccess != nil {
		allowed, err := deps.allowAccess(c, section.EventID)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error loading event access", err.Error())
		}
		if !allowed {
			return utils.Error(c, http.StatusForbidden, "Event is not public", "")
		}
	}
	if !section.IsVisible {
		return utils.Error(c, http.StatusForbidden, "Section is not public", "")
	}
	if deps.sectionVisible != nil {
		visible, err := deps.sectionVisible(section)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error loading event config", err.Error())
		}
		if !visible {
			return utils.Error(c, http.StatusForbidden, "Section is not public", "")
		}
	}
	attendees, err := deps.getAttendees(section.EventID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching attendees", err.Error())
	}
	attendees = publicAttendeesForSection(section, attendees)
	return utils.Success(c, http.StatusOK, "Attendees loaded", newPublicAttendeeResponses(attendees, deps.imageViewURL))
}

func newPublicAttendeeResponses(attendees []models.Guest, imageViewURL func(path string) (string, *time.Time)) []dtos.PublicAttendee {
	items := dtos.NewPublicAttendees(attendees)
	if imageViewURL == nil {
		return items
	}

	for i := range items {
		rawImageURL := strings.TrimSpace(items[i].ImageURL)
		if rawImageURL == "" {
			continue
		}
		viewURL, expiresAt := imageViewURL(rawImageURL)
		if strings.TrimSpace(viewURL) == "" {
			continue
		}
		items[i].ImageViewURL = viewURL
		items[i].ImageViewURLExpiresAt = expiresAt
	}
	return items
}

func publicAttendeeImageViewURL(path string) (string, *time.Time) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || utils.IsAbsoluteURLLike(trimmed) {
		return path, nil
	}
	if guestResourceSvc == nil {
		return path, nil
	}

	viewURL, err := guestResourceSvc.GetPresignedURLWithTTL(trimmed, resourcesService.ResourceViewURLTTLMinutes)
	if err != nil || strings.TrimSpace(viewURL) == "" {
		return path, nil
	}
	expiresAt := time.Now().UTC().Add(time.Duration(resourcesService.ResourceViewURLTTLMinutes) * time.Minute)
	return viewURL, &expiresAt
}

func publicAttendeesForSection(section *models.EventSection, attendees []models.Guest) []models.Guest {
	if isHostAttendeeSection(section) {
		return filterPublicAttendees(attendees, func(attendee models.Guest) bool {
			return attendee.IsHost || isHostRole(attendee.Role)
		})
	}

	if isGraduateAttendeeSection(section) {
		filtered := filterPublicAttendees(attendees, func(attendee models.Guest) bool {
			return isGraduateRole(attendee.Role)
		})
		if len(filtered) > 0 {
			return filtered
		}
	}

	return attendees
}

func filterPublicAttendees(attendees []models.Guest, keep func(models.Guest) bool) []models.Guest {
	filtered := make([]models.Guest, 0, len(attendees))
	for _, attendee := range attendees {
		if keep(attendee) {
			filtered = append(filtered, attendee)
		}
	}
	return filtered
}

func isHostAttendeeSection(section *models.EventSection) bool {
	if section == nil {
		return false
	}
	return isHostSectionType(section.ComponentType) || isHostSectionType(section.Key)
}

func isGraduateAttendeeSection(section *models.EventSection) bool {
	if section == nil {
		return false
	}
	return isGraduateSectionType(section.ComponentType) || isGraduateSectionType(section.Key)
}

func isHostSectionType(value string) bool {
	switch guestService.NormalizePublicGuestRole(value) {
	case "host", "hosts", "hostsection", "hostssection":
		return true
	default:
		return false
	}
}

func isGraduateSectionType(value string) bool {
	switch guestService.NormalizePublicGuestRole(value) {
	case "graduateslist":
		return true
	default:
		return false
	}
}

func isHostRole(role string) bool {
	return guestService.IsPublicHostRole(role)
}

func isGraduateRole(role string) bool {
	return guestService.IsPublicGraduateRole(role)
}

// GET /events/section/:sectionId/attendees
// Public: returns guests for the event linked to the given section, ordered by display order.
func GetAttendees(c echo.Context) error {
	var accessConfig *models.EventConfig
	return handleGetAttendees(attendeeDeps{
		getSection: func(id uuid.UUID) (*models.EventSection, error) {
			if eventSectionSvc == nil {
				return nil, errAttendeeServiceUnavailable
			}
			return eventSectionSvc.GetEventSectionByID(id)
		},
		getAttendees: func(eventID uuid.UUID) ([]models.Guest, error) {
			if guestSvc == nil {
				return nil, errAttendeeServiceUnavailable
			}
			return guestSvc.ListAttendeesByEventID(eventID)
		},
		allowAccess: func(c echo.Context, eventID uuid.UUID) (bool, error) {
			result, err := publicaccess.AllowEventReadFromRequestWithConfig(c, eventID, guestPublicEventReadDeps())
			accessConfig = result.Config
			return result.Allowed, err
		},
		sectionVisible: func(section *models.EventSection) (bool, error) {
			return eventsService.PageSpecSectionVisible(section.ComponentType, accessConfig), nil
		},
		imageViewURL: publicAttendeeImageViewURL,
	}, c)
}

func guestPublicEventReadDeps() publicaccess.EventReadDeps {
	return publicaccess.EventReadDeps{
		ConfigRepo:           guestConfigRepo,
		TokenRepo:            guestAccessTokenRepo,
		InvitationRepo:       guestInvitationRepo,
		IsEventActive:        guestEventActive,
		RequirePasswordProof: true,
	}
}

func guestEventActive(eventID uuid.UUID) (bool, error) {
	if guestEventRepo == nil {
		return true, nil
	}
	event, err := guestEventRepo.GetEventByIDRaw(eventID)
	if err != nil || event == nil {
		return false, err
	}
	return event.IsActive, nil
}
