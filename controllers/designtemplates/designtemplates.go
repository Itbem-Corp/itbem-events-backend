package designtemplates

import (
	"events-stocks/services/colors"
	"events-stocks/services/fonts"
	"events-stocks/services/templates"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
)

// GET /api/catalogs/design-templates
func ListDesignTemplates(c echo.Context) error {
	list, err := templates.ListDesignTemplates()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error listing design templates", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Design templates loaded", list)
}

// GET /api/catalogs/design-templates/:id
func GetDesignTemplate(c echo.Context) error {
	id, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	tmpl, err := templates.GetDesignTemplateByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Design template not found", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Design template loaded", tmpl)
}

// GET /api/catalogs/color-palettes
func ListColorPalettes(c echo.Context) error {
	list, err := colors.ListColorPalettes()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error listing color palettes", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Color palettes loaded", list)
}

// GET /api/catalogs/font-sets
func ListFontSets(c echo.Context) error {
	list, err := fonts.ListFontSets()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error listing font sets", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Font sets loaded", list)
}
