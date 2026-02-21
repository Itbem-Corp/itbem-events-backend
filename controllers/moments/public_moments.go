package moments

import (
	"errors"
	"net/http"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/services/ports"
	resourcesService "events-stocks/services/resources"
	momentsService "events-stocks/services/moments"
	"events-stocks/utils"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

var (
	publicTokenRepo ports.AccessTokenRepository
	publicResSvc    *resourcesService.ResourceService
)

func InitPublicMomentsController(
	tokenRepo ports.AccessTokenRepository,
	resSvc *resourcesService.ResourceService,
) {
	publicTokenRepo = tokenRepo
	publicResSvc = resSvc
}

func getEventByIdentifier(identifier string) (*models.Event, error) {
	var event models.Event
	err := configuration.DB.Where("identifier = ?", identifier).First(&event).Error
	if err != nil {
		return nil, err
	}
	return &event, nil
}

// GET /api/events/:identifier/moments
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

	list, err := momentsService.ListMomentsByEventID(event.ID, true)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading moments", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moments loaded", list)
}

// POST /api/events/:identifier/moments
func CreatePublicMoment(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
	}

	prettyToken := c.FormValue("pretty_token")
	if prettyToken == "" {
		return utils.Error(c, http.StatusUnauthorized, "Missing invitation token", "")
	}

	token, err := publicTokenRepo.GetByPrettyToken(prettyToken)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusUnauthorized, "Invalid invitation token", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error validating token", err.Error())
	}

	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}

	file, header, err := c.Request().FormFile("file")
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Missing file", err.Error())
	}
	defer file.Close()

	contentPath, err := publicResSvc.UploadToMomentsFolder(file, header)
	if err != nil {
		return utils.Error(c, http.StatusUnprocessableEntity, "Error uploading file", err.Error())
	}

	description := c.FormValue("description")
	eventID := event.ID
	moment := models.Moment{
		EventID:      &eventID,
		InvitationID: token.InvitationID,
		ContentURL:   contentPath,
		Description:  description,
		IsApproved:   false,
	}

	if err := momentsService.CreateMoment(&moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error saving moment", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Moment submitted for review", moment)
}
