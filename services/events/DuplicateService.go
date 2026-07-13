package events

import (
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

var ErrDuplicateServiceUnavailable = errors.New("duplicate service not initialized")

var _duplicateSvc *DuplicateService

func SetDefaultDuplicateService(svc *DuplicateService) {
	_duplicateSvc = svc
}

func DuplicateEventByID(eventID uuid.UUID, payload dtos.EventPayload) (*models.Event, error) {
	if _duplicateSvc == nil {
		return nil, ErrDuplicateServiceUnavailable
	}
	return _duplicateSvc.DuplicateEventByID(eventID, payload)
}

type DuplicateService struct {
	db         *gorm.DB
	cache      ports.CacheRepository
	storage    ports.ObjectStorageRepository
	bucket     string
	provider   string
	uploadPath string
}

type DuplicateServiceDeps struct {
	Cache      ports.CacheRepository
	Storage    ports.ObjectStorageRepository
	Bucket     string
	Provider   string
	UploadPath string
}

func NewDuplicateService(db *gorm.DB, deps DuplicateServiceDeps) *DuplicateService {
	return &DuplicateService{
		db:         db,
		cache:      deps.Cache,
		storage:    deps.Storage,
		bucket:     deps.Bucket,
		provider:   deps.Provider,
		uploadPath: deps.UploadPath,
	}
}

func (s *DuplicateService) DuplicateEventByID(sourceID uuid.UUID, payload dtos.EventPayload) (*models.Event, error) {
	if s == nil || s.db == nil {
		return nil, ErrDuplicateServiceUnavailable
	}

	var duplicate models.Event
	copiedResourcePaths := []string{}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var source models.Event
		if err := tx.First(&source, "id = ?", sourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: %v", ErrEventNotFound, err)
			}
			return err
		}

		duplicate = buildDuplicateEvent(&source)
		if err := payload.ApplyTo(&duplicate); err != nil {
			return err
		}
		normalizeDuplicateEventForCreate(&duplicate)

		name, err := generateUniqueEventName(tx, duplicate.Name)
		if err != nil {
			return err
		}
		duplicate.Name = name

		identifierBase := duplicate.Identifier
		if strings.TrimSpace(identifierBase) == "" {
			identifierBase = duplicate.Name
		}
		identifier, err := generateUniqueEventIdentifier(tx, identifierBase)
		if err != nil {
			return err
		}
		duplicate.Identifier = identifier

		if err := tx.Create(&duplicate).Error; err != nil {
			return err
		}

		config, err := duplicateEventConfig(tx, sourceID, duplicate.ID)
		if err != nil {
			return err
		}
		duplicate.EventConfig = config

		sectionIDMap, err := duplicateEventSections(tx, sourceID, duplicate.ID)
		if err != nil {
			return err
		}
		if err := s.duplicateSectionResources(tx, sectionIDMap, &copiedResourcePaths); err != nil {
			return err
		}

		analytics := models.EventAnalytics{ID: uuid.Must(uuid.NewV4()), EventID: duplicate.ID}
		if err := tx.Create(&analytics).Error; err != nil {
			return err
		}

		return nil
	}); err != nil {
		s.deleteCopiedResourceObjects(copiedResourcePaths)
		return nil, err
	}

	if err := invalidateEventsCache(s.cache); err != nil {
		return nil, err
	}
	return &duplicate, nil
}

func buildDuplicateEvent(source *models.Event) models.Event {
	if source == nil {
		return models.Event{ID: uuid.Must(uuid.NewV4()), Name: defaultDuplicateName("")}
	}

	return models.Event{
		ID:               uuid.Must(uuid.NewV4()),
		ClientID:         cloneUUIDPointer(source.ClientID),
		Name:             defaultDuplicateName(source.Name),
		Description:      source.Description,
		Address:          source.Address,
		SecondAddress:    source.SecondAddress,
		MusicUrl:         source.MusicUrl,
		EventDateTime:    source.EventDateTime,
		Timezone:         source.Timezone,
		Language:         source.Language,
		EventTypeID:      source.EventTypeID,
		OrganizerName:    source.OrganizerName,
		OrganizerEmail:   source.OrganizerEmail,
		OrganizerPhone:   source.OrganizerPhone,
		MaxGuests:        cloneIntPointer(source.MaxGuests),
		AllowGuestAccess: source.AllowGuestAccess,
		IsActive:         false,
	}
}

func normalizeDuplicateEventForCreate(event *models.Event) {
	if event.ID == uuid.Nil {
		event.ID = uuid.Must(uuid.NewV4())
	}
	event.Name = strings.TrimSpace(event.Name)
	if event.Name == "" {
		event.Name = defaultDuplicateName("")
	}
	event.Identifier = strings.TrimSpace(event.Identifier)
	event.Timezone = strings.TrimSpace(event.Timezone)
	if event.Timezone == "" {
		event.Timezone = "America/Mexico_City"
	}
	event.Language = strings.TrimSpace(event.Language)
	if event.Language == "" {
		event.Language = "es"
	}

	// Avoid sharing storage-owned or routing-sensitive fields with the source event.
	event.CoverImageURL = ""
	event.CoverImageURL2 = ""
	event.CustomDomain = ""
	event.SlugLocked = false
	event.IsActive = false
	event.CreatedAt = time.Time{}
	event.UpdatedAt = time.Time{}
	event.DeletedAt = gorm.DeletedAt{}
	event.Client = nil
	event.EventType = models.EventType{}
	event.EventConfig = models.EventConfig{}
}

func defaultDuplicateName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Evento copia"
	}
	return name + " (copia)"
}

func cloneUUIDPointer(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func generateUniqueEventName(tx *gorm.DB, baseName string) (string, error) {
	base := strings.TrimSpace(baseName)
	if base == "" {
		base = defaultDuplicateName("")
	}

	candidate := base
	for i := 2; ; i++ {
		exists, err := eventNameExists(tx, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s %d", base, i)
	}
}

func eventNameExists(tx *gorm.DB, name string) (bool, error) {
	var count int64
	if err := tx.Model(&models.Event{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func generateUniqueEventIdentifier(tx *gorm.DB, baseIdentifier string) (string, error) {
	base := utils.Slugify(baseIdentifier)
	if base == "" {
		base = "event"
	}

	candidate := base
	for i := 2; ; i++ {
		exists, err := eventIdentifierExists(tx, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

func eventIdentifierExists(tx *gorm.DB, identifier string) (bool, error) {
	var count int64
	if err := tx.Model(&models.Event{}).Where("identifier = ?", identifier).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func duplicateEventConfig(tx *gorm.DB, sourceID, duplicateID uuid.UUID) (models.EventConfig, error) {
	var sourceConfig models.EventConfig
	if err := tx.First(&sourceConfig, "id = ?", sourceID).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return models.EventConfig{}, err
		}
		defaultConfig := NewDefaultEventConfig(duplicateID)
		if err := tx.Create(defaultConfig).Error; err != nil {
			return models.EventConfig{}, err
		}
		return *defaultConfig, nil
	}

	config := copyEventConfigForDuplicate(&sourceConfig, duplicateID)
	if err := tx.Create(&config).Error; err != nil {
		return models.EventConfig{}, err
	}
	return config, nil
}

func copyEventConfigForDuplicate(source *models.EventConfig, duplicateID uuid.UUID) models.EventConfig {
	if source == nil {
		return *NewDefaultEventConfig(duplicateID)
	}

	config := *source
	config.ID = duplicateID
	config.DesignTemplateID = cloneUUIDPointer(source.DesignTemplateID)
	config.ColorPaletteID = cloneUUIDPointer(source.ColorPaletteID)
	config.FontSetID = cloneUUIDPointer(source.FontSetID)
	config.ActiveUntil = cloneTimePointer(source.ActiveUntil)
	config.DesignTemplate = nil
	config.ColorPalette = nil
	config.FontSet = nil
	config.CreatedAt = time.Time{}
	config.UpdatedAt = time.Time{}
	config.DeletedAt = gorm.DeletedAt{}
	return config
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func duplicateEventSections(tx *gorm.DB, sourceID, duplicateID uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	var sections []models.EventSection
	if err := tx.Where("event_id = ?", sourceID).Order("\"order\" ASC").Find(&sections).Error; err != nil {
		return nil, err
	}

	sectionIDMap := make(map[uuid.UUID]uuid.UUID, len(sections))
	for i := range sections {
		section := copyEventSectionForDuplicate(&sections[i], duplicateID)
		if err := tx.Create(&section).Error; err != nil {
			return nil, err
		}
		sectionIDMap[sections[i].ID] = section.ID
	}
	return sectionIDMap, nil
}

func copyEventSectionForDuplicate(source *models.EventSection, duplicateID uuid.UUID) models.EventSection {
	if source == nil {
		return models.EventSection{ID: uuid.Must(uuid.NewV4()), EventID: duplicateID, Config: "{}"}
	}

	section := *source
	section.ID = uuid.Must(uuid.NewV4())
	section.EventID = duplicateID
	section.Event = models.Event{}
	section.CreatedAt = time.Time{}
	section.UpdatedAt = time.Time{}
	section.DeletedAt = gorm.DeletedAt{}
	return section
}

func (s *DuplicateService) duplicateSectionResources(tx *gorm.DB, sectionIDMap map[uuid.UUID]uuid.UUID, copiedResourcePaths *[]string) error {
	if len(sectionIDMap) == 0 {
		return nil
	}

	for sourceSectionID, duplicateSectionID := range sectionIDMap {
		var resources []models.Resource
		if err := tx.Where("event_section_id = ?", sourceSectionID).Order("position ASC").Find(&resources).Error; err != nil {
			return err
		}

		for i := range resources {
			resource := copyResourceForDuplicate(&resources[i], duplicateSectionID)
			if strings.TrimSpace(resource.Path) != "" && !isExternalResourcePath(resource.Path) {
				copiedPath, err := s.copyResourceObject(resource.Path)
				if err != nil {
					return err
				}
				resource.Path = copiedPath
				if copiedResourcePaths != nil {
					*copiedResourcePaths = append(*copiedResourcePaths, copiedPath)
				}
			}
			if err := tx.Create(&resource).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *DuplicateService) deleteCopiedResourceObjects(paths []string) {
	if s == nil || s.storage == nil || len(paths) == 0 {
		return
	}
	for _, resourcePath := range paths {
		folder, filename, err := splitObjectPath(resourcePath)
		if err != nil {
			continue
		}
		if folder == "" {
			folder = s.uploadPath
		}
		_ = s.storage.DeleteFile(filename, folder, s.bucket, s.provider)
	}
}

func copyResourceForDuplicate(source *models.Resource, duplicateSectionID uuid.UUID) models.Resource {
	if source == nil {
		return models.Resource{ID: uuid.Must(uuid.NewV4()), EventSectionID: &duplicateSectionID}
	}

	resource := *source
	resource.ID = uuid.Must(uuid.NewV4())
	resource.EventSectionID = &duplicateSectionID
	resource.ResourceType = models.ResourceType{}
	resource.Position = cloneIntPointer(source.Position)
	resource.CreatedAt = time.Time{}
	resource.UpdatedAt = time.Time{}
	return resource
}

func (s *DuplicateService) copyResourceObject(sourcePath string) (string, error) {
	if s.storage == nil {
		return "", fmt.Errorf("object storage repository not configured")
	}

	folder, filename, err := splitObjectPath(sourcePath)
	if err != nil {
		return "", err
	}
	objectFolder := folder
	if objectFolder == "" {
		objectFolder = s.uploadPath
	}

	stream, err := s.storage.GetFileStream(filename, objectFolder, s.bucket, s.provider)
	if err != nil {
		return "", fmt.Errorf("failed to read source resource %s: %w", sourcePath, err)
	}
	defer stream.Close()

	content, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("failed to read source resource content %s: %w", sourcePath, err)
	}

	newFilename := duplicateResourceFilename(filename)
	if err := s.storage.UploadRawBytesSimple(content, newFilename, inferResourceContentType(filename), objectFolder, s.bucket, s.provider); err != nil {
		return "", fmt.Errorf("failed to copy resource object %s: %w", sourcePath, err)
	}
	if objectFolder == "" {
		return newFilename, nil
	}
	return objectFolder + "/" + newFilename, nil
}

func splitObjectPath(objectPath string) (string, string, error) {
	cleanPath := strings.Trim(strings.TrimSpace(objectPath), "/")
	if cleanPath == "" {
		return "", "", fmt.Errorf("resource path is empty")
	}

	parts := strings.Split(cleanPath, "/")
	filename := strings.TrimSpace(parts[len(parts)-1])
	if filename == "" {
		return "", "", fmt.Errorf("resource path has no filename: %s", objectPath)
	}

	folder := ""
	if len(parts) > 1 {
		folder = strings.Join(parts[:len(parts)-1], "/")
	}
	return folder, filename, nil
}

func duplicateResourceFilename(filename string) string {
	return uuid.Must(uuid.NewV4()).String() + strings.ToLower(path.Ext(filename))
}

func inferResourceContentType(filename string) string {
	switch strings.ToLower(path.Ext(filename)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".heic":
		return "image/heic"
	case ".heif":
		return "image/heif"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mp3":
		return "audio/mpeg"
	case ".ogg":
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	case ".aac":
		return "audio/aac"
	case ".flac":
		return "audio/flac"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".eot":
		return "application/vnd.ms-fontobject"
	default:
		return "application/octet-stream"
	}
}

func isExternalResourcePath(resourcePath string) bool {
	trimmed := strings.TrimSpace(resourcePath)
	return strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "//")
}
