package moments

import (
	"events-stocks/models"
	momentsService "events-stocks/services/moments"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
)

var momentSvc *momentsService.MomentService

func InitMomentsController(svc *momentsService.MomentService) {
	momentSvc = svc
}

// GET /moments
func ListMoments(c echo.Context) error {
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
