package resources

import (
	Resources "events-stocks/services/resources" // Asegúrate que apunte a tu service
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"strings"
)

// Instancia del servicio (esto puedes inyectarlo si usas DI)
var resourceSvc = Resources.NewResourceService("your-bucket", "aws", "resources")

// GET /resources/:id
func GetResource(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	resource, err := resourceSvc.GetResourceByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Resource not found", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Resource loaded", resource)
}

func CreateResource(c echo.Context) error {
	sectionIDStr := c.FormValue("section_id")
	resourceTypeIDStr := c.FormValue("resource_type_id")
	altText := c.FormValue("alt_text")
	title := c.FormValue("title")

	sectionID, err := uuid.FromString(sectionIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid section_id UUID", err.Error())
	}

	resourceTypeID, err := uuid.FromString(resourceTypeIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid resource_type_id UUID", err.Error())
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "File is required", err.Error())
	}
	file, err := fileHeader.Open()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error opening file", err.Error())
	}
	defer file.Close()

	resource, err := resourceSvc.UploadAndCreateResource(
		file,
		fileHeader,
		sectionID,
		resourceTypeID,
		altText,
		title,
	)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to create resource", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Resource created", resource)
}

// POST /resources (upload un archivo)
func UploadResource(c echo.Context) error {
	sectionIDStr := c.FormValue("section_id")
	resourceTypeIDStr := c.FormValue("resource_type_id")
	altText := c.FormValue("alt_text")
	title := c.FormValue("title")

	sectionID, err := uuid.FromString(sectionIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid section ID", err.Error())
	}
	resourceTypeID, err := uuid.FromString(resourceTypeIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid resource type ID", err.Error())
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "File is required", err.Error())
	}
	file, err := fileHeader.Open()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error opening file", err.Error())
	}
	defer file.Close()

	resource, err := resourceSvc.UploadAndCreateResource(file, fileHeader, sectionID, resourceTypeID, altText, title)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to upload resource", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Resource uploaded", resource)
}

// DELETE /resources/:id
func DeleteResource(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	if err := resourceSvc.DeleteResource(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting resource", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Resource deleted", nil)
}

// PUT /resources/:id (actualiza solo metadata, no el archivo)
func UpdateResourceMetadata(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	altText := c.FormValue("alt_text")
	title := c.FormValue("title")

	// 📥 Obtener archivo
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "File is required", err.Error())
	}
	file, err := fileHeader.Open()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error opening file", err.Error())
	}
	defer file.Close()

	// 🔁 Obtener recurso existente
	resource, err := resourceSvc.GetResourceByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Resource not found", err.Error())
	}

	// 🔄 Reemplazar archivo
	newURL, err := resourceSvc.ReplaceFile(getFilenameFromURL(resource.URL), file, fileHeader)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to replace file", err.Error())
	}

	// 🧱 Actualizar metadata
	resource.URL = newURL
	resource.AltText = altText
	resource.Title = title

	if err := resourceSvc.UpdateResource(resource); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to update metadata", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Resource updated with new file", resource)
}

func ListResourcesBySection(c echo.Context) error {
	sectionIDParam := c.Param("id")
	sectionID, err := uuid.FromString(sectionIDParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid section UUID", err.Error())
	}

	resources, err := resourceSvc.ListResourcesBySection(sectionID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to list resources", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Resources loaded", resources)
}

func getFilenameFromURL(url string) string {
	parts := strings.Split(url, "/")
	if len(parts) == 0 {
		return url
	}
	return parts[len(parts)-1]
}
