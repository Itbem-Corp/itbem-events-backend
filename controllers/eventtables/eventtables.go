package eventtables

import (
	"net/http"
	"strings"
	"sync"

	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	guestsService "events-stocks/services/guests"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

var (
	tableSvc        *eventsService.EventTableService
	seatingGuestSvc *guestsService.GuestService
)

func InitEventTablesController(svc *eventsService.EventTableService, guestServices ...*guestsService.GuestService) {
	tableSvc = svc
	seatingGuestSvc = nil
	if len(guestServices) > 0 {
		seatingGuestSvc = guestServices[0]
	}
}

func GetSeatingWorkspace(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if _, _, authErr := authz.RequireEventAccess(c, eventID); authErr != nil {
		return authz.Respond(c, authErr)
	}
	if tableSvc == nil || seatingGuestSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Seating workspace unavailable", "")
	}

	var tables []models.EventTable
	var guests []dtos.SeatingGuest
	var tablesErr, guestsErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		tables, tablesErr = tableSvc.ListByEventID(eventID)
	}()
	go func() {
		defer wait.Done()
		guests, guestsErr = seatingGuestSvc.ListSeatingGuestsByEventID(eventID)
	}()
	wait.Wait()
	if tablesErr != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading tables", tablesErr.Error())
	}
	if guestsErr != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading seating guests", guestsErr.Error())
	}
	return utils.Success(c, http.StatusOK, "Seating workspace loaded", dtos.SeatingWorkspaceResponse{
		Tables: dtos.NewEventTableResponses(tables),
		Guests: guests,
	})
}

func ListTables(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if _, _, authErr := authz.RequireEventAccess(c, eventID); authErr != nil {
		return authz.Respond(c, authErr)
	}
	tables, err := tableSvc.ListByEventID(eventID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading tables", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Tables loaded", dtos.NewEventTableResponses(tables))
}

func CreateTable(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityGuestManage); authErr != nil {
		return authz.Respond(c, authErr)
	}
	var body tableCreateRequest
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return utils.Error(c, http.StatusBadRequest, "name is required", "")
	}
	if body.Capacity <= 0 {
		return utils.Error(c, http.StatusBadRequest, "capacity must be greater than zero", "")
	}
	table := models.EventTable{
		EventID:   eventID,
		Name:      name,
		Capacity:  body.Capacity,
		SortOrder: body.sortOrder(),
	}
	if err := tableSvc.CreateTable(&table); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating table", err.Error())
	}
	return utils.Success(c, http.StatusCreated, "Table created", dtos.NewEventTableResponse(table))
}

func UpdateTable(c echo.Context) error {
	tableID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	table, err := tableSvc.GetTableByID(tableID)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Table not found", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, table.EventID, authz.CapabilityGuestManage); authErr != nil {
		return authz.Respond(c, authErr)
	}
	var body tableUpdateRequest
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			return utils.Error(c, http.StatusBadRequest, "name is required", "")
		}
		table.Name = name
	}
	if body.Capacity != nil {
		if *body.Capacity <= 0 {
			return utils.Error(c, http.StatusBadRequest, "capacity must be greater than zero", "")
		}
		table.Capacity = *body.Capacity
	}
	if sortOrder := body.sortOrder(); sortOrder != nil {
		table.SortOrder = *sortOrder
	}
	if err := tableSvc.UpdateTable(table); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating table", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Table updated", dtos.NewEventTableResponse(*table))
}

func DeleteTable(c echo.Context) error {
	tableID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	table, err := tableSvc.GetTableByID(tableID)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Table not found", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, table.EventID, authz.CapabilityGuestManage); authErr != nil {
		return authz.Respond(c, authErr)
	}
	if err := tableSvc.DeleteTable(table); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting table", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Table deleted", nil)
}

func AssignTables(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityGuestManage); authErr != nil {
		return authz.Respond(c, authErr)
	}
	var body struct {
		Assignments []tableAssignmentRequest `json:"assignments"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	assignments := make(map[uuid.UUID]*uuid.UUID, len(body.Assignments))
	for _, item := range body.Assignments {
		guestID, err := uuid.FromString(item.guestID())
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid guest_id", err.Error())
		}
		var tableID *uuid.UUID
		if rawTableID := item.tableID(); rawTableID != "" {
			parsed, err := uuid.FromString(rawTableID)
			if err != nil {
				return utils.Error(c, http.StatusBadRequest, "Invalid table_id", err.Error())
			}
			tableID = &parsed
		}
		assignments[guestID] = tableID
	}
	if err := tableSvc.AssignGuests(eventID, assignments); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Error assigning tables", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Tables assigned", nil)
}

func SavePlan(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityGuestManage); authErr != nil {
		return authz.Respond(c, authErr)
	}
	var body dtos.SeatingPlanSaveRequest
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	tables, err := tableSvc.SaveSeatingPlan(eventID, body)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Error saving seating plan", err.Error())
	}
	response := dtos.SeatingPlanSaveResponse{Tables: dtos.NewEventTableResponses(tables)}
	if seatingGuestSvc != nil {
		if guests, guestErr := seatingGuestSvc.ListSeatingGuestsByEventID(eventID); guestErr == nil {
			response.Guests = guests
		}
	}
	return utils.Success(c, http.StatusOK, "Seating plan saved", response)
}

type tableCreateRequest struct {
	Name         string `json:"name"`
	Capacity     int    `json:"capacity"`
	SortOrder    int    `json:"sort_order"`
	SortOrderAlt *int   `json:"sortOrder"`
}

func (r tableCreateRequest) sortOrder() int {
	if r.SortOrderAlt != nil {
		return *r.SortOrderAlt
	}
	return r.SortOrder
}

type tableUpdateRequest struct {
	Name         *string `json:"name"`
	Capacity     *int    `json:"capacity"`
	SortOrder    *int    `json:"sort_order"`
	SortOrderAlt *int    `json:"sortOrder"`
}

func (r tableUpdateRequest) sortOrder() *int {
	if r.SortOrderAlt != nil {
		return r.SortOrderAlt
	}
	return r.SortOrder
}

type tableAssignmentRequest struct {
	GuestID    string  `json:"guest_id"`
	GuestIDAlt string  `json:"guestId"`
	TableID    *string `json:"table_id"`
	TableIDAlt *string `json:"tableId"`
}

func (r tableAssignmentRequest) guestID() string {
	if strings.TrimSpace(r.GuestIDAlt) != "" {
		return strings.TrimSpace(r.GuestIDAlt)
	}
	return strings.TrimSpace(r.GuestID)
}

func (r tableAssignmentRequest) tableID() string {
	if r.TableIDAlt != nil {
		return strings.TrimSpace(*r.TableIDAlt)
	}
	if r.TableID != nil {
		return strings.TrimSpace(*r.TableID)
	}
	return ""
}
