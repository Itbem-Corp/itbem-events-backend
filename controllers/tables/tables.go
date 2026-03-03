package tables

import (
	"events-stocks/dtos"
	"events-stocks/models"
	tablesService "events-stocks/services/tables"
	"events-stocks/utils"
	"net/http"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

var tableSvc *tablesService.TableService

func InitTablesController(svc *tablesService.TableService) { tableSvc = svc }

// GET /events/:id/tables
func ListTables(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}
	tables, err := tableSvc.ListByEventID(eventID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error listing tables", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Tables loaded", tables)
}

// POST /events/:id/tables
func CreateTable(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}
	var table models.Table
	if err := c.Bind(&table); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if table.Name == "" {
		return utils.Error(c, http.StatusBadRequest, "Table name is required", "")
	}
	if table.Capacity <= 0 {
		table.Capacity = 10
	}
	if err := tableSvc.Create(eventID, &table); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating table", err.Error())
	}
	return utils.Success(c, http.StatusCreated, "Table created", table)
}

// PUT /tables/:id
func UpdateTable(c echo.Context) error {
	tableID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid table ID", err.Error())
	}
	var table models.Table
	if err := c.Bind(&table); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	table.ID = tableID
	if err := tableSvc.Update(&table); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating table", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Table updated", table)
}

// DELETE /tables/:id
func DeleteTable(c echo.Context) error {
	tableID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid table ID", err.Error())
	}
	if err := tableSvc.Delete(tableID); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting table", err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// PUT /events/:id/tables/assign
func BatchAssign(c echo.Context) error {
	var req dtos.BatchAssignRequest
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if len(req.Assignments) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No assignments provided", "")
	}
	if err := tableSvc.BatchAssign(req.Assignments); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error assigning guests", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Guests assigned", nil)
}
