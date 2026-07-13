package designtemplates

import (
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/colors"
	"events-stocks/services/fonts"
	resourcesService "events-stocks/services/resources"
	"events-stocks/services/templates"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"strings"
	"sync"
	"time"
)

var designTemplateResourceSvc *resourcesService.ResourceService

func InitDesignTemplatesController(resourceSvc *resourcesService.ResourceService) {
	designTemplateResourceSvc = resourceSvc
}

func isExternalDesignCatalogURL(value string) bool {
	return utils.IsAbsoluteURLLike(value)
}

func designCatalogViewURLWithExpiry(path string) (string, *time.Time) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || isExternalDesignCatalogURL(trimmed) || designTemplateResourceSvc == nil {
		return path, nil
	}

	viewURL, err := designTemplateResourceSvc.GetPresignedURLWithTTL(trimmed, resourcesService.ResourceViewURLTTLMinutes)
	if err != nil || viewURL == "" {
		return path, nil
	}

	expiresAt := time.Now().UTC().Add(time.Duration(resourcesService.ResourceViewURLTTLMinutes) * time.Minute)
	return viewURL, &expiresAt
}

func withDesignTemplatePreviewViewURL(response dtos.DesignTemplateResponse) dtos.DesignTemplateResponse {
	response.PreviewViewURL, response.PreviewViewURLExpiresAt = designCatalogViewURLWithExpiry(response.PreviewURL)
	return response
}

func withFontResponseViewURL(font *dtos.FontResponse) {
	if font == nil {
		return
	}
	font.ViewURL, font.ViewURLExpiresAt = designCatalogViewURLWithExpiry(font.URL)
}

func withFontSetFontViewURLs(fontSet *dtos.FontSetResponse) {
	if fontSet == nil {
		return
	}
	for i := range fontSet.Patterns {
		withFontResponseViewURL(fontSet.Patterns[i].Font)
	}
}

func fontSetResponseWithFontViewURLs(response dtos.FontSetResponse) dtos.FontSetResponse {
	withFontSetFontViewURLs(&response)
	return response
}

func fontSetResponsesWithFontViewURLs(fontSets []models.FontSet) []dtos.FontSetResponse {
	responses := dtos.NewFontSetResponses(fontSets)
	for i := range responses {
		responses[i] = fontSetResponseWithFontViewURLs(responses[i])
	}
	return responses
}

func designTemplateResponse(template *models.DesignTemplate) dtos.DesignTemplateResponse {
	response := withDesignTemplatePreviewViewURL(dtos.NewDesignTemplateResponse(template))
	withFontSetFontViewURLs(response.FontSet)
	withFontSetFontViewURLs(response.DefaultFontSet)
	return response
}

func designTemplateResponses(templates []models.DesignTemplate) []dtos.DesignTemplateResponse {
	responses := make([]dtos.DesignTemplateResponse, 0, len(templates))
	for i := range templates {
		responses = append(responses, designTemplateResponse(&templates[i]))
	}
	return responses
}

// GET /api/catalogs/design-templates
func ListDesignTemplates(c echo.Context) error {
	list, err := templates.ListDesignTemplates()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error listing design templates", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Design templates loaded", designTemplateResponses(list))
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
	return utils.Success(c, http.StatusOK, "Design template loaded", designTemplateResponse(tmpl))
}

// GET /api/catalogs/color-palettes
func ListColorPalettes(c echo.Context) error {
	list, err := colors.ListColorPalettes()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error listing color palettes", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Color palettes loaded", dtos.NewColorPaletteResponses(list))
}

// GET /api/catalogs/font-sets
func ListFontSets(c echo.Context) error {
	list, err := fonts.ListFontSets()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error listing font sets", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Font sets loaded", fontSetResponsesWithFontViewURLs(list))
}

// ListDesignCatalogWorkspace returns the three design catalogs required by
// the dashboard in one round trip. The underlying reads remain independent.
func ListDesignCatalogWorkspace(c echo.Context) error {
	var templateList []models.DesignTemplate
	var paletteList []models.ColorPalette
	var fontSetList []models.FontSet
	var templateErr, paletteErr, fontSetErr error

	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		templateList, templateErr = templates.ListDesignTemplates()
	}()
	go func() {
		defer wait.Done()
		paletteList, paletteErr = colors.ListColorPalettes()
	}()
	go func() {
		defer wait.Done()
		fontSetList, fontSetErr = fonts.ListFontSets()
	}()
	wait.Wait()

	if templateErr != nil || paletteErr != nil || fontSetErr != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading design catalogs", "One or more design catalogs could not be loaded")
	}
	return utils.Success(c, http.StatusOK, "Design catalogs loaded", dtos.DesignCatalogWorkspaceResponse{
		Templates: designTemplateResponses(templateList),
		Palettes:  dtos.NewColorPaletteResponses(paletteList),
		FontSets:  fontSetResponsesWithFontViewURLs(fontSetList),
	})
}
