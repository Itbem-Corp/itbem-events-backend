package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"events-stocks/configuration"
	cacheController "events-stocks/controllers/cache"
	clientrolesController "events-stocks/controllers/clientroles"
	clientsController "events-stocks/controllers/clients"
	designtemplatesController "events-stocks/controllers/designtemplates"
	eventconfigController "events-stocks/controllers/eventconfig"
	eventmembersController "events-stocks/controllers/eventmembers"
	eventsController "events-stocks/controllers/events"
	eventsectionController "events-stocks/controllers/eventsection"
	eventtablesController "events-stocks/controllers/eventtables"
	fontsController "events-stocks/controllers/fonts"
	guestsController "events-stocks/controllers/guests"
	invitationsController "events-stocks/controllers/invitations"
	momentsController "events-stocks/controllers/moments"
	resourcesController "events-stocks/controllers/resources"
	sessionsController "events-stocks/controllers/sessions"
	usersController "events-stocks/controllers/users"
	"events-stocks/internal/authz"
	"events-stocks/internal/observability"
	"events-stocks/middleware/applicationaccess"
	customValidator "events-stocks/middleware/validator"
	"events-stocks/models"
	authproviderrepository "events-stocks/repositories/authproviderrepository"
	bucketrepository "events-stocks/repositories/bucketrepository"
	clientrepository "events-stocks/repositories/clientrepository"
	clientrolerepository "events-stocks/repositories/clientrolerepository"
	clienttyperepository "events-stocks/repositories/clienttyperepository"
	colorrepository "events-stocks/repositories/colorrepository"
	eventanalyticsrepository "events-stocks/repositories/eventanalyticsrepository"
	eventconfigrepository "events-stocks/repositories/eventconfigrepository"
	eventmemberrepository "events-stocks/repositories/eventmemberrepository"
	eventsectionrepository "events-stocks/repositories/eventsectionrepository"
	eventsrepository "events-stocks/repositories/eventsrepository"
	eventtablerepository "events-stocks/repositories/eventtablerepository"
	eventtyperepository "events-stocks/repositories/eventtyperepository"
	fontrepository "events-stocks/repositories/fontrepository"
	gormrepository "events-stocks/repositories/gormrepository"
	guestrepository "events-stocks/repositories/guestrepository"
	gueststatusrepository "events-stocks/repositories/gueststatusrepository"
	invitationaccesstokenrepository "events-stocks/repositories/invitationaccesstokenrepository"
	invitationlogrepository "events-stocks/repositories/invitationlogrepository"
	invitationrepository "events-stocks/repositories/invitationrepository"
	jobqueuerepository "events-stocks/repositories/jobqueuerepository"
	momentrepository "events-stocks/repositories/momentrepository"
	momenttyperepository "events-stocks/repositories/momenttyperepository"
	redisrepository "events-stocks/repositories/redisrepository"
	resourcerepository "events-stocks/repositories/resourcerepository"
	sqsrepository "events-stocks/repositories/sqsrepository"
	templatesrepository "events-stocks/repositories/templatesrepository"
	userrepository "events-stocks/repositories/userrepository"
	"events-stocks/routes"
	applicationsService "events-stocks/services/applications"
	clientrolesService "events-stocks/services/clientroles"
	clientsService "events-stocks/services/clients"
	clienttypesService "events-stocks/services/clienttypes"
	colorsService "events-stocks/services/colors"
	eventsService "events-stocks/services/events"
	fontsService "events-stocks/services/fonts"
	guestsService "events-stocks/services/guests"
	invitationsService "events-stocks/services/invitations"
	momentsService "events-stocks/services/moments"
	outboxService "events-stocks/services/outbox"
	productmetricsService "events-stocks/services/productmetrics"
	resourcesService "events-stocks/services/resources"
	templatesService "events-stocks/services/templates"
	usersService "events-stocks/services/users"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// Run boots the API, wires concrete dependencies, and handles graceful shutdown.
func Run() error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})).With(
		"service", "eventiapp-backend",
		"environment", strings.TrimSpace(os.Getenv("ENV")),
		"source_revision", strings.TrimSpace(os.Getenv("SOURCE_REVISION")),
	))

	cfg := configuration.LoadConfig()
	if err := configuration.ValidateSecurityConfiguration(); err != nil {
		return fmt.Errorf("security configuration: %w", err)
	}
	configuration.InicializarRedis(cfg)
	configuration.InicializarPostgreSQL(cfg)
	configuration.MigrarModelos()
	// Performance-only indexes are built asynchronously and fail open. Their
	// absence may slow a fallback plan but must not delay API readiness.
	go configuration.MigratePerformanceIndexes()
	configuration.SeedBaseData()
	// SeedBaseData publishes versioned, stable design templates and palettes.
	// Valkey survives API restarts, so an empty catalog cached before the seed
	// would otherwise hide newly published templates until the TTL expires.
	for _, resource := range []string{utils.RedisTemplatesKey, utils.RedisColorPalettesKey} {
		if err := redisrepository.Invalidate(resource, "all"); err != nil {
			return fmt.Errorf("invalidate seeded design catalog cache %s: %w", resource, err)
		}
	}
	configuration.InitAwsServices(cfg)
	sqsrepository.Init(cfg.AwsRegion, cfg.S3ClientId, cfg.S3ClientSecret, cfg.SQSImageQueueURL, cfg.SQSVideoQueueURL)
	jobqueuerepository.Init(cfg.AwsRegion, cfg.S3ClientId, cfg.S3ClientSecret, cfg.SQSWorkerQueueURL, cfg.SNSWorkerTopicARN)
	dispatcherCtx, stopDispatcher := context.WithCancel(context.Background())
	defer stopDispatcher()
	outboxService.StartDispatcher(dispatcherCtx, configuration.DB)
	metricsCollector := productmetricsService.NewCollector(configuration.DB)
	productmetricsService.Configure(metricsCollector)
	metricsCollector.Start(dispatcherCtx)

	e := newServer(cfg)
	wireDependencies(cfg)
	routes.ConfigurarRutas(e, cfg)

	port := cfg.Port
	if port == "" {
		port = "8080"
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server started", "port", port)
		if err := e.Start(":" + port); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-quit:
		slog.Info("shutdown signal received", "signal", sig.String())
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("server start: %w", err)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}
	metricsCollector.Flush(ctx)
	slog.Info("server stopped gracefully")
	return nil
}

func newServer(cfg *models.Config) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = handleHTTPError
	e.IPExtractor = trustedProxyIPExtractor(cfg.TrustedProxyCidrs)

	e.Use(middleware.RequestIDWithConfig(middleware.RequestIDConfig{
		Generator: func() string {
			return uuid.Must(uuid.NewV4()).String()
		},
	}))

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			requestID := c.Response().Header().Get(echo.HeaderXRequestID)
			correlationID := observability.NormalizeCorrelationID(c.Request().Header.Get("X-Correlation-ID"))
			if correlationID == "" {
				correlationID = requestID
			}
			request := c.Request()
			c.SetRequest(request.WithContext(observability.WithCorrelationID(request.Context(), correlationID)))
			start := time.Now()
			err := next(c)

			status := c.Response().Status
			if err != nil {
				if he, ok := err.(*echo.HTTPError); ok {
					status = he.Code
				} else {
					status = http.StatusInternalServerError
				}
			}

			if c.Path() == "/health" && status < http.StatusBadRequest {
				return err
			}
			level := slog.LevelInfo
			if status >= http.StatusInternalServerError {
				level = slog.LevelError
			} else if status >= http.StatusBadRequest {
				level = slog.LevelWarn
			}
			slog.Log(c.Request().Context(), level, "http request completed",
				"event", "http_request_completed",
				"component", "http",
				"request_id", requestID,
				"correlation_id", correlationID,
				"method", c.Request().Method,
				"route", redactedRequestURI(c),
				"status", status,
				"latency_ms", time.Since(start).Milliseconds(),
			)
			return err
		}
	})

	e.Use(middleware.Recover())
	e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		XSSProtection:         "1; mode=block",
		ContentTypeNosniff:    "nosniff",
		XFrameOptions:         "SAMEORIGIN",
		HSTSMaxAge:            31536000,
		HSTSExcludeSubdomains: false,
		HSTSPreloadEnabled:    true,
		ContentSecurityPolicy: "default-src 'self'; frame-ancestors 'self' https://dashboard.eventiapp.com.mx",
	}))
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Response().Header()
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
			h.Set("X-Permitted-Cross-Domain-Policies", "none")
			return next(c)
		}
	})
	e.Use(configuration.GetCORSConfig(cfg))
	e.Use(middleware.ContextTimeoutWithConfig(middleware.ContextTimeoutConfig{
		Timeout: 5 * time.Minute,
	}))
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		Level:     5,
		MinLength: 1024,
	}))
	e.Validator = customValidator.New()

	return e
}

func trustedProxyIPExtractor(raw string) echo.IPExtractor {
	options := []echo.TrustOption{
		echo.TrustLoopback(true),
		echo.TrustLinkLocal(false),
		echo.TrustPrivateNet(false),
	}
	for _, candidate := range strings.Split(raw, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		_, network, err := net.ParseCIDR(candidate)
		if err != nil {
			// Deployed environments reject malformed CIDRs during the security
			// preflight. Ignoring one here keeps local startup fail-safe.
			continue
		}
		options = append(options, echo.TrustIPRange(network))
	}
	return echo.ExtractIPFromXFFHeader(options...)
}

func handleHTTPError(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	status := http.StatusInternalServerError
	message := http.StatusText(status)

	var he *echo.HTTPError
	if errors.As(err, &he) {
		status = he.Code
		message = httpErrorMessage(he)
	}
	if message == "" {
		message = http.StatusText(status)
	}

	if writeErr := utils.Error(c, status, message, ""); writeErr != nil {
		c.Logger().Error(writeErr)
	}
}

func httpErrorMessage(he *echo.HTTPError) string {
	switch msg := he.Message.(type) {
	case nil:
		return http.StatusText(he.Code)
	case string:
		return msg
	case map[string]string:
		return msg["message"]
	case map[string]interface{}:
		if message, ok := msg["message"].(string); ok {
			return message
		}
	}
	message := fmt.Sprint(he.Message)
	if message == "<nil>" {
		return http.StatusText(he.Code)
	}
	return message
}

func wireDependencies(cfg *models.Config) {
	redisRepo := redisrepository.NewRedisRepo()
	transactor := gormrepository.NewGormTransactor()

	eventsRepo := eventsrepository.NewEventsRepo()
	eventAnalyticsRepo := eventanalyticsrepository.NewEventAnalyticsRepo()
	eventConfigRepo := eventconfigrepository.NewEventConfigRepo()
	eventMemberRepo := eventmemberrepository.NewEventMemberRepo(gormrepository.DB())
	eventSectionRepo := eventsectionrepository.NewEventSectionRepo()
	eventTableRepo := eventtablerepository.NewEventTableRepo()
	guestRepo := guestrepository.NewGuestRepo()
	invitationRepo := invitationrepository.NewInvitationRepo()
	accessTokenRepo := invitationaccesstokenrepository.NewAccessTokenRepo()
	invLogRepo := invitationlogrepository.NewInvitationLogRepo()
	momentRepo := momentrepository.NewMomentRepo()
	userRepo := userrepository.NewUserRepo()
	clientRepo := clientrepository.NewClientRepo()
	clientRoleRepo := clientrolerepository.NewClientRoleRepo()
	clientTypeRepo := clienttyperepository.NewClientTypeRepo()
	colorRepo := colorrepository.NewColorRepo()
	eventTypeRepo := eventtyperepository.NewEventTypeRepo()
	fontRepo := fontrepository.NewFontRepo()
	guestStatusRepo := gueststatusrepository.NewGuestStatusRepo()
	momentTypeRepo := momenttyperepository.NewMomentTypeRepo()
	templateRepo := templatesrepository.NewDesignTemplateRepo()
	authProviderRepo := authproviderrepository.NewAuthProviderRepo()
	mediaPublisher := sqsrepository.NewPublisher()
	resourceRepo := resourcerepository.NewResourceRepo()
	bucketRepo := bucketrepository.NewBucketRepo()

	resourceSvc := resourcesService.NewResourceService(cfg, resourcesService.ResourceServiceDeps{
		Repo:    resourceRepo,
		Cache:   redisRepo,
		Storage: bucketRepo,
	})
	eventSvc := eventsService.NewEventService(eventsRepo, redisRepo)
	eventAnalyticsSvc := eventsService.NewEventAnalyticsService(eventAnalyticsRepo, redisRepo)
	eventConfigSvc := eventsService.NewEventConfigService(eventConfigRepo, redisRepo)
	eventSectSvc := eventsService.NewEventSectionService(eventSectionRepo, redisRepo)
	eventTableSvc := eventsService.NewEventTableService(eventTableRepo, redisRepo)
	pageSpecSvc := eventsService.NewPageSpecService(accessTokenRepo, invitationRepo, eventsRepo, eventSectionRepo, eventConfigRepo, resourceRepo).
		WithGuestVersionRepository(guestRepo).
		WithMomentVersionRepository(momentRepo)
	duplicateSvc := eventsService.NewDuplicateService(gormrepository.DB(), eventsService.DuplicateServiceDeps{
		Cache:      redisRepo,
		Storage:    bucketRepo,
		Bucket:     resourceSvc.Bucket,
		Provider:   resourceSvc.Provider,
		UploadPath: resourceSvc.UploadPath,
	})
	repairSvc := eventsService.NewRepairService(gormrepository.DB(), eventsRepo)
	guestSvc := guestsService.NewGuestService(guestRepo, accessTokenRepo, redisRepo, transactor, invitationRepo)
	invitationSvc := invitationsService.NewInvitationServiceWithDeps(invitationsService.InvitationServiceDeps{
		Repo:       invitationRepo,
		GuestRepo:  guestRepo,
		TokenRepo:  accessTokenRepo,
		LogRepo:    invLogRepo,
		Cache:      redisRepo,
		EventsRepo: eventsRepo,
		ConfigRepo: eventConfigRepo,
		CoverViewURL: func(path, bucket string) (string, *time.Time) {
			trimmed := strings.TrimSpace(path)
			if trimmed == "" || strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
				return path, nil
			}
			viewURL, err := resourceSvc.WithBucket(bucket).GetPresignedURLWithTTL(trimmed, resourcesService.ResourceViewURLTTLMinutes)
			if err != nil || strings.TrimSpace(viewURL) == "" {
				return path, nil
			}
			expiresAt := time.Now().UTC().Add(time.Duration(resourcesService.ResourceViewURLTTLMinutes) * time.Minute)
			return viewURL, &expiresAt
		},
	})
	momentSvc := momentsService.NewMomentService(momentRepo, redisRepo, mediaPublisher)
	userSvc := usersService.NewUserService(userRepo, authProviderRepo, cfg, resourceSvc)
	applicationSessionSvc := applicationsService.NewSessionService(
		gormrepository.DB(),
		userSvc.SyncUser,
		func(user *models.User) string {
			if user == nil || strings.TrimSpace(user.ProfileImage) == "" {
				return ""
			}
			url, _ := resourceSvc.WithBucket(user.ProfileImageBucket).GetAvatarPresignedURL(user.ProfileImage)
			return url
		},
	)
	applicationaccess.Configure(applicationSessionSvc)
	sessionsController.Configure(applicationSessionSvc)
	adminSvc := usersService.NewAdminUserService(userRepo, clientRepo, authProviderRepo)
	clientSvc := clientsService.NewClientService(clientRepo, clientRoleRepo, clientTypeRepo, resourceSvc, redisRepo, transactor)
	clientRoleSvc := clientrolesService.NewClientRoleService(clientRoleRepo)
	clientTypeSvc := clienttypesService.NewClientTypeService(clientTypeRepo)
	colorSvc := colorsService.NewColorService(colorRepo, redisRepo)
	eventTypeSvc := eventsService.NewEventTypeService(eventTypeRepo, redisRepo)
	fontSvc := fontsService.NewFontService(resourceSvc, fontsService.FontServiceDeps{Repo: fontRepo, Cache: redisRepo})
	guestStatusSvc := guestsService.NewGuestStatusService(guestStatusRepo, redisRepo)
	momentTypeSvc := momentsService.NewMomentTypeService(momentTypeRepo, redisRepo)
	templateSvc := templatesService.NewDesignTemplateService(templateRepo, redisRepo)

	authz.Configure(authz.Hooks{
		SyncUser:              userSvc.SyncUser,
		CheckAccessRecursive:  clientRepo.CheckAccessRecursive,
		GetClientByID:         clientRepo.GetClientByID,
		GetEventByIDRaw:       eventsRepo.GetEventByIDRaw,
		GetEventSectionByID:   eventSectionRepo.GetEventSectionByID,
		GetMomentByID:         momentRepo.GetMomentByID,
		GetGuestByID:          guestRepo.GetGuestByID,
		GetInvitationByIDLite: invitationRepo.GetInvitationByIDLite,
		GetResourceByID:       resourceRepo.GetResourceByID,
		GetEventMemberRole:    eventMemberRepo.RoleForUser,
	})

	resourcesController.InitResourceController(resourceSvc, resourcesController.PublicResourceAccessDeps{
		SectionRepo:    eventSectionRepo,
		ConfigRepo:     eventConfigRepo,
		TokenRepo:      accessTokenRepo,
		InvitationRepo: invitationRepo,
		EventRepo:      eventsRepo,
	})
	cacheController.InitCacheController(redisRepo)
	fontsController.InitFontsController(fontSvc)
	designtemplatesController.InitDesignTemplatesController(resourceSvc)
	eventsController.InitEventsController(eventSvc, eventConfigSvc, accessTokenRepo, invitationRepo, guestSvc)
	eventsController.InitCoverController(resourceSvc)
	eventsController.InitDuplicateController(duplicateSvc)
	eventsController.InitRepairController(repairSvc)
	eventmembersController.InitEventMembersController(eventMemberRepo, clientSvc)
	eventconfigController.InitEventConfigController(eventConfigSvc, resourceSvc)
	eventsectionController.InitEventSectionController(eventSectSvc)
	eventtablesController.InitEventTablesController(eventTableSvc, guestSvc)
	guestsController.InitGuestsController(guestSvc, eventSectSvc, guestsController.PublicGuestAccessDeps{
		ConfigRepo:     eventConfigRepo,
		TokenRepo:      accessTokenRepo,
		InvitationRepo: invitationRepo,
		EventRepo:      eventsRepo,
		ResourceSvc:    resourceSvc,
	})
	invitationsController.InitInvitationsController(invitationSvc)
	momentsController.InitMomentsController(momentSvc, resourceSvc)
	momentsController.InitPublicMomentsController(accessTokenRepo, eventsRepo, eventConfigRepo, invitationRepo, redisRepo, resourceSvc)
	usersController.InitUsersController(userSvc, adminSvc, resourceSvc)
	clientsController.InitClientsController(clientSvc)
	clientrolesController.InitClientRolesController(clientSvc, clientRoleSvc)

	resourcesService.SetDefaultResourceService(resourceSvc)
	eventsService.SetDefaultEventService(eventSvc)
	eventsService.SetDefaultEventAnalyticsService(eventAnalyticsSvc)
	eventsService.SetDefaultEventConfigService(eventConfigSvc)
	eventsService.SetDefaultEventSectionService(eventSectSvc)
	eventsService.SetDefaultPageSpecService(pageSpecSvc)
	eventsService.SetDefaultDuplicateService(duplicateSvc)
	eventsService.SetDefaultRepairService(repairSvc)
	guestsService.SetDefaultGuestService(guestSvc)
	invitationsService.SetDefaultInvitationService(invitationSvc)
	momentsService.SetDefaultMomentService(momentSvc)
	usersService.SetDefaultUserService(userSvc)
	clientsService.SetDefaultClientService(clientSvc)
	clientrolesService.SetDefaultClientRoleService(clientRoleSvc)
	clienttypesService.SetDefaultClientTypeService(clientTypeSvc)
	colorsService.SetDefaultColorService(colorSvc)
	eventsService.SetDefaultEventTypeService(eventTypeSvc)
	fontsService.SetDefaultFontService(fontSvc)
	guestsService.SetDefaultGuestStatusService(guestStatusSvc)
	momentsService.SetDefaultMomentTypeService(momentTypeSvc)
	templatesService.SetDefaultDesignTemplateService(templateSvc)
}
