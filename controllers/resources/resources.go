package resources

import (
	"encoding/json"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/bucketrepository"
	Resources "events-stocks/services/resources" // Asegúrate que apunte a tu service
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"strings"
)

var resourceSvc *Resources.ResourceService

func InitResourceController(c *models.Config) {
	resourceSvc = Resources.NewResourceService(c)
}

// GET /resources/:id
func GetResource(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	resource, viewURL, err := resourceSvc.GetResourceByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Resource not found", err.Error())
	}

	response := map[string]interface{}{
		"id":               resource.ID,
		"event_section_id": resource.EventSectionID,
		"resource_type_id": resource.ResourceTypeID,
		"alt_text":         resource.AltText,
		"title":            resource.Title,
		"position":         resource.Position,
		"view_url":         viewURL,
		"created_at":       resource.CreatedAt,
	}

	return utils.Success(c, http.StatusOK, "Resource loaded", response)
}

func GetResourcesBySectionID(c echo.Context) error {
	sectionIDParam := c.Param("key")
	sectionID, err := uuid.FromString(sectionIDParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	if dataStr, ok := c.Get(sectionIDParam + ":" + utils.RedisResourcesKey).(string); ok && dataStr != "" {
		var cached []dtos.ResourceResponse
		if err := json.Unmarshal([]byte(dataStr), &cached); err == nil {
			return utils.Success(c, http.StatusOK, "Resources loaded (from cache)", cached)
		}
	}

	// fallback si no hay cache o falló el unmarshal
	data, err := resourceSvc.GetResourcesBySectionID(sectionID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to load resources", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Resources loaded", data)
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
		&sectionID,
		resourceTypeID,
		altText,
		title,
	)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to create resource", err.Error())
	}

	// 🔐 Generar URL firmada
	parts := strings.Split(resource.Path, "/")
	filename := parts[len(parts)-1]

	viewURL, _ := bucketrepository.GetPresignedFileURL(
		filename,
		resourceSvc.UploadPath,
		resourceSvc.Bucket,
		resourceSvc.Provider,
		60,
	)

	// 🧼 Estructura final del response
	response := map[string]interface{}{
		"id":               resource.ID,
		"event_section_id": resource.EventSectionID,
		"resource_type_id": resource.ResourceTypeID,
		"alt_text":         resource.AltText,
		"title":            resource.Title,
		"position":         resource.Position,
		"view_url":         viewURL,
		"created_at":       resource.CreatedAt,
	}

	return utils.Success(c, http.StatusCreated, "Resource created", response)
}

func UploadMultipleResources(c echo.Context) error {
	sectionIDStr := c.FormValue("section_id")
	resourceTypeIDStr := c.FormValue("resource_type_id")

	sectionID, err := uuid.FromString(sectionIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid section_id UUID", err.Error())
	}

	resourceTypeID, err := uuid.FromString(resourceTypeIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid resource_type_id UUID", err.Error())
	}

	form, err := c.MultipartForm()
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid multipart form", err.Error())
	}

	files := form.File["files"]
	if len(files) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No files provided", "")
	}

	resources, err := resourceSvc.UploadMultipleResources(files, &sectionID, resourceTypeID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to upload resources", err.Error())
	}

	var result []map[string]interface{}
	for _, r := range resources {
		parts := strings.Split(r.Path, "/")
		filename := parts[len(parts)-1]

		viewURL, _ := bucketrepository.GetPresignedFileURL(
			filename,
			resourceSvc.UploadPath,
			resourceSvc.Bucket,
			resourceSvc.Provider,
			60,
		)

		result = append(result, map[string]interface{}{
			"id":               r.ID,
			"event_section_id": r.EventSectionID,
			"resource_type_id": r.ResourceTypeID,
			"alt_text":         r.AltText,
			"title":            r.Title,
			"position":         r.Position,
			"view_url":         viewURL,
			"created_at":       r.CreatedAt,
		})
	}

	return utils.Success(c, http.StatusCreated, "Resources uploaded", result)
}

func UpdateFileContent(c echo.Context) error {
	idStr := c.Param("id")
	_, err := uuid.FromString(idStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid resource ID", err.Error())
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

	filename := c.FormValue("filename")
	if filename == "" {
		return utils.Error(c, http.StatusBadRequest, "Filename is required", "")
	}

	path, err := resourceSvc.UpdateFileContent(file, filename, fileHeader)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to update file content", err.Error())
	}

	parts := strings.Split(path, "/")
	viewURL, _ := bucketrepository.GetPresignedFileURL(
		parts[len(parts)-1],
		resourceSvc.UploadPath,
		resourceSvc.Bucket,
		resourceSvc.Provider,
		60,
	)

	return utils.Success(c, http.StatusOK, "File content updated", map[string]string{
		"path":     path,
		"view_url": viewURL,
	})
}

func ReplaceFile(c echo.Context) error {
	idStr := c.Param("id")
	_, err := uuid.FromString(idStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid resource ID", err.Error())
	}

	oldFilename := c.FormValue("old_filename")
	if oldFilename == "" {
		return utils.Error(c, http.StatusBadRequest, "Old filename is required", "")
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

	path, err := resourceSvc.ReplaceFile(oldFilename, file, fileHeader)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to replace file", err.Error())
	}

	parts := strings.Split(path, "/")
	viewURL, _ := bucketrepository.GetPresignedFileURL(
		parts[len(parts)-1],
		resourceSvc.UploadPath,
		resourceSvc.Bucket,
		resourceSvc.Provider,
		60,
	)

	return utils.Success(c, http.StatusOK, "File replaced", map[string]string{
		"path":     path,
		"view_url": viewURL,
	})
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
