package resources

import (
	"events-stocks/controllers/publicaccess"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/internal/tenantresources"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	"events-stocks/services/ports"
	Resources "events-stocks/services/resources"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var (
	resourceSvc             *Resources.ResourceService
	resourceSectionRepo     ports.EventSectionRepository
	resourceConfigRepo      ports.EventConfigRepository
	resourceAccessTokenRepo ports.AccessTokenRepository
	resourceInvitationRepo  ports.InvitationRepository
	resourceEventRepo       ports.EventsRepository
)

type PublicResourceAccessDeps struct {
	SectionRepo    ports.EventSectionRepository
	ConfigRepo     ports.EventConfigRepository
	TokenRepo      ports.AccessTokenRepository
	InvitationRepo ports.InvitationRepository
	EventRepo      ports.EventsRepository
}

func InitResourceController(svc *Resources.ResourceService, deps ...PublicResourceAccessDeps) {
	resourceSvc = svc
	if len(deps) == 0 {
		return
	}
	resourceSectionRepo = deps[0].SectionRepo
	resourceConfigRepo = deps[0].ConfigRepo
	resourceAccessTokenRepo = deps[0].TokenRepo
	resourceInvitationRepo = deps[0].InvitationRepo
	resourceEventRepo = deps[0].EventRepo
}

func resourceServiceForContext(c echo.Context) *Resources.ResourceService {
	if resourceSvc == nil {
		return nil
	}
	bucket, err := tenantresources.BucketFromContext(c)
	if err != nil {
		return resourceSvc
	}
	return resourceSvc.WithBucket(bucket)
}

func filenameFromPath(path string) string {
	path = strings.TrimSpace(strings.Trim(path, "/"))
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}

func resourceViewURLExpiresAt(ttlMinutes int) time.Time {
	return time.Now().UTC().Add(time.Duration(ttlMinutes) * time.Minute)
}

func resourceResponse(resource *models.Resource, viewURL string, ttlMinutes int) dtos.ResourceResponse {
	if resource != nil && utils.IsAbsoluteURLLike(resource.Path) {
		return dtos.NewResourceResponse(resource, viewURL, nil)
	}

	expiresAt := resourceViewURLExpiresAt(ttlMinutes)
	return dtos.NewResourceResponse(resource, viewURL, &expiresAt)
}

func adminResourceResponse(resource *models.Resource, viewURL string, ttlMinutes int) dtos.AdminResourceResponse {
	expiresAt := resourceViewURLExpiresAt(ttlMinutes)
	return dtos.NewAdminResourceResponse(resource, viewURL, &expiresAt)
}

// GET /resources/:id
func GetResource(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	resource, err := resourceSvc.GetResourceRecordByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Resource not found", err.Error())
	}
	if err := requirePublicResourceAccess(c, resource); err != nil {
		return err
	}
	if c.Response().Committed {
		return nil
	}

	resource, viewURL, err := resourceSvc.GetResourceByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Resource not found", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Resource loaded", resourceResponse(resource, viewURL, Resources.ResourceViewURLTTLMinutes))
}

func GetResourcesBySectionID(c echo.Context) error {
	sectionIDParam := c.Param("key")
	sectionID, err := uuid.FromString(sectionIDParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if err := requirePublicResourceSectionAccess(c, sectionID); err != nil {
		return err
	}
	if c.Response().Committed {
		return nil
	}

	return loadResourcesBySectionID(c, sectionID)
}

func GetResourcesBySectionIDAdmin(c echo.Context) error {
	sectionIDParam := c.Param("key")
	sectionID, err := uuid.FromString(sectionIDParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if _, _, authErr := authz.RequireEventSectionAccess(c, sectionID); authErr != nil {
		return authz.Respond(c, authErr)
	}
	return loadAdminResourcesBySectionID(c, sectionID)
}

func requirePublicResourceSectionAccess(c echo.Context, sectionID uuid.UUID) error {
	if resourceSectionRepo == nil {
		return utils.Error(c, http.StatusInternalServerError, "Resource access dependencies unavailable", "event section repository is not configured")
	}
	section, err := resourceSectionRepo.GetEventSectionByID(sectionID)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Section not found", err.Error())
	}
	access, err := publicaccess.AllowEventReadFromRequestWithConfig(c, section.EventID, publicaccess.EventReadDeps{
		ConfigRepo:           resourceConfigRepo,
		TokenRepo:            resourceAccessTokenRepo,
		InvitationRepo:       resourceInvitationRepo,
		IsEventActive:        resourceEventActive,
		RequirePasswordProof: true,
	})
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading event access", err.Error())
	}
	if !access.Allowed {
		return utils.Error(c, http.StatusForbidden, "Event is not public", "")
	}
	if !section.IsVisible {
		return utils.Error(c, http.StatusForbidden, "Section is not public", "")
	}
	if strings.TrimSpace(section.ComponentType) == "" {
		return utils.Error(c, http.StatusForbidden, "Section is not public", "")
	}
	if !eventsService.PageSpecSectionVisible(section.ComponentType, access.Config) {
		return utils.Error(c, http.StatusForbidden, "Section is not public", "")
	}
	return nil
}

func resourceEventActive(eventID uuid.UUID) (bool, error) {
	if resourceEventRepo == nil {
		return true, nil
	}
	event, err := resourceEventRepo.GetEventByIDRaw(eventID)
	if err != nil || event == nil {
		return false, err
	}
	return event.IsActive, nil
}

func loadResourcesBySectionID(c echo.Context, sectionID uuid.UUID) error {
	data, err := resourceSvc.GetResourcesBySectionID(sectionID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to load resources", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Resources loaded", data)
}

func loadAdminResourcesBySectionID(c echo.Context, sectionID uuid.UUID) error {
	data, err := resourceSvc.GetAdminResourcesBySectionID(sectionID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to load resources", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Resources loaded", data)
}

func requirePublicResourceAccess(c echo.Context, resource *models.Resource) error {
	if resource == nil || resource.EventSectionID == nil {
		return utils.Error(c, http.StatusForbidden, "Resource is not public", "")
	}
	return requirePublicResourceSectionAccess(c, *resource.EventSectionID)
}

func ListResourceTypes(c echo.Context) error {
	types, err := resourceSvc.ListResourceTypes()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching resource types", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Resource types loaded", dtos.NewResourceTypeResponses(types))
}

func resourceFormValue(c echo.Context, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(c.FormValue(key)); value != "" {
			return value
		}
	}
	return ""
}

func resourceSectionIDFormValue(c echo.Context) string {
	return resourceFormValue(
		c,
		"section_id",
		"sectionId",
		"sectionID",
		"SectionID",
		"SectionId",
		"event_section_id",
		"eventSectionId",
		"eventSectionID",
		"EventSectionID",
		"EventSectionId",
	)
}

func resourceTypeIDFormValue(c echo.Context) string {
	return resourceFormValue(
		c,
		"resource_type_id",
		"resourceTypeId",
		"resourceTypeID",
		"ResourceTypeID",
		"ResourceTypeId",
	)
}

func resourcePositionFormValue(c echo.Context) string {
	return resourceFormValue(c, "position", "Position", "order", "Order", "sort_order", "sortOrder", "SortOrder")
}

func CreateResource(c echo.Context) error {
	sectionIDStr := resourceSectionIDFormValue(c)
	resourceTypeIDStr := resourceTypeIDFormValue(c)
	altText := resourceFormValue(c, "alt_text", "altText", "AltText")
	title := resourceFormValue(c, "title", "Title")
	positionStr := resourcePositionFormValue(c)

	// section_id is optional — resources like event covers don't belong to a section
	var sectionID *uuid.UUID
	if sectionIDStr != "" {
		parsed, err := uuid.FromString(sectionIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid section_id UUID", err.Error())
		}
		sectionID = &parsed
	}

	// resource_type_id is optional — when omitted, infer from file MIME type
	if sectionID == nil {
		user, err := authz.CurrentUser(c)
		if err != nil {
			return authz.Respond(c, err)
		}
		if !user.IsPrimaryRoot() {
			return authz.Respond(c, &authz.Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Only the primary platform administrator can create platform resources"})
		}
	} else if _, _, err := authz.RequireEventSectionCapability(c, *sectionID, authz.CapabilityEventManage); err != nil {
		return authz.Respond(c, err)
	}

	var position *int
	if positionStr != "" {
		parsed, err := strconv.Atoi(positionStr)
		if err != nil || parsed < 0 {
			return utils.Error(c, http.StatusBadRequest, "Invalid position", "position must be a non-negative integer")
		}
		position = &parsed
	}

	var resourceTypeID uuid.UUID
	if resourceTypeIDStr != "" {
		parsed, err := uuid.FromString(resourceTypeIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid resource_type_id UUID", err.Error())
		}
		resourceTypeID = parsed
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "File is required", err.Error())
	}

	// If no resource_type_id provided, resolve from MIME type
	if resourceTypeIDStr == "" {
		code := "file"
		ct := fileHeader.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "image/") {
			code = "image"
		} else if strings.HasPrefix(ct, "video/") {
			code = "video"
		} else if strings.HasPrefix(ct, "audio/") {
			code = "audio"
		}
		resolved, err := resourceSvc.ResolveResourceTypeByCode(code)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Could not resolve resource type", err.Error())
		}
		resourceTypeID = resolved
	}

	file, err := fileHeader.Open()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error opening file", err.Error())
	}
	defer file.Close()

	scopedResourceSvc := resourceServiceForContext(c)
	resource, err := scopedResourceSvc.UploadAndCreateResource(
		file,
		fileHeader,
		sectionID,
		resourceTypeID,
		altText,
		title,
		position,
	)
	if err != nil {
		status, detail := Resources.UploadErrorResponse(err)
		return utils.Error(c, status, "Failed to create resource", detail)
	}

	// 🔐 Generar URL firmada
	viewURL, _ := scopedResourceSvc.GetPresignedURLWithTTL(resource.Path, Resources.ResourceMutationURLTTLMinutes)

	// 🧼 Estructura final del response
	return utils.Success(c, http.StatusCreated, "Resource created", adminResourceResponse(resource, viewURL, Resources.ResourceMutationURLTTLMinutes))
}

func UploadMultipleResources(c echo.Context) error {
	sectionIDStr := resourceSectionIDFormValue(c)
	resourceTypeIDStr := resourceTypeIDFormValue(c)

	var sectionID *uuid.UUID
	if sectionIDStr != "" {
		parsed, err := uuid.FromString(sectionIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid section_id UUID", err.Error())
		}
		sectionID = &parsed
	}
	if sectionID == nil {
		user, err := authz.CurrentUser(c)
		if err != nil {
			return authz.Respond(c, err)
		}
		if !user.IsPrimaryRoot() {
			return authz.Respond(c, &authz.Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Only the primary platform administrator can create platform resources"})
		}
	} else if _, _, err := authz.RequireEventSectionCapability(c, *sectionID, authz.CapabilityEventManage); err != nil {
		return authz.Respond(c, err)
	}

	var resourceTypeID uuid.UUID
	if resourceTypeIDStr != "" {
		parsed, err := uuid.FromString(resourceTypeIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid resource_type_id UUID", err.Error())
		}
		resourceTypeID = parsed
	} else {
		resolved, err := resourceSvc.ResolveResourceTypeByCode("image")
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Could not resolve resource type", err.Error())
		}
		resourceTypeID = resolved
	}

	form, err := c.MultipartForm()
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid multipart form", err.Error())
	}

	files := form.File["files"]
	if len(files) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No files provided", "")
	}

	scopedResourceSvc := resourceServiceForContext(c)
	resources, err := scopedResourceSvc.UploadMultipleResources(files, sectionID, resourceTypeID)
	if err != nil {
		status, detail := Resources.UploadErrorResponse(err)
		return utils.Error(c, status, "Failed to upload resources", detail)
	}

	result := make([]dtos.AdminResourceResponse, 0, len(resources))
	for _, r := range resources {
		viewURL, _ := scopedResourceSvc.GetPresignedURLWithTTL(r.Path, Resources.ResourceMutationURLTTLMinutes)
		result = append(result, adminResourceResponse(r, viewURL, Resources.ResourceMutationURLTTLMinutes))
	}

	return utils.Success(c, http.StatusCreated, "Resources uploaded", result)
}

func UpdateFileContent(c echo.Context) error {
	idStr := c.Param("id")
	id, err := uuid.FromString(idStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid resource ID", err.Error())
	}
	_, resource, authErr := authz.RequireResourceCapability(c, id, authz.CapabilityEventManage)
	if authErr != nil {
		return authz.Respond(c, authErr)
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
	currentFilename := filenameFromPath(resource.Path)
	if currentFilename == "" {
		return utils.Error(c, http.StatusInternalServerError, "Resource path missing", "")
	}
	if filename == "" {
		filename = currentFilename
	}
	if filename != currentFilename {
		return utils.Error(c, http.StatusBadRequest, "Filename mismatch", "filename must match the stored resource path")
	}

	scopedResourceSvc := resourceSvc.WithBucket(resource.MediaBucket)
	path, err := scopedResourceSvc.UpdateFileContent(file, resource.Path, fileHeader)
	if err != nil {
		status, detail := Resources.UploadErrorResponse(err)
		return utils.Error(c, status, "Failed to update file content", detail)
	}
	if path != resource.Path {
		resource.Path = path
		if err := scopedResourceSvc.UpdateResource(resource); err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Failed to update resource path", err.Error())
		}
	} else if err := scopedResourceSvc.TouchResourceUpdatedAt(resource, time.Now().UTC()); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to update resource timestamp", err.Error())
	}

	viewURL, _ := scopedResourceSvc.GetPresignedURLWithTTL(path, Resources.ResourceMutationURLTTLMinutes)

	expiresAt := resourceViewURLExpiresAt(Resources.ResourceMutationURLTTLMinutes)
	return utils.Success(c, http.StatusOK, "File content updated", dtos.NewResourceFileMutationResponse(path, viewURL, &expiresAt))
}

func ReplaceFile(c echo.Context) error {
	idStr := c.Param("id")
	id, err := uuid.FromString(idStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid resource ID", err.Error())
	}
	_, resource, authErr := authz.RequireResourceCapability(c, id, authz.CapabilityEventManage)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}

	oldFilename := filenameFromPath(resource.Path)
	if oldFilename == "" {
		return utils.Error(c, http.StatusInternalServerError, "Resource path missing", "")
	}
	if formOldFilename := c.FormValue("old_filename"); formOldFilename != "" && formOldFilename != oldFilename {
		return utils.Error(c, http.StatusBadRequest, "Filename mismatch", "old_filename must match the stored resource path")
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

	oldPath := resource.Path
	scopedResourceSvc := resourceSvc.WithBucket(resource.MediaBucket)
	path, err := scopedResourceSvc.ReplaceFile(oldPath, file, fileHeader)
	if err != nil {
		status, detail := Resources.UploadErrorResponse(err)
		return utils.Error(c, status, "Failed to replace file", detail)
	}
	if path != oldPath {
		resource.Path = path
		if err := scopedResourceSvc.UpdateResource(resource); err != nil {
			if cleanupErr := scopedResourceSvc.DeleteObjectByPath(path); cleanupErr != nil {
				slog.Error("replacement resource rollback failed", "resource_id", resource.ID, "path", path, "error", cleanupErr)
			}
			return utils.Error(c, http.StatusInternalServerError, "Failed to update resource path", err.Error())
		}
		if cleanupErr := scopedResourceSvc.DeleteObjectByPath(oldPath); cleanupErr != nil {
			slog.Warn("old replacement resource cleanup failed", "resource_id", resource.ID, "path", oldPath, "error", cleanupErr)
		}
	} else if err := scopedResourceSvc.TouchResourceUpdatedAt(resource, time.Now().UTC()); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to update resource timestamp", err.Error())
	}

	viewURL, _ := scopedResourceSvc.GetPresignedURLWithTTL(path, Resources.ResourceMutationURLTTLMinutes)

	expiresAt := resourceViewURLExpiresAt(Resources.ResourceMutationURLTTLMinutes)
	return utils.Success(c, http.StatusOK, "File replaced", dtos.NewResourceFileMutationResponse(path, viewURL, &expiresAt))
}

// DELETE /resources/:id
func DeleteResource(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if _, _, authErr := authz.RequireResourceCapability(c, id, authz.CapabilityEventManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	if err := resourceSvc.DeleteResource(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting resource", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Resource deleted", nil)
}
