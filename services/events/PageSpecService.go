package events

import (
	"encoding/json"
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/accesstoken"
	"events-stocks/internal/previewtoken"
	"events-stocks/models"
	"events-stocks/services/ports"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid"
)

var _pageSpecSvc *PageSpecService

var (
	ErrPageSpecNotPublic     = errors.New("event page is not public")
	ErrPageSpecInactive      = errors.New("event page is inactive")
	ErrPageSpecTokenExpired  = errors.New("token expired")
	ErrPageSpecTokenNotFound = errors.New("token not found")
)

const defaultPageSpecMaxUploadsPerGuest = 30

func SetDefaultPageSpecService(svc *PageSpecService) {
	_pageSpecSvc = svc
}

type PageSpecService struct {
	accessTokens ports.AccessTokenRepository
	invitations  ports.InvitationRepository
	events       ports.EventsRepository
	sections     ports.EventSectionRepository
	configs      ports.EventConfigRepository
	resources    PageSpecResourceVersionRepository
	guests       PageSpecGuestVersionRepository
	moments      PageSpecMomentVersionRepository
}

type PageSpecResourceVersionRepository interface {
	LatestResourceUpdatedAtByEventID(eventID uuid.UUID) (*time.Time, error)
}

type PageSpecSectionResourceVersionRepository interface {
	LatestResourceUpdatedAtBySectionIDs(sectionIDs []uuid.UUID) (*time.Time, error)
}

type PageSpecGuestVersionRepository interface {
	LatestPublicAttendeeUpdatedAtByEventID(eventID uuid.UUID) (*time.Time, error)
}

type PageSpecMomentVersionRepository interface {
	LatestPublicMomentUpdatedAtByEventID(eventID uuid.UUID) (*time.Time, error)
}

func (s *PageSpecService) WithGuestVersionRepository(guests PageSpecGuestVersionRepository) *PageSpecService {
	if s != nil {
		s.guests = guests
	}
	return s
}

func (s *PageSpecService) WithMomentVersionRepository(moments PageSpecMomentVersionRepository) *PageSpecService {
	if s != nil {
		s.moments = moments
	}
	return s
}

func NewPageSpecService(
	accessTokens ports.AccessTokenRepository,
	invitations ports.InvitationRepository,
	eventsRepo ports.EventsRepository,
	sections ports.EventSectionRepository,
	configs ports.EventConfigRepository,
	resourceVersions ...PageSpecResourceVersionRepository,
) *PageSpecService {
	var resources PageSpecResourceVersionRepository
	if len(resourceVersions) > 0 {
		resources = resourceVersions[0]
	}
	return &PageSpecService{
		accessTokens: accessTokens,
		invitations:  invitations,
		events:       eventsRepo,
		sections:     sections,
		configs:      configs,
		resources:    resources,
	}
}

// pageSpecDeps holds the repository functions needed to build a PageSpec.
// Extracted as a struct so tests can inject mocks without modifying package-level state.
type pageSpecDeps struct {
	getToken                       func(token string) (*models.InvitationAccessToken, error)
	getInvitation                  func(id uuid.UUID) (*models.Invitation, error)
	getEvent                       func(id uuid.UUID) (*models.Event, error)
	getSections                    func(eventID uuid.UUID) ([]models.EventSection, error)
	getConfig                      func(id uuid.UUID) (*models.EventConfig, error)
	getResourceVersion             func(eventID uuid.UUID) (*time.Time, error)
	getResourceVersionBySectionIDs func(sectionIDs []uuid.UUID) (*time.Time, error)
	getGuestVersion                func(eventID uuid.UUID) (*time.Time, error)
	getMomentVersion               func(eventID uuid.UUID) (*time.Time, error)
	now                            func() time.Time
}

type identifierPageSpecDeps struct {
	getEventByIdentifier           func(identifier string) (*models.Event, error)
	getToken                       func(token string) (*models.InvitationAccessToken, error)
	getInvitation                  func(id uuid.UUID) (*models.Invitation, error)
	getSections                    func(eventID uuid.UUID) ([]models.EventSection, error)
	getConfig                      func(id uuid.UUID) (*models.EventConfig, error)
	getResourceVersion             func(eventID uuid.UUID) (*time.Time, error)
	getResourceVersionBySectionIDs func(sectionIDs []uuid.UUID) (*time.Time, error)
	getGuestVersion                func(eventID uuid.UUID) (*time.Time, error)
	getMomentVersion               func(eventID uuid.UUID) (*time.Time, error)
	validatePreviewToken           func(token string, eventID uuid.UUID) bool
	now                            func() time.Time
}

// buildSpecDeps holds the repository functions needed after the event is resolved.
type buildSpecDeps struct {
	getSections                    func(eventID uuid.UUID) ([]models.EventSection, error)
	getConfig                      func(id uuid.UUID) (*models.EventConfig, error)
	getResourceVersion             func(eventID uuid.UUID) (*time.Time, error)
	getResourceVersionBySectionIDs func(sectionIDs []uuid.UUID) (*time.Time, error)
	getGuestVersion                func(eventID uuid.UUID) (*time.Time, error)
	getMomentVersion               func(eventID uuid.UUID) (*time.Time, error)
	previewAuthorized              bool
}

// buildPageSpecFromEvent builds a PageSpec from an already-resolved event.
// Shared by both the token-based and identifier-based flows.
func buildPageSpecFromEvent(event *models.Event, deps buildSpecDeps) (*dtos.PageSpec, error) {
	cfg := loadEffectivePageSpecConfig(event.ID, deps.getConfig)
	return buildPageSpecFromEventWithConfig(event, deps, cfg)
}

func loadEffectivePageSpecConfig(eventID uuid.UUID, getConfig func(id uuid.UUID) (*models.EventConfig, error)) *models.EventConfig {
	if getConfig == nil {
		return NewDefaultEventConfig(eventID)
	}
	loaded, err := getConfig(eventID)
	if err != nil {
		return NewDefaultEventConfig(eventID)
	}
	cfg := effectivePageSpecConfig(loaded)
	if cfg == nil {
		return NewDefaultEventConfig(eventID)
	}
	return cfg
}

func buildPageSpecFromEventWithConfig(event *models.Event, deps buildSpecDeps, cfg *models.EventConfig) (*dtos.PageSpec, error) {
	// 1. Fetch visible SDUI sections ordered by position
	sections, err := deps.getSections(event.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load sections: %w", err)
	}

	if cfg == nil {
		cfg = NewDefaultEventConfig(event.ID)
	}
	resourceSectionIDs := pageSpecVisibleResourceSectionIDs(sections, cfg)
	resourceUpdatedAt, guestUpdatedAt, momentUpdatedAt := loadPageSpecContentVersions(
		event.ID,
		resourceSectionIDs,
		pageSpecHasVisiblePublicAttendeeSection(sections, cfg),
		deps,
	)

	// 2. Build contact only if event has organizer data
	var contact *dtos.PageSpecContact
	if pageSpecContactVisible(cfg) && (event.OrganizerName != "" || event.OrganizerPhone != "" || event.OrganizerEmail != "") {
		contact = &dtos.PageSpecContact{
			Name:  event.OrganizerName,
			Phone: event.OrganizerPhone,
			Email: event.OrganizerEmail,
		}
	}

	// 3. Build access control from EventConfig (best-effort — never blocks the page)
	var access *dtos.PageSpecAccess
	if cfg != nil {
		isZero := cfg.ActiveFrom.IsZero() || cfg.ActiveFrom.Year() <= 1970
		access = &dtos.PageSpecAccess{
			PasswordProtected: cfg.HasAuthPasswordPreview(),
			AccessVersion:     pageSpecAccessVersion(cfg),
			PreviewAuthorized: deps.previewAuthorized,
		}
		if !isZero {
			t := cfg.ActiveFrom
			access.ActiveFrom = &t
		}
		if cfg.ActiveUntil != nil && !cfg.ActiveUntil.IsZero() {
			access.ActiveUntil = cfg.ActiveUntil
		}
	}

	// 4. Build meta
	var eventDateTime *time.Time
	if !event.EventDateTime.IsZero() {
		t := event.EventDateTime
		eventDateTime = &t
	}
	meta := dtos.PageSpecMeta{
		PageTitle:      event.Name,
		Contact:        contact,
		EventID:        event.ID.String(),
		Identifier:     event.Identifier,
		CoverImageURL:  event.CoverImageURL,
		CoverVariants:  dtos.NewPublicMediaVariants(event.CoverVariants),
		EventDateTime:  eventDateTime,
		Address:        event.Address,
		SecondAddress:  event.SecondAddress,
		Timezone:       event.Timezone,
		Language:       event.Language,
		EventType:      event.EventType.Name,
		ContentVersion: pageSpecContentVersion(event, cfg, sections, resourceUpdatedAt, guestUpdatedAt, momentUpdatedAt),
		Access:         access,
		FooterVisible:  cfg == nil || cfg.ShowFooter,
		Theme:          buildPageSpecTheme(cfg),
	}
	if event.MusicUrl != "" {
		musicUrl := event.MusicUrl
		meta.MusicUrl = &musicUrl
	}

	// 5. Build sections — for MomentWall, inject runtime flags from EventConfig
	specSections := make([]dtos.PageSpecSection, 0, len(sections))
	for _, s := range sections {
		if strings.TrimSpace(s.ComponentType) == "" {
			continue
		}

		if !s.IsVisible {
			continue
		}

		if !pageSpecSectionVisible(s.ComponentType, cfg) {
			continue
		}

		config := dtos.EventSectionConfigRaw(s.Config)

		config = injectRuntimeSectionConfig(s.ComponentType, config, event, cfg)

		specSections = append(specSections, dtos.PageSpecSection{
			Type:      s.ComponentType,
			Title:     s.Title,
			SectionId: s.ID.String(),
			Order:     s.Order,
			Config:    config,
		})
	}
	sortPageSpecSections(specSections)

	return &dtos.PageSpec{
		Meta:     meta,
		Sections: specSections,
	}, nil
}

// loadPageSpecContentVersions overlaps the independent aggregate reads used by
// contentVersion. The fan-out is fixed at three and every loader is best-effort,
// matching the prior sequential behavior while making miss latency approach the
// slowest query instead of the sum of all queries.
func loadPageSpecContentVersions(
	eventID uuid.UUID,
	resourceSectionIDs []uuid.UUID,
	includeGuestVersion bool,
	deps buildSpecDeps,
) (resourceUpdatedAt, guestUpdatedAt, momentUpdatedAt time.Time) {
	type versionLoader struct {
		destination *time.Time
		load        func() time.Time
	}

	loaders := make([]versionLoader, 0, 3)
	if deps.getResourceVersionBySectionIDs != nil || deps.getResourceVersion != nil {
		loaders = append(loaders, versionLoader{
			destination: &resourceUpdatedAt,
			load: func() time.Time {
				return pageSpecResourceUpdatedAt(
					eventID,
					resourceSectionIDs,
					deps.getResourceVersionBySectionIDs,
					deps.getResourceVersion,
				)
			},
		})
	}
	if includeGuestVersion && deps.getGuestVersion != nil {
		loaders = append(loaders, versionLoader{
			destination: &guestUpdatedAt,
			load: func() time.Time {
				return pageSpecGuestUpdatedAt(eventID, deps.getGuestVersion)
			},
		})
	}
	if deps.getMomentVersion != nil {
		loaders = append(loaders, versionLoader{
			destination: &momentUpdatedAt,
			load: func() time.Time {
				return pageSpecMomentUpdatedAt(eventID, deps.getMomentVersion)
			},
		})
	}
	if len(loaders) == 1 {
		*loaders[0].destination = loaders[0].load()
		return resourceUpdatedAt, guestUpdatedAt, momentUpdatedAt
	}

	var waitGroup sync.WaitGroup
	waitGroup.Add(len(loaders))
	for _, loader := range loaders {
		loader := loader
		go func() {
			defer waitGroup.Done()
			*loader.destination = loader.load()
		}()
	}
	waitGroup.Wait()
	return resourceUpdatedAt, guestUpdatedAt, momentUpdatedAt
}

func sortPageSpecSections(sections []dtos.PageSpecSection) {
	sort.SliceStable(sections, func(i, j int) bool {
		if sections[i].Order != sections[j].Order {
			return sections[i].Order < sections[j].Order
		}
		return sections[i].SectionId < sections[j].SectionId
	})
}

// effectivePageSpecConfig treats legacy all-false visibility configs as defaults.
func effectivePageSpecConfig(cfg *models.EventConfig) *models.EventConfig {
	if cfg == nil {
		return nil
	}
	return cfg.WithVisibilityDefaults()
}

// PageSpecSectionVisible exposes the public section visibility contract used by page specs.
func PageSpecSectionVisible(componentType string, cfg *models.EventConfig) bool {
	return pageSpecSectionVisible(componentType, effectivePageSpecConfig(cfg))
}

func pageSpecContactVisible(cfg *models.EventConfig) bool {
	return cfg == nil || cfg.ShowContactSection
}

func pageSpecAccessVersion(cfg *models.EventConfig) string {
	if cfg == nil {
		return ""
	}
	if !cfg.UpdatedAt.IsZero() {
		return cfg.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	if !cfg.CreatedAt.IsZero() {
		return cfg.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return ""
}

func latestPageSpecTime(current time.Time, candidates ...time.Time) time.Time {
	for _, candidate := range candidates {
		if candidate.IsZero() {
			continue
		}
		candidate = candidate.UTC()
		if current.IsZero() || candidate.After(current) {
			current = candidate
		}
	}
	return current
}

func pageSpecResourceUpdatedAt(
	eventID uuid.UUID,
	sectionIDs []uuid.UUID,
	getResourceVersionBySectionIDs func([]uuid.UUID) (*time.Time, error),
	getResourceVersion func(uuid.UUID) (*time.Time, error),
) time.Time {
	if getResourceVersionBySectionIDs != nil {
		if len(sectionIDs) == 0 {
			return time.Time{}
		}
		updatedAt, err := getResourceVersionBySectionIDs(sectionIDs)
		if err != nil || updatedAt == nil {
			return time.Time{}
		}
		return *updatedAt
	}
	if getResourceVersion == nil {
		return time.Time{}
	}
	updatedAt, err := getResourceVersion(eventID)
	if err != nil || updatedAt == nil {
		return time.Time{}
	}
	return *updatedAt
}

func pageSpecVisibleResourceSectionIDs(sections []models.EventSection, cfg *models.EventConfig) []uuid.UUID {
	sectionIDs := make([]uuid.UUID, 0, len(sections))
	for _, section := range sections {
		if section.ID == uuid.Nil || !section.IsVisible {
			continue
		}
		if !pageSpecSectionVisible(section.ComponentType, cfg) {
			continue
		}
		if !pageSpecSectionUsesResources(section.ComponentType) {
			continue
		}
		sectionIDs = append(sectionIDs, section.ID)
	}
	return sectionIDs
}

func pageSpecSectionUsesResources(componentType string) bool {
	switch pageSpecSectionKind(componentType) {
	case "GraduationHero", "EventVenue", "Reception", "PhotoGrid", "RSVPConfirmation", "GraduatesList", "Hosts", "HERO", "GALLERY":
		return true
	default:
		return false
	}
}

func pageSpecGuestUpdatedAt(eventID uuid.UUID, getGuestVersion func(uuid.UUID) (*time.Time, error)) time.Time {
	if getGuestVersion == nil {
		return time.Time{}
	}
	updatedAt, err := getGuestVersion(eventID)
	if err != nil || updatedAt == nil {
		return time.Time{}
	}
	return *updatedAt
}

func pageSpecMomentUpdatedAt(eventID uuid.UUID, getMomentVersion func(uuid.UUID) (*time.Time, error)) time.Time {
	if getMomentVersion == nil {
		return time.Time{}
	}
	updatedAt, err := getMomentVersion(eventID)
	if err != nil || updatedAt == nil {
		return time.Time{}
	}
	return *updatedAt
}

func pageSpecHasVisiblePublicAttendeeSection(sections []models.EventSection, cfg *models.EventConfig) bool {
	for _, section := range sections {
		if !section.IsVisible {
			continue
		}
		switch pageSpecSectionKind(section.ComponentType) {
		case "GraduatesList", "Hosts":
			if pageSpecSectionVisible(section.ComponentType, cfg) {
				return true
			}
		}
	}
	return false
}

func pageSpecContentVersion(event *models.Event, cfg *models.EventConfig, sections []models.EventSection, extraTimes ...time.Time) string {
	var latest time.Time
	latest = latestPageSpecTime(latest, extraTimes...)
	if event != nil {
		latest = latestPageSpecTime(latest, event.UpdatedAt, event.CreatedAt)
	}
	if cfg != nil {
		latest = latestPageSpecTime(latest, cfg.UpdatedAt, cfg.CreatedAt)
	}
	for _, section := range sections {
		latest = latestPageSpecTime(latest, section.UpdatedAt, section.CreatedAt)
	}
	if latest.IsZero() {
		return ""
	}
	return latest.UTC().Format(time.RFC3339Nano)
}

// EventConfigAccessVersion returns the version string used by public PageSpec access gates.
func EventConfigAccessVersion(cfg *models.EventConfig) string {
	return pageSpecAccessVersion(cfg)
}

func injectRuntimeSectionConfig(componentType string, config json.RawMessage, event *models.Event, cfg *models.EventConfig) json.RawMessage {
	sectionKind := pageSpecSectionKind(componentType)
	var configMap map[string]interface{}
	if err := json.Unmarshal(config, &configMap); err != nil {
		if !pageSpecSectionNeedsRuntimeConfig(sectionKind) {
			return config
		}
		configMap = map[string]interface{}{}
	}
	if configMap == nil {
		if !pageSpecSectionNeedsRuntimeConfig(sectionKind) {
			return config
		}
		configMap = map[string]interface{}{}
	}

	switch sectionKind {
	case "CountdownHeader":
		if !event.EventDateTime.IsZero() {
			configMap["targetDate"] = event.EventDateTime.Format(time.RFC3339)
		}
	case "MomentWall":
		momentsWallPublished := cfg != nil && cfg.ShowMomentWall
		configMap["identifier"] = event.Identifier
		configMap["allow_uploads"] = cfg != nil && cfg.AllowUploads && !momentsWallPublished
		configMap["allow_messages"] = cfg != nil && cfg.AllowMessages
		configMap["auto_approve_uploads"] = cfg != nil && cfg.AutoApproveUploads
		configMap["published"] = momentsWallPublished
		configMap["moments_wall_published"] = momentsWallPublished
		configMap["show_moment_wall"] = momentsWallPublished
		configMap["share_uploads_enabled"] = cfg != nil && cfg.AllowUploads && cfg.ShareUploadsEnabled && !momentsWallPublished
		configMap["max_uploads_per_guest"] = pageSpecMaxUploadsPerGuest(cfg)
		if cfg != nil {
			setConfigString(configMap, "moment_request_message", cfg.DefaultMomentRequestMessage)
			setConfigString(configMap, "subtitle", cfg.DefaultMomentRequestMessage)
		}
	case "RSVPConfirmation":
		if cfg != nil {
			setConfigString(configMap, "welcome_message", cfg.DefaultWelcomeMessage)
			setConfigString(configMap, "thank_you_message", cfg.DefaultThankYouMessage)
			setConfigString(configMap, "guest_signature_title", cfg.DefaultGuestSignatureTitle)
		}
	}

	if updated, err := json.Marshal(configMap); err == nil {
		return json.RawMessage(updated)
	}
	return config
}

func pageSpecSectionNeedsRuntimeConfig(sectionKind string) bool {
	switch sectionKind {
	case "CountdownHeader", "MomentWall", "RSVPConfirmation":
		return true
	default:
		return false
	}
}

func pageSpecMaxUploadsPerGuest(cfg *models.EventConfig) int {
	if cfg != nil && cfg.MaxUploadsPerGuest > 0 {
		return cfg.MaxUploadsPerGuest
	}
	return defaultPageSpecMaxUploadsPerGuest
}

func setConfigString(config map[string]interface{}, key, value string) {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		config[key] = trimmed
	}
}

func buildPageSpecTheme(cfg *models.EventConfig) *dtos.PageSpecTheme {
	if cfg == nil {
		return nil
	}

	theme := &dtos.PageSpecTheme{}
	if cfg.DesignTemplate != nil {
		theme.DesignTemplateID = pageSpecUUIDValueString(cfg.DesignTemplate.ID)
		theme.DesignTemplateIdentifier = cfg.DesignTemplate.Identifier
	} else {
		theme.DesignTemplateID = pageSpecUUIDPointerString(cfg.DesignTemplateID)
	}

	palette := cfg.ColorPalette
	if palette == nil && cfg.DesignTemplate != nil {
		palette = cfg.DesignTemplate.ColorPalette
	}
	if palette != nil {
		theme.ColorPaletteID = pageSpecUUIDValueString(palette.ID)
		theme.ColorPaletteName = palette.Name
		theme.Colors = buildPageSpecColorMap(palette)
	} else if cfg.ColorPaletteID != nil {
		theme.ColorPaletteID = pageSpecUUIDPointerString(cfg.ColorPaletteID)
	} else if cfg.DesignTemplate != nil {
		theme.ColorPaletteID = pageSpecUUIDPointerString(cfg.DesignTemplate.ColorPaletteID)
	}

	fontSet := cfg.FontSet
	if fontSet == nil && cfg.DesignTemplate != nil {
		fontSet = cfg.DesignTemplate.FontSet
	}
	if fontSet != nil {
		theme.FontSetID = pageSpecUUIDValueString(fontSet.ID)
		theme.FontSetName = fontSet.Name
		theme.Fonts = buildPageSpecFontMap(fontSet)
		theme.FontURLs = buildPageSpecFontURLMap(fontSet)
	} else if cfg.FontSetID != nil {
		theme.FontSetID = pageSpecUUIDPointerString(cfg.FontSetID)
	} else if cfg.DesignTemplate != nil {
		theme.FontSetID = pageSpecUUIDPointerString(cfg.DesignTemplate.FontSetID)
	}

	if theme.DesignTemplateID == "" &&
		theme.DesignTemplateIdentifier == "" &&
		theme.ColorPaletteID == "" &&
		theme.FontSetID == "" &&
		len(theme.Colors) == 0 &&
		len(theme.Fonts) == 0 &&
		len(theme.FontURLs) == 0 {
		return nil
	}
	return theme
}

func pageSpecUUIDValueString(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

func pageSpecUUIDPointerString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return pageSpecUUIDValueString(*id)
}

func buildPageSpecColorMap(palette *models.ColorPalette) map[string]string {
	colors := map[string]string{}
	for _, pattern := range palette.Patterns {
		key := normalizeThemeKey(pattern.Key)
		value := strings.TrimSpace(pattern.Color.Value)
		if key != "" && value != "" {
			colors[key] = value
		}
	}
	if len(colors) == 0 {
		return nil
	}
	return colors
}

func buildPageSpecFontMap(fontSet *models.FontSet) map[string]string {
	fonts := map[string]string{}
	for _, pattern := range fontSet.Patterns {
		key := normalizeThemeKey(pattern.Key)
		value := strings.TrimSpace(pattern.Font.Name)
		if key != "" && value != "" {
			fonts[key] = value
		}
	}
	if len(fonts) == 0 {
		return nil
	}
	return fonts
}

func buildPageSpecFontURLMap(fontSet *models.FontSet) map[string]string {
	fontURLs := map[string]string{}
	for _, pattern := range fontSet.Patterns {
		key := normalizeThemeKey(pattern.Key)
		value := strings.TrimSpace(pattern.Font.Resource.Path)
		if key != "" && value != "" {
			fontURLs[key] = value
		}
	}
	if len(fontURLs) == 0 {
		return nil
	}
	return fontURLs
}

func normalizeThemeKey(key string) string {
	key = strings.TrimSpace(strings.ToLower(key))
	key = strings.ReplaceAll(key, " ", "_")
	key = strings.ReplaceAll(key, "-", "_")
	return key
}

func pageSpecSectionVisible(componentType string, cfg *models.EventConfig) bool {
	if cfg == nil {
		return true
	}

	switch pageSpecSectionKind(componentType) {
	case "CountdownHeader":
		return cfg.ShowCountdown
	case "RSVPConfirmation":
		return cfg.ShowRSVPSection
	case "EventVenue", "MAP", "LegacyMap":
		return cfg.ShowEventLocation
	case "Reception":
		return cfg.ShowSecondLocation
	case "PhotoGrid", "GALLERY", "LegacyGallery":
		return cfg.ShowPhotoGallery
	case "MomentWall":
		return cfg.ShowMomentWall || cfg.AllowUploads
	case "Agenda", "AgendaSection", "SCHEDULE", "LegacySchedule":
		return cfg.ShowEventSchedule
	case "GraduationHero", "HERO", "LegacyHero":
		return cfg.ShowHeader
	case "Contact", "ContactSection":
		return cfg.ShowContactSection
	case "Hosts", "HostSection", "HostsSection", "GraduatesList":
		return cfg.ShowHostsSection
	default:
		return true
	}
}

func pageSpecSectionKind(componentType string) string {
	normalized := normalizePageSpecSectionType(componentType)
	switch normalized {
	case "countdown", "countdownheader":
		return "CountdownHeader"
	case "rsvp", "rsvpsection", "rsvpconfirmation":
		return "RSVPConfirmation"
	case "eventvenue", "eventlocation", "venue":
		return "EventVenue"
	case "reception", "secondlocation":
		return "Reception"
	case "photogrid", "photogallery":
		return "PhotoGrid"
	case "momentwall", "momentswall":
		return "MomentWall"
	case "agenda", "agendasection", "schedule", "legacyschedule":
		return "Agenda"
	case "graduationhero", "graduationheader":
		return "GraduationHero"
	case "hero", "legacyhero":
		return "HERO"
	case "text", "legacytext":
		return "TEXT"
	case "contact", "contactsection":
		return "Contact"
	case "graduateslist":
		return "GraduatesList"
	case "hosts", "host", "hostsection", "hostssection":
		return "Hosts"
	case "gallery", "legacygallery":
		return "GALLERY"
	case "map", "legacymap":
		return "MAP"
	case "music", "legacymusic":
		return "MUSIC"
	default:
		return strings.TrimSpace(componentType)
	}
}

func normalizePageSpecSectionType(componentType string) string {
	normalized := strings.ToLower(strings.TrimSpace(componentType))
	normalized = strings.ReplaceAll(normalized, "_", "")
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, " ", "")
	return normalized
}

// getPageSpec is the testable core — it accepts deps explicitly.
func getPageSpec(deps pageSpecDeps, token string) (*dtos.PageSpec, error) {
	if deps.getToken == nil {
		return nil, ErrPageSpecTokenNotFound
	}
	now := pageSpecNow(deps.now)
	// 1. Resolve token → access token record
	accessToken, err := deps.getToken(token)
	if err != nil || accessToken == nil {
		return nil, ErrPageSpecTokenNotFound
	}
	if isExpiredPageSpecAccessToken(accessToken, now) {
		return nil, ErrPageSpecTokenExpired
	}

	// 2. Access token → invitation → event ID
	invitation, err := deps.getInvitation(accessToken.InvitationID)
	if err != nil || invitation == nil {
		return nil, fmt.Errorf("invitation not found")
	}

	// 3. Fetch event
	event, err := deps.getEvent(invitation.EventID)
	if err != nil || event == nil {
		return nil, fmt.Errorf("event not found")
	}
	if !event.IsActive {
		return nil, ErrPageSpecInactive
	}

	return buildPageSpecFromEvent(event, buildSpecDeps{
		getSections:                    deps.getSections,
		getConfig:                      deps.getConfig,
		getResourceVersion:             deps.getResourceVersion,
		getResourceVersionBySectionIDs: deps.getResourceVersionBySectionIDs,
		getGuestVersion:                deps.getGuestVersion,
		getMomentVersion:               deps.getMomentVersion,
	})
}

func validPageSpecInvitationToken(deps identifierPageSpecDeps, eventID uuid.UUID, token string) bool {
	if token == "" || deps.getToken == nil || deps.getInvitation == nil {
		return false
	}
	now := pageSpecNow(deps.now)
	accessToken, err := deps.getToken(token)
	if err != nil || accessToken == nil {
		return false
	}
	if isExpiredPageSpecAccessToken(accessToken, now) {
		return false
	}
	invitation, err := deps.getInvitation(accessToken.InvitationID)
	if err != nil || invitation == nil {
		return false
	}
	return invitation.EventID == eventID
}

func validPageSpecPreviewToken(deps identifierPageSpecDeps, eventID uuid.UUID, token string) bool {
	return deps.validatePreviewToken != nil && deps.validatePreviewToken(token, eventID)
}

func accessTokenLookup(repo ports.AccessTokenRepository) func(string) (*models.InvitationAccessToken, error) {
	if repo == nil {
		return nil
	}
	return func(token string) (*models.InvitationAccessToken, error) {
		token = strings.TrimSpace(token)
		if token == "" {
			return nil, ErrPageSpecTokenNotFound
		}
		return accesstoken.Lookup(repo, token)
	}
}

func pageSpecNow(now func() time.Time) time.Time {
	if now != nil {
		return now()
	}
	return time.Now()
}

func isExpiredPageSpecAccessToken(accessToken *models.InvitationAccessToken, now time.Time) bool {
	return accessToken != nil && accessToken.ExpiresAt != nil && now.After(*accessToken.ExpiresAt)
}

func getPageSpecByIdentifier(deps identifierPageSpecDeps, identifier, previewToken, invitationToken string) (*dtos.PageSpec, error) {
	event, err := deps.getEventByIdentifier(identifier)
	if err != nil || event == nil {
		return nil, fmt.Errorf("event not found")
	}

	cfg := loadEffectivePageSpecConfig(event.ID, deps.getConfig)
	validPreview := false
	if strings.TrimSpace(previewToken) != "" {
		validPreview = validPageSpecPreviewToken(deps, event.ID, previewToken)
	}
	if !event.IsActive && !validPreview {
		return nil, ErrPageSpecInactive
	}
	if !cfg.IsPublic {
		if !validPreview && !validPageSpecInvitationToken(deps, event.ID, invitationToken) {
			return nil, ErrPageSpecNotPublic
		}
	}

	return buildPageSpecFromEventWithConfig(event, buildSpecDeps{
		getSections:                    deps.getSections,
		getConfig:                      deps.getConfig,
		getResourceVersion:             deps.getResourceVersion,
		getResourceVersionBySectionIDs: deps.getResourceVersionBySectionIDs,
		getGuestVersion:                deps.getGuestVersion,
		getMomentVersion:               deps.getMomentVersion,
		previewAuthorized:              validPreview,
	}, cfg)
}

func (s *PageSpecService) GetPageSpecByToken(token string) (*dtos.PageSpec, error) {
	var getResourceVersion func(uuid.UUID) (*time.Time, error)
	var getResourceVersionBySectionIDs func([]uuid.UUID) (*time.Time, error)
	if s.resources != nil {
		getResourceVersion = s.resources.LatestResourceUpdatedAtByEventID
		if sectionResources, ok := s.resources.(PageSpecSectionResourceVersionRepository); ok {
			getResourceVersionBySectionIDs = sectionResources.LatestResourceUpdatedAtBySectionIDs
		}
	}
	var getGuestVersion func(uuid.UUID) (*time.Time, error)
	if s.guests != nil {
		getGuestVersion = s.guests.LatestPublicAttendeeUpdatedAtByEventID
	}
	var getMomentVersion func(uuid.UUID) (*time.Time, error)
	if s.moments != nil {
		getMomentVersion = s.moments.LatestPublicMomentUpdatedAtByEventID
	}
	return getPageSpec(pageSpecDeps{
		getToken:                       accessTokenLookup(s.accessTokens),
		getInvitation:                  s.invitations.GetInvitationByIDLite,
		getEvent:                       s.events.GetEventByIDForSpec,
		getSections:                    s.sections.ListByEventIDForSpec,
		getConfig:                      s.configs.GetEventConfigByID,
		getResourceVersion:             getResourceVersion,
		getResourceVersionBySectionIDs: getResourceVersionBySectionIDs,
		getGuestVersion:                getGuestVersion,
		getMomentVersion:               getMomentVersion,
	}, token)
}

func (s *PageSpecService) GetPageSpecByIdentifier(identifier, previewToken, invitationToken string) (*dtos.PageSpec, error) {
	var getInvitation func(id uuid.UUID) (*models.Invitation, error)
	if s.invitations != nil {
		getInvitation = s.invitations.GetInvitationByIDLite
	}
	var getResourceVersion func(uuid.UUID) (*time.Time, error)
	var getResourceVersionBySectionIDs func([]uuid.UUID) (*time.Time, error)
	if s.resources != nil {
		getResourceVersion = s.resources.LatestResourceUpdatedAtByEventID
		if sectionResources, ok := s.resources.(PageSpecSectionResourceVersionRepository); ok {
			getResourceVersionBySectionIDs = sectionResources.LatestResourceUpdatedAtBySectionIDs
		}
	}
	var getGuestVersion func(uuid.UUID) (*time.Time, error)
	if s.guests != nil {
		getGuestVersion = s.guests.LatestPublicAttendeeUpdatedAtByEventID
	}
	var getMomentVersion func(uuid.UUID) (*time.Time, error)
	if s.moments != nil {
		getMomentVersion = s.moments.LatestPublicMomentUpdatedAtByEventID
	}
	return getPageSpecByIdentifier(identifierPageSpecDeps{
		getEventByIdentifier:           s.events.GetEventByIdentifier,
		getToken:                       accessTokenLookup(s.accessTokens),
		getInvitation:                  getInvitation,
		getSections:                    s.sections.ListByEventIDForSpec,
		getConfig:                      s.configs.GetEventConfigByID,
		getResourceVersion:             getResourceVersion,
		getResourceVersionBySectionIDs: getResourceVersionBySectionIDs,
		getGuestVersion:                getGuestVersion,
		getMomentVersion:               getMomentVersion,
		validatePreviewToken:           previewtoken.Validate,
	}, identifier, previewToken, invitationToken)
}

// GetPageSpecByToken builds a SDUI PageSpec for the event associated with the given access token.
// The token can be either the raw UUID token or the human-readable pretty_token.
func GetPageSpecByToken(token string) (*dtos.PageSpec, error) {
	if _pageSpecSvc == nil {
		return nil, fmt.Errorf("page spec service not initialized")
	}
	return _pageSpecSvc.GetPageSpecByToken(token)
}

// GetPageSpecByIdentifier builds a SDUI PageSpec by event slug identifier.
// Used by the public preview route (/e/{identifier}).
func GetPageSpecByIdentifier(identifier, previewToken, invitationToken string) (*dtos.PageSpec, error) {
	if _pageSpecSvc == nil {
		return nil, fmt.Errorf("page spec service not initialized")
	}
	return _pageSpecSvc.GetPageSpecByIdentifier(identifier, previewToken, invitationToken)
}
