package fonts

import (
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	fontsSvc "events-stocks/services/fonts"
	resourcesSvc "events-stocks/services/resources"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
	"net/http"
)

var fontSvc *fontsSvc.FontService

func InitFontsController(service *fontsSvc.FontService) {
	fontSvc = service
}

func UploadFonts(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}

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
		status, detail := resourcesSvc.UploadErrorResponse(err)
		return utils.Error(c, status, "Upload failed", detail)
	}

	return utils.Success(c, http.StatusCreated, "Fonts uploaded", dtos.NewFontResponses(fonts))
}
