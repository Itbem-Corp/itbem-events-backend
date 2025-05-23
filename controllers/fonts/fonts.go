package fonts

import (
	"events-stocks/configuration"
	"events-stocks/services/fonts" // <-- adapta al import real si es diferente
	services "events-stocks/services/resources"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
	"net/http"
)

var (
	cfg      = configuration.LoadConfig()
	resource = services.NewResourceService(cfg)
	fontSvc  = fonts.NewFontService(resource)
)

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
