package fonts

import (
	"events-stocks/models"
	fontsSvc "events-stocks/services/fonts"
	services "events-stocks/services/resources"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
	"net/http"
)

var fontSvc *fontsSvc.FontService

func InitFontsController(cfg *models.Config) {
	resource := services.NewResourceService(cfg)
	fontSvc = fontsSvc.NewFontService(resource)
}

func UploadFonts(c echo.Context) error {
	form, err := c.MultipartForm()
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid form", err.Error())
	}

	files := form.File["files"]
	if len(files) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No files provided", "")
	}

	fonts, err := fontSvc.UploadAndCreateFonts(files)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Upload failed", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Fonts uploaded", fonts)
}
