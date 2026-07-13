package events

import (
	"bytes"
	"events-stocks/dtos"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"io"
)

func TestBuildDuplicateEventResetsOperationalFields(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	eventTypeID := uuid.Must(uuid.NewV4())
	sourceID := uuid.Must(uuid.NewV4())
	maxGuests := 120
	source := &models.Event{
		ID:               sourceID,
		ClientID:         &clientID,
		Name:             "Boda Ana y Luis",
		Identifier:       "boda-ana-luis",
		Description:      "Una celebracion",
		CoverImageURL:    "events/source/cover.webp",
		CoverImageURL2:   "events/source/cover-2.webp",
		CustomDomain:     "boda.example.com",
		Address:          "Jardin Principal",
		SecondAddress:    "Salon Norte",
		MusicUrl:         "https://cdn.example.com/song.mp3",
		EventDateTime:    time.Date(2026, time.March, 14, 20, 0, 0, 0, time.UTC),
		Timezone:         "America/Mexico_City",
		Language:         "es",
		EventTypeID:      eventTypeID,
		OrganizerName:    "Eventi",
		OrganizerEmail:   "hola@example.com",
		OrganizerPhone:   "555-0101",
		MaxGuests:        &maxGuests,
		AllowGuestAccess: true,
		SlugLocked:       true,
		IsActive:         true,
	}

	duplicate := buildDuplicateEvent(source)
	normalizeDuplicateEventForCreate(&duplicate)

	require.NotEqual(t, uuid.Nil, duplicate.ID)
	assert.NotEqual(t, sourceID, duplicate.ID)
	assert.Equal(t, clientID, *duplicate.ClientID)
	assert.NotSame(t, source.ClientID, duplicate.ClientID)
	assert.Equal(t, "Boda Ana y Luis (copia)", duplicate.Name)
	assert.Empty(t, duplicate.Identifier)
	assert.Equal(t, source.Description, duplicate.Description)
	assert.Equal(t, source.Address, duplicate.Address)
	assert.Equal(t, source.SecondAddress, duplicate.SecondAddress)
	assert.Equal(t, source.MusicUrl, duplicate.MusicUrl)
	assert.Equal(t, source.EventDateTime, duplicate.EventDateTime)
	assert.Equal(t, source.Timezone, duplicate.Timezone)
	assert.Equal(t, source.Language, duplicate.Language)
	assert.Equal(t, source.EventTypeID, duplicate.EventTypeID)
	assert.Equal(t, source.OrganizerName, duplicate.OrganizerName)
	assert.Equal(t, source.OrganizerEmail, duplicate.OrganizerEmail)
	assert.Equal(t, source.OrganizerPhone, duplicate.OrganizerPhone)
	assert.Equal(t, maxGuests, *duplicate.MaxGuests)
	assert.NotSame(t, source.MaxGuests, duplicate.MaxGuests)
	assert.True(t, duplicate.AllowGuestAccess)

	assert.Empty(t, duplicate.CoverImageURL)
	assert.Empty(t, duplicate.CoverImageURL2)
	assert.Empty(t, duplicate.CustomDomain)
	assert.False(t, duplicate.SlugLocked)
	assert.False(t, duplicate.IsActive)
	assert.True(t, duplicate.CreatedAt.IsZero())
	assert.True(t, duplicate.UpdatedAt.IsZero())
	assert.False(t, duplicate.DeletedAt.Valid)
}

func TestCopyEventConfigForDuplicatePreservesReusableSettings(t *testing.T) {
	duplicateID := uuid.Must(uuid.NewV4())
	templateID := uuid.Must(uuid.NewV4())
	paletteID := uuid.Must(uuid.NewV4())
	fontSetID := uuid.Must(uuid.NewV4())
	source := &models.EventConfig{
		ID:                          uuid.Must(uuid.NewV4()),
		IsPublic:                    true,
		IsAuthPreview:               true,
		AllowUploads:                true,
		AllowMessages:               true,
		AuthPasswordPreview:         "secret",
		NotifyOnMomentUpload:        true,
		DesignTemplateID:            &templateID,
		ColorPaletteID:              &paletteID,
		FontSetID:                   &fontSetID,
		DefaultWelcomeMessage:       "Bienvenidos",
		DefaultMomentRequestMessage: "Compartan fotos",
		DefaultThankYouMessage:      "Gracias",
		DefaultGuestSignatureTitle:  "Firma",
		ShowCountdown:               true,
		ShowRSVPSection:             true,
		ShowEventLocation:           true,
		ShowMomentWall:              true,
		VisibilityConfigured:        true,
		ShareUploadsEnabled:         true,
		MaxUploadsPerGuest:          18,
		AutoApproveUploads:          true,
		ShowHeader:                  true,
		ShowFooter:                  true,
		CreatedAt:                   time.Now(),
		UpdatedAt:                   time.Now(),
		DeletedAt:                   gorm.DeletedAt{Time: time.Now(), Valid: true},
	}

	config := copyEventConfigForDuplicate(source, duplicateID)

	assert.Equal(t, duplicateID, config.ID)
	assert.True(t, config.IsPublic)
	assert.True(t, config.IsAuthPreview)
	assert.True(t, config.AllowUploads)
	assert.True(t, config.AllowMessages)
	assert.Equal(t, "secret", config.AuthPasswordPreview)
	assert.True(t, config.NotifyOnMomentUpload)
	assert.Equal(t, templateID, *config.DesignTemplateID)
	assert.Equal(t, paletteID, *config.ColorPaletteID)
	assert.Equal(t, fontSetID, *config.FontSetID)
	assert.Equal(t, "Bienvenidos", config.DefaultWelcomeMessage)
	assert.Equal(t, "Compartan fotos", config.DefaultMomentRequestMessage)
	assert.Equal(t, "Gracias", config.DefaultThankYouMessage)
	assert.Equal(t, "Firma", config.DefaultGuestSignatureTitle)
	assert.True(t, config.ShowCountdown)
	assert.True(t, config.ShowRSVPSection)
	assert.True(t, config.ShowEventLocation)
	assert.True(t, config.ShowMomentWall)
	assert.True(t, config.VisibilityConfigured)
	assert.True(t, config.ShareUploadsEnabled)
	assert.Equal(t, 18, config.MaxUploadsPerGuest)
	assert.True(t, config.AutoApproveUploads)
	assert.True(t, config.ShowHeader)
	assert.True(t, config.ShowFooter)
	assert.True(t, config.CreatedAt.IsZero())
	assert.True(t, config.UpdatedAt.IsZero())
	assert.False(t, config.DeletedAt.Valid)
	assert.Nil(t, config.DesignTemplate)
	assert.Nil(t, config.ColorPalette)
	assert.Nil(t, config.FontSet)
}

func TestCopyEventSectionForDuplicatePreservesSduiConfig(t *testing.T) {
	sourceID := uuid.Must(uuid.NewV4())
	duplicateID := uuid.Must(uuid.NewV4())
	source := &models.EventSection{
		ID:            sourceID,
		EventID:       uuid.Must(uuid.NewV4()),
		Key:           "hero",
		Title:         "Bienvenidos",
		ComponentType: "GraduationHero",
		Config:        `{"headline":"Generacion 2026"}`,
		Order:         3,
		IsVisible:     true,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		DeletedAt:     gorm.DeletedAt{Time: time.Now(), Valid: true},
	}

	section := copyEventSectionForDuplicate(source, duplicateID)

	require.NotEqual(t, uuid.Nil, section.ID)
	assert.NotEqual(t, sourceID, section.ID)
	assert.Equal(t, duplicateID, section.EventID)
	assert.Equal(t, "hero", section.Key)
	assert.Equal(t, "Bienvenidos", section.Title)
	assert.Equal(t, "GraduationHero", section.ComponentType)
	assert.Equal(t, `{"headline":"Generacion 2026"}`, section.Config)
	assert.Equal(t, 3, section.Order)
	assert.True(t, section.IsVisible)
	assert.True(t, section.CreatedAt.IsZero())
	assert.True(t, section.UpdatedAt.IsZero())
	assert.False(t, section.DeletedAt.Valid)
	assert.Equal(t, models.Event{}, section.Event)
}

func TestCopyResourceForDuplicateResetsIdentityAndSection(t *testing.T) {
	sourceID := uuid.Must(uuid.NewV4())
	sourceSectionID := uuid.Must(uuid.NewV4())
	duplicateSectionID := uuid.Must(uuid.NewV4())
	resourceTypeID := uuid.Must(uuid.NewV4())
	position := 1
	source := &models.Resource{
		ID:             sourceID,
		EventSectionID: &sourceSectionID,
		ResourceTypeID: resourceTypeID,
		ResourceType:   models.ResourceType{Code: "image", Label: "Image"},
		Path:           "events/hero.webp",
		AltText:        "Hero",
		Title:          "Principal",
		Position:       &position,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	resource := copyResourceForDuplicate(source, duplicateSectionID)

	require.NotEqual(t, uuid.Nil, resource.ID)
	assert.NotEqual(t, sourceID, resource.ID)
	require.NotNil(t, resource.EventSectionID)
	assert.Equal(t, duplicateSectionID, *resource.EventSectionID)
	assert.Equal(t, resourceTypeID, resource.ResourceTypeID)
	assert.Equal(t, models.ResourceType{}, resource.ResourceType)
	assert.Equal(t, "events/hero.webp", resource.Path)
	assert.Equal(t, "Hero", resource.AltText)
	assert.Equal(t, "Principal", resource.Title)
	require.NotNil(t, resource.Position)
	assert.Equal(t, position, *resource.Position)
	assert.NotSame(t, source.Position, resource.Position)
	assert.True(t, resource.CreatedAt.IsZero())
	assert.True(t, resource.UpdatedAt.IsZero())
}

func TestCopyResourceObjectWritesNewObjectPath(t *testing.T) {
	storage := &duplicateStorageMock{content: []byte("image-bytes")}
	svc := &DuplicateService{
		storage:    storage,
		bucket:     "events-bucket",
		provider:   "aws",
		uploadPath: "events",
	}

	copiedPath, err := svc.copyResourceObject("events/photo.webp")

	require.NoError(t, err)
	assert.NotEqual(t, "events/photo.webp", copiedPath)
	assert.Contains(t, copiedPath, "events/")
	assert.Contains(t, copiedPath, ".webp")
	assert.Equal(t, "photo.webp", storage.readFilename)
	assert.Equal(t, "events", storage.readFolder)
	assert.Equal(t, "events", storage.uploadFolder)
	assert.Equal(t, "image/webp", storage.uploadContentType)
	assert.Equal(t, []byte("image-bytes"), storage.uploadContent)
	assert.NotEmpty(t, storage.uploadFilename)
	assert.NotEqual(t, "photo.webp", storage.uploadFilename)
}

func TestDeleteCopiedResourceObjectsUsesObjectPath(t *testing.T) {
	storage := &duplicateStorageMock{}
	svc := &DuplicateService{
		storage:    storage,
		bucket:     "events-bucket",
		provider:   "aws",
		uploadPath: "events",
	}

	svc.deleteCopiedResourceObjects([]string{"events/copied.webp"})

	assert.Equal(t, "copied.webp", storage.deletedFilename)
	assert.Equal(t, "events", storage.deletedFolder)
}

type duplicateStorageMock struct {
	content           []byte
	readFilename      string
	readFolder        string
	uploadFilename    string
	uploadFolder      string
	uploadContentType string
	uploadContent     []byte
	deletedFilename   string
	deletedFolder     string
}

func (m *duplicateStorageMock) FileExists(filename, folder, bucket, provider string) (bool, string, error) {
	return true, "", nil
}
func (m *duplicateStorageMock) GetPresignedFileURL(filename, folder, bucket, provider string, minutes int) (string, error) {
	return "", nil
}
func (m *duplicateStorageMock) GetPresignedPutURL(objectKey, bucket, provider, contentType string, minutes int) (string, error) {
	return "", nil
}
func (m *duplicateStorageMock) CreateMultipartUpload(objectKey, bucket, provider, contentType string) (string, error) {
	return "", nil
}
func (m *duplicateStorageMock) GetPresignedUploadPartURL(objectKey, bucket, provider, uploadID string, partNumber, minutes int) (string, error) {
	return "", nil
}
func (m *duplicateStorageMock) CompleteMultipartUpload(objectKey, bucket, provider, uploadID string, parts []dtos.CompletedUploadPart) error {
	return nil
}
func (m *duplicateStorageMock) AbortMultipartUpload(objectKey, bucket, provider, uploadID string) error {
	return nil
}
func (m *duplicateStorageMock) UpdateFile(content []byte, filename, contentType, folder, bucket, provider string) (string, error) {
	return "", nil
}
func (m *duplicateStorageMock) UploadRawBytesSimple(content []byte, filename, contentType, folder, bucket, provider string) error {
	m.uploadContent = append([]byte(nil), content...)
	m.uploadFilename = filename
	m.uploadFolder = folder
	m.uploadContentType = contentType
	return nil
}
func (m *duplicateStorageMock) DeleteFile(filename, folder, bucket, provider string) error {
	m.deletedFilename = filename
	m.deletedFolder = folder
	return nil
}
func (m *duplicateStorageMock) GetFileStream(filename, folder, bucket, provider string) (io.ReadCloser, error) {
	m.readFilename = filename
	m.readFolder = folder
	return io.NopCloser(bytes.NewReader(m.content)), nil
}
